// Package plan is the plan an agent keeps for a task: a short list of steps, each pending, doing or done. A model writes it with the plan
// tool, the harness shows it back at the end of every request (the hot tail, which is never cached, so showing it costs the cache nothing),
// and a run that would end with steps open is asked once to finish them or to change the plan. Small models are poor at making a plan and
// worse at keeping to one; the evidence is that a plan the harness holds and repeats helps them more than any other single scaffold
// (docs/LANDSCAPE.md).
package plan

import (
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	Pending = "pending"
	Doing   = "doing"
	Done    = "done"

	MaxSteps    = 12
	MaxStepRune = 160
)

// Item is one step.
type Item struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

// Normalize checks a list a model wrote and returns it cleaned: single-spaced one-line steps, a missing status taken as pending, and at
// most one step doing. The error says what to change.
func Normalize(in []Item) ([]Item, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf(`the plan has no steps: give "items" as a list of {"step": "...", "status": "pending|doing|done"}`)
	}
	if len(in) > MaxSteps {
		return nil, fmt.Errorf("the plan has %d steps; the limit is %d: merge steps that belong together", len(in), MaxSteps)
	}
	out := make([]Item, 0, len(in))
	doing := 0
	for i, it := range in {
		step := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) && !unicode.IsSpace(r) {
				return -1
			}
			return r
		}, it.Step)), " ")
		if step == "" {
			return nil, fmt.Errorf("step %d is empty", i+1)
		}
		if utf8.RuneCountInString(step) > MaxStepRune {
			return nil, fmt.Errorf("step %d is %d characters; the limit is %d: say it shorter", i+1, utf8.RuneCountInString(step), MaxStepRune)
		}
		st := strings.ToLower(strings.TrimSpace(it.Status))
		switch st {
		case "", Pending, "todo", "open":
			st = Pending
		case Doing, "in_progress", "in-progress", "active":
			st = Doing
			doing++
		case Done, "completed", "complete", "finished":
			st = Done
		default:
			return nil, fmt.Errorf("step %d has the status %q: use pending, doing or done", i+1, it.Status)
		}
		out = append(out, Item{Step: step, Status: st})
	}
	if doing > 1 {
		return nil, fmt.Errorf("%d steps are doing; one at a time: mark the others pending or done", doing)
	}
	return out, nil
}

// Open is how many steps are not done.
func Open(items []Item) int {
	n := 0
	for _, it := range items {
		if it.Status != Done {
			n++
		}
	}
	return n
}

// Lines renders a plan as the model reads it: "[x] done step", "[>] the step in hand", "[ ] a step to come".
func Lines(items []Item) string {
	var b strings.Builder
	for i, it := range items {
		mark := "[ ]"
		switch it.Status {
		case Done:
			mark = "[x]"
		case Doing:
			mark = "[>]"
		}
		fmt.Fprintf(&b, "%s %d. %s\n", mark, i+1, it.Step)
	}
	return b.String()
}

// Frame is the plan as the hot tail carries it ("" for no plan): one <live plan> frame, which the agent guards like any other.
func Frame(items []Item) string {
	if len(items) == 0 {
		return ""
	}
	return "<live plan>\nYour plan (the plan tool changes it):\n" + Lines(items) + "</live>"
}

// Store keeps each agent's plan.
type Store struct {
	mu sync.Mutex
	by map[string][]Item
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{by: map[string][]Item{}} }

// Set replaces an agent's plan with a normalized list.
func (s *Store) Set(agent string, items []Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.by[agent] = append([]Item(nil), items...)
}

// Get returns a copy of an agent's plan.
func (s *Store) Get(agent string) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Item(nil), s.by[agent]...)
}

// Open is how many steps of the agent's plan are not done.
func (s *Store) Open(agent string) int { return Open(s.Get(agent)) }
