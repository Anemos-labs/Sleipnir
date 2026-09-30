package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/reee344/sleipnir/internal/core"
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
// differently each time is not repetition.

const (
	repeatWindow = 20
	repeatNudge  = 4
	repeatStop   = 8
)

// ErrStuck is returned by Run when the agent repeated one failing call until the guard
// ended the run. errors.Is(err, ErrStuck) tells it from a provider failure or the step limit.
var ErrStuck = errors.New("agent stuck")

type repeatGuard struct {
	recent []string // keys of the failed calls among the last repeatWindow calls ("" for a call that did not fail)
	nudged map[string]bool
}

func (g *repeatGuard) reset() { *g = repeatGuard{} }

// observe records a batch of calls and their results. It returns the note to hand the model
// (empty if none) and, when the run must end, the error to end it with. calls and results are
// in the same order, as runTools returns them.
func (g *repeatGuard) observe(agentID string, calls, results []core.Block) (note string, stop error) {
	for i, call := range calls {
		if i >= len(results) {
			break
		}
		key := ""
		if results[i].IsError {
			sum := sha256.New()
			sum.Write([]byte(call.ToolName))
			sum.Write([]byte{0})
			sum.Write(call.Input)
			sum.Write([]byte{0})
			sum.Write([]byte(results[i].PlainText()))
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
