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
	if r.IsError || !strings.Contains(r.Text, "1 of 3 done") || !strings.Contains(r.Text, "[>] 2. fix it") {
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
