package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeLog writes a session log of events (type, agent, data) at times from t0, one second apart, and sets the directory's and the
// log's modification time to mod.
func writeLog(t *testing.T, dir string, t0, mod time.Time, evs ...[3]any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i, e := range evs {
		data, _ := json.Marshal(e[2])
		line, _ := json.Marshal(map[string]any{"seq": i + 1, "ts": t0.Add(time.Duration(i) * time.Second), "type": e[0], "agent": e[1], "data": json.RawMessage(data)})
		b.Write(line)
		b.WriteByte('\n')
	}
	log := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(log, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{log, dir} {
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStateRootIsTheOneDefinition(t *testing.T) {
	t.Setenv("SLEIPNIR_HOME", "")
	if got := StateRoot("/home/x"); got != filepath.Join("/home/x", ".sleipnir") {
		t.Errorf("StateRoot = %q", got)
	}
	if got, want := StateRoot("/home/x"), stateRoot("/home/x"); got != want {
		t.Errorf("StateRoot %q differs from the session's own %q", got, want)
	}
	t.Setenv("SLEIPNIR_HOME", "/state")
	if got := StateRoot("/home/x"); got != "/state" {
		t.Errorf("SLEIPNIR_HOME is the state directory: %q", got)
	}
	if got := SessionsDir(""); got != filepath.Join("/state", "sessions") {
		t.Errorf("SessionsDir = %q", got)
	}
}

func TestSummarizeReadsWhatAListingShows(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	writeLog(t, dir, t0, t0,
		[3]any{"session.start", "", map[string]any{"model": "m-1", "cwd": "/p/sub", "root": "/p"}},
		[3]any{"user.input", "mgr", map[string]any{"text": "do the task", "origin": "task"}},
		[3]any{"user.input", "mgr", map[string]any{"text": "   \n "}},
		[3]any{"user.input", "mgr", map[string]any{"text": "  fix\tthe   failing\n test  "}},
		[3]any{"user.input", "mgr", map[string]any{"text": "second prompt"}},
		[3]any{"agent.spawn", "be-1", map[string]any{}},
		[3]any{"session.end", "", map[string]any{"cost_usd": 0.5, "reason": "exit"}},
		[3]any{"session.start", "", map[string]any{"model": "m-2"}},
		[3]any{"session.end", "", map[string]any{"cost_usd": 1.25, "reason": "interrupted"}},
	)
	s := Summarize(filepath.Join(dir, "events.jsonl"))
	if s.Model != "m-2" || s.CostUSD != 1.25 {
		t.Errorf("the last run's model and cost: %q %v", s.Model, s.CostUSD)
	}
	if s.First != "fix the failing test" {
		t.Errorf("the first prompt a person typed, on one line: %q", s.First)
	}
	if s.Cwd != "/p/sub" || s.Root != "/p" || s.Agents != 2 || s.EndReason != "interrupted" || s.Open {
		t.Errorf("summary: %+v", s)
	}
	if !s.Started.Equal(t0) || !s.Last.Equal(t0.Add(8*time.Second)) {
		t.Errorf("times: %v %v", s.Started, s.Last)
	}
	if s := Summarize(filepath.Join(dir, "none.jsonl")); !reflect.DeepEqual(s, Summary{}) {
		t.Errorf("a log that cannot be read: %+v", s)
	}
}

func TestIntegrationIsReadFromTheLog(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	writeLog(t, dir, t0, t0,
		[3]any{"session.start", "", map[string]any{"model": "m"}},
		[3]any{"session.isolation", "", map[string]any{"version": 1, "base": "abc123", "commit": false}},
		[3]any{"swarm.integration", "", map[string]any{"branch": "sleipnir/x/_integration", "applied": false, "reason": "the checkout has changes of its own"}},
	)
	in := Summarize(filepath.Join(dir, "events.jsonl")).Integration
	if in == nil || in.Applied || !strings.Contains(in.Message, "NOT applied") || in.Hint != "git diff --binary abc123 sleipnir/x/_integration | git apply --3way" {
		t.Fatalf("not applied: %+v", in)
	}
	writeLog(t, dir, t0, t0,
		[3]any{"session.isolation", "", map[string]any{"version": 1, "base": "abc123", "commit": true}},
		[3]any{"swarm.integration", "", map[string]any{"branch": "b", "applied": true, "committed": true}},
	)
	if in := Summarize(filepath.Join(dir, "events.jsonl")).Integration; in == nil || !in.Applied || !in.Committed || in.Hint != "" {
		t.Fatalf("applied: %+v", in)
	}
}

func TestListRecordedNewestFirstWithLocksAndNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", filepath.Join(home, "state"))
	root := SessionsDir(home)
	now := time.Now()
	old, older := now.Add(-48*time.Hour), now.Add(-96*time.Hour)
	a := filepath.Join(root, "20261001-090000-aaaaaa")
	b := filepath.Join(root, "20261002-090000-bbbbbb")
	writeLog(t, a, older, older,
		[3]any{"session.start", "", map[string]any{"model": "m"}},
		[3]any{"user.input", "", map[string]any{"text": "first"}},
		[3]any{"agent.snapshot", "main", map[string]any{}},
		[3]any{"session.end", "", map[string]any{"cost_usd": 0.1, "reason": "exit"}},
	)
	writeLog(t, b, old, old,
		[3]any{"session.start", "", map[string]any{"model": "m"}},
		[3]any{"user.input", "", map[string]any{"text": "second"}},
	)
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil { // not a session: no log
		t.Fatal(err)
	}
	if err := UpdateMeta(a, func(m *Meta) { m.Name = "shop" }); err != nil {
		t.Fatal(err)
	}
	list, err := ListRecorded(home, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != filepath.Base(b) || list[1].ID != filepath.Base(a) {
		t.Fatalf("newest first, sessions only: %+v", list)
	}
	if !list[1].Resumable || list[1].Name != "shop" || list[1].Interrupted || list[1].First != "first" || list[1].Bytes == 0 {
		t.Errorf("a: %+v", list[1])
	}
	if list[0].Resumable || !list[0].Interrupted || list[0].Locked || list[0].Live {
		t.Errorf("b ended without a session.end and nothing holds it: interrupted, not live: %+v", list[0])
	}
	if !list[1].LastWritten.Equal(older) {
		t.Errorf("the rename kept the directory's time: %v, want %v", list[1].LastWritten, older)
	}

	// a session a process holds is locked, live, and not interrupted (it is still being written)
	unlock, err := lockDir(b)
	if err != nil {
		t.Fatal(err)
	}
	list, _ = ListRecorded(home, now)
	if !list[0].Locked || !list[0].Live || list[0].Interrupted {
		t.Errorf("held: %+v", list[0])
	}
	unlock()
	if Locked(b) {
		t.Error("released, it is not held")
	}
	// probing never creates the lock file
	if Locked(a) {
		t.Error("nothing holds a")
	}
	if _, err := os.Stat(filepath.Join(a, ".lock")); err == nil {
		t.Error("probing a session created its lock file")
	}
}

func TestListRecordedWithoutState(t *testing.T) {
	t.Setenv("SLEIPNIR_HOME", filepath.Join(t.TempDir(), "none"))
	list, err := ListRecorded("", time.Now())
	if err != nil || len(list) != 0 {
		t.Fatalf("no state directory: %v %v", list, err)
	}
}

func TestValidID(t *testing.T) {
	for id, ok := range map[string]bool{
		"20261009-221530-a91c3e": true, "20261009-221530-A91C3E": false, "../20261009-221530-a91c3e": false, "": false,
		"20261009-221530-a91c3e/..": false, "2026100-221530-a91c3e": false,
	} {
		if ValidID(id) != ok {
			t.Errorf("ValidID(%q) != %v", id, ok)
		}
	}
}
