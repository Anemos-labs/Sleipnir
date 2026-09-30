package mock

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed", "invalid_request")
		return
	}
	if s.rateLimit(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeErr(w, 400, err.Error(), "invalid_request")
		return
	}
	p, err := parseChat(r, body)
	if err != nil {
		writeErr(w, 400, err.Error(), "invalid_request")
		return
	}

	now := s.cfg.Now
	started := now()
	prompt := p.renderPrompt()
	promptTokens := tokens(len(prompt))

	s.mu.Lock()
	s.n++
	n := s.n
	resp := s.resp
	s.mu.Unlock()

	eng := s.router.Route(p.model, p.session)
	engine := s.router.Engine(eng)
	cached := engine.Lookup(prompt)

	capture := wantsTokenIDs(body)
	call := &Call{
		N: n, Model: p.model, Session: p.session, Engine: eng, System: p.system,
		Messages: p.messages, Tools: p.tools, Cached: cached, Prompt: promptTokens, Raw: p.raw,
	}
	reply := resp(call)

	stat := CallStat{
		N: n, Session: p.session, Engine: eng, Model: p.model, PromptTokens: promptTokens,
		Cached: cached, Streamed: p.stream, StartedAt: started, Header: r.Header.Clone(),
	}

	if reply.Fault != nil && !reply.Fault.MidStream {
		if reply.Fault.RetryAfter > 0 {
			w.Header().Set("Retry-After", itoa(int(reply.Fault.RetryAfter.Seconds())))
		}
		stat.Status = reply.Fault.Status
		s.record(stat)
		writeErr(w, reply.Fault.Status, reply.Fault.Message, "provider_error")
		return
	}

	// Prefill: time proportional to the tokens that were not cached.
	sleep(s.cfg.FirstToken + time.Duration(promptTokens-cached)*s.cfg.PrefillPer)
	// The request's blocks become readable now, at first token, and not
	// before. Parallel requests that started earlier could not read them.
	engine.Insert(prompt)
	stat.FirstByteAt = now()

	completion := reply.OutputTokens
	if completion == 0 {
		completion = tokens(len(reply.Text) + len(reply.Reasoning))
		for _, tc := range reply.ToolCalls {
			completion += tokens(len(tc.Name) + len(tc.Args))
		}
		if completion == 0 {
			completion = 1
		}
	}
	uncached := promptTokens - cached
	price := s.cfg.Price
	cst := (float64(uncached)*price.InputPerM + float64(cached)*price.CacheReadPerM + float64(completion)*price.OutputPerM) / 1e6
	stat.Completion, stat.Cost, stat.Status = completion, cst, 200

	finish := reply.Finish
	if finish == "" {
		finish = "stop"
		if len(reply.ToolCalls) > 0 {
			finish = "tool_calls"
		}
	}
	usage := map[string]any{
		"prompt_tokens": promptTokens, "completion_tokens": completion, "total_tokens": promptTokens + completion,
		"cost": cst,
	}
	if cached > 0 {
		usage["prompt_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	id := "gen-mock-" + itoa(n)
	// Token capture emulation (vLLM-style): whitespace-run tokens with hashed ids,
	// so prompts are prefix-stable and completions are reproducible.
	var promptIDs, compIDs []int32
	var lps []map[string]any
	if capture {
		promptIDs = fakeTokenize(string(prompt))
		compIDs = fakeTokenize(completionText(reply))
		for _, tid := range compIDs {
			lps = append(lps, map[string]any{"logprob": fakeLogprob(tid)})
		}
	}

	if s.cfg.CacheGenerated {
		defer func() {
			gen := append([]byte(nil), prompt...)
			gen = append(gen, []byte(reply.Text)...)
			engine.Insert(gen)
		}()
	}
	defer s.record(stat)

	w.Header().Set("X-Generation-Id", id)
	if !p.stream {
		msg := map[string]any{"role": "assistant"}
		if reply.Text != "" || len(reply.ToolCalls) == 0 {
			msg["content"] = reply.Text
		} else {
			msg["content"] = nil
		}
		if reply.Reasoning != "" {
			msg["reasoning"] = reply.Reasoning
		}
		if len(reply.ReasoningDetails) > 0 {
			msg["reasoning_details"] = json.RawMessage(reply.ReasoningDetails)
		}
		if len(reply.ToolCalls) > 0 {
			var tcs []map[string]any
			for _, tc := range reply.ToolCalls {
				tcs = append(tcs, map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Name, "arguments": tc.Args}})
			}
			msg["tool_calls"] = tcs
		}
		w.Header().Set("Content-Type", "application/json")
		choice := map[string]any{"index": 0, "message": msg, "finish_reason": finish}
		out := map[string]any{
			"id": id, "object": "chat.completion", "model": p.model, "provider": "mock",
			"choices": []any{choice},
			"usage":   usage,
		}
		if capture {
			out["prompt_token_ids"] = promptIDs
			choice["token_ids"] = compIDs
			choice["logprobs"] = map[string]any{"content": lps}
		}
		json.NewEncoder(w).Encode(out)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		w.Write([]byte("data: "))
		w.Write(b)
		w.Write([]byte("\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}
	chunk := func(d map[string]any, fin any) map[string]any {
		return map[string]any{
			"id": id, "object": "chat.completion.chunk", "model": p.model, "provider": "mock",
			"choices": []any{map[string]any{"index": 0, "delta": d, "finish_reason": fin}},
		}
	}
	w.Write([]byte(": HEIMDALL PROCESSING\n\n"))
	if fl != nil {
		fl.Flush()
	}
	first := true
	emit := func(d map[string]any) {
		if first {
			d["role"] = "assistant"
			first = false
		}
		send(chunk(d, nil))
		sleep(s.cfg.DecodePer)
	}
	if reply.Reasoning != "" {
		for _, piece := range pieces(reply.Reasoning, 24) {
			emit(map[string]any{"reasoning": piece})
		}
	}
	if len(reply.ReasoningDetails) > 0 {
		var items []map[string]any
		if json.Unmarshal(reply.ReasoningDetails, &items) == nil {
			for _, it := range items {
				emit(map[string]any{"reasoning_details": []any{it}})
			}
		}
	}
	if reply.Fault != nil && reply.Fault.MidStream {
		emit(map[string]any{"content": "partial…"})
		b, _ := json.Marshal(map[string]any{
			"id": id, "object": "chat.completion.chunk", "model": p.model,
			"error":   map[string]any{"code": reply.Fault.Status, "message": reply.Fault.Message, "metadata": map[string]any{"error_type": "provider_error"}},
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": ""}, "finish_reason": "error"}},
		})
		w.Write([]byte("data: " + string(b) + "\n\ndata: [DONE]\n\n"))
		return
	}
	for _, piece := range pieces(reply.Text, 24) {
		emit(map[string]any{"content": piece})
	}
	for i, tc := range reply.ToolCalls {
		emit(map[string]any{"tool_calls": []any{map[string]any{
			"index": i, "id": tc.ID, "type": "function",
			"function": map[string]any{"name": tc.Name, "arguments": ""},
		}}})
		for _, piece := range pieces(tc.Args, 16) {
			emit(map[string]any{"tool_calls": []any{map[string]any{
				"index": i, "function": map[string]any{"arguments": piece},
			}}})
		}
	}
	if first { // empty completion still needs a first frame
		emit(map[string]any{"content": ""})
	}
	last := chunk(map[string]any{}, finish)
	if capture {
		last["prompt_token_ids"] = promptIDs
		ch := last["choices"].([]any)[0].(map[string]any)
		ch["token_ids"] = compIDs
		ch["logprobs"] = map[string]any{"content": lps}
	}
	send(last)
	fin := map[string]any{"id": id, "object": "chat.completion.chunk", "model": p.model, "provider": "mock", "choices": []any{}, "usage": usage}
	send(fin)
	w.Write([]byte("data: [DONE]\n\n"))
}

// wantsTokenIDs reports whether the request asked for vLLM-style token capture.
func wantsTokenIDs(body []byte) bool {
	var q struct {
		Return bool `json:"return_token_ids"`
	}
	_ = json.Unmarshal(body, &q)
	return q.Return
}

// completionText is what the fake tokenizer sees as the model's output.
func completionText(r Reply) string {
	t := r.Text
	for _, tc := range r.ToolCalls {
		t += " <tool_call> " + tc.Name + " " + tc.Args + " </tool_call>"
	}
	return t
}

// fakeTokenize splits text into words (an optional leading space plus a run of
// non-space characters) and single newlines, and hashes each to an id. It is
// deterministic and prefix-stable at message boundaries, which is all the
// capture tests need.
func fakeTokenize(text string) []int32 {
	var ids []int32
	emit := func(tok string) {
		var h uint32 = 2166136261
		for i := 0; i < len(tok); i++ {
			h = (h ^ uint32(tok[i])) * 16777619
		}
		ids = append(ids, int32(h%50000)+1)
	}
	for i := 0; i < len(text); {
		j := i
		switch {
		case text[j] == '\n':
			j++
		default:
			if text[j] == ' ' {
				j++
			}
			for j < len(text) && text[j] != ' ' && text[j] != '\n' {
				j++
			}
			if j == i { // lone space
				j++
			}
		}
		emit(text[i:j])
		i = j
	}
	return ids
}

func fakeLogprob(id int32) float64 { return -0.05 - float64(id%17)/20 }

func (s *Server) record(st CallStat) {
	s.mu.Lock()
	s.stats = append(s.stats, st)
	s.mu.Unlock()
}

func pieces(s string, n int) []string {
	if s == "" {
		return nil
	}
	var out []string
	r := []rune(s)
	for len(r) > 0 {
		k := n
		if k > len(r) {
			k = len(r)
		}
		out = append(out, string(r[:k]))
		r = r[k:]
	}
	return out
}

func sleep(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
