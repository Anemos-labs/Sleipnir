package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	hdrSession = "Mcp-Session-Id"
	hdrVersion = "MCP-Protocol-Version"
	userAgent  = "sleipnir-mcp"

	// After this many consecutive transport-level failures (connection refused,
	// reset, TLS errors) the HTTP transport declares the server gone so the
	// manager reconnects with backoff instead of failing call after call.
	maxConsecutiveFailures = 3

	// A GET stream that delivers nothing, not even a keep-alive comment, for
	// this long is presumed dead (a NAT that dropped the flow never sends a FIN).
	listenIdleTimeout = 10 * time.Minute
)

// HTTPOptions configures the HTTP transports (streamable HTTP and legacy SSE).
type HTTPOptions struct {
	// URL is the server endpoint; Headers are sent with every request (after
	// ${VAR} expansion; the transport-managed headers cannot be set).
	URL     string
	Headers map[string]string
	Net     NetOptions
	// MaxMessageBytes bounds one incoming message (default DefaultMaxMessageBytes).
	MaxMessageBytes int
	// Redact masks secrets in error text. Optional.
	Redact *redactor
	// OnDrop is told about discarded input. Optional; must not block.
	OnDrop func(reason string)
}

func (o *HTTPOptions) prepare() (*url.URL, http.Header, int, error) {
	u, err := parseServerURL(o.URL)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("url: %w", err)
	}
	if err := checkTransportURL(u, o.Net.AllowPrivate); err != nil {
		return nil, nil, 0, err
	}
	hdr := http.Header{}
	for _, k := range sortedKeys(o.Headers) {
		if err := validHeader(k, o.Headers[k]); err != nil {
			return nil, nil, 0, fmt.Errorf("headers.%s: %w", clipForError(k), err)
		}
		hdr.Set(k, o.Headers[k])
	}
	max := o.MaxMessageBytes
	if max <= 0 {
		max = DefaultMaxMessageBytes
	}
	return u, hdr, max, nil
}

// httpTransport is the streamable HTTP transport (MCP 2025-03-26 and later).
//
// Every client message is a POST to the one endpoint. The server answers a
// request with a JSON body or an SSE stream (which may carry server requests
// and notifications before the response), and a notification or a response
// with 202. A session id the server assigns at initialize travels back in
// Mcp-Session-Id; the negotiated protocol version in MCP-Protocol-Version. A
// GET on the same endpoint opens a stream for server-initiated messages that
// belong to no request (tools/list_changed above all), and a DELETE ends the
// session.
type httpTransport struct {
	u      *url.URL
	client *http.Client
	hdr    http.Header
	max    int
	redact *redactor
	onDrop func(string)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	h      Handler

	mu       sync.Mutex
	session  string
	version  string
	failures int
	failed   bool
	closing  bool // no new stream goroutines once Close has begun

	listenOnce sync.Once
	closeOnce  sync.Once
}

// NewHTTPTransport returns a streamable HTTP transport.
func NewHTTPTransport(o HTTPOptions) (Transport, error) {
	u, hdr, max, err := o.prepare()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &httpTransport{
		u: u, client: newHTTPClient(o.Net), hdr: hdr, max: max, redact: o.Redact, onDrop: o.OnDrop,
		ctx: ctx, cancel: cancel,
	}, nil
}

// Start implements Transport.
func (t *httpTransport) Start(h Handler) error {
	t.h = h
	return nil
}

// SetProtocolVersion implements versioned.
func (t *httpTransport) SetProtocolVersion(v string) {
	t.mu.Lock()
	t.version = v
	t.mu.Unlock()
}

func (t *httpTransport) drop(reason string) {
	if t.onDrop != nil {
		t.onDrop(reason)
	}
}

// spawn runs f as a tracked goroutine unless the transport is closing. Close
// waits for tracked goroutines, and a WaitGroup must not gain members once a
// Wait may have started; this is the one place they are added.
func (t *httpTransport) spawn(f func()) bool {
	t.mu.Lock()
	if t.closing {
		t.mu.Unlock()
		return false
	}
	t.wg.Add(1)
	t.mu.Unlock()
	go func() {
		defer t.wg.Done()
		f()
	}()
	return true
}

// fail ends the transport on its own account, once.
func (t *httpTransport) fail(err error) {
	t.mu.Lock()
	first := !t.failed
	t.failed = true
	t.mu.Unlock()
	if first && t.ctx.Err() == nil && t.h.Closed != nil {
		t.h.Closed(err)
	}
}

func (t *httpTransport) request(ctx context.Context, method string, body []byte, accept string) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.u.String(), rd)
	if err != nil {
		return nil, errors.New("could not build request")
	}
	for k, v := range t.hdr {
		req.Header[k] = append([]string(nil), v...)
	}
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", accept)
	t.mu.Lock()
	if t.session != "" {
		req.Header.Set(hdrSession, t.session)
	}
	if t.version != "" {
		req.Header.Set(hdrVersion, t.version)
	}
	t.mu.Unlock()
	return req, nil
}

func (t *httpTransport) noteSuccess() {
	t.mu.Lock()
	t.failures = 0
	t.mu.Unlock()
}

// noteFailure counts a transport-level failure of one request. A caller giving
// up (context canceled or timed out) is not the server's fault.
func (t *httpTransport) noteFailure(err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	t.mu.Lock()
	t.failures++
	dead := t.failures >= maxConsecutiveFailures
	t.mu.Unlock()
	if dead {
		t.fail(fmt.Errorf("server unreachable: %w", netError(t.redact, err)))
	}
}

func (t *httpTransport) captureSession(resp *http.Response) {
	sid := resp.Header.Get(hdrSession)
	if sid == "" || len(sid) > 256 {
		return
	}
	for i := 0; i < len(sid); i++ {
		if sid[i] < 0x21 || sid[i] > 0x7e { // the spec allows visible ASCII only
			return
		}
	}
	t.mu.Lock()
	if t.session == "" {
		t.session = sid
	}
	t.mu.Unlock()
}

func (t *httpTransport) hasSession() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.session != ""
}

// Send implements Transport.
func (t *httpTransport) Send(ctx context.Context, msg []byte) error {
	if t.ctx.Err() != nil {
		return &closedError{}
	}
	var env envelope
	_ = json.Unmarshal(msg, &env)
	isRequest := env.Method != "" && env.hasID()
	reqID := append([]byte(nil), env.ID...)

	// The request lives as long as its response stream, which outlasts Send: it
	// ends when the caller cancels, the transport closes, or the stream is done.
	rctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(t.ctx, cancel)
	// release detaches the request from the transport's lifetime and cancels it;
	// the stream goroutine calls it when it is done, so nothing accumulates on
	// t.ctx over thousands of calls.
	release := func() {
		stop()
		cancel()
	}

	req, err := t.request(rctx, http.MethodPost, msg, "application/json, text/event-stream")
	if err != nil {
		release()
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		release()
		t.noteFailure(err)
		return t.wrapErr(err)
	}
	t.noteSuccess()
	streaming, err := t.handle(resp, isRequest, reqID, rctx, release)
	if !streaming {
		release()
	}
	return err
}

// wrapErr turns a net/http error into one that is safe to surface: the URL
// (which may hold a token) is gone and secrets are masked, while the cause
// stays in the chain for errors.Is (a blocked address, a cancelled context).
func (t *httpTransport) wrapErr(err error) error { return netError(t.redact, err) }

// handle interprets the response to one POST. streaming reports that a
// goroutine now owns resp.Body and the request context.
func (t *httpTransport) handle(resp *http.Response, isRequest bool, reqID []byte, rctx context.Context, release func()) (streaming bool, err error) {
	defer func() {
		if !streaming {
			resp.Body.Close()
		}
	}()
	code := resp.StatusCode
	if code >= 200 && code < 300 {
		t.captureSession(resp)
	}
	switch {
	case code == http.StatusAccepted || code == http.StatusNoContent:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		if isRequest {
			return false, errors.New("server accepted the request but will not answer it (HTTP 202)")
		}
		return false, nil
	case code >= 200 && code < 300:
		ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		switch ct {
		case "text/event-stream":
			t.wg.Add(1)
			go t.readPostStream(resp, isRequest, reqID, rctx, release)
			return true, nil
		case "application/json", "application/json-rpc":
			return false, t.readJSONBody(resp, isRequest, reqID)
		case "":
			if resp.ContentLength == 0 && !isRequest {
				return false, nil
			}
		}
		return false, fmt.Errorf("server answered with unexpected content type %q", clipForError(ct))
	case code == http.StatusNotFound && t.hasSession():
		t.fail(ErrSessionExpired)
		return false, ErrSessionExpired
	case code >= 300 && code < 400:
		return false, fmt.Errorf("server redirected to %s; redirects are not followed (they could carry credentials to another origin)", redirectHost(resp))
	}
	// An error status may still carry a JSON-RPC error worth relaying (servers
	// answer "invalid session" or "bad request" that way).
	if isRequest && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var env envelope
		if json.Unmarshal(body, &env) == nil && env.Error != nil {
			env.ID = reqID
			out, _ := json.Marshal(env)
			t.deliver(out)
			return false, nil
		}
	}
	return false, httpStatusError(code)
}

func redirectHost(resp *http.Response) string {
	if loc, err := resp.Location(); err == nil && loc.Host != "" {
		return cleanText(loc.Host)
	}
	return "another location"
}

func httpStatusError(code int) error {
	msg := fmt.Sprintf("server returned HTTP %d %s", code, http.StatusText(code))
	switch code {
	case http.StatusUnauthorized:
		msg += " (authentication required: set an Authorization header)"
	case http.StatusForbidden:
		msg += " (access denied)"
	case http.StatusNotFound:
		msg += " (nothing at this URL: check it)"
	case http.StatusTooManyRequests:
		msg += " (rate limited)"
	}
	return errors.New(msg)
}

func (t *httpTransport) deliver(msg []byte) {
	if t.h.Message != nil {
		t.h.Message(msg)
	}
}

// readJSONBody delivers a single-JSON response, bounded.
func (t *httpTransport) readJSONBody(resp *http.Response, isRequest bool, reqID []byte) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.max)+1))
	switch {
	case err != nil:
		return fmt.Errorf("reading response: %s", t.redact.apply(cleanText(transportError(err).Error())))
	case len(body) > t.max:
		t.drop("dropped an oversized response")
		if isRequest && len(reqID) > 0 {
			t.deliver(tooLargeResponse(reqID, t.max))
			return nil
		}
		return ErrMessageTooLarge
	}
	if len(bytes.TrimSpace(body)) > 0 {
		t.deliver(body)
	}
	return nil
}

// readPostStream reads the SSE stream that answers one POST. When the stream
// ends before the response arrived the call is failed by a synthetic error
// response for its id (a real response delivered earlier makes it a harmless
// duplicate, ignored by the client), so a dropped connection is a prompt error
// instead of a wait for the timeout.
func (t *httpTransport) readPostStream(resp *http.Response, isRequest bool, reqID []byte, rctx context.Context, release func()) {
	defer release()
	defer resp.Body.Close()
	rd := newSSEReader(resp.Body, t.max)
	for {
		ev, err := rd.Next()
		if err != nil {
			if rctx.Err() != nil {
				return // the call finished or was cancelled: not a failure
			}
			if isRequest && len(reqID) > 0 {
				if errors.Is(err, ErrMessageTooLarge) {
					t.drop("dropped an oversized event")
					t.deliver(tooLargeResponse(reqID, t.max))
				} else {
					t.deliver(marshalError(reqID, CodeInternalError, "server closed the response stream before answering"))
				}
			}
			return
		}
		if (ev.Event != "" && ev.Event != "message") || ev.Data == "" {
			continue // other event types, and keep-alives with no payload
		}
		t.deliver([]byte(ev.Data))
	}
}

// Listen implements listener: it opens the GET stream for server-initiated
// messages. A server without one answers 405, which ends the attempt quietly.
func (t *httpTransport) Listen() {
	t.listenOnce.Do(func() { t.spawn(t.listenLoop) })
}

func (t *httpTransport) listenLoop() {
	delay := time.Second
	failures := 0
	lastID := ""
	for t.ctx.Err() == nil {
		streamed, id, retry, stop := t.listenAttempt(lastID)
		if id != "" {
			lastID = id
		}
		if stop {
			return
		}
		if streamed {
			failures, delay = 0, time.Second
		} else if failures++; failures >= 5 {
			return
		}
		if retry >= 0 {
			delay = min(max(time.Duration(retry)*time.Millisecond, 100*time.Millisecond), 30*time.Second)
		}
		select {
		case <-time.After(delay):
		case <-t.ctx.Done():
			return
		}
		if !streamed {
			delay = min(delay*2, 30*time.Second)
		}
	}
}

// listenAttempt makes one attempt at the GET stream. stop ends the loop for good
// (the server has no such stream, or the session is gone).
func (t *httpTransport) listenAttempt(lastID string) (streamed bool, id string, retry int, stop bool) {
	retry = -1
	ctx, cancel := context.WithCancel(t.ctx)
	defer cancel()
	req, err := t.request(ctx, http.MethodGet, nil, "text/event-stream")
	if err != nil {
		return false, "", retry, true
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return false, "", retry, false
	}
	defer resp.Body.Close()
	switch code := resp.StatusCode; {
	case code == http.StatusNotFound && t.hasSession():
		t.fail(ErrSessionExpired)
		return false, "", retry, true
	case code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented || code == http.StatusNotFound || code == http.StatusUnauthorized || code == http.StatusForbidden:
		return false, "", retry, true // no server-initiated stream on offer
	case code < 200 || code >= 300:
		return false, "", retry, false
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct != "text/event-stream" {
		return false, "", retry, true
	}
	// A dead peer that never closes the flow would leave this read blocked
	// forever; the idle timer cancels the request when nothing arrives.
	idle := time.AfterFunc(listenIdleTimeout, cancel)
	defer idle.Stop()
	rd := newSSEReader(&idleReader{r: resp.Body, timer: idle, d: listenIdleTimeout}, t.max)
	for {
		ev, err := rd.Next()
		if err != nil {
			return streamed, rd.lastEventID(), retry, false
		}
		streamed = true
		if ev.Retry >= 0 {
			retry = ev.Retry
		}
		if (ev.Event != "" && ev.Event != "message") || ev.Data == "" {
			continue
		}
		t.deliver([]byte(ev.Data))
	}
}

// idleReader resets a timer on every read that returns data.
type idleReader struct {
	r     io.Reader
	timer *time.Timer
	d     time.Duration
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.timer.Reset(i.d)
	}
	return n, err
}

// Close implements Transport. It ends the session (best effort: a server may
// answer 405, meaning it does not let clients end sessions), aborts every
// stream and waits for the goroutines that read them.
func (t *httpTransport) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closing = true
		hadSession := t.session != ""
		t.mu.Unlock()
		if hadSession {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if req, err := t.request(ctx, http.MethodDelete, nil, "application/json"); err == nil {
				if resp, err := t.client.Do(req); err == nil {
					_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
					resp.Body.Close()
				}
			}
			cancel()
		}
		t.cancel()
		done := make(chan struct{})
		go func() { t.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		t.client.CloseIdleConnections()
	})
	return nil
}
