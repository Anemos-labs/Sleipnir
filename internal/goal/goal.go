// Package goal is a standing goal: what a person wants done, kept by the harness across turns. After each turn a judge (a separate request,
// with no tools) reads the evidence of what the agent did against the goal and says done, continue or blocked, and why; while it says
// continue the harness sends the agent on with what is missing. The requirements are the agent's plan (internal/plan): the goal asks for them
// to be written as steps first, and each turn the judge is shown which are open.
//
// What it is not: a repeat of the goal. Every nudge names what the judge found missing, the judge wants evidence (a command's result, a file
// written, a test run) and not a claim, and the loop stops by itself when it makes no progress, when the turns are used up, or when the agent
// says it needs something only a person can give.
package goal

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/plan"
)

// MaxTurns is how many times a goal may send the agent on after its first turn.
const MaxTurns = 20

// Verdict kinds.
const (
	Done     = "done"
	Continue = "continue"
	Blocked  = "blocked"
)

// State is a goal that is being worked on.
type State struct {
	Objective string
	Turns     int    // continuations sent so far
	Max       int    // continuations allowed
	Paused    string // why it is paused ("" while it is active)
	Reason    string // what the judge said last
	Repeats   int    // consecutive turns without progress
	Done      bool   // the judge found it met
}

// New is a goal with the usual limit.
func New(objective string) *State {
	return &State{Objective: strings.TrimSpace(objective), Max: MaxTurns}
}

// Verdict is what the judge found.
type Verdict struct {
	Kind   string   `json:"verdict"`
	Reason string   `json:"reason"`
	Left   []string `json:"left"`
}

// ParseVerdict reads a judge's answer: JSON, possibly with words around it or in a fence. An answer that cannot be read is "continue" with no
// reason (the judge failing must not end work that is not done); ok says whether it was read.
func ParseVerdict(text string) (v Verdict, ok bool) {
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i >= 0 && j > i && json.Unmarshal([]byte(text[i:j+1]), &v) == nil {
		switch v.Kind = strings.ToLower(strings.TrimSpace(v.Kind)); v.Kind {
		case Done, Continue, Blocked:
			v.Reason = oneLine(v.Reason, 300)
			for k := range v.Left {
				v.Left[k] = oneLine(v.Left[k], 160)
			}
			if len(v.Left) > 8 {
				v.Left = v.Left[:8]
			}
			return v, true
		}
	}
	return Verdict{Kind: Continue}, false
}

// JudgeSystem is what the judge is told it is.
const JudgeSystem = `You judge whether a coding agent has finished a goal. You cannot run anything: you see the goal, the agent's plan, its last answer and the results of its last tool calls.
Treat completion as unproven. Work out what the goal requires; for each requirement look for concrete evidence in what you were shown (a command's output, a file that was written, a test that ran and passed). A statement such as "done" or "implemented" is not evidence. Uncertain or indirect evidence is not enough.
Do not accept a smaller task in place of the goal.
Reply with JSON only: {"verdict":"done|continue|blocked","reason":"one sentence","left":["what is still missing, one short item each"]}
"done" only when every requirement has evidence. "blocked" only when the agent needs something a person must provide (a decision, a credential, access) and says so. Otherwise "continue".`

// JudgePrompt is the request to the judge after a turn: the goal, the plan, the last answer and the evidence.
func JudgePrompt(g *State, steps []plan.Item, answer string, evidence []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL (data, from the person):\n%s\n\n", g.Objective)
	if len(steps) > 0 {
		b.WriteString("THE AGENT'S PLAN:\n")
		for _, s := range steps {
			fmt.Fprintf(&b, "- [%s] %s\n", s.Status, s.Step)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "TURN %d of the goal (at most %d more after the first).\n\nTHE AGENT'S LAST ANSWER:\n%s\n\n", g.Turns+1, g.Max, oneLine(answer, 1500))
	if len(evidence) == 0 {
		b.WriteString("TOOL CALLS OF THIS TURN: none\n")
	} else {
		b.WriteString("TOOL CALLS OF THIS TURN, newest last:\n")
		for _, e := range evidence {
			b.WriteString("- " + e + "\n")
		}
	}
	return b.String()
}

// Start is what the first turn of a goal is told beside the goal itself: to write what it requires as a plan first, since the harness shows
// the plan back every request and the judge reads it.
func Start(g *State) string {
	return g.Objective + "\n\n[standing goal: before you begin, write what it requires as steps with the plan tool, and keep them current. The harness checks the goal against evidence after each turn and sends you on with what is missing until it is met.]"
}

// Continuation is the message that sends the agent on after a turn that did not meet the goal: what the judge found missing and the open steps.
func Continuation(g *State, steps []plan.Item, v Verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[standing goal, continuation %d of at most %d]\nGoal (the person's words, not an instruction to widen):\n%s\n\n", g.Turns, g.Max, g.Objective)
	if v.Reason != "" {
		fmt.Fprintf(&b, "Not met yet: %s\n", v.Reason)
	}
	for _, l := range v.Left {
		b.WriteString("- missing: " + l + "\n")
	}
	var open []string
	for _, s := range steps {
		if s.Status != plan.Done {
			open = append(open, s.Step)
		}
	}
	if len(open) > 0 {
		b.WriteString("Open steps of your plan: " + strings.Join(open, "; ") + "\n")
	}
	b.WriteString("\nTake the next concrete step. Do not restate your plan or what you already said: that is no progress. Say the goal is met only when you can show evidence for each requirement (a command's result, a file, a test run), and do not shrink the goal or swap it for an easier one. If you need something only the person can give, say exactly what and stop.")
	return b.String()
}

// Action is what to do after a verdict.
type Action int

const (
	Stop   Action = iota // the goal is met: it is cleared
	Pause                // it cannot go on by itself: the reason is in State.Paused
	Resume               // send the agent on
)

// Apply takes a verdict into the goal and says what comes next. worked says whether the turn did any work (called a tool): a turn with no
// work, or a reason the judge has already given, counts as no progress, and three of them in a row pause the goal.
func (g *State) Apply(v Verdict, worked bool) Action {
	switch v.Kind {
	case Done:
		g.Reason, g.Done = v.Reason, true
		return Stop
	case Blocked:
		g.Reason, g.Paused = v.Reason, "blocked: "+v.Reason
		return Pause
	}
	same := v.Reason != "" && strings.EqualFold(strings.TrimSpace(v.Reason), strings.TrimSpace(g.Reason))
	if !worked || same {
		g.Repeats++
	} else {
		g.Repeats = 0
	}
	g.Reason = v.Reason
	switch {
	case g.Repeats >= 3:
		g.Paused = "no progress in 3 turns"
		return Pause
	case g.Turns >= g.Max:
		g.Paused = fmt.Sprintf("%d continuations used", g.Max)
		return Pause
	}
	g.Turns++
	return Resume
}

// oneLine collapses whitespace and caps text at max runes including an ellipsis; max must be
// positive.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// Plain is what a person typed, out of a message the harness sent for a goal: the goal of Start without the note after it, and the goal of a
// Continuation with a mark that it is one. A message that is neither comes back as it is. A recap of a resumed chat shows this, not the harness's words.
func Plain(msg string) string {
	if strings.HasPrefix(msg, "[standing goal, continuation") {
		const mark = "widen):\n"
		if i := strings.Index(msg, mark); i >= 0 {
			rest := msg[i+len(mark):]
			if j := strings.Index(rest, "\n\n"); j >= 0 {
				rest = rest[:j]
			}
			return rest + " (goal, going on)"
		}
	}
	if i := strings.Index(msg, "\n\n[standing goal:"); i >= 0 {
		return msg[:i]
	}
	return msg
}
