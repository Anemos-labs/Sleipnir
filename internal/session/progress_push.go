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
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
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
	remote string
	ref    string // the remote branch, without refs/heads/
	log    events.Emitter
	tell   func(level, msg string)

	mu      sync.Mutex
	want    string // the newest tip asked for
	pushed  string // the last tip the remote acknowledged
	running bool
	failing bool // the last push failed and the person was told
	told    bool // the person was told where the work is pushed
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
		p.mu.Unlock()
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

// shortSHA is the first 12 characters of a commit id.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
