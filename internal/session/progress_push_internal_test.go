package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// A failing push is said once and retried after the next merge; a recovery is said; pushes
// are coalesced to the newest tip and never run two at a time.
func TestProgressPusherReportsFailureOnceAndRetries(t *testing.T) {
	var mu sync.Mutex
	var pushed []string
	fail := true
	release := make(chan struct{}, 10)
	entered := make(chan struct{}, 10)
	git := func(ctx context.Context, args ...string) ([]byte, error) {
		entered <- struct{}{}
		<-release
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return nil, errors.New("git push: exit status 128: could not read Username")
		}
		pushed = append(pushed, args[len(args)-1])
		return nil, nil
	}
	var told []string
	var tmu sync.Mutex
	p := &progressPusher{git: git, remote: "origin", ref: "sleipnir/s/_integration", tell: func(level, msg string) {
		tmu.Lock()
		told = append(told, level+": "+msg)
		tmu.Unlock()
	}}
	wait := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Wait(ctx)
	}

	release <- struct{}{}
	p.Kick("aaa")
	<-entered
	wait()
	release <- struct{}{}
	p.Kick("bbb") // still failing: not said again
	<-entered
	wait()
	if len(told) != 1 || !strings.HasPrefix(told[0], "warn: Could not push") {
		t.Fatalf("told %q", told)
	}

	mu.Lock()
	fail = false
	mu.Unlock()
	p.Kick("ccc") // blocks in git until released
	<-entered
	p.Kick("ddd")
	p.Kick("eee") // coalesced with ddd
	release <- struct{}{}
	release <- struct{}{}
	wait()
	if got := strings.Join(pushed, ","); got != "+ccc:refs/heads/sleipnir/s/_integration,+eee:refs/heads/sleipnir/s/_integration" {
		t.Fatalf("pushed %s", got)
	}
	if len(told) != 2 || !strings.Contains(told[1], "pushed to origin branch sleipnir/s/_integration") {
		t.Fatalf("told %q", told)
	}
	p.Kick("eee") // already there: no push
	wait()
	if len(pushed) != 2 {
		t.Fatalf("pushed again: %v", pushed)
	}
}

// A branch whose push remote is "." (the repository itself) falls back to origin.
func TestPushRemoteIgnoresTheRepositoryItself(t *testing.T) {
	git := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "for-each-ref" {
			return []byte(".\n"), nil
		}
		return nil, nil
	}
	if got := pushRemote(context.Background(), git, "main"); got != "origin" {
		t.Fatalf("remote %q, want origin", got)
	}
}

// The pushed branch is named after the work, once, at the first push.
func TestProgressBranchNamesTheWork(t *testing.T) {
	for _, c := range []struct{ topic, want string }{
		{"Build Ravenwatch: a self-hosted uptime monitor with alerts", "sleipnir/build-ravenwatch-a-self-hosted-uptime-monitor-a1b2c3"},
		{"Add CSV export", "sleipnir/add-csv-export-a1b2c3"},
		{"   ¿?  ", "sleipnir/20261019-015656-a1b2c3/_integration"},
		{strings.Repeat("x", 80), "sleipnir/" + strings.Repeat("x", 48) + "-a1b2c3"},
	} {
		if got := progressBranch("sleipnir/20261019-015656-a1b2c3", c.topic); got != c.want {
			t.Errorf("progressBranch(%q) = %q, want %q", c.topic, got, c.want)
		}
	}

	var refs []string
	var mu sync.Mutex
	git := func(ctx context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		refs = append(refs, args[len(args)-1])
		mu.Unlock()
		return nil, nil
	}
	names := 0
	p := &progressPusher{git: git, remote: "origin", name: func(tip string) string { names++; return "sleipnir/add-csv-export-a1b2c3" }}
	for _, tip := range []string{"aaa", "bbb"} {
		p.Kick(tip)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		p.Wait(ctx)
		cancel()
	}
	if names != 1 || len(refs) != 2 || refs[1] != "+bbb:refs/heads/sleipnir/add-csv-export-a1b2c3" {
		t.Fatalf("named %d times, pushed %q", names, refs)
	}
}
