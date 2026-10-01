package inspect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// evb writes hand-made event logs, one JSON line per event with explicit times, so
// a test controls every number it asserts on. It appends to events.jsonl in dir.
type evb struct {
	t    testing.TB
	dir  string
	path string
	seq  uint64
	now  time.Time
}

var t0 = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func newEvb(t testing.TB) *evb {
	t.Helper()
	dir := t.TempDir()
	return &evb{t: t, dir: dir, path: filepath.Join(dir, "events.jsonl"), now: t0}
}

// at moves the clock to t0 + d.
func (b *evb) at(d time.Duration) *evb { b.now = t0.Add(d); return b }

func (b *evb) line(agent, typ string, data any) []byte {
	b.t.Helper()
	b.seq++
	var raw json.RawMessage
	switch d := data.(type) {
	case nil:
	case json.RawMessage:
		raw = d
	default:
		var err error
		if raw, err = json.Marshal(d); err != nil {
			b.t.Fatal(err)
		}
	}
	e := events.Event{Seq: b.seq, TS: b.now, Session: "s1", Agent: agent, Type: typ, Data: raw}
	line, err := json.Marshal(e)
	if err != nil {
		b.t.Fatal(err)
	}
	return append(line, '\n')
}

func (b *evb) write(p []byte) {
	b.t.Helper()
	f, err := os.OpenFile(b.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		b.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(p); err != nil {
		b.t.Fatal(err)
	}
	// The builder lives on a fake timeline (t0 is a fixed date) and the views decide "live" and "idle" by comparing the clock they
	// are given with the log's modification time, so the file's time must be on that timeline too: with the real time it is
	// ahead of any fake "now" once that date has passed, and every test of an old log became a test of a live one.
	if err := os.Chtimes(b.path, b.now, b.now); err != nil {
		b.t.Fatal(err)
	}
}

// emit appends one event and returns its seq.
func (b *evb) emit(agent, typ string, data any) uint64 {
	b.t.Helper()
	b.write(b.line(agent, typ, data))
	return b.seq
}

// raw appends bytes as they are (torn lines, garbage).
func (b *evb) raw(s string) { b.t.Helper(); b.write([]byte(s)) }

// m is shorthand for a JSON object.
type m = map[string]any

// req emits a model.request for a main request.
func (b *evb) req(agent, id string, secs []m, extra m) uint64 {
	p := m{"req": id, "agent": agent, "role": "worker", "kind": "main", "model": "claude-sonnet-5-5", "provider": "test", "dialect": "openai-chat",
		"sections": secs, "thread_from": 1, "thread_to": 1, "cache_key": "k", "renderer": "sleipnir-kv/1"}
	for k, v := range extra {
		p[k] = v
	}
	return b.emit(agent, events.TypeModelRequest, p)
}

// resp emits a model.response with the given usage.
func (b *evb) resp(agent, id string, in, read, write, out int, extra m) uint64 {
	u := core.Usage{InputTokens: in, CacheReadTokens: read, CacheWrite5mTokens: write, OutputTokens: out}
	p := m{"req": id, "model": "claude-sonnet-5-5", "usage": u, "hit_ratio": u.HitRatio(), "stop": "tool_use", "ttfb_ms": 100, "total_ms": 900}
	for k, v := range extra {
		p[k] = v
	}
	return b.emit(agent, events.TypeModelResponse, p)
}

func sec(name string, tokens int, hash string) m {
	return m{"name": name, "hash": hash, "tokens": tokens}
}

// h64 returns a valid-looking 64 hex digit hash derived from s.
func h64(s string) string { return string(core.HashString(s)) }

func near(t testing.TB, what string, got, want float64) {
	t.Helper()
	if d := got - want; d > 1e-9 || d < -1e-9 {
		t.Errorf("%s = %.9f, want %.9f", what, got, want)
	}
}

func mustLoad(t testing.TB, dir string) *Session {
	t.Helper()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustTail(t testing.TB, s *Session) int {
	t.Helper()
	n, err := s.Tail()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// fixtureDir is the recorded single-agent run used across the repo's tests.
const fixtureDir = "../rl/traj/testdata/agent_run"

func requireFixture(t testing.TB) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fixtureDir, "events.jsonl")); err != nil {
		t.Skipf("fixture not available: %v", err)
	}
}

// reqByID returns a request row from a session.
func reqByID(t testing.TB, s *Session, id string) Req {
	t.Helper()
	for _, r := range s.Requests(RequestQuery{Tail: true, Limit: 20000}).Requests {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no request %s", id)
	return Req{}
}

func sprint(v any) string { return fmt.Sprintf("%+v", v) }
