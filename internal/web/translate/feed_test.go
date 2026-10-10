package translate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
)

// feedCase is an event that the terminal's feed has a line for and the page shows as a system row: the row's glyph, text and agent
// ("" text: the event has no row).
type feedCase struct {
	name       string
	agent, typ string
	data       map[string]any
	glyph      string
	text       string
	ag         string
}

// feedCases are the events whose rows are the State's feed lines, in the shapes the producers write (internal/swarm, internal/agent,
// internal/tools/shell).
var feedCases = []feedCase{
	{"a hold", "mgr", "swarm.hold", map[string]any{"reason": "Not finished: running: be-1 (T3)"},
		"⚠", "the manager answered while work was unfinished and was sent back to it · Not finished: running: be-1 (T3)", "mgr"},
	{"a run that ended with work left", "mgr", "swarm.unfinished", map[string]any{"unfinished": "running: be-1 (T3); waiting: T5"},
		"⚠", "the run ended with work unfinished · running: be-1 (T3); waiting: T5", "mgr"},
	{"a run that ended with work left, without the list", "mgr", "swarm.unfinished", map[string]any{},
		"⚠", "the run ended with work unfinished", "mgr"},
	{"a wake", "mgr", "swarm.wake", map[string]any{"n": 1, "note": "While you were idle: T1 is in review (be-1)"},
		"◇", "the idle manager was woken · While you were idle: T1 is in review (be-1)", "mgr"},
	{"the bound on wakes", "mgr", "swarm.wake.paused", map[string]any{"n": 8},
		"⚠", "the manager will not be woken again until you write to it", "mgr"},
	{"the bound on a worker's wakes", "be-1", "swarm.wake_limit", map[string]any{"limit": 40, "task": "T1"},
		"⚠", "peer mail no longer wakes be-1", "be-1"},
	{"a shutdown with agents still running", "swarm", "swarm.shutdown", map[string]any{"waited": "10s", "note": "agents still running were left behind"},
		"⚠", "the swarm shut down with agents still running", ""},
	{"a panic of an output sink", "swarm", "sink.panic", map[string]any{"agent": "be-1", "panic": "index out of range [3] with length 2"},
		"✗", "a panic in be-1's output sink was recovered · index out of range [3] with length 2", "be-1"},
	{"a compaction rejected", "be-1", "compact.reject", map[string]any{"stage": "stale", "reason": "the thread moved while the patch was made"},
		"⚠", "compaction rejected (stale) · the thread moved while the patch was made", "be-1"},
	{"a compactor's patch that was unusable", "be-1", "compact.reject", map[string]any{"stage": "model_patch", "reason": "the patch dropped the last user turn", "fallback": "mechanical"},
		"⚠", "the compactor's patch was unusable: folding mechanically instead · the patch dropped the last user turn", "be-1"},
	{"a new epoch of the shared prefix", "swarm", "layer.commit", map[string]any{"scope": "shared-epoch", "reason": "the constitution changed", "hash": "abc123def456"},
		"↻", "the shared prefix was re-written: a new epoch · the constitution changed", ""},
	{"an agent that took the new prefix", "be-1", "layer.commit", map[string]any{"scope": "shared-sync", "reason": "the agent took the new prefix"}, "", "", ""},
	{"a background job that finished", "be-1", "tool.job", map[string]any{"id": "j1", "agent": "be-1", "command": "npm run build", "status": "finished", "exit": 0, "duration_ms": 61000},
		"✓", "background job j1 finished: npm run build · 1m01s", "be-1"},
	{"a background job that exited with an error", "be-1", "tool.job", map[string]any{"id": "j2", "agent": "be-1", "command": "go test ./...", "status": "finished", "exit": 2, "duration_ms": 4200},
		"✗", "background job j2 exited 2: go test ./... · 4.2 s", "be-1"},
	{"a background job that was killed", "be-1", "tool.job", map[string]any{"id": "j3", "agent": "be-1", "command": "sleep 600", "status": "killed", "duration_ms": 12000},
		"✗", "background job j3 was killed: sleep 600 · 12 s", "be-1"},
	{"a background job that started", "be-1", "tool.job", map[string]any{"id": "j4", "agent": "be-1", "command": "sleep 600", "status": "started"}, "", "", ""},
}

// feedLog is a small team's log up to the first event of a case, and the case's event.
func feedLog(tc feedCase) []events.Event {
	b := newLog(t0)
	b.add(time.Second, "", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work"})
	b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "mgr", "role": "manager", "model": "m1"})
	b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "task": "T1", "model": "m1"})
	b.add(time.Second, tc.agent, tc.typ, tc.data)
	return b.evs
}

// Each of these events becomes a system row of the manager's channel that says what the terminal's feed says for it, in a hosted
// (followed) log and in a replayed one, with the agent it is about; the events that write no line of the feed write no row. The words are
// the State's own: the same events folded into a State of their own give the same line.
func TestFeedLinesBecomeSystemRows(t *testing.T) {
	for _, tc := range feedCases {
		t.Run(tc.name, func(t *testing.T) {
			evs := feedLog(tc)
			var ref *state.State
			if tc.text != "" {
				ref = state.New()
				for _, e := range evs {
					ref.Apply(e)
				}
			}
			for _, logOnly := range []bool{false, true} {
				h := translateLog(t, evs, "/work", logOnly)
				raws := h.raws()
				checkStream(t, raws)
				rows := ofKind(h.decoded(), "sys")
				if tc.text == "" {
					if len(rows) != 0 {
						t.Fatalf("logOnly %v: rows for an event without a line of the feed: %v", logOnly, rows)
					}
					continue
				}
				if len(rows) != 1 {
					t.Fatalf("logOnly %v: %d system rows: %v", logOnly, len(rows), rows)
				}
				r := rows[0]
				if r["ch"] != "mgr" || r["glyph"] != tc.glyph || r["text"] != tc.text {
					t.Errorf("logOnly %v: the row is %v, want %s %q", logOnly, r, tc.glyph, tc.text)
				}
				if got, _ := r["ag"].(string); got != tc.ag {
					t.Errorf("logOnly %v: the row is about %q, want %q", logOnly, got, tc.ag)
				}
				feed := ref.Snapshot().Feed
				last := feed[len(feed)-1]
				if want := last.Text + " · " + last.Detail; last.Detail == "" && r["text"] != last.Text || last.Detail != "" && r["text"] != want {
					t.Errorf("logOnly %v: the terminal's line is %q (%q), the page says %q", logOnly, last.Text, last.Detail, r["text"])
				}
			}
		})
	}
}

// The rows of these events are in the history of a resumed session, at t 0 with their real time, and in a Replay of the recorded log.
func TestFeedRowsAreInTheHistoryAndTheReplay(t *testing.T) {
	b := newLog(t0)
	b.add(time.Second, "", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work"})
	b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "mgr", "role": "manager", "model": "m1"})
	b.add(time.Second, "mgr", "swarm.hold", map[string]any{"reason": "T1 is unfinished"})
	b.add(time.Second, "mgr", "swarm.wake", map[string]any{"n": 1, "note": "be-1 finished"})
	b.add(time.Second, "mgr", "swarm.unfinished", map[string]any{"unfinished": "waiting: T2"})
	b.add(time.Second, "", "session.end", map[string]any{})
	dir := t.TempDir()
	var buf []byte
	for _, e := range b.evs {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		buf = append(append(buf, line...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), buf, 0o600); err != nil {
		t.Fatal(err)
	}
	want := []string{"the manager answered while work was unfinished and was sent back to it · T1 is unfinished",
		"the idle manager was woken · be-1 finished", "the run ended with work unfinished · waiting: T2"}
	texts := func(raws []string) (out []string) {
		for _, r := range raws {
			var e map[string]any
			if err := json.Unmarshal([]byte(r), &e); err != nil {
				t.Fatal(err)
			}
			if e["k"] == "sys" {
				out = append(out, e["text"].(string))
			}
		}
		return out
	}

	raws, err := Replay(context.Background(), dir, Config{Tab: "r", Root: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	var replayed []string
	for _, r := range raws {
		replayed = append(replayed, string(r))
	}
	if got := texts(replayed); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the replay's rows: %q, want %q", got, want)
	}

	h := newHarness(t, Config{Root: "/work"})
	h.set(t0.Add(time.Hour))
	log := openLog(t, dir, "s1", h.now)
	detach := h.tr.Attach(log, dir)
	defer detach()
	var history []string
	for _, e := range h.decoded() {
		if e["k"] == "sys" {
			if e["t"] != 0.0 || e["at"] == nil {
				t.Errorf("a row of the history is not at t 0 with its real time: %v", e)
			}
			history = append(history, e["text"].(string))
		}
	}
	if strings.Join(history, "\n") != strings.Join(want, "\n") {
		t.Errorf("the history's rows: %q, want %q", history, want)
	}
}

// Text the log carries is data: escape sequences, control and bidirectional characters are removed, a secret is masked, markup stays
// text, and a row is one line of at most 300 characters, whichever field the text came in.
func TestFeedRowsPassHostileTextAsData(t *testing.T) {
	hostile := "<script>window.__pwn=1</script>\x1b[2J\x1b]52;c;ZXZpbA==\x07 ‮gnp.exe key=" + secret + "​\nsecond line " + strings.Repeat("x", 2000)
	for _, tc := range []feedCase{
		{agent: "mgr", typ: "swarm.hold", data: map[string]any{"reason": hostile}},
		{agent: "mgr", typ: "swarm.unfinished", data: map[string]any{"unfinished": hostile}},
		{agent: "mgr", typ: "swarm.wake", data: map[string]any{"note": hostile}},
		{agent: "swarm", typ: "sink.panic", data: map[string]any{"agent": "<img src=x onerror=alert(1)>", "panic": hostile}},
		{agent: "be-1", typ: "compact.reject", data: map[string]any{"stage": "<b>stage</b>", "reason": hostile}},
		{agent: "swarm", typ: "layer.commit", data: map[string]any{"scope": "shared-epoch", "reason": hostile}},
		{agent: "be-1", typ: "tool.job", data: map[string]any{"id": "<i>j</i>", "agent": "be-1", "command": hostile, "status": "finished", "exit": 1}},
		{agent: "be-1", typ: "swarm.wake_limit", data: map[string]any{"limit": 3}},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			if tc.typ == "swarm.wake_limit" {
				tc.agent = hostile // an agent id is data too
			}
			h := translateLog(t, feedLog(tc), "/work", false)
			rows := ofKind(h.decoded(), "sys")
			if len(rows) != 1 {
				t.Fatalf("%d rows: %v", len(rows), rows)
			}
			raw := string(lines(h.raws()))
			for _, bad := range []string{"\x1b", "\u001b", "‮", "​", "\x07", secret, "<script>", "<img", "<b>", "<i>"} {
				if strings.Contains(raw, bad) {
					t.Errorf("the output carries %q: %s", bad, raw)
				}
			}
			txt, _ := rows[0]["text"].(string)
			if n := utf8.RuneCountInString(txt); n == 0 || n > capReason || strings.ContainsAny(txt, "\n\r") {
				t.Errorf("the row is %d characters on %d lines", n, strings.Count(txt, "\n")+1)
			}
		})
	}
}

// A flood of these events is held to the rate limit of notices: 20 rows in 10 seconds, and one row counts the rest; the journal does not
// grow with the flood.
func TestFeedRowsAreLimited(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	h.feed(b.add(time.Second, "", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work"}))
	for i := 0; i < 500; i++ {
		h.feed(b.add(5*time.Millisecond, "be-1", "tool.job", map[string]any{"id": "j", "agent": "be-1", "command": "make", "status": "finished", "exit": 0, "duration_ms": 10}))
	}
	h.advance(11 * time.Second)
	rows := ofKind(h.decoded(), "sys")
	if len(rows) != 21 || rows[20]["text"] != "480 more notices" {
		t.Fatalf("%d rows, the last %v", len(rows), rows[len(rows)-1])
	}
}
