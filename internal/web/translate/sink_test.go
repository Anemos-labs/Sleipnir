package translate

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// call is a tool_use block.
func call(id, name string, input any) core.Block {
	raw, _ := json.Marshal(input)
	return core.Block{Kind: core.BlockToolUse, ToolID: id, ToolName: name, Input: raw}
}

// The manager's streamed text is one say that names its message and more events that continue it, at most one per 100 ms, cut at
// white space; the message ends on the answer, on a tool start, on a retried request (reset) and at the end of the turn. A worker's
// text is a stream.
func TestSinkMapping(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	s := h.tr.Sink()
	step := func(d time.Duration, fn func()) {
		h.set(h.now().Add(d))
		fn()
		h.drain()
	}
	step(10*time.Millisecond, func() { s.Text("main", "Hello ") })
	for i := 0; i < 20; i++ { // 20 deltas over 200 ms: two more events, not twenty
		step(10*time.Millisecond, func() { s.Text("main", "word ") })
		if i%10 == 9 {
			h.advance(0)
		}
	}
	step(10*time.Millisecond, func() { s.Response("main", nil, 0) })
	step(10*time.Millisecond, func() { s.Text("main", "second ") })
	step(10*time.Millisecond, func() { s.ToolStart("main", call("c1", "read", map[string]any{"path": "/work/a.go"})) })
	step(10*time.Millisecond, func() { s.Text("main", "third ") })
	step(10*time.Millisecond, func() { s.(interface{ Reset(string) }).Reset("main") })
	step(10*time.Millisecond, func() { s.Text("main", "fourth ") })
	h.tr.Emit(&wire.Final{}, &wire.Turn{S: "end"})
	step(10*time.Millisecond, func() { s.Text("be-1", "worker prose ") })
	step(10*time.Millisecond, func() { s.Response("be-1", nil, 0) })

	evs := h.decoded()
	says, streams, mores := ofKind(evs, "say"), ofKind(evs, "stream"), ofKind(evs, "more")
	if len(says) != 4 || says[0]["who"] != "mgr" || says[0]["stream"] != true || says[0]["rate"] != 240.0 {
		t.Fatalf("says: %v", says)
	}
	if says[0]["mid"] != "m"+jsNum(says[0]["seq"]) {
		t.Fatalf("mid %v is not m+seq", says[0]["mid"])
	}
	if len(streams) != 1 || streams[0]["id"] != "be-1" || streams[0]["text"] != "worker prose " {
		t.Fatalf("streams: %v", streams)
	}
	var text strings.Builder
	text.WriteString(says[0]["text"].(string))
	ends, resets, firstMores := 0, 0, 0
	for _, m := range mores {
		if m["mid"] == says[0]["mid"] {
			firstMores++
			if s, ok := m["text"].(string); ok {
				text.WriteString(s)
			}
		}
		if m["end"] == true {
			ends++
		}
		if m["reset"] == true {
			resets++
		}
	}
	if want := "Hello " + strings.Repeat("word ", 20); text.String() != want {
		t.Fatalf("the first message is %q, want %q", text.String(), want)
	}
	if firstMores > 4 {
		t.Fatalf("%d more events for 200 ms of text: they are not coalesced", firstMores)
	}
	if ends != 5 || resets != 1 {
		t.Fatalf("%d ends (want 5: answer, tool start, reset, turn end, worker answer), %d resets", ends, resets)
	}
}

// jsNum writes a JSON number as an integer.
func jsNum(v any) string {
	f, _ := v.(float64)
	b, _ := json.Marshal(int64(f))
	return string(b)
}

// Each tool shows by its page name with its argument (VOCAB.md 8.2); a write shows its content as code, then its row with the lines
// written, then the end of its file's write; a patch is one row for every file and a diff for each; a refusal is a refused row, and a
// refusal for want of anyone to answer a refuse row.
func TestToolRows(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	s := h.tr.Sink()
	type tc struct {
		tool  string
		input map[string]any
		res   *tools.Result
		name  string
		arg   string
	}
	cases := []tc{
		{"bash", map[string]any{"command": "go test ./...\necho hi"}, &tools.Result{Text: "ok  example.com/a 0.1s\nok  example.com/b"}, "Bash", "go test ./... echo hi"},
		{"bash_output", map[string]any{"id": "job_1"}, &tools.Result{Text: "running"}, "Bash", "output job_1"},
		{"bash_kill", map[string]any{"id": "job_1"}, &tools.Result{Text: "killed"}, "Bash", "kill job_1"},
		{"read", map[string]any{"path": "/work/api/x.go"}, &tools.Result{Text: "1\n2\n3"}, "Read", "api/x.go"},
		{"edit", map[string]any{"path": "api/x.go"}, &tools.Result{Text: "Edited api/x.go", Meta: map[string]any{"path": "api/x.go", "added": 4, "removed": 2}}, "Edit", "api/x.go"},
		{"glob", map[string]any{"pattern": "**/*.go"}, &tools.Result{Text: "a.go"}, "Glob", "**/*.go"},
		{"grep", map[string]any{"pattern": "TODO", "path": "api"}, &tools.Result{Text: "x"}, "Grep", "TODO api"},
		{"ls", map[string]any{"path": "api"}, &tools.Result{Text: "x.go"}, "Ls", "api"},
		{"web_fetch", map[string]any{"url": "https://example.com/docs/a?b=c"}, &tools.Result{Text: "page"}, "WebFetch", "example.com/docs/a"},
		{"web_search", map[string]any{"query": "go generics"}, &tools.Result{Text: "results"}, "WebSearch", "go generics"},
		{"plan", map[string]any{"items": []map[string]any{{"step": "a"}, {"step": "b"}}}, &tools.Result{Text: "ok"}, "Plan", "2 steps"},
		{"task", map[string]any{"action": "claim", "id": "T4"}, &tools.Result{Text: "claimed"}, "TaskBoard", "claim T4"},
		{"spawn", map[string]any{"role": "backend", "task": "T4"}, &tools.Result{Text: "be-3 started; it will report through the board"}, "Spawn", "be-3"},
		{"mail", map[string]any{"to": "main", "text": "hi"}, &tools.Result{Text: "sent"}, "Mail", "→ mgr"},
		{"note", map[string]any{"text": "x"}, &tools.Result{Text: "noted"}, "Notes", "append"},
		{"wait", map[string]any{"until": []string{"T4"}}, &tools.Result{Text: "T4 merged"}, "Wait", "T4"},
		{"recall", map[string]any{"handle": "h7"}, &tools.Result{Text: "..."}, "Recall", "h7"},
		{"skill", map[string]any{"name": "release"}, &tools.Result{Text: "..."}, "Skill", "release"},
		{"mcp__github__search_issues", map[string]any{"query": "bug"}, &tools.Result{Text: "..."}, "github.search_issues", "bug"},
		{"frobnicate", map[string]any{"title": "x"}, &tools.Result{Text: "..."}, "Frobnicate", "x"},
	}
	for i, c := range cases {
		cl := call("c"+jsNum(float64(i)), c.tool, c.input)
		h.set(h.now().Add(10 * time.Millisecond))
		s.ToolStart("be-1", cl)
		s.ToolEnd("be-1", cl, c.res, time.Millisecond)
	}
	write := call("w", "write", map[string]any{"path": "/work/api/new.go", "content": "package api\n\nfunc A() {}\n"})
	patch := call("p", "apply_patch", map[string]any{"patch": "*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** Add File: b.go\n+z\n*** End Patch"})
	refused := call("r", "bash", map[string]any{"command": "rm -rf /"})
	nobody := call("n", "bash", map[string]any{"command": "npm i"})
	s.ToolStart("be-1", write)
	s.ToolEnd("be-1", write, &tools.Result{Text: "Created api/new.go (3 lines, 26 bytes)", Meta: map[string]any{"path": "api/new.go", "created": true}}, time.Millisecond)
	s.ToolStart("be-1", patch)
	s.ToolEnd("be-1", patch, &tools.Result{Text: "Patch applied", Meta: map[string]any{"files": []any{"a.go", "b.go"}, "added": 2, "removed": 1}}, time.Millisecond)
	s.ToolEnd("be-1", refused, &tools.Result{Text: "permission denied: rm -rf / is never allowed", IsError: true, Meta: map[string]any{"error_kind": "permission"}}, 0)
	s.ToolEnd("be-1", nobody, &tools.Result{Text: "approval required: npm i" + perm.NoOneToAsk, IsError: true}, 0)
	h.drain()
	evs := h.decoded()
	rows := ofKind(evs, "tool")
	if len(rows) != len(cases)+3 {
		t.Fatalf("%d rows for %d calls", len(rows), len(cases)+3)
	}
	for i, c := range cases {
		if rows[i]["name"] != c.name || rows[i]["arg"] != c.arg {
			t.Errorf("%s: name %v arg %v, want %s %q", c.tool, rows[i]["name"], rows[i]["arg"], c.name, c.arg)
		}
	}
	if rows[0]["out"] != "2 lines" || rows[4]["file"] != "api/x.go" || rows[4]["add"] != 4.0 || rows[4]["del"] != 2.0 {
		t.Errorf("bash and edit rows: %v %v", rows[0], rows[4])
	}
	w := rows[len(cases)]
	if w["name"] != "Write" || w["file"] != "api/new.go" || w["add"] != 3.0 {
		t.Errorf("write row: %v", w)
	}
	p := rows[len(cases)+1]
	if p["name"] != "Edit" || p["arg"] != "a.go, b.go" || p["file"] != "a.go" || p["add"] != 2.0 {
		t.Errorf("patch row: %v", p)
	}
	r := rows[len(cases)+2]
	if r["refused"] != true || r["ok"] != false || !strings.Contains(r["reason"].(string), "never allowed") || r["out"] != "" {
		t.Errorf("refused row: %v", r)
	}
	if ref := ofKind(evs, "refuse"); len(ref) != 1 || ref[0]["name"] != "Bash" || ref[0]["arg"] != "npm i" {
		t.Errorf("refuse rows: %v", ref)
	}
	code := ofKind(evs, "stream")
	if len(code) != 1 || code[0]["code"] != true || code[0]["file"] != "api/new.go" || code[0]["rate"] != 400.0 || !strings.HasPrefix(code[0]["text"].(string), "package api") {
		t.Errorf("code stream: %v", code)
	}
	var diffs []string
	for _, d := range ofKind(evs, "diff") {
		diffs = append(diffs, d["file"].(string))
	}
	if strings.Join(diffs, " ") != "api/x.go api/new.go a.go b.go" {
		t.Errorf("diffs: %v", diffs)
	}
	states := ofKind(evs, "state")
	if len(states) == 0 || states[0]["s"] != "tool" || states[0]["doing"] != "Bash go test ./... echo hi" {
		t.Errorf("the first tool start's state: %v", states)
	}
}

// A sink call never blocks, whatever the translator is doing: with its goroutine stuck in a publisher that does not return, 100,000
// calls return at once; when it can go on, it reports the loss once and sends every agent's state, token table and layers afresh.
func TestSinkNeverBlocks(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	var armed atomic.Bool
	publish := func(f wire.Frame) {
		if armed.Load() {
			once.Do(func() { <-release }) // the first frame after the setup blocks until the test lets it go
		}
	}
	tr := New(Config{Tab: "t", StartedAt: time.Now(), Publish: publish, Limits: Limits{SinkQueue: 1000}})
	defer tr.Close()
	// an agent the State knows, so that the fresh set has something in it
	tr.mu.Lock()
	b := newLog(time.Now())
	tr.applyLog(b.add(0, "be-1", "agent.spawn", map[string]any{"id": "be-1", "role": "backend"}))
	tr.applyLog(b.add(time.Millisecond, "be-1", "model.request", request("r1")))
	tr.applyLog(b.add(time.Millisecond, "be-1", "model.response", response("r1", 100)))
	tr.mu.Unlock()
	armed.Store(true)
	s := tr.Sink()
	s.Text("be-1", "first ") // its frame blocks the goroutine
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	for i := 0; i < 100_000; i++ {
		s.ToolStart("be-1", call("c", "bash", map[string]any{"command": "true"}))
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("100,000 sink calls took %v", d)
	}
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, evs, _, _ := tr.Journal()
		raw := string(lines(evs))
		if strings.Contains(raw, "the page fell behind") {
			if !strings.Contains(raw, `"k":"use","seq"`) || !strings.Contains(raw, `"k":"layers"`) {
				t.Fatalf("no fresh use and layers after the loss")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the loss was never reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The queue holds at most its bounds whatever comes: text deltas are dropped first and counted.
func TestSinkQueueBounds(t *testing.T) {
	n := 1_000_000
	if raceEnabled {
		n = 100_000
	}
	q := newSinkQueue(1000, 1<<20, make(chan struct{}, 1))
	for i := 0; i < n; i++ {
		if i%3 == 0 {
			q.push(sinkItem{op: opToolEnd, agent: "a", out: "x"})
		} else {
			q.push(sinkItem{op: opText, agent: "a" + string(rune('a'+i%2)), text: strings.Repeat("x", 100)})
		}
		if len(q.items) > 1000 || q.bytes > 1<<20 {
			t.Fatalf("after %d pushes: %d items, %d bytes", i, len(q.items), q.bytes)
		}
	}
	items, lost := q.take()
	if lost == 0 || len(items) == 0 {
		t.Fatalf("lost %d, kept %d", lost, len(items))
	}
	tools := 0
	for _, it := range items {
		if it.op == opToolEnd {
			tools++
		}
	}
	if tools < len(items)/2 {
		t.Fatalf("text was not dropped first: %d tool items of %d", tools, len(items))
	}
}
