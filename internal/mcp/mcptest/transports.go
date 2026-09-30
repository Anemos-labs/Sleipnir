package mcptest

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// ServeStdio serves one client over newline-delimited JSON-RPC until the
// reader ends. Requests run concurrently, so a slow tool cannot block the
// delivery of its own cancellation.
func (s *Server) ServeStdio(r io.Reader, w io.Writer) error {
	ss := s.newSession("stdio")
	defer ss.close()
	var wmu sync.Mutex
	write := func(b []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		line := make([]byte, 0, len(b)+1)
		line = append(append(line, b...), '\n')
		_, _ = w.Write(line)
	}
	ss.mu.Lock()
	ss.stream = func(b []byte) bool { write(b); return true }
	ss.crashFn = func() {
		if c, ok := w.(io.Closer); ok {
			_ = c.Close()
		}
		if c, ok := r.(io.Closer); ok {
			_ = c.Close()
		}
	}
	ss.mu.Unlock()

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		ss.process(append([]byte(nil), line...), write, false)
	}
	return sc.Err()
}

// HTTPOptions shapes the streamable HTTP endpoint.
type HTTPOptions struct {
	// JSONOnly answers requests with application/json instead of an SSE stream.
	JSONOnly bool
	// NoSessions never issues Mcp-Session-Id.
	NoSessions bool
	// NoStream answers GET with 405 (a server with nothing to push).
	NoStream bool
}

// Recorded is one HTTP request the server received.
type Recorded struct {
	Method string
	Header http.Header
	Body   string
}

// HTTP is the streamable HTTP endpoint of a Server (an http.Handler).
type HTTP struct {
	s *Server
	o HTTPOptions

	mu       sync.Mutex
	sessions map[string]*session
	shared   *session
	log      []Recorded
}

// HTTP returns a streamable HTTP handler for the server.
func (s *Server) HTTP(o HTTPOptions) *HTTP {
	return &HTTP{s: s, o: o, sessions: map[string]*session{}}
}

// Requests returns every request received so far.
func (h *HTTP) Requests() []Recorded {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Recorded(nil), h.log...)
}

// ExpireSessions forgets every session: the next request with an old id gets
// 404, as a restarted server would answer.
func (h *HTTP) ExpireSessions() {
	h.mu.Lock()
	h.sessions = map[string]*session{}
	h.mu.Unlock()
}

func newSID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (h *HTTP) session(w http.ResponseWriter, r *http.Request, create bool) *session {
	if h.o.NoSessions {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.shared == nil {
			h.shared = h.s.newSession("shared")
			h.shared.crashFn = func() { panic(http.ErrAbortHandler) }
		}
		return h.shared
	}
	if create {
		ss := h.s.newSession(newSID())
		ss.crashFn = func() { panic(http.ErrAbortHandler) }
		h.mu.Lock()
		h.sessions[ss.id] = ss
		h.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", ss.id)
		return ss
	}
	sid := r.Header.Get("Mcp-Session-Id")
	h.mu.Lock()
	ss := h.sessions[sid]
	h.mu.Unlock()
	if ss == nil {
		if sid == "" {
			http.Error(w, "missing session", http.StatusBadRequest)
		} else {
			http.Error(w, "unknown session", http.StatusNotFound)
		}
	}
	return ss
}

func (h *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	h.mu.Lock()
	h.log = append(h.log, Recorded{Method: r.Method, Header: r.Header.Clone(), Body: string(body)})
	h.mu.Unlock()
	switch r.Method {
	case http.MethodPost:
		h.post(w, r, body)
	case http.MethodGet:
		h.get(w, r)
	case http.MethodDelete:
		sid := r.Header.Get("Mcp-Session-Id")
		h.mu.Lock()
		ss := h.sessions[sid]
		delete(h.sessions, sid)
		h.mu.Unlock()
		if ss != nil {
			ss.close()
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *HTTP) post(w http.ResponseWriter, r *http.Request, body []byte) {
	accept := r.Header.Get("Accept")
	if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
		http.Error(w, "Accept must list application/json and text/event-stream", http.StatusNotAcceptable)
		return
	}
	var m rpcIn
	if err := jsonUnmarshal(body, &m); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	isRequest := m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null"
	ss := h.session(w, r, m.Method == "initialize")
	if ss == nil {
		return
	}
	if !isRequest {
		ss.process(body, func([]byte) {}, true)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if h.o.JSONOnly {
		var mu sync.Mutex
		var resp []byte
		ss.process(body, func(b []byte) {
			var x rpcIn
			if jsonUnmarshal(b, &x) == nil && x.Method == "" && idKey(x.ID) == idKey(m.ID) {
				mu.Lock()
				resp = b
				mu.Unlock()
			}
		}, true)
		mu.Lock()
		defer mu.Unlock()
		if resp == nil {
			http.Error(w, "no response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	var mu sync.Mutex
	out := func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	if fl != nil {
		fl.Flush()
	}
	ss.process(body, out, true)
}

func (h *HTTP) get(w http.ResponseWriter, r *http.Request) {
	if h.o.NoStream {
		http.Error(w, "no stream", http.StatusMethodNotAllowed)
		return
	}
	ss := h.session(w, r, false)
	if ss == nil {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	ch := make(chan []byte, 32)
	push := func(b []byte) bool {
		select {
		case ch <- b:
			return true
		default:
			return false
		}
	}
	ss.attach(push)
	defer func() {
		ss.mu.Lock()
		ss.stream = nil
		ss.mu.Unlock()
	}()
	for {
		select {
		case b := <-ch:
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			if fl != nil {
				fl.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

// SSEOptions shapes the legacy HTTP+SSE endpoint.
type SSEOptions struct {
	// Endpoint, when set, is announced verbatim as the message endpoint (to test
	// how a client treats a hostile one).
	Endpoint string
}

// SSE is the legacy HTTP+SSE endpoint of a Server: GET .../sse opens the
// stream, POST .../messages?sessionId=... sends messages.
type SSE struct {
	s *Server
	o SSEOptions

	mu       sync.Mutex
	sessions map[string]*sseSession
	posts    []string
}

type sseSession struct {
	ss   *session
	send func([]byte) bool
	stop chan struct{}
}

// SSE returns a legacy SSE handler for the server.
func (s *Server) SSE(o SSEOptions) *SSE { return &SSE{s: s, o: o, sessions: map[string]*sseSession{}} }

// PostedTo lists the URLs (path and query) messages were POSTed to.
func (h *SSE) PostedTo() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.posts...)
}

func (h *SSE) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.stream(w, r)
	case http.MethodPost:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		h.mu.Lock()
		h.posts = append(h.posts, r.URL.RequestURI())
		sess := h.sessions[r.URL.Query().Get("sessionId")]
		h.mu.Unlock()
		if sess == nil {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		sess.ss.process(body, func(b []byte) { sess.send(b) }, false)
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "Accepted")
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *SSE) stream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	var wmu sync.Mutex
	over := false
	write := func(event, data string) {
		wmu.Lock()
		defer wmu.Unlock()
		if over { // a request goroutine that outlived the stream: the ResponseWriter is no longer ours
			return
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		if fl != nil {
			fl.Flush()
		}
	}
	defer func() {
		wmu.Lock()
		over = true
		wmu.Unlock()
	}()
	sid := newSID()
	ss := h.s.newSession(sid)
	defer ss.close()
	stop := make(chan struct{})
	var once sync.Once
	sess := &sseSession{ss: ss, stop: stop, send: func(b []byte) bool { write("message", string(b)); return true }}
	ss.mu.Lock()
	ss.stream = sess.send
	ss.crashFn = func() { once.Do(func() { close(stop) }) }
	ss.mu.Unlock()
	h.mu.Lock()
	h.sessions[sid] = sess
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.sessions, sid)
		h.mu.Unlock()
	}()
	endpoint := "/messages?sessionId=" + sid
	if h.o.Endpoint != "" {
		endpoint = h.o.Endpoint
	}
	write("endpoint", endpoint)
	select {
	case <-r.Context().Done():
	case <-stop:
	}
}
