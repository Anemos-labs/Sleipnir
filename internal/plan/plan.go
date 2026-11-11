// Package plan is the plan an agent keeps for a task: a short list of steps, each pending, doing or done. A model writes it with the plan
// tool, the harness shows it back at the end of every request (the hot tail, which is never cached, so showing it costs the cache nothing),
// and a run that would end with steps open is asked once to finish them or to change the plan. Small models are poor at making a plan and
// worse at keeping to one; the evidence is that a plan the harness holds and repeats helps them more than any other single scaffold
// (docs/LANDSCAPE.md).
package plan

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	Pending = "pending"
	Doing   = "doing"
	Done    = "done"

	// MaxSteps and MaxStepRune are ceilings for memory only, far above what a plan needs: up to 10000 steps of up to 16384
	// characters, each kept as the agent wrote it. A team's manager held to 24 steps of 160 characters spent its first calls
	// cutting its plan down.
	MaxSteps    = 10000
	MaxStepRune = 1 << 14
	// MaxEffort is the largest estimate a step keeps (hours); a larger one is read as this.
	MaxEffort = 200
	// MaxMilestoneRune bounds the milestone id a step names.
	MaxMilestoneRune = 8
)

// Item is one step. Effort is the agent's estimate of the step's work in hours (0: none given); Milestone is the id of the goal
// milestone the step serves ("M3", "" for none). Both weigh the step in the plan's progress (Progress).
type Item struct {
	Step      string `json:"step"`
	Status    string `json:"status"`
	Effort    Hours  `json:"effort,omitempty"`
	Milestone string `json:"milestone,omitempty"`
}

// Hours is an effort estimate in hours. It reads a JSON number or a string a model writes for one ("3", "3h", "90m", "1.5 hours",
// "2d" for two working days of 8 hours); one it cannot read is no estimate, never a refused plan.
type Hours float64

// UnmarshalJSON implements json.Unmarshaler.
func (h *Hours) UnmarshalJSON(b []byte) error {
	*h = 0
	var n float64
	if json.Unmarshal(b, &n) == nil {
		*h = Hours(n)
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	*h = parseHours(s)
	return nil
}

// hoursText is an estimate as a model writes it: a number and an optional unit.
var hoursText = regexp.MustCompile(`(?i)^~?\s*(\d+(?:\.\d+)?)\s*(m|min|mins|minutes?|h|hr|hrs|hours?|d|days?)?\.?$`)

func parseHours(s string) Hours {
	m := hoursText.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	switch u := strings.ToLower(m[2]); {
	case strings.HasPrefix(u, "m"):
		n /= 60
	case strings.HasPrefix(u, "d"):
		n *= 8
	}
	return Hours(n)
}

// milestoneID is a milestone id as a model writes it ("M3", "m3", "3", "Milestone 3", "**M3**").
var milestoneID = regexp.MustCompile(`(?i)^\**\s*(?:m|milestone\s*)?(\d{1,3})\b`)

// normMilestone is the id a step names, as the goal numbers its milestones ("M3"): a form ParseMilestones would number is
// rewritten, anything else kept as written (upper case, at most MaxMilestoneRune runes).
func normMilestone(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if m := milestoneID.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return "M" + strconv.Itoa(n)
	}
	s = strings.ToUpper(strings.Join(strings.Fields(s), ""))
	if r := []rune(s); len(r) > MaxMilestoneRune {
		s = string(r[:MaxMilestoneRune])
	}
	return s
}

// Normalize checks a list a model wrote and returns it cleaned: single-spaced one-line steps, a missing status taken as pending, and at
// most one step doing. The error says what to change.
func Normalize(in []Item) ([]Item, error) { return normalize(in, true) }

// NormalizeDelegated is Normalize for the plan of an agent that hands its steps to others, a team's manager: any number of steps
// may be doing at once, one for each that a worker is on. Held to one, a manager with four workers on four steps was refused ("4
// steps are doing; one at a time") and could only mark finished work done or running work pending. A replay of a saved plan
// (resume, the web view) uses it too, since the tool already judged what it saved.
func NormalizeDelegated(in []Item) ([]Item, error) { return normalize(in, false) }

// NormalizeSaved is for a replay of a plan the tool accepted before (resume, the web view): an older version shortened a step
// over its limit and kept the plan, so a saved step over MaxStepRune is cut to it, and steps past MaxSteps are dropped, rather
// than the saved plan lost. delegated is NormalizeDelegated's rule on doing steps.
func NormalizeSaved(in []Item, delegated bool) ([]Item, error) {
	if len(in) > MaxSteps {
		in = in[:MaxSteps]
	}
	clipped := make([]Item, len(in))
	for i, it := range in {
		if r := []rune(it.Step); len(r) > MaxStepRune {
			it.Step = string(r[:MaxStepRune-1]) + "…"
		}
		clipped[i] = it
	}
	return normalize(clipped, !delegated)
}

func normalize(in []Item, oneDoing bool) ([]Item, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf(`the plan has no steps: give "items" as a list of {"step": "...", "status": "pending|doing|done"}`)
	}
	if len(in) > MaxSteps {
		return nil, fmt.Errorf("the plan has %d steps, over the %d-step ceiling", len(in), MaxSteps)
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
		if n := utf8.RuneCountInString(step); n > MaxStepRune {
			return nil, fmt.Errorf("step %d is %d characters, over the %d-character ceiling", i+1, n, MaxStepRune)
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
		e := float64(it.Effort)
		switch {
		case math.IsNaN(e) || e < 0:
			e = 0
		case e > MaxEffort:
			e = MaxEffort
		}
		out = append(out, Item{Step: step, Status: st, Effort: Hours(math.Round(e*100) / 100), Milestone: normMilestone(it.Milestone)})
	}
	if oneDoing && doing > 1 {
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
		fmt.Fprintf(&b, "%s %d. %s%s\n", mark, i+1, it.Step, Tag(it))
	}
	return b.String()
}

// Tag is what a line of the plan says beside a step's text: its milestone and its estimate (" (M3, 4h)"), "" for a step with
// neither.
func Tag(it Item) string {
	var parts []string
	if it.Milestone != "" {
		parts = append(parts, it.Milestone)
	}
	if it.Effort > 0 {
		parts = append(parts, FormatHours(float64(it.Effort)))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// FormatHours is an estimate as a person reads it: "45m", "3h", "1.5h".
func FormatHours(h float64) string {
	if h < 1 {
		return strconv.Itoa(int(math.Max(1, math.Round(h*60)))) + "m"
	}
	return strconv.FormatFloat(math.Round(h*10)/10, 'f', -1, 64) + "h"
}

// Mark is what the judge has ruled on one milestone of a goal: Ruled says whether it has ruled on the goal's milestones yet.
type Mark struct {
	ID    string
	Met   bool
	Ruled bool
}

// Weighed is how far a plan is, in the plan's own estimate (hours, or steps when no step has one): Done is the work of the steps
// done, Claimed the part of the steps marked done that the judge has ruled unmet (counted open), and Total all of it, with a share
// for each milestone that no step names (Missing). Hours says whether the weights are estimates in hours.
type Weighed struct {
	Done, Claimed, Total float64
	Missing              []string
	Hours                bool
}

// Pct is Done as a share of Total, from 0 to 100.
func (w Weighed) Pct() int {
	if w.Total <= 0 {
		return 0
	}
	return int(math.Floor(100 * w.Done / w.Total))
}

// Progress weighs a plan against its goal's milestones (ms; none for a plan without a goal). Each step weighs its estimate; a
// step without one weighs the mean of those that have one (one step, when none has). A done step counts as done unless it serves a
// milestone the judge has ruled unmet: the judge's ruling wins over the agent's mark, which is the step's Claimed share. A
// milestone no step names adds an open share of the mean weight, so a plan that leaves out part of the goal does not show the goal
// nearer its end than it is.
func Progress(items []Item, ms []Mark) Weighed {
	var w Weighed
	sum, n := 0.0, 0
	for _, it := range items {
		if it.Effort > 0 {
			sum += float64(it.Effort)
			n++
		}
	}
	mean := 1.0
	if n > 0 {
		mean, w.Hours = sum/float64(n), true
	}
	byID := make(map[string]Mark, len(ms))
	for _, m := range ms {
		byID[m.ID] = m
	}
	named := map[string]bool{}
	for _, it := range items {
		e := float64(it.Effort)
		if e <= 0 {
			e = mean
		}
		w.Total += e
		named[it.Milestone] = true
		if it.Status != Done {
			continue
		}
		if m, ok := byID[it.Milestone]; ok && m.Ruled && !m.Met {
			w.Claimed += e
			continue
		}
		w.Done += e
	}
	for _, m := range ms {
		if !named[m.ID] && !m.Met {
			w.Missing = append(w.Missing, m.ID)
			w.Total += mean
		}
	}
	return w
}

// Summary is a plan's progress in one line: "3 of 14 steps done, about 20% of 41h estimated".
func Summary(items []Item, ms []Mark) string {
	w := Progress(items, ms)
	s := fmt.Sprintf("%d of %d steps done", len(items)-Open(items), len(items))
	if w.Hours {
		s += fmt.Sprintf(", about %d%% of %s estimated", w.Pct(), FormatHours(w.Total))
	}
	return s
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
