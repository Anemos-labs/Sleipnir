package agent_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/provider/mock"
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
	if len(notices) != 9 {
		t.Fatalf("%d notices, want one before each of the nine repeats:\n%s", len(notices), strings.Join(notices, "\n"))
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
