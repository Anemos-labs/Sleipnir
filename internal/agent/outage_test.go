package agent_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/provider/chaos"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
)

// The first real swarm run on a marketplace endpoint lost a worker to "Database is temporarily unavailable": six attempts over
// forty seconds, and the task went back to the board. Of 591 benchmark sessions, 53 got as far as the last attempt.

func TestAnEndpointThatIsDownForALongTimeIsWaitedOut(t *testing.T) {
	var n atomic.Int32
	r := newLimRig(t, func(c *agent.Config) { c.NoCompaction = true; c.OutagePatience = time.Minute }, nil, func(*mock.Call) mock.Reply {
		if n.Add(1) <= 9 {
			return mock.Reply{Fault: &mock.Fault{Status: 503, Message: "Database is temporarily unavailable"}}
		}
		return mock.Reply{Text: "back"}
	})
	res, err := r.agent.Run(context.Background(), "hi")
	if err != nil || res.Text != "back" {
		t.Fatalf("res=%+v err=%v: nine failures in a row are an outage that an agent with patience waits out", res, err)
	}
	if got := n.Load(); got != 10 {
		t.Errorf("%d requests, want 10", got)
	}
	notices := r.sink.all()
	// Five before the repeats of the usual six attempts, and one for the wait that only patience allows: the next only when half a minute has gone
	// (these delays are milliseconds, so the other three repeats are not announced).
	if len(notices) != 6 {
		t.Fatalf("%d notices, want 6:\n%s", len(notices), strings.Join(notices, "\n"))
	}
	// The first six attempts count as they always did; the ones that only patience allows say how long is waited and how long is left.
	if !strings.HasSuffix(notices[0], "(attempt 2 of 6)") || !strings.HasSuffix(notices[4], "(attempt 6 of 6)") {
		t.Errorf("the notices of the first attempts changed:\n%s", strings.Join(notices, "\n"))
	}
	if !strings.Contains(notices[5], "attempt 7, waited ") || !strings.HasSuffix(notices[5], " of 1m0s for the endpoint)") {
		t.Errorf("a notice of an attempt that only patience allows: %q", notices[5])
	}
}

func TestPatienceEndsWhenItIsUsedUp(t *testing.T) {
	var n atomic.Int32
	r := newLimRig(t, func(c *agent.Config) { c.NoCompaction = true; c.OutagePatience = 300 * time.Millisecond }, nil, func(*mock.Call) mock.Reply {
		n.Add(1)
		return mock.Reply{Fault: &mock.Fault{Status: 503, Message: "Database is temporarily unavailable"}}
	})
	_, err := r.agent.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("an endpoint that never comes back must end the run, however patient the agent")
	}
	// Six attempts, and then waits that add up to the patience: more than the six, and far fewer than for ever.
	if got := n.Load(); got < 7 || got > 20 {
		t.Errorf("%d requests, want more than six and not many more", got)
	}
}

// Patience is for an endpoint that said it is down. What the endpoint refused is wrong however long it waits.
func TestARequestTheEndpointRefusedIsNotWaitedFor(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404} {
		var n atomic.Int32
		r := newLimRig(t, func(c *agent.Config) { c.NoCompaction = true; c.OutagePatience = time.Hour }, nil, func(*mock.Call) mock.Reply {
			n.Add(1)
			return mock.Reply{Fault: &mock.Fault{Status: status, Message: "no"}}
		})
		if _, err := r.agent.Run(context.Background(), "hi"); err == nil {
			t.Errorf("status %d: no error", status)
		}
		if got := n.Load(); got != 1 {
			t.Errorf("status %d: %d requests, want 1", status, got)
		}
	}
}

// A person who is waiting for an endpoint can still stop: the wait is cut by the context like every other.
func TestAnAgentThatIsWaitingForTheEndpointCanBeInterrupted(t *testing.T) {
	var n atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newLimRig(t, func(c *agent.Config) { c.NoCompaction = true; c.OutagePatience = time.Hour }, nil, func(*mock.Call) mock.Reply {
		if n.Add(1) == 8 {
			cancel() // the person pressed Ctrl-C while the endpoint was down
		}
		return mock.Reply{Fault: &mock.Fault{Status: 503, Message: "down"}}
	})
	_, err := r.agent.Run(ctx, "hi")
	if err == nil || ctx.Err() == nil {
		t.Fatalf("err = %v: the run must end with the cancellation", err)
	}
	if got := n.Load(); got > 9 {
		t.Errorf("%d requests: the agent went on after it was cancelled", got)
	}
}

// downFor is a chaos plan that does to the first n requests what f says and serves the rest.
func downFor(n int, f chaos.Fault) chaos.Plan {
	return chaos.PlanFunc(func(i int, _ *http.Request) chaos.Fault {
		if i <= n {
			return f
		}
		return chaos.Fault{}
	})
}

// unreachableRig is an agent in front of an endpoint that the plan breaks, with the client's first-byte timeout short enough that a
// stall is seen quickly.
func unreachableRig(t *testing.T, plan chaos.Plan, mutate func(*agent.Config)) (*limRig, *chaos.Handler) {
	t.Helper()
	var h *chaos.Handler
	r := newLimRigOver(t, func(c *agent.Config) {
		c.NoCompaction = true
		mutate(c)
	}, nil, func(*mock.Call) mock.Reply { return mock.Reply{Text: "back"} }, func(next http.Handler) http.Handler {
		h = chaos.New(next, plan)
		return h
	}, func(c *openaichat.Config) {
		c.FirstByteTimeout, c.StreamIdleTimeout = 60*time.Millisecond, 60*time.Millisecond
	})
	return r, h
}

// On 2026-10-11 the gateway was down twice, for about twenty and eighty minutes, and said so with 502s and 503s as often as with
// nothing at all: connections refused and reset, requests that got no byte back. An agent told that the endpoint exists waits the
// silence out as it waits a 503 out; one that is not still fails a misaddressed endpoint after the usual six attempts.
func TestAGatewayThatAnswersNothingIsWaitedOutWhenItIsKnownToExist(t *testing.T) {
	for _, c := range []struct {
		name  string
		fault chaos.Fault
		n     int
	}{
		{"reset connections", chaos.Fault{Kind: chaos.Reset}, 9},
		{"no response at all", chaos.Fault{Kind: chaos.Stall, For: time.Hour}, 4}, // more than provider.MaxSilentAttempts
	} {
		t.Run(c.name, func(t *testing.T) {
			r, h := unreachableRig(t, downFor(c.n, c.fault), func(cfg *agent.Config) { cfg.OutagePatience, cfg.WaitUnreachable = time.Minute, true })
			res, err := r.agent.Run(context.Background(), "hi")
			if err != nil || res.Text != "back" {
				t.Fatalf("res=%+v err=%v: an endpoint known to exist that answers nothing for a while is waited out", res, err)
			}
			if got := len(h.Events()); got != c.n+1 {
				t.Errorf("%d requests, want %d", got, c.n+1)
			}
		})
		t.Run(c.name+", endpoint not known to exist", func(t *testing.T) {
			r, h := unreachableRig(t, downFor(c.n, c.fault), func(cfg *agent.Config) { cfg.OutagePatience = time.Minute })
			if _, err := r.agent.Run(context.Background(), "hi"); err == nil {
				t.Fatal("a failure with no status is not waited for unless the endpoint is known to exist")
			}
			if got := len(h.Events()); got > 6 {
				t.Errorf("%d requests: more than the six attempts every failure gets", got)
			}
		})
	}
}

// Patience is how long an outage may last, not the sum of the waits between attempts: an endpoint that answers nothing spends each
// attempt's whole timeout saying so, and an agent that counted only its waits would sit through hours of them.
func TestPatienceCountsTheTimeTheEndpointWasSilent(t *testing.T) {
	r, h := unreachableRig(t, downFor(1<<30, chaos.Fault{Kind: chaos.Stall, For: time.Hour}), func(cfg *agent.Config) {
		cfg.OutagePatience, cfg.WaitUnreachable = 250*time.Millisecond, true
	})
	start := time.Now()
	if _, err := r.agent.Run(context.Background(), "hi"); err == nil {
		t.Fatal("an endpoint that never answers must end the run, however patient the agent")
	}
	// Each attempt waits 60 ms for a first byte that never comes and the waits between them are milliseconds: about five attempts
	// fit in the patience. Counting the waits alone would have taken thousands.
	if got := len(h.Events()); got < 3 || got > 8 {
		t.Errorf("%d requests, want the few that fit in the patience", got)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the run took %v to give up on a patience of 250ms", d)
	}
}
