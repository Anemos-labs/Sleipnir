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
