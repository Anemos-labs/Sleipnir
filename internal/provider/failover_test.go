package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
)

type scripted struct {
	name  string
	errs  []error // the answers in turn; past the end, success
	calls int
	last  *Request
}

func (p *scripted) Profile() Profile { return Profile{Name: p.name} }

func (p *scripted) Do(_ context.Context, req *Request, _ func(Event)) (*Response, error) {
	p.calls++
	p.last = req
	if p.calls <= len(p.errs) && p.errs[p.calls-1] != nil {
		return nil, p.errs[p.calls-1]
	}
	return &Response{Model: p.name}, nil
}

// A primary in an outage for longer than After hands its requests to the secondary for Retry, then is tried again; a failure that
// is not an outage, and an outage shorter than After, are the primary's answer.
func TestFailoverMovesToTheSecondaryAfterASustainedOutage(t *testing.T) {
	down := &Error{Kind: ErrServer, Status: 503}
	bad := &Error{Kind: ErrBadRequest, Status: 400}
	now := time.Unix(1000, 0)
	prim := &scripted{name: "prim", errs: []error{bad, down, bad, down, down, down, down, down, nil}}
	sec := &scripted{name: "sec"}
	var moves []bool
	f := &Failover{Primary: prim, Secondary: sec, After: time.Minute, Retry: 10 * time.Minute, Now: func() time.Time { return now },
		OnSwitch: func(to bool, _ error) { moves = append(moves, to) }}
	if f.Profile().Name != "prim" {
		t.Fatal("the profile is not the primary's")
	}
	do := func() (string, error) {
		resp, err := f.Do(context.Background(), &Request{}, nil)
		if err != nil {
			return "", err
		}
		return resp.Model, nil
	}
	if _, err := do(); !errors.Is(err, bad) {
		t.Fatalf("a bad request went elsewhere: %v", err)
	}
	if _, err := do(); !errors.Is(err, down) { // an outage begins
		t.Fatalf("an outage just begun: %v", err)
	}
	if _, err := do(); !errors.Is(err, bad) { // and ends: the endpoint answers
		t.Fatalf("a bad request: %v", err)
	}
	now = now.Add(time.Hour)
	if _, err := do(); !errors.Is(err, down) { // a new outage, which the old one's start does not age
		t.Fatalf("an outage begun after the endpoint answered: %v", err)
	}
	now = now.Add(30 * time.Second)
	if _, err := do(); !errors.Is(err, down) {
		t.Fatalf("an outage shorter than After: %v", err)
	}
	now = now.Add(31 * time.Second)
	if got, err := do(); err != nil || got != "sec" {
		t.Fatalf("after a sustained outage: %q, %v", got, err)
	}
	now = now.Add(5 * time.Minute)
	if got, _ := do(); got != "sec" || prim.calls != 6 {
		t.Fatalf("within Retry: %q, primary called %d times", got, prim.calls)
	}
	now = now.Add(6 * time.Minute) // past Retry: the primary is tried again, and is still out
	if got, _ := do(); got != "sec" || prim.calls != 7 {
		t.Fatalf("past Retry with the primary still out: %q, primary called %d times", got, prim.calls)
	}
	now = now.Add(11 * time.Minute)
	if got, _ := do(); got != "sec" || prim.calls != 8 {
		t.Fatalf("past Retry again: %q, primary called %d times", got, prim.calls)
	}
	now = now.Add(11 * time.Minute) // the primary is back
	if got, _ := do(); got != "prim" {
		t.Fatalf("the primary is back: %q", got)
	}
	if len(moves) != 2 || !moves[0] || moves[1] {
		t.Fatalf("switches = %v, want [true false]", moves)
	}
}

// An answer from the secondary is charged at the secondary's price when it carries no exact charge of its own.
func TestFailoverPricesTheSecondary(t *testing.T) {
	now := time.Unix(1000, 0)
	prim := &scripted{name: "prim", errs: []error{&Error{Kind: ErrServer, Status: 503}, &Error{Kind: ErrServer, Status: 503}}}
	f := &Failover{Primary: prim, Secondary: &scripted{name: "sec"}, After: time.Minute, Retry: time.Hour, Now: func() time.Time { return now },
		SecondaryPrice: func(core.Usage) float64 { return 0.25 }}
	if _, err := f.Do(context.Background(), &Request{}, nil); err == nil {
		t.Fatal("an outage just begun went to the secondary")
	}
	now = now.Add(2 * time.Minute)
	resp, err := f.Do(context.Background(), &Request{}, nil)
	if err != nil || resp.Model != "sec" || resp.CostUSD == nil || *resp.CostUSD != 0.25 || !resp.CostEstimated {
		t.Fatalf("secondary answer %+v, %v", resp, err)
	}
}

// A limit sized for the primary asks the secondary for no more than its maximum; the caller's request is left as it was.
func TestFailoverCapsTheOutputLimitAtTheSecondarysMaximum(t *testing.T) {
	now := time.Unix(1000, 0)
	prim := &scripted{name: "prim", errs: []error{&Error{Kind: ErrServer, Status: 503}, &Error{Kind: ErrServer, Status: 503}, &Error{Kind: ErrServer, Status: 503}}}
	sec := &scripted{name: "sec"}
	f := &Failover{Primary: prim, Secondary: sec, SecondaryModel: "small", SecondaryMaxOutput: 20000, After: time.Minute, Retry: time.Hour,
		Now: func() time.Time { return now }}
	_, _ = f.Do(context.Background(), &Request{Prompt: &core.Prompt{}}, nil)
	now = now.Add(2 * time.Minute)
	req := &Request{Prompt: &core.Prompt{Model: "big", Params: core.Params{MaxTokens: 32000}}}
	if _, err := f.Do(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if got := sec.last.Prompt.Params.MaxTokens; got != 20000 || sec.last.Prompt.Model != "small" {
		t.Fatalf("secondary asked for %d tokens of %q, want 20000 of small", got, sec.last.Prompt.Model)
	}
	if req.Prompt.Params.MaxTokens != 32000 || req.Prompt.Model != "big" {
		t.Fatalf("the caller's request changed: %+v", req.Prompt)
	}
	small := &Request{Prompt: &core.Prompt{Model: "big", Params: core.Params{MaxTokens: 4096}}}
	if _, err := f.Do(context.Background(), small, nil); err != nil || sec.last.Prompt.Params.MaxTokens != 4096 {
		t.Fatalf("a limit within the secondary's maximum became %d (%v)", sec.last.Prompt.Params.MaxTokens, err)
	}
}
