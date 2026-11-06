package plantool

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/plan"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

func run(t *testing.T, tool *Tool, agent, in string) *tools.Result {
	t.Helper()
	res, err := tool.Run(context.Background(), &tools.Call{Name: "plan", Input: []byte(in), Env: &tools.Env{Agent: agent}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestThePlanToolKeepsOnePlanPerAgentAndSaysWhatIsWrong(t *testing.T) {
	st := plan.NewStore()
	tool := New(st)
	if !tool.Spec().ReadOnly {
		t.Error("the plan changes nothing but the plan: it must not ask the person")
	}
	r := run(t, tool, "a1", `{"items":[{"step":"read the test","status":"done"},{"step":"fix it","status":"doing"},{"step":"run go test"}]}`)
	if r.IsError || !strings.Contains(r.Text, "1 of 3 steps done") || !strings.Contains(r.Text, "[>] 2. fix it") {
		t.Fatalf("%+v", r)
	}
	if st.Open("a1") != 2 || st.Open("a2") != 0 {
		t.Errorf("per agent: %d %d", st.Open("a1"), st.Open("a2"))
	}
	for _, bad := range []string{`{}`, `{"items":[]}`, `not json`, `{"items":[{"step":"a","status":"doing"},{"step":"b","status":"doing"}]}`} {
		if r := run(t, tool, "a1", bad); !r.IsError {
			t.Errorf("%s was accepted: %+v", bad, r)
		}
	}
	if st.Open("a1") != 2 {
		t.Error("a refused plan must leave the old one")
	}
}

// A check's advice is added to a saved plan's result, and the plan is kept: an estimate the tool reads as hours weighs the
// progress it reports.
func TestThePlanToolAddsTheCheckAndWeighsTheEstimates(t *testing.T) {
	st := plan.NewStore()
	var seen string
	tool := New(st).WithCheck(func(agent string, items []plan.Item) string {
		seen = agent
		return "no step names milestone M3"
	})
	r := run(t, tool, "mgr", `{"items":[{"step":"api","status":"done","effort":1,"milestone":"M1"},{"step":"ui","effort":"9h","milestone":"m2"}]}`)
	if r.IsError || seen != "mgr" || !strings.Contains(r.Text, "about 10% of 10h estimated") || !strings.HasSuffix(r.Text, "no step names milestone M3") ||
		!strings.Contains(r.Text, "[ ] 2. ui (M2, 9h)") {
		t.Fatalf("%+v", r)
	}
	if got := st.Get("mgr"); len(got) != 2 || got[1].Effort != 9 || got[1].Milestone != "M2" {
		t.Errorf("kept: %+v", got)
	}
}

// A team's manager hands its steps to workers, several at once: its plan may have a step doing for each, which anyone else's may not.
func TestAManagersPlanMayHaveSeveralStepsDoing(t *testing.T) {
	st := plan.NewStore()
	tool := New(st)
	in := `{"items":[{"step":"store","status":"doing"},{"step":"web ui","status":"doing"},{"step":"ci","status":"doing"},{"step":"review"}]}`
	call := func(agent, role string) *tools.Result {
		res, err := tool.Run(context.Background(), &tools.Call{Name: "plan", Input: []byte(in), Env: &tools.Env{Agent: agent, Role: role}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if r := call("mgr", ManagerRole); r.IsError || st.Open("mgr") != 4 {
		t.Fatalf("the manager's plan of three steps in hand: %+v", r)
	}
	if r := call("be-1", "backend"); !r.IsError || !strings.Contains(r.Text, "3 steps are doing") {
		t.Fatalf("a worker's plan of three steps in hand: %+v", r)
	}
	if items, err := plan.NormalizeDelegated(st.Get("mgr")); err != nil || len(items) != 4 {
		t.Fatalf("the saved plan does not replay: %v %v", items, err)
	}
}
