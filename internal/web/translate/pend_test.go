package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// batchLog is a log of one step of be-1 with two tool calls run side by side: a quick one whose result comes at quick and a slow one
// whose result comes at slow, then the turn.append that carries both outputs just after the slow one.
func batchLog(quick, slow time.Duration) []events.Event {
	b := newLog(t0)
	b.add(0, "", "session.start", map[string]any{"root": "/work", "swarm": true})
	b.add(time.Millisecond, "be-1", "tool.call", map[string]any{"id": "q", "name": "bash", "input": map[string]any{"command": "go vet ./..."}})
	b.add(time.Millisecond, "be-1", "tool.call", map[string]any{"id": "s", "name": "bash", "input": map[string]any{"command": "go test ./..."}})
	start := b.at
	b.at = start.Add(quick)
	b.add(0, "be-1", "tool.result", map[string]any{"id": "q", "name": "bash", "error": false, "ms": quick.Milliseconds()})
	b.at = start.Add(slow)
	b.add(0, "be-1", "tool.result", map[string]any{"id": "s", "name": "bash", "error": false, "ms": slow.Milliseconds()})
	b.add(10*time.Millisecond, "be-1", "turn.append", map[string]any{"role": "user", "blocks": []map[string]any{
		{"kind": "tool_result", "tool_id": "q", "result": []map[string]any{{"kind": "text", "text": "vet ok"}}},
		{"kind": "tool_result", "tool_id": "s", "result": []map[string]any{{"kind": "text", "text": "ok  a\nok  b"}}}}})
	b.add(time.Second, "", "session.end", map[string]any{"reason": "other"})
	return b.evs
}

// encodeLog writes events as a log file's lines.
func encodeLog(evs []events.Event) []byte {
	var b bytes.Buffer
	for _, e := range evs {
		raw, _ := json.Marshal(e)
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// rowsOut is the out of each tool row, by its call id.
func rowsOut(evs []map[string]any) map[string]any {
	out := map[string]any{}
	for _, e := range ofKind(evs, "tool") {
		out[e["tid"].(string)] = e["out"]
	}
	return out
}

// A replayed session pairs every tool row with its output whatever the call took: the replay reads the log to its end, it waits for
// nothing.
func TestReplayKeepsTheOutputOfSlowCalls(t *testing.T) {
	for _, slow := range []time.Duration{500 * time.Millisecond, 5 * time.Second, 2 * time.Minute} {
		evs, err := Replay(context.Background(), recordedDir(t, encodeLog(batchLog(100*time.Millisecond, slow))), Config{})
		if err != nil {
			t.Fatal(err)
		}
		got := rowsOut(decodeAll(t, toRaws(evs)))
		if got["q"] != "vet ok" || got["s"] != "2 lines" {
			t.Errorf("a call of %v: rows %v", slow, got)
		}
	}
}

// toRaws converts encoded events.
func toRaws(evs []json.RawMessage) []wire.Raw {
	out := make([]wire.Raw, len(evs))
	for i, e := range evs {
		out[i] = e
	}
	return out
}

// A followed log does not send a tool row before the output of its step arrives: while another call of the same step runs, its row
// waits; it is sent with what the log has two seconds after the step's last result, when no turn.append has come by then.
func TestFollowWaitsForTheOutputOfASlowBatch(t *testing.T) {
	evs := batchLog(200*time.Millisecond, 8*time.Second)
	h := newHarness(t, Config{StartedAt: t0})
	h.tr.d.logOnly = true
	for _, e := range evs {
		for h.now().Add(500 * time.Millisecond).Before(e.TS) { // the clock ticks while the slow call runs
			h.advance(500 * time.Millisecond)
		}
		h.feed(e)
	}
	if got := rowsOut(h.decoded()); got["q"] != "vet ok" || got["s"] != "2 lines" {
		t.Fatalf("rows %v", got)
	}

	// a step whose turn.append never comes: its rows go out two seconds after its last result, with no output
	b := newLog(t0)
	b.add(0, "be-1", "tool.call", map[string]any{"id": "x", "name": "bash", "input": map[string]any{"command": "true"}})
	b.add(3*time.Second, "be-1", "tool.result", map[string]any{"id": "x", "name": "bash", "error": false})
	h2 := newHarness(t, Config{StartedAt: t0})
	h2.tr.d.logOnly = true
	h2.feed(b.evs...)
	h2.advance(time.Second)
	if n := len(ofKind(h2.decoded(), "tool")); n != 0 {
		t.Fatal("a row was sent a second after its result")
	}
	h2.advance(1500 * time.Millisecond)
	if got := rowsOut(h2.decoded()); len(got) != 1 {
		t.Fatalf("the row was not sent after the grace: %v", got)
	}
}

// The same with a real follower of a file that another writer appends to, the slow call's result and the output coming seconds after
// the quick call's result.
func TestFollowOfAFileKeepsTheOutputOfASlowBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a slow call of three seconds")
	}
	evs := batchLog(200*time.Millisecond, 3*time.Second)
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, encodeLog(evs[:4]), 0o600); err != nil { // the start, both calls, the quick result
		t.Fatal(err)
	}
	tr := New(Config{Tab: "w"})
	defer tr.Close()
	stop := tr.Follow(path)
	defer stop()
	time.Sleep(3 * time.Second)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(encodeLog(evs[4:]))
	f.Close()
	waitFor(t, func() bool {
		_, raws, _, _ := tr.Journal()
		return len(ofKind(decodeAll(t, raws), "tool")) == 2
	})
	_, raws, _, _ := tr.Journal()
	if got := rowsOut(decodeAll(t, raws)); got["q"] != "vet ok" || got["s"] != "2 lines" {
		t.Fatalf("rows %v", got)
	}
}

// A subscription that drops events (its buffer filled while the translator was held up) and then falls silent is healed by the
// clock: the dropped events are read back from the file without waiting for a later event to show the gap.
func TestALossySubscriptionHealsWhenQuiet(t *testing.T) {
	dir := t.TempDir()
	log := openLog(t, dir, "s", time.Now)
	release := make(chan struct{})
	var armed atomic.Bool
	var held atomic.Bool
	publish := func(f wire.Frame) {
		if armed.Load() && held.CompareAndSwap(false, true) {
			<-release // the first frame after the setup holds the translator up
		}
	}
	tr := New(Config{Tab: "t", Publish: publish})
	defer tr.Close()
	defer tr.Attach(log, dir)()
	armed.Store(true)
	const n = 3000 // 6,000 events: more than the subscription's 4,096
	for i := 0; i < n; i++ {
		req := "r" + jsNum(float64(i))
		log.Emit("be-1", events.TypeModelRequest, request(req))
		log.Emit("be-1", events.TypeModelResponse, response(req, 100))
		if i == 0 {
			waitFor(t, func() bool { return held.Load() })
		}
	}
	close(release)
	waitFor(t, func() bool {
		_, raws, _, _ := tr.Journal()
		return strings.Count(string(lines(raws)), `"k":"req"`) == n
	})
}
