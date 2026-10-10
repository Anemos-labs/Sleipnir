package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/demo"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// recordedDir writes a log into a session directory of the test's own.
func recordedDir(t *testing.T, log []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), log, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// rawLines writes events one per line.
func rawLines(evs []json.RawMessage) []byte {
	var b bytes.Buffer
	for _, e := range evs {
		b.Write(e)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// A recorded session replays to exactly what the same log translates to when it is followed: the recorded shop and handbook logs
// give their goldens, with the project root taken from the log.
func TestReplayEqualsTheGolden(t *testing.T) {
	shop, err := os.ReadFile(filepath.Join("testdata", "shop.events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		golden string
		log    []byte
	}{{"demo-shop-watch.ui.jsonl", shop}, {"demo-handbook-watch.ui.jsonl", statetest.DemoLog()}} {
		t.Run(tc.golden, func(t *testing.T) {
			evs, err := Replay(context.Background(), recordedDir(t, tc.log), Config{Tab: "rec"})
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			if err != nil {
				t.Fatal(err)
			}
			if got := rawLines(evs); !bytes.Equal(got, want) {
				t.Fatalf("the replay differs from %s:\n%s", tc.golden, firstDiff(string(want), string(got)))
			}
		})
	}
}

// A log that is still being written replays to a prefix of what it replays to once it has grown: a page that asks for the events
// after the ones it has gets the rest, not a different beginning.
func TestReplayOfALogBeingWrittenIsAPrefix(t *testing.T) {
	log, err := os.ReadFile(filepath.Join("testdata", "shop.events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cut := len(log) / 2
	for log[cut] != '\n' {
		cut++
	}
	part, err := Replay(context.Background(), recordedDir(t, log[:cut+1]), Config{})
	if err != nil {
		t.Fatal(err)
	}
	whole, err := Replay(context.Background(), recordedDir(t, log), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(part) == 0 || len(part) >= len(whole) {
		t.Fatalf("%d events of the half, %d of the whole", len(part), len(whole))
	}
	if !bytes.HasPrefix(rawLines(whole), rawLines(part)) {
		t.Fatalf("the half is not a prefix of the whole:\n%s", firstDiff(string(rawLines(whole)[:len(rawLines(part))]), string(rawLines(part))))
	}
}

// A replay keeps the journal's bounds (the keyframe of what it evicted first), refuses a log over 256 MiB with one row, masks and
// cleans the log's text as a live session's, and stops when ctx ends.
func TestReplayBoundsMaskingAndCancel(t *testing.T) {
	log, err := os.ReadFile(filepath.Join("testdata", "shop.events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	evs, err := Replay(context.Background(), recordedDir(t, log), Config{Limits: Limits{JournalEvents: 50, JournalBytes: 1 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	var kf, seqs int
	for _, e := range evs {
		var h struct {
			Seq uint64 `json:"seq"`
		}
		_ = json.Unmarshal(e, &h)
		if h.Seq == 0 {
			if seqs > 0 {
				t.Fatal("a keyframe event after the retained ones")
			}
			kf++
		} else {
			seqs++
		}
	}
	if seqs > 50 || kf == 0 {
		t.Fatalf("%d retained, %d keyframe events", seqs, kf)
	}

	big := t.TempDir()
	f, err := os.Create(filepath.Join(big, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxHistoryBytes + 1); err != nil { // sparse: nothing is written
		t.Fatal(err)
	}
	f.Close()
	evs, err = Replay(context.Background(), big, Config{})
	if err != nil || len(evs) != 1 || !strings.Contains(string(evs[0]), "more than the 256 MiB a page replays") {
		t.Fatalf("a log over the bound: %v %s", err, rawLines(evs))
	}

	b := newLog(t0)
	b.add(time.Second, "", "session.start", map[string]any{"root": "/work", "swarm": true})
	b.add(time.Second, "be-1", "tool.call", map[string]any{"id": "c1", "name": "bash", "input": map[string]any{"command": "echo key=" + secret}})
	b.add(time.Second, "be-1", "tool.result", map[string]any{"id": "c1", "name": "bash", "error": false})
	b.add(time.Second, "be-1", "turn.append", map[string]any{"role": "user", "blocks": []map[string]any{{"kind": "tool_result", "tool_id": "c1", "result": []map[string]any{{"kind": "text", "text": "\x1b[31m" + secret + "\u202e"}}}}})
	b.add(time.Second, "be-1", "mail.send", map[string]any{"id": "m1", "from": "be-1", "to": "mgr", "text": "<script>x</script> " + secret})
	var lb bytes.Buffer
	for _, e := range b.evs {
		raw, _ := json.Marshal(e)
		lb.Write(raw)
		lb.WriteByte('\n')
	}
	evs, err = Replay(context.Background(), recordedDir(t, lb.Bytes()), Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(rawLines(evs))
	for _, bad := range []string{secret, "\x1b", "\u202e", "<script>"} {
		if strings.Contains(raw, bad) {
			t.Errorf("the replay carries %q", bad)
		}
	}
	if !strings.Contains(raw, `"name":"Bash"`) || !strings.Contains(raw, "⟦redacted:") {
		t.Fatalf("replay: %s", raw)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	long := bytes.Repeat(log, 3)
	if _, err := Replay(ctx, recordedDir(t, long), Config{}); err == nil {
		t.Fatal("a cancelled replay finished")
	}
	if _, err := Replay(context.Background(), t.TempDir(), Config{}); err == nil {
		t.Fatal("a directory without a log replayed")
	}
}

// frameLog collects published frames.
type frameLog struct {
	mu     sync.Mutex
	frames []wire.Frame
}

func (f *frameLog) publish(fr wire.Frame) {
	f.mu.Lock()
	f.frames = append(f.frames, fr)
	f.mu.Unlock()
}

// has reports whether an ev frame of the tab contains s.
func (f *frameLog) has(tab, s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fr := range f.frames {
		if ev, ok := fr.Data.(wire.EvFrame); ok && fr.Tab == tab && ev.Tab == tab && strings.Contains(string(ev.Ev), s) {
			return true
		}
	}
	return false
}

// Watching a session that a running Sleipnir writes (the demo, through the real harness: it holds the session directory's lock while
// it runs) publishes the session's events for the read-only tab as they are written, and ends by itself once the session ended and
// released its directory. Ending ctx ends it too.
func TestFollowDirEndsWithTheSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", filepath.Join(dir, "state"))
	sdir := filepath.Join(dir, "session")
	fl := &frameLog{}
	done := make(chan error, 1)
	go func() { done <- FollowDir(context.Background(), Config{Tab: "w-1", Publish: fl.publish}, sdir) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := demo.Run(ctx, demo.Options{Scenario: "handbook", Topics: 4, Dir: dir}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("following ended with %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("following did not end after the session ended")
	}
	for _, s := range []string{`"k":"final"`, `"who":"mgr"`, `"k":"tool"`} {
		if !fl.has("w-1", s) {
			t.Errorf("no %s published for the tab", s)
		}
	}

	// a session that has not ended is followed until ctx ends
	open := recordedDir(t, []byte{})
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- FollowDir(ctx2, Config{Tab: "w-2", Publish: fl.publish}, open) }()
	b := newLog(time.Now())
	e := b.add(0, "be-1", "mail.send", map[string]any{"id": "m1", "from": "be-1", "to": "mgr", "text": "hello"})
	raw, _ := json.Marshal(e)
	if err := os.WriteFile(filepath.Join(open, "events.jsonl"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return fl.has("w-2", `"text":"hello"`) })
	select {
	case err := <-done2:
		t.Fatalf("following ended early: %v", err)
	case <-time.After(1500 * time.Millisecond):
	}
	cancel2()
	select {
	case err := <-done2:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("following did not end with its context")
	}
}
