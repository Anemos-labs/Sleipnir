package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/chaos"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// A run through an endpoint that fails the way real ones do must come out as if it had not: the answer arrives, no tool is run twice
// because a response that carried its call was cut off and asked for again, no call is left without its result (or a result without
// its call) in the thread the next request is built from, and nothing hangs. The faults are the ones the benchmark and the dogfood
// runs met on a marketplace endpoint within a day (internal/provider/chaos).

const chaosAnswer = "all done"

// chaosModel is the model: two steps that call a tool and then the answer. It answers by the number of assistant turns the request
// carries, so that a request that is repeated after a failure is answered as the one that failed was.
func chaosModel(c *mock.Call) mock.Reply {
	switch assistantTurns(c) {
	case 0:
		return mock.Reply{Text: "reading the code first, which takes a few words to say", ToolCalls: []mock.ToolCall{{ID: "c0", Name: "look", Args: `{"n":1}`}}}
	case 1:
		return mock.Reply{Text: "now the change, which takes a few words as well", ToolCalls: []mock.ToolCall{{ID: "c1", Name: "change", Args: `{"n":2}`}}}
	}
	return mock.Reply{Text: chaosAnswer}
}

// chaosFaults is every fault the endpoint was seen to have that waiting can cure, in a form that fits a test: the stalls are short
// because the client's timeouts are, and the cuts fall inside the first response (which is about 500 bytes) and inside the later ones.
func chaosFaults() []chaos.Fault {
	return []chaos.Fault{
		chaos.Down(), chaos.Lost(), chaos.Internal(), chaos.BadGateway(), chaos.Limited(5 * time.Millisecond),
		{Kind: chaos.Reset},
		{Kind: chaos.Abort, After: 40}, {Kind: chaos.Abort, After: 260},
		{Kind: chaos.Truncate, After: 40}, {Kind: chaos.Truncate, After: 260},
		{Kind: chaos.Stall, For: time.Hour},
		{Kind: chaos.StallMidStream, After: 120, For: time.Hour},
		{Kind: chaos.Garbage},
	}
}

type chaosRun struct {
	rig   *limRig
	h     *chaos.Handler
	looks atomic.Int32
	edits atomic.Int32
}

func newChaosRun(t *testing.T, plan chaos.Plan) *chaosRun {
	t.Helper()
	// A 429 says "Retry-After: 1" at the least (the header has whole seconds): the agent waits what it is asked, up to this bound.
	was := agent.MaxRetryAfter
	agent.MaxRetryAfter = 20 * time.Millisecond
	t.Cleanup(func() { agent.MaxRetryAfter = was })
	cr := &chaosRun{}
	fts := []fakeTool{
		{name: "look", readOnly: true, run: func(json.RawMessage) *tools.Result { cr.looks.Add(1); return &tools.Result{Text: "the code"} }},
		{name: "change", run: func(json.RawMessage) *tools.Result { cr.edits.Add(1); return &tools.Result{Text: "changed"} }},
	}
	cr.rig = newLimRigOver(t, func(c *agent.Config) {
		c.NoCompaction = true
		c.OutagePatience = time.Minute // the faults here are transient: the agent is as patient as the real one
	}, fts, chaosModel, func(next http.Handler) http.Handler {
		cr.h = chaos.New(next, plan)
		return cr.h
	}, func(c *openaichat.Config) {
		c.FirstByteTimeout, c.StreamIdleTimeout = 250*time.Millisecond, 250*time.Millisecond
	})
	return cr
}

// mayGiveUp says whether what the endpoint did to the request that the run ended on leaves the agent the right to stop. The policy is
// the provider's and the agent's own: a request that got no response at all (a stall before the first byte) twice is not tried a
// third time (provider.MaxSilentAttempts), and one that keeps failing without being an outage (no status of 500 or more, no 429: a cut
// connection, a stream that broke or went quiet, a body that is not a response) is tried six times and no more. An outage is waited
// for, up to the patience. A request is over when the endpoint passes it.
func mayGiveUp(evs []chaos.Event) bool {
	attempts, stalls := 0, 0
	var last chaos.Fault
	for _, e := range evs {
		if e.Fault.Kind == chaos.Pass {
			attempts, stalls = 0, 0
			continue
		}
		attempts++
		if e.Fault.Kind == chaos.Stall {
			stalls++
		}
		last = e.Fault
	}
	outage := last.Kind == chaos.Status && (last.Status >= 500 || last.Status == http.StatusTooManyRequests)
	return stalls >= provider.MaxSilentAttempts || (attempts >= 6 && !outage)
}

// check says what is wrong with a finished run, or "". A run that failed is wrong unless the endpoint gave the agent the right to give
// up (mayGiveUp); then what it must still be is a clean thread and no tool run twice.
func (cr *chaosRun) check(res *agent.Result, err error) string {
	var bad []string
	switch {
	case err != nil && mayGiveUp(cr.h.Events()):
		// the policy ended it: the invariants below still hold
	case err != nil:
		bad = append(bad, fmt.Sprintf("the run failed: %v", err))
	case res == nil || res.Text != chaosAnswer:
		bad = append(bad, fmt.Sprintf("the answer is %+v, want %q", res, chaosAnswer))
	}
	if n := cr.looks.Load(); n > 1 || (err == nil && n != 1) {
		bad = append(bad, fmt.Sprintf("look ran %d times, want once", n))
	}
	if n := cr.edits.Load(); n > 1 || (err == nil && n != 1) {
		bad = append(bad, fmt.Sprintf("change ran %d times, want once: a response that carried the call was repeated and the call was run again", n))
	}
	if msg := threadProblem(cr.rig.agent.Stack().Thread.Turns); msg != "" {
		bad = append(bad, msg)
	}
	return strings.Join(bad, "; ")
}

// threadProblem says what is wrong with the turns of a thread as a provider would be sent them, or "": every call has its result in
// the turn after the one that made it, every result has its call, and no turn is empty.
func threadProblem(turns []core.Turn) string {
	pending := map[string]bool{}
	for i, t := range turns {
		if len(t.Blocks) == 0 {
			return fmt.Sprintf("turn %d (%s) is empty", i, t.Role)
		}
		var uses, results []string
		for _, b := range t.Blocks {
			switch b.Kind {
			case core.BlockToolUse:
				uses = append(uses, b.ToolID)
			case core.BlockToolResult:
				results = append(results, b.ToolID)
			}
		}
		for _, id := range results {
			if !pending[id] {
				return fmt.Sprintf("turn %d has a result for %q, which no earlier turn called", i, id)
			}
			delete(pending, id)
		}
		if len(uses) > 0 && len(pending) > 0 {
			return fmt.Sprintf("turn %d calls again while %v still wait for their results", i, pending)
		}
		for _, id := range uses {
			pending[id] = true
		}
	}
	if len(pending) > 0 {
		return fmt.Sprintf("the thread ends with calls that have no results: %v", pending)
	}
	return ""
}

// Each fault, at each of the three requests the run needs: the run still ends with the answer, having run each tool once.
func TestEveryFaultAtEveryStepIsSurvived(t *testing.T) {
	for _, f := range chaosFaults() {
		for step := 1; step <= 3; step++ {
			t.Run(fmt.Sprintf("%v at request %d", f, step), func(t *testing.T) {
				faults := make([]chaos.Fault, step)
				faults[step-1] = f
				cr := newChaosRun(t, chaos.Script(faults...))
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // a hang guard
				defer cancel()
				res, err := cr.rig.agent.Run(ctx, "do the work")
				if msg := cr.check(res, err); msg != "" {
					t.Fatalf("%s\nwhat the endpoint did: %+v\nwhat the person was told:\n%s", msg, cr.h.Events(), strings.Join(cr.rig.sink.all(), "\n"))
				}
				if got := cr.h.Faulted(); got != 1 && !(f.Kind == chaos.Abort || f.Kind == chaos.Truncate || f.Kind == chaos.StallMidStream) {
					t.Errorf("%d requests were faulted, want the one", got)
				}
			})
		}
	}
}

// A random mixture: eighty runs of it, each its own seed (the nightly run does a thousand, SLEIPNIR_CHAOS_SEEDS): whatever the sequence
// of failures, the invariants hold. A failing seed is a failing run on any machine (chaos.Random is a function of the seed and the
// number of the request).
func TestARandomMixtureOfFaultsIsSurvived(t *testing.T) {
	seeds := 80
	if testing.Short() {
		seeds = 20
	}
	if v := os.Getenv("SLEIPNIR_CHAOS_SEEDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("SLEIPNIR_CHAOS_SEEDS=%q is not a number of runs", v)
		}
		seeds = n
	}
	faults := chaosFaults()
	for seed := int64(1); seed <= int64(seeds); seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			cr := newChaosRun(t, chaos.Random(seed, 0.45, faults...))
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			res, err := cr.rig.agent.Run(ctx, "do the work")
			if msg := cr.check(res, err); msg != "" {
				t.Fatalf("seed %d: %s\nwhat the endpoint did: %+v\nwhat the person was told:\n%s", seed, msg, cr.h.Events(), strings.Join(cr.rig.sink.all(), "\n"))
			}
		})
	}
}

// What cannot be cured by waiting ends the run at once, whatever is behind it.
func TestAFaultThatWaitingCannotCureEndsTheRunAtOnce(t *testing.T) {
	cr := newChaosRun(t, chaos.Script(chaos.Fault{}, chaos.Refused()))
	res, err := cr.rig.agent.Run(context.Background(), "do the work")
	if err == nil {
		t.Fatalf("a refused key is not a reason to go on: %+v", res)
	}
	if n := len(cr.h.Events()); n != 2 {
		t.Errorf("%d requests, want the one that worked and the one that was refused", n)
	}
	if msg := threadProblem(cr.rig.agent.Stack().Thread.Turns); msg != "" {
		t.Errorf("a run that ended on a refusal left a thread that cannot be sent: %s", msg)
	}
}
