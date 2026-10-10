package web

// The event hub: named topics, a replay ring per topic, bounded queues per subscriber, and the
// text/event-stream handler.
//
// Loss policy, per subscriber, applied when its queue is full (by events or bytes):
//
//	Coalescable, with a Key   a newer event with the same Type and Key replaces it in the queue;
//	                          nothing is lost, the older state is simply gone
//	Coalescable               the first to be dropped to make room for anything else
//	ordinary                  dropped after the coalescable ones, when the incoming event is also
//	                          not critical; the subscriber is told with a gap event
//	Critical                  never dropped; if the queue holds nothing that may be dropped, the
//	                          subscriber is cut off with a lagged event and reconnects with
//	                          Last-Event-ID, which replays what it missed from the ring
//
// A gap event ({"reason": ..., "last": id}) means "you may have missed events": the client
// fetches a snapshot of the state and carries on. The hub starts no goroutines; a stream is the
// goroutine of the request that serves it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Errors returned by the hub.
var (
	// ErrClosed is returned by a stream that was closed, or by the hub after Close.
	ErrClosed = errors.New("web: stream closed")
	// ErrLagged is returned by a stream whose queue could not hold a critical event; the
	// subscriber must reconnect with the id of the last event it received.
	ErrLagged = errors.New("web: subscriber lagged behind critical events")
	// ErrTopicClosed is returned by a stream after the topic was closed and the stream drained.
	ErrTopicClosed = errors.New("web: topic closed")
	// ErrTooManyStreams is returned by Subscribe when the hub already serves the most it may.
	ErrTooManyStreams = errors.New("web: too many open streams")
	// ErrTooManyTopics is returned when a new topic would exceed the limit.
	ErrTooManyTopics = errors.New("web: too many topics")
	// ErrOutOfOrder is returned by Publish for an explicit id that is not above the last one.
	ErrOutOfOrder = errors.New("web: event id is not above the topic's last id")
	// ErrBadEvent is returned by Publish for an event or topic that cannot be sent: an invalid
	// topic or type, data that is not JSON, or both flags set.
	ErrBadEvent = errors.New("web: invalid event")
	// ErrEventTooLarge is returned by Publish for data above HubConfig.MaxEventBytes.
	ErrEventTooLarge = errors.New("web: event data too large")
)

// HubConfig bounds the hub. Zero fields take the defaults shown.
type HubConfig struct {
	// ReplayEvents is how many events a topic keeps for Last-Event-ID resume (1024).
	ReplayEvents int
	// ReplayBytes is how many bytes of event data a topic keeps for resume (8 MiB).
	ReplayBytes int
	// Buffer is how many events may wait in one subscriber's queue (256).
	Buffer int
	// BufferBytes is how many bytes may wait in one subscriber's queue (4 MiB).
	BufferBytes int
	// MaxEventBytes is the largest event data Publish accepts (256 KiB). It is reduced to half of
	// BufferBytes if that is smaller, so that a critical event always fits an empty queue.
	MaxEventBytes int
	// MaxTopics bounds the topics (1024).
	MaxTopics int
	// MaxStreams bounds the open streams (32). Browsers keep six connections per origin; a page
	// needs a handful.
	MaxStreams int
	// Heartbeat is the interval of the comment lines that keep an idle stream open (15 s).
	Heartbeat time.Duration
	// WriteTimeout is the deadline for each write to a client, refreshed on every write (10 s). A
	// client that stops reading is cut off when it passes.
	WriteTimeout time.Duration
	// Retry is the reconnection delay the client is asked to wait (3 s).
	Retry time.Duration
}

// fill replaces zero fields by their defaults and keeps the limits consistent.
func (c *HubConfig) fill() {
	def := func(p *int, v int) {
		if *p <= 0 {
			*p = v
		}
	}
	def(&c.ReplayEvents, 1024)
	def(&c.ReplayBytes, 8<<20)
	def(&c.Buffer, 256)
	def(&c.BufferBytes, 4<<20)
	def(&c.MaxEventBytes, 256<<10)
	def(&c.MaxTopics, 1024)
	def(&c.MaxStreams, 32)
	if c.Heartbeat <= 0 {
		c.Heartbeat = 15 * time.Second
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 10 * time.Second
	}
	if c.Retry <= 0 {
		c.Retry = 3 * time.Second
	}
	if c.MaxEventBytes > c.BufferBytes/2 {
		c.MaxEventBytes = c.BufferBytes / 2
	}
	if c.MaxEventBytes > c.ReplayBytes {
		c.MaxEventBytes = c.ReplayBytes
	}
}

// Event is one message of a topic.
type Event struct {
	// ID orders the events of a topic. Zero lets the hub assign the next one; a non-zero id must
	// be above the topic's last (a producer that has its own sequence, such as a log's, can use
	// it). Ids are not required to be contiguous.
	ID uint64
	// Type is the SSE event name: lower-case letters, digits, dots, dashes and underscores, up to
	// 64 characters, starting with a letter. The hub itself sends gap, lagged and closed.
	Type string
	// Data is the JSON payload. Empty means {}. It is validated, compacted and HTML-escaped by
	// Publish, and must not be modified afterwards.
	Data json.RawMessage
	// Critical events are never dropped for a slow subscriber; one that cannot keep up with
	// them is disconnected with a lagged event.
	Critical bool
	// Coalescable events may be dropped, or replaced by a newer event with the same Type and
	// Key, for a slow subscriber. A Coalescable event must not also be Critical.
	Coalescable bool
	// Key names the value a Coalescable event sets (for example "stream/mgr"): a newer event with
	// the same Type and Key makes an older one still in a subscriber's queue redundant.
	Key string
}

// tier orders events by how readily they are given up: 0 coalescable, 1 ordinary, 2 critical.
func (e Event) tier() int {
	switch {
	case e.Coalescable:
		return 0
	case e.Critical:
		return 2
	}
	return 1
}

// size is what an event counts against the byte bounds.
func (e Event) size() int { return len(e.Data) + len(e.Type) + len(e.Key) + 48 }

var (
	topicRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	typeRE  = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
)

// evq is a queue of events with removal from the middle, used for rings and subscriber queues.
type evq struct {
	evs   []Event
	head  int
	bytes int
}

// len returns the number of queued events.
func (q *evq) len() int { return len(q.evs) - q.head }

// push appends an event.
func (q *evq) push(e Event) {
	q.evs = append(q.evs, e)
	q.bytes += e.size()
}

// pop removes and returns the oldest event.
func (q *evq) pop() (Event, bool) {
	if q.head >= len(q.evs) {
		return Event{}, false
	}
	e := q.evs[q.head]
	q.evs[q.head] = Event{}
	q.head++
	q.bytes -= e.size()
	if q.head == len(q.evs) {
		q.evs, q.head = q.evs[:0], 0
	} else if q.head >= 32 && q.head*2 >= len(q.evs) {
		q.evs = append([]Event(nil), q.evs[q.head:]...)
		q.head = 0
	}
	return e, true
}

// removeFirst removes the oldest event for which match is true.
func (q *evq) removeFirst(match func(Event) bool) bool {
	for i := q.head; i < len(q.evs); i++ {
		if match(q.evs[i]) {
			q.bytes -= q.evs[i].size()
			copy(q.evs[i:], q.evs[i+1:])
			q.evs[len(q.evs)-1] = Event{}
			q.evs = q.evs[:len(q.evs)-1]
			return true
		}
	}
	return false
}

// removeAll removes every event for which match is true and returns how many.
func (q *evq) removeAll(match func(Event) bool) int {
	n := 0
	kept := q.evs[:q.head]
	for _, e := range q.evs[q.head:] {
		if match(e) {
			q.bytes -= e.size()
			n++
			continue
		}
		kept = append(kept, e)
	}
	for i := len(kept); i < len(q.evs); i++ {
		q.evs[i] = Event{}
	}
	q.evs = kept
	return n
}

// since returns a copy of the events with an id above id.
func (q *evq) since(id uint64) []Event {
	var out []Event
	for _, e := range q.evs[q.head:] {
		if e.ID > id {
			out = append(out, e)
		}
	}
	return out
}

// topic is a named sequence of events and its subscribers.
type topic struct {
	name    string
	ring    evq
	last    uint64
	evicted uint64 // the highest id that has left the ring
	subs    map[*Stream]struct{}
}

// Hub fans events out to the subscribers of topics. Its methods are safe for concurrent use.
type Hub struct {
	cfg      HubConfig
	mu       sync.Mutex
	topics   map[string]*topic
	streams  int
	closed   bool
	draining bool // Drain runs: no new subscriber, and the streams end once they have delivered what they hold
	done     chan struct{}
}

// NewHub creates a hub. Server.Hub is the one a server uses; a hub of its own is for tests and
// for code that streams without a server.
func NewHub(cfg HubConfig) *Hub {
	cfg.fill()
	return &Hub{cfg: cfg, topics: map[string]*topic{}, done: make(chan struct{})}
}

// compactEvent validates an event and returns it with its data compacted to one line.
func (h *Hub) compactEvent(name string, ev Event) (Event, error) {
	if !topicRE.MatchString(name) || !typeRE.MatchString(ev.Type) || len(ev.Key) > 128 || (ev.Critical && ev.Coalescable) {
		return ev, ErrBadEvent
	}
	data := ev.Data
	if len(data) == 0 {
		data = json.RawMessage("{}")
	}
	if len(data) > h.cfg.MaxEventBytes {
		return ev, ErrEventTooLarge
	}
	// Marshal of a RawMessage validates it and writes it compact, with <, > and & and the
	// line separators escaped: the result is one line, which is what the SSE framing needs.
	b, err := json.Marshal(data)
	if err != nil {
		return ev, ErrBadEvent
	}
	ev.Data = b
	if len(ev.Data) > h.cfg.MaxEventBytes {
		return ev, ErrEventTooLarge
	}
	return ev, nil
}

// Publish adds an event to a topic and offers it to the topic's subscribers. It returns the id
// the event has. A topic is created by its first event or subscriber. An event with ID 0 gets the
// next id of the topic. Publish never blocks on a subscriber.
func (h *Hub) Publish(name string, ev Event) (uint64, error) {
	ev, err := h.compactEvent(name, ev)
	if err != nil {
		return 0, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, ErrClosed
	}
	t, err := h.topicLocked(name)
	if err != nil {
		return 0, err
	}
	switch {
	case ev.ID == 0:
		ev.ID = t.last + 1
	case ev.ID <= t.last:
		return 0, ErrOutOfOrder
	}
	t.last = ev.ID
	t.ring.push(ev)
	for t.ring.len() > h.cfg.ReplayEvents || (t.ring.bytes > h.cfg.ReplayBytes && t.ring.len() > 1) {
		old, _ := t.ring.pop()
		t.evicted = old.ID
	}
	for s := range t.subs {
		s.enqueueLocked(ev)
	}
	return ev.ID, nil
}

// topicLocked returns the topic of a name, creating it within the limit.
func (h *Hub) topicLocked(name string) (*topic, error) {
	if t := h.topics[name]; t != nil {
		return t, nil
	}
	if len(h.topics) >= h.cfg.MaxTopics {
		return nil, ErrTooManyTopics
	}
	t := &topic{name: name, subs: map[*Stream]struct{}{}}
	h.topics[name] = t
	return t, nil
}

// LastID returns the id of the last event published to a topic, or 0. A handler that serves a
// snapshot reads it before building the snapshot, and the client resumes the stream from it.
func (h *Hub) LastID(name string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.topics[name]; t != nil {
		return t.last
	}
	return 0
}

// CloseTopic ends a topic: its subscribers receive what is already queued and then
// ErrTopicClosed (a closed event on the wire), and its replay ring is freed. Publishing to the
// name afterwards starts a new topic.
func (h *Hub) CloseTopic(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.topics[name]
	if t == nil {
		return
	}
	delete(h.topics, name)
	for s := range t.subs {
		s.draining = true
		s.wake()
	}
	t.subs = nil
}

// Close ends every stream and refuses new events and subscribers. It is idempotent.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	close(h.done)
	for _, t := range h.topics {
		for s := range t.subs {
			s.closed = true
			s.wake()
		}
	}
}

// Drain ends the streams gracefully, for a shutdown: it takes no new subscriber, every open stream delivers the events it has
// already queued (a last event published just before, a bye, included) and then ends, and the streams still open when ctx ends
// are closed as Close closes them. Events published while it drains reach no stream. The hub is closed when it returns.
func (h *Hub) Drain(ctx context.Context) {
	h.mu.Lock()
	if h.closed || h.draining {
		h.mu.Unlock()
		h.Close()
		return
	}
	h.draining = true
	for _, t := range h.topics {
		for s := range t.subs {
			s.draining = true
			s.wake()
		}
	}
	h.mu.Unlock()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for h.Streams() > 0 {
		select {
		case <-ctx.Done():
			h.Close()
			return
		case <-tick.C:
		}
	}
	h.Close()
}

// Streams returns the number of open streams.
func (h *Hub) Streams() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.streams
}

// SubscribeOpts says where a subscription starts.
type SubscribeOpts struct {
	// Resume replays the retained events with an id above After before the live ones. If After is
	// older than the oldest retained event, or ahead of the topic's last id (the server restarted),
	// the stream starts with a gap event instead. Without Resume the stream is live only.
	Resume bool
	After  uint64
}

// Stream is one subscription. Next is called by one goroutine; Close may be called by any.
type Stream struct {
	h *Hub
	t *topic

	replay    []Event
	rpos      int
	q         evq
	gap       *Event
	dropped   int
	lagged    bool
	closed    bool
	draining  bool
	counted   bool
	delivered uint64
	notify    chan struct{}
}

// Subscribe opens a stream on a topic. It fails with ErrTooManyStreams, ErrTooManyTopics or
// ErrClosed, or ErrBadEvent for an invalid topic name.
func (h *Hub) Subscribe(name string, o SubscribeOpts) (*Stream, error) {
	if !topicRE.MatchString(name) {
		return nil, ErrBadEvent
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.draining {
		return nil, ErrClosed
	}
	if h.streams >= h.cfg.MaxStreams {
		return nil, ErrTooManyStreams
	}
	t, err := h.topicLocked(name)
	if err != nil {
		return nil, err
	}
	s := &Stream{h: h, t: t, counted: true, notify: make(chan struct{}, 1)}
	if o.Resume {
		switch {
		case o.After > t.last:
			s.gap = gapEvent("ahead", 0, t.last)
		case o.After < t.evicted:
			s.gap = gapEvent("aged", 0, t.last)
		default:
			s.replay = t.ring.since(o.After)
		}
	}
	t.subs[s] = struct{}{}
	h.streams++
	return s, nil
}

// gapEvent builds the notice that the client may have missed events.
func gapEvent(reason string, dropped int, last uint64) *Event {
	data := fmt.Sprintf(`{"reason":%q,"dropped":%d,"last":%d}`, reason, dropped, last)
	return &Event{Type: "gap", Data: json.RawMessage(data)}
}

// wake signals the goroutine waiting in Next.
func (s *Stream) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// unlinkLocked stops the topic from offering events to the stream.
func (s *Stream) unlinkLocked() {
	if s.t != nil && s.t.subs != nil {
		delete(s.t.subs, s)
	}
}

// lagLocked cuts the subscriber off: its queue is dropped and it will report ErrLagged.
func (s *Stream) lagLocked() {
	s.lagged = true
	s.q = evq{}
	s.replay = nil
	s.unlinkLocked()
	s.wake()
}

// fullLocked reports whether an event of size sz does not fit the queue.
func (s *Stream) fullLocked(sz int) bool {
	return s.q.len() >= s.h.cfg.Buffer || s.q.bytes+sz > s.h.cfg.BufferBytes
}

// enqueueLocked offers an event to the subscriber under the loss policy.
func (s *Stream) enqueueLocked(ev Event) {
	if s.closed || s.lagged || s.draining {
		return
	}
	if ev.Coalescable && ev.Key != "" {
		s.q.removeAll(func(o Event) bool { return o.Coalescable && o.Type == ev.Type && o.Key == ev.Key })
	}
	sz := ev.size()
	for s.fullLocked(sz) {
		if s.q.removeFirst(func(o Event) bool { return o.tier() == 0 }) {
			s.dropped++
			continue
		}
		if ev.tier() == 2 {
			if s.q.removeFirst(func(o Event) bool { return o.tier() == 1 }) {
				s.dropped++
				continue
			}
			s.lagLocked()
			return
		}
		s.dropped++ // nothing queued may be given up for this one: it is the one that is lost
		s.wake()
		return
	}
	s.q.push(ev)
	s.wake()
}

// poll takes the next event without waiting. ok is false when there is none yet; err is set
// when the stream has ended.
func (s *Stream) poll() (ev Event, ok bool, err error) {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	switch {
	case s.closed:
		return Event{}, false, ErrClosed
	case s.lagged:
		return Event{}, false, ErrLagged
	case s.gap != nil:
		ev, s.gap = *s.gap, nil
		return ev, true, nil
	case s.dropped > 0:
		ev, s.dropped = *gapEvent("overflow", s.dropped, s.t.last), 0
		return ev, true, nil
	case s.rpos < len(s.replay):
		ev = s.replay[s.rpos]
		s.replay[s.rpos] = Event{}
		s.rpos++
		s.delivered = ev.ID
		return ev, true, nil
	}
	if ev, ok = s.q.pop(); ok {
		s.delivered = ev.ID
		return ev, true, nil
	}
	switch {
	case s.draining && s.h.draining:
		return Event{}, false, ErrClosed // the server shuts down: what was queued is delivered, and the stream ends without more
	case s.draining:
		return Event{}, false, ErrTopicClosed
	}
	return Event{}, false, nil
}

// Next returns the next event, waiting for one. Synthetic events (gap) have ID 0. It returns
// ErrLagged, ErrTopicClosed or ErrClosed when the stream has ended, or ctx's error.
func (s *Stream) Next(ctx context.Context) (Event, error) {
	for {
		ev, ok, err := s.poll()
		if ok || err != nil {
			return ev, err
		}
		select {
		case <-s.notify:
		case <-ctx.Done():
			return Event{}, ctx.Err()
		}
	}
}

// Delivered returns the id of the last real event handed out, which is what a client that
// reconnects after ErrLagged should resume from.
func (s *Stream) Delivered() uint64 {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	return s.delivered
}

// Close ends the subscription, frees its queue and releases its place among the hub's open
// streams. It is idempotent.
func (s *Stream) Close() {
	h := s.h
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.counted {
		s.counted = false
		h.streams--
	}
	if s.closed {
		return
	}
	s.closed = true
	s.q = evq{}
	s.replay = nil
	s.unlinkLocked()
	if t := s.t; len(t.subs) == 0 && t.last == 0 && h.topics[t.name] == t {
		delete(h.topics, t.name) // nothing was ever published here: do not keep the name
	}
	s.wake()
}

// ---- the HTTP side --------------------------------------------------------------------------

// sseHandler serves a topic as text/event-stream.
type sseHandler struct {
	h     *Hub
	topic func(*http.Request) (string, bool)
}

// isStream marks the handler as a stream for Server.Handle.
func (*sseHandler) isStream() {}

// ServeSSE returns the handler of an event stream. topic maps a request to the topic to stream
// (from the path, the query, the session it names) and reports false for a request that names no
// stream the caller may read, which is answered 404. Register it with Server.Handle on a GET
// pattern; the envelope has already authenticated the request. A client resumes with the
// Last-Event-ID header (which EventSource sends by itself when it reconnects), or the "after"
// query parameter for its first connection.
func (h *Hub) ServeSSE(topic func(*http.Request) (string, bool)) http.Handler {
	return &sseHandler{h: h, topic: topic}
}

// canFlush reports whether w, or a writer it wraps, can be flushed.
func canFlush(w http.ResponseWriter) bool {
	for {
		if _, ok := w.(http.Flusher); ok {
			return true
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = u.Unwrap()
	}
}

// lastEventID reads where the client wants to resume from.
func lastEventID(r *http.Request) (id uint64, resume bool, err error) {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("after")
	}
	if v == "" {
		return 0, false, nil
	}
	id, err = strconv.ParseUint(v, 10, 64)
	return id, err == nil, err
}

// ServeHTTP streams events until the client goes away, the credential behind the request ends,
// the subscriber lags, the topic closes or the hub shuts down.
func (sh *sseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := sh.h
	name, ok := sh.topic(r)
	if !ok {
		Error(w, http.StatusNotFound, "not_found", "no such stream")
		return
	}
	after, resume, err := lastEventID(r)
	if err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "Last-Event-ID must be an event id")
		return
	}
	if r.Method == http.MethodHead { // the headers of a stream, without opening one
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		return
	}
	if !canFlush(w) {
		Error(w, http.StatusInternalServerError, "internal", "this connection cannot stream")
		return
	}
	st, err := h.Subscribe(name, SubscribeOpts{Resume: resume, After: after})
	switch {
	case errors.Is(err, ErrTooManyStreams), errors.Is(err, ErrTooManyTopics):
		w.Header().Set("Retry-After", "5")
		Error(w, http.StatusServiceUnavailable, "too_many_streams", "too many event streams are open")
		return
	case errors.Is(err, ErrClosed):
		Error(w, http.StatusServiceUnavailable, "shutting_down", "the server is shutting down")
		return
	case err != nil:
		Error(w, http.StatusNotFound, "not_found", "no such stream")
		return
	}
	defer st.Close()

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{}) // the server's ReadTimeout is for requests, not for a stream
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if h.write(w, rc, []byte("retry: "+strconv.FormatInt(h.cfg.Retry.Milliseconds(), 10)+"\n\n")) != nil {
		return
	}

	var revoked <-chan struct{}
	valid := func() bool { return true }
	if info := infoFrom(r.Context()); info != nil && info.p != nil {
		revoked, valid = info.p.done, info.p.valid
	}
	tick := time.NewTicker(h.cfg.Heartbeat)
	defer tick.Stop()
	var buf []byte
	for {
		ev, ok, err := st.poll()
		if err != nil {
			switch {
			case errors.Is(err, ErrLagged):
				_ = h.write(w, rc, []byte(`event: lagged`+"\n"+`data: {"after":`+strconv.FormatUint(st.Delivered(), 10)+"}\n\n"))
			case errors.Is(err, ErrTopicClosed):
				_ = h.write(w, rc, []byte("event: closed\ndata: {}\n\n"))
			}
			return
		}
		if ok {
			buf = appendEvent(buf[:0], ev)
			if h.write(w, rc, buf) != nil {
				return
			}
			continue
		}
		select {
		case <-st.notify:
		case <-tick.C:
			if !valid() || h.write(w, rc, []byte(": ping\n\n")) != nil {
				return
			}
		case <-r.Context().Done():
			return
		case <-revoked:
			return
		case <-h.done:
			return
		}
	}
}

// appendEvent writes the SSE framing of an event. Data is one line (Publish compacts it) and Type
// is restricted to safe characters, so nothing in an event can end its frame early.
func appendEvent(dst []byte, ev Event) []byte {
	if ev.ID != 0 {
		dst = append(dst, "id: "...)
		dst = strconv.AppendUint(dst, ev.ID, 10)
		dst = append(dst, '\n')
	}
	dst = append(dst, "event: "...)
	dst = append(dst, ev.Type...)
	dst = append(dst, "\ndata: "...)
	dst = append(dst, ev.Data...)
	return append(dst, '\n', '\n')
}

// write sends b and flushes, under a write deadline that starts now.
func (h *Hub) write(w http.ResponseWriter, rc *http.ResponseController, b []byte) error {
	_ = rc.SetWriteDeadline(time.Now().Add(h.cfg.WriteTimeout))
	if _, err := w.Write(b); err != nil {
		return err
	}
	return rc.Flush()
}
