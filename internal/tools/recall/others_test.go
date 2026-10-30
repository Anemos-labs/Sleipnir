package recall

import (
	"errors"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
)

// agent= reads a teammate's archived turns, and the hint to read a hit names it again; an agent with no turns is refused, and a
// handle stays the caller's own.
func TestRecallReadsATeammate(t *testing.T) {
	tool, env, _ := newTool(t, 4, text)
	if err := tool.Archive.Put("rv-1", core.Turn{ID: 7, Role: core.RoleAssistant, Blocks: []core.Block{core.Text("the retry loop leaks a timer")}}); err != nil {
		t.Fatal(err)
	}
	res := call(t, tool, env, map[string]any{"query": "timer leaks", "agent": "rv-1"})
	if res.IsError || !strings.Contains(res.Text, "t7") || !strings.Contains(res.Text, `agent="rv-1"`) {
		t.Fatalf("search: %+v", res)
	}
	if res := call(t, tool, env, map[string]any{"turns": "t7", "agent": "rv-1"}); res.IsError || !strings.Contains(res.Text, "retry loop") {
		t.Fatalf("turns: %+v", res)
	}
	if res := call(t, tool, env, map[string]any{"query": "x", "agent": "zz-9"}); !res.IsError {
		t.Fatalf("unknown agent: %+v", res)
	}
	if res := call(t, tool, env, map[string]any{"handle": "out_x", "agent": "rv-1"}); !res.IsError {
		t.Fatalf("handle of another: %+v", res)
	}
}

// session= goes through Past, and its errors come back as refusals; without Past it is refused.
func TestRecallReadsAnEarlierSession(t *testing.T) {
	tool, env, _ := newTool(t, 2, text)
	if res := call(t, tool, env, map[string]any{"query": "x", "session": "last"}); !res.IsError {
		t.Fatalf("no Past: %+v", res)
	}
	past := kv.NewArchive(events.NewMemBlobs())
	if err := past.Put("mgr", core.Turn{ID: 3, Role: core.RoleAssistant, Blocks: []core.Block{core.Text("we chose sqlite for restarts")}}); err != nil {
		t.Fatal(err)
	}
	tool.Past = func(session, agent string) (*kv.Archive, string, error) {
		if session != "last" {
			return nil, "", errors.New("no session " + session)
		}
		return past, "mgr", nil
	}
	res := call(t, tool, env, map[string]any{"query": "sqlite", "session": "last"})
	if res.IsError || !strings.Contains(res.Text, "t3") || !strings.Contains(res.Text, `session="last"`) {
		t.Fatalf("search: %+v", res)
	}
	if res := call(t, tool, env, map[string]any{"query": "sqlite", "session": "nope"}); !res.IsError || !strings.Contains(res.Text, "no session nope") {
		t.Fatalf("bad session: %+v", res)
	}
}

// session="all" goes through PastSearch with the query and agent, names each hit's session and how to read it; it needs a
// query, and without PastSearch it is refused.
func TestRecallSearchesAllEarlierSessions(t *testing.T) {
	tool, env, _ := newTool(t, 2, text)
	if res := call(t, tool, env, map[string]any{"query": "x", "session": "all"}); !res.IsError {
		t.Fatalf("no PastSearch: %+v", res)
	}
	var gotAgent string
	tool.PastSearch = func(agent, query string, limit int) ([]kv.Hit, int, error) {
		gotAgent = agent
		if query == "nothing" {
			return nil, 3, nil
		}
		return []kv.Hit{{Scope: "20260101-000000-aaaa", Turn: 4, Score: 2, Snippet: "we chose sqlite for restarts"}}, 3, nil
	}
	res := call(t, tool, env, map[string]any{"query": "sqlite restarts", "session": "all", "agent": "bk-1"})
	if res.IsError || gotAgent != "bk-1" || !strings.Contains(res.Text, "session 20260101-000000-aaaa t4") ||
		!strings.Contains(res.Text, "3 most recent") || !strings.Contains(res.Text, `agent="bk-1"`) {
		t.Fatalf("search: %+v", res)
	}
	if res := call(t, tool, env, map[string]any{"query": "nothing", "session": "all"}); res.IsError || !strings.Contains(res.Text, "no matches in the 3") {
		t.Fatalf("no hits: %+v", res)
	}
	if res := call(t, tool, env, map[string]any{"turns": "t4", "session": "all"}); !res.IsError {
		t.Fatalf("turns with all: %+v", res)
	}
}
