package tooleval

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// scripted answers by the prompt: it waits when the wait tool's description says so, and spawns otherwise. Its first
// request fails as overloaded when flaky is set.
type scripted struct {
	mu       sync.Mutex
	n        int
	flaky    bool
	hang     bool
	maxSeen  int
	gotModel string
}

func (s *scripted) Profile() provider.Profile { return provider.Profile{Name: "scripted"} }

func (s *scripted) Do(ctx context.Context, req *provider.Request, _ func(provider.Event)) (*provider.Response, error) {
	s.mu.Lock()
	s.n++
	n := s.n
	s.maxSeen = req.Prompt.Params.MaxTokens
	s.gotModel = req.Prompt.Model
	s.mu.Unlock()
	if s.hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.flaky && n == 1 {
		return nil, &provider.Error{Kind: provider.ErrOverloaded, Message: "busy"}
	}
	tool, args := "spawn", `{"task":"T23"}`
	for _, t := range req.Prompt.Tools {
		if t.Name == "wait" && strings.Contains(t.Description, "Use wait") {
			tool, args = "wait", `{}`
		}
	}
	usd := 0.001
	return &provider.Response{CostUSD: &usd, Turn: core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{core.ToolUse("t1", tool, json.RawMessage(args))}}}, nil
}

func fakeBase(context.Context, string, []string) (*core.Prompt, error) { return testBase(), nil }

var queued = Case{ID: "queued", Source: "a run", Failure: "spawned a queued task", Expect: Expect{
	Any:  []Rule{{Tool: "wait"}},
	None: []Rule{{Tool: "spawn", Args: map[string]any{"task": "^T23$"}, Why: "queued"}},
}}

func TestRunComparesVariantsAndTheirSize(t *testing.T) {
	p := &scripted{flaky: true}
	m := Model{Name: "fake", Provider: p, Model: cost.Model{ID: "fake-1", MaxOutput: 4096}}
	variants := []Variant{{Name: Current}, {Name: "explicit", Tools: map[string]string{"wait": "Sleep until something changes. Use wait when a task is queued."}}}
	var seen int
	r, err := Run(context.Background(), []Model{m}, []Case{queued}, variants, Options{Samples: 2, Base: fakeBase, OnSample: func(Sample) { seen++ }})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 4 || len(r.Samples) != 4 {
		t.Fatalf("%d samples seen, %d kept", seen, len(r.Samples))
	}
	cur, exp := r.Variants[0], r.Variants[1]
	if cur.Passed != 0 || cur.Asked != 2 || exp.Passed != 2 || exp.Asked != 2 || cur.Errors != 0 {
		t.Fatalf("variants %+v", r.Variants)
	}
	if exp.Bytes <= cur.Bytes || exp.Tools["wait"] <= cur.Tools["wait"] || exp.Tools["spawn"] != cur.Tools["spawn"] {
		t.Fatalf("sizes %+v", r.Variants)
	}
	c := r.Cases[0]
	if c.Passed[Current] != 0 || c.Passed["explicit"] != 2 || !strings.Contains(c.Why[Current], "queued") {
		t.Fatalf("case %+v", c)
	}
	if p.maxSeen != 4096 || p.gotModel != "fake-1" {
		t.Fatalf("the request asked for %d tokens of %q", p.maxSeen, p.gotModel)
	}
	if r.USD < 0.0039 || r.USD > 0.0041 {
		t.Fatalf("cost %v", r.USD)
	}
	var out bytes.Buffer
	Print(&out, r)
	for _, want := range []string{"current", "explicit", "2/2", "0/2", "[a run]", "queued", "cost: $0.0040"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the report has no %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	PrintSizes(&out, r)
	if !strings.Contains(out.String(), "wait") || !strings.Contains(out.String(), "total") {
		t.Errorf("sizes:\n%s", out.String())
	}
}

func TestRunCountsAnUnansweredSampleApart(t *testing.T) {
	m := Model{Name: "slow", Provider: &scripted{hang: true}, Model: cost.Model{ID: "slow"}}
	r, err := Run(context.Background(), []Model{m}, []Case{queued}, nil, Options{Samples: 1, Timeout: 50 * time.Millisecond, Base: fakeBase})
	if err != nil {
		t.Fatal(err)
	}
	v := r.Variants[0]
	if v.Errors != 1 || v.Asked != 0 || !strings.Contains(r.Samples[0].Error, "no answer within") {
		t.Fatalf("%+v %+v", v, r.Samples)
	}
}

func TestRunRefusesAVariantNamingNoTool(t *testing.T) {
	m := Model{Name: "fake", Provider: &scripted{}, Model: cost.Model{ID: "fake"}}
	_, err := Run(context.Background(), []Model{m}, []Case{queued}, []Variant{{Name: "typo", Tools: map[string]string{"wiat": "x"}}}, Options{Base: fakeBase})
	if err == nil || !strings.Contains(err.Error(), "wiat") {
		t.Fatalf("got %v", err)
	}
}
