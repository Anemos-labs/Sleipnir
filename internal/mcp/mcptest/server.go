// Package mcptest is a small, well-behaved MCP server for tests: the reference
// the client is tested against, over stdio, streamable HTTP and the legacy SSE
// transport. It is independent of the client code (it implements the protocol
// itself), so a bug shared by both sides cannot hide.
//
// The reference tools are echo, big, slow, crash, add_tool (list_changed),
// image, error, structured, links, progress, sampling, roots, env, pid and
// unicode; there are two resources, a resource template and two prompts. Tests
// add their own tools with AddTool and change the list at runtime with
// AddTool, RemoveTool and NotifyToolsChanged.
//
// Misbehaving servers (hangs, floods, garbage, malformed JSON-RPC) are not
// here: a test that needs one scripts the raw bytes itself, where the
// misbehaviour is visible next to the assertion.
package mcptest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Versions the reference server speaks, newest first.
var Versions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// Tool is one tool of the server.
type Tool struct {
	Name        string
	Description string
	// Schema is the input schema; default {"type":"object"}.
	Schema      json.RawMessage
	Annotations map[string]any
	Handler     func(ctx context.Context, c *Call) *Result
}

// Result is what a tool handler returns.
type Result struct {
	Content    []map[string]any
	Structured any
	IsError    bool
	// Raw, when set, is sent verbatim as the response's "result".
	Raw json.RawMessage
	// Drop sends no response at all (a tool that hangs up on the call).
	Drop bool
}

// Text returns a text content item.
func Text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

// TextResult returns a result with one text item.
func TextResult(s string) *Result { return &Result{Content: []map[string]any{Text(s)}} }

// Prompt is a prompt template.
type Prompt struct {
	Name        string
	Description string
	Arguments   []map[string]any
	Render      func(args map[string]string) []map[string]any // messages
}

// Server is the reference MCP server.
type Server struct {
	Name         string
	Version      string
	Instructions string
	// ProtocolVersion, when set, is always what initialize answers with;
	// otherwise the client's version if the server speaks it, else the newest.
	ProtocolVersion string
	// PageSize splits tools/list into pages of this many tools (0: one page).
	PageSize int
	// Capabilities replaces the default capabilities object when non-nil.
	Capabilities map[string]any
	// OnCrash replaces the default crash (dropping the connection); the
	// subprocess helper sets it to exit the process.
	OnCrash func()

	mu        sync.Mutex
	tools     map[string]*Tool
	order     []string
	prompts   []Prompt
	sessions  map[*session]struct{}
	cancelled []string
	calls     map[string]int
	inits     []string // protocolVersion of each initialize received
	added     int
}

// New returns a server with the reference tools, resources and prompts.
func New() *Server {
	s := &Server{
		Name: "mcptest", Version: "1.0.0",
		tools: map[string]*Tool{}, sessions: map[*session]struct{}{}, calls: map[string]int{},
	}
	for _, t := range referenceTools(s) {
		s.AddTool(t)
	}
	s.prompts = []Prompt{
		{Name: "greet", Description: "Greet someone", Arguments: []map[string]any{{"name": "name", "description": "who to greet", "required": true}},
			Render: func(a map[string]string) []map[string]any {
				return []map[string]any{{"role": "user", "content": Text("Please greet " + a["name"] + " warmly.")}}
			}},
		{Name: "review", Description: "Review code",
			Render: func(map[string]string) []map[string]any {
				return []map[string]any{
					{"role": "user", "content": Text("Review the diff.")},
					{"role": "assistant", "content": Text("Send it over.")},
				}
			}},
	}
	return s
}

// AddTool adds or replaces a tool. It does not notify clients; call
// NotifyToolsChanged for that.
func (s *Server) AddTool(t Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tools[t.Name]; !ok {
		s.order = append(s.order, t.Name)
	}
	tt := t
	s.tools[t.Name] = &tt
}

// RemoveTool removes a tool.
func (s *Server) RemoveTool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tools, name)
	for i, n := range s.order {
		if n == name {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// SetTools replaces the whole tool list.
func (s *Server) SetTools(ts ...Tool) {
	s.mu.Lock()
	s.tools, s.order = map[string]*Tool{}, nil
	s.mu.Unlock()
	for _, t := range ts {
		s.AddTool(t)
	}
}

// NotifyToolsChanged sends notifications/tools/list_changed to every client
// that can receive it (stdio, an open GET stream, a legacy SSE stream).
func (s *Server) NotifyToolsChanged() { s.broadcast("notifications/tools/list_changed") }

// NotifyPromptsChanged sends notifications/prompts/list_changed.
func (s *Server) NotifyPromptsChanged() { s.broadcast("notifications/prompts/list_changed") }

func (s *Server) broadcast(method string) {
	s.mu.Lock()
	ss := make([]*session, 0, len(s.sessions))
	for x := range s.sessions {
		ss = append(ss, x)
	}
	s.mu.Unlock()
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	for _, x := range ss {
		x.push(msg)
	}
}

// Cancelled returns the request ids the server saw notifications/cancelled for.
func (s *Server) Cancelled() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cancelled...)
}

// Calls reports how many times a tool was called.
func (s *Server) Calls(tool string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[tool]
}

// Initializes returns the protocol version of every initialize received.
func (s *Server) Initializes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.inits...)
}

// Sessions returns the number of live sessions/connections.
func (s *Server) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Server) capabilities() map[string]any {
	if s.Capabilities != nil {
		return s.Capabilities
	}
	return map[string]any{
		"tools":     map[string]any{"listChanged": true},
		"resources": map[string]any{"listChanged": true},
		"prompts":   map[string]any{"listChanged": true},
		"logging":   map[string]any{},
	}
}

// Call is one tool invocation, handed to handlers.
type Call struct {
	Args json.RawMessage
	Name string
	ss   *session
	out  func([]byte)
	meta struct {
		Token json.RawMessage `json:"progressToken"`
	}
}

// Progress sends notifications/progress for this call (if the client asked).
func (c *Call) Progress(progress, total float64, msg string) {
	if len(c.meta.Token) == 0 {
		return
	}
	p := map[string]any{"progressToken": c.meta.Token, "progress": progress}
	if total > 0 {
		p["total"] = total
	}
	if msg != "" {
		p["message"] = msg
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": p})
	c.out(b)
}

// Ask sends a request to the client and waits for its answer: a result, or an
// error object.
func (c *Call) Ask(ctx context.Context, method string, params any) (result json.RawMessage, rpcErr map[string]any, err error) {
	return c.ss.ask(ctx, c.out, method, params)
}

// Crash ends the connection abruptly (in the subprocess helper: exits).
func (c *Call) Crash() {
	if c.ss.s.OnCrash != nil {
		c.ss.s.OnCrash()
		return
	}
	c.ss.crash()
}

// session is one client connection: a stdio process pair, an HTTP session, or a
// legacy SSE stream.
type session struct {
	s  *Server
	id string

	mu       sync.Mutex
	waiting  map[string]chan json.RawMessage
	nextID   int64
	inflight map[string]context.CancelFunc
	stream   func([]byte) bool // server-initiated messages (nil when no channel is open)
	crashFn  func()
	init     bool
}

func (s *Server) newSession(id string) *session {
	ss := &session{s: s, id: id, waiting: map[string]chan json.RawMessage{}, inflight: map[string]context.CancelFunc{}, crashFn: func() {}}
	s.mu.Lock()
	s.sessions[ss] = struct{}{}
	s.mu.Unlock()
	return ss
}

func (ss *session) close() {
	ss.s.mu.Lock()
	delete(ss.s.sessions, ss)
	ss.s.mu.Unlock()
	ss.mu.Lock()
	for _, c := range ss.inflight {
		c()
	}
	ss.mu.Unlock()
}

func (ss *session) crash() { ss.crashFn() }

// push delivers a server-initiated message on the session's open channel, if any.
func (ss *session) push(msg []byte) {
	ss.mu.Lock()
	f := ss.stream
	ss.mu.Unlock()
	if f != nil {
		f(msg)
	}
}

func (ss *session) ask(ctx context.Context, out func([]byte), method string, params any) (json.RawMessage, map[string]any, error) {
	ss.mu.Lock()
	ss.nextID++
	id := "srv-" + strconv.FormatInt(ss.nextID, 10)
	ch := make(chan json.RawMessage, 1)
	ss.waiting[id] = ch
	ss.mu.Unlock()
	defer func() {
		ss.mu.Lock()
		delete(ss.waiting, id)
		ss.mu.Unlock()
	}()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	out(b)
	select {
	case raw := <-ch:
		var m struct {
			Result json.RawMessage `json:"result"`
			Error  map[string]any  `json:"error"`
		}
		_ = json.Unmarshal(raw, &m)
		return m.Result, m.Error, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-time.After(10 * time.Second):
		return nil, nil, fmt.Errorf("client did not answer %s", method)
	}
}

type rpcIn struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func idKey(raw json.RawMessage) string { return strings.TrimSpace(strings.Trim(string(raw), `"`)) }

// process handles one incoming message. A request is answered on out; when sync
// is false it runs in its own goroutine (stdio, where a slow tool must not
// block the reader that will deliver its cancellation). It reports whether the
// message was a request.
func (ss *session) process(raw []byte, out func([]byte), sync bool) (isRequest bool) {
	var m rpcIn
	if err := json.Unmarshal(raw, &m); err != nil {
		out(errorResponse(nil, -32700, "parse error"))
		return false
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method != "" && hasID:
		if sync {
			ss.request(m, out)
		} else {
			go ss.request(m, out)
		}
		return true
	case m.Method != "":
		ss.notification(m)
	case hasID:
		ss.mu.Lock()
		ch := ss.waiting[idKey(m.ID)]
		ss.mu.Unlock()
		if ch != nil {
			ch <- append(json.RawMessage(nil), raw...)
		}
	}
	return false
}

func (ss *session) notification(m rpcIn) {
	switch m.Method {
	case "notifications/initialized":
		ss.mu.Lock()
		ss.init = true
		ss.mu.Unlock()
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
			Reason    string          `json:"reason"`
		}
		_ = json.Unmarshal(m.Params, &p)
		key := idKey(p.RequestID)
		ss.s.mu.Lock()
		ss.s.cancelled = append(ss.s.cancelled, key)
		ss.s.mu.Unlock()
		ss.mu.Lock()
		cancel := ss.inflight[key]
		ss.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

func errorResponse(id json.RawMessage, code int, msg string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	return b
}

func resultResponse(id json.RawMessage, result any) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return b
}

func (ss *session) request(m rpcIn, out func([]byte)) {
	ctx, cancel := context.WithCancel(context.Background())
	key := idKey(m.ID)
	ss.mu.Lock()
	ss.inflight[key] = cancel
	ss.mu.Unlock()
	defer func() {
		cancel()
		ss.mu.Lock()
		delete(ss.inflight, key)
		ss.mu.Unlock()
	}()

	s := ss.s
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
		s.mu.Lock()
		s.inits = append(s.inits, p.ProtocolVersion)
		s.mu.Unlock()
		ver := s.ProtocolVersion
		if ver == "" {
			ver = Versions[0]
			for _, v := range Versions {
				if v == p.ProtocolVersion {
					ver = v
				}
			}
		}
		res := map[string]any{
			"protocolVersion": ver, "capabilities": s.capabilities(),
			"serverInfo": map[string]any{"name": s.Name, "version": s.Version},
		}
		if s.Instructions != "" {
			res["instructions"] = s.Instructions
		}
		out(resultResponse(m.ID, res))
	case "ping":
		out(resultResponse(m.ID, map[string]any{}))
	case "tools/list":
		out(resultResponse(m.ID, s.listTools(m.Params)))
	case "tools/call":
		ss.callTool(ctx, m, out)
	case "resources/list":
		out(resultResponse(m.ID, map[string]any{"resources": []map[string]any{
			{"uri": "file:///readme.md", "name": "readme", "mimeType": "text/markdown", "description": "The readme"},
			{"uri": "mem://blob", "name": "blob", "mimeType": "application/octet-stream"},
		}}))
	case "resources/templates/list":
		out(resultResponse(m.ID, map[string]any{"resourceTemplates": []map[string]any{
			{"uriTemplate": "file:///{path}", "name": "files", "description": "Any file", "mimeType": "text/plain"},
		}}))
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(m.Params, &p)
		switch p.URI {
		case "file:///readme.md":
			out(resultResponse(m.ID, map[string]any{"contents": []map[string]any{{"uri": p.URI, "mimeType": "text/markdown", "text": "# Readme\nhello"}}}))
		case "mem://blob":
			out(resultResponse(m.ID, map[string]any{"contents": []map[string]any{{"uri": p.URI, "mimeType": "application/octet-stream", "blob": base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 3})}}}))
		default:
			out(errorResponse(m.ID, -32002, "Resource not found"))
		}
	case "prompts/list":
		var ps []map[string]any
		s.mu.Lock()
		for _, p := range s.prompts {
			e := map[string]any{"name": p.Name, "description": p.Description}
			if p.Arguments != nil {
				e["arguments"] = p.Arguments
			}
			ps = append(ps, e)
		}
		s.mu.Unlock()
		out(resultResponse(m.ID, map[string]any{"prompts": ps}))
	case "prompts/get":
		var p struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		_ = json.Unmarshal(m.Params, &p)
		s.mu.Lock()
		var found *Prompt
		for i := range s.prompts {
			if s.prompts[i].Name == p.Name {
				found = &s.prompts[i]
			}
		}
		s.mu.Unlock()
		if found == nil {
			out(errorResponse(m.ID, -32602, "Unknown prompt: "+p.Name))
			return
		}
		out(resultResponse(m.ID, map[string]any{"description": found.Description, "messages": found.Render(p.Arguments)}))
	default:
		out(errorResponse(m.ID, -32601, "Method not found: "+m.Method))
	}
}

func (s *Server) listTools(params json.RawMessage) map[string]any {
	var p struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(params, &p)
	s.mu.Lock()
	defer s.mu.Unlock()
	names := append([]string(nil), s.order...)
	start := 0
	if strings.HasPrefix(p.Cursor, "c") {
		start, _ = strconv.Atoi(p.Cursor[1:])
	}
	end := len(names)
	next := ""
	if s.PageSize > 0 && start+s.PageSize < end {
		end = start + s.PageSize
		next = "c" + strconv.Itoa(end)
	}
	if start > len(names) {
		start = len(names)
	}
	list := []map[string]any{}
	for _, n := range names[start:end] {
		t := s.tools[n]
		e := map[string]any{"name": t.Name, "description": t.Description}
		if len(t.Schema) > 0 {
			e["inputSchema"] = t.Schema
		} else {
			e["inputSchema"] = map[string]any{"type": "object"}
		}
		if t.Annotations != nil {
			e["annotations"] = t.Annotations
		}
		list = append(list, e)
	}
	res := map[string]any{"tools": list}
	if next != "" {
		res["nextCursor"] = next
	}
	return res
}

func (ss *session) callTool(ctx context.Context, m rpcIn, out func([]byte)) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		out(errorResponse(m.ID, -32602, "Invalid params"))
		return
	}
	s := ss.s
	s.mu.Lock()
	t := s.tools[p.Name]
	s.calls[p.Name]++
	s.mu.Unlock()
	if t == nil {
		out(errorResponse(m.ID, -32602, "Unknown tool: "+p.Name))
		return
	}
	c := &Call{Args: p.Arguments, Name: p.Name, ss: ss, out: out}
	c.meta.Token = p.Meta.ProgressToken
	res := t.Handler(ctx, c)
	if res == nil || res.Drop || ctx.Err() != nil {
		return // a cancelled request gets no response (spec)
	}
	if res.Raw != nil {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res.Raw})
		out(b)
		return
	}
	body := map[string]any{"content": res.Content}
	if res.Content == nil {
		body["content"] = []any{}
	}
	if res.IsError {
		body["isError"] = true
	}
	if res.Structured != nil {
		body["structuredContent"] = res.Structured
	}
	out(resultResponse(m.ID, body))
}

// ToolNames lists the current tool names in listing order.
func (s *Server) ToolNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

func sortedEnv() []string {
	env := os.Environ()
	sort.Strings(env)
	return env
}
