package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

type scripted struct {
	name  string
	errs  []error // the answers in turn; past the end, success
	calls int
}

func (p *scripted) Profile() Profile { return Profile{Name: p.name} }

func (p *scripted) Do(context.Context, *Request, func(Event)) (*Response, error) {
	p.calls++
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
