package tooleval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// reader reads a file that is not there on its first turn, and stops on its second.
type reader struct {
	mu sync.Mutex
	n  int
}

func (r *reader) Profile() provider.Profile { return provider.Profile{Name: "reader"} }

func (r *reader) Do(context.Context, *provider.Request, func(provider.Event)) (*provider.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	t := core.Turn{Role: core.RoleAssistant}
	stop := core.StopEnd
	if r.n == 1 {
		t.Blocks = []core.Block{core.Text("looking"), core.ToolUse("t1", "read", json.RawMessage(`{"path":"missing.go"}`))}
		stop = core.StopToolUse
	} else {
		t.Blocks = []core.Block{core.Text("done")}
	}
	return &provider.Response{Turn: t, Stop: stop, Usage: core.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

func TestHarvestReplaysTheRequestOfAFailedCall(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "README.md"), []byte("# p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := cost.Model{ID: "reader", ContextTokens: 100_000, MaxOutput: 4096}
	s, err := session.New(context.Background(), session.Options{
		Cwd: ws, Root: ws, Home: t.TempDir(), Dir: t.TempDir(), Provider: &reader{}, ModelInfo: &m, Model: m.ID,
		Mode: perm.ModeYolo, NoWeb: true, NoMCP: true, NoMemory: true, TrustProject: true, Offline: true, TrainingLogs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Run(context.Background(), "read missing.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cs, err := Harvest(res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 {
		t.Fatalf("%d cases: %+v", len(cs), cs)
	}
	c := cs[0]
	if c.Role != "solo" || c.Prompt == nil || !strings.Contains(c.Source, "request") || !strings.Contains(c.Failure, "missing.go") {
		t.Fatalf("case %+v", c)
	}
	if err := validate(cs); err != nil {
		t.Fatal(err)
	}
	// the recorded prompt is the one that made the call: its last message is the person's, and the call it made fails the case
	last := c.Prompt.Messages[len(c.Prompt.Messages)-1]
	if last.Role != core.RoleUser {
		t.Fatalf("the prompt ends with %s", last.Role)
	}
	p, err := Build(nil, c, Variant{Name: Current})
	if err != nil {
		t.Fatal(err)
	}
	again := []core.Block{core.ToolUse("x", "read", json.RawMessage(`{"path":"missing.go"}`))}
	if ok, why := Check(again, p.Tools, c.Expect); ok || !strings.Contains(why, "the call failed") {
		t.Fatalf("the same call: %v %q", ok, why)
	}
	other := []core.Block{core.ToolUse("x", "glob", json.RawMessage(`{"pattern":"*.go"}`))}
	if ok, why := Check(other, p.Tools, c.Expect); !ok {
		t.Fatalf("another call: %q", why)
	}
}

func TestRoleOfAgent(t *testing.T) {
	for id, want := range map[string]string{"mgr": "manager", "main": "solo", "be-1": "be", "fe-12": "fe", "judge": "judge"} {
		if got := roleOfAgent(id); got != want {
			t.Errorf("%s: %s", id, got)
		}
	}
}

func TestExactArgsMatchTheCallTheyCameFrom(t *testing.T) {
	in := json.RawMessage(`{"command":"echo 'a.b' > (x)","n":3,"ok":true,"files":["a/*.go","b"]}`)
	r := Rule{Tool: "bash", Args: exactArgs(in)}
	if !matches(r, core.ToolUse("x", "bash", in)) {
		t.Fatalf("the rule %s does not match its call", r)
	}
	if matches(r, core.ToolUse("x", "bash", json.RawMessage(`{"command":"echo 'aXb' > (x)","n":3,"ok":true,"files":["a/*.go","b"]}`))) {
		t.Fatal("a regexp character in the call was not taken literally")
	}
}
