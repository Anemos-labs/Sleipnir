//go:build unix

package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// marker is a hook command that leaves evidence it ran.
func marker(name string) string { return "echo ran > " + name }

func ran(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestUntrustedHooksNeverRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin Origin
	}{{"no origin", ""}, {"project origin", OriginProject}} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := ParseAs(tc.origin, ".claude/settings.json", settings(t, PreToolUse, group{hooks: []hookSpec{cmdHook(marker("pwned.txt"))}}))
			if err != nil {
				t.Fatal(err)
			}
			r := &Runner{Set: s, Dir: realTemp(t), Env: testEnv()} // Trusted is false
			res := run(t, r, Event{Name: PreToolUse, Tool: "bash"})
			if ran(r.Dir, "pwned.txt") {
				t.Fatal("a hook from an untrusted source ran")
			}
			if res.Blocked || res.Ran() != 0 || len(res.Runs) != 1 || !strings.Contains(res.Runs[0].Skipped, "not trusted") {
				t.Errorf("result = %+v", res)
			}
			if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "were not run because they come from a source that is not trusted") ||
				!strings.Contains(res.Errors[0].Message, ".claude/settings.json") {
				t.Errorf("the user must be told: %v", res.Errors)
			}
		})
	}
}

func TestUserHooksRunWithoutTrustingTheProject(t *testing.T) {
	user, _ := ParseAs(OriginUser, "~/.sleipnir/config.json", settings(t, Stop, group{hooks: []hookSpec{cmdHook(marker("user.txt"))}}))
	proj, _ := ParseAs(OriginProject, ".claude/settings.json", settings(t, Stop, group{hooks: []hookSpec{cmdHook(marker("proj.txt"))}}))
	r := &Runner{Set: Merge(user, proj), Dir: realTemp(t), Env: testEnv()}
	res := run(t, r, Event{Name: Stop})
	if !ran(r.Dir, "user.txt") || ran(r.Dir, "proj.txt") {
		t.Errorf("user ran: %v, project ran: %v", ran(r.Dir, "user.txt"), ran(r.Dir, "proj.txt"))
	}
	if res.Ran() != 1 || len(res.Runs) != 2 {
		t.Errorf("runs = %+v", res.Runs)
	}
	// Once the project is trusted, both run.
	r.Trusted = true
	run(t, r, Event{Name: Stop})
	if !ran(r.Dir, "proj.txt") {
		t.Error("a trusted runner must run project hooks")
	}
}

func TestAnUntrustedCopyDoesNotShadowATrustedHook(t *testing.T) {
	proj, _ := ParseAs(OriginProject, "proj", settings(t, Stop, group{hooks: []hookSpec{cmdHook(marker("same.txt"))}}))
	user, _ := ParseAs(OriginUser, "user", settings(t, Stop, group{hooks: []hookSpec{cmdHook(marker("same.txt"))}}))
	r := &Runner{Set: Merge(proj, user), Dir: realTemp(t), Env: testEnv()} // the untrusted copy comes first
	run(t, r, Event{Name: Stop})
	if !ran(r.Dir, "same.txt") {
		t.Fatal("the user's own hook was dropped as a duplicate of an untrusted one")
	}
}

func TestApproveCallback(t *testing.T) {
	s, _ := ParseAs(OriginProject, "proj", settings(t, Stop, group{hooks: []hookSpec{cmdHook(marker("a.txt")), cmdHook(marker("b.txt"))}}))
	var mu sync.Mutex
	var asked []Hook
	r := &Runner{Set: s, Dir: realTemp(t), Env: testEnv(), Approve: func(_ context.Context, h Hook) bool {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, h)
		return strings.Contains(h.Command, "a.txt")
	}}
	res := run(t, r, Event{Name: Stop})
	if !ran(r.Dir, "a.txt") || ran(r.Dir, "b.txt") {
		t.Errorf("a ran: %v, b ran: %v", ran(r.Dir, "a.txt"), ran(r.Dir, "b.txt"))
	}
	if len(asked) != 2 || asked[0].Command != marker("a.txt") || asked[0].Event != Stop || asked[0].Origin != OriginProject {
		t.Errorf("the callback must see each hook: %+v", asked)
	}
	if !strings.Contains(res.Runs[1].Skipped, "not trusted") {
		t.Errorf("runs = %+v", res.Runs)
	}
	// Decisions are remembered, for approvals and refusals alike.
	_ = os.Remove(filepath.Join(r.Dir, "a.txt"))
	run(t, r, Event{Name: Stop})
	run(t, r, Event{Name: Stop})
	if len(asked) != 2 || !ran(r.Dir, "a.txt") {
		t.Errorf("asked %d times in total; approved hook ran again: %v", len(asked), ran(r.Dir, "a.txt"))
	}
}

func TestApprovalPromptsAreSerialisedAndNotDuplicated(t *testing.T) {
	s, _ := ParseAs(OriginProject, "proj", settings(t, Stop, group{hooks: []hookSpec{cmdHook("exit 0")}}))
	var calls, inFlight, overlaps atomic.Int32
	r := &Runner{Set: s, Dir: realTemp(t), Env: testEnv(), Approve: func(context.Context, Hook) bool {
		calls.Add(1)
		if inFlight.Add(1) > 1 {
			overlaps.Add(1)
		}
		time.Sleep(100 * time.Millisecond)
		inFlight.Add(-1)
		return true
	}}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, err := r.Run(context.Background(), Event{Name: Stop}); err != nil || res.Ran() != 1 {
				t.Errorf("ran %d, err %v", res.Ran(), err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || overlaps.Load() != 0 {
		t.Errorf("the user was asked %d times (%d overlapping)", calls.Load(), overlaps.Load())
	}
}

func TestACancelledApprovalIsNotRemembered(t *testing.T) {
	s, _ := ParseAs(OriginProject, "proj", settings(t, Stop, group{hooks: []hookSpec{cmdHook(marker("x.txt"))}}))
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	r := &Runner{Set: s, Dir: realTemp(t), Env: testEnv(), Approve: func(context.Context, Hook) bool {
		calls.Add(1)
		cancel() // the user gave up while the question was open
		return true
	}}
	res, err := r.Run(ctx, Event{Name: Stop})
	if err == nil || ran(r.Dir, "x.txt") {
		t.Fatalf("err = %v, ran = %v", err, ran(r.Dir, "x.txt"))
	}
	_ = res
	// Asked again on the next event, not silently approved.
	if _, err := r.Run(context.Background(), Event{Name: Stop}); err != nil || calls.Load() != 2 || !ran(r.Dir, "x.txt") {
		t.Errorf("err %v calls %d ran %v", err, calls.Load(), ran(r.Dir, "x.txt"))
	}
}

func TestHTTPHooksAreOffByDefault(t *testing.T) {
	s, err := ParseAs(OriginUser, "user", settings(t, Stop, group{hooks: []hookSpec{{"type": "http", "url": "http://127.0.0.1:1/never"}}}))
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Set: s, Dir: realTemp(t), Env: testEnv(), Trusted: true}
	res := run(t, r, Event{Name: Stop})
	if res.Ran() != 0 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "http hooks are disabled") {
		t.Errorf("result = %+v", res)
	}
}
