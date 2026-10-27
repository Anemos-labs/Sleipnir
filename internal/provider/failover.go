package provider

import (
	"context"
	"sync"
	"time"
)

// Failover sends requests to Primary and, once Primary has been in an outage (Error.Outage) for After, to Secondary: the
// fallback model of a session (models.roles.fallback). Requests stay on Secondary for Retry; the next one after that tries
// Primary again and, while Primary is still out, goes on to Secondary within the same call. A failure that is not an outage
// (a bad request, a refused key) is Primary's answer and is returned as it is, and so is an outage shorter than After: the
// caller's own retries wait those out.
//
// A team's manager whose endpoint answered "No endpoints are currently available" for five minutes failed, and the session
// ended as an error with every task done and verified.
//
// Profile is Primary's: the caller shapes its requests for the model it chose, and Secondary renders them for its own wire
// format. A fallback model should have at least Primary's context window.
type Failover struct {
	Primary, Secondary Provider
	// SecondaryModel is the model id Secondary's requests name in place of the one the caller wrote for Primary.
	SecondaryModel string
	After, Retry   time.Duration
	// Now is the clock (time.Now when nil).
	Now func() time.Time
	// OnSwitch, when set, is told each time requests move: to Secondary (with Primary's failure) or back to Primary (nil).
	OnSwitch func(toSecondary bool, cause error)

	mu      sync.Mutex
	failing time.Time // when Primary's current outage began (zero: none)
	since   time.Time // when requests went to Secondary (zero: they go to Primary)
}

// Profile returns Primary's profile.
func (f *Failover) Profile() Profile { return f.Primary.Profile() }

func (f *Failover) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Do sends req to Primary, or to Secondary while Primary is out (see Failover).
func (f *Failover) Do(ctx context.Context, req *Request, on func(Event)) (*Response, error) {
	f.mu.Lock()
	onSecondary := !f.since.IsZero() && f.now().Sub(f.since) < f.Retry
	f.mu.Unlock()
	if onSecondary {
		return f.secondary(ctx, req, on)
	}
	resp, err := f.Primary.Do(ctx, req, on)
	if err == nil {
		f.mu.Lock()
		back := !f.since.IsZero()
		f.failing, f.since = time.Time{}, time.Time{}
		f.mu.Unlock()
		if back && f.OnSwitch != nil {
			f.OnSwitch(false, nil)
		}
		return resp, nil
	}
	pe, ok := AsError(err)
	if !ok || pe.Outage() == NotOutage || ctx.Err() != nil {
		return resp, err
	}
	now := f.now()
	f.mu.Lock()
	if f.failing.IsZero() {
		f.failing = now
	}
	switched := now.Sub(f.failing) >= f.After
	first := switched && f.since.IsZero()
	if switched {
		f.since = now
	}
	f.mu.Unlock()
	if !switched {
		return resp, err
	}
	if first && f.OnSwitch != nil {
		f.OnSwitch(true, err)
	}
	if on != nil {
		on(Event{Kind: EvReset}) // what Primary streamed before it failed is not part of Secondary's answer
	}
	return f.secondary(ctx, req, on)
}

// secondary sends req to Secondary, for SecondaryModel: the prompt names the model it is for.
func (f *Failover) secondary(ctx context.Context, req *Request, on func(Event)) (*Response, error) {
	if req.Prompt != nil && f.SecondaryModel != "" && req.Prompt.Model != f.SecondaryModel {
		r, p := *req, *req.Prompt
		p.Model = f.SecondaryModel
		r.Prompt = &p
		req = &r
	}
	return f.Secondary.Do(ctx, req, on)
}
