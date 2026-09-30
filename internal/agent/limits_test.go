package agent_test

// Tests for the per-turn budgets (S24) and the other loop bounds of tranche 2:
// the number of tool calls one model turn may run, the characters its results may put
// into the next request, and what happens to the rest.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/tools"
	"github.com/reee344/sleipnir/internal/tools/recall"
)

// noticeSink records what an agent tells the person.
type noticeSink struct {
	agent.NopSink
	mu sync.Mutex
	n  []string
}

func (s *noticeSink) Notice(_, level, msg string) {
	s.mu.Lock()
	s.n = append(s.n, level+": "+msg)
	s.mu.Unlock()
}

func (s *noticeSink) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.n...)
}

// limRig is an agent over the in-process mock endpoint whose Config the test can adjust.
type limRig struct {
	agent   *agent.Agent
	log     *events.MemLog
	blobs   events.Blobs
	handles *tools.Handles
	archive *kv.Archive
	sink    *noticeSink
	reg     *tools.Registry
}

func newLimRig(t *testing.T, mutate func(*agent.Config), fts []fakeTool, r mock.Responder) *limRig {
	t.Helper()
	agent.RetryBase = time.Millisecond
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, r)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	client := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
	reg := tools.NewRegistry()
	for _, f := range fts {
		reg.Register(f)
	}
	blobs := events.NewMemBlobs()
	archive := kv.NewArchive(blobs)
	reg.Register(recall.New(archive))
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	model := cost.Model{ID: "mock-1", ContextTokens: 1_000_000, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}}
	rig := &limRig{log: events.NewMemLog(), blobs: blobs, handles: tools.NewHandles(), archive: archive, sink: &noticeSink{}, reg: reg}
	cfg := agent.Config{
		ID: "be-1", Role: "backend", Model: model, Provider: client, Tools: reg, ToolSpecs: specs,
		Const:  kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: strings.Repeat("You are a careful coding agent. ", 40)}}),
		Shared: kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "project", Text: strings.Repeat("The repo is a Go service. ", 60), Vol: kv.VolEpoch}}),
		Params: core.Params{MaxTokens: 512}, Events: rig.log, SessionID: "lim", MaxSteps: 60,
		Blobs: blobs, Handles: rig.handles, Archive: archive, Sink: rig.sink, Now: time.Now,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := agent.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rig.agent = a
	return rig
}

func manyCalls(name string, n int) []mock.ToolCall {
	var tcs []mock.ToolCall
	for i := 0; i < n; i++ {
		tcs = append(tcs, mock.ToolCall{ID: fmt.Sprintf("c%d", i), Name: name, Args: fmt.Sprintf(`{"n":%d}`, i)})
	}
	return tcs
}

// toolMessages returns the role=tool messages of a request, in order.
func toolMessages(c *mock.Call) []string {
	var out []string
	for _, m := range c.Messages {
		if m.Role == "tool" {
			out = append(out, m.Content)
		}
	}
	return out
}

func eventData(t *testing.T, log *events.MemLog, typ string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range log.OfType(typ) {
		var m map[string]any
		if err := json.Unmarshal(e.Data, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestSec_S24_CallsBeyondTheCapAreAnsweredNotRun(t *testing.T) {
	var ran atomic.Int32
	fts := []fakeTool{{name: "read", readOnly: true, run: func(json.RawMessage) *tools.Result { ran.Add(1); return &tools.Result{Text: "file content"} }}}
	var mu sync.Mutex
	var seen []string
	r := newLimRig(t, func(c *agent.Config) { c.MaxToolCallsPerTurn = 5 }, fts, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		if len(toolMessages(c)) == 0 {
			return mock.Reply{Text: "reading", ToolCalls: manyCalls("read", 20)}
		}
		seen = toolMessages(c)
		return mock.Reply{Text: "done"}
	})
	if _, err := r.agent.Run(context.Background(), "read everything"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if ran.Load() != 5 {
		t.Fatalf("%d calls ran, the cap is 5", ran.Load())
	}
	if len(seen) != 20 {
		t.Fatalf("every call needs an answer: %d tool messages for 20 calls", len(seen))
	}
	for i, m := range seen {
		switch {
		case i < 5 && m != "file content":
			t.Errorf("call %d should have run: %q", i, m)
		case i >= 5 && (!strings.Contains(m, "Not run") || !strings.Contains(m, "20 tool calls") || !strings.Contains(m, "at most 5")):
			t.Errorf("call %d should have been refused with an explanation: %q", i, m)
		}
	}
	if err := kv.Validate(r.agent.Thread().Snapshot().Turns); err != nil {
		t.Fatalf("the thread must stay a valid conversation: %v", err)
	}
	refused := 0
	for _, m := range eventData(t, r.log, events.TypeToolResult) {
		if meta, _ := m["meta"].(map[string]any); meta["error_kind"] == agent.ErrKindTooManyCalls && m["refused"] == true {
			refused++
		}
	}
	if refused != 15 {
		t.Errorf("%d refused calls in the log, want 15", refused)
	}
	if b := eventData(t, r.log, "tool.budget"); len(b) != 1 || b[0]["refused"] != float64(15) {
		t.Errorf("tool.budget events: %v", b)
	}
}

func TestSec_S24_TheFirstCallsRunInOrderWhateverTheirKind(t *testing.T) {
	var mu sync.Mutex
	var order []string
	rec := func(tag string) func(json.RawMessage) *tools.Result {
		return func(in json.RawMessage) *tools.Result {
			var v struct{ N int }
			_ = json.Unmarshal(in, &v)
			mu.Lock()
			order = append(order, fmt.Sprintf("%s%d", tag, v.N))
			mu.Unlock()
			return &tools.Result{Text: fmt.Sprintf("%s%d", tag, v.N)}
		}
	}
	fts := []fakeTool{{name: "write", run: rec("w")}, {name: "read", readOnly: true, run: rec("r")}}
	var seen []string
	r := newLimRig(t, func(c *agent.Config) { c.MaxToolCallsPerTurn = 4 }, fts, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		if len(toolMessages(c)) == 0 {
			return mock.Reply{ToolCalls: []mock.ToolCall{
				{ID: "c0", Name: "write", Args: `{"n":0}`}, {ID: "c1", Name: "read", Args: `{"n":1}`}, {ID: "c2", Name: "write", Args: `{"n":2}`},
				{ID: "c3", Name: "read", Args: `{"n":3}`}, {ID: "c4", Name: "write", Args: `{"n":4}`}, {ID: "c5", Name: "read", Args: `{"n":5}`}}}
		}
		seen = toolMessages(c)
		return mock.Reply{Text: "ok"}
	})
	if _, err := r.agent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "w0,r1,w2,r3" {
		t.Fatalf("ran %v, want the first four calls, writes as barriers, in order", order)
	}
	if len(seen) != 6 || seen[0] != "w0" || seen[3] != "r3" || !strings.Contains(seen[4], "Not run") || !strings.Contains(seen[5], "Not run") {
		t.Fatalf("results: %q", seen)
	}
}

func TestSec_S24_DefaultsAndUnlimited(t *testing.T) {
	for _, tc := range []struct {
		name         string
		calls, chars int // the Config
		asked        int // calls in the turn
		wantRan      int
		wantBytes    func(int) bool
	}{
		// The default cap is well above what a fan-out needs and well below a runaway; the
		// budget holds the turn to about 1.5 times 120,000 characters however many results.
		{"defaults", 0, 0, agent.DefaultMaxToolCalls + 40, agent.DefaultMaxToolCalls, func(n int) bool { return n < 200_000 }},
		{"defaults with 60 calls", 0, 0, 60, 60, func(n int) bool { return n < 200_000 }},
		{"unlimited", -1, -1, 60, 60, func(n int) bool { return n > 1_300_000 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ran atomic.Int32
			big := strings.Repeat("a line of source code\n", 1090) // ~24k chars
			fts := []fakeTool{{name: "read", readOnly: true, run: func(json.RawMessage) *tools.Result { ran.Add(1); return &tools.Result{Text: big} }}}
			var mu sync.Mutex
			total := 0
			r := newLimRig(t, func(c *agent.Config) { c.MaxToolCallsPerTurn, c.MaxTurnResultChars = tc.calls, tc.chars }, fts, func(c *mock.Call) mock.Reply {
				mu.Lock()
				defer mu.Unlock()
				if len(toolMessages(c)) == 0 {
					return mock.Reply{ToolCalls: manyCalls("read", tc.asked)}
				}
				total = 0
				for _, m := range toolMessages(c) {
					total += len(m)
				}
				return mock.Reply{Text: "ok"}
			})
			if _, err := r.agent.Run(context.Background(), "go"); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if int(ran.Load()) != tc.wantRan || !tc.wantBytes(total) {
				t.Fatalf("%d calls ran and %d bytes of results were sent (want %d calls)", ran.Load(), total, tc.wantRan)
			}
		})
	}
}

// Results that fit are untouched; the first that does not gets an excerpt and a handle,
// and every later one keeps a floor; the handle leads to the whole text, through the
// recall tool as the model would use it.
func TestSec_S24_ResultsOverBudgetSpillBehindARecallHandle(t *testing.T) {
	texts := map[string]string{}
	for i := 0; i < 4; i++ {
		texts[fmt.Sprintf("r%d", i)] = fmt.Sprintf("HEAD-%d ", i) + strings.Repeat(fmt.Sprintf("line %d of a long result\n", i), 260) + fmt.Sprintf("TAIL-%d", i) // ~6.5k chars
	}
	fts := []fakeTool{{name: "read", readOnly: true, run: func(in json.RawMessage) *tools.Result {
		var v struct{ Path string }
		_ = json.Unmarshal(in, &v)
		return &tools.Result{Text: texts[v.Path]}
	}}}
	var mu sync.Mutex
	var first, second []string
	var recalled string
	handleRe := regexp.MustCompile(`out_[0-9a-f]{16,}`)
	r := newLimRig(t, func(c *agent.Config) { c.MaxTurnResultChars = 10_000 }, fts, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		tm := toolMessages(c)
		switch {
		case len(tm) == 0:
			var tcs []mock.ToolCall
			for i := 0; i < 4; i++ {
				tcs = append(tcs, mock.ToolCall{ID: fmt.Sprintf("c%d", i), Name: "read", Args: fmt.Sprintf(`{"path":"r%d"}`, i)})
			}
			return mock.Reply{ToolCalls: tcs}
		case len(tm) == 4:
			first = tm
			h := handleRe.FindString(tm[3])
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "rc", Name: "recall", Args: fmt.Sprintf(`{"handle":%q,"limit":100000}`, h)}}}
		default:
			second = tm
			recalled = tm[len(tm)-1]
			return mock.Reply{Text: "done"}
		}
	})
	if _, err := r.agent.Run(context.Background(), "read four files"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if first[0] != texts["r0"] {
		t.Errorf("a result that fits must be sent whole: %d chars", len(first[0]))
	}
	for i := 1; i < 4; i++ {
		m := first[i]
		if !strings.Contains(m, fmt.Sprintf("HEAD-%d", i)) || !strings.Contains(m, fmt.Sprintf("TAIL-%d", i)) {
			t.Errorf("result %d lost its head or tail", i)
		}
		if !strings.Contains(m, "exceeded 10000 characters") || !regexp.MustCompile(`saved as out_[0-9a-f]{16,}: recall\(handle="out_[0-9a-f]{16,}"\)`).MatchString(m) {
			t.Errorf("result %d does not say where the rest is: ...%s", i, m[len(m)-200:])
		}
		if len(m) > 4000 {
			t.Errorf("result %d is %d chars: it was not cut", i, len(m))
		}
	}
	total := 0
	for _, m := range first {
		total += len(m)
	}
	if total > 10_000+3*(1500+400) {
		t.Errorf("the turn put %d characters into the next request against a budget of 10000 (+ floors)", total)
	}
	if len(second) != 5 || recalled != texts["r3"] {
		t.Fatalf("recall(handle) must return the whole text of the spilled result: got %d chars, want %d", len(recalled), len(texts["r3"]))
	}
	// The handle is in the session's table and resolves to the blob.
	h := handleRe.FindString(first[1])
	ref, n, ok := r.handles.Resolve(h)
	if !ok || n != len(texts["r1"]) {
		t.Fatalf("handle %s: ok=%v len=%d", h, ok, n)
	}
	if b, err := r.blobs.Get(ref); err != nil || string(b) != texts["r1"] {
		t.Fatalf("the blob behind %s is not the full result: %v", h, err)
	}
}

func TestSec_S24_BudgetKeepsErrorsSmallResultsAndNonTextBlocks(t *testing.T) {
	big := strings.Repeat("x", 30_000)
	fts := []fakeTool{
		{name: "big", readOnly: true, run: func(json.RawMessage) *tools.Result { return &tools.Result{Text: big} }},
		{name: "fail", readOnly: true, run: func(json.RawMessage) *tools.Result { return tools.Errorf("%s", big) }},
		{name: "small", readOnly: true, run: func(json.RawMessage) *tools.Result { return &tools.Result{Text: "tiny"} }},
		{name: "pic", readOnly: true, run: func(json.RawMessage) *tools.Result {
			return &tools.Result{Text: big, Blocks: []core.Block{{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "abc"}}}
		}},
	}
	r := newLimRig(t, func(c *agent.Config) { c.MaxTurnResultChars = 20_000 }, fts, func(c *mock.Call) mock.Reply {
		if len(toolMessages(c)) == 0 {
			return mock.Reply{ToolCalls: []mock.ToolCall{
				{ID: "a", Name: "big", Args: `{}`}, {ID: "b", Name: "fail", Args: `{}`}, {ID: "c", Name: "small", Args: `{}`}, {ID: "d", Name: "pic", Args: `{}`}}}
		}
		return mock.Reply{Text: "ok"}
	})
	if _, err := r.agent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	var results []core.Block
	for _, tr := range r.agent.Thread().Snapshot().Turns {
		if tr.Role == core.RoleUser && tr.Origin == core.OriginTool {
			results = tr.Blocks
		}
	}
	if len(results) != 4 {
		t.Fatalf("%d results", len(results))
	}
	if results[0].IsError || !results[1].IsError || results[2].PlainText() != "tiny" {
		t.Errorf("error flags or small results changed: %v %v %q", results[0].IsError, results[1].IsError, results[2].PlainText())
	}
	if len(results[1].PlainText()) > 6000 || !strings.Contains(results[1].PlainText(), "recall(handle=") {
		t.Errorf("an over-budget error result must be cut too: %d chars", len(results[1].PlainText()))
	}
	hasImage := false
	for _, c := range results[3].Result {
		hasImage = hasImage || c.Kind == core.BlockImage
	}
	if !hasImage {
		t.Errorf("a non-text block was dropped by the budget: %+v", results[3].Result)
	}
}

// Nothing is spilled while a turn stays inside its budget, and nothing about the request
// changes for it (no event, no marker).
func TestSec_S24_AnOrdinaryTurnIsUntouched(t *testing.T) {
	fts := []fakeTool{{name: "read", readOnly: true, run: func(json.RawMessage) *tools.Result {
		return &tools.Result{Text: strings.Repeat("ordinary output line\n", 500)}
	}}}
	var seen []string
	r := newLimRig(t, nil, fts, func(c *mock.Call) mock.Reply {
		if len(toolMessages(c)) == 0 {
			return mock.Reply{ToolCalls: manyCalls("read", 6)}
		}
		seen = toolMessages(c)
		return mock.Reply{Text: "ok"}
	})
	if _, err := r.agent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	for i, m := range seen {
		if m != strings.Repeat("ordinary output line\n", 500) {
			t.Errorf("result %d was altered (%d chars)", i, len(m))
		}
	}
	if b := eventData(t, r.log, "tool.budget"); len(b) != 0 {
		t.Errorf("a turn within budget logged %v", b)
	}
}
