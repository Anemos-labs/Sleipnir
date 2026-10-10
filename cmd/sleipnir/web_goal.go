package main

// The standing goal (/goal) of a chat: judged after each turn, sent on while it is not met. The terminal chat (chat_tty.go,
// sessionHost) and the web interface (web_tab.go) share what is here: judging a turn, the /goal command, and for the web the loop
// that runs a turn and its continuations, which the terminal program runs itself one turn at a time.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// judgeTurn is what a standing goal does after a turn that ended with turnErr (session.GoalJudged): the verdict becomes a note, a
// goal that is not met the message of the next turn, a goal that is met is dropped (*gp becomes nil), and the goal is saved in the
// session's log. A chat with no goal does nothing.
func judgeTurn(ctx context.Context, s *session.Session, gp **goal.State, turnErr error) (note, next string, j session.GoalJudgment) {
	if *gp == nil {
		return "", "", j
	}
	note, next, j = s.GoalJudged(ctx, *gp, turnErr)
	if (*gp).Done {
		*gp = nil
	}
	s.SaveGoal(*gp)
	return note, next, j
}

// goalCommandTo is /goal ARG: with text it sets the goal and returns the message that starts on it; alone it writes where the goal
// stands; pause, resume and clear are what they say (resume returns the continuation to send). What it says goes to out, and the
// goal is saved in the session's log whatever it did.
func goalCommandTo(s *session.Session, gp **goal.State, arg string, out io.Writer) (send string) {
	g := *gp
	defer func() { s.SaveGoal(*gp) }()
	switch strings.ToLower(arg) {
	case "":
		if g == nil {
			fmt.Fprintln(out, "no goal. /goal TEXT sets one: the harness keeps the agent going until a judge finds evidence that it is met")
			return ""
		}
		state := "active"
		if g.Paused != "" {
			state = "paused: " + g.Paused
		}
		fmt.Fprintf(out, "goal (%s): %s\ncontinuations %d of %d", state, tools.SanitizeForTerminal(g.Objective), g.Turns, g.Max)
		if g.Reason != "" {
			fmt.Fprintf(out, "; the judge's last word: %s", tools.SanitizeForTerminal(g.Reason))
		}
		fmt.Fprintln(out)
		for _, st := range s.GoalPlan() {
			fmt.Fprintf(out, "  [%s] %s\n", st.Status, tools.SanitizeForTerminal(st.Step))
		}
		return ""
	case "clear":
		*gp = nil
		fmt.Fprintln(out, "goal cleared")
		return ""
	case "pause":
		if g != nil {
			g.Paused = "paused by you"
			fmt.Fprintln(out, "goal paused; /goal resume goes on")
		}
		return ""
	case "resume":
		if g == nil {
			fmt.Fprintln(out, "no goal to resume")
			return ""
		}
		return resumeGoal(s, g)
	}
	*gp = goal.New(arg)
	s.ClearGoalPlan()
	fmt.Fprintln(out, "goal set: checked after each turn. Esc pauses it; /goal shows where it stands")
	return goal.Start(*gp)
}

// resumeGoal takes the pause off a goal and returns the continuation that sends the agent on: a goal that ran out of continuations
// gets as many again.
func resumeGoal(s *session.Session, g *goal.State) string {
	g.Paused, g.Repeats = "", 0
	g.Max = g.Turns + goal.MaxTurns
	g.Turns++
	return goal.Continuation(g, s.GoalPlan(), goal.Verdict{Kind: goal.Continue, Reason: g.Reason})
}

// goalEvent is what the web's goal loop reports while it runs a turn: Kind "ran" (a run of the session returned: Result, Err),
// "judging" (the judge is asked), "judged" (Note, Judgment), "state" (the goal changed: Goal, a copy whose Done says it was met,
// or nil when there is none any more), "next" (a continuation starts: Prompt).
type goalEvent struct {
	Kind     string
	Result   *session.Result
	Err      error
	Note     string
	Judgment session.GoalJudgment
	Goal     *goal.State
	Prompt   string
}

// Errors of the goal's actions, which the routes turn into their codes.
var (
	errNoGoal     = errors.New("there is no goal: /goal TEXT sets one")
	errNotPaused  = errors.New("the goal is not paused")
	errNotActive  = errors.New("the goal is not active")
	errEmptyGoal  = errors.New("/goal needs text")
	budgetReached = "budget reached"
)

// goalLoop runs a standing goal across turns for the web: judge after each turn, continue, pause on cancel, failure, a spent budget
// or a judge that cannot be asked. Its state is safe for concurrent readers; the actions that change it run on the tab's turn
// goroutine or while no turn runs (the tab serialises them).
type goalLoop struct {
	mu   sync.Mutex
	goal *goal.State
	// yield, when set, says that the person has something waiting: a continuation is then not sent, and the goal goes on after their
	// turn (the terminal chat's rule: what was typed meanwhile goes first).
	yield func() bool
}

// cloneGoal copies a goal state (nil stays nil).
func cloneGoal(g *goal.State) *goal.State {
	if g == nil {
		return nil
	}
	c := *g
	return &c
}

// State returns a copy of the goal, nil when there is none.
func (g *goalLoop) State() *goal.State {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cloneGoal(g.goal)
}

// set replaces the goal (nil clears it) and saves it in the session's log.
func (g *goalLoop) set(s *session.Session, st *goal.State) {
	g.mu.Lock()
	g.goal = cloneGoal(st)
	g.mu.Unlock()
	if s != nil {
		s.SaveGoal(st)
	}
}

// Load takes the goal the session's log last recorded (a resumed session's, paused), or none.
func (g *goalLoop) Load(s *session.Session) {
	g.mu.Lock()
	g.goal = s.LoadGoal()
	g.mu.Unlock()
}

// Set starts a goal and returns the message of its first turn; its plan starts empty.
func (g *goalLoop) Set(s *session.Session, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errEmptyGoal
	}
	st := goal.New(text)
	s.ClearGoalPlan()
	g.set(s, st)
	return goal.Start(st), nil
}

// Pause pauses the active goal, saying why.
func (g *goalLoop) Pause(s *session.Session, why string) error {
	st := g.State()
	switch {
	case st == nil:
		return errNoGoal
	case st.Paused != "":
		return errNotActive
	}
	st.Paused = why
	g.set(s, st)
	return nil
}

// Resume takes the pause off the goal and returns the continuation to send.
func (g *goalLoop) Resume(s *session.Session) (string, error) {
	st := g.State()
	switch {
	case st == nil:
		return "", errNoGoal
	case st.Paused == "":
		return "", errNotPaused
	}
	next := resumeGoal(s, st)
	g.set(s, st)
	return next, nil
}

// Clear drops the goal and its plan.
func (g *goalLoop) Clear(s *session.Session) error {
	if g.State() == nil {
		return errNoGoal
	}
	g.set(s, nil)
	s.ClearGoalPlan()
	return nil
}

// Turn runs one turn of prompt and, while the goal continues and nothing of the person's waits, its continuations; it reports through
// emit and returns the error of the last run. A turn that ends on the budget pauses the goal with "budget reached".
func (g *goalLoop) Turn(ctx context.Context, s *session.Session, prompt string, emit func(goalEvent)) error {
	for {
		res, err := s.Run(ctx, prompt)
		emit(goalEvent{Kind: "ran", Result: res, Err: err})
		cur := g.State()
		if cur == nil || cur.Done {
			return err
		}
		if cur.Paused == "" && errors.Is(err, agent.ErrBudget) {
			cur.Paused = budgetReached
			g.set(s, cur)
			emit(goalEvent{Kind: "state", Goal: cloneGoal(cur)})
			return err
		}
		if err == nil && cur.Paused == "" {
			emit(goalEvent{Kind: "judging"})
		}
		wasPaused, prev := cur.Paused != "", cur
		note, next, j := judgeTurn(ctx, s, &cur, err)
		g.mu.Lock()
		g.goal = cloneGoal(cur)
		g.mu.Unlock()
		if !wasPaused {
			emit(goalEvent{Kind: "judged", Note: note, Judgment: j})
			shown := cur
			if shown == nil {
				shown = prev // met: judgeTurn dropped it, and prev says Done
			}
			emit(goalEvent{Kind: "state", Goal: cloneGoal(shown)})
		}
		if next == "" || ctx.Err() != nil || (g.yield != nil && g.yield()) {
			return err
		}
		prompt = next
		emit(goalEvent{Kind: "next", Prompt: next})
	}
}
