package anthropic_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/anthropic"
	"github.com/reee344/sleipnir/internal/provider/providertest"
)

// adapter is the Messages client under the stream harness. The base URL is never
// dialled: the harness answers every request itself.
func adapter() providertest.Adapter {
	return providertest.Adapter{
		Name: "anthropic", Model: "claude-opus-5-5",
		New: func(hc *http.Client) provider.Provider {
			return anthropic.New(anthropic.Config{BaseURL: "http://127.0.0.1:9", APIKey: "k", HTTPClient: hc})
		},
	}
}

// recorded is a stream as the Messages API sends it, written out by hand from the
// wire format (event names, then one JSON object per data line). Multi-byte
// characters are in the text raw (not as \u escapes), so that cutting the stream
// between their bytes is a case.
type recorded struct {
	name string
	body string
	// want checks that the stream decodes to what it says, so that comparing two
	// decodes of it is not comparing two empty answers.
	want func(t *testing.T, o providertest.Outcome)
}

func ev(name, json string) string { return "event: " + name + "\ndata: " + json + "\n\n" }

// crlf writes every line end as CRLF, which the SSE format allows.
func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

// unnamed drops the event: lines: the JSON carries the type as well, and gateways
// that re-frame a stream do not always keep the names.
func unnamed(s string) string {
	var out []string
	for _, l := range strings.SplitAfter(s, "\n") {
		if !strings.HasPrefix(l, "event:") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "")
}

func answer(t *testing.T, o providertest.Outcome) *provider.Response {
	t.Helper()
	if o.Err != nil {
		t.Fatalf("want an answer, got %v", o.Err)
	}
	return o.Resp
}

func eventTexts(o providertest.Outcome, k provider.EventKind) []string {
	var out []string
	for _, e := range o.Events {
		if e.Kind == k {
			out = append(out, e.Text)
		}
	}
	return out
}

var textStream = "" +
	ev("message_start", `{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"cache_creation_input_tokens":8,"cache_read_input_tokens":96,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":8},"output_tokens":1}}}`) +
	ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
	ev("ping", `{"type": "ping"}`) +
	ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Grüße, "}}`) +
	ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"世界 🐎 \ud83d\udc0e"}}`) +
	ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
	ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":6}}`) +
	ev("message_stop", `{"type":"message_stop"}`)

func wantText(t *testing.T, o providertest.Outcome) {
	r := answer(t, o)
	if got := r.Turn.PlainText(); got != "Grüße, 世界 🐎 🐎" {
		t.Errorf("text = %q", got)
	}
	if !reflect.DeepEqual(eventTexts(o, provider.EvText), []string{"Grüße, ", "世界 🐎 🐎"}) {
		t.Errorf("text events = %q", eventTexts(o, provider.EvText))
	}
	want := core.Usage{InputTokens: 25, CacheReadTokens: 96, CacheWrite1hTokens: 8, OutputTokens: 6}
	if r.Usage != want || r.ID != "msg_01" || r.Model != "claude-opus-5-5" || r.Stop != core.StopEnd {
		t.Errorf("usage = %+v id = %q model = %q stop = %v", r.Usage, r.ID, r.Model, r.Stop)
	}
}

var streams = []recorded{
	{name: "text with a ping, cache usage and raw multi-byte characters", body: textStream, want: wantText},

	{name: "the same stream with CRLF line ends", body: crlf(textStream), want: wantText},

	{name: "the same stream without event names", body: unnamed(textStream), want: wantText},

	{name: "two tool calls interleaved, arguments cut mid-escape and mid-character, blocks closed out of order", body: "" +
		ev("message_start", `{"type":"message_start","message":{"id":"msg_02","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":300,"output_tokens":1}}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Lecture de café ☕."}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"bash","input":{}}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\": \"sr"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"echo 世"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"c/ü.go\", \"note\": \"a \\\"q\\\" \\u00e9\"}"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"界\"}"}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":2}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":77}}`) +
		ev("message_stop", `{"type":"message_stop"}`),
		want: func(t *testing.T, o providertest.Outcome) {
			r := answer(t, o)
			calls := r.Turn.ToolCalls()
			if len(calls) != 2 || r.Stop != core.StopToolUse || r.Turn.PlainText() != "Lecture de café ☕." {
				t.Fatalf("calls = %+v stop = %v text = %q", calls, r.Stop, r.Turn.PlainText())
			}
			if calls[0].ToolID != "toolu_1" || calls[0].ToolName != "read" || calls[0].Invalid != "" ||
				string(calls[0].Input) != `{"path": "src/ü.go", "note": "a \"q\" \u00e9"}` {
				t.Errorf("first call = %+v (%s)", calls[0], calls[0].Input)
			}
			if calls[1].ToolID != "toolu_2" || string(calls[1].Input) != `{"command": "echo 世界"}` {
				t.Errorf("second call = %+v (%s)", calls[1], calls[1].Input)
			}
			var done []int
			for _, e := range o.Events {
				if e.Kind == provider.EvBlockDone {
					done = append(done, e.Index)
				}
			}
			if !reflect.DeepEqual(done, []int{0, 2, 1}) {
				t.Errorf("blocks finished in the order %v", done)
			}
		}},

	{name: "thinking with a signature in two pieces, redacted thinking, citations", body: "" +
		ev("message_start", `{"type":"message_start","message":{"id":"msg_03","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":100,"output_tokens":1}}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think — é "}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"🧠 step <two> & done."}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"AAAA"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"BBBB"}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"UkVEQUNURUQ="}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		ev("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"","citations":null}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Done ✓"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"char_location","cited_text":"x","document_index":0}}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":2}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":42,"output_tokens_details":{"thinking_tokens":30}}}`) +
		ev("message_stop", `{"type":"message_stop"}`),
		want: func(t *testing.T, o providertest.Outcome) {
			r := answer(t, o)
			b := r.Turn.Blocks
			if len(b) != 3 || b[0].Kind != core.BlockThinking || b[1].Kind != core.BlockRedactedThinking || b[2].Kind != core.BlockText {
				t.Fatalf("blocks = %+v", b)
			}
			if b[0].Text != "Let me think — é 🧠 step <two> & done." || !strings.Contains(string(b[0].Wire), `"signature":"AAAABBBB"`) {
				t.Errorf("thinking = %q wire = %s", b[0].Text, b[0].Wire)
			}
			if b[2].Text != "Done ✓" || r.Usage.OutputTokens != 42 || r.Usage.ReasoningTokens != 30 {
				t.Errorf("text = %q usage = %+v", b[2].Text, r.Usage)
			}
		}},

	{name: "an overload after some text, with escape sequences in its message", body: "" +
		ev("message_start", `{"type":"message_start","message":{"id":"msg_04","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":4,"output_tokens":1}}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial ü"}}`) +
		ev("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded\u001b]52;c;AAAA\u0007 \n\n now \u001b[2J"}}`),
		want: func(t *testing.T, o providertest.Outcome) {
			pe, isProv := provider.AsError(o.Err)
			if !isProv || pe.Kind != provider.ErrOverloaded || pe.Message != "overloaded_error: Overloaded now" {
				t.Fatalf("err = %#v", o.Err)
			}
			if got := eventTexts(o, provider.EvText); !reflect.DeepEqual(got, []string{"partial ü"}) {
				t.Errorf("text events = %q", got)
			}
		}},
}

// fixtures are the streams the package's own tests replay, as files.
func fixtures(t testing.TB) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "sse", "*.sse"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	sort.Strings(paths)
	out := map[string][]byte{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = b
	}
	return out
}

var _ = json.Valid
