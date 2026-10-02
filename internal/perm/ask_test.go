package perm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// promptRecorder is a Prompter that counts and records what it is asked.
type promptRecorder struct {
	mu       sync.Mutex
	calls    int32
	requests []Request
	answer   func(n int, r Request) Decision
}

func (p *promptRecorder) prompt(ctx context.Context, r Request) Decision {
	n := int(atomic.AddInt32(&p.calls, 1))
	p.mu.Lock()
	p.requests = append(p.requests, r)
	p.mu.Unlock()
	if p.answer != nil {
		return p.answer(n, r)
	}
	return Decision{Allow: true}
}

func (p *promptRecorder) count() int { return int(atomic.LoadInt32(&p.calls)) }

func askEngine(t *testing.T, f fixture, cfg Config, pr Prompter) *Engine {
	t.Helper()
	cfg.Prompter = pr
	return f.engine(t, cfg)
}

var bg = context.Background()

func TestAskWithoutPrompterDenies(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{})
	d := e.Check(bg, f.request(bash("make")))
	if d.Allow || !strings.HasPrefix(d.Reason, "approval required") {
		t.Fatalf("got %+v", d)
	}
	if !strings.Contains(d.Reason, "make") {
		t.Errorf("the reason should say what needs approval: %q", d.Reason)
	}
	// A run nobody can answer says so: the model then stops looking for another way to the same action.
	if !strings.Contains(d.Reason, "no one to ask") || !IsNoOneToAsk(d.Reason) {
		t.Errorf("the refusal does not say that nothing can be approved: %q", d.Reason)
	}
}

// With someone to ask, the question is put and the refusal is theirs, not the harness's.
func TestAskWithAPrompterNeverSaysNoOneCanAnswer(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(n int, r Request) Decision { return Decision{Allow: false, Reason: "not today"} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	d := e.Check(bg, f.request(bash("make")))
	if d.Allow || strings.Contains(d.Reason, "no one to ask") || IsNoOneToAsk(d.Reason) {
		t.Fatalf("got %+v", d)
	}
}

// A person who said no is not asked to be asked again by another route: the refusal tells the model so. What nobody answered, and
// what a prompter refused for its own reason, is not a person's no and does not say it.
func TestARefusalByAPersonTellsTheModelNotToMakeTheChangeAnotherWay(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		reason string
		advice bool
	}{
		{"denied by user", true},
		{"", true}, // a prompter that gave no reason: the engine says the user declined
		{"no answer", false},
		{"not today", false},
	} {
		rec := &promptRecorder{answer: func(n int, r Request) Decision { return Decision{Allow: false, Reason: c.reason} }}
		e := askEngine(t, f, Config{}, rec.prompt)
		d := e.Check(bg, f.request(bash("make")))
		if d.Allow {
			t.Fatalf("reason %q: allowed", c.reason)
		}
		if got := strings.Contains(d.Reason, "the person said no to this"); got != c.advice {
			t.Errorf("reason %q: advice = %v, want %v (%q)", c.reason, got, c.advice, d.Reason)
		}
	}
	// an approval is never touched
	rec := &promptRecorder{answer: func(n int, r Request) Decision { return Decision{Allow: true, Reason: "allowed by user"} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	if d := e.Check(bg, f.request(bash("make"))); !d.Allow || d.Reason != "allowed by user" {
		t.Errorf("an approval = %+v", d)
	}
}

func TestAskAnswers(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(n int, r Request) Decision {
		if strings.Contains(r.Command, "yes") {
			return Decision{Allow: true}
		}
		return Decision{Allow: false, Reason: "not today"}
	}}
	e := askEngine(t, f, Config{}, rec.prompt)
	if d := e.Check(bg, f.request(bash("make yes"))); !d.Allow || d.Reason != "approved by the user" {
		t.Errorf("approve: %+v", d)
	}
	if d := e.Check(bg, f.request(bash("make no"))); d.Allow || d.Reason != "not today" {
		t.Errorf("decline: %+v", d)
	}
	if d := e.Check(bg, f.request(bash("make quietly-declined"))); d.Allow {
		t.Errorf("decline without a reason: %+v", d)
	}
	// Allowed and denied requests never reach the prompter.
	before := rec.count()
	e.Check(bg, f.request(bash("ls")))
	e.Check(bg, f.request(bash("cat ~/.ssh/id_rsa")))
	if rec.count() != before {
		t.Errorf("the prompter was consulted for decided requests")
	}
	// It receives the request as made.
	q := f.request(rq{tool: "Bash", cmd: "make check", role: "coder"})
	e.Check(bg, q)
	last := rec.requests[len(rec.requests)-1]
	if last.Command != q.Command || last.Role != "coder" || last.Agent != "a1" {
		t.Errorf("prompter saw %+v", last)
	}
	// The one-line summary a human reads says why they are being asked.
	if !strings.HasPrefix(last.Summary, q.Summary) || !strings.Contains(last.Summary, "not on the read-only allowlist") {
		t.Errorf("prompt summary %q", last.Summary)
	}
}

func TestRememberSession(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	if d := e.Check(bg, f.request(bash("mytool test"))); !d.Allow {
		t.Fatal(d)
	}
	if got := e.Rules(Allow); len(got) != 1 || got[0] != "Bash(mytool test)" {
		t.Fatalf("rules after remember: %q", got)
	}
	if d := e.Check(bg, f.request(bash("mytool test"))); !d.Allow || !strings.Contains(d.Reason, "allowed by rule Bash(mytool test)") {
		t.Errorf("second call: %+v", d)
	}
	if rec.count() != 1 {
		t.Errorf("prompted %d times, want 1", rec.count())
	}
	// The remembered rule is exact: other arguments prompt again.
	e.Check(bg, f.request(bash("mytool test -v")))
	e.Check(bg, f.request(bash("mytool build")))
	if rec.count() != 3 {
		t.Errorf("prompted %d times, want 3", rec.count())
	}
}

func TestRememberOnceAddsNothing(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeOnce} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	e.Check(bg, f.request(bash("mytool test")))
	e.Check(bg, f.request(bash("mytool test")))
	if rec.count() != 2 || len(e.Rules(Allow)) != 0 {
		t.Errorf("prompts %d, rules %q", rec.count(), e.Rules(Allow))
	}
}

func TestRememberProjectPersists(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	var persisted []string
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeProject} }}
	e := askEngine(t, f, Config{Persist: func(s Scope, r Rule) {
		mu.Lock()
		persisted = append(persisted, fmt.Sprintf("%s:%s:%s", s, r.Action, r))
		mu.Unlock()
	}}, rec.prompt)
	e.Check(bg, f.request(bash("mytool test")))
	e.Check(bg, f.request(bash("mytool test")))
	mu.Lock()
	defer mu.Unlock()
	if len(persisted) != 1 || persisted[0] != "project:allow:Bash(mytool test)" {
		t.Errorf("persisted %q", persisted)
	}
}

func TestSessionScopeDoesNotPersist(t *testing.T) {
	f := newFixture(t)
	var n int32
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{Persist: func(Scope, Rule) { atomic.AddInt32(&n, 1) }}, rec.prompt)
	e.Check(bg, f.request(bash("mytool test")))
	if atomic.LoadInt32(&n) != 0 {
		t.Error("a session rule was persisted")
	}
}

func TestRememberDeny(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: false, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	e.Check(bg, f.request(bash("make danger")))
	d := e.Check(bg, f.request(bash("make danger")))
	if d.Allow || !strings.Contains(d.Reason, "denied by rule Bash(make danger)") {
		t.Errorf("second call: %+v", d)
	}
	if rec.count() != 1 {
		t.Errorf("prompted %d times, want 1", rec.count())
	}
	if got := e.Rules(Deny); len(got) != 1 {
		t.Errorf("deny rules %q", got)
	}
}

func TestRememberIgnoredWhenAnAskRuleCausedTheQuestion(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{Ask: []string{"Bash(git push:*)"}}, rec.prompt)
	e.Check(bg, f.request(bash("git push origin x")))
	e.Check(bg, f.request(bash("git push origin x")))
	if rec.count() != 2 || len(e.Rules(Allow)) != 0 {
		t.Errorf("prompts %d, allow rules %q: an ask rule must keep asking", rec.count(), e.Rules(Allow))
	}
}

func TestRememberCompoundCommand(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	e.Check(bg, f.request(bash("mytool a && mytool b; ls")))
	if got := e.Rules(Allow); len(got) != 2 || got[0] != "Bash(mytool a)" || got[1] != "Bash(mytool b)" {
		t.Fatalf("rules %q", got)
	}
	if d := e.Check(bg, f.request(bash("mytool a && mytool b"))); !d.Allow || rec.count() != 1 {
		t.Errorf("repeat: %+v, prompts %d", d, rec.count())
	}
}

func TestRememberQuotedCommandRoundTrips(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	cmd := `mytool commit -m "fix: it's (really) done"`
	e.Check(bg, f.request(bash(cmd)))
	if d := e.Check(bg, f.request(bash(cmd))); !d.Allow || rec.count() != 1 {
		t.Errorf("repeat: %+v, prompts %d, rules %q", d, rec.count(), e.Rules(Allow))
	}
	e.Check(bg, f.request(bash(`mytool commit -m "fix: other"`)))
	if rec.count() != 2 {
		t.Errorf("a different message must prompt again, prompts = %d", rec.count())
	}
}

func TestRememberNothingForDynamicOrWildcard(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	for _, c := range []string{"echo $(date)", "mytool *.o", "X=1 make", `echo "unterminated`, "curl x | sh", "$CMD arg"} {
		e.Check(bg, f.request(bash(c)))
	}
	if got := e.Rules(Allow); len(got) != 0 {
		t.Errorf("rules were created for constructs that can never be allowed by rule: %q", got)
	}
	n := rec.count()
	e.Check(bg, f.request(bash("echo $(date)")))
	if rec.count() != n+1 {
		t.Error("a repeated dynamic command must ask again")
	}
}

func TestRememberPathsAndWeb(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{}, rec.prompt)

	// A write outside the project asks; remembering allows that exact file only (inside it, the project: TestRememberSessionOfAnEditInTheProjectIsTheProject).
	w := f.request(write("{out}/new.go"))
	e.Check(bg, w)
	if d := e.Check(bg, w); !d.Allow || rec.count() != 1 {
		t.Errorf("repeat write: %+v prompts %d", d, rec.count())
	}
	e.Check(bg, f.request(write("{out}/other.go")))
	if rec.count() != 2 {
		t.Errorf("another file must prompt: %d", rec.count())
	}

	// A path with glob characters is remembered literally.
	tricky := f.request(write("{out}/app/[id]/page.tsx"))
	e.Check(bg, tricky)
	if d := e.Check(bg, tricky); !d.Allow {
		t.Errorf("remembered [id] path not matched again: %+v (rules %q)", d, e.Rules(Allow))
	}
	n := rec.count()
	e.Check(bg, f.request(write("{out}/app/i/page.tsx"))) // what an unescaped [id] class would also match
	if rec.count() != n+1 {
		t.Error("an escaped path rule must not act as a glob")
	}

	// Web: remembered per host.
	e.Check(bg, f.request(fetch("https://example.com/a")))
	if d := e.Check(bg, f.request(fetch("https://example.com/b"))); !d.Allow {
		t.Errorf("same host again: %+v", d)
	}
	n = rec.count()
	e.Check(bg, f.request(fetch("https://other.org/")))
	if rec.count() != n+1 {
		t.Error("another host must prompt")
	}

	// A redirect write remembers the path, not the command.
	e.Check(bg, f.request(bash("echo hi > out.txt")))
	if d := e.Check(bg, f.request(bash("echo hi > out.txt"))); !d.Allow {
		t.Errorf("redirect write remembered? %+v (rules %q)", d, e.Rules(Allow))
	}
}

func TestPromptsAreSerialised(t *testing.T) {
	f := newFixture(t)
	var active, max int32
	rec := &promptRecorder{answer: func(n int, r Request) Decision {
		cur := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&max)
			if cur <= m || atomic.CompareAndSwapInt32(&max, m, cur) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return Decision{Allow: true}
	}}
	e := askEngine(t, f, Config{}, rec.prompt)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d := e.Check(bg, f.request(bash(fmt.Sprintf("make target%d", i))))
			if !d.Allow {
				t.Errorf("request %d: %+v", i, d)
			}
		}(i)
	}
	wg.Wait()
	if max != 1 {
		t.Errorf("up to %d prompts were on screen at once", max)
	}
	if rec.count() != 40 {
		t.Errorf("prompted %d times, want 40 (distinct requests are not coalesced)", rec.count())
	}
}

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (e *Engine) inflightWaiters() (n int32, prompts int) {
	e.pr.mu.Lock()
	defer e.pr.mu.Unlock()
	for _, p := range e.pr.inflight {
		n += atomic.LoadInt32(&p.waiters)
	}
	return n, len(e.pr.inflight)
}

func TestIdenticalPendingPromptsAreCoalesced(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	rec := &promptRecorder{answer: func(n int, r Request) Decision {
		<-release
		return Decision{Allow: true, Reason: fmt.Sprintf("answer %d", n)}
	}}
	e := askEngine(t, f, Config{}, rec.prompt)

	const n = 20
	results := make([]Decision, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := f.request(bash("make shared"))
			r.Agent = fmt.Sprintf("agent-%d", i) // different agents, same action
			results[i] = e.Check(bg, r)
		}(i)
	}
	waitFor(t, "all callers to queue on one prompt", func() bool {
		w, p := e.inflightWaiters()
		return p == 1 && w == n-1 && rec.count() >= 1 // the leader registers the question before it asks it
	})
	if rec.count() != 1 {
		t.Fatalf("prompter called %d times while pending", rec.count())
	}
	close(release)
	wg.Wait()
	for i, d := range results {
		if !d.Allow || d.Reason != "answer 1" {
			t.Errorf("caller %d: %+v", i, d)
		}
	}
	if rec.count() != 1 {
		t.Errorf("prompter called %d times, want 1", rec.count())
	}
	// The window closed: the same request afterwards is a new question.
	e.Check(bg, f.request(bash("make shared")))
	if rec.count() != 2 {
		t.Errorf("a later identical request should prompt again, prompts = %d", rec.count())
	}
}

func TestDifferentRequestsAreNotCoalesced(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	rec := &promptRecorder{answer: func(n int, r Request) Decision { <-release; return Decision{Allow: true} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	var wg sync.WaitGroup
	for _, c := range []string{"make a", "make b", "make c"} {
		wg.Add(1)
		go func(c string) { defer wg.Done(); e.Check(bg, f.request(bash(c))) }(c)
	}
	waitFor(t, "three queued prompts", func() bool {
		_, p := e.inflightWaiters()
		return p == 3
	})
	for i := 0; i < 3; i++ {
		release <- struct{}{}
	}
	wg.Wait()
	if rec.count() != 3 {
		t.Errorf("prompts = %d, want 3", rec.count())
	}
}

func TestCanceledWaiterLeavesTheOthersAlone(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	rec := &promptRecorder{answer: func(n int, r Request) Decision { <-release; return Decision{Allow: true} }}
	e := askEngine(t, f, Config{}, rec.prompt)

	var wg sync.WaitGroup
	var leader, patient Decision
	wg.Add(2)
	go func() { defer wg.Done(); leader = e.Check(bg, f.request(bash("make shared"))) }()
	waitFor(t, "leader prompt", func() bool { _, p := e.inflightWaiters(); return p == 1 })
	go func() { defer wg.Done(); patient = e.Check(bg, f.request(bash("make shared"))) }()
	waitFor(t, "patient waiter", func() bool { w, _ := e.inflightWaiters(); return w == 1 })

	ctx, cancel := context.WithCancel(bg)
	done := make(chan Decision, 1)
	go func() { done <- e.Check(ctx, f.request(bash("make shared"))) }()
	waitFor(t, "impatient waiter", func() bool { w, _ := e.inflightWaiters(); return w == 2 })
	cancel()
	if d := <-done; d.Allow || !strings.Contains(d.Reason, "canceled") {
		t.Errorf("canceled waiter: %+v", d)
	}
	close(release)
	wg.Wait()
	if !leader.Allow || !patient.Allow || rec.count() != 1 {
		t.Errorf("leader %+v patient %+v prompts %d", leader, patient, rec.count())
	}
}

func TestLeaderCancelLetsAWaiterTakeOver(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(n int, r Request) Decision {
		return Decision{Allow: true, Reason: fmt.Sprintf("answer %d", n)}
	}}
	started := make(chan struct{})
	first := int32(0)
	pr := func(ctx context.Context, r Request) Decision {
		if atomic.AddInt32(&first, 1) == 1 {
			close(started)
			<-ctx.Done() // the first asker's user walked away
			return Decision{Allow: true, Reason: "too late"}
		}
		return rec.prompt(ctx, r)
	}
	e := askEngine(t, f, Config{}, pr)

	leaderCtx, cancel := context.WithCancel(bg)
	var leader, follower Decision
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); leader = e.Check(leaderCtx, f.request(bash("make shared"))) }()
	<-started
	go func() { defer wg.Done(); follower = e.Check(bg, f.request(bash("make shared"))) }()
	waitFor(t, "follower to queue", func() bool { w, _ := e.inflightWaiters(); return w == 1 })
	cancel()
	wg.Wait()
	if leader.Allow || !strings.Contains(leader.Reason, "canceled") {
		t.Errorf("canceled leader must not be approved by a late answer: %+v", leader)
	}
	if !follower.Allow || follower.Reason != "answer 1" {
		t.Errorf("follower should have asked for itself: %+v", follower)
	}
}

func TestQueuedPromptCanBeCanceled(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	rec := &promptRecorder{answer: func(n int, r Request) Decision { <-release; return Decision{Allow: true} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); e.Check(bg, f.request(bash("make first"))) }()
	waitFor(t, "first prompt on screen", func() bool { return rec.count() == 1 })

	ctx, cancel := context.WithTimeout(bg, 30*time.Millisecond)
	defer cancel()
	d := e.Check(ctx, f.request(bash("make second")))
	if d.Allow || !strings.Contains(d.Reason, "canceled") {
		t.Errorf("queued request: %+v", d)
	}
	close(release)
	wg.Wait()
	if rec.count() != 1 {
		t.Errorf("the canceled request must never reach the prompter (prompts = %d)", rec.count())
	}
	// And the engine is still usable.
	if d := e.Check(bg, f.request(bash("make third"))); !d.Allow {
		t.Errorf("after cancellation: %+v", d)
	}
}

func TestAlreadyCanceledContextNeverPrompts(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{}
	e := askEngine(t, f, Config{}, rec.prompt)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if d := e.Check(ctx, f.request(bash("make"))); d.Allow || rec.count() != 0 {
		t.Errorf("%+v, prompts %d", d, rec.count())
	}
}

func TestPanickingPrompterDoesNotWedgeTheEngine(t *testing.T) {
	f := newFixture(t)
	calls := int32(0)
	e := askEngine(t, f, Config{}, func(ctx context.Context, r Request) Decision {
		if atomic.AddInt32(&calls, 1) == 1 {
			panic("prompt UI crashed")
		}
		return Decision{Allow: true}
	})
	func() {
		defer func() { _ = recover() }()
		e.Check(bg, f.request(bash("make shared")))
	}()
	done := make(chan Decision, 1)
	go func() { done <- e.Check(bg, f.request(bash("make shared"))) }()
	select {
	case d := <-done:
		if !d.Allow {
			t.Errorf("after a panic: %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the engine deadlocked after a prompter panic")
	}
}

// "Don't ask again" for a runner command remembers the prefix, so that the next go test of another package is not asked about; what a
// prefix must not cover is still asked: a flag that runs a program, a different subcommand, a chain, a program that is not a runner.
func TestRememberSessionOfARunnerCommandIsItsPrefix(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	if d := e.Check(bg, f.request(bash("go test ./a"))); !d.Allow {
		t.Fatal(d)
	}
	if got := e.Rules(Allow); len(got) != 1 || got[0] != "Bash(go test:*)" {
		t.Fatalf("rules after remember: %q", got)
	}
	for _, cmd := range []string{"go test ./b", "go test -race -count=1 ./..."} {
		if d := e.Check(bg, f.request(bash(cmd))); !d.Allow {
			t.Errorf("%s: %+v", cmd, d)
		}
	}
	if rec.count() != 1 {
		t.Errorf("prompted %d times, want 1", rec.count())
	}
	for _, cmd := range []string{"go run .", "go test ./a && rm -rf x", "go env -w X=1", "gofmt -w ."} {
		before := rec.count()
		e.Check(bg, f.request(bash(cmd)))
		if rec.count() != before+1 {
			t.Errorf("%q was not asked about", cmd)
		}
	}
}

// The question says what the second option remembers, and what it says is what it does.
func TestRememberSessionIsNamedInTheQuestion(t *testing.T) {
	f := newFixture(t)
	var seen []string
	rec := &promptRecorder{answer: func(_ int, r Request) Decision {
		seen = append(seen, r.Remembers)
		return Decision{Allow: true, Remember: ScopeSession}
	}}
	e := askEngine(t, f, Config{}, rec.prompt)
	e.Check(bg, f.request(bash("go test ./a")))
	e.Check(bg, f.request(bash("bash x.sh")))
	e.Check(bg, f.request(bash("FOO=1 go test ./a")))
	want := []string{`"go test" commands`, "", ""}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("Remembers = %q, want %q", seen, want)
	}
}

// An edit is never repeated exactly, so a yes for the session to an edit inside the project is a yes to edits inside the project; an
// edit outside it, and a no, stay exact.
func TestRememberSessionOfAnEditInTheProjectIsTheProject(t *testing.T) {
	f := newFixture(t)
	var said string
	rec := &promptRecorder{answer: func(_ int, r Request) Decision {
		said = r.Remembers
		return Decision{Allow: true, Remember: ScopeSession}
	}}
	e := askEngine(t, f, Config{Ask: []string{"Edit(./secrets/**)"}}, rec.prompt)
	if d := e.Check(bg, f.request(write("{root}/src/new1.go"))); !d.Allow {
		t.Fatal(d)
	}
	if said != "edits in this project" {
		t.Errorf("Remembers = %q", said)
	}
	if d := e.Check(bg, f.request(write("{root}/other/new2.go"))); !d.Allow || rec.count() != 1 {
		t.Errorf("a second edit in the project: %+v, prompts %d", d, rec.count())
	}
	// what the project keeps asked about stays asked about, whatever was allowed
	n := rec.count()
	e.Check(bg, f.request(write("{root}/secrets/k.txt")))
	if rec.count() != n+1 {
		t.Error("an edit the project keeps asked about was allowed by the rule for the project")
	}
	if d := e.Check(bg, f.request(write("{root}/.git/hooks/pre-commit"))); d.Allow {
		t.Error("an edit of a git hook was allowed by the rule for the project")
	}
	if d := e.Check(bg, f.request(write("{out}/x.txt"))); !d.Allow || rec.count() != n+2 || said != "" {
		t.Errorf("an edit outside the project: %+v, prompts %d, Remembers %q", d, rec.count(), said)
	}
	if d := e.Check(bg, f.request(write("{out}/y.txt"))); rec.count() != n+3 {
		t.Errorf("another edit outside is asked about again: %+v, prompts %d", d, rec.count())
	}
}

// A team asks several questions at once and the person answers them one at a time. "Yes, and don't ask again" to the first settles the
// ones queued behind it that the new rule covers: the person is not asked what they have just answered.
func TestAQuestionQueuedBehindAnotherIsSettledByItsAnswer(t *testing.T) {
	f := newFixture(t)
	onScreen, answer := make(chan struct{}), make(chan struct{})
	rec := &promptRecorder{answer: func(n int, r Request) Decision {
		if n == 1 {
			close(onScreen)
			<-answer
		}
		return Decision{Allow: true, Remember: ScopeSession}
	}}
	e := askEngine(t, f, Config{}, rec.prompt)
	var wg sync.WaitGroup
	got := make([]Decision, 2)
	ask := func(i int, cmd string) {
		defer wg.Done()
		got[i] = e.Check(bg, f.request(bash(cmd)))
	}
	wg.Add(1)
	go ask(0, "go test ./a")
	<-onScreen
	wg.Add(1)
	go ask(1, "go test ./b")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) { // the second is in the queue
		e.pr.mu.Lock()
		n := len(e.pr.inflight)
		e.pr.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second question never queued")
		}
	}
	close(answer)
	wg.Wait()
	if !got[0].Allow || !got[1].Allow {
		t.Fatalf("decisions %+v", got)
	}
	if rec.count() != 1 {
		t.Errorf("the person was asked %d times, want 1: the second was covered by the answer to the first", rec.count())
	}
}

// A person running a Python project's tests is asked once, not for every variant: "don't ask again" for python3 -m unittest remembers that
// and not the line, while an interpreter given a program of its own, or another module, is still asked about.
func TestRememberSessionOfAPythonTestRunIsItsPrefix(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	if d := e.Check(bg, f.request(bash("python3 -m unittest -v"))); !d.Allow {
		t.Fatal(d)
	}
	if got := e.Rules(Allow); len(got) != 1 || got[0] != "Bash(python3 -m unittest:*)" {
		t.Fatalf("rules after remember: %q", got)
	}
	for _, cmd := range []string{"python3 -m unittest discover -s tests", "python3 -m unittest -v 2>&1 | tail -20", "python3 -m unittest test_x.T.test_a"} {
		if d := e.Check(bg, f.request(bash(cmd))); !d.Allow {
			t.Errorf("%s: %+v", cmd, d)
		}
	}
	if rec.count() != 1 {
		t.Errorf("prompted %d times, want 1", rec.count())
	}
	for _, cmd := range []string{"python3 -c 'print(1)'", "python3 script.py", "python3 -m http.server", "python -m unittest"} {
		before := rec.count()
		e.Check(bg, f.request(bash(cmd)))
		if rec.count() != before+1 {
			t.Errorf("%q was not asked about", cmd)
		}
	}
}

// The same for a Node project's tests: five spellings of node --test (a directory, a glob, 2>&1) were five questions in a trial; "node script.js" is still asked.
func TestRememberSessionOfANodeTestRunIsItsPrefix(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	if d := e.Check(bg, f.request(bash("node --test test/*.test.js 2>&1"))); !d.Allow {
		t.Fatal(d)
	}
	for _, cmd := range []string{"node --test test/ 2>&1", "node --test test", "node --test --test-reporter=spec"} {
		n := rec.count()
		if d := e.Check(bg, f.request(bash(cmd))); !d.Allow || rec.count() != n {
			t.Errorf("%s: %+v (asked %d more times)", cmd, d, rec.count()-n)
		}
	}
	before := rec.count()
	e.Check(bg, f.request(bash("node script.js")))
	if rec.count() != before+1 {
		t.Error("node script.js was not asked about")
	}
}

// What a person is told when a command names a file by a variable is words they can act on, not "cannot tell statically which path is".
func TestAVariableFileNameIsExplainedInPlainWords(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: false} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	e.Check(bg, f.request(bash(`for f in $LIST; do cat "$f"; done`)))
	if rec.count() != 1 {
		t.Fatalf("asked %d times", rec.count())
	}
	if got := rec.requests[0].Summary; !strings.Contains(got, `"$f" is a file name that is only known when the command runs`) || strings.Contains(got, "statically") {
		t.Errorf("the question says %q", got)
	}
}

// A compound line of runner commands ("go vet ./a && go test ./a") that is allowed for the session remembers each command by its prefix: the
// same line for another package (what a worker does next) is not a new question.
func TestACompoundOfRunnersIsRememberedByPrefix(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	e.Check(bg, f.request(bash("go vet ./a && go test ./a")))
	if d := e.Check(bg, f.request(bash("go vet ./b && go test ./b"))); !d.Allow || rec.count() != 1 {
		t.Errorf("the same line for another package: %+v after %d questions", d, rec.count())
	}
}

// A line of several commands says, in the question, that a yes for the session remembers each of them: "npm test commands" alone hid the rm that came with it.
func TestTheQuestionOfACompoundLineNamesEveryCommandItRemembers(t *testing.T) {
	f := newFixture(t)
	var shown string
	rec := &promptRecorder{answer: func(_ int, r Request) Decision { shown = r.Remembers; return Decision{Allow: true} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	e.Check(bg, f.request(bash("npm test 2>&1 | tail -3; rm -rf /tmp/notesmoke")))
	if !strings.Contains(shown, `"npm test" commands`) || !strings.Contains(shown, "rm -rf /tmp/notesmoke") {
		t.Errorf("the question says it remembers %q", shown)
	}
}
