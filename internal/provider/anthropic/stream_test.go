package anthropic_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/anthropic"
)

// recorder collects streaming events from concurrent callbacks.
type recorder struct {
	mu  sync.Mutex
	evs []provider.Event
}

func (r *recorder) on(e provider.Event) {
	r.mu.Lock()
	r.evs = append(r.evs, e)
	r.mu.Unlock()
}

func (r *recorder) kinds() []provider.EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]provider.EventKind, len(r.evs))
	for i, e := range r.evs {
		out[i] = e.Kind
	}
	return out
}

func (r *recorder) texts(kind provider.EventKind) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var sb strings.Builder
	for _, e := range r.evs {
		if e.Kind == kind {
			sb.WriteString(e.Text)
		}
	}
	return sb.String()
}

func (r *recorder) count(kind provider.EventKind) int {
	n := 0
	for _, k := range r.kinds() {
		if k == kind {
			n++
		}
	}
	return n
}

// serveFile serves a fixture. dribble writes it one byte at a time with a flush
// after each, which puts every possible chunk boundary through the SSE reader
// (mid-line, mid-escape, mid-UTF-8 sequence).
func serveFile(t *testing.T, path, contentType string, dribble bool) *httptest.Server {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", path))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Request-Id", "req_fixture")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		if !dribble {
			w.Write(data)
			return
		}
		for i := range data {
			w.Write(data[i : i+1])
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func hello(model string) *core.Prompt {
	return &core.Prompt{Model: model, Messages: []core.Message{user(core.Text("hi"))}, Params: core.Params{MaxTokens: 256}}
}

func newClient(ts *httptest.Server) *anthropic.Client {
	return anthropic.New(anthropic.Config{BaseURL: ts.URL, APIKey: "test-key-not-a-secret", StreamIdleTimeout: 5 * time.Second})
}

// callFixture runs one request against a fixture.
func callFixture(t *testing.T, path, ct string, dribble bool, req *provider.Request) (*provider.Response, *recorder, error) {
	t.Helper()
	ts := serveFile(t, path, ct, dribble)
	rec := &recorder{}
	if req == nil {
		req = &provider.Request{Prompt: hello("claude-opus-5-5")}
	}
	resp, err := newClient(ts).Do(context.Background(), req, rec.on)
	return resp, rec, err
}

const sse = "text/event-stream"

func wantKind(t *testing.T, err error, kind provider.ErrKind, retryable bool) *provider.Error {
	t.Helper()
	pe, ok := provider.AsError(err)
	if !ok {
		t.Fatalf("not a provider error: %v", err)
	}
	if pe.Kind != kind || pe.Retryable() != retryable {
		t.Fatalf("kind=%v retryable=%v (%v), want %v/%v", pe.Kind, pe.Retryable(), pe, kind, retryable)
	}
	return pe
}

type fixtureCase struct {
	file  string
	check func(t *testing.T, r *provider.Response, ev *recorder)
}

func TestStreamFixtures(t *testing.T) {
	cases := []fixtureCase{
		{"text.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			if got := r.Turn.PlainText(); got != "Hello, world "+string(rune(0xe9))+string(rune(0x1F40E)) {
				t.Errorf("text = %q", got)
			}
			if len(r.Turn.Blocks) != 1 || r.Turn.Blocks[0].Kind != core.BlockText || r.Turn.Blocks[0].Wire != nil {
				t.Errorf("blocks = %+v", r.Turn.Blocks)
			}
			if r.Usage != (core.Usage{InputTokens: 25, OutputTokens: 6}) {
				t.Errorf("usage = %+v", r.Usage)
			}
			if r.Stop != core.StopEnd || r.ID != "msg_fixture_text" || r.Model != "claude-opus-5-5" || r.Provider != "anthropic" || r.CostUSD != nil {
				t.Errorf("response = %+v", r)
			}
			if r.Turn.Role != core.RoleAssistant || r.Turn.Origin != core.OriginModel || r.Turn.Model != "claude-opus-5-5" {
				t.Errorf("turn = %+v", r.Turn)
			}
			want := `{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":25,"output_tokens":6}`
			if string(r.RawUsage) != want {
				t.Errorf("raw usage = %s, want %s", r.RawUsage, want)
			}
			k := ev.kinds()
			if k[0] != provider.EvStart || k[len(k)-1] != provider.EvUsage || ev.count(provider.EvText) != 2 || ev.count(provider.EvBlockDone) != 1 {
				t.Errorf("events = %v", k)
			}
			if ev.evs[0].RequestID != "msg_fixture_text" {
				t.Errorf("EvStart request id = %q", ev.evs[0].RequestID)
			}
			if r.TTFB <= 0 || r.Total < r.TTFB {
				t.Errorf("timings ttfb=%v total=%v", r.TTFB, r.Total)
			}
		}},
		{"thinking_signature.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			b := r.Turn.Blocks
			if len(b) != 2 || b[0].Kind != core.BlockThinking || b[1].Kind != core.BlockText {
				t.Fatalf("blocks = %+v", b)
			}
			if b[0].Text != "Let me think about <this> & that." || b[0].WireFormat != "anthropic" {
				t.Errorf("thinking = %+v", b[0])
			}
			// The replay form is the block as the API would have sent it whole.
			want := `{"type":"thinking","thinking":"Let me think about <this> & that.","signature":"sig-fixture-thinking-1"}`
			if string(b[0].Wire) != want {
				t.Errorf("wire = %s\nwant   %s", b[0].Wire, want)
			}
			if ev.texts(provider.EvThinking) != "Let me think about <this> & that." || ev.texts(provider.EvText) != "Done." {
				t.Errorf("events: thinking %q text %q", ev.texts(provider.EvThinking), ev.texts(provider.EvText))
			}
			if r.Usage.OutputTokens != 42 || r.Usage.ReasoningTokens != 30 {
				t.Errorf("usage = %+v", r.Usage)
			}
		}},
		{"thinking_hidden.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			// A model that hides its reasoning returns an empty thinking string with
			// the reasoning in the signature: the block must not be dropped as empty.
			b := r.Turn.Blocks
			if len(b) != 2 || b[0].Kind != core.BlockThinking || b[0].Text != "" {
				t.Fatalf("blocks = %+v", b)
			}
			want := `{"type":"thinking","thinking":"","signature":"sig-fixture-hidden-reasoning"}`
			if string(b[0].Wire) != want {
				t.Errorf("wire = %s", b[0].Wire)
			}
			if ev.count(provider.EvThinking) != 0 {
				t.Errorf("no thinking text was streamed")
			}
		}},
		{"redacted_thinking.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			b := r.Turn.Blocks
			if len(b) != 2 || b[0].Kind != core.BlockRedactedThinking {
				t.Fatalf("blocks = %+v", b)
			}
			// Verbatim, including a member this adapter has never heard of.
			want := `{"type":"redacted_thinking","data":"opaque-fixture-data","extra_future_field":{"a":[1,2]}}`
			if string(b[0].Wire) != want || b[0].WireFormat != "anthropic" {
				t.Errorf("wire = %s", b[0].Wire)
			}
		}},
		{"tool_use_json.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			calls := r.Turn.ToolCalls()
			if len(calls) != 2 || r.Stop != core.StopToolUse {
				t.Fatalf("calls = %+v stop = %v", calls, r.Stop)
			}
			// The input is the text the API streamed, concatenated: never re-encoded.
			in := "{\"path\": \"src/main.go\", \"lines\": [1, 2], \"note\": \"a \\\"quoted\\\" <tag> \\" + "u00e9\"}"
			if calls[0].ToolID != "toolu_fixture_1" || calls[0].ToolName != "read" || string(calls[0].Input) != in || calls[0].Invalid != "" {
				t.Errorf("call 0 = %+v", calls[0])
			}
			wire := `{"type":"tool_use","id":"toolu_fixture_1","name":"read","input":` + in + `}`
			if string(calls[0].Wire) != wire || calls[0].WireFormat != "anthropic" {
				t.Errorf("wire = %s", calls[0].Wire)
			}
			// A tool with no arguments arrives with no deltas at all.
			if string(calls[1].Input) != "{}" || string(calls[1].Wire) != `{"type":"tool_use","id":"toolu_fixture_2","name":"bash","input":{}}` {
				t.Errorf("call 1 = %+v wire %s", calls[1], calls[1].Wire)
			}
			if !json.Valid(calls[0].Wire) {
				t.Error("wire must be valid JSON")
			}
			if ev.count(provider.EvToolStart) != 2 || ev.count(provider.EvToolDelta) != 3 {
				t.Errorf("events = %v", ev.kinds())
			}
			if got := ev.texts(provider.EvToolDelta); got != in {
				t.Errorf("streamed deltas = %q", got)
			}
			var start provider.Event
			for _, e := range ev.evs {
				if e.Kind == provider.EvToolStart {
					start = e
					break
				}
			}
			if start.ToolID != "toolu_fixture_1" || start.ToolName != "read" || start.Index != 1 {
				t.Errorf("tool start = %+v", start)
			}
		}},
		{"tool_use_truncated.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			calls := r.Turn.ToolCalls()
			if len(calls) != 1 || calls[0].Invalid == "" || r.Stop != core.StopMaxTokens {
				t.Fatalf("calls = %+v stop = %v", calls, r.Stop)
			}
			// The harness reports the raw text back to the model; the replayable
			// form is an empty object because the API rejects anything else.
			var raw string
			if json.Unmarshal(calls[0].Input, &raw) != nil || raw != `{"path": "a.go", "old_string": "func main` {
				t.Errorf("input = %s", calls[0].Input)
			}
			if string(calls[0].Wire) != `{"type":"tool_use","id":"toolu_fixture_cut","name":"edit","input":{}}` {
				t.Errorf("wire = %s", calls[0].Wire)
			}
		}},
		{"citations.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			if r.Turn.PlainText() != "The sky is blue." || len(r.Turn.Blocks) != 1 {
				t.Errorf("turn = %+v", r.Turn)
			}
		}},
		{"ping_and_unknown.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			if r.Turn.PlainText() != "fine" || r.Stop != core.StopEnd {
				t.Errorf("turn = %+v stop = %v", r.Turn, r.Stop)
			}
			if ev.count(provider.EvStart) != 1 {
				t.Errorf("EvStart fires at message_start only: %v", ev.kinds())
			}
		}},
		{"cache_usage.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			want := core.Usage{InputTokens: 120, CacheReadTokens: 8000, CacheWrite5mTokens: 500, CacheWrite1hTokens: 1000, OutputTokens: 210, ReasoningTokens: 150}
			if r.Usage != want {
				t.Errorf("usage = %+v, want %+v", r.Usage, want)
			}
			if r.Usage.TotalInput() != 9620 {
				t.Errorf("total input = %d", r.Usage.TotalInput())
			}
			for _, k := range []string{`"ephemeral_1h_input_tokens":1000`, `"service_tier":"standard"`, `"thinking_tokens":150`} {
				if !strings.Contains(string(r.RawUsage), k) {
					t.Errorf("raw usage lacks %s: %s", k, r.RawUsage)
				}
			}
		}},
		{"delta_zero_input.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			// Some gateways repeat the input counters as zero on message_delta.
			// Counters only grow, so the larger value is kept.
			want := core.Usage{InputTokens: 77, CacheReadTokens: 900, CacheWrite5mTokens: 11, OutputTokens: 15}
			if r.Usage != want {
				t.Errorf("usage = %+v, want %+v", r.Usage, want)
			}
			if !strings.Contains(string(r.RawUsage), `"input_tokens":0`) {
				t.Errorf("the audit record keeps what was sent: %s", r.RawUsage)
			}
		}},
		{"refusal.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			if r.Stop != core.StopRefusal || r.StopDetail != "cyber: This request was declined." || len(r.Turn.Blocks) != 0 {
				t.Errorf("stop=%v detail=%q blocks=%d", r.Stop, r.StopDetail, len(r.Turn.Blocks))
			}
			if ev.count(provider.EvStart) != 1 || ev.count(provider.EvUsage) != 1 {
				t.Errorf("events = %v", ev.kinds())
			}
		}},
		{"pause_turn.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			if r.Stop != core.StopPause || len(r.Turn.Blocks) != 1 {
				t.Fatalf("stop=%v blocks=%+v", r.Stop, r.Turn.Blocks)
			}
			b := r.Turn.Blocks[0]
			// A server tool block is opaque to the harness and must be resent as is.
			if b.Kind != "server_tool_use" || b.WireFormat != "anthropic" || len(r.Turn.ToolCalls()) != 0 {
				t.Errorf("block = %+v", b)
			}
			if string(b.Wire) != `{"type":"server_tool_use","id":"srvtoolu_fixture_1","name":"web_search","input":{"query": "go 1.24"}}` {
				t.Errorf("wire = %s", b.Wire)
			}
		}},
		{"stop_sequence.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			if r.Stop != core.StopEnd || r.StopDetail != "</done>" || r.Turn.PlainText() != "one two" {
				t.Errorf("stop=%v detail=%q", r.Stop, r.StopDetail)
			}
		}},
		{"transformations.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			want := []provider.Transformation{
				{Type: "thinking_dropped", Path: "messages.1.content.0", Reason: "prefix_binding_mismatch"},
				{Type: "thinking_dropped", Path: "messages.3.content.0", Reason: "prefix_binding_mismatch"},
				{Type: "context_edit", Path: "clear_tool_uses_20250919", Reason: `{"type":"clear_tool_uses_20250919","cleared_tool_uses":3,"cleared_input_tokens":12000}`},
				{Type: "cache_miss", Reason: "messages_changed (812 input tokens)"},
			}
			if !reflect.DeepEqual(r.Transformations, want) {
				t.Errorf("transformations:\n got %+v\nwant %+v", r.Transformations, want)
			}
		}},
		{"empty_prewarm.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			if len(r.Turn.Blocks) != 0 || r.Stop != core.StopMaxTokens {
				t.Errorf("blocks=%d stop=%v", len(r.Turn.Blocks), r.Stop)
			}
			if r.Usage != (core.Usage{InputTokens: 3, CacheWrite1hTokens: 4000}) {
				t.Errorf("usage = %+v", r.Usage)
			}
		}},
		{"unknown_blocks.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			b := r.Turn.Blocks
			if len(b) != 3 {
				t.Fatalf("blocks = %+v", b)
			}
			if b[0].Kind != core.BlockCompaction || string(b[0].Wire) != `{"type":"compaction","content":"summary of earlier turns"}` {
				t.Errorf("compaction = %+v", b[0])
			}
			if b[1].Kind != "web_search_tool_result" || !strings.Contains(string(b[1].Wire), `"url":"https://example.com"`) {
				t.Errorf("unknown block = %+v", b[1])
			}
			if b[2].Kind != core.BlockText || b[2].Text != "after" {
				t.Errorf("empty text blocks are dropped: %+v", b[2])
			}
		}},
		{"no_message_stop.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			// A message_delta that carries stop_reason completes the message even if
			// a proxy dropped the closing event.
			if r.Turn.PlainText() != "complete enough" || r.Stop != core.StopEnd || ev.count(provider.EvBlockDone) != 1 {
				t.Errorf("turn = %+v events = %v", r.Turn, ev.kinds())
			}
		}},
		{"parallel_tools.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			calls := r.Turn.ToolCalls()
			if len(calls) != 2 || calls[0].ToolID != "toolu_p1" || string(calls[0].Input) != `{"path":"a"}` || calls[1].ToolID != "toolu_p2" || string(calls[1].Input) != `{"path":"b"}` {
				t.Errorf("calls = %+v", calls)
			}
			var done []int
			for _, e := range ev.evs {
				if e.Kind == provider.EvBlockDone {
					done = append(done, e.Index)
				}
			}
			if !reflect.DeepEqual(done, []int{1, 0}) {
				t.Errorf("EvBlockDone follows content_block_stop order: %v", done)
			}
		}},
		{"signature_cumulative.sse", func(t *testing.T, r *provider.Response, ev *recorder) {
			if !strings.Contains(string(r.Turn.Blocks[0].Wire), `"signature":"sig-fixture-complete"`) {
				t.Errorf("wire = %s", r.Turn.Blocks[0].Wire)
			}
		}},
	}
	for _, c := range cases {
		for _, dribble := range []bool{false, true} {
			name := strings.TrimSuffix(c.file, ".sse")
			if dribble {
				name += "/byte-by-byte"
			}
			t.Run(name, func(t *testing.T) {
				resp, ev, err := callFixture(t, "sse/"+c.file, sse, dribble, nil)
				if err != nil {
					t.Fatalf("%v", err)
				}
				c.check(t, resp, ev)
			})
		}
	}
}

func TestStreamFixtureFailures(t *testing.T) {
	cases := []struct {
		file      string
		kind      provider.ErrKind
		retryable bool
		contains  string
	}{
		{"cut_off.sse", provider.ErrNetwork, true, "before message_stop"},
		{"error_overloaded.sse", provider.ErrOverloaded, true, "overloaded_error: Overloaded"},
		{"error_before_start.sse", provider.ErrRateLimit, true, "slow down"},
		{"malformed_event.sse", provider.ErrServer, true, "malformed content_block_delta"},
	}
	for _, c := range cases {
		for _, dribble := range []bool{false, true} {
			t.Run(strings.TrimSuffix(c.file, ".sse"), func(t *testing.T) {
				resp, _, err := callFixture(t, "sse/"+c.file, sse, dribble, nil)
				if err == nil || resp != nil {
					t.Fatalf("want an error, got %+v", resp)
				}
				pe := wantKind(t, err, c.kind, c.retryable)
				if !strings.Contains(pe.Message, c.contains) {
					t.Errorf("message = %q", pe.Message)
				}
				if pe.Status != 0 {
					t.Errorf("an in-stream failure has no HTTP status: %d", pe.Status)
				}
			})
		}
	}
	t.Run("an empty body", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", sse)
		}))
		defer ts.Close()
		_, err := newClient(ts).Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
		pe := wantKind(t, err, provider.ErrNetwork, true)
		if !strings.Contains(pe.Message, "before message_start") {
			t.Errorf("message = %q", pe.Message)
		}
	})
}

func TestStreamIsIdenticalWhateverTheChunking(t *testing.T) {
	entries, err := os.ReadDir("testdata/sse")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Run(e.Name(), func(t *testing.T) {
			a, ea, errA := callFixture(t, "sse/"+e.Name(), sse, false, nil)
			b, eb, errB := callFixture(t, "sse/"+e.Name(), sse, true, nil)
			if (errA == nil) != (errB == nil) {
				t.Fatalf("whole: %v, dribbled: %v", errA, errB)
			}
			if errA != nil {
				pa, _ := provider.AsError(errA)
				pb, _ := provider.AsError(errB)
				if pa.Kind != pb.Kind || pa.Message != pb.Message {
					t.Fatalf("errors differ: %v vs %v", errA, errB)
				}
				return
			}
			if !reflect.DeepEqual(a.Turn, b.Turn) || a.Usage != b.Usage || a.Stop != b.Stop || !reflect.DeepEqual(a.Transformations, b.Transformations) || string(a.RawUsage) != string(b.RawUsage) {
				t.Fatalf("responses differ:\n%+v\n%+v", a, b)
			}
			if !reflect.DeepEqual(ea.kinds(), eb.kinds()) {
				t.Fatalf("events differ:\n%v\n%v", ea.kinds(), eb.kinds())
			}
		})
	}
}

func TestStreamCRLFLineEndings(t *testing.T) {
	data, err := os.ReadFile("testdata/sse/thinking_signature.sse")
	if err != nil {
		t.Fatal(err)
	}
	crlf := strings.ReplaceAll(string(data), "\n", "\r\n")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", sse)
		w.Write([]byte(crlf))
	}))
	defer ts.Close()
	resp, err := newClient(ts).Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
	if err != nil || len(resp.Turn.Blocks) != 2 || resp.Turn.Blocks[1].Text != "Done." {
		t.Fatalf("%v %+v", err, resp)
	}
}

func TestNonStreamingFixtures(t *testing.T) {
	t.Run("message with thinking, text and tool_use", func(t *testing.T) {
		resp, ev, err := callFixture(t, "json/message.json", "application/json", false, &provider.Request{Prompt: hello("claude-opus-5-5"), NoStream: true})
		if err != nil {
			t.Fatal(err)
		}
		b := resp.Turn.Blocks
		if len(b) != 3 || b[0].Kind != core.BlockThinking || b[0].Text != "plan it" || b[1].Text != "Running the tests." || b[2].Kind != core.BlockToolUse {
			t.Fatalf("blocks = %+v", b)
		}
		// Whole blocks are kept exactly as received (this includes "citations":null on text: not replayed).
		if string(b[0].Wire) != `{"type":"thinking","thinking":"plan it","signature":"sig-fixture-json-1"}` {
			t.Errorf("wire = %s", b[0].Wire)
		}
		if string(b[2].Input) != `{"command":"go test ./..."}` || string(b[2].Wire) != `{"type":"tool_use","id":"toolu_json_1","name":"bash","input":{"command":"go test ./..."}}` {
			t.Errorf("tool_use = %+v", b[2])
		}
		want := core.Usage{InputTokens: 30, CacheReadTokens: 1000, CacheWrite5mTokens: 200, OutputTokens: 55}
		if resp.Usage != want || resp.Stop != core.StopToolUse || resp.ID != "msg_fixture_json" || len(resp.Transformations) != 0 {
			t.Errorf("usage=%+v stop=%v id=%s", resp.Usage, resp.Stop, resp.ID)
		}
		// A non-streaming reply is replayed to consumers as the events a stream would have produced.
		k := ev.kinds()
		if k[0] != provider.EvStart || k[len(k)-1] != provider.EvUsage || ev.count(provider.EvBlockDone) != 3 || ev.count(provider.EvToolStart) != 1 ||
			ev.texts(provider.EvText) != "Running the tests." || ev.texts(provider.EvThinking) != "plan it" {
			t.Errorf("events = %v", k)
		}
		if ev.evs[0].RequestID != "req_fixture" {
			t.Errorf("EvStart request id = %q", ev.evs[0].RequestID)
		}
	})
	t.Run("a warm-up returns no content", func(t *testing.T) {
		resp, ev, err := callFixture(t, "json/warm_empty.json", "application/json", false, &provider.Request{Prompt: hello("claude-opus-5-5"), Warm: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Turn.Blocks) != 0 || resp.Stop != core.StopMaxTokens || resp.Usage != (core.Usage{InputTokens: 5, CacheWrite1hTokens: 2048}) {
			t.Errorf("%+v", resp)
		}
		if ev.count(provider.EvStart) != 1 || ev.count(provider.EvUsage) != 1 {
			t.Errorf("events = %v", ev.kinds())
		}
	})
	t.Run("an error object with status 200", func(t *testing.T) {
		_, _, err := callFixture(t, "json/error_in_200.json", "application/json", false, &provider.Request{Prompt: hello("m"), NoStream: true})
		wantKind(t, err, provider.ErrOverloaded, true)
	})
	t.Run("refusal", func(t *testing.T) {
		resp, _, err := callFixture(t, "json/refusal.json", "application/json", false, &provider.Request{Prompt: hello("m"), NoStream: true})
		if err != nil || resp.Stop != core.StopRefusal || resp.StopDetail != "" {
			t.Fatalf("%v %+v", err, resp)
		}
	})
	t.Run("transformations", func(t *testing.T) {
		resp, _, err := callFixture(t, "json/transformations.json", "application/json", false, &provider.Request{Prompt: hello("m"), NoStream: true})
		if err != nil {
			t.Fatal(err)
		}
		want := []provider.Transformation{
			{Type: "thinking_dropped", Path: "messages.1.content.0", Reason: "model_binding_mismatch"},
			{Type: "context_edit", Path: "clear_thinking_20251015", Reason: `{"type":"clear_thinking_20251015","cleared_thinking_turns":2,"cleared_input_tokens":900}`},
		}
		if !reflect.DeepEqual(resp.Transformations, want) {
			t.Errorf("%+v", resp.Transformations)
		}
	})
	t.Run("the server decides the format, not the request", func(t *testing.T) {
		// A streaming request answered with one JSON body (some gateways do this).
		resp, _, err := callFixture(t, "json/message.json", "application/json", false, nil)
		if err != nil || resp.Turn.ToolCalls()[0].ToolID != "toolu_json_1" {
			t.Fatalf("%v %+v", err, resp)
		}
		// A non-streaming request answered with an event stream.
		resp, _, err = callFixture(t, "sse/text.sse", sse, false, &provider.Request{Prompt: hello("m"), NoStream: true})
		if err != nil || resp.Turn.PlainText() == "" {
			t.Fatalf("%v %+v", err, resp)
		}
	})
	t.Run("unparseable bodies", func(t *testing.T) {
		for _, body := range []string{"", "not json", `[1,2]`, `{"content":"nope"}`} {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(body))
			}))
			_, err := newClient(ts).Do(context.Background(), &provider.Request{Prompt: hello("m"), NoStream: true}, nil)
			ts.Close()
			wantKind(t, err, provider.ErrServer, true)
		}
	})
}
