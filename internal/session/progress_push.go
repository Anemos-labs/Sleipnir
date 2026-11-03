package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// Progress pushing (swarm.push_progress, default on).
//
// An isolated team's verified work lives on the local integration branch until the
// run ends and the result is applied to the checkout. A long run can merge dozens of
// tasks before that, and a machine that dies in the meantime takes all of it along: a
// real run had 60 tasks done and nothing anywhere but one local disk. So after every
// merge the session pushes the integration tip to the checkout's push remote, on a
// branch of the harness's own namespace named after the work (progressBranch:
// sleipnir/<what the goal or first task says>-<session suffix>, which people browsing
// the remote can read). It never touches the person's branches, and it
// never blocks the merge queue: one background push at a time, coalesced to the newest
// tip, with a failure reported once and retried after the next merge.

// TypeProgressPush is the log event of one push of the integration tip.
const TypeProgressPush = "swarm.progress_push"

// progressPushTimeout bounds one git push.
const progressPushTimeout = 2 * time.Minute

// gitRunner runs git with args in the repository and returns its combined output.
type gitRunner func(ctx context.Context, args ...string) ([]byte, error)

// commandGit runs the git on PATH in dir, with the person's own configuration (the
// push needs their credentials, which internal/gitx deliberately never consults),
// never waiting for a password prompt.
func commandGit(dir string) gitRunner {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		// The person's credentials, but none of the programs a repository's own configuration can add to a push: no hooks (a
		// pre-push hook), no fsmonitor, no ext:: transport. A push of the harness's branch needs none of them.
		safe := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "protocol.ext.allow=never"}
		own, err := personalPrograms(ctx, dir)
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", args[0], err)
		}
		safe = append(safe, own...)
		cmd := exec.CommandContext(ctx, "git", append(safe, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(clipText(string(out), 400)))
		}
		return out, nil
	}
}

// programKeys are the settings that name a program git runs for a push or a fetch: the ssh command, the password prompt, the
// credential helpers (of every URL), a git:// proxy, and the programs a local remote is reached through.
const programKeys = `^(core\.(sshcommand|askpass|gitproxy)|credential\..*helper|remote\..*\.(receivepack|uploadpack))$`

// personalPrograms is the -c configuration that keeps the programs a push runs to the person's own: a setting of programKeys
// that the repository's configuration makes (.git/config, its worktree file, or a file they include, which commands in the
// sandbox can write) is put back to what the person's global and system configuration say, or to git's default. It is read
// right before each push, so an include created after the session started counts too.
func personalPrograms(ctx context.Context, dir string) ([]string, error) {
	read := func(scope string) [][2]string {
		// -z: a key and its value end with a newline and a NUL, so a remote name with a blank in it is read whole
		cmd := exec.CommandContext(ctx, "git", "config", scope, "--includes", "--null", "--get-regexp", programKeys)
		cmd.Dir = dir
		out, _ := cmd.Output() // exit 1: no such setting; anything else leaves the defaults below
		var kv [][2]string
		for _, rec := range strings.Split(string(out), "\x00") {
			if k, v, _ := strings.Cut(rec, "\n"); k != "" {
				kv = append(kv, [2]string{k, v})
			}
		}
		return kv
	}
	local := append(read("--local"), read("--worktree")...)
	if len(local) == 0 {
		return nil, nil
	}
	for _, l := range local {
		if strings.Contains(l[0], "=") {
			// -c takes the key up to the first "=": remote."a=b".receivepack could not be put back, and keeps its program
			return nil, fmt.Errorf("the repository's configuration sets %s, a name the push cannot override; rename the remote", l[0])
		}
	}
	var own [][2]string
	if os.Getenv("GIT_CONFIG_NOSYSTEM") == "" {
		own = read("--system")
	}
	own = append(own, read("--global")...)
	var out []string
	done := map[string]bool{}
	helpers := false
	for _, l := range local {
		k := l[0]
		if done[k] {
			continue
		}
		done[k] = true
		var vals []string
		for _, o := range own {
			if o[0] == k {
				vals = append(vals, o[1])
			}
		}
		switch {
		case credentialHelper(k):
			// an empty helper clears the whole list, whatever URL each entry was set for: one clear, then every helper of the
			// person's own, generic and per-URL, in their order
			if !helpers {
				helpers = true
				out = append(out, "-c", "credential.helper=")
				for _, o := range own {
					if credentialHelper(o[0]) {
						out = append(out, "-c", o[0]+"="+o[1])
					}
				}
			}
			continue
		case strings.HasPrefix(k, "credential."):
			v := ""
			if len(vals) > 0 {
				v = vals[len(vals)-1]
			}
			out = append(out, "-c", k+"="+v)
			continue
		case len(vals) > 0:
			out = append(out, "-c", k+"="+vals[len(vals)-1])
			continue
		}
		switch {
		case k == "core.sshcommand":
			ssh := "ssh"
			if p := os.Getenv("GIT_SSH"); p != "" {
				ssh = "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
			}
			out = append(out, "-c", k+"="+ssh)
		case k == "core.askpass":
			out = append(out, "-c", k+"=")
		case k == "core.gitproxy":
			out = append(out, "-c", k+"=none")
		case strings.HasSuffix(k, ".receivepack"):
			out = append(out, "-c", k+"=git-receive-pack")
		case strings.HasSuffix(k, ".uploadpack"):
			out = append(out, "-c", k+"=git-upload-pack")
		}
	}
	return out, nil
}

// clipText bounds s to n bytes.
func clipText(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// snapshotSecrets is the hold of a progress push whose history starts from a snapshot of the person's working tree (workspace.Manager
// Snapshot): a file the snapshot added that the person never committed and whose name says it holds credentials (.env, a private key,
// .npmrc; mdfile.SensitivePath) would be published with the first push, on the strength of the repository's own ignore rules alone.
// base is the commit the team started from. It returns why it holds, or "" when base is no snapshot or adds no such file. A check
// that cannot be made holds too: publishing is the step that cannot be taken back.
func snapshotSecrets(ctx context.Context, git gitRunner, base string) string {
	out, err := git(ctx, "log", "-1", "--format=%P%x00%s", base)
	if err != nil {
		return "could not check what the starting snapshot of your working tree holds (" + clipText(err.Error(), 200) + ")"
	}
	parents, subject, _ := strings.Cut(strings.TrimSpace(string(out)), "\x00")
	if subject != workspace.SnapshotMessage || strings.Contains(parents, " ") || parents == "" {
		return ""
	}
	out, err = git(ctx, "diff", "--name-only", "-z", "--no-renames", "--diff-filter=A", parents, base, "--")
	if err != nil {
		return "could not check what the starting snapshot of your working tree holds (" + clipText(err.Error(), 200) + ")"
	}
	for _, f := range strings.Split(string(out), "\x00") {
		if f == "" {
			continue
		}
		if why := mdfile.SensitivePath(f); why != "" {
			return "the team started from your uncommitted files, and " + f + " is among them (" + why +
				"); pushing would publish it. Commit or ignore such files before a run, or remove them, to have progress pushed"
		}
	}
	return ""
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
	remote string
	ref    string // the remote branch, without refs/heads/; "" until the first push names it
	// name gives the remote branch at the first push of tip, when ref is "".
	name func(tip string) string
	log  events.Emitter
	tell func(level, msg string)
	// hold, when set, is asked once, before the first push: a reason not to publish the integration branch at all ("" to go on). The
	// history pushed starts from the base the team began on, and that base can hold what the person never committed (snapshotSecrets).
	hold func() string

	mu      sync.Mutex
	want    string // the newest tip asked for
	pushed  string // the last tip the remote acknowledged
	running bool
	failing bool // the last push failed and the person was told
	told    bool // the person was told where the work is pushed
	held    bool // hold was asked; reason says why nothing is pushed ("" when it is)
	reason  string
	idle    chan struct{}
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
		named := p.ref != ""
		asked, reason := p.held, p.reason
		p.mu.Unlock()
		if !asked && p.hold != nil {
			reason = p.hold()
			p.mu.Lock()
			p.held, p.reason = true, reason
			p.mu.Unlock()
			if reason != "" && p.tell != nil {
				p.tell("warn", "The team's verified work is not pushed to "+p.remote+" during this run: "+reason+
					". It stays on the local integration branch and is applied to your checkout at the end.")
			}
		}
		if reason != "" {
			p.mu.Lock()
			p.running = false
			p.mu.Unlock()
			return
		}
		if !named {
			ref := p.name(tip)
			p.mu.Lock()
			p.ref = ref
			p.mu.Unlock()
		}
		err := p.push(tip)
		p.mu.Lock()
		if err != nil {
			p.running = false
			first := !p.failing
			p.failing = true
			p.mu.Unlock()
			if first && p.tell != nil {
				p.tell("warn", fmt.Sprintf("Could not push the team's verified work to %s (branch %s): %s. It is safe on the local integration branch; the push is retried after the next merge.",
					p.remote, p.ref, clipText(err.Error(), 300)))
			}
			return
		}
		p.pushed = tip
		recovered, first := p.failing, !p.told
		p.failing, p.told = false, true
		p.mu.Unlock()
		if (first || recovered) && p.tell != nil {
			p.tell("delivery", fmt.Sprintf("Verified work is pushed to %s branch %s after every merge (now at %s), so a lost machine loses none of it.",
				p.remote, p.ref, shortSHA(tip)))
		}
	}
}

// push runs one git push of tip. The branch is the harness's own, so it is forced:
// a resumed session's rebuilt integration branch must still reach it.
func (p *progressPusher) push(tip string) error {
	ctx, cancel := context.WithTimeout(context.Background(), progressPushTimeout)
	defer cancel()
	_, err := p.git(ctx, "push", "--no-verify", "--quiet", p.remote, "+"+tip+":refs/heads/"+p.ref)
	if p.log != nil {
		data := map[string]any{"remote": p.remote, "branch": p.ref, "tip": tip, "ok": err == nil}
		if err != nil {
			data["error"] = clipText(err.Error(), 300)
		}
		_, _ = p.log.Emit("swarm", TypeProgressPush, data)
	}
	return err
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

// progressBranch is the remote branch the progress of session prefix
// ("sleipnir/<session id>") is pushed to: sleipnir/<topic as a slug>-<the session id's
// last six characters>, so the branch says what the work is and two sessions on the
// same goal still get branches of their own. Without a usable topic it is the
// integration branch's own name.
func progressBranch(prefix, topic string) string {
	slug := slugWords(topic, 48)
	if slug == "" {
		return prefix + "/_integration"
	}
	id := strings.TrimPrefix(prefix, "sleipnir/")
	if len(id) > 6 {
		id = id[len(id)-6:]
	}
	return "sleipnir/" + slug + "-" + id
}

// slugWords is the first words of s, lower case, as [a-z0-9] runs joined by "-", at
// most max bytes and cut at a word boundary.
func slugWords(s string, max int) string {
	var words []string
	var w strings.Builder
	flush := func() {
		if w.Len() > 0 {
			words = append(words, w.String())
			w.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			w.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	out := ""
	for _, word := range words {
		next := word
		if out != "" {
			next = out + "-" + word
		}
		if len(next) > max {
			if out == "" {
				out = word[:max]
			}
			break
		}
		out = next
	}
	return out
}

// shortSHA is the first 12 characters of a commit id.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// credentialHelper reports whether a configuration key names a credential helper: credential.helper, or
// credential.<url>.helper.
func credentialHelper(k string) bool {
	return k == "credential.helper" || strings.HasPrefix(k, "credential.") && strings.HasSuffix(k, ".helper")
}
