package tooleval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// TestBuiltinCasesFitTheBuild plays every shipped case on its base prompt: its turns' calls are ones the harness accepts, and
// its rules name tools of the prompt and arguments of their schemas, so a renamed tool or argument fails here, not as a
// pass rate that quietly holds.
func TestBuiltinCasesFitTheBuild(t *testing.T) {
	cs, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) < 8 {
		t.Fatalf("%d builtin cases", len(cs))
	}
	bases := map[string]*core.Prompt{}
	for _, c := range cs {
		if c.Source == "" || c.Failure == "" {
			t.Errorf("%s: say where the failure was seen and what it was", c.ID)
		}
		k := baseKey(c)
		if bases[k] == nil {
			b, err := Base(context.Background(), roleOf(c), c.Team)
			if err != nil {
				t.Fatal(err)
			}
			bases[k] = b
		}
		p, err := Build(bases[k], c, Variant{Name: Current})
		if err != nil {
			t.Errorf("%s: %v", c.ID, err)
			continue
		}
		tools := map[string]core.ToolSpec{}
		for _, tl := range p.Tools {
			tools[tl.Name] = tl
		}
		for _, m := range p.Messages {
			for _, b := range m.Blocks {
				if b.Kind == core.BlockToolUse {
					if why := invalid(b, tools); why != "" {
						t.Errorf("%s: a turn's call: %s", c.ID, why)
					}
				}
			}
		}
		if c.Goal != "" && !promptHas(p, c.Goal) {
			t.Errorf("%s: the goal is not in the prompt", c.ID)
		}
		if c.Board != "" && !promptHas(p, c.Board) {
			t.Errorf("%s: the board is not in the prompt", c.ID)
		}
		for _, r := range append(append(append([]Rule{}, c.Expect.Any...), c.Expect.All...), c.Expect.None...) {
			tl, ok := tools[r.Tool]
			if !ok {
				t.Errorf("%s: rule %s names no tool of the prompt", c.ID, r)
				continue
			}
			var schema struct{ Properties map[string]any }
			_ = json.Unmarshal(tl.InputSchema, &schema)
			for k := range r.Args {
				if _, ok := schema.Properties[k]; !ok {
					t.Errorf("%s: rule %s: %s has no argument %q", c.ID, r, r.Tool, k)
				}
			}
		}
	}
}

func promptHas(p *core.Prompt, s string) bool {
	for _, m := range p.Messages {
		for _, b := range m.Blocks {
			if b.Text == s {
				return true
			}
		}
	}
	return false
}

func TestLoadReadsAFileAnArrayLinesAndADirectory(t *testing.T) {
	dir := t.TempDir()
	one := `{"id":"a","expect":{"min_calls":1}}`
	files := map[string]string{
		"one.json":    one,
		"many.json":   `[{"id":"b","expect":{"min_calls":1}},{"id":"c","expect":{"min_calls":1}}]`,
		"lines.jsonl": "{\"id\":\"d\",\"expect\":{\"min_calls\":1}}\n\n{\"id\":\"e\",\"expect\":{\"min_calls\":1}}\n",
		"skip.txt":    "not a case",
	}
	for n, s := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	if got := strings.Join(ids, ","); got != "d,e,b,c,a" {
		t.Fatalf("ids %s", got)
	}
	cs, err = Load(filepath.Join(dir, "one.json"))
	if err != nil || len(cs) != 1 || cs[0].ID != "a" {
		t.Fatalf("one file: %v %v", cs, err)
	}
}

func TestValidateRefusesASuiteThatWouldMisreport(t *testing.T) {
	cases := map[string]Case{
		"no id":         {Expect: Expect{MinCalls: 1}},
		"asks nothing":  {ID: "x"},
		"two-part turn": {ID: "x", Turns: []Turn{{User: "hi", Assistant: "hello"}}, Expect: Expect{MinCalls: 1}},
		"empty turn":    {ID: "x", Turns: []Turn{{}}, Expect: Expect{MinCalls: 1}},
		"no tool":       {ID: "x", Expect: Expect{Any: []Rule{{}}}},
		"bad regexp":    {ID: "x", Expect: Expect{Any: []Rule{{Tool: "spawn", Args: map[string]any{"task": "("}}}}},
	}
	for name, c := range cases {
		if validate([]Case{c}) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := Case{ID: "x", Expect: Expect{MinCalls: 1}}
	if err := validate([]Case{ok, ok}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a repeated id: %v", err)
	}
	if err := validate([]Case{ok}); err != nil {
		t.Errorf("a good case: %v", err)
	}
}

func TestFilterMatchesAnIdOrItsPrefix(t *testing.T) {
	cs := []Case{{ID: "manager-a"}, {ID: "manager-b"}, {ID: "managers"}, {ID: "solo-a"}}
	var ids []string
	for _, c := range Filter(cs, []string{"manager", "solo-a"}) {
		ids = append(ids, c.ID)
	}
	if got := strings.Join(ids, ","); got != "manager-a,manager-b,solo-a" {
		t.Fatalf("got %s", got)
	}
	if len(Filter(cs, nil)) != 4 {
		t.Fatal("no ids keeps every case")
	}
}
