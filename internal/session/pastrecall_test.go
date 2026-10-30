package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// writePastSession writes a session log under home with turns of main and bk-1, and a snapshot of main so it can be resumed.
func writePastSession(t *testing.T, home, id, root string) {
	t.Helper()
	dir := filepath.Join(home, ".sleipnir", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	seq := uint64(0)
	line := func(typ, agent string, data any) {
		raw, _ := json.Marshal(data)
		seq++
		e, _ := json.Marshal(events.Event{Seq: seq, Session: id, Agent: agent, Type: typ, Data: raw})
		b.Write(append(e, '\n'))
	}
	line(events.TypeSessionStart, "", map[string]any{"root": root})
	line(events.TypeTurnAppend, soloAgent, core.Turn{ID: 1, Role: core.RoleUser, Blocks: []core.Block{core.Text("why did we pick sqlite for the cache")}})
	line(events.TypeTurnAppend, soloAgent, core.Turn{ID: 2, Role: core.RoleAssistant, Blocks: []core.Block{core.Text("sqlite was chosen because the cache must survive restarts")}})
	line(events.TypeTurnAppend, "bk-1", core.Turn{ID: 1, Role: core.RoleAssistant, Blocks: []core.Block{core.Text("the migration needs a lock")}})
	line(events.TypeAgentSnapshot, soloAgent, map[string]any{})
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// recall session=… reads an earlier session of the same project: last, or by id, the person's agent by default or a named one;
// this session, another project's session and a path are refused, and nothing is written into the earlier session's folder.
func TestPastArchiveReadsEarlierSessionsOfThisProject(t *testing.T) {
	t.Setenv("SLEIPNIR_HOME", "")
	home, root := t.TempDir(), t.TempDir()
	writePastSession(t, home, "20260101-000000-aaaa", root)
	writePastSession(t, home, "20260102-000000-bbbb", t.TempDir()) // another project, newer
	s := &Session{ID: "20260103-000000-cccc", opts: Options{Home: home, Root: root}}

	ar, who, err := s.pastArchive("last", "")
	if err != nil || who != soloAgent {
		t.Fatalf("last: %v %q", err, who)
	}
	if hits := ar.Search(who, "sqlite restarts", 4); len(hits) == 0 || hits[0].Turn != 2 {
		t.Fatalf("search: %+v", hits)
	}
	ar, who, err = s.pastArchive("20260101-000000-aaaa", "bk-1")
	if err != nil || who != "bk-1" || len(ar.Search(who, "lock", 4)) != 1 {
		t.Fatalf("by id and agent: %v %q", err, who)
	}
	if _, _, err := s.pastArchive("20260101-000000-aaaa", "fe-9"); err == nil || !strings.Contains(err.Error(), "bk-1, main") {
		t.Fatalf("unknown agent: %v", err)
	}
	for _, bad := range []string{"20260102-000000-bbbb", "../x", s.ID, ""} {
		if _, _, err := s.pastArchive(bad, ""); err == nil {
			t.Fatalf("%q was read", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".sleipnir", "sessions", "20260101-000000-aaaa", "blobs")); !os.IsNotExist(err) {
		t.Fatalf("reading wrote into the earlier session: %v", err)
	}
}

// recall session="all" searches the earlier sessions of this project together, the person's agent by default or a named one,
// and never another project's sessions.
func TestSearchPastSearchesEveryEarlierSessionOfThisProject(t *testing.T) {
	t.Setenv("SLEIPNIR_HOME", "")
	home, root := t.TempDir(), t.TempDir()
	writePastSession(t, home, "20260101-000000-aaaa", root)
	writePastSession(t, home, "20260102-000000-bbbb", t.TempDir()) // another project
	writePastSession(t, home, "20260103-000000-cccc", root)
	s := &Session{ID: "20260104-000000-dddd", opts: Options{Home: home, Root: root}}

	hits, n, err := s.searchPast("", "sqlite restarts", 8)
	if err != nil || n != 2 || len(hits) != 4 {
		t.Fatalf("searchPast: %v, %d sessions, %+v", err, n, hits)
	}
	scopes := map[string]bool{}
	for _, h := range hits[:2] {
		if h.Turn != 2 {
			t.Fatalf("the turn with both words should lead: %+v", hits)
		}
		scopes[h.Scope] = true
	}
	if !scopes["20260101-000000-aaaa"] || !scopes["20260103-000000-cccc"] {
		t.Fatalf("scopes: %+v", hits)
	}
	if hits, _, _ := s.searchPast("bk-1", "migration lock", 8); len(hits) != 2 || hits[0].Snippet == "" {
		t.Fatalf("by agent: %+v", hits)
	}
	if hits, _, _ := s.searchPast("bk-1", "sqlite", 8); len(hits) != 0 {
		t.Fatalf("another agent's turns were searched: %+v", hits)
	}
	lone := &Session{ID: "20260104-000000-eeee", opts: Options{Home: home, Root: t.TempDir()}}
	if _, _, err := lone.searchPast("", "sqlite", 8); err == nil {
		t.Fatal("a project without earlier sessions should say so")
	}
}
