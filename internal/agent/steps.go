package agent

import (
	"crypto/sha256"
	"fmt"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// Step windows.
//
// MaxSteps was a ceiling: the request after the last one the run was allowed ended it, whatever the agent was doing. A
// worker building a large part of a project spends its allowance on the work itself, and was stopped in the middle of an
// edit, with nothing said to its successor and an attempt counted against its task; in one twelve-hour team run that
// happened eighteen times, twelve of them to the workers building its web interface, one of them eight times. The repetition
// guard (repeat.go) and the budget are what end a run that has gone wrong; the step limit was a third, cruder, backstop.
//
// With Config.ExtendSteps, MaxSteps is the length of a window instead. At the end of a window in which the agent made
// progress (at least stepProgressEdits successful edits it had not made before in this run) the run gets another window,
// and the person sees a notice. At the end of one without, the agent is told how many steps are left (graceSteps) and what to
// do with them (Config.WrapUp), and the run ends after them as it always did. Counting only new edits keeps an agent that
// writes the same two versions of a file in turn from earning windows: its edits stop being new after the first round.
//
// Edits made by shell commands are not seen; the write, edit and patch tools are how an agent changes files. Without
// ExtendSteps (the default, and every RL rollout, whose step budget is part of its task) nothing here applies.

// stepProgressEdits is how many new successful edits a window must hold for the run to get another.
const stepProgressEdits = 3

// graceSteps is how many steps an agent gets to wrap up after a window without progress: a tenth of the window, at least
// two and at most ten. The first of them is the request already under way when the window ends; the note is in the turn
// that answers it.
func graceSteps(window int) int {
	return max(2, min(10, window/10))
}

// stepWindow is the state of a run's step windows; used by run only.
type stepWindow struct {
	seen  map[[8]byte]bool // edit calls (tool and arguments) that succeeded in this run
	edits int              // new successful edits in the current window
	grace bool             // the last window made no progress: the run ends at its limit
	owed  bool             // the wrap-up note has not been delivered yet
	ended int              // new edits of the window that ended without progress, for the note (edits goes on counting)
}

// reset starts a run with no windows behind it.
func (w *stepWindow) reset() { *w = stepWindow{} }

// observe counts the successful edits of a batch that this run has not made before. calls and results are in the same
// order, as runTools returns them.
func (w *stepWindow) observe(calls, results []core.Block) {
	for i, call := range calls {
		if i >= len(results) || results[i].IsError || editedPaths(call) == nil {
			continue
		}
		sum := sha256.New()
		sum.Write([]byte(call.ToolName))
		sum.Write([]byte{0})
		sum.Write(call.Input)
		var key [8]byte
		copy(key[:], sum.Sum(nil))
		if w.seen[key] {
			continue
		}
		if w.seen == nil {
			w.seen = map[[8]byte]bool{}
		}
		w.seen[key] = true
		w.edits++
	}
}

// nextWindow decides what happens when a run reaches its step limit at step: it moves *limit and reports true when the
// run goes on (another window, or the grace steps to wrap up), and false when it ends.
func (a *Agent) nextWindow(step int, limit *int) bool {
	w := &a.win
	if !a.cfg.ExtendSteps || w.grace {
		return false
	}
	window := a.cfg.MaxSteps
	if w.edits >= stepProgressEdits {
		a.cfg.Sink.Notice(a.cfg.ID, "info", fmt.Sprintf("%d steps so far, %d new edits in the last %d: the run goes on for another %d",
			step, w.edits, window, window))
		w.edits = 0
		*limit += window
		return true
	}
	g := graceSteps(window)
	a.cfg.Sink.Notice(a.cfg.ID, "warn", fmt.Sprintf("%d steps so far and %d new edits in the last %d: %d steps left to wrap up",
		step, w.edits, window, g))
	w.grace, w.owed, w.ended = true, true, w.edits
	*limit += g
	return true
}

// wrapUp adds the owed wrap-up note to blocks, the user turn this step pushes, and records it; left is how many requests
// the run has after this one.
func (a *Agent) wrapUp(blocks []core.Block, left int) []core.Block {
	w := &a.win
	if !w.owed {
		return blocks
	}
	w.owed = false
	what := a.cfg.WrapUp
	if what == "" {
		what = "Then give your answer: what is done, what is left, and how to continue."
	}
	note := fmt.Sprintf("[harness] Your last %d steps made %s. This run ends after %s. Finish or set aside the change "+
		"you are on so that the tree is left in a state that builds. %s", a.cfg.MaxSteps, editCount(w.ended), stepCount(left), what)
	a.emit(events.TypeAgentStuck, map[string]any{"phase": "steps", "note": note})
	return append(blocks, core.Text(note))
}

// stepCount says how many more steps a run has, in words.
func stepCount(n int) string {
	if n == 1 {
		return "1 more step"
	}
	return fmt.Sprintf("%d more steps", max(n, 0))
}

// editCount says how many new edits a window held, in words.
func editCount(n int) string {
	switch n {
	case 0:
		return "no change to any file"
	case 1:
		return "1 new edit"
	}
	return fmt.Sprintf("only %d new edits", n)
}

// stepsLeftLocked is how many requests the current run has left, and false when that is unknown because a window with
// progress extends it. Callers hold a.mu.
func (a *Agent) stepsLeftLocked() (int, bool) {
	if a.cfg.ExtendSteps && !a.stepFinal {
		return 0, false
	}
	return a.stepEnd - a.stepInRun, true
}
