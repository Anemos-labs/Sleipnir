package openaichat_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/providertest"
)

// Frames an endpoint may send that are not the ones the wire format shows. Each
// case names what a caller must be able to rely on: an error that is an error, an
// answer that is whole, nothing that looks like one when it is not.
func TestHostileAndOddFrames(t *testing.T) {
	const finish = `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"
	for _, tc := range []struct {
		name string
		body string
		// wantKind is the error kind the caller must see; wantText the answer it must get
		// when there is no error.
		wantKind *provider.ErrKind
		wantText string
	}{
		{name: "an error frame whose message is not a string is still an error",
			body:     `data: {"error":{"message":{"detail":"upstream exploded"},"code":500}}` + "\n\n" + "data: [DONE]\n\n",
			wantKind: kind(provider.ErrServer)},
		{name: "an error frame that is a bare string is still an error",
			body:     `data: {"error":"upstream exploded"}` + "\n\n" + "data: [DONE]\n\n",
			wantKind: kind(provider.ErrServer)},
		{name: "an error frame with a string code keeps its text",
			body:     `data: {"error":{"message":"bad key","code":"invalid_api_key"}}` + "\n\n" + "data: [DONE]\n\n",
			wantKind: kind(provider.ErrServer)},
		{name: "an SSE error event with a plain-text payload is an error",
			body:     "event: error\ndata: upstream timed out\n\n" + "data: [DONE]\n\n",
			wantKind: kind(provider.ErrServer)},
		{name: "a frame with a wrongly typed member does not take the rest of the frame with it",
			body:     `data: {"choices":[{"index":0,"delta":{"content":"kept","reasoning_details":"not a list"}}]}` + "\n\n" + finish,
			wantText: "kept"},
		{name: "an empty body is not an answer",
			body:     "",
			wantKind: kind(provider.ErrNetwork)},
		{name: "keep-alives alone are not an answer",
			body:     ": ping\n\n: ping\n\n",
			wantKind: kind(provider.ErrNetwork)},
		{name: "text with no finish and no [DONE] is a dropped connection",
			body:     `data: {"choices":[{"index":0,"delta":{"content":"half an ans"}}]}` + "\n\n",
			wantKind: kind(provider.ErrNetwork)},
		{name: "[DONE] alone ends the stream",
			body: "data: [DONE]\n\n", wantText: ""},
		{name: "a finish reason alone ends the stream",
			body:     `data: {"choices":[{"index":0,"delta":{"content":"whole"},"finish_reason":"stop"}]}` + "\n\n",
			wantText: "whole"},
		{name: "frames after [DONE] are not read",
			body:     `data: {"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n" + `data: {"choices":[{"index":0,"delta":{"content":"late"}}]}` + "\n\n",
			wantText: "a"},
		{name: "non-JSON payloads are noise",
			body:     "data: hello\n\n" + `data: {"choices":[{"index":0,"delta":{"content":"x"}}]}` + "\n\n" + finish,
			wantText: "x"},
		{name: "a NUL and an escape in the model's own text come through untouched",
			body:     `data: {"choices":[{"index":0,"delta":{"content":"a\u0000b\u001b[31mc"},"finish_reason":"stop"}]}` + "\n\n",
			wantText: "a\x00b\x1b[31mc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := adapter(false).Run(t, []byte(tc.body), providertest.Whole)
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

// What a server puts in the identifiers it sends back, and in the message of an
// error, reaches the log and the terminal: it is cut and cleaned on the way in.
func TestServerTextInIdentifiersAndErrorsIsInert(t *testing.T) {
	esc := "\x1b]52;c;ZXZpbA==\x07\x1b[2J"
	long := strings.Repeat("y", 10_000)
	frame := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return "data: " + string(b) + "\n\n"
	}
	body := frame(map[string]any{
		"id": esc + long, "model": "m\u202eodel", "provider": "p\x00" + long,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "ok"}, "finish_reason": "stop"}},
	})
	o := adapter(false).Run(t, []byte(body), providertest.Whole)
	providertest.Check(t, o)
	if o.Err != nil {
		t.Fatal(o.Err)
	}
	if len(o.Resp.ID) > 256 || len(o.Resp.Provider) > 256 || o.Resp.Model != "model" {
		t.Errorf("id %d bytes, model %q, provider %d bytes", len(o.Resp.ID), o.Resp.Model, len(o.Resp.Provider))
	}

	body = frame(map[string]any{"error": map[string]any{"code": 503, "message": esc + "\n" + esc + long}})
	o = adapter(false).Run(t, []byte(body), providertest.Whole)
	providertest.Check(t, o)
	pe, ok := provider.AsError(o.Err)
	if !ok || pe.Kind != provider.ErrServer || pe.Status != 503 || !strings.HasSuffix(pe.Message, "…") {
		t.Fatalf("err = %#v", o.Err)
	}
}

// The code of an error that arrives inside a stream is whatever number the endpoint
// chose. It decides what kind of failure the caller sees; it is reported as an HTTP
// status only when it is one; and an error that says nothing still says that.
func TestInBandErrorCodesAndEmptyMessages(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		kind        provider.ErrKind
		status      int
		message     string // "" means: only that it is not empty
	}{
		{"rate limit", `{"error":{"code":429,"message":"slow down"}}`, provider.ErrRateLimit, 429, "slow down"},
		{"payment", `{"error":{"code":402,"message":"out of credit"}}`, provider.ErrPayment, 402, "out of credit"},
		{"auth", `{"error":{"code":401,"message":"bad key"}}`, provider.ErrAuth, 401, "bad key"},
		{"a vendor code of four digits is not an HTTP status", `{"error":{"code":1234,"message":"vendor"}}`, provider.ErrServer, 0, "vendor"},
		{"a negative code is not an HTTP status", `{"error":{"code":-5,"message":"odd"}}`, provider.ErrBadRequest, 0, "odd"},
		{"a code below 100 is not an HTTP status", `{"error":{"code":42,"message":"odd"}}`, provider.ErrBadRequest, 0, "odd"},
		{"a string code carries no status", `{"error":{"code":"rate_limit_exceeded","message":"x"}}`, provider.ErrServer, 0, "x"},
		{"a code and no message", `{"error":{"code":500}}`, provider.ErrServer, 500, ""},
		{"an empty error object", `{"error":{}}`, provider.ErrServer, 0, ""},
		{"a message of white space and escapes only", `{"error":{"code":502,"message":"\n\u001b[2J \t"}}`, provider.ErrServer, 502, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := adapter(false).Run(t, []byte("data: "+tc.frame+"\n\n"), providertest.Whole)
			providertest.Check(t, o)
			pe, ok := provider.AsError(o.Err)
			if !ok || pe.Kind != tc.kind || pe.Status != tc.status {
				t.Fatalf("err = %#v, want kind %v status %d", o.Err, tc.kind, tc.status)
			}
			if tc.message != "" && pe.Message != tc.message {
				t.Errorf("message = %q, want %q", pe.Message, tc.message)
			}
		})
	}
}

// A refusal with nothing in its body still says what happened.
func TestHTTPErrorWithAnEmptyBodyNamesItsStatus(t *testing.T) {
	a := adapter(false)
	a.Status = 502
	o := a.Run(t, nil, providertest.Whole)
	providertest.Check(t, o)
	pe, ok := provider.AsError(o.Err)
	if !ok || pe.Kind != provider.ErrServer || pe.Status != 502 || !strings.Contains(pe.Message, "502") {
		t.Fatalf("err = %#v", o.Err)
	}
}
