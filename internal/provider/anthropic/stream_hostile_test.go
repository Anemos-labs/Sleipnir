package anthropic_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/providertest"
)

const (
	start    = `{"type":"message_start","message":{"id":"msg_h","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":7,"output_tokens":1}}}`
	stopTurn = `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`
)

// Sequences the API never sends, and sequences that are legitimate and unusual. Each
// case names what a caller must be able to rely on: a message that never began is
// not an answer, an error event is an error, and the events that are merely
// unexpected are skipped without taking the rest of the message with them.
func TestHostileAndOddEventSequences(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		// wantKind is the error kind the caller must see; wantText the answer it must get
		// when there is no error.
		wantKind *provider.ErrKind
		wantText string
	}{
		{name: "message_stop alone is not an answer",
			body:     ev("message_stop", `{"type":"message_stop"}`),
			wantKind: kind(provider.ErrServer)},
		{name: "a stop reason with no message before it is not an answer",
			body:     ev("message_delta", stopTurn),
			wantKind: kind(provider.ErrNetwork)},
		{name: "a stop reason and message_stop with no message before them are not an answer",
			body:     ev("message_delta", stopTurn) + ev("message_stop", `{"type":"message_stop"}`),
			wantKind: kind(provider.ErrServer)},
		{name: "blocks with no message around them are not an answer",
			body: ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ghost"}}`) +
				ev("message_stop", `{"type":"message_stop"}`),
			wantKind: kind(provider.ErrServer)},
		{name: "a message with no content is an empty answer",
			body:     ev("message_start", start) + ev("message_delta", stopTurn) + ev("message_stop", `{"type":"message_stop"}`),
			wantText: ""},
		{name: "an empty body is not an answer", body: "", wantKind: kind(provider.ErrNetwork)},
		{name: "keep-alives alone are not an answer", body: ": ping\n\n" + ev("ping", `{"type":"ping"}`), wantKind: kind(provider.ErrNetwork)},
		{name: "an error event without an error object is an error",
			body:     ev("message_start", start) + ev("error", `{"type":"error","error":null}`),
			wantKind: kind(provider.ErrServer)},
		{name: "an error event that is not JSON is an error",
			body:     ev("message_start", start) + ev("error", `upstream exploded`),
			wantKind: kind(provider.ErrServer)},
		{name: "an API error type says what to do about it",
			body:     ev("error", `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`),
			wantKind: kind(provider.ErrRateLimit)},
		{name: "an unknown event with a payload that is not JSON is noise",
			body: ev("message_start", start) + ev("future_event", `not json at all`) +
				ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":0}`) + ev("message_delta", stopTurn) + ev("message_stop", `{"type":"message_stop"}`),
			wantText: "ok"},
		{name: "deltas for blocks that never started, and stops for them, are skipped",
			body: ev("message_start", start) +
				ev("content_block_delta", `{"type":"content_block_delta","index":9,"delta":{"type":"text_delta","text":"ghost"}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":9}`) +
				ev("content_block_start", `{"type":"content_block_start","index":-4,"content_block":{"type":"text","text":""}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":-4,"delta":{"type":"text_delta","text":"odd index"}}`) +
				ev("message_delta", stopTurn) + ev("message_stop", `{"type":"message_stop"}`),
			wantText: "odd index"},
		{name: "a text block that starts with text keeps it in front of its deltas",
			body: ev("message_start", start) +
				ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Hello, "}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":0}`) + ev("message_delta", stopTurn) + ev("message_stop", `{"type":"message_stop"}`),
			wantText: "Hello, world"},
		{name: "the model's own escape sequences and NULs come through untouched",
			body: ev("message_start", start) +
				ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a\u0000b\u001b[31mc"}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":0}`) + ev("message_delta", stopTurn) + ev("message_stop", `{"type":"message_stop"}`),
			wantText: "a\x00b\x1b[31mc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := adapter().Run(t, []byte(tc.body), providertest.Whole)
			providertest.Check(t, o)
			if tc.wantKind != nil {
				pe, ok := provider.AsError(o.Err)
				if !ok || pe.Kind != *tc.wantKind {
					t.Fatalf("want an error of kind %v, got err=%v resp=%v", *tc.wantKind, o.Err, o.Resp != nil)
				}
				return
			}
			if o.Err != nil {
				t.Fatalf("want an answer, got %v", o.Err)
			}
			if got := o.Resp.Turn.PlainText(); got != tc.wantText {
				t.Fatalf("answer = %q, want %q", got, tc.wantText)
			}
		})
	}
}

func kind(k provider.ErrKind) *provider.ErrKind { return &k }

// What a server puts in the identifiers it sends back, in a stop reason and in the
// message of an error reaches the log and the terminal: it is cut and cleaned on
// the way in.
func TestServerTextInIdentifiersAndErrorsIsInert(t *testing.T) {
	esc := "\x1b]52;c;ZXZpbA==\x07\x1b[2J"
	long := strings.Repeat("y", 10_000)
	frame := func(name string, v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return ev(name, string(b))
	}
	body := frame("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": esc + long, "model": "m\u202eodel" + long, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	}}) + ev("message_delta", stopTurn) + ev("message_stop", `{"type":"message_stop"}`)
	o := adapter().Run(t, []byte(body), providertest.Whole)
	providertest.Check(t, o)
	if o.Err != nil {
		t.Fatal(o.Err)
	}
	if len(o.Resp.ID) > 256 || !strings.HasPrefix(o.Resp.Model, "model") || len(o.Resp.Model) > 256 {
		t.Errorf("id %d bytes, model %q (%d bytes)", len(o.Resp.ID), o.Resp.Model[:10], len(o.Resp.Model))
	}

	body = ev("message_start", start) + frame("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": esc + "\n" + esc + long}})
	o = adapter().Run(t, []byte(body), providertest.Whole)
	providertest.Check(t, o)
	pe, ok := provider.AsError(o.Err)
	if !ok || pe.Kind != provider.ErrServer || !strings.HasSuffix(pe.Message, "…") {
		t.Fatalf("err = %#v", o.Err)
	}
}

var _ = core.StopEnd
