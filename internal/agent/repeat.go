package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
)

// The repetition guard.
//
// A model that is stuck (a small one, or any one facing a refusal it cannot get round) repeats
// the same call and gets the same error, request after request, for every step the run allows.
// A real 2B model made two hundred requests and sent 2.5M input tokens over two commands that
// were refused 68 and 66 times. The step limit and the budget are backstops, not a way to notice
// that nothing changes.
//
// The guard remembers the last repeatWindow tool calls and counts how often the same call (the
// tool and its exact arguments) failed with the same result. At repeatNudge failures the model
// is told so in the results turn, once; at repeatStop the run ends with ErrStuck, after that
// turn has been answered, so the thread stays valid. Only failures count: a call that succeeds
// with the same output (a poll, a re-read) is not a sign of being stuck, and one that fails
// differently each time is not repetition. A failure is a tool error, or a command that exited
// with a status other than 0.

const (
	repeatWindow = 20
	repeatNudge  = 4
	repeatStop   = 8
)

// Refusals of a run with nobody to ask count together, whatever was asked, among the last repeatWindow calls: a model that is refused and
// tries another command each time makes no call twice, but every one of them hits the same wall. Five of twenty is told so, once; ten of
// twenty ends the run. A worker that is refused now and then in a long run of other work never gets there.
const (
	refusalNudge = 5
	refusalStop  = 10
)

// durationRe finds the durations a command prints ("0.004s", "12ms", "1m3s" as its 1m and 3s).
var durationRe = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ns|µs|μs|us|ms|s|m|h)\b`)

// ErrStuck is returned by Run when the agent repeated one failing call until the guard
// ended the run. errors.Is(err, ErrStuck) tells it from a provider failure or the step limit.
var ErrStuck = errors.New("agent stuck")

type repeatGuard struct {
	recent        []string // keys of the failed calls among the last repeatWindow calls ("" for a call that did not fail)
	nudged        map[string]bool
	refused       []bool // for the last repeatWindow calls: was it refused by a run with nobody to ask?
	refusalNudged bool
}

// reset clears the repeated-call guard's accumulated state.
func (g *repeatGuard) reset() { *g = repeatGuard{} }

// observe records a batch of calls and their results. It returns the note to hand the model
// (empty if none) and, when the run must end, the error to end it with. calls and results are
// in the same order, as runTools returns them.
func (g *repeatGuard) observe(agentID string, calls, results []core.Block) (note string, stop error) {
	return g.observeExits(agentID, calls, results, nil)
}

// observeExits is observe for a batch in which some calls failed without their result being an error: a command that exited with
// a status other than 0 (tools.Result.Failed), which the model is meant to read and act on. exitFailed says, per call, that one
// did; it may be shorter than calls. Such a failure counts like any other, except that what a command prints about how long it
// took ("FAIL x 0.004s", "(0.02s)") is not part of how it failed: the same test failing again is the same failure.
func (g *repeatGuard) observeExits(agentID string, calls, results []core.Block, exitFailed []bool) (note string, stop error) {
	for i, call := range calls {
		if i >= len(results) {
			break
		}
		g.refused = append(g.refused, results[i].IsError && perm.IsNoOneToAsk(results[i].PlainText()))
		if len(g.refused) > repeatWindow {
			g.refused = g.refused[len(g.refused)-repeatWindow:]
		}
		refusals := 0
		for _, r := range g.refused {
			if r {
				refusals++
			}
		}
		switch {
		case refusals >= refusalStop && stop == nil:
			stop = fmt.Errorf("agent %s: %w: %d of its last %d actions were refused and this run has no one to ask", agentID, ErrStuck, refusals, len(g.refused))
		case refusals >= refusalNudge && !g.refusalNudged && note == "":
			g.refusalNudged = true
			note = fmt.Sprintf("[harness] %d of your last %d actions were refused, and nobody is here to approve them: another way to the same action "+
				"(a script, another command, a program that runs it) will be refused too. Finish with what you could check, and say which permission you needed.", refusals, len(g.refused))
		case refusals < refusalNudge:
			g.refusalNudged = false // a new burst may be told again
		}
		key := ""
		byExit := i < len(exitFailed) && exitFailed[i] && !results[i].IsError
		if results[i].IsError || byExit {
			text := results[i].PlainText()
			if byExit {
				text = durationRe.ReplaceAllString(text, "#")
			}
			sum := sha256.New()
			sum.Write([]byte(call.ToolName))
			sum.Write([]byte{0})
			sum.Write(call.Input)
			sum.Write([]byte{0})
			sum.Write([]byte(text))
			key = hex.EncodeToString(sum.Sum(nil)[:8])
		}
		g.recent = append(g.recent, key)
		if len(g.recent) > repeatWindow {
			g.recent = g.recent[len(g.recent)-repeatWindow:]
		}
		if key == "" {
			continue
		}
		n := 0
		for _, k := range g.recent {
			if k == key {
				n++
			}
		}
		name := safeToolName(call.ToolName)
		switch {
		case n >= repeatStop && stop == nil:
			stop = fmt.Errorf("agent %s: %w: %s failed the same way %d times", agentID, ErrStuck, name, n)
		case n == repeatNudge && !g.nudged[key]:
			if g.nudged == nil {
				g.nudged = map[string]bool{}
			}
			g.nudged[key] = true
			note = fmt.Sprintf("[harness] %s has now failed the same way %d times with the same arguments. Repeating it will not change the result: "+
				"try another approach, or stop and say what is blocking you.", name, n)
		}
	}
	// A key that left the window may be nudged again if it returns.
	if len(g.nudged) > 0 {
		live := map[string]bool{}
		for _, k := range g.recent {
			if k != "" {
				live[k] = true
			}
		}
		for k := range g.nudged {
			if !live[k] {
				delete(g.nudged, k)
			}
		}
	}
	return note, stop
}

// safeToolName is a tool name fit for a note to the model: a model can invent a name, and the
// note is the harness's own text, so what it quotes is short and plain.
func safeToolName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' || r == ':' {
			return r
		}
		return -1
	}, s)
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "the call"
	}
	return s
}
