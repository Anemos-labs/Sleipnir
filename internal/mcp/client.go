package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// ListKind names one of the three listings a server can announce a change to.
type ListKind string

const (
	ListTools     ListKind = "tools"
	ListPrompts   ListKind = "prompts"
	ListResources ListKind = "resources"
)

var listKinds = []ListKind{ListTools, ListPrompts, ListResources}

// Root is a workspace root advertised to a server (roots/list).
type Root struct {
	Path string // absolute filesystem path
	Name string
}

// Progress is a progress notification for a running call.
type Progress struct {
	Progress float64
	Total    float64 // 0 when unknown
	Message  string
}

// LogMessage is a notifications/message from a server. Text is sanitised.
type LogMessage struct {
	Level  string
	Logger string
	Text   string
}

// ClientOptions configures a Client. The zero value is usable.
type ClientOptions struct {
	// Name and Version identify this client in initialize (default "sleipnir",
	// "dev").
	Name, Version string

	// InitTimeout is how long the server may stay silent during the handshake
	// (default 60s; callers normally also bound it with a context deadline).
	InitTimeout time.Duration

	// Roots are advertised to the server (the roots capability, and answers to
	// roots/list). Give them to local servers only: a remote server has no
	// business learning local paths.
	Roots []Root

	// RequestTimeout applies to calls that set no timeout of their own
	// (listings, ping). Default 30s.
	RequestTimeout time.Duration
	// MaxCallDuration is the absolute cap on one call, however much progress it
	// reports. Default 30 minutes.
	MaxCallDuration time.Duration
	// MaxInFlight bounds concurrent requests to this server; further calls wait.
	// Default 32.
	MaxInFlight int
	// List limits: a server cannot make a listing unbounded. Defaults 100 pages,
	// 2000 items, 32 MiB of responses.
	MaxListPages int
	MaxListItems int
	MaxListBytes int

	// OnListChanged is told, from its own goroutine and coalesced, that a
	// server announced a change of one of its listings. It runs off the
	// transport's read path, so it may take its time (a manager re-lists there).
	OnListChanged func(ListKind)
	// OnLog receives notifications/message. Optional.
	OnLog func(LogMessage)
	// Logf receives diagnostics. Optional.
	Logf func(format string, args ...any)

	redact *redactor
}

func (o *ClientOptions) defaults() {
	if o.Name == "" {
		o.Name = "sleipnir"
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 30 * time.Second
	}
	if o.InitTimeout <= 0 {
		o.InitTimeout = 60 * time.Second
	}
	if o.MaxCallDuration <= 0 {
		o.MaxCallDuration = 30 * time.Minute
	}
	if o.MaxInFlight <= 0 {
		o.MaxInFlight = 32
	}
	if o.MaxListPages <= 0 {
		o.MaxListPages = 100
	}
	if o.MaxListItems <= 0 {
		o.MaxListItems = 2000
	}
	if o.MaxListBytes <= 0 {
		o.MaxListBytes = 32 << 20
	}
}

// Stats are counters of things a client tolerated. They exist so tests and
// diagnostics can see that hostile input was met and absorbed.
type Stats struct {
	Malformed     int64 // messages that were not valid JSON-RPC
	Unexpected    int64 // responses to nothing we sent, cancelled or duplicated
	Unknown       int64 // notifications and requests of unknown methods
	Dropped       int64 // callbacks dropped because the dispatcher was saturated
	ServerRequest int64 // requests the server made of us
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

type pending struct {
	ch       chan rpcResult
	progress func(Progress)
	activity chan struct{}
}

// Client is one MCP connection: JSON-RPC 2.0 request/response correlation over
// a Transport, the initialize handshake, and typed calls for the protocol's
// tools, resources and prompts. Its methods are safe for concurrent use.
//
// The client is strict about what it sends and tolerant about what it
// receives: outgoing messages are exactly formed, incoming ones may carry
// unknown fields, unknown notifications, batches, numeric-string ids and
// non-JSON noise, none of which disturb the connection (they are counted, see
// Stats), while size, rate and memory stay bounded whatever the server does.
type Client struct {
	t    Transport
	opts ClientOptions

	mu      sync.Mutex
	nextID  int64
	pending map[int64]*pending
	closed  bool
	cause   error

	done      chan struct{}
	closeOnce sync.Once
	bg        sync.WaitGroup
	sem       chan struct{}

	replies   chan []byte
	callbacks chan func()
	dirty     [3]atomic.Bool
	wake      chan struct{}

	init   atomic.Pointer[InitializeResult]
	badRun atomic.Int64

	statMalformed, statUnexpected, statUnknown, statDropped, statServerReq atomic.Int64
}

// NewClient returns a client over t. Call Start, then Initialize; or use
// Connect, which does all three.
func NewClient(t Transport, o ClientOptions) *Client {
	o.defaults()
	return &Client{
		t: t, opts: o,
		pending:   map[int64]*pending{},
		done:      make(chan struct{}),
		sem:       make(chan struct{}, o.MaxInFlight),
		replies:   make(chan []byte, 64),
		callbacks: make(chan func(), 256),
		wake:      make(chan struct{}, 1),
	}
}

// Connect starts the transport, performs the initialize handshake and returns
// a ready client. On failure the transport is closed.
func Connect(ctx context.Context, t Transport, o ClientOptions) (*Client, error) {
	c := NewClient(t, o)
	if err := c.Start(); err != nil {
		_ = t.Close()
		return nil, err
	}
	if _, err := c.Initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Start starts the transport and the client's helper goroutines.
func (c *Client) Start() error {
	if err := c.t.Start(Handler{Message: c.handle, Closed: func(err error) { c.fail(err) }}); err != nil {
		return err
	}
	c.bg.Add(2)
	go c.replyLoop()
	go c.dispatchLoop()
	return nil
}

// Done is closed when the connection has ended, for any reason.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err reports why the connection ended (nil while it is open). errors.Is(err,
// ErrClosed) always holds; the cause (server exit status, session expiry) is
// wrapped inside.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		return nil
	}
	return &closedError{cause: c.cause}
}

// Stats returns the tolerance counters.
func (c *Client) Stats() Stats {
	return Stats{
		Malformed: c.statMalformed.Load(), Unexpected: c.statUnexpected.Load(), Unknown: c.statUnknown.Load(),
		Dropped: c.statDropped.Load(), ServerRequest: c.statServerReq.Load(),
	}
}

// Initialized returns the handshake result (nil before Initialize succeeded).
func (c *Client) Initialized() *InitializeResult { return c.init.Load() }

// logf forwards formatted diagnostics when the MCP client has a logging callback.
func (c *Client) logf(format string, args ...any) {
	if c.opts.Logf != nil {
		c.opts.Logf(format, args...)
	}
}

// clean makes server-provided error text safe to surface: sanitised, capped,
// and stripped of any credential the manager knows this server holds.
func (c *Client) clean(s string) string {
	s, _ = truncateRunes(cleanText(s), 500)
	return c.opts.redact.apply(s)
}

// fail ends the client: waiting calls are released with the cause. It never
// touches the transport (it may run on the transport's own goroutine); Close
// does that.
func (c *Client) fail(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.cause = cause
	pend := c.pending
	c.pending = map[int64]*pending{}
	close(c.done)
	c.mu.Unlock()
	err := &closedError{cause: cause}
	for _, p := range pend {
		p.ch <- rpcResult{err: err} // capacity 1, one send per entry: never blocks
	}
}

// Close ends the connection: it fails outstanding calls, shuts the transport
// down (for a child process: stdin EOF, then SIGTERM, then SIGKILL to the
// whole process group) and waits for the client's goroutines.
func (c *Client) Close() error {
	c.fail(nil)
	c.closeOnce.Do(func() { _ = c.t.Close() })
	// Bounded: a callback that never returns must not turn Close into a hang.
	wait := make(chan struct{})
	go func() { c.bg.Wait(); close(wait) }()
	select {
	case <-wait:
	case <-time.After(5 * time.Second):
		c.logf("mcp: helper goroutines did not stop within 5s of Close")
	}
	return nil
}

// safely runs a callback so that a panic in code we do not own cannot take the
// whole harness down from a goroutine nobody supervises.
func (c *Client) safely(f func()) {
	defer func() {
		if r := recover(); r != nil {
			c.logf("mcp: callback panicked: %v\n%s", r, debug.Stack())
		}
	}()
	f()
}

// ---- incoming ----

// handle processes one incoming message. It runs on the transport's read
// goroutine (or several, for HTTP), so it never blocks: replies and callbacks
// go through bounded queues.
func (c *Client) handle(msg []byte) {
	msg = bytes.TrimSpace(msg)
	if len(msg) > 0 && msg[0] == '[' {
		// JSON-RPC batches were legal in 2025-03-26; tolerate them from servers.
		var batch []json.RawMessage
		if json.Unmarshal(msg, &batch) != nil || len(batch) == 0 {
			c.malformed()
			return
		}
		for i, m := range batch {
			if i >= 256 {
				break
			}
			c.handleOne(m)
		}
		return
	}
	c.handleOne(msg)
}

// maxMalformedRun is how many consecutive unparseable messages end a
// connection: one more is never going to be the valid one.
const maxMalformedRun = 1000

// malformed counts malformed messages and fails the client when the consecutive-malformation limit
// is exceeded.
func (c *Client) malformed() {
	c.statMalformed.Add(1)
	if c.badRun.Add(1) > maxMalformedRun {
		c.fail(errors.New("server sent too many malformed messages"))
	}
}

// handleOne decodes and routes an MCP request, notification, or response, resetting the
// malformed-message streak for recognized envelopes.
func (c *Client) handleOne(msg []byte) {
	var env envelope
	if err := json.Unmarshal(msg, &env); err != nil {
		c.malformed()
		return
	}
	switch {
	case env.Method != "" && env.hasID():
		c.badRun.Store(0)
		c.serverRequest(&env)
	case env.Method != "":
		c.badRun.Store(0)
		c.notification(&env)
	case env.hasID() || env.Result != nil || env.Error != nil:
		c.badRun.Store(0)
		c.response(&env)
	default:
		c.malformed()
	}
}

func (c *Client) response(env *envelope) {
	id, ok := callID(env.ID)
	if !ok {
		c.statUnexpected.Add(1)
		return
	}
	c.mu.Lock()
	p := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if p == nil {
		c.statUnexpected.Add(1) // late (cancelled, timed out), duplicate, or invented
		return
	}
	var r rpcResult
	switch {
	case env.Error != nil:
		r.err = &RPCError{Code: env.Error.Code, Message: c.clean(env.Error.Message), Data: env.Error.Data}
	case env.Result == nil || string(bytes.TrimSpace(env.Result)) == "null":
		r.result = json.RawMessage("{}")
	default:
		r.result = env.Result
	}
	p.ch <- r
}

// notification dispatches supported MCP notifications, ignores irrelevant lifecycle messages, and
// counts unknown methods.
func (c *Client) notification(env *envelope) {
	switch env.Method {
	case "notifications/progress":
		c.progress(env.Params)
	case "notifications/tools/list_changed":
		c.markDirty(ListTools)
	case "notifications/prompts/list_changed":
		c.markDirty(ListPrompts)
	case "notifications/resources/list_changed":
		c.markDirty(ListResources)
	case "notifications/message":
		c.logMessage(env.Params)
	case "notifications/cancelled", "notifications/resources/updated", "notifications/initialized":
		// A server cancelling a request it sent us (we hold none), or updates of
		// resources we never subscribed to: nothing to do, not an anomaly.
	default:
		c.statUnknown.Add(1)
	}
}

func (c *Client) progress(params json.RawMessage) {
	var p struct {
		Token    json.RawMessage `json:"progressToken"`
		Progress float64         `json:"progress"`
		Total    float64         `json:"total"`
		Message  string          `json:"message"`
	}
	if json.Unmarshal(params, &p) != nil {
		c.statUnknown.Add(1)
		return
	}
	id, ok := callID(p.Token)
	if !ok {
		c.statUnknown.Add(1)
		return
	}
	c.mu.Lock()
	pe := c.pending[id]
	c.mu.Unlock()
	if pe == nil {
		c.statUnknown.Add(1)
		return
	}
	select { // any sign of life restarts the call's inactivity timer
	case pe.activity <- struct{}{}:
	default:
	}
	if pe.progress != nil {
		prog := Progress{Progress: p.Progress, Total: p.Total, Message: c.clean(p.Message)}
		c.enqueue(func() { pe.progress(prog) })
	}
}

// logMessage decodes, sanitizes, and queues a server log callback, ignoring malformed messages or
// absent logging handlers.
func (c *Client) logMessage(params json.RawMessage) {
	if c.opts.OnLog == nil {
		return
	}
	var p struct {
		Level  string          `json:"level"`
		Logger string          `json:"logger"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	text := string(p.Data)
	var s string
	if json.Unmarshal(p.Data, &s) == nil {
		text = s
	}
	m := LogMessage{Level: cleanMeta(p.Level, 16), Logger: cleanMeta(p.Logger, 64), Text: c.clean(text)}
	c.enqueue(func() { c.opts.OnLog(m) })
}

// enqueue schedules a callback on the dispatcher. When the queue is full the
// callback is dropped: progress and logs are best effort, and blocking the
// read goroutine on them would let a chatty server wedge itself.
func (c *Client) enqueue(f func()) {
	select {
	case c.callbacks <- f:
	default:
		c.statDropped.Add(1)
	}
}

// markDirty records a list change. Unlike progress it must not be lost, so it
// is a flag, not a queue entry: any number of announcements before the
// dispatcher runs collapse into one callback, and the last one always fires.
func (c *Client) markDirty(k ListKind) {
	for i, kk := range listKinds {
		if kk == k {
			c.dirty[i].Store(true)
		}
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// dispatchLoop serially invokes queued callbacks and coalesced list-change notifications until
// client shutdown, containing callback panics.
func (c *Client) dispatchLoop() {
	defer c.bg.Done()
	for {
		select {
		case <-c.done:
			return
		case f := <-c.callbacks:
			c.safely(f)
		case <-c.wake:
			for i, k := range listKinds {
				if c.dirty[i].Swap(false) && c.opts.OnListChanged != nil {
					k := k
					c.safely(func() { c.opts.OnListChanged(k) })
				}
			}
		}
	}
}

// serverRequest answers a request the server made of us. Being a client with
// nothing to offer is the point: the answers are minimal, and never involve
// the harness's model or environment.
func (c *Client) serverRequest(env *envelope) {
	c.statServerReq.Add(1)
	var out []byte
	switch env.Method {
	case "ping":
		out, _ = marshalResult(env.ID, struct{}{})
	case "roots/list":
		if len(c.opts.Roots) == 0 {
			out = marshalError(env.ID, CodeMethodNotFound, "roots are not supported by this client")
			break
		}
		out, _ = marshalResult(env.ID, map[string]any{"roots": c.rootList()})
	case "sampling/createMessage":
		// Sampling would let a server run prompts of its choosing on the
		// harness's model, with the harness's credentials and budget, and read
		// what comes back. It is refused categorically.
		out = marshalError(env.ID, CodeMethodNotFound, "sampling is not supported by this client")
	case "elicitation/create":
		out = marshalError(env.ID, CodeMethodNotFound, "elicitation is not supported by this client")
	default:
		c.statUnknown.Add(1)
		out = marshalError(env.ID, CodeMethodNotFound, "method not supported by this client")
	}
	select {
	case c.replies <- out:
	default:
		c.statDropped.Add(1)
	}
}

// rootList converts configured filesystem roots to file URI objects and includes optional display
// names.
func (c *Client) rootList() []map[string]string {
	out := make([]map[string]string, 0, len(c.opts.Roots))
	for _, r := range c.opts.Roots {
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(r.Path)}
		if len(u.Path) > 0 && u.Path[0] != '/' {
			u.Path = "/" + u.Path // Windows drive paths
		}
		root := map[string]string{"uri": u.String()}
		if r.Name != "" {
			root["name"] = r.Name
		}
		out = append(out, root)
	}
	return out
}

// replyLoop sends queued server-request replies with individual ten-second deadlines until
// shutdown, ignoring send errors.
func (c *Client) replyLoop() {
	defer c.bg.Done()
	for {
		select {
		case <-c.done:
			return
		case msg := <-c.replies:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = c.t.Send(ctx, msg)
			cancel()
		}
	}
}

// ---- outgoing ----

type callOpts struct {
	timeout  time.Duration // inactivity timeout; 0 = client default, <0 = none
	maxTotal time.Duration // absolute cap; 0 = client default
	progress func(Progress)
	noCancel bool // initialize must not be cancelled (spec)
}

// request sends one request and waits for its response. build receives the
// request id so a params object can carry it as the progress token.
func (c *Client) request(ctx context.Context, method string, build func(id int64) any, o callOpts) (json.RawMessage, error) {
	// Backpressure: at most MaxInFlight requests at once, the rest wait.
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, c.Err()
	}

	timeout := o.timeout
	if timeout == 0 {
		timeout = c.opts.RequestTimeout
	}
	maxTotal := o.maxTotal
	if maxTotal <= 0 || maxTotal > c.opts.MaxCallDuration {
		maxTotal = c.opts.MaxCallDuration
	}

	p := &pending{ch: make(chan rpcResult, 1), progress: o.progress, activity: make(chan struct{}, 1)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, &closedError{cause: c.cause}
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = p
	c.mu.Unlock()

	var params any
	if build != nil {
		params = build(id)
	}
	msg, err := marshalRequest(id, method, params)
	if err != nil {
		c.forget(id)
		return nil, fmt.Errorf("mcp: encoding %s: %w", method, err)
	}

	// The call's context also aborts an HTTP request or response stream when the
	// call ends, so a finished or abandoned call leaves nothing running.
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Send runs beside the wait rather than before it. For stdio it returns as
	// soon as the bytes are written, but an HTTP server may hold the POST open
	// for as long as the tool runs (a JSON response arrives only at the end), and
	// the timers must already be ticking then: a call that cannot time out while
	// it is being sent is a call that can hang the agent forever.
	sendErr := make(chan error, 1)
	go func() { sendErr <- c.t.Send(cctx, msg) }()

	var idle <-chan time.Time
	var idleTimer *time.Timer
	if timeout > 0 {
		idleTimer = time.NewTimer(timeout)
		defer idleTimer.Stop()
		idle = idleTimer.C
	}
	total := time.NewTimer(maxTotal)
	defer total.Stop()
	for {
		select {
		case err := <-sendErr:
			sendErr = nil // sent; the answer comes through the handler
			if err != nil {
				select { // the answer may have raced in ahead of the error
				case r := <-p.ch:
					return r.result, r.err
				default:
				}
				c.forget(id)
				if errors.Is(err, ErrClosed) {
					return nil, c.endedErr(ctx, err)
				}
				if ctx.Err() != nil && !o.noCancel {
					c.sendCancel(id, ctx.Err())
				}
				return nil, err
			}
		case r := <-p.ch:
			return r.result, r.err
		case <-p.activity:
			if idleTimer != nil {
				idleTimer.Reset(timeout)
			}
		case <-idle:
			return c.abandon(id, p, &timeoutError{method: method, after: timeout}, o)
		case <-total.C:
			return c.abandon(id, p, &timeoutError{method: method, after: maxTotal, total: true}, o)
		case <-ctx.Done():
			return c.abandon(id, p, ctx.Err(), o)
		case <-c.done:
			// fail() has already put the closed error in p.ch, or the response won
			// the race; take whichever it is.
			select {
			case r := <-p.ch:
				return r.result, r.err
			default:
				return nil, c.Err()
			}
		}
	}
}

// closeCauseWait bounds how long a call whose send failed on a finished
// transport waits to be told why. A child process's transport collects the exit
// status (up to two seconds) and the last of its stderr (a third of a second)
// before it reports; this is a little more than both.
const closeCauseWait = 3 * time.Second

// endedErr is the error of a call whose send failed because the transport had
// finished. That error only knows the raw cause (end of file); the owner of
// the transport reports the real one (how the child died, why the session
// ended) through Closed a moment later, and that is what the caller, and
// through it the model, should be told. The wait is bounded so a transport
// that never reports cannot hold a call.
func (c *Client) endedErr(ctx context.Context, sendErr error) error {
	t := time.NewTimer(closeCauseWait)
	defer t.Stop()
	select {
	case <-c.done:
		return c.Err()
	case <-t.C:
	case <-ctx.Done():
	}
	return sendErr
}

// ended reports whether the connection is over or known to be ending: the
// transport has finished but has not yet told the client why (see ender). A
// call that arrives now would fail at once.
func (c *Client) ended() bool {
	select {
	case <-c.done:
		return true
	default:
	}
	e, ok := c.t.(ender)
	return ok && e.Ended()
}

// abandon gives up on a call: it stops waiting, tells the server to stop
// working (notifications/cancelled), and returns cause. A response that raced
// in just before is honoured instead.
func (c *Client) abandon(id int64, p *pending, cause error, o callOpts) (json.RawMessage, error) {
	c.forget(id)
	select {
	case r := <-p.ch:
		return r.result, r.err
	default:
	}
	if !o.noCancel {
		c.sendCancel(id, cause)
	}
	return nil, cause
}

// forget removes a pending RPC ID under the client lock.
func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// sendCancel tells the server a request is no longer wanted. It is best effort
// and off the caller's path: the caller already gave up, and the server may
// have finished anyway (a late response is then ignored as unexpected).
func (c *Client) sendCancel(id int64, cause error) {
	reason := "client cancelled"
	var te *timeoutError
	if errors.As(cause, &te) {
		reason = "client timed out waiting for a response"
	}
	msg, err := marshalNotification("notifications/cancelled", map[string]any{"requestId": id, "reason": reason})
	if err != nil {
		return
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	c.bg.Add(1)
	go func() {
		defer c.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = c.t.Send(ctx, msg)
	}()
}

// notify encodes and sends an MCP notification, translating closed-transport failures through
// client termination handling.
func (c *Client) notify(ctx context.Context, method string, params any) error {
	msg, err := marshalNotification(method, params)
	if err != nil {
		return err
	}
	if err := c.t.Send(ctx, msg); err != nil {
		if errors.Is(err, ErrClosed) {
			return c.endedErr(ctx, err)
		}
		return err
	}
	return nil
}

// ---- protocol ----

// Initialize performs the handshake: initialize, protocol version check,
// notifications/initialized. It must be called once, first.
func (c *Client) Initialize(ctx context.Context) (*InitializeResult, error) {
	caps := map[string]any{}
	if len(c.opts.Roots) > 0 {
		caps["roots"] = map[string]any{"listChanged": false}
	}
	// No sampling, no elicitation: those capabilities are refused, so they are
	// not declared.
	raw, err := c.request(ctx, "initialize", func(int64) any {
		return map[string]any{
			"protocolVersion": LatestProtocolVersion,
			"capabilities":    caps,
			"clientInfo":      map[string]any{"name": c.opts.Name, "version": c.opts.Version},
		}
	}, callOpts{noCancel: true, timeout: c.opts.InitTimeout})
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	res, err := decodeInitialize(raw)
	if err != nil {
		return nil, err
	}
	ok, known := checkVersion(res.ProtocolVersion)
	if !ok {
		return nil, fmt.Errorf("%w: server answered %q (this client speaks %s down to %s)",
			ErrProtocolVersion, clipForError(res.ProtocolVersion), LatestProtocolVersion, OldestProtocolVersion)
	}
	res.Tolerated = !known
	if v, ok := c.t.(versioned); ok {
		v.SetProtocolVersion(res.ProtocolVersion)
	}
	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		return nil, fmt.Errorf("initialized notification: %w", err)
	}
	c.init.Store(res)
	if l, ok := c.t.(listener); ok {
		l.Listen()
	}
	return res, nil
}

// capable reports whether the server may be asked for a capability. A server
// that declared capabilities but not this one is not asked. A server that
// declared none at all (sloppy, not spec-following) is probed: it would fail
// with method-not-found if it truly had nothing.
func (c *Client) capable(k ListKind) error {
	res := c.init.Load()
	if res == nil {
		return errors.New("mcp: client is not initialized")
	}
	caps := res.Capabilities
	if caps.Empty {
		return nil
	}
	ok := false
	switch k {
	case ListTools:
		ok = caps.Tools != nil
	case ListPrompts:
		ok = caps.Prompts != nil
	case ListResources:
		ok = caps.Resources != nil
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnsupported, k)
	}
	return nil
}

// Ping checks that the server answers.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.request(ctx, "ping", nil, callOpts{})
	return err
}

// listAll pages through a listing under the client's limits. Request errors
// fail the listing (a partial list would make the tool set depend on luck);
// anomalies of a misbehaving server (looping cursors, too many pages or
// items, too many bytes) end it early with a warning, deterministically.
func listAll[T any](c *Client, ctx context.Context, method, key string, decode func(json.RawMessage) (T, bool)) ([]T, []string, error) {
	var out []T
	var warnings []string
	seen := map[string]bool{}
	cursor := ""
	total := 0
	for page := 0; ; page++ {
		if page >= c.opts.MaxListPages {
			warnings = append(warnings, fmt.Sprintf("%s: server has more than %d pages; the rest are ignored", key, c.opts.MaxListPages))
			return out, warnings, nil
		}
		var build func(int64) any
		if cursor != "" {
			cur := cursor
			build = func(int64) any { return map[string]string{"cursor": cur} }
		}
		raw, err := c.request(ctx, method, build, callOpts{})
		if err != nil {
			return nil, warnings, err
		}
		total += len(raw)
		if total > c.opts.MaxListBytes {
			warnings = append(warnings, fmt.Sprintf("%s: listing exceeds %d bytes; the rest is ignored", key, c.opts.MaxListBytes))
			return out, warnings, nil
		}
		items, next, err := pageResult(raw, key)
		if err != nil {
			return nil, warnings, err
		}
		for _, it := range items {
			if len(out) >= c.opts.MaxListItems {
				warnings = append(warnings, fmt.Sprintf("%s: server lists more than %d entries; the rest are ignored", key, c.opts.MaxListItems))
				return out, warnings, nil
			}
			if v, ok := decode(it); ok {
				out = append(out, v)
			}
		}
		if next == "" {
			return out, warnings, nil
		}
		if seen[next] || len(next) > 4096 {
			warnings = append(warnings, fmt.Sprintf("%s: server repeated a pagination cursor; listing stopped", key))
			return out, warnings, nil
		}
		seen[next] = true
		cursor = next
	}
}

// ListTools returns every tool the server offers (paginated). Tools with an
// unusable definition are returned with Problem set.
func (c *Client) ListTools(ctx context.Context) ([]Tool, []string, error) {
	if err := c.capable(ListTools); err != nil {
		return nil, nil, err
	}
	return listAll(c, ctx, "tools/list", "tools", func(raw json.RawMessage) (Tool, bool) { return decodeTool(raw), true })
}

// ListPrompts returns the server's prompts.
func (c *Client) ListPrompts(ctx context.Context) ([]Prompt, []string, error) {
	if err := c.capable(ListPrompts); err != nil {
		return nil, nil, err
	}
	return listAll(c, ctx, "prompts/list", "prompts", func(raw json.RawMessage) (Prompt, bool) { return decodePrompt(raw), true })
}

// ListResources returns the server's resources.
func (c *Client) ListResources(ctx context.Context) ([]Resource, []string, error) {
	if err := c.capable(ListResources); err != nil {
		return nil, nil, err
	}
	return listAll(c, ctx, "resources/list", "resources", decodeResource)
}

// ListResourceTemplates returns the server's resource templates.
func (c *Client) ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, []string, error) {
	if err := c.capable(ListResources); err != nil {
		return nil, nil, err
	}
	return listAll(c, ctx, "resources/templates/list", "resourceTemplates", decodeResourceTemplate)
}

// ReadResource reads one resource.
func (c *Client) ReadResource(ctx context.Context, uri string) (*ReadResourceResult, error) {
	if err := c.capable(ListResources); err != nil {
		return nil, err
	}
	raw, err := c.request(ctx, "resources/read", func(int64) any { return map[string]string{"uri": uri} }, callOpts{})
	if err != nil {
		return nil, err
	}
	return decodeReadResource(raw)
}

// GetPrompt expands a prompt with arguments.
func (c *Client) GetPrompt(ctx context.Context, name string, args map[string]string) (*GetPromptResult, error) {
	if err := c.capable(ListPrompts); err != nil {
		return nil, err
	}
	raw, err := c.request(ctx, "prompts/get", func(int64) any {
		p := map[string]any{"name": name}
		if len(args) > 0 {
			p["arguments"] = args
		}
		return p
	}, callOpts{})
	if err != nil {
		return nil, err
	}
	return decodeGetPrompt(raw)
}

// CallOptions tunes one tools/call.
type CallOptions struct {
	// Timeout is the inactivity timeout: the call fails when nothing (response
	// or progress) arrives for this long. Zero means the client default, a
	// negative value means none (the context still applies).
	Timeout time.Duration
	// MaxTotal caps the whole call however much progress it reports.
	MaxTotal time.Duration
	// OnProgress receives progress notifications, from the client's dispatcher
	// goroutine.
	OnProgress func(Progress)
}

// CallTool invokes a tool. args must be a JSON object (or empty). A tool that
// ran and failed comes back as a result with IsError set; a non-nil error
// means the call itself failed (protocol error, timeout, connection lost).
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage, o CallOptions) (*CallToolResult, error) {
	if err := c.capable(ListTools); err != nil {
		return nil, err
	}
	args = bytes.TrimSpace(args)
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	if args[0] != '{' {
		return nil, errors.New("mcp: tool arguments must be a JSON object")
	}
	raw, err := c.request(ctx, "tools/call", func(id int64) any {
		return map[string]any{
			"name":      name,
			"arguments": args,
			"_meta":     map[string]any{"progressToken": id},
		}
	}, callOpts{timeout: o.Timeout, maxTotal: o.MaxTotal, progress: o.OnProgress})
	if err != nil {
		return nil, err
	}
	return decodeCallToolResult(raw)
}
