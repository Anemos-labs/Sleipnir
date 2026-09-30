package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/cost"
)

// Config configures the mock server.
type Config struct {
	Engines int
	Engine  EngineConfig
	PinTTL  time.Duration
	Price   cost.Price // dollars per million tokens, for usage.cost

	// Latency model. All zero by default so tests run instantly.
	FirstToken time.Duration
	PrefillPer time.Duration // per uncached prompt token
	DecodePer  time.Duration // per output token

	// CacheGenerated also publishes the assistant reply's blocks (engines that
	// cache decode output). Off by default: the conservative assumption.
	CacheGenerated bool

	// RPM simulates a per-key request limit over a sliding minute (0 = off).
	RPM int

	Now func() time.Time
}

// ToolCall is a model-issued function call.
type ToolCall struct {
	ID, Name, Args string
}

// Msg is a parsed chat message.
type Msg struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	// ReasoningDetails is what the client sent back on an assistant message.
	ReasoningDetails json.RawMessage
}

// Call is what a Responder sees.
type Call struct {
	N        int // 1-based request counter across the server
	Model    string
	Session  string
	Engine   int
	System   string
	Messages []Msg
	Tools    []string
	// Cached is how many prompt tokens were served from cache for this call.
	Cached int
	Prompt int
	Raw    map[string]json.RawMessage
}

// LastUser returns the content of the last user message.
func (c *Call) LastUser() string {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == "user" {
			return c.Messages[i].Content
		}
	}
	return ""
}

// Fault makes a call fail.
type Fault struct {
	Status     int
	Message    string
	RetryAfter time.Duration
	// MidStream sends an in-band error after some content has streamed.
	MidStream bool
}

// Reply is a scripted model response.
type Reply struct {
	Text             string
	Reasoning        string
	ReasoningDetails json.RawMessage
	ToolCalls        []ToolCall
	Finish           string // default "stop" / "tool_calls"
	OutputTokens     int    // override; default derived from content
	Fault            *Fault
}

// Responder produces the model's reply.
type Responder func(c *Call) Reply

// CallStat is one served request, for assertions.
type CallStat struct {
	N            int
	Session      string
	Engine       int
	Model        string
	PromptTokens int
	Cached       int
	Completion   int
	Cost         float64
	Streamed     bool
	Status       int
	FirstByteAt  time.Time
	StartedAt    time.Time
	Header       http.Header
}

// HitRatio is cached/prompt for the call.
func (s CallStat) HitRatio() float64 {
	if s.PromptTokens == 0 {
		return 0
	}
	return float64(s.Cached) / float64(s.PromptTokens)
}

// Server is the mock provider.
type Server struct {
	cfg    Config
	router *Router
	resp   Responder

	mu     sync.Mutex
	n      int
	stats  []CallStat
	window []time.Time
}

// New builds a Server. A nil responder echoes the last user message.
func New(cfg Config, r Responder) *Server {
	if cfg.Engines == 0 {
		cfg.Engines = 1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Price.InputPerM == 0 {
		cfg.Price = cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1}
	}
	engines := make([]*Engine, cfg.Engines)
	for i := range engines {
		engines[i] = NewEngine(cfg.Engine, cfg.Now)
	}
	if r == nil {
		r = func(c *Call) Reply { return Reply{Text: "echo: " + c.LastUser()} }
	}
	return &Server{cfg: cfg, router: NewRouter(engines, cfg.PinTTL, cfg.Now), resp: r}
}

// Handler serves the OpenAI-style API rooted at "/".
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", s.handleChat)
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	mux.HandleFunc("/models", s.handleModels)
	mux.HandleFunc("/v1/models", s.handleModels)
	return mux
}

// Start launches an httptest server; callers Close it.
func (s *Server) Start() *httptest.Server { return httptest.NewServer(s.Handler()) }

// SetResponder swaps the responder.
func (s *Server) SetResponder(r Responder) {
	s.mu.Lock()
	s.resp = r
	s.mu.Unlock()
}

// Stats returns a copy of all served calls in order.
func (s *Server) Stats() []CallStat {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CallStat(nil), s.stats...)
}

// Reset clears stats and counters (caches are kept).
func (s *Server) Reset() {
	s.mu.Lock()
	s.stats, s.n = nil, 0
	s.mu.Unlock()
}

// FlushCaches empties the prefix cache of every engine (see Engine.Flush): a provider that dropped its cache. The conversation pins
// are kept, so the same requests go to the same engines and find them cold.
func (s *Server) FlushCaches() {
	for i := 0; i < s.router.Len(); i++ {
		s.router.Engine(i).Flush()
	}
}

// CacheOutage takes the prefix cache of every engine away for d (see Engine.SetDown) and returns at once: every request in that time
// finds nothing it was promised, and when it is over the cache is empty. It is the demo's cache break.
func (s *Server) CacheOutage(d time.Duration) {
	for i := 0; i < s.router.Len(); i++ {
		s.router.Engine(i).SetDown(true)
	}
	time.AfterFunc(d, func() {
		for i := 0; i < s.router.Len(); i++ {
			s.router.Engine(i).SetDown(false)
		}
	})
}

// Router exposes the router for tests that inspect engines.
func (s *Server) Router() *Router { return s.router }

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"data":[{"id":"mock-1","context_length":1000000,"pricing":{"prompt":"0.000004","completion":"0.00002","input_cache_read":"0.000001"},"supported_parameters":["tools","reasoning_effort"]}]}`)
}

func (s *Server) rateLimit(w http.ResponseWriter) bool {
	if s.cfg.RPM <= 0 {
		return false
	}
	now := s.cfg.Now()
	s.mu.Lock()
	cut := now.Add(-time.Minute)
	kept := s.window[:0]
	for _, t := range s.window {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	s.window = kept
	remaining := s.cfg.RPM - len(s.window)
	limited := remaining <= 0
	if !limited {
		s.window = append(s.window, now)
		remaining--
	}
	s.mu.Unlock()
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(s.cfg.RPM))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(max(remaining, 0)))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(time.Minute).Unix(), 10))
	if limited {
		w.Header().Set("Retry-After", "1")
		writeErr(w, 429, "Rate limit exceeded", "rate_limit_exceeded")
	}
	return limited
}

func writeErr(w http.ResponseWriter, status int, msg, typ string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": status, "message": msg, "metadata": map[string]any{"error_type": typ},
	}})
}

// parsedRequest is the decoded chat request.
type parsedRequest struct {
	raw      map[string]json.RawMessage
	model    string
	session  string
	stream   bool
	messages []Msg
	system   string
	tools    []string
	toolsRaw json.RawMessage
}

func parseChat(r *http.Request, body []byte) (*parsedRequest, error) {
	p := &parsedRequest{}
	if err := json.Unmarshal(body, &p.raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	json.Unmarshal(p.raw["model"], &p.model)
	json.Unmarshal(p.raw["stream"], &p.stream)
	// Affinity precedence as documented by the marketplace: body session_id,
	// then prompt_cache_key, then the header.
	var sid, pck string
	json.Unmarshal(p.raw["session_id"], &sid)
	json.Unmarshal(p.raw["prompt_cache_key"], &pck)
	switch {
	case sid != "":
		p.session = sid
	case pck != "":
		p.session = pck
	default:
		p.session = r.Header.Get("X-Session-Id")
	}
	if t, ok := p.raw["tools"]; ok {
		p.toolsRaw = t
		var tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(t, &tools); err != nil {
			return nil, fmt.Errorf("invalid tools: %v", err)
		}
		for _, x := range tools {
			p.tools = append(p.tools, x.Function.Name)
		}
	}
	var msgs []struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
		ToolCallID       string          `json:"tool_call_id"`
		ReasoningDetails json.RawMessage `json:"reasoning_details"`
	}
	if err := json.Unmarshal(p.raw["messages"], &msgs); err != nil || len(msgs) == 0 {
		return nil, fmt.Errorf("messages must be a non-empty array")
	}
	for _, m := range msgs {
		msg := Msg{Role: m.Role, ToolCallID: m.ToolCallID, ReasoningDetails: m.ReasoningDetails}
		msg.Content = contentText(m.Content)
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments})
		}
		p.messages = append(p.messages, msg)
	}
	if err := validateOrder(p.messages); err != nil {
		return nil, err
	}
	if p.messages[0].Role == "system" || p.messages[0].Role == "developer" {
		p.system = p.messages[0].Content
	}
	return p, nil
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(p.Text)
		}
		return sb.String()
	}
	return string(raw)
}

// validateOrder enforces the message rules real chat servers enforce.
func validateOrder(msgs []Msg) error {
	var pending map[string]bool
	for i, m := range msgs {
		switch m.Role {
		case "system", "developer":
			if i != 0 {
				return fmt.Errorf("messages[%d]: system message must come first", i)
			}
		case "user":
			if len(pending) > 0 {
				return fmt.Errorf("messages[%d]: tool_calls %v were not answered before the next user message", i, keysOf(pending))
			}
		case "assistant":
			if len(pending) > 0 {
				return fmt.Errorf("messages[%d]: tool_calls %v were not answered", i, keysOf(pending))
			}
			pending = map[string]bool{}
			for _, tc := range m.ToolCalls {
				if tc.ID == "" {
					return fmt.Errorf("messages[%d]: tool call without id", i)
				}
				pending[tc.ID] = true
			}
		case "tool":
			if !pending[m.ToolCallID] {
				return fmt.Errorf("messages[%d]: tool message answers unknown tool_call_id %q", i, m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
		default:
			return fmt.Errorf("messages[%d]: invalid role %q", i, m.Role)
		}
	}
	return nil
}

func keysOf(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// renderPrompt is the mock's chat template: the byte stream engines hash.
func (p *parsedRequest) renderPrompt() []byte {
	var b bytes.Buffer
	if len(p.toolsRaw) > 0 {
		b.WriteString("<|tools|>")
		b.Write(p.toolsRaw)
		b.WriteString("<|end|>\n")
	}
	for _, m := range p.messages {
		b.WriteString("<|" + m.Role + "|>\n")
		b.WriteString(m.Content)
		if m.ToolCallID != "" {
			b.WriteString("[" + m.ToolCallID + "]")
		}
		for _, tc := range m.ToolCalls {
			b.WriteString("<|call|>" + tc.ID + ":" + tc.Name + "(" + tc.Args + ")")
		}
		if len(m.ReasoningDetails) > 0 {
			b.WriteString("<|reasoning|>")
			b.Write(m.ReasoningDetails)
		}
		b.WriteString("\n<|end|>\n")
	}
	b.WriteString("<|assistant|>\n")
	return b.Bytes()
}
