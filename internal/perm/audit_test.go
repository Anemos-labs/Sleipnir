package perm

import (
	"context"
	"sync"
	"testing"
)

type auditLog struct {
	mu sync.Mutex
	ev []Audit
}

func (l *auditLog) add(a Audit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ev = append(l.ev, a)
}

func (l *auditLog) kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, a := range l.ev {
		s := a.Kind
		if a.Kind == "decide" {
			s += ":" + a.By
		}
		out = append(out, s)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Every question and every refusal is reported, with who settled it; a plain allow is not (there is one per tool call).
func TestAuditReportsQuestionsAndRefusals(t *testing.T) {
	f := newFixture(t)
	run := func(cfg Config, req rq) (Decision, *auditLog) {
		t.Helper()
		l := &auditLog{}
		cfg.Audit = l.add
		e := f.engine(t, cfg)
		return e.Check(context.Background(), f.request(req)), l
	}

	if d, l := run(Config{}, read("{root}/main.go")); !d.Allow || len(l.kinds()) != 0 {
		t.Errorf("a plain allow: %+v %v", d, l.kinds())
	}

	d, l := run(Config{}, read("{out}/secret.txt")) // no one to ask
	if d.Allow || !sameStrings(l.kinds(), []string{"ask", "decide:no one"}) {
		t.Errorf("no prompter: %+v %v", d, l.kinds())
	}
	if ask := l.ev[0]; ask.Request.Tool != "Read" || ask.Reason == "" {
		t.Errorf("the ask does not say what and why: %+v", ask)
	}

	d, l = run(Config{Prompter: func(context.Context, Request) Decision {
		return Decision{Allow: true, Reason: "ok", Remember: ScopeSession}
	}}, read("{out}/secret.txt"))
	if !d.Allow || !sameStrings(l.kinds(), []string{"ask", "decide:user"}) || !l.ev[1].Decision.Allow || l.ev[1].Decision.Remember != ScopeSession {
		t.Errorf("a user's yes: %+v %v", d, l.kinds())
	}

	d, l = run(Config{Prompter: func(context.Context, Request) Decision { return Decision{Reason: "no"} }}, read("{out}/secret.txt"))
	if d.Allow || !sameStrings(l.kinds(), []string{"ask", "decide:user"}) || l.ev[1].Decision.Allow {
		t.Errorf("a user's no: %+v %v", d, l.kinds())
	}

	d, l = run(Config{}, read("{home}/.ssh/id_rsa")) // a built-in protection: refused without a question
	if d.Allow || !sameStrings(l.kinds(), []string{"decide:policy"}) {
		t.Errorf("a protection: %+v %v", d, l.kinds())
	}

	// A request cancelled while it waits for the person is reported as cancelled, not as a refusal by them. The prompter
	// says when it has been reached, so the cancellation cannot come before the question was put.
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	l = &auditLog{}
	e := f.engine(t, Config{Audit: l.add, Prompter: func(pctx context.Context, _ Request) Decision {
		close(entered)
		<-pctx.Done()
		return Decision{}
	}})
	done := make(chan Decision, 1)
	go func() { done <- e.Check(ctx, f.request(read("{out}/secret.txt"))) }()
	<-entered
	cancel()
	if d := <-done; d.Allow || !sameStrings(l.kinds(), []string{"ask", "decide:canceled"}) {
		t.Errorf("a cancelled wait: %+v %v", d, l.kinds())
	}
}
