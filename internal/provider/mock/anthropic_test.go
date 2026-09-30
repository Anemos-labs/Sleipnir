package mock

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/provider"
)

// ---------------------------------------------------------------------------
// helpers

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (r reply) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("not JSON (status %d): %s", r.status, r.body)
	}
	return m
}

func (r reply) errMsg(t *testing.T) string {
	t.Helper()
	e, _ := r.json(t)["error"].(map[string]any)
	if e == nil {
		return ""
	}
	return fmt.Sprint(e["message"])
}

func (r reply) errType(t *testing.T) string {
	t.Helper()
	e, _ := r.json(t)["error"].(map[string]any)
	if e == nil {
		return ""
	}
	return fmt.Sprint(e["type"])
}

// post sends a request. headers overrides the defaults; an empty value removes one.
func post(t *testing.T, ts *httptest.Server, body string, headers ...string) reply {
	t.Helper()
	hr, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Anthropic-Version", "2023-06-01")
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			hr.Header.Del(headers[i])
		} else {
			hr.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: b}
}

func newA(t *testing.T, cfg AnthropicConfig, r Responder) (*AnthropicServer, *httptest.Server) {
	t.Helper()
	srv := NewAnthropic(cfg, r)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	return srv, ts
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type obj = map[string]any

func text(s string) obj { return obj{"type": "text", "text": s} }

func userMsg(blocks ...obj) obj { return obj{"role": "user", "content": blocks} }

func req(fields obj, msgs ...obj) string {
	body := obj{"model": "mock-1", "max_tokens": 256, "messages": msgs}
	for k, v := range fields {
		body[k] = v
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func withCC(b obj, ttl string) obj {
	c := obj{}
	for k, v := range b {
		c[k] = v
	}
	cc := obj{"type": "ephemeral"}
	if ttl != "" {
		cc["ttl"] = ttl
	}
	c["cache_control"] = cc
	return c
}

func big(n int, word string) string { return strings.Repeat(word+" ", n) }

// sseEvent is one parsed server-sent event.
type sseEv struct {
	name string
	data map[string]any
	raw  string
}

func readSSE(t *testing.T, body []byte) []sseEv {
	t.Helper()
	r := provider.NewSSEReader(bytes.NewReader(body))
	var out []sseEv
	for {
		ev, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(ev.Data, &m); err != nil {
			t.Fatalf("event %s is not JSON: %s", ev.Event, ev.Data)
		}
		out = append(out, sseEv{name: ev.Event, data: m, raw: string(ev.Data)})
	}
}

func eventNames(evs []sseEv) string {
	var n []string
	for _, e := range evs {
		n = append(n, e.name)
	}
	return strings.Join(n, ",")
}

func usageOfMsg(t *testing.T, m map[string]any) map[string]float64 {
	t.Helper()
	u, _ := m["usage"].(map[string]any)
	out := map[string]float64{}
	for k, v := range u {
		if f, ok := v.(float64); ok {
			out[k] = f
		}
	}
	return out
}

// ---------------------------------------------------------------------------

func TestAnthropicResponseShapeAndUsage(t *testing.T) {
	srv, ts := newA(t, AnthropicConfig{MinPrefixTokens: 100}, func(c *Call) Reply { return Reply{Text: "hello"} })
	sys := []obj{withCC(text(big(400, "alpha")), "")}
	body := req(obj{"system": sys}, userMsg(text("hi")))

	r1 := post(t, ts, body)
	if r1.status != 200 {
		t.Fatalf("%d %s", r1.status, r1.body)
	}
	m := r1.json(t)
	for _, k := range []string{"id", "type", "role", "model", "content", "stop_reason", "usage"} {
		if _, ok := m[k]; !ok {
			t.Errorf("response lacks %q: %s", k, r1.body)
		}
	}
	if m["type"] != "message" || m["role"] != "assistant" || m["stop_reason"] != "end_turn" || m["model"] != "mock-1" {
		t.Errorf("message = %v", m)
	}
	if r1.header.Get("Request-Id") == "" {
		t.Error("missing request-id header")
	}
	u := usageOfMsg(t, m)
	if u["cache_creation_input_tokens"] <= 0 || u["cache_read_input_tokens"] != 0 {
		t.Fatalf("first call writes: %v", u)
	}
	cc := m["usage"].(map[string]any)["cache_creation"].(map[string]any)
	if cc["ephemeral_5m_input_tokens"].(float64) != u["cache_creation_input_tokens"] || cc["ephemeral_1h_input_tokens"].(float64) != 0 {
		t.Errorf("cache_creation split = %v", cc)
	}

	r2 := post(t, ts, body)
	u2 := usageOfMsg(t, r2.json(t))
	if u2["cache_read_input_tokens"] != u["cache_creation_input_tokens"] || u2["cache_creation_input_tokens"] != 0 {
		t.Fatalf("second call reads what the first wrote: %v vs %v", u2, u)
	}
	// input_tokens is the uncached remainder only: the user turn after the marker.
	if u2["input_tokens"] <= 0 || u2["input_tokens"] > 5 {
		t.Errorf("input_tokens = %v", u2["input_tokens"])
	}

	st := srv.AnthropicStats()
	if len(st) != 2 || st[1].Read != int(u2["cache_read_input_tokens"]) || st[1].Cached != st[1].Read {
		t.Fatalf("stats %+v", st)
	}
	// The embedded Server's stats are recorded too, like the chat mock.
	if cs := srv.Stats(); len(cs) != 2 || cs[1].Cached != st[1].Read || cs[1].PromptTokens != st[1].PromptTokens || cs[1].Status != 200 {
		t.Fatalf("chat-style stats %+v", cs)
	}
	if st[1].PromptTokens != st[1].Read+st[1].Write5m+st[1].Write1h+st[1].Uncached {
		t.Fatalf("usage does not add up: %+v", st[1])
	}
}

func TestAnthropicRequestValidation(t *testing.T) {
	sys5 := func(n int) obj { return withCC(text(big(50, fmt.Sprint("w", n))), "") }
	toolUse := obj{"role": "assistant", "content": []obj{{"type": "tool_use", "id": "toolu_1", "name": "bash", "input": obj{}}}}
	result := func(id string) obj {
		return userMsg(obj{"type": "tool_result", "tool_use_id": id, "content": "ok"})
	}
	tools := []obj{{"name": "bash", "description": "d", "input_schema": obj{"type": "object"}}}
	hot := func(extra obj) obj {
		m := obj{"role": "system", "clear_at": "next_user_message", "content": []obj{text("hot")}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name    string
		body    string
		headers []string
		status  int
		typ     string
		want    string
	}{
		{"missing anthropic-version", req(nil, userMsg(text("hi"))), []string{"Anthropic-Version", ""}, 400, "invalid_request_error", "anthropic-version: header is required"},
		{"bad JSON", `{"model":`, nil, 400, "invalid_request_error", "Invalid JSON"},
		{"unknown top-level member", req(obj{"tool_choise": 1}, userMsg(text("hi"))), nil, 400, "", "tool_choise: Extra inputs are not permitted"},
		{"missing max_tokens", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, nil, 400, "", "max_tokens: Field required"},
		{"no messages", `{"model":"m","max_tokens":5,"messages":[]}`, nil, 400, "", "at least one message"},
		{"first message is a system message", req(nil, obj{"role": "system", "content": "x"}, userMsg(text("hi"))), []string{"Anthropic-Beta", ""}, 400, "", "cannot be the first message"},
		{"empty text block", req(nil, userMsg(text(""))), nil, 400, "", "text content blocks must be non-empty"},
		{"whitespace text block", req(nil, userMsg(text(" \n"))), nil, 400, "", "must contain non-whitespace text"},
		{"cache_control on an empty text block", req(nil, userMsg(withCC(text(""), ""))), nil, 400, "", "cache_control cannot be set for empty text blocks"},
		{"bad cache_control type", req(nil, userMsg(obj{"type": "text", "text": "x", "cache_control": obj{"type": "forever"}})), nil, 400, "", "Input should be 'ephemeral'"},
		{"bad ttl", req(nil, userMsg(withCC(text("x"), "2h"))), nil, 400, "", "Input should be '5m' or '1h'"},
		{"cache_control on a thinking block", req(nil, userMsg(text("q")),
			obj{"role": "assistant", "content": []obj{{"type": "thinking", "thinking": "t", "signature": "s", "cache_control": obj{"type": "ephemeral"}}, text("a")}}, userMsg(text("more"))),
			nil, 400, "", "cache_control: Extra inputs are not permitted"},
		{"five breakpoints", req(nil, userMsg(withCC(text("a"), ""), withCC(text("b"), ""), withCC(text("c"), ""), withCC(text("d"), ""), withCC(text("e"), ""))),
			nil, 400, "", "A maximum of 4 blocks with cache_control may be provided. Found 5."},
		{"1h after 5m", req(nil, userMsg(withCC(text("a"), ""), withCC(text("b"), "1h"))), nil, 400, "", "ttl='1h' cache_control block must not come after"},
		{"unanswered tool_use", req(obj{"tools": tools}, userMsg(text("q")), toolUse, userMsg(text("no result"))), nil, 400, "", "`tool_use` ids were found without `tool_result` blocks immediately after: toolu_1"},
		{"tool_use as the last message", req(obj{"tools": tools}, userMsg(text("q")), toolUse), nil, 400, "", "`tool_use` ids were found without `tool_result` blocks immediately after"},
		{"orphan tool_result", req(obj{"tools": tools}, userMsg(text("q")), result("toolu_9")), nil, 400, "", "unexpected `tool_use_id` found in `tool_result` blocks: toolu_9"},
		{"tool_result after text", req(obj{"tools": tools}, userMsg(text("q")), toolUse, userMsg(text("first"), obj{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok"})), nil, 400, "", "`tool_result` blocks must come first"},
		{"a system message needs the beta for clear_at", req(nil, userMsg(text("q")), hot(nil)), nil, 400, "", "clear_at: Extra inputs are not permitted"},
		{"clear_at with a bad value", req(nil, userMsg(text("q")), hot(obj{"clear_at": "tomorrow"})), []string{"Anthropic-Beta", betaClearAt}, 400, "", "Input should be 'never' or 'next_user_message'"},
		{"a system message after an assistant message", req(nil, userMsg(text("q")), obj{"role": "assistant", "content": "a"}, hot(nil)), []string{"Anthropic-Beta", betaClearAt}, 400, "", "must follow a user message"},
		{"a system message followed by a user message", req(nil, userMsg(text("q")), hot(nil), userMsg(text("again"))), []string{"Anthropic-Beta", betaClearAt}, 400, "", "must be the last message or be followed by an assistant message"},
		{"cache_control on a system message", req(nil, userMsg(text("q")), hot(obj{"content": []obj{withCC(text("hot"), "")}})), []string{"Anthropic-Beta", betaClearAt}, 400, "", "not allowed on a system message"},
		{"unknown beta", req(nil, userMsg(text("q"))), []string{"Anthropic-Beta", "no-such-feature-2099-01-01"}, 400, "", "Unexpected value(s) `no-such-feature-2099-01-01` for the `anthropic-beta` header"},
		{"block_binding without its beta", req(obj{"thinking": obj{"type": "adaptive", "block_binding": obj{"prefix_mismatch_behavior": "drop_block"}}}, userMsg(text("q"))), nil, 400, "", "block_binding: Extra inputs are not permitted"},
		{"bad binding mode", req(obj{"thinking": obj{"type": "adaptive", "block_binding": obj{"prefix_mismatch_behavior": "ignore"}}}, userMsg(text("q"))), []string{"Anthropic-Beta", betaBinding}, 400, "", "Input should be 'error' or 'drop_block'"},
		{"temperature on a model that removed it", req(obj{"model": "claude-opus-5-5", "temperature": 0.5}, userMsg(text("q"))), nil, 400, "", "`temperature` is not supported"},
		{"thinking disabled on an always-thinking model", req(obj{"model": "claude-opus-5-5", "thinking": obj{"type": "disabled"}}, userMsg(text("q"))), nil, 400, "", `"thinking.type.disabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`},
		{"thinking budget on a model that removed it", req(obj{"model": "claude-opus-5-5", "thinking": obj{"type": "enabled", "budget_tokens": 2000}}, userMsg(text("q"))), nil, 400, "", `"thinking.type.enabled" is not supported for this model`},
		{"adaptive thinking on a budget-only model", req(obj{"model": "claude-haiku-4-5", "thinking": obj{"type": "adaptive"}}, userMsg(text("q"))), nil, 400, "", `"thinking.type.adaptive" is not supported for this model`},
		{"forced tool_choice on a model that removed it", req(obj{"model": "claude-opus-5-5", "tools": tools, "tool_choice": obj{"type": "any"}}, userMsg(text("q"))), nil, 400, "", `tool_choice: type "tool" and "any" are not supported for this model.`},
		{"tool_choice without tools", req(obj{"tool_choice": obj{"type": "none"}}, userMsg(text("q"))), nil, 400, "", "may only be specified while providing tools"},
		{"effort on a model without it", req(obj{"model": "claude-haiku-4-5", "output_config": obj{"effort": "high"}}, userMsg(text("q"))), nil, 400, "", "does not support the effort parameter"},
		{"an effort level the model lacks", req(obj{"model": "claude-opus-4-5", "output_config": obj{"effort": "max"}}, userMsg(text("q"))), nil, 400, "", "Input should be one of low, medium, high"},
		{"max_tokens 0 with stream", req(obj{"max_tokens": 0, "stream": true}, userMsg(text("q"))), nil, 400, "", "max_tokens: 0 is not allowed with stream: true"},
		{"max_tokens 0 with a forced tool_choice", req(obj{"model": "claude-opus-5", "max_tokens": 0, "tools": tools, "tool_choice": obj{"type": "any"}}, userMsg(text("q"))), nil, 400, "", "forced tool_choice"},
		{"max_tokens 0 with thinking enabled", req(obj{"model": "claude-haiku-4-5", "max_tokens": 0, "thinking": obj{"type": "enabled", "budget_tokens": 2000}}, userMsg(text("q"))), nil, 400, "", "thinking.type \"enabled\""},
		{"bad image media type", req(nil, userMsg(obj{"type": "image", "source": obj{"type": "base64", "media_type": "image/tiff", "data": "AAAA"}})), nil, 400, "", "media_type"},
		{"a system role on a model without it", req(obj{"model": "claude-sonnet-5"}, userMsg(text("q")), obj{"role": "assistant", "content": "a"}, userMsg(text("b")), hot(nil)), []string{"Anthropic-Beta", betaClearAt}, 400, "", "role 'system' is not supported on this model"},
		{"a tool without a schema", req(obj{"tools": []obj{{"name": "x"}}}, userMsg(text("q"))), nil, 400, "", "input_schema: Field required"},
		{"stop sequence empty", req(obj{"stop_sequences": []string{""}}, userMsg(text("q"))), nil, 400, "", "stop sequences must be non-empty"},
		{"sys5 is fine", req(obj{"system": []obj{sys5(1)}}, userMsg(text("q"))), nil, 200, "", ""},
		{"string content and string system are fine", req(obj{"system": "be brief"}, obj{"role": "user", "content": "hi"}), nil, 200, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ts := newA(t, AnthropicConfig{}, nil)
			r := post(t, ts, c.body, c.headers...)
			if r.status != c.status {
				t.Fatalf("status = %d, want %d: %s", r.status, c.status, r.body)
			}
			if c.status == 200 {
				return
			}
			if c.typ != "" && r.errType(t) != c.typ {
				t.Errorf("error type = %q, want %q", r.errType(t), c.typ)
			}
			if r.errType(t) == "" || r.json(t)["type"] != "error" {
				t.Errorf("not an API error body: %s", r.body)
			}
			if !strings.Contains(r.errMsg(t), c.want) {
				t.Errorf("message = %q, want it to contain %q", r.errMsg(t), c.want)
			}
		})
	}
}

func TestAnthropicAuth(t *testing.T) {
	_, ts := newA(t, AnthropicConfig{APIKey: "test-key-not-a-secret"}, nil)
	body := req(nil, userMsg(text("hi")))
	if r := post(t, ts, body); r.status != 401 || r.errType(t) != "authentication_error" {
		t.Fatalf("no key: %d %s", r.status, r.body)
	}
	if r := post(t, ts, body, "X-Api-Key", "wrong"); r.status != 401 {
		t.Fatalf("wrong key: %d", r.status)
	}
	if r := post(t, ts, body, "X-Api-Key", "test-key-not-a-secret"); r.status != 200 {
		t.Fatalf("x-api-key: %d %s", r.status, r.body)
	}
	if r := post(t, ts, body, "Authorization", "Bearer test-key-not-a-secret"); r.status != 200 {
		t.Fatalf("bearer: %d %s", r.status, r.body)
	}
}

func TestAnthropicFaultsAreAPIShaped(t *testing.T) {
	cases := []struct {
		fault Fault
		typ   string
	}{
		{Fault{Status: 429, Message: "slow down", RetryAfter: 7 * time.Second}, "rate_limit_error"},
		{Fault{Status: 529, Message: "Overloaded"}, "overloaded_error"},
		{Fault{Status: 500, Message: "boom"}, "api_error"},
		{Fault{Status: 402, Message: "no credit"}, "billing_error"},
		{Fault{Status: 401, Message: "bad key"}, "authentication_error"},
		{Fault{Status: 403, Message: "no"}, "permission_error"},
		{Fault{Status: 404, Message: "model: x"}, "not_found_error"},
		{Fault{Status: 413, Message: "too big"}, "request_too_large"},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			f := c.fault
			srv, ts := newA(t, AnthropicConfig{}, func(*Call) Reply { return Reply{Fault: &f} })
			r := post(t, ts, req(nil, userMsg(text("hi"))))
			if r.status != f.Status || r.errType(t) != c.typ || r.errMsg(t) != f.Message {
				t.Fatalf("%d %s", r.status, r.body)
			}
			if f.RetryAfter > 0 && r.header.Get("Retry-After") != "7" {
				t.Errorf("retry-after = %q", r.header.Get("Retry-After"))
			}
			if st := srv.AnthropicStats(); len(st) != 1 || st[0].Status != f.Status {
				t.Errorf("stats %+v", st)
			}
			if n, _ := srv.Cache().Resident(); n != 0 {
				t.Error("a failed request must not write cache entries")
			}
		})
	}
}

func TestAnthropicStreamShape(t *testing.T) {
	args := `{"path":"a.txt","lines":[1,2,3],"note":"` + strings.Repeat("x", 40) + `"}`
	_, ts := newA(t, AnthropicConfig{}, func(*Call) Reply {
		return Reply{
			Reasoning: "I should read the file before anything else, then decide.",
			Text:      "Reading the file now, this is a somewhat longer sentence.",
			ToolCalls: []ToolCall{{ID: "toolu_a", Name: "read", Args: args}},
		}
	})
	r := post(t, ts, req(obj{"stream": true}, userMsg(text("go"))))
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %s", r.status, r.header.Get("Content-Type"))
	}
	evs := readSSE(t, r.body)
	if evs[0].name != "message_start" || evs[1].name != "ping" || evs[len(evs)-1].name != "message_stop" || evs[len(evs)-2].name != "message_delta" {
		t.Fatalf("event order: %s", eventNames(evs))
	}
	for _, e := range evs {
		if e.data["type"] != e.name {
			t.Errorf("event %s carries type %v", e.name, e.data["type"])
		}
	}
	start := evs[0].data["message"].(map[string]any)
	if start["stop_reason"] != nil || len(start["content"].([]any)) != 0 || start["id"] == "" {
		t.Errorf("message_start = %v", start)
	}
	su := start["usage"].(map[string]any)
	for _, k := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "cache_creation", "output_tokens"} {
		if _, ok := su[k]; !ok {
			t.Errorf("message_start usage lacks %s", k)
		}
	}

	// Per block: start, deltas of the right type, stop, in index order.
	type acc struct {
		typ     string
		text    string
		think   string
		sig     string
		partial string
		deltas  []string
		stopped bool
	}
	blocks := map[int]*acc{}
	var order []int
	for _, e := range evs {
		switch e.name {
		case "content_block_start":
			i := int(e.data["index"].(float64))
			cb := e.data["content_block"].(map[string]any)
			blocks[i] = &acc{typ: cb["type"].(string)}
			order = append(order, i)
		case "content_block_delta":
			i := int(e.data["index"].(float64))
			d := e.data["delta"].(map[string]any)
			b := blocks[i]
			b.deltas = append(b.deltas, d["type"].(string))
			switch d["type"] {
			case "text_delta":
				b.text += d["text"].(string)
			case "thinking_delta":
				b.think += d["thinking"].(string)
			case "signature_delta":
				b.sig = d["signature"].(string)
			case "input_json_delta":
				b.partial += d["partial_json"].(string)
			}
		case "content_block_stop":
			blocks[int(e.data["index"].(float64))].stopped = true
		}
	}
	if fmt.Sprint(order) != "[0 1 2]" {
		t.Fatalf("block order %v", order)
	}
	th, tx, tu := blocks[0], blocks[1], blocks[2]
	if th.typ != "thinking" || th.think != "I should read the file before anything else, then decide." || !strings.HasPrefix(th.sig, "sig1.") || th.deltas[len(th.deltas)-1] != "signature_delta" {
		t.Errorf("thinking block: %+v", th)
	}
	if tx.typ != "text" || tx.text != "Reading the file now, this is a somewhat longer sentence." {
		t.Errorf("text block: %+v", tx)
	}
	if tu.typ != "tool_use" || tu.partial != args || tu.deltas[0] != "input_json_delta" || len(tu.deltas) < 3 {
		t.Errorf("tool_use block (the args must arrive as partial JSON in several deltas, starting with an empty one): %+v", tu)
	}
	for i, b := range blocks {
		if !b.stopped {
			t.Errorf("block %d never stopped", i)
		}
	}
	// The final usage is cumulative and repeats the input side.
	md := evs[len(evs)-2].data
	d := md["delta"].(map[string]any)
	if d["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v", d["stop_reason"])
	}
	mu := md["usage"].(map[string]any)
	if mu["output_tokens"].(float64) <= 1 || mu["input_tokens"].(float64) != su["input_tokens"].(float64) {
		t.Errorf("message_delta usage = %v", mu)
	}
	// Pings arrive between blocks too.
	pings := 0
	for _, e := range evs {
		if e.name == "ping" {
			pings++
		}
	}
	if pings < 2 {
		t.Errorf("pings = %d, want at least 2", pings)
	}
	if !strings.Contains(string(r.body), "event: ping\ndata: {\"type\": \"ping\"}") {
		t.Errorf("ping wire form differs from the API's")
	}

	t.Run("minimal delta usage", func(t *testing.T) {
		_, ts := newA(t, AnthropicConfig{MinimalDeltaUsage: true}, nil)
		evs := readSSE(t, post(t, ts, req(obj{"stream": true}, userMsg(text("hi")))).body)
		mu := evs[len(evs)-2].data["usage"].(map[string]any)
		if len(mu) != 1 || mu["output_tokens"] == nil {
			t.Errorf("usage = %v", mu)
		}
	})
}

func TestAnthropicMidStreamFault(t *testing.T) {
	_, ts := newA(t, AnthropicConfig{}, func(*Call) Reply {
		return Reply{Text: "a fairly long partial answer that gets cut off", Fault: &Fault{Status: 529, Message: "Overloaded", MidStream: true}}
	})
	r := post(t, ts, req(obj{"stream": true}, userMsg(text("hi"))))
	if r.status != 200 {
		t.Fatalf("the status was already sent: %d", r.status)
	}
	evs := readSSE(t, r.body)
	last := evs[len(evs)-1]
	if last.name != "error" {
		t.Fatalf("stream must end with an error event: %s", eventNames(evs))
	}
	e := last.data["error"].(map[string]any)
	if e["type"] != "overloaded_error" || e["message"] != "Overloaded" {
		t.Errorf("error = %v", e)
	}
	for _, ev := range evs {
		if ev.name == "message_stop" || ev.name == "message_delta" {
			t.Errorf("an errored stream has no %s", ev.name)
		}
	}
}

func TestAnthropicPrewarm(t *testing.T) {
	srv, ts := newA(t, AnthropicConfig{MinPrefixTokens: 100}, func(*Call) Reply { return Reply{Text: "real answer"} })
	sys := []obj{withCC(text(big(500, "shared")), "1h")}
	warm := req(obj{"max_tokens": 0, "system": sys}, userMsg(text("placeholder")))
	r := post(t, ts, warm)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	m := r.json(t)
	if m["stop_reason"] != "max_tokens" || len(m["content"].([]any)) != 0 {
		t.Fatalf("a pre-warm returns no content: %v", m)
	}
	u := usageOfMsg(t, m)
	if u["output_tokens"] != 0 || u["cache_creation_input_tokens"] <= 0 {
		t.Fatalf("a pre-warm bills a write and no output: %v", u)
	}
	cc := m["usage"].(map[string]any)["cache_creation"].(map[string]any)
	if cc["ephemeral_1h_input_tokens"].(float64) != u["cache_creation_input_tokens"] {
		t.Errorf("the write is 1h: %v", cc)
	}
	// The follow-up shares the marked block and reads it.
	real := req(obj{"system": sys}, userMsg(text("the actual question")))
	u2 := usageOfMsg(t, post(t, ts, real).json(t))
	if u2["cache_read_input_tokens"] != u["cache_creation_input_tokens"] {
		t.Fatalf("follow-up read %v, warm wrote %v", u2["cache_read_input_tokens"], u["cache_creation_input_tokens"])
	}
	if st := srv.AnthropicStats(); st[0].Completion != 0 || st[0].Stop != "max_tokens" {
		t.Errorf("stat %+v", st[0])
	}
}

func TestAnthropicTierInvalidationOverHTTP(t *testing.T) {
	// The tools must be big enough to be cacheable on their own (the minimum
	// prefix is 100 tokens here): a marker on a prefix below it writes nothing.
	tools := []obj{
		{"name": "bash", "description": big(60, "run"), "input_schema": obj{"type": "object", "properties": obj{"c": obj{"type": "string"}}}},
		withCC(obj{"name": "read", "description": big(60, "read"), "input_schema": obj{"type": "object"}}, "1h"),
	}
	sys := func(s string) []obj { return []obj{withCC(text(big(300, s)), "1h")} }
	conv := func(fields obj) string {
		f := obj{"tools": tools, "system": sys("const")}
		for k, v := range fields {
			f[k] = v
		}
		return req(f, userMsg(text(big(200, "shared")), withCC(text(big(100, "task")), "")))
	}
	type want struct{ tools, system, msgs bool } // whether that tier is read
	cases := []struct {
		name   string
		fields obj
		want   want
	}{
		{"same request", nil, want{true, true, true}},
		{"tool_choice changes", obj{"tool_choice": obj{"type": "none"}}, want{true, true, false}},
		{"tool_choice auto vs omitted", obj{"tool_choice": obj{"type": "auto"}}, want{true, true, false}},
		{"parallel tool use toggled", obj{"tool_choice": obj{"type": "auto", "disable_parallel_tool_use": true}}, want{true, true, false}},
		{"thinking config changes", obj{"thinking": obj{"type": "adaptive"}}, want{true, true, false}},
		{"effort changes", obj{"output_config": obj{"effort": "low"}}, want{true, true, false}},
		{"effort set to the model default equals omitted", obj{"output_config": obj{"effort": "high"}}, want{true, true, true}},
		{"system changes", obj{"system": sys("edited")}, want{true, false, false}},
		{"a tool changes", obj{"tools": []obj{tools[0], withCC(obj{"name": "read", "description": big(60, "read") + "more", "input_schema": obj{"type": "object"}}, "1h")}}, want{false, false, false}},
		{"the model changes", obj{"model": "other-model"}, want{false, false, false}},
		{"max_tokens and metadata are not cache inputs", obj{"max_tokens": 999, "metadata": obj{"user_id": "u"}}, want{true, true, true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, ts := newA(t, AnthropicConfig{MinPrefixTokens: 100}, nil)
			if r := post(t, ts, conv(nil)); r.status != 200 {
				t.Fatalf("%d %s", r.status, r.body)
			}
			if r := post(t, ts, conv(c.fields)); r.status != 200 {
				t.Fatalf("%d %s", r.status, r.body)
			}
			st := srv.AnthropicStats()[1]
			got := want{st.ReadTiers.Tools > 0, st.ReadTiers.System > 0, st.ReadTiers.Messages > 0}
			if got != c.want {
				t.Errorf("read tiers %+v (tools/system/messages read = %+v), want %+v; hits %+v", st.ReadTiers, got, c.want, st.Hits)
			}
			if st.Read+st.Write5m+st.Write1h+st.Uncached != st.PromptTokens {
				t.Errorf("usage does not add up: %+v", st)
			}
		})
	}

	t.Run("thinking config ahead of tools", func(t *testing.T) {
		cfg := AnthropicConfig{MinPrefixTokens: 100}
		cfg.Cache.ParamsAheadOfTools = func(string) bool { return true }
		srv, ts := newA(t, cfg, nil)
		post(t, ts, conv(nil))
		post(t, ts, conv(obj{"thinking": obj{"type": "adaptive"}}))
		if st := srv.AnthropicStats()[1]; st.Read != 0 {
			t.Errorf("read %d: models that render the config ahead of tools rebuild everything", st.Read)
		}
	})
	t.Run("images", func(t *testing.T) {
		srv, ts := newA(t, AnthropicConfig{MinPrefixTokens: 100}, nil)
		img := obj{"type": "image", "source": obj{"type": "base64", "media_type": "image/png", "data": "AAAA"}}
		post(t, ts, conv(nil))
		body := req(obj{"tools": tools, "system": sys("const")}, userMsg(text(big(200, "shared")), withCC(text(big(100, "task")), ""), img))
		post(t, ts, body)
		st := srv.AnthropicStats()[1]
		if st.ReadTiers.Tools == 0 || st.ReadTiers.System == 0 || st.ReadTiers.Messages != 0 {
			t.Errorf("an image invalidates the messages tier only: %+v", st.ReadTiers)
		}
	})
}

func TestAnthropicClearAtCostsNothingAfterItClears(t *testing.T) {
	srv, ts := newA(t, AnthropicConfig{MinPrefixTokens: 100}, func(c *Call) Reply {
		if len(c.Messages) < 4 {
			return Reply{ToolCalls: []ToolCall{{ID: fmt.Sprint("toolu_", len(c.Messages)), Name: "bash", Args: "{}"}}}
		}
		return Reply{Text: "done"}
	})
	tools := []obj{{"name": "bash", "description": "run", "input_schema": obj{"type": "object"}}}
	hot := func(s string) obj {
		return obj{"role": "system", "clear_at": "next_user_message", "content": []obj{text(big(200, s))}}
	}
	use := func(id string) obj {
		return obj{"role": "assistant", "content": []obj{{"type": "tool_use", "id": id, "name": "bash", "input": obj{}}}}
	}
	result := func(id string, marker bool) obj {
		b := obj{"type": "tool_result", "tool_use_id": id, "content": big(150, "out")}
		if marker {
			b = withCC(b, "")
		}
		return userMsg(b)
	}
	hdr := []string{"Anthropic-Beta", betaClearAt}
	f := obj{"tools": tools, "system": []obj{withCC(text(big(200, "const")), "")}}

	r1 := post(t, ts, req(f, userMsg(text(big(100, "task"))), use("toolu_a"), result("toolu_a", true), hot("boardone")), hdr...)
	if r1.status != 200 {
		t.Fatalf("%d %s", r1.status, r1.body)
	}
	r2 := post(t, ts, req(f, userMsg(text(big(100, "task"))), use("toolu_a"), result("toolu_a", false), hot("boardone"), use("toolu_b"), result("toolu_b", true), hot("boardtwo")), hdr...)
	if r2.status != 200 {
		t.Fatalf("%d %s", r2.status, r2.body)
	}
	st := srv.AnthropicStats()
	// Request 1 rendered its hot block (tokens counted after the marker).
	// Request 2's first hot block is cleared by a later user message: it renders
	// nothing, so it costs no tokens and the cache entry written before it (up to
	// the tool_result the marker sat on) is still read.
	hotTokens := tok(len(big(200, "boardone")))
	if st[0].PromptTokens-st[0].Read-st[0].Write5m != hotTokens {
		t.Fatalf("request 1 should bill its active hot block as uncached input: %+v (hot %d)", st[0], hotTokens)
	}
	if st[1].Read != st[0].Write5m {
		t.Fatalf("request 2 must read everything request 1 cached: read %d, wrote %d", st[1].Read, st[0].Write5m)
	}
	if st[1].Uncached != hotTokens+0 && st[1].Uncached < tok(len(big(200, "boardtwo"))) {
		t.Errorf("only the newest hot block is uncached input: %+v", st[1])
	}
	// Total prompt of request 2 = request 1's cached prefix + assistant + result + the NEW hot block; the cleared one is absent.
	if want := st[0].Read + st[0].Write5m + tok(len(`bash`)+2) + tok(len(big(150, "out"))) + tok(len(big(200, "boardtwo"))); st[1].PromptTokens != want {
		t.Errorf("request 2 prompt = %d, want %d (a cleared message renders no tokens)", st[1].PromptTokens, want)
	}
}

func TestAnthropicResponderView(t *testing.T) {
	var got *Call
	_, ts := newA(t, AnthropicConfig{MinPrefixTokens: 100}, func(c *Call) Reply { got = c; return Reply{Text: "ok"} })
	tools := []obj{{"name": "bash", "description": "run", "input_schema": obj{"type": "object"}}, {"name": "read", "description": "r", "input_schema": obj{"type": "object"}}}
	body := req(obj{"tools": tools, "system": []obj{text("be terse"), text("really")}, "model": "claude-opus-5-5"},
		userMsg(text("first"), text("second")),
		obj{"role": "assistant", "content": []obj{text("thinking aloud"), {"type": "tool_use", "id": "toolu_1", "name": "bash", "input": obj{"command": "ls"}}}},
		userMsg(obj{"type": "tool_result", "tool_use_id": "toolu_1", "content": []obj{text("a.txt")}}, text("and this")),
	)
	if r := post(t, ts, body); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if got.Model != "claude-opus-5-5" || got.System != "be terse\n\nreally" || fmt.Sprint(got.Tools) != "[bash read]" || got.N != 1 {
		t.Errorf("call = %+v", got)
	}
	roles := []string{}
	for _, m := range got.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,tool,user" {
		t.Fatalf("messages roles = %v", roles)
	}
	if got.Messages[0].Content != "firstsecond" || got.Messages[1].Content != "thinking aloud" || got.Messages[1].ToolCalls[0].Name != "bash" ||
		got.Messages[2].ToolCallID != "toolu_1" || got.Messages[2].Content != "a.txt" || got.Messages[3].Content != "and this" {
		t.Errorf("messages = %+v", got.Messages)
	}
	if got.LastUser() != "and this" {
		t.Errorf("LastUser = %q", got.LastUser())
	}
	if _, ok := got.Raw["tools"]; !ok || got.Prompt <= 0 {
		t.Errorf("raw/prompt: %+v", got)
	}
	// The default responder echoes the last user message.
	_, ts2 := newA(t, AnthropicConfig{}, nil)
	m := post(t, ts2, req(nil, userMsg(text("ping")))).json(t)
	if m["content"].([]any)[0].(map[string]any)["text"] != "echo: ping" {
		t.Errorf("default responder: %v", m["content"])
	}
}

func TestAnthropicStampedeOverAColdPrefix(t *testing.T) {
	cfg := AnthropicConfig{MinPrefixTokens: 100}
	cfg.FirstToken = 120 * time.Millisecond
	srv, ts := newA(t, cfg, nil)
	body := req(obj{"system": []obj{withCC(text(big(400, "shared")), "")}}, userMsg(text("task")))

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); post(t, ts, body) }()
	}
	wg.Wait()
	st := srv.AnthropicStats()
	if len(st) != 2 || st[0].Read != 0 || st[1].Read != 0 || st[0].Write5m == 0 || st[1].Write5m == 0 {
		t.Fatalf("both parallel requests over a cold prefix pay the write: %+v", st)
	}
	if third := post(t, ts, body); third.status != 200 {
		t.Fatal(third.body)
	}
	if st := srv.AnthropicStats(); st[2].Read != st[0].Write5m || st[2].Write5m != 0 {
		t.Fatalf("a request after the first byte reads: %+v", st[2])
	}
}

func TestAnthropicEntriesAreReadableAtTheFirstByte(t *testing.T) {
	// The moment a client sees the first byte of request A, request B reads what
	// A wrote: the contract the swarm's warm gate relies on.
	cfg := AnthropicConfig{MinPrefixTokens: 100}
	cfg.FirstToken = 60 * time.Millisecond
	cfg.DecodePer = 20 * time.Millisecond // A keeps streaming long after its first byte
	srv, ts := newA(t, cfg, func(*Call) Reply { return Reply{Text: strings.Repeat("slow output ", 20)} })
	sys := []obj{withCC(text(big(400, "shared")), "")}
	streaming := req(obj{"system": sys, "stream": true}, userMsg(text("a")))

	hr, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/messages", strings.NewReader(streaming))
	hr.Header.Set("Anthropic-Version", "2023-06-01")
	hr.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n') // "event: message_start": the first response byte
	if err != nil || !strings.HasPrefix(line, "event: message_start") {
		t.Fatalf("first line %q %v", line, err)
	}
	if r := post(t, ts, req(obj{"system": sys}, userMsg(text("b")))); r.status != 200 {
		t.Fatal(r.body)
	}
	go io.Copy(io.Discard, br)
	var a, b *AnthropicStat
	for _, st := range srv.AnthropicStats() {
		st := st
		switch st.N {
		case 1:
			a = &st
		case 2:
			b = &st
		}
	}
	if a == nil || b == nil || a.Read != 0 || a.Write5m == 0 || b.Read != a.Write5m {
		t.Fatalf("B must read exactly what A wrote, from A's first byte on: A %+v B %+v", a, b)
	}
}

func TestAnthropicRateLimitIsAPIShaped(t *testing.T) {
	cfg := AnthropicConfig{}
	cfg.RPM = 2
	_, ts := newA(t, cfg, nil)
	body := req(nil, userMsg(text("hi")))
	for i := 0; i < 2; i++ {
		if r := post(t, ts, body); r.status != 200 || r.header.Get("Anthropic-Ratelimit-Requests-Limit") != "2" {
			t.Fatalf("call %d: %d %v", i, r.status, r.header)
		}
	}
	r := post(t, ts, body)
	if r.status != 429 || r.errType(t) != "rate_limit_error" || r.header.Get("Retry-After") == "" || r.header.Get("Anthropic-Ratelimit-Requests-Remaining") != "0" {
		t.Fatalf("%d %v %s", r.status, r.header, r.body)
	}
	if _, err := time.Parse(time.RFC3339, r.header.Get("Anthropic-Ratelimit-Requests-Reset")); err != nil {
		t.Errorf("reset must be an RFC 3339 timestamp: %v", err)
	}
}

func TestAnthropicTopLevelCacheControl(t *testing.T) {
	srv, ts := newA(t, AnthropicConfig{MinPrefixTokens: 100}, nil)
	body := func(extra string) string {
		return `{"model":"mock-1","max_tokens":10,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":[` +
			mustJSON(t, text(big(300, "x"))) + extra + `]}]}`
	}
	post(t, ts, body(""))
	post(t, ts, body(","+mustJSON(t, text("tail"))))
	st := srv.AnthropicStats()
	if st[0].Write5m == 0 || st[0].Markers != 1 {
		t.Fatalf("automatic caching writes on the last block: %+v", st[0])
	}
	// Growth: the automatic breakpoint moves to the new last block and reads the old entry.
	if st[1].Read != st[0].Write5m || st[1].Markers != 1 {
		t.Fatalf("second: %+v", st[1])
	}
	// An explicit marker on the last block with a different TTL is a 400.
	bad := `{"model":"mock-1","max_tokens":10,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":[` + mustJSON(t, withCC(text("x"), "1h")) + `]}]}`
	if r := post(t, ts, bad); r.status != 400 {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

func TestAnthropicOmittedThinkingText(t *testing.T) {
	_, ts := newA(t, AnthropicConfig{OmitThinkingText: true}, func(*Call) Reply { return Reply{Reasoning: "secret reasoning", Text: "answer"} })
	evs := readSSE(t, post(t, ts, req(obj{"stream": true}, userMsg(text("hi")))).body)
	var kinds []string
	for _, e := range evs {
		if e.name == "content_block_delta" {
			kinds = append(kinds, e.data["delta"].(map[string]any)["type"].(string))
		}
	}
	if strings.Contains(strings.Join(kinds, ","), "thinking_delta") || !strings.Contains(strings.Join(kinds, ","), "signature_delta") {
		t.Errorf("hidden reasoning must arrive as a signature only: %v", kinds)
	}
	m := post(t, ts, req(nil, userMsg(text("hi")))).json(t)
	th := m["content"].([]any)[0].(map[string]any)
	if th["type"] != "thinking" || th["thinking"] != "" || th["signature"] == "" {
		t.Errorf("thinking block = %v", th)
	}
}

func TestAnthropicRawBlocksFromReasoningDetails(t *testing.T) {
	_, ts := newA(t, AnthropicConfig{}, func(*Call) Reply {
		return Reply{ReasoningDetails: json.RawMessage(`[{"type":"redacted_thinking","data":"opaque-fixture"}]`), Text: "ok"}
	})
	m := post(t, ts, req(nil, userMsg(text("hi")))).json(t)
	c := m["content"].([]any)
	if c[0].(map[string]any)["type"] != "redacted_thinking" || c[0].(map[string]any)["data"] != "opaque-fixture" || c[1].(map[string]any)["type"] != "text" {
		t.Errorf("content = %v", c)
	}
	evs := readSSE(t, post(t, ts, req(obj{"stream": true}, userMsg(text("hi")))).body)
	if evs[2].name != "content_block_start" || evs[2].data["content_block"].(map[string]any)["type"] != "redacted_thinking" {
		t.Errorf("events: %s", eventNames(evs))
	}
}

func TestAnthropicFinishReasons(t *testing.T) {
	cases := map[string]string{"": "end_turn", "stop": "end_turn", "length": "max_tokens", "refusal": "refusal", "pause_turn": "pause_turn", "stop_sequence": "stop_sequence"}
	for finish, want := range cases {
		t.Run(want+"/"+finish, func(t *testing.T) {
			_, ts := newA(t, AnthropicConfig{}, func(*Call) Reply { return Reply{Text: "x", Finish: finish} })
			m := post(t, ts, req(nil, userMsg(text("hi")))).json(t)
			if m["stop_reason"] != want {
				t.Fatalf("stop_reason = %v", m["stop_reason"])
			}
			if want == "refusal" && m["stop_details"] == nil {
				t.Errorf("a refusal carries stop_details")
			}
			if want != "refusal" && m["stop_details"] != nil {
				t.Errorf("stop_details only on refusals: %v", m["stop_details"])
			}
		})
	}
	_, ts := newA(t, AnthropicConfig{}, func(*Call) Reply { return Reply{ToolCalls: []ToolCall{{Name: "bash", Args: "{}"}}} })
	m := post(t, ts, req(nil, userMsg(text("hi")))).json(t)
	tu := m["content"].([]any)[0].(map[string]any)
	if m["stop_reason"] != "tool_use" || !strings.HasPrefix(tu["id"].(string), "toolu_") {
		t.Errorf("tool call without id: %v", m)
	}
}
