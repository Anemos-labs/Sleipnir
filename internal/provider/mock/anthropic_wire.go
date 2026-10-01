package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
)

// This file parses and validates POST /v1/messages requests the way the real API
// does, with error messages in its style. The mock is deliberately strict:
// an adapter bug (a marker on a thinking block, a string where blocks belong, a
// tool_result that does not follow its tool_use) must fail a test here, not pass
// silently and fail in production.

// aErr is an API error response.
type aErr struct {
	status int
	typ    string
	msg    string
}

func badReq(format string, args ...any) *aErr {
	return &aErr{status: 400, typ: "invalid_request_error", msg: fmt.Sprintf(format, args...)}
}

// aCC is a parsed cache_control marker.
type aCC struct{ ttl string } // "5m" or "1h"

func (c *aCC) duration(ttl5, ttl1 time.Duration) time.Duration {
	if c != nil && c.ttl == "1h" {
		return ttl1
	}
	return ttl5
}

// aBlock is one content block as received.
type aBlock struct {
	typ string
	raw json.RawMessage
	cc  *aCC

	text      string
	id, name  string
	input     json.RawMessage
	toolUseID string
	isError   bool
	result    []aBlock
	thinking  string
	signature string // signature of a thinking block, or data of a redacted one
	content   string // compaction summary
	images    int
}

func (b aBlock) isThinking() bool { return b.typ == "thinking" || b.typ == "redacted_thinking" }

type aMsg struct {
	role    string
	clearAt string
	blocks  []aBlock
}

type aTool struct {
	name, desc string
	schema     json.RawMessage
	strict     bool
	cc         *aCC
}

// aReq is a validated request.
type aReq struct {
	raw       map[string]json.RawMessage
	model     string
	rules     aModel
	maxTokens int
	stream    bool

	system       []aBlock
	systemString bool
	tools        []aTool
	msgs         []aMsg

	toolChoice     string // canonical JSON, "" when omitted
	toolChoiceType string

	thinkingType string
	thinking     string // canonical rendering-relevant config
	bindingSet   bool
	bindingMode  string
	effort       string
	temperature  *float64
	stop         []string
	userID       string
	speed        string
	topCC        *aCC

	betas map[string]bool
}

// aModel is what the mock knows about a model family. It is written from the
// provider docs independently of the adapter's own table, so a disagreement
// between the two shows up as a 400 in a test.
type aModel struct {
	known         bool
	noSampling    bool
	alwaysThinks  bool
	budgetOnly    bool // "adaptive" is rejected, "enabled" needs a budget
	noEnabled     bool // "enabled" (a budget) is rejected
	noForcedTools bool
	midSystem     bool
	efforts       []string
	binding       bool // runs the preserved-thinking prefix check
	defaultEffort string
}

var (
	efFive  = []string{"low", "medium", "high", "xhigh", "max"}
	efFour  = []string{"low", "medium", "high", "max"}
	efThree = []string{"low", "medium", "high"}
)

var aModels = []struct {
	prefix string
	m      aModel
}{
	{"claude-fable-5-1", aModel{known: true, noSampling: true, alwaysThinks: true, noEnabled: true, noForcedTools: true, midSystem: true, efforts: efFive, binding: true, defaultEffort: "high"}},
	{"claude-mythos-5-1", aModel{known: true, noSampling: true, alwaysThinks: true, noEnabled: true, noForcedTools: true, midSystem: true, efforts: efFive, defaultEffort: "high"}},
	{"claude-fable-5", aModel{known: true, noSampling: true, alwaysThinks: true, noEnabled: true, midSystem: true, efforts: efFive, defaultEffort: "high"}},
	{"claude-mythos-5", aModel{known: true, noSampling: true, alwaysThinks: true, noEnabled: true, midSystem: true, efforts: efFive, defaultEffort: "high"}},
	{"claude-opus-5-5", aModel{known: true, noSampling: true, alwaysThinks: true, noEnabled: true, noForcedTools: true, midSystem: true, efforts: efFive, binding: true, defaultEffort: "medium"}},
	{"claude-opus-5", aModel{known: true, noSampling: true, noEnabled: true, midSystem: true, efforts: efFive, defaultEffort: "high"}},
	{"claude-opus-4-8", aModel{known: true, noSampling: true, noEnabled: true, midSystem: true, efforts: efFive, defaultEffort: "high"}},
	{"claude-opus-4-7", aModel{known: true, noSampling: true, noEnabled: true, efforts: efFive, defaultEffort: "high"}},
	{"claude-opus-4-6", aModel{known: true, efforts: efFour, defaultEffort: "high"}},
	{"claude-opus-4-5", aModel{known: true, budgetOnly: true, efforts: efThree, defaultEffort: "high"}},
	{"claude-sonnet-5-5", aModel{known: true, noSampling: true, alwaysThinks: true, noEnabled: true, noForcedTools: true, midSystem: true, efforts: efFive, binding: true, defaultEffort: "high"}},
	{"claude-sonnet-5", aModel{known: true, noSampling: true, noEnabled: true, efforts: efFive, defaultEffort: "high"}},
	{"claude-sonnet-4-6", aModel{known: true, efforts: efFour, defaultEffort: "high"}},
	{"claude-sonnet-4-5", aModel{known: true, budgetOnly: true}},
	{"claude-haiku-4-5", aModel{known: true, budgetOnly: true}},
	{"claude-opus-4", aModel{known: true, budgetOnly: true}},
	{"claude-sonnet-4", aModel{known: true, budgetOnly: true}},
	{"claude-3", aModel{known: true, budgetOnly: true}},
}

func modelRules(model string) aModel {
	id := cost.Normalize(model)
	for _, f := range aModels {
		if strings.HasPrefix(id, f.prefix) {
			return f.m
		}
	}
	// Unknown models (the mock's own "mock-1") accept everything the newest
	// families do, so tests are not forced onto a real model id.
	return aModel{midSystem: true, defaultEffort: "high"}
}

// knownTopLevel are the request members the mock accepts; anything else is
// rejected the way the API does.
var knownTopLevel = map[string]bool{
	"model": true, "max_tokens": true, "messages": true, "system": true, "tools": true,
	"tool_choice": true, "thinking": true, "output_config": true, "temperature": true,
	"top_p": true, "top_k": true, "stop_sequences": true, "stream": true, "metadata": true,
	"cache_control": true, "diagnostics": true, "service_tier": true, "speed": true,
	"context_management": true, "inference_geo": true, "container": true, "mcp_servers": true,
	"fallbacks": true, "fallback_credit_token": true, "compaction": true,
}

// DefaultKnownBetas are the anthropic-beta values the mock accepts. An unknown
// value is a 400 on the real API, which is how a misspelt beta shows up.
var DefaultKnownBetas = []string{
	"mid-conversation-system-clear-at-2026-08-21",
	"thinking-binding-controls-2026-08-01",
	"thinking-display-updates-2026-08-18",
	"mid-conversation-output-config-2026-07-01",
	"mid-conversation-tool-changes-2026-07-01",
	"inline-tools-2026-09-15",
	"compact-2026-01-12",
	"compact-2026-09-04",
	"context-management-2025-06-27",
	"server-side-fallback-2026-07-01",
	"fallback-credit-2026-07-01",
	"cache-diagnosis-2026-04-07",
	"prompt-caching-scope-2026-01-05",
	"extended-cache-ttl-2025-04-11",
}

const (
	betaClearAt = "mid-conversation-system-clear-at-2026-08-21"
	betaBinding = "thinking-binding-controls-2026-08-01"
	betaDisplay = "thinking-display-updates-2026-08-18"
)

func parseBetas(header []string, known []string) (map[string]bool, *aErr) {
	set := map[string]bool{}
	ok := map[string]bool{}
	for _, k := range known {
		ok[k] = true
	}
	var bad []string
	for _, h := range header {
		for _, v := range strings.Split(h, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if !ok[v] {
				bad = append(bad, v)
				continue
			}
			set[v] = true
		}
	}
	if len(bad) > 0 {
		return nil, badReq("Unexpected value(s) `%s` for the `anthropic-beta` header. Please consult our documentation at docs.anthropic.com or try again without the header.", strings.Join(bad, "`, `"))
	}
	return set, nil
}

// parseRequest decodes and validates a request body.
func parseRequest(body []byte, betas map[string]bool) (*aReq, *aErr) {
	q := &aReq{betas: betas}
	if err := json.Unmarshal(body, &q.raw); err != nil {
		return nil, badReq("Invalid JSON in request body: %v", err)
	}
	for _, k := range sortedKeys(q.raw) {
		if !knownTopLevel[k] {
			return nil, badReq("%s: Extra inputs are not permitted", k)
		}
	}
	if !unmarshalField(q.raw, "model", &q.model) || strings.TrimSpace(q.model) == "" {
		return nil, badReq("model: Field required")
	}
	q.rules = modelRules(q.model)
	if raw, ok := q.raw["max_tokens"]; !ok {
		return nil, badReq("max_tokens: Field required")
	} else if err := json.Unmarshal(raw, &q.maxTokens); err != nil || q.maxTokens < 0 {
		return nil, badReq("max_tokens: Input should be a non-negative integer")
	}
	if raw, ok := q.raw["stream"]; ok {
		if err := json.Unmarshal(raw, &q.stream); err != nil {
			return nil, badReq("stream: Input should be a valid boolean")
		}
	}
	if e := q.parseSampling(); e != nil {
		return nil, e
	}
	if e := q.parseThinking(); e != nil {
		return nil, e
	}
	if e := q.parseOutputConfig(); e != nil {
		return nil, e
	}
	if e := q.parseTools(); e != nil {
		return nil, e
	}
	if e := q.parseToolChoice(); e != nil {
		return nil, e
	}
	if e := q.parseSystem(); e != nil {
		return nil, e
	}
	if e := q.parseMessages(); e != nil {
		return nil, e
	}
	if e := q.checkStructure(); e != nil {
		return nil, e
	}
	if raw, ok := q.raw["cache_control"]; ok {
		cc, e := parseCC(raw, "cache_control")
		if e != nil {
			return nil, e
		}
		q.topCC = cc
	}
	if e := q.checkPrewarm(); e != nil {
		return nil, e
	}
	return q, nil
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func unmarshalField(m map[string]json.RawMessage, key string, v any) bool {
	raw, ok := m[key]
	return ok && json.Unmarshal(raw, v) == nil
}

func (q *aReq) parseSampling() *aErr {
	if raw, ok := q.raw["temperature"]; ok {
		var t float64
		if err := json.Unmarshal(raw, &t); err != nil || t < 0 || t > 1 {
			return badReq("temperature: Input should be a number between 0 and 1")
		}
		if q.rules.noSampling {
			return badReq("`temperature` is not supported for this model; sampling parameters were removed")
		}
		q.temperature = &t
	}
	for _, k := range []string{"top_p", "top_k"} {
		if _, ok := q.raw[k]; ok && q.rules.noSampling {
			return badReq("`%s` is not supported for this model; sampling parameters were removed", k)
		}
	}
	if raw, ok := q.raw["stop_sequences"]; ok {
		if err := json.Unmarshal(raw, &q.stop); err != nil {
			return badReq("stop_sequences: Input should be a valid list of strings")
		}
		for i, s := range q.stop {
			if s == "" {
				return badReq("stop_sequences.%d: stop sequences must be non-empty", i)
			}
		}
	}
	if raw, ok := q.raw["metadata"]; ok {
		var m struct {
			UserID string `json:"user_id"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return badReq("metadata: Input should be an object")
		}
		q.userID = m.UserID
	}
	if raw, ok := q.raw["speed"]; ok {
		_ = json.Unmarshal(raw, &q.speed)
	}
	return nil
}

func (q *aReq) parseThinking() *aErr {
	raw, ok := q.raw["thinking"]
	if !ok {
		return nil
	}
	var t struct {
		Type         string          `json:"type"`
		BudgetTokens *int            `json:"budget_tokens"`
		Display      string          `json:"display"`
		BlockBinding json.RawMessage `json:"block_binding"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return badReq("thinking: %v", err)
	}
	q.thinkingType = t.Type
	switch t.Type {
	case "adaptive":
		if q.rules.budgetOnly {
			return badReq("\"thinking.type.adaptive\" is not supported for this model. Use \"thinking.type.enabled\" with budget_tokens.")
		}
	case "enabled":
		if q.rules.noEnabled {
			return badReq("\"thinking.type.enabled\" is not supported for this model. Use \"thinking.type.adaptive\" and \"output_config.effort\" to control thinking behavior.")
		}
		if t.BudgetTokens == nil || *t.BudgetTokens < 1024 {
			return badReq("thinking.enabled.budget_tokens: Input should be greater than or equal to 1024")
		}
		if q.maxTokens != 0 && *t.BudgetTokens >= q.maxTokens {
			return badReq("thinking.enabled.budget_tokens: Input should be less than max_tokens")
		}
	case "disabled":
		if q.rules.alwaysThinks {
			return badReq("\"thinking.type.disabled\" is not supported for this model. Use \"thinking.type.adaptive\" and \"output_config.effort\" to control thinking behavior.")
		}
	case "between_tools":
		if !strings.HasPrefix(cost.Normalize(q.model), "claude-sonnet-5-5") {
			return badReq("\"thinking.type.between_tools\" is not supported for this model.")
		}
	default:
		return badReq("thinking.type: Input should be 'adaptive', 'enabled' or 'disabled'")
	}
	if len(t.BlockBinding) > 0 {
		if !q.betas[betaBinding] {
			return badReq("thinking.%s.block_binding: Extra inputs are not permitted", t.Type)
		}
		var b struct {
			Mode string `json:"prefix_mismatch_behavior"`
		}
		bd := json.NewDecoder(bytes.NewReader(t.BlockBinding))
		bd.DisallowUnknownFields()
		if err := bd.Decode(&b); err != nil {
			return badReq("thinking.%s.block_binding: %v", t.Type, err)
		}
		switch b.Mode {
		case "error", "drop_block":
		case "":
			// The object with no member is accepted and changes nothing.
		default:
			return badReq("thinking.%s.block_binding.prefix_mismatch_behavior: Input should be 'error' or 'drop_block'", t.Type)
		}
		q.bindingSet = b.Mode != ""
		q.bindingMode = b.Mode
	}
	switch t.Display {
	case "", "summarized", "omitted":
	case "updates":
		if !q.betas[betaDisplay] {
			return badReq("thinking.%s.display: Input should be 'summarized' or 'omitted'", t.Type)
		}
	default:
		return badReq("thinking.%s.display: Input should be 'summarized', 'omitted' or 'updates'", t.Type)
	}
	// The cache sees the mode and the budget; display and binding do not enter it.
	q.thinking = t.Type
	if t.BudgetTokens != nil {
		q.thinking += fmt.Sprintf(":%d", *t.BudgetTokens)
	}
	return nil
}

func (q *aReq) parseOutputConfig() *aErr {
	raw, ok := q.raw["output_config"]
	if !ok {
		return nil
	}
	var o struct {
		Effort string          `json:"effort"`
		Format json.RawMessage `json:"format"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return badReq("output_config: Input should be an object")
	}
	if o.Effort != "" {
		if !q.rules.known {
			// unknown models accept any level
		} else if len(q.rules.efforts) == 0 {
			return badReq("output_config.effort: this model does not support the effort parameter")
		} else if !contains(q.rules.efforts, o.Effort) {
			return badReq("output_config.effort: Input should be one of %s", strings.Join(q.rules.efforts, ", "))
		}
		// Setting the model's default level equals omitting it.
		if o.Effort != q.rules.defaultEffort {
			q.effort = o.Effort
		}
	}
	return nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func (q *aReq) parseTools() *aErr {
	raw, ok := q.raw["tools"]
	if !ok {
		return nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return badReq("tools: Input should be a valid list")
	}
	for i, r := range list {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(r, &m); err != nil {
			return badReq("tools.%d: Input should be an object", i)
		}
		t := aTool{}
		for _, k := range sortedKeys(m) {
			switch k {
			case "name", "description", "input_schema", "strict", "cache_control", "type", "eager_input_streaming", "defer_loading", "allowed_callers":
			default:
				return badReq("tools.%d.%s: Extra inputs are not permitted", i, k)
			}
		}
		if !unmarshalField(m, "name", &t.name) || t.name == "" {
			return badReq("tools.%d.custom.name: Field required", i)
		}
		_ = json.Unmarshal(m["description"], &t.desc)
		_ = json.Unmarshal(m["strict"], &t.strict)
		sch, has := m["input_schema"]
		if !has || len(sch) == 0 || sch[0] != '{' {
			return badReq("tools.%d.custom.input_schema: Field required", i)
		}
		t.schema = sch
		if c, ok := m["cache_control"]; ok {
			cc, e := parseCC(c, fmt.Sprintf("tools.%d.cache_control", i))
			if e != nil {
				return e
			}
			t.cc = cc
		}
		q.tools = append(q.tools, t)
	}
	return nil
}

func (q *aReq) parseToolChoice() *aErr {
	raw, ok := q.raw["tool_choice"]
	if !ok {
		return nil
	}
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return badReq("tool_choice: Input should be an object")
	}
	switch tc.Type {
	case "auto", "none":
	case "any", "tool":
		if q.rules.noForcedTools {
			return badReq("tool_choice: type \"tool\" and \"any\" are not supported for this model.")
		}
	default:
		return badReq("tool_choice.type: Input should be 'auto', 'any', 'tool' or 'none'")
	}
	if len(q.tools) == 0 {
		return badReq("tool_choice: may only be specified while providing tools")
	}
	q.toolChoiceType = tc.Type
	var c bytes.Buffer
	if err := json.Compact(&c, raw); err == nil {
		q.toolChoice = c.String()
	}
	return nil
}

func parseCC(raw json.RawMessage, path string) (*aCC, *aErr) {
	var c struct {
		Type string `json:"type"`
		TTL  string `json:"ttl"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, badReq("%s: %v", path, err)
	}
	if c.Type != "ephemeral" {
		return nil, badReq("%s.type: Input should be 'ephemeral'", path)
	}
	switch c.TTL {
	case "", "5m":
		return &aCC{ttl: "5m"}, nil
	case "1h":
		return &aCC{ttl: "1h"}, nil
	}
	return nil, badReq("%s.ttl: Input should be '5m' or '1h'", path)
}

func (q *aReq) parseSystem() *aErr {
	raw, ok := q.raw["system"]
	if !ok {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		q.systemString = true
		if strings.TrimSpace(s) != "" {
			q.system = []aBlock{{typ: "text", text: s, raw: textRaw(s)}}
		}
		return nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return badReq("system: Input should be a valid string or a list of text blocks")
	}
	for i, r := range list {
		b, e := parseBlock(r, fmt.Sprintf("system.%d", i), "system")
		if e != nil {
			return e
		}
		if b.typ != "text" {
			return badReq("system.%d.type: Input should be 'text'", i)
		}
		q.system = append(q.system, b)
	}
	return nil
}

func (q *aReq) parseMessages() *aErr {
	raw, ok := q.raw["messages"]
	if !ok {
		return badReq("messages: Field required")
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
		return badReq("messages: at least one message is required")
	}
	for i, r := range list {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(r, &m); err != nil {
			return badReq("messages.%d: Input should be an object", i)
		}
		for _, k := range sortedKeys(m) {
			switch k {
			case "role", "content", "clear_at", "output_config":
			default:
				return badReq("messages.%d.%s: Extra inputs are not permitted", i, k)
			}
		}
		msg := aMsg{}
		if !unmarshalField(m, "role", &msg.role) {
			return badReq("messages.%d.role: Field required", i)
		}
		switch msg.role {
		case "user", "assistant", "system":
		default:
			return badReq("messages.%d.role: Input should be 'user', 'assistant' or 'system'", i)
		}
		if ca, ok := m["clear_at"]; ok {
			if msg.role != "system" {
				return badReq("messages.%d.clear_at: Extra inputs are not permitted", i)
			}
			if !q.betas[betaClearAt] {
				return badReq("messages.%d.system.clear_at: Extra inputs are not permitted", i)
			}
			if err := json.Unmarshal(ca, &msg.clearAt); err != nil || (msg.clearAt != "never" && msg.clearAt != "next_user_message") {
				return badReq("messages.%d.system.clear_at: Input should be 'never' or 'next_user_message'", i)
			}
			if msg.clearAt == "never" {
				msg.clearAt = ""
			}
		}
		if _, ok := m["output_config"]; ok && msg.role != "system" {
			return badReq("messages.%d.output_config: Extra inputs are not permitted", i)
		}
		if msg.role == "system" && !q.rules.midSystem {
			return badReq("messages.%d.role: role 'system' is not supported on this model", i)
		}

		content, has := m["content"]
		if !has {
			return badReq("messages.%d.content: Field required", i)
		}
		var str string
		if json.Unmarshal(content, &str) == nil {
			if strings.TrimSpace(str) == "" {
				return badReq("messages.%d: all messages must have non-empty content except for the optional final assistant message", i)
			}
			msg.blocks = []aBlock{{typ: "text", text: str, raw: textRaw(str)}}
		} else {
			var blocks []json.RawMessage
			if err := json.Unmarshal(content, &blocks); err != nil {
				return badReq("messages.%d.content: Input should be a valid string or a list of content blocks", i)
			}
			for j, br := range blocks {
				b, e := parseBlock(br, fmt.Sprintf("messages.%d.content.%d", i, j), msg.role)
				if e != nil {
					return e
				}
				msg.blocks = append(msg.blocks, b)
			}
			if len(msg.blocks) == 0 && !(msg.role == "system" && len(m["output_config"]) > 0) && i != len(list)-1 {
				return badReq("messages.%d: all messages must have non-empty content except for the optional final assistant message", i)
			}
		}
		if msg.role == "system" {
			for j, b := range msg.blocks {
				if b.typ != "text" {
					return badReq("messages.%d.content.%d.type: a system message accepts only text blocks", i, j)
				}
				if b.cc != nil {
					return badReq("messages.%d.content.%d.cache_control: cache_control is not allowed on a system message; put the breakpoint on the preceding user turn", i, j)
				}
			}
		}
		q.msgs = append(q.msgs, msg)
	}
	return nil
}

// parseBlock decodes one content block. role is the role of the message that
// holds it ("system" for the top-level system array).
func parseBlock(raw json.RawMessage, path, role string) (aBlock, *aErr) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return aBlock{}, badReq("%s: Input should be an object", path)
	}
	b := aBlock{raw: raw}
	if !unmarshalField(m, "type", &b.typ) {
		return aBlock{}, badReq("%s.type: Field required", path)
	}
	allowed := map[string][]string{
		"text":              {"type", "text", "cache_control", "citations"},
		"image":             {"type", "source", "cache_control"},
		"document":          {"type", "source", "title", "context", "citations", "cache_control"},
		"tool_use":          {"type", "id", "name", "input", "cache_control", "caller"},
		"tool_result":       {"type", "tool_use_id", "content", "is_error", "cache_control"},
		"thinking":          {"type", "thinking", "signature"},
		"redacted_thinking": {"type", "data"},
		"compaction":        {"type", "content", "cache_control"},
	}
	if keys, ok := allowed[b.typ]; ok {
		for _, k := range sortedKeys(m) {
			if !contains(keys, k) {
				return aBlock{}, badReq("%s.%s.%s: Extra inputs are not permitted", path, b.typ, k)
			}
		}
	}
	if c, ok := m["cache_control"]; ok {
		cc, e := parseCC(c, path+".cache_control")
		if e != nil {
			return aBlock{}, e
		}
		b.cc = cc
	}
	switch b.typ {
	case "text":
		_ = json.Unmarshal(m["text"], &b.text)
		switch {
		case b.text == "":
			if b.cc != nil {
				return aBlock{}, badReq("%s.text: cache_control cannot be set for empty text blocks", path)
			}
			return aBlock{}, badReq("%s.text: text content blocks must be non-empty", path)
		case strings.TrimSpace(b.text) == "":
			return aBlock{}, badReq("%s.text: text content blocks must contain non-whitespace text", path)
		}
	case "image":
		if e := checkImage(m["source"], path); e != nil {
			return aBlock{}, e
		}
		b.images = 1
	case "tool_use":
		if !unmarshalField(m, "id", &b.id) || b.id == "" {
			return aBlock{}, badReq("%s.tool_use.id: Field required", path)
		}
		if !unmarshalField(m, "name", &b.name) || b.name == "" {
			return aBlock{}, badReq("%s.tool_use.name: Field required", path)
		}
		in := m["input"]
		if len(in) == 0 || in[0] != '{' {
			return aBlock{}, badReq("%s.tool_use.input: Input should be a valid dictionary", path)
		}
		b.input = in
	case "tool_result":
		if role != "user" {
			return aBlock{}, badReq("%s: tool_result blocks belong in user messages", path)
		}
		if !unmarshalField(m, "tool_use_id", &b.toolUseID) || b.toolUseID == "" {
			return aBlock{}, badReq("%s.tool_result.tool_use_id: Field required", path)
		}
		_ = json.Unmarshal(m["is_error"], &b.isError)
		if c, ok := m["content"]; ok {
			var s string
			if json.Unmarshal(c, &s) == nil {
				if s != "" {
					b.result = []aBlock{{typ: "text", text: s}}
				}
			} else {
				var kids []json.RawMessage
				if err := json.Unmarshal(c, &kids); err != nil {
					return aBlock{}, badReq("%s.tool_result.content: Input should be a valid string or a list of blocks", path)
				}
				for k, kr := range kids {
					kb, e := parseBlock(kr, fmt.Sprintf("%s.content.%d", path, k), "tool_result")
					if e != nil {
						return aBlock{}, e
					}
					if kb.typ != "text" && kb.typ != "image" && kb.typ != "document" {
						return aBlock{}, badReq("%s.content.%d.type: tool_result content accepts text, image and document blocks", path, k)
					}
					b.images += kb.images
					b.result = append(b.result, kb)
				}
			}
		}
		if b.cc != nil && len(b.result) == 0 {
			return aBlock{}, badReq("%s.cache_control: cache_control cannot be set for an empty tool_result", path)
		}
	case "thinking":
		if role != "assistant" {
			return aBlock{}, badReq("%s: thinking blocks belong in assistant messages", path)
		}
		_ = json.Unmarshal(m["thinking"], &b.thinking)
		if !unmarshalField(m, "signature", &b.signature) || b.signature == "" {
			return aBlock{}, badReq("%s.thinking.signature: Field required", path)
		}
	case "redacted_thinking":
		if role != "assistant" {
			return aBlock{}, badReq("%s: redacted_thinking blocks belong in assistant messages", path)
		}
		if !unmarshalField(m, "data", &b.signature) || b.signature == "" {
			return aBlock{}, badReq("%s.redacted_thinking.data: Field required", path)
		}
	case "compaction":
		_ = json.Unmarshal(m["content"], &b.content)
	case "document", "server_tool_use", "web_search_tool_result", "mcp_tool_use", "fallback":
		// Accepted and opaque.
	default:
		return aBlock{}, badReq("%s.type: Input tag %q found using 'type' does not match any of the expected tags", path, b.typ)
	}
	return b, nil
}

func checkImage(raw json.RawMessage, path string) *aErr {
	var s struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
		FileID    string `json:"file_id"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return badReq("%s.image.source: Field required", path)
	}
	switch s.Type {
	case "base64":
		switch s.MediaType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			return badReq("%s.image.source.base64.media_type: Input should be 'image/jpeg', 'image/png', 'image/gif' or 'image/webp'", path)
		}
		if s.Data == "" {
			return badReq("%s.image.source.base64.data: Field required", path)
		}
	case "url":
		if !strings.HasPrefix(s.URL, "http") {
			return badReq("%s.image.source.url.url: Input should be a valid URL", path)
		}
	case "file":
		if s.FileID == "" {
			return badReq("%s.image.source.file.file_id: Field required", path)
		}
	default:
		return badReq("%s.image.source.type: Input should be 'base64', 'url' or 'file'", path)
	}
	return nil
}

// checkStructure enforces the ordering rules of the messages array.
func (q *aReq) checkStructure() *aErr {
	if q.msgs[0].role == "system" {
		return badReq("messages.0: a system message cannot be the first message; use the top-level system parameter")
	}
	for i, m := range q.msgs {
		switch m.role {
		case "assistant":
			var ids []string
			for _, b := range m.blocks {
				if b.typ == "tool_use" {
					ids = append(ids, b.id)
				}
			}
			if len(ids) > 0 {
				if i+1 >= len(q.msgs) || q.msgs[i+1].role != "user" {
					return badReq("messages.%d: `tool_use` ids were found without `tool_result` blocks immediately after: %s. Each `tool_use` block must have a corresponding `tool_result` block in the next message.", i, strings.Join(ids, ", "))
				}
				answered := map[string]bool{}
				for _, b := range q.msgs[i+1].blocks {
					if b.typ == "tool_result" {
						answered[b.toolUseID] = true
					}
				}
				var missing []string
				for _, id := range ids {
					if !answered[id] {
						missing = append(missing, id)
					}
				}
				if len(missing) > 0 {
					return badReq("messages.%d: `tool_use` ids were found without `tool_result` blocks immediately after: %s. Each `tool_use` block must have a corresponding `tool_result` block in the next message.", i, strings.Join(missing, ", "))
				}
			}
		case "user":
			var prev []aBlock
			if i > 0 && q.msgs[i-1].role == "assistant" {
				prev = q.msgs[i-1].blocks
			}
			asked := map[string]bool{}
			for _, b := range prev {
				if b.typ == "tool_use" {
					asked[b.id] = true
				}
			}
			seenOther := false
			for j, b := range m.blocks {
				if b.typ != "tool_result" {
					seenOther = true
					continue
				}
				if !asked[b.toolUseID] {
					return badReq("messages.%d.content.%d: unexpected `tool_use_id` found in `tool_result` blocks: %s. Each `tool_result` block must have a corresponding `tool_use` block in the previous message.", i, j, b.toolUseID)
				}
				if seenOther {
					return badReq("messages.%d.content.%d: `tool_result` blocks must come first in the content of a user message; found one after other content", i, j)
				}
			}
		case "system":
			// Placement (docs): follows a user message, and is the last entry or is
			// followed by an assistant turn. An effort-only message (no content) may
			// sit anywhere.
			if len(m.blocks) == 0 {
				continue
			}
			if q.msgs[i-1].role != "user" {
				return badReq("messages.%d: a system message must follow a user message", i)
			}
			if i+1 < len(q.msgs) && q.msgs[i+1].role != "assistant" {
				return badReq("messages.%d: a system message must be the last message or be followed by an assistant message", i)
			}
		}
	}
	return nil
}

// checkPrewarm applies the max_tokens:0 restrictions.
func (q *aReq) checkPrewarm() *aErr {
	if q.maxTokens != 0 {
		return nil
	}
	switch {
	case q.stream:
		return badReq("max_tokens: 0 is not allowed with stream: true")
	case q.thinkingType == "enabled":
		return badReq("max_tokens: 0 is not allowed with thinking.type \"enabled\"")
	case q.toolChoiceType == "any" || q.toolChoiceType == "tool":
		return badReq("max_tokens: 0 is not allowed with a forced tool_choice")
	}
	if raw, ok := q.raw["output_config"]; ok && bytes.Contains(raw, []byte(`"format"`)) {
		return badReq("max_tokens: 0 is not allowed with output_config.format")
	}
	return nil
}
