package openaichat_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/provider/providertest"
)

// adapter is the OpenAI-style client under the stream harness. The base URL is
// never dialled: the harness answers every request itself.
func adapter(capture bool) providertest.Adapter {
	return providertest.Adapter{
		Name: "openaichat", Model: "vendor/model-x", Capture: capture,
		New: func(hc *http.Client) provider.Provider {
			cfg := openaichat.Config{Name: "t", BaseURL: "http://127.0.0.1:9/v1", APIKey: "k", HTTPClient: hc}
			if capture {
				p := openaichat.DefaultProfile("t", cfg.BaseURL)
				p.CaptureTokens = true
				cfg.Profile = &p
			}
			return openaichat.New(cfg)
		},
	}
}

// recorded is a stream as a gateway sends it, written out by hand from the wire
// format the adapter reads and the frames the mock endpoint and the existing tests
// use. Multi-byte characters are in the text raw (not as \u escapes), so that
// cutting the stream between their bytes is a case.
type recorded struct {
	name    string
	body    string
	capture bool
	// want checks that the stream decodes to what it says, so that comparing two
	// decodes of it is not comparing two empty answers.
	want func(t *testing.T, o providertest.Outcome)
}

func data(json string) string { return "data: " + json + "\n\n" }

// crlf writes every line end as CRLF, which the SSE format allows.
func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

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

var streams = []recorded{
	{name: "text, comments, CRLF frame, usage-only last chunk, [DONE]", body: "" +
		": HEIMDALL PROCESSING\n\n" +
		data(`{"id":"gen-1","object":"chat.completion.chunk","model":"vendor/model-x","provider":"Acme","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`) +
		data(`{"id":"gen-1","choices":[{"index":0,"delta":{"content":"Grüße, "}}]}`) +
		crlf(data(`{"id":"gen-1","choices":[{"index":0,"delta":{"content":"世界 🐎 🐎"}}]}`)) +
		": keep-alive\n\n" +
		"event: message\n" + data(`{"id":"gen-1","choices":[{"index":0,"delta":{"content":" — done. é"},"finish_reason":"stop"}]}`) +
		data(`{"id":"gen-1","model":"vendor/model-x","choices":[],"usage":{"prompt_tokens":120,"completion_tokens":17,"total_tokens":137,"prompt_tokens_details":{"cached_tokens":96,"cache_write_tokens":8},"cost":0.000123}}`) +
		"data: [DONE]\n\n",
		want: func(t *testing.T, o providertest.Outcome) {
			r := answer(t, o)
			if got := r.Turn.PlainText(); got != "Grüße, 世界 🐎 🐎 — done. é" {
				t.Errorf("text = %q", got)
			}
			if !reflect.DeepEqual(eventTexts(o, provider.EvText), []string{"Grüße, ", "世界 🐎 🐎", " — done. é"}) {
				t.Errorf("text events = %q", eventTexts(o, provider.EvText))
			}
			wantUsage := core.Usage{InputTokens: 16, CacheReadTokens: 96, CacheWrite5mTokens: 8, OutputTokens: 17}
			if r.Usage != wantUsage || r.CostUSD == nil || *r.CostUSD != 0.000123 {
				t.Errorf("usage = %+v cost = %v", r.Usage, r.CostUSD)
			}
			if r.ID != "gen-1" || r.Model != "vendor/model-x" || r.Provider != "Acme" || r.Stop != core.StopEnd {
				t.Errorf("id=%q model=%q provider=%q stop=%v", r.ID, r.Model, r.Provider, r.Stop)
			}
		}},

	{name: "two parallel tool calls with arguments cut mid-escape and mid-character", body: "" +
		data(`{"id":"c1","model":"vendor/model-x","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":""}}]}}]}`) +
		data(`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\": \"src/ma"}}]}}]}`) +
		data(`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"bash","arguments":""}}]}}]}`) +
		data(`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"in.go\", \"note\": \"café \\\"q\\\" 世"}}]}}]}`) +
		data(`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"command\":\"ls -la\"}"}}]}}]}`) +
		data(`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"界\"}"}}]}}]}`) +
		data(`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`) +
		data(`{"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":30,"total_tokens":40}}`) +
		"data: [DONE]\n\n",
		want: func(t *testing.T, o providertest.Outcome) {
			r := answer(t, o)
			calls := r.Turn.ToolCalls()
			if len(calls) != 2 || r.Stop != core.StopToolUse {
				t.Fatalf("calls = %+v stop = %v", calls, r.Stop)
			}
			if calls[0].ToolID != "call_a" || calls[0].ToolName != "read" || calls[0].Invalid != "" ||
				string(calls[0].Input) != `{"path": "src/main.go", "note": "café \"q\" 世界"}` {
				t.Errorf("first call = %+v (%s)", calls[0], calls[0].Input)
			}
			if calls[1].ToolID != "call_b" || calls[1].ToolName != "bash" || string(calls[1].Input) != `{"command":"ls -la"}` {
				t.Errorf("second call = %+v (%s)", calls[1], calls[1].Input)
			}
			if got := len(eventTexts(o, provider.EvToolDelta)); got != 4 {
				t.Errorf("%d tool deltas, want 4", got)
			}
		}},

	{name: "reasoning text and reasoning_details, answer cut by length", body: "" +
		": OPENROUTER PROCESSING\n\n" +
		data(`{"id":"r1","model":"vendor/thinker","provider":"Other","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"Let me think about ü"}}]}`) +
		data(`{"id":"r1","choices":[{"index":0,"delta":{"reasoning":" this.","reasoning_details":[{"type":"reasoning.text","text":"step ","index":0,"format":"unknown"}]}}]}`) +
		data(`{"id":"r1","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"one 🧠","signature":"sig-0","index":0},{"type":"reasoning.encrypted","data":"QUJD","index":1}]}}]}`) +
		data(`{"id":"r1","choices":[{"index":0,"delta":{"content":"The answer is 42"},"finish_reason":null}]}`) +
		data(`{"id":"r1","choices":[{"index":0,"delta":{"content":"."},"finish_reason":"length"}]}`) +
		data(`{"id":"r1","choices":[],"usage":{"prompt_tokens":50,"completion_tokens":40,"completion_tokens_details":{"reasoning_tokens":25},"cost":"0.0004"}}`) +
		"data: [DONE]\n\n",
		want: func(t *testing.T, o providertest.Outcome) {
			r := answer(t, o)
			if got := r.Turn.PlainText(); got != "The answer is 42." || r.Stop != core.StopMaxTokens {
				t.Errorf("text = %q stop = %v", got, r.Stop)
			}
			var think *core.Block
			for i := range r.Turn.Blocks {
				if r.Turn.Blocks[i].Kind == core.BlockThinking {
					think = &r.Turn.Blocks[i]
				}
			}
			if think == nil || think.Text != "Let me think about ü this." {
				t.Fatalf("thinking block = %+v", think)
			}
			var items []map[string]any
			if err := json.Unmarshal(think.Wire, &items); err != nil || len(items) != 2 ||
				items[0]["text"] != "step one 🧠" || items[0]["signature"] != "sig-0" || items[1]["data"] != "QUJD" {
				t.Errorf("reasoning_details = %s (%v)", think.Wire, err)
			}
			if r.Usage.ReasoningTokens != 25 || r.Usage.OutputTokens != 40 || r.CostUSD == nil || *r.CostUSD != 0.0004 {
				t.Errorf("usage = %+v cost = %v", r.Usage, r.CostUSD)
			}
		}},

	{name: "token capture: prompt ids, completion ids and logprobs", capture: true, body: "" +
		data(`{"id":"t1","model":"local","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"token_ids":[11,12],"logprobs":{"content":[{"logprob":-0.25},{"logprob":-1.5}]}}],"prompt_token_ids":[1,2,3,4]}`) +
		data(`{"id":"t1","choices":[{"index":0,"delta":{"content":"lo ✓"},"token_ids":[13],"logprobs":{"content":[{"logprob":-0.125}]}}]}`) +
		data(`{"id":"t1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`) +
		data(`{"id":"t1","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":3}}`) +
		"data: [DONE]\n\n",
		want: func(t *testing.T, o providertest.Outcome) {
			r := answer(t, o)
			if r.Turn.PlainText() != "Hello ✓" || r.Tokens == nil ||
				!reflect.DeepEqual(r.Tokens.PromptIDs, []int32{1, 2, 3, 4}) ||
				!reflect.DeepEqual(r.Tokens.CompletionIDs, []int32{11, 12, 13}) ||
				!reflect.DeepEqual(r.Tokens.Logprobs, []float32{-0.25, -1.5, -0.125}) {
				t.Errorf("text = %q tokens = %+v", r.Turn.PlainText(), r.Tokens)
			}
		}},

	{name: "an error after some text, with escape sequences in its message", body: "" +
		data(`{"id":"e1","choices":[{"index":0,"delta":{"content":"partial ü"}}]}`) +
		data(`{"error":{"code":429,"message":"rate limited\u001b]52;c;ZXZpbA==\u0007 slow\n\ndown \u001b[2J","metadata":{"provider_name":"Acme"}}}`) +
		"data: [DONE]\n\n",
		want: func(t *testing.T, o providertest.Outcome) {
			pe, isProv := provider.AsError(o.Err)
			if !isProv || pe.Kind != provider.ErrRateLimit || pe.Status != 429 || pe.Message != "rate limited slow down" {
				t.Fatalf("err = %#v", o.Err)
			}
			if got := eventTexts(o, provider.EvText); !reflect.DeepEqual(got, []string{"partial ü"}) {
				t.Errorf("text events = %q", got)
			}
		}},

	{name: "no [DONE]: the finish reason ends the stream", body: "" +
		data(`{"id":"n1","choices":[{"index":0,"delta":{"content":"ok"}}]}`) +
		data(`{"id":"n1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`) +
		data(`{"id":"n1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`),
		want: func(t *testing.T, o providertest.Outcome) {
			r := answer(t, o)
			if r.Turn.PlainText() != "ok" || r.Usage.InputTokens != 3 || r.Usage.OutputTokens != 1 {
				t.Errorf("text = %q usage = %+v", r.Turn.PlainText(), r.Usage)
			}
		}},
}
