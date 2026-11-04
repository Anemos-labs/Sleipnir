// Package tooleval measures how well a model uses the harness's tools in the situations where models went wrong before.
//
// A case is one such situation, a real failure turned into a test. Its context is the role's actual first prompt in a team
// session of this build (Base: the system text and every tool spec), then the case's turns: what the agent had done, what the
// tools answered and what the harness showed it. Its expectation says what the model's next response must and must not do.
// A case can also carry the exact prompt of a recorded request (Harvest takes them from a session kept with
// --training-logs), so a failure seen in a run can be replayed as it happened.
//
// Each case is asked a few times, for each variant of the tool descriptions, and the report gives the pass rate of each
// variant beside the size of its descriptions: a description can then be rewritten, or cut, and the change kept only when
// the pass rate holds. The cases name the run they came from, so a pass rate that falls says which lesson was lost.
package tooleval

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// Case is one situation a model is asked to act in, and what its next response must do.
type Case struct {
	ID string `json:"id"`
	// Role is the role whose base prompt the turns are played on: manager (the default), solo, or a worker role.
	Role string `json:"role,omitempty"`
	// Team names the role of each worker slot of the session, a role as often as it has slots (two backend slots make a
	// backend pool); without it the team is the default one, a slot per role.
	Team []string `json:"team,omitempty"`
	// Source names where the failure was seen (a run's notes, a session and request), and Failure says what went wrong
	// there: what a falling pass rate has lost.
	Source  string `json:"source,omitempty"`
	Failure string `json:"failure,omitempty"`
	// Goal replaces the base prompt's objective; Board replaces its live board view.
	Goal  string `json:"goal,omitempty"`
	Board string `json:"board,omitempty"`
	// Turns follow the base messages, in order.
	Turns []Turn `json:"turns,omitempty"`
	// Prompt is a recorded prompt played as it was (Harvest): the base, Goal, Board and Turns are not used.
	Prompt *core.Prompt `json:"prompt,omitempty"`
	Expect Expect       `json:"expect"`
}

// Turn is one step of a case's history. Exactly one of its parts is set: a user message (harness text: a live board, a
// notice, mail), an assistant turn (its text and its calls), or the results of the assistant's last calls, in order.
type Turn struct {
	User      string   `json:"user,omitempty"`
	Assistant string   `json:"assistant,omitempty"`
	Calls     []Call   `json:"calls,omitempty"`
	Results   []Result `json:"results,omitempty"`
}

// Call is a tool call of an assistant turn.
type Call struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Result is what a tool answered.
type Result struct {
	Text  string `json:"text"`
	Error bool   `json:"error,omitempty"`
}

// Expect is what the next response must do. Every call must name a tool of the prompt with arguments its schema accepts,
// whatever the rules say; a response with no call passes only when MinCalls is 0 and no Any or All rule asks for one.
type Expect struct {
	// Any: at least one call matches one of these rules.
	Any []Rule `json:"any,omitempty"`
	// All: every one of these rules is matched by some call.
	All []Rule `json:"all,omitempty"`
	// None: no call matches any of these rules.
	None []Rule `json:"none,omitempty"`
	// MinCalls and MaxCalls bound the number of calls (MaxCalls 0: no bound).
	MinCalls int `json:"min_calls,omitempty"`
	MaxCalls int `json:"max_calls,omitempty"`
}

// Rule matches a call: its tool, and arguments of which every one named matches. A string is a regular expression the
// argument (a string, or a number or bool as JSON) must match; an array matches an array argument that holds a match for
// each element; anything else must equal the argument as JSON. A missing argument matches only the regular expression "^$".
// Why says what the rule guards against, for the report.
type Rule struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
	Why  string         `json:"why,omitempty"`
}

// String is the rule as the report names it.
func (r Rule) String() string {
	if len(r.Args) == 0 {
		return r.Tool
	}
	keys := make([]string, 0, len(r.Args))
	for k := range r.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		b, _ := json.Marshal(r.Args[k])
		parts = append(parts, k+"="+string(b))
	}
	return r.Tool + "(" + strings.Join(parts, " ") + ")"
}

//go:embed cases/*.json
var builtin embed.FS

// Builtin is the suite shipped with this build: the failures of real team runs, each with the run it came from.
func Builtin() ([]Case, error) {
	return loadFS(builtin, "cases")
}

// Load reads cases from a file (a JSON case, a JSON array of cases, or JSON lines) or from every .json and .jsonl file of a
// directory, in name order.
func Load(path string) ([]Case, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return loadFS(os.DirFS(path), ".")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cs, err := parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cs, validate(cs)
}

// loadFS reads every case file of dir in fsys.
func loadFS(fsys fs.FS, dir string) ([]Case, error) {
	ents, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []Case
	for _, e := range ents {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || (ext != ".json" && ext != ".jsonl") {
			continue
		}
		b, err := fs.ReadFile(fsys, filepath.ToSlash(filepath.Join(dir, e.Name())))
		if err != nil {
			return nil, err
		}
		cs, err := parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out = append(out, cs...)
	}
	return out, validate(out)
}

// parse reads one case, an array of cases, or one case per line.
func parse(b []byte) ([]Case, error) {
	t := strings.TrimSpace(string(b))
	switch {
	case t == "":
		return nil, nil
	case strings.HasPrefix(t, "["):
		var cs []Case
		return cs, json.Unmarshal([]byte(t), &cs)
	}
	var one Case
	if err := json.Unmarshal([]byte(t), &one); err == nil {
		return []Case{one}, nil
	}
	var out []Case
	for i, line := range strings.Split(t, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		var c Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// validate refuses a suite a run would misreport: a case without an id, two with one id, a turn that is not one thing, or a
// case that asks nothing of the response.
func validate(cs []Case) error {
	seen := map[string]bool{}
	var errs []error
	for _, c := range cs {
		switch {
		case c.ID == "":
			errs = append(errs, errors.New("a case has no id"))
			continue
		case seen[c.ID]:
			errs = append(errs, fmt.Errorf("case %s: the id is used twice", c.ID))
		}
		seen[c.ID] = true
		e := c.Expect
		if len(e.Any)+len(e.All)+len(e.None) == 0 && e.MinCalls == 0 && e.MaxCalls == 0 {
			errs = append(errs, fmt.Errorf("case %s: expect asks nothing", c.ID))
		}
		for i, t := range c.Turns {
			n := 0
			if t.User != "" {
				n++
			}
			if t.Assistant != "" || len(t.Calls) > 0 {
				n++
			}
			if len(t.Results) > 0 {
				n++
			}
			if n != 1 {
				errs = append(errs, fmt.Errorf("case %s: turn %d must be one of user, assistant (with calls) or results", c.ID, i+1))
			}
		}
		for _, r := range append(append(append([]Rule{}, e.Any...), e.All...), e.None...) {
			if r.Tool == "" {
				errs = append(errs, fmt.Errorf("case %s: a rule names no tool", c.ID))
			}
			if _, err := compileArgs(r.Args); err != nil {
				errs = append(errs, fmt.Errorf("case %s: rule %s: %w", c.ID, r.Tool, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Filter keeps the cases whose id is one of ids or starts with one of them followed by "-" (all when ids is empty).
func Filter(cs []Case, ids []string) []Case {
	if len(ids) == 0 {
		return cs
	}
	var out []Case
	for _, c := range cs {
		for _, id := range ids {
			if c.ID == id || strings.HasPrefix(c.ID, id+"-") {
				out = append(out, c)
				break
			}
		}
	}
	return out
}
