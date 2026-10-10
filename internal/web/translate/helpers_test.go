package translate

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

// harness drives a translator step by step, without its goroutine: log events, sink calls and the clock are applied in the order
// the test gives, so that what comes out is a function of the input.
type harness struct {
	tb     testing.TB
	tr     *Translator
	mu     sync.Mutex
	clock  time.Time
	frames []wire.Frame
}

// newHarness builds a translator whose clock and publisher the test owns.
func newHarness(tb testing.TB, cfg Config) *harness {
	h := &harness{tb: tb}
	if cfg.Tab == "" {
		cfg.Tab = "t1"
	}
	h.clock = cfg.StartedAt
	cfg.Now = h.now
	cfg.Publish = h.publish
	h.tr = newTranslator(cfg)
	return h
}

func (h *harness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *harness) publish(f wire.Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frames = append(h.frames, f)
}

// set moves the clock to at (never back).
func (h *harness) set(at time.Time) {
	h.mu.Lock()
	if at.After(h.clock) {
		h.clock = at
	}
	h.mu.Unlock()
}

// feed applies log events as the subscription would deliver them, with a tick of the clock at each event's time.
func (h *harness) feed(evs ...events.Event) {
	for _, e := range evs {
		h.set(e.TS)
		h.tr.mu.Lock()
		h.tr.followEvent(e)
		h.tr.tick(h.now())
		h.tr.mu.Unlock()
	}
}

// advance moves the clock by d and ticks.
func (h *harness) advance(d time.Duration) {
	h.set(h.now().Add(d))
	h.tr.mu.Lock()
	h.tr.tick(h.now())
	h.tr.mu.Unlock()
}

// drain applies the queued sink calls.
func (h *harness) drain() { h.tr.drainSink() }

// raws are the journal's retained events.
func (h *harness) raws() []wire.Raw {
	_, evs, _, _ := h.tr.Journal()
	return evs
}

// decoded are the retained events as maps.
func (h *harness) decoded() []map[string]any {
	return decodeAll(h.tb, h.raws())
}

// decodeAll decodes encoded events into maps.
func decodeAll(tb testing.TB, raws []wire.Raw) []map[string]any {
	tb.Helper()
	out := make([]map[string]any, 0, len(raws))
	for _, r := range raws {
		var m map[string]any
		if err := json.Unmarshal(r, &m); err != nil {
			tb.Fatalf("an event is not JSON: %v: %s", err, r)
		}
		out = append(out, m)
	}
	return out
}

// ofKind keeps the events of the kinds.
func ofKind(evs []map[string]any, kinds ...string) []map[string]any {
	var out []map[string]any
	for _, e := range evs {
		for _, k := range kinds {
			if e["k"] == k {
				out = append(out, e)
			}
		}
	}
	return out
}

// lines writes encoded events one per line.
func lines(raws []wire.Raw) []byte {
	var b bytes.Buffer
	for _, r := range raws {
		b.Write(r)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// golden compares got with testdata/<name>, or rewrites it with -update.
func golden(tb testing.TB, name string, got []byte) {
	tb.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			tb.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("missing golden file %s (create it with: go test ./internal/web/translate -run %s -update): %v", path, tb.Name(), err)
	}
	if !bytes.Equal(want, got) {
		wl, gl := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
		for i := 0; i < len(wl) || i < len(gl); i++ {
			var w, g string
			if i < len(wl) {
				w = wl[i]
			}
			if i < len(gl) {
				g = gl[i]
			}
			if w != g {
				tb.Fatalf("the translation changed: line %d of %s\nwant %s\ngot  %s\nIf the change is intended, read the diff, then run: go test ./internal/web/translate -run %s -update",
					i+1, path, w, g, tb.Name())
			}
		}
	}
}

// shopEvents is the recorded log of the demo's shop scenario (a manager, scouts, writers in worktrees, a reviewer, the merge queue).
func shopEvents(tb testing.TB) []events.Event {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "shop.events.jsonl"))
	if err != nil {
		tb.Fatal(err)
	}
	evs, err := statetest.Parse(b)
	if err != nil {
		tb.Fatal(err)
	}
	return evs
}

// ev builds a log event.
func ev(seq uint64, at time.Time, agent, typ string, data any) events.Event {
	raw, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return events.Event{Seq: seq, TS: at, Session: "s1", Agent: agent, Type: typ, Data: raw}
}

// logBuilder makes a sequence of log events with increasing seqs and times.
type logBuilder struct {
	seq uint64
	at  time.Time
	evs []events.Event
}

// newLog starts a log at t0.
func newLog(t0 time.Time) *logBuilder { return &logBuilder{at: t0} }

// add appends an event dt after the previous one.
func (b *logBuilder) add(dt time.Duration, agent, typ string, data any) events.Event {
	b.seq++
	b.at = b.at.Add(dt)
	e := ev(b.seq, b.at, agent, typ, data)
	b.evs = append(b.evs, e)
	return e
}

// t0 is the start of the hand-built logs.
var t0 = time.Date(2026, 10, 9, 22, 15, 30, 0, time.UTC)

// jsonUnmarshal is json.Unmarshal (a name the tests can use where a parameter is called json).
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
