package inspect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
)

func TestTornLastLineIsLeftForTheNextPoll(t *testing.T) {
	b := newEvb(t)
	b.emit("a", "user.input", m{"text": "hello"})
	b.emit("", "log.open", nil)
	full := b.line("a", "tool.call", m{"id": "c1", "name": "read", "input": m{}})
	b.raw(string(full[:len(full)/2])) // the writer is mid-line

	s := mustLoad(t, b.dir)
	log := s.Log()
	if log.Events != 2 || log.TornBytes != int64(len(full)/2) || log.BadLines != 0 {
		t.Fatalf("after a torn tail: events %d torn %d bad %d", log.Events, log.TornBytes, log.BadLines)
	}
	if n := mustTail(t, s); n != 0 {
		t.Errorf("polling an unchanged torn log applied %d events", n)
	}

	// The writer finishes the line: it is picked up exactly once.
	b.raw(string(full[len(full)/2:]))
	if n := mustTail(t, s); n != 1 {
		t.Fatalf("completed line applied %d events, want 1", n)
	}
	log = s.Log()
	if log.Events != 3 || log.TornBytes != 0 || log.Bytes != log.FileBytes {
		t.Errorf("after completion: %+v", log)
	}
	if got := s.Summary().Totals.ToolCalls; got != 1 {
		t.Errorf("tool calls = %d, want 1", got)
	}
}

func TestFragmentThatIsAWholeEventIsConsumed(t *testing.T) {
	b := newEvb(t)
	b.emit("a", "user.input", m{"text": "one"})
	whole := b.line("a", "tool.call", m{"id": "c1", "name": "read", "input": m{}})
	b.raw(string(bytes.TrimRight(whole, "\n"))) // flushed exactly before the newline
	s := mustLoad(t, b.dir)
	if s.Log().Events != 2 || s.Log().TornBytes != 0 {
		t.Fatalf("a complete event without its newline: %+v", s.Log())
	}
	b.raw("\n") // the newline arrives: an empty line, ignored
	b.emit("a", "tool.result", m{"id": "c1", "name": "read", "chars": 1, "ms": 1})
	if n := mustTail(t, s); n != 1 {
		t.Errorf("applied %d, want 1", n)
	}
	if l := s.Log(); l.BadLines != 0 || l.Events != 3 {
		t.Errorf("log = %+v", l)
	}
}

func TestGarbageBlankCRLFAndNonEventLines(t *testing.T) {
	b := newEvb(t)
	b.emit("a", "user.input", m{"text": "one"})
	b.raw("this is not json\n")
	b.raw("\n")
	b.raw("   \n")
	b.raw(`{"hello":"world"}` + "\n")                                // JSON, but no seq or type
	b.raw(`{"seq":0,"type":"x","ts":"2026-01-01T00:00:00Z"}` + "\n") // seq 0 is not an event
	line := b.line("a", "user.input", m{"text": "two"})
	b.raw(string(bytes.TrimRight(line, "\n")) + "\r\n") // CRLF
	s := mustLoad(t, b.dir)
	if l := s.Log(); l.Events != 2 || l.BadLines != 3 {
		t.Errorf("events %d bad %d, want 2 events and 3 bad lines", l.Events, l.BadLines)
	}
	found := false
	for _, w := range s.Summary().Warnings {
		found = found || strings.Contains(w, "3 lines were not valid events")
	}
	if !found {
		t.Errorf("warnings = %v", s.Summary().Warnings)
	}
}

func TestOversizeLineIsSkippedNotBuffered(t *testing.T) {
	b := newEvb(t)
	b.emit("a", "user.input", m{"text": "one"})
	b.emit("a", "user.input", m{"text": strings.Repeat("x", 40000)}) // over the 4 KiB limit below
	b.emit("a", "tool.call", m{"id": "c1", "name": "read", "input": m{}})
	s, err := LoadWith(b.dir, Options{MaxLineBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	l := s.Log()
	if l.Events != 2 || l.BadLines != 1 || l.TornBytes != 0 || l.Bytes != l.FileBytes {
		t.Errorf("log = %+v: the huge line is skipped whole and the next one still parses", l)
	}
	if got := s.Summary().Totals.ToolCalls; got != 1 {
		t.Errorf("tool calls = %d", got)
	}
}

func TestTailFollowsARealWriter(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "live")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	const n = 1500
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // the writer: a busy agent
		defer wg.Done()
		for i := 1; i <= n; i++ {
			id := fmt.Sprintf("a.%d", i)
			log.Emit("a", "model.request", m{"req": id, "agent": "a", "kind": "main", "model": "claude-sonnet-5-5", "sections": []m{sec("shared", 1000, h64("s"))}, "thread_from": 1, "thread_to": i})
			log.Emit("a", "model.response", m{"req": id, "model": "claude-sonnet-5-5", "usage": core.Usage{InputTokens: 10, CacheReadTokens: 990, OutputTokens: 1}})
			if i%100 == 0 {
				log.Flush()
			}
		}
		log.Flush()
	}()
	stop := make(chan struct{})
	go func() { // a reader hammering every view while the tail loop runs
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s.Summary()
			s.Agents()
			s.Requests(RequestQuery{Tail: true, Limit: 50})
			s.Compactions()
			s.Anomalies()
			s.Swarm()
			_, _ = s.Events(EventQuery{Tail: true, Limit: 20})
		}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.Tail(); err != nil {
			t.Fatal(err)
		}
		if s.Summary().Totals.Requests == n {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	log.Close()
	mustTail(t, s)
	sum := s.Summary()
	if sum.Totals.Requests != n || sum.Totals.Pending != 0 || sum.Totals.CacheRead != int64(n*990) {
		t.Errorf("totals = %+v after following %d requests", sum.Totals, n)
	}
	if l := s.Log(); l.TornBytes != 0 || l.BadLines != 0 || l.Reloads != 0 || l.Bytes != l.FileBytes {
		t.Errorf("log = %+v", l)
	}
}

func TestTruncationRebuildsTheModel(t *testing.T) {
	b := newEvb(t)
	secs := []m{sec("shared", 1000, h64("s"))}
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("a.%d", i)
		b.at(time.Duration(i)*time.Second).req("a", id, secs, nil)
		b.resp("a", id, 10, 100, 0, 1, nil)
	}
	s := mustLoad(t, b.dir)
	if s.Summary().Totals.Requests != 5 {
		t.Fatal("setup")
	}
	// events.Open truncates a torn tail on resume, and a resumed session may then
	// write different events: the file is now shorter than what we consumed.
	if err := os.WriteFile(b.path, b.line("a", "user.input", m{"text": "fresh start"}), 0o600); err != nil {
		t.Fatal(err)
	}
	n := mustTail(t, s)
	sum := s.Summary()
	if n != 1 || sum.Totals.Requests != 0 || sum.Log.Reloads != 1 || sum.Session.Goal != "fresh start" {
		t.Errorf("after truncation: applied %d, requests %d, reloads %d, goal %q", n, sum.Totals.Requests, sum.Log.Reloads, sum.Session.Goal)
	}
	found := false
	for _, w := range sum.Warnings {
		found = found || strings.Contains(w, "truncated or replaced")
	}
	if !found {
		t.Errorf("warnings = %v", sum.Warnings)
	}
}

func TestReplacementByAnotherFileRebuildsTheModel(t *testing.T) {
	b := newEvb(t)
	b.emit("a", "user.input", m{"text": "old"})
	s := mustLoad(t, b.dir)
	// A different file with more bytes moves into place: same path, new inode.
	other := newEvb(t)
	other.emit("a", "user.input", m{"text": "a much longer replacement goal so that the new file is bigger"})
	other.emit("a", "tool.call", m{"id": "c", "name": "read", "input": m{}})
	if err := os.Rename(other.path, b.path); err != nil {
		t.Fatal(err)
	}
	mustTail(t, s)
	if sum := s.Summary(); sum.Log.Reloads != 1 || !strings.HasPrefix(sum.Session.Goal, "a much longer") || sum.Totals.ToolCalls != 1 {
		t.Errorf("after replacement: reloads %d goal %q calls %d", sum.Log.Reloads, sum.Session.Goal, sum.Totals.ToolCalls)
	}
}

func TestLoadAcceptsTheFileItself(t *testing.T) {
	b := newEvb(t)
	b.emit("a", "user.input", m{"text": "hi"})
	s, err := Load(b.path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Log().Events != 1 || s.Dir() != b.dir {
		t.Errorf("log %+v dir %s", s.Log(), s.Dir())
	}
}

func TestRefusesSymlinkedLogAndMissingLog(t *testing.T) {
	b := newEvb(t)
	b.emit("a", "user.input", m{"text": "hi"})
	dir := t.TempDir()
	if err := os.Symlink(b.path, filepath.Join(dir, "events.jsonl")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := Load(dir); err == nil {
		t.Error("a symlinked events.jsonl was opened: the inspector must not follow it")
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Error("a directory without events.jsonl loaded")
	}
	fifoDir := t.TempDir()
	if err := mkfifo(filepath.Join(fifoDir, "events.jsonl")); err == nil {
		done := make(chan error, 1)
		go func() { _, err := Load(fifoDir); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Error("a FIFO was read as a log")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("opening a FIFO as the log blocked")
		}
	}
}

// ---- retention and streaming ---------------------------------------------------------------------------------

func TestRetentionWindowKeepsTotalsExact(t *testing.T) {
	b := newEvb(t)
	secs := []m{sec("shared", 1000, h64("s"))}
	// a.1 is still in flight when hundreds of later requests push it out of the window.
	b.at(0).req("a", "a.1", secs, nil)
	const n = 400
	for i := 2; i <= n; i++ {
		id := fmt.Sprintf("a.%d", i)
		b.at(time.Duration(i)*time.Second).req("a", id, secs, m{"thread_to": i})
		b.resp("a", id, 10, 990, 0, 1, nil)
	}
	s, err := LoadWith(b.dir, Options{MaxRequests: 50})
	if err != nil {
		t.Fatal(err)
	}
	page := s.Requests(RequestQuery{Tail: true, Limit: 20000})
	if page.Retained > 62 || page.Dropped != n-page.Retained || len(page.Requests) != page.Retained {
		t.Fatalf("retained %d dropped %d rows %d of %d requests", page.Retained, page.Dropped, len(page.Requests), n)
	}
	if last := page.Requests[len(page.Requests)-1]; last.ID != fmt.Sprintf("a.%d", n) {
		t.Errorf("the window must end at the newest request, got %s", last.ID)
	}
	for i := 1; i < len(page.Requests); i++ {
		if page.Requests[i].Seq <= page.Requests[i-1].Seq {
			t.Fatal("window not in sequence order")
		}
	}
	sum := s.Summary()
	if sum.Totals.Requests != n || sum.Totals.CacheRead != int64((n-1)*990) || sum.Totals.Pending != 1 {
		t.Errorf("totals = %+v: eviction must not touch the totals", sum.Totals)
	}
	if _, ok := s.Layers("a.2", false); ok {
		t.Error("a request outside the window is still served")
	}
	found := false
	for _, w := range sum.Warnings {
		found = found || strings.Contains(w, "covers the newest")
	}
	if !found {
		t.Errorf("warnings = %v, want the window mentioned", sum.Warnings)
	}
	// The forgotten in-flight request completes: its usage still lands in the totals.
	b.at(time.Hour).resp("a", "a.1", 5, 995, 0, 1, nil)
	mustTail(t, s)
	if sum := s.Summary(); sum.Totals.Pending != 0 || sum.Totals.CacheRead != int64(n*990+5) {
		t.Errorf("late response: pending %d read %d", sum.Totals.Pending, sum.Totals.CacheRead)
	}
}

// genReader produces a session's JSONL lazily, so a test can stream far more
// bytes than it could hold.
type genReader struct {
	n, i int
	buf  bytes.Buffer
	seq  uint64
	sent int64
}

func (g *genReader) Read(p []byte) (int, error) {
	for g.buf.Len() < len(p) && g.i < g.n {
		g.i++
		id := fmt.Sprintf("a.%d", g.i)
		ts := t0.Add(time.Duration(g.i) * time.Second)
		write := func(typ string, data any) {
			g.seq++
			raw, _ := json.Marshal(data)
			line, _ := json.Marshal(events.Event{Seq: g.seq, TS: ts, Session: "big", Agent: "a", Type: typ, Data: raw})
			g.buf.Write(line)
			g.buf.WriteByte('\n')
		}
		write("model.request", m{"req": id, "agent": "a", "kind": "main", "model": "claude-sonnet-5-5", "provider": "p",
			"sections":    []m{sec("shared", 2000, h64("s")), sec("spine", 50+g.i%7, h64(fmt.Sprint("sp", g.i/40)))},
			"thread_from": 1 + g.i/40, "thread_to": g.i, "cache_key": "k", "renderer": "sleipnir-kv/1",
			"manifest": m{"tools": h64("tools"), "system": []string{h64("sys")}, "wire": h64(id)}})
		write("model.response", m{"req": id, "model": "claude-sonnet-5-5", "usage": core.Usage{InputTokens: 100, CacheReadTokens: 4900, OutputTokens: 20}, "cost_usd": 0.0012, "ttfb_ms": 100, "total_ms": 900})
		write("tool.call", m{"id": "c" + id, "name": "bash", "input": m{"command": strings.Repeat("echo hello; ", 20)}})
		write("tool.result", m{"id": "c" + id, "name": "bash", "chars": 400, "ms": 20})
	}
	if g.buf.Len() == 0 {
		return 0, io.EOF
	}
	n, _ := g.buf.Read(p)
	g.sent += int64(n)
	return n, nil
}

func TestHugeLogIsStreamedNotSlurped(t *testing.T) {
	n := 30000
	switch {
	case testing.Short():
		n = 4000
	case raceEnabled:
		n = 8000
	}
	g := &genReader{n: n}
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	s, err := LoadReader(g, Options{MaxRequests: 500})
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	sum := s.Summary()
	if sum.Totals.Requests != n || sum.Totals.CacheRead != int64(n*4900) || sum.Totals.ToolCalls != n {
		t.Fatalf("totals = %+v over %d requests: exact over the whole stream", sum.Totals, n)
	}
	near(t, "reported cost", sum.Cost.Reported, float64(n)*0.0012)
	page := s.Requests(RequestQuery{Tail: true, Limit: 20000})
	if page.Retained > 625 || page.Dropped != n-page.Retained {
		t.Errorf("retained %d dropped %d", page.Retained, page.Dropped)
	}
	// What is held afterwards is tiny next to what went through.
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("streamed %.1f MB, retained heap grew %.1f MB", float64(g.sent)/1e6, float64(grew)/1e6)
	if g.sent < int64(n)*1200 {
		t.Fatalf("the generator only produced %d bytes for %d requests", g.sent, n)
	}
	if limit := g.sent / 4; grew > limit || grew > 48<<20 {
		t.Errorf("heap grew by %d bytes while streaming %d: the log is being slurped", grew, g.sent)
	}
	// The sparse offset index is what keeps memory O(events/256).
	if len(s.index) > int(s.events)/indexEvery+2 {
		t.Errorf("index has %d entries for %d events", len(s.index), s.events)
	}
}

func TestLoadContextCancelsAndReportsProgress(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 3, Steps: 20})
	var last, total int64
	s, err := LoadContext(t.Context(), dir, Options{}, func(read, tot int64) { last, total = read, tot })
	if err != nil {
		t.Fatal(err)
	}
	if last != total || total == 0 || s.Summary().Totals.Requests == 0 {
		t.Errorf("progress ended at %d of %d", last, total)
	}
	cctx, ccancel := context.WithCancel(t.Context())
	ccancel()
	if _, err := LoadContext(cctx, dir, Options{}, nil); err == nil {
		t.Error("a cancelled load returned no error")
	}
}
