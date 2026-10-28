package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
)

// A manager's file edit while writer roles have free slots says once for each top-level directory that work bigger than a
// handoff is a task for one of them. A manager with seven free slots wrote two of four independent modules itself and ran the
// other two one after the other on one backend member.
func TestAManagersEditInAModuleNamesTheFreeWriterSlots(t *testing.T) {
	r := newRVRig(t, Config{MaxWorkers: 8, Team: []string{"backend", "frontend", "tester", "reviewer"}},
		func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	h := &holdHooks{s: r.sw, m: r.sw.get("mgr")}
	edit := func(path string) string {
		in, _ := json.Marshal(map[string]string{"path": path, "content": "package x"})
		o := h.BeforeTool(context.Background(), agent.ToolHookCall{Agent: "mgr", Role: "manager", Tool: "write", Input: in})
		if o.Veto {
			t.Fatalf("the edit was vetoed: %s", o.Reason)
		}
		return o.Context
	}
	got := edit("wordfreq/wordfreq.go")
	if !strings.Contains(got, "Writer slots are free (backend, frontend)") || !strings.Contains(got, "Work in wordfreq/ bigger than a handoff") {
		t.Fatalf("the first edit in wordfreq/ carried %q", got)
	}
	if got := edit("wordfreq/extra_test.go"); got != "" {
		t.Fatalf("a second edit in the same module carried %q", got)
	}
	if got := edit("jsonpath/jsonpath.go"); !strings.Contains(got, "Work in jsonpath/") {
		t.Fatalf("the first edit in another module carried %q", got)
	}

	solo := newRVRig(t, Config{MaxWorkers: 8}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} })
	if _, err := solo.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	sh := &holdHooks{s: solo.sw, m: solo.sw.get("mgr")}
	in, _ := json.Marshal(map[string]string{"path": "lru/lru.go"})
	if o := sh.BeforeTool(context.Background(), agent.ToolHookCall{Agent: "mgr", Role: "manager", Tool: "edit", Input: in}); o.Context != "" {
		t.Fatalf("a swarm without a team roster carried %q", o.Context)
	}
}
