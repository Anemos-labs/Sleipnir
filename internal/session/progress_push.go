package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Progress pushing (swarm.push_progress, default on).
//
// An isolated team's verified work lives on the local integration branch until the
// run ends and the result is applied to the checkout. A long run can merge dozens of
// tasks before that, and a machine that dies in the meantime takes all of it along: a
// real run had 60 tasks done and nothing anywhere but one local disk. So after every
// merge the session pushes the integration tip to the checkout's push remote, on a
// branch of the harness's own namespace (the integration branch's name,
// sleipnir/<session>/_integration). It never touches the person's branches, and it
// never blocks the merge queue: one background push at a time, coalesced to the newest
// tip, with a failure reported once and retried after the next merge.
//
// swarm.push_to (or --push-to) names another target: a remote or repository URL, a
// branch, and a folder of that repository. A project built at the root of its checkout
// can so land in a folder of a monorepo: each integration commit is rewritten, in
// order, onto the target branch with the project's tree in place of the folder's (a
// subtree-style commit, the original's author, date and message, and a
// Sleipnir-Source trailer naming it), and the branch is pushed without force. The
// trailer tells the next push, of this process or a resumed one, where to go on from.

// TypeProgressPush is the log event of one push of the integration tip.
const TypeProgressPush = "swarm.progress_push"

// progressPushTimeout bounds one git push.
const progressPushTimeout = 2 * time.Minute

// gitRunner runs git with args in the repository and returns its combined output.
type gitRunner func(ctx context.Context, args ...string) ([]byte, error)

// commandGit runs the git on PATH in dir, with the person's own configuration (the
// push needs their credentials, which internal/gitx deliberately never consults),
// never waiting for a password prompt.
func commandGit(dir string) gitRunner { return commandGitEnv(dir) }

// commandGitEnv is commandGit with env added to the environment (later entries win).
func commandGitEnv(dir string, env ...string) gitRunner {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never"), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(clipText(string(out), 400)))
		}
		return out, nil
	}
}

// clipText bounds s to n bytes.
func clipText(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// pushRemote is the remote the checkout's branch pushes to: its configured push
// remote (not ".", the repository itself), else "origin" when that exists; "" when
// there is none.
func pushRemote(ctx context.Context, git gitRunner, branch string) string {
	if branch != "" {
		if out, err := git(ctx, "for-each-ref", "--format=%(push:remotename)", "refs/heads/"+branch); err == nil {
			if r := strings.TrimSpace(string(out)); r != "" && r != "." && !strings.ContainsAny(r, " \n/") {
				return r
			}
		}
	}
	if _, err := git(ctx, "remote", "get-url", "--push", "origin"); err == nil {
		return "origin"
	}
	return ""
}

// progressPusher pushes the newest integration tip to remote's ref, one push at a time.
type progressPusher struct {
	git    gitRunner
	remote string // a remote's name or a repository URL
	ref    string // the remote branch, without refs/heads/
	force  bool   // the branch is the harness's own: a rebuilt integration branch replaces it
	// dir, when set, is the folder of the target repository the project lands in: the
	// integration commits are rewritten onto ref with their tree there (rewrite).
	dir string
	// gitEnv runs git in the checkout with env added (rewrite's private index); nil is
	// commandGitEnv of root.
	gitEnv func(env ...string) gitRunner
	root   string // the checkout, for gitEnv and the private index's folder
	log    events.Emitter
	tell   func(level, msg string)

	mu      sync.Mutex
	base    string // the commit the integration branch started from (SetBase)
	want    string // the newest tip asked for
	pushed  string // the last tip the remote acknowledged
	running bool
	failing bool // the last push failed and the person was told
	told    bool // the person was told where the work is pushed
	idle    chan struct{}
}

// SetBase records the commit the integration branch started from: with a dir, the
// first push rewrites the integration commits after it.
func (p *progressPusher) SetBase(base string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.base = base
	p.mu.Unlock()
}

// where names the target in a notice.
func (p *progressPusher) where() string {
	w := p.remote + " branch " + p.ref
	if p.dir != "" {
		w += " in the folder " + p.dir + "/"
	}
	return w
}

// Kick asks for tip to be pushed. It returns at once.
func (p *progressPusher) Kick(tip string) {
	if p == nil || tip == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.want = tip
	if p.running || p.want == p.pushed {
		return
	}
	p.running = true
	p.idle = make(chan struct{})
	go p.loop(p.idle)
}

// loop pushes until the remote has the newest tip, or a push fails (the next Kick
// tries again: retrying at once would only fail the same way).
func (p *progressPusher) loop(idle chan struct{}) {
	defer close(idle)
	for {
		p.mu.Lock()
		tip := p.want
		if tip == p.pushed {
			p.running = false
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		err := p.push(tip)
		p.mu.Lock()
		if err != nil {
			p.running = false
			first := !p.failing
			p.failing = true
			p.mu.Unlock()
			if first && p.tell != nil {
				p.tell("warn", fmt.Sprintf("Could not push the team's verified work to %s: %s. It is safe on the local integration branch; the push is retried after the next merge.",
					p.where(), clipText(err.Error(), 300)))
			}
			return
		}
		p.pushed = tip
		recovered, first := p.failing, !p.told
		p.failing, p.told = false, true
		p.mu.Unlock()
		if (first || recovered) && p.tell != nil {
			p.tell("delivery", fmt.Sprintf("Verified work is pushed to %s after every merge (now at %s), so a lost machine loses none of it.",
				p.where(), shortSHA(tip)))
		}
	}
}

// push runs one git push of tip. The harness's own branch is forced: a resumed
// session's rebuilt integration branch must still reach it. A branch the person named
// only moves forward, and with a dir it is first rewritten on top of the remote's.
func (p *progressPusher) push(tip string) error {
	ctx, cancel := context.WithTimeout(context.Background(), progressPushTimeout)
	defer cancel()
	head, err := tip, error(nil)
	if p.dir != "" {
		head, err = p.rewrite(ctx, tip)
	}
	if err == nil && head != "" {
		spec := head + ":refs/heads/" + p.ref
		if p.force {
			spec = "+" + spec
		}
		_, err = p.git(ctx, "push", "--no-verify", "--quiet", p.remote, spec)
	}
	if p.log != nil {
		data := map[string]any{"remote": p.remote, "branch": p.ref, "tip": tip, "ok": err == nil}
		if p.dir != "" {
			data["dir"] = p.dir
			data["head"] = head
		}
		if err != nil {
			data["error"] = clipText(err.Error(), 300)
		}
		_, _ = p.log.Emit("swarm", TypeProgressPush, data)
	}
	return err
}

// sourceTrailer names, in a rewritten commit, the integration commit it was made from.
const sourceTrailer = "Sleipnir-Source"

// rewrite makes the commits that put the integration branch up to tip into p.dir of
// the target branch, and returns the new head of that branch ("" when the branch
// already holds tip's tree). It fetches the branch (or, when the branch does not exist
// yet, the repository's default branch, so that the work can become a pull request
// against it) into refs/sleipnir/push/<branch> of the checkout, finds the last
// integration commit the branch carries from its Sleipnir-Source trailer (the
// integration base when none of tip's history is there), and rewrites each later
// first-parent commit onto it. A commit that changes nothing in the folder is skipped,
// so a push that is repeated, or a resumed session's rebuilt branch, adds no commits.
func (p *progressPusher) rewrite(ctx context.Context, tip string) (string, error) {
	out, err := p.git(ctx, "ls-remote", "--symref", p.remote, "HEAD", "refs/heads/"+p.ref)
	if err != nil {
		return "", err
	}
	from, exists := "", false
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 3 && f[0] == "ref:" && f[2] == "HEAD" && from == "":
			from = f[1]
		case len(f) == 2 && f[1] == "refs/heads/"+p.ref:
			from, exists = f[1], true
		}
	}
	parent, parentTree := "", ""
	if from != "" {
		local := "refs/sleipnir/push/" + p.ref
		if _, err := p.git(ctx, "fetch", "--no-tags", "--quiet", "--no-write-fetch-head", p.remote, "+"+from+":"+local); err != nil {
			return "", err
		}
		if parent, err = p.revParse(ctx, local+"^{commit}"); err != nil {
			return "", err
		}
		if parentTree, err = p.revParse(ctx, parent+"^{tree}"); err != nil {
			return "", err
		}
	}
	p.mu.Lock()
	start := p.base
	p.mu.Unlock()
	if exists {
		out, err := p.git(ctx, "log", "--first-parent", "-n1", "--grep=^"+sourceTrailer+": ", "--format=%(trailers:key="+sourceTrailer+",valueonly,separator=%x20)", parent)
		if err != nil {
			return "", err
		}
		if f := strings.Fields(string(out)); len(f) > 0 {
			if _, err := p.git(ctx, "merge-base", "--is-ancestor", f[len(f)-1], tip); err == nil {
				start = f[len(f)-1]
			}
		}
	}
	revs := []string{tip}
	if start != "" {
		out, err := p.git(ctx, "rev-list", "--first-parent", "--reverse", start+".."+tip)
		if err != nil {
			return "", err
		}
		revs = strings.Fields(string(out))
	}
	tmp, err := os.MkdirTemp("", "sleipnir-push-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	gitEnv := p.gitEnv
	if gitEnv == nil {
		gitEnv = func(env ...string) gitRunner { return commandGitEnv(p.root, env...) }
	}
	index := gitEnv("GIT_INDEX_FILE=" + filepath.Join(tmp, "index"))
	head := parent
	for _, c := range revs {
		tree, err := p.graft(ctx, index, parentTree, c)
		if err != nil {
			return "", err
		}
		if tree == parentTree {
			continue
		}
		if head, err = p.commit(ctx, gitEnv, c, tree, head); err != nil {
			return "", err
		}
		parentTree = tree
	}
	if head == parent {
		return "", nil
	}
	return head, nil
}

// graft is the tree of parentTree ("" for none) with p.dir replaced by commit c's tree,
// built in a private index.
func (p *progressPusher) graft(ctx context.Context, index gitRunner, parentTree, c string) (string, error) {
	steps := [][]string{{"read-tree", "--empty"}}
	if parentTree != "" {
		steps = [][]string{{"read-tree", parentTree}, {"rm", "--cached", "-r", "-f", "-q", "--ignore-unmatch", "--", ":(literal)" + p.dir}}
	}
	steps = append(steps, []string{"read-tree", "--prefix=" + p.dir + "/", c + "^{tree}"})
	for _, args := range steps {
		if _, err := index(ctx, args...); err != nil {
			return "", err
		}
	}
	out, err := index(ctx, "write-tree")
	return strings.TrimSpace(string(out)), err
}

// commit makes the rewritten commit of c: tree on parent ("" for none), with c's
// author, committer, dates and message and a trailer naming c. The same c on the same
// parent gives the same commit.
func (p *progressPusher) commit(ctx context.Context, gitEnv func(env ...string) gitRunner, c, tree, parent string) (string, error) {
	out, err := p.git(ctx, "log", "-1", "--date=raw", "--format=%an%x00%ae%x00%ad%x00%cn%x00%ce%x00%cd%x00%B", c)
	if err != nil {
		return "", err
	}
	f := strings.SplitN(string(out), "\x00", 7)
	if len(f) != 7 {
		return "", fmt.Errorf("reading commit %s: unexpected format", shortSHA(c))
	}
	env := []string{"GIT_AUTHOR_NAME=" + f[0], "GIT_AUTHOR_EMAIL=" + f[1], "GIT_AUTHOR_DATE=" + f[2],
		"GIT_COMMITTER_NAME=" + f[3], "GIT_COMMITTER_EMAIL=" + f[4], "GIT_COMMITTER_DATE=" + f[5]}
	msg := strings.TrimRight(f[6], "\n")
	if msg == "" {
		msg = "sleipnir: integrate " + shortSHA(c)
	}
	args := []string{"commit-tree", "--no-gpg-sign"}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	args = append(args, "-m", msg+"\n\n"+sourceTrailer+": "+c, tree)
	out, err = gitEnv(env...)(ctx, args...)
	return strings.TrimSpace(string(out)), err
}

// revParse is the object rev names.
func (p *progressPusher) revParse(ctx context.Context, rev string) (string, error) {
	out, err := p.git(ctx, "rev-parse", "--verify", "--quiet", rev)
	return strings.TrimSpace(string(out)), err
}

// Wait returns when no push is running, or when ctx ends. An early return says
// nothing about the push in flight: it runs on to its own timeout.
func (p *progressPusher) Wait(ctx context.Context) {
	if p == nil {
		return
	}
	p.mu.Lock()
	idle := p.idle
	p.mu.Unlock()
	if idle == nil {
		return
	}
	select {
	case <-idle:
	case <-ctx.Done():
	}
}

// shortSHA is the first 12 characters of a commit id.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
