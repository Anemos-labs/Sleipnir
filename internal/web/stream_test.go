package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// guard is the most a test waits for something that has to happen: a hang guard, not a timing.
const guard = 20 * time.Second

func ev(typ string, data string) Event { return Event{Type: typ, Data: json.RawMessage(data)} }

func crit(typ string) Event { return Event{Type: typ, Critical: true} }

func coal(typ, key string) Event { return Event{Type: typ, Coalescable: true, Key: key} }

// drain reads every event that is available without waiting.
func drain(t testing.TB, s *Stream) ([]Event, error) {
	t.Helper()
	var out []Event
	for {
		e, ok, err := s.poll()
		if err != nil {
			return out, err
		}
		if !ok {
			return out, nil
		}
		out = append(out, e)
	}
}

func ids(evs []Event) []uint64 {
	var out []uint64
	for _, e := range evs {
		if e.ID != 0 {
			out = append(out, e.ID)
		}
	}
	return out
}

func mustPublish(t testing.TB, h *Hub, topic string, e Event) uint64 {
	t.Helper()
	id, err := h.Publish(topic, e)
	if err != nil {
		t.Fatalf("Publish(%s, %+v): %v", topic, e, err)
	}
	return id
}

func mustSubscribe(t testing.TB, h *Hub, topic string, o SubscribeOpts) *Stream {
	t.Helper()
	s, err := h.Subscribe(topic, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestPublishAssignsIDsAndKeepsThemOrdered(t *testing.T) {
	h := NewHub(HubConfig{})
	if id := mustPublish(t, h, "global", ev("a", `{}`)); id != 1 {
		t.Errorf("first id = %d", id)
	}
	if id := mustPublish(t, h, "global", ev("a", `{}`)); id != 2 {
		t.Errorf("second id = %d", id)
	}
	if id := mustPublish(t, h, "other", ev("a", `{}`)); id != 1 {
		t.Errorf("topics count apart: %d", id)
	}
	// A producer with a sequence of its own may use it, with gaps; it must grow.
	e := ev("a", `{}`)
	e.ID = 40
	if id := mustPublish(t, h, "global", e); id != 40 {
		t.Errorf("explicit id = %d", id)
	}
	if id := mustPublish(t, h, "global", ev("a", `{}`)); id != 41 {
		t.Errorf("after an explicit id = %d", id)
	}
	for _, bad := range []uint64{1, 40, 41} {
		e.ID = bad
		if _, err := h.Publish("global", e); !errors.Is(err, ErrOutOfOrder) {
			t.Errorf("id %d: %v, want ErrOutOfOrder", bad, err)
		}
	}
	if got := h.LastID("global"); got != 41 {
		t.Errorf("LastID = %d", got)
	}
	if got := h.LastID("nothing"); got != 0 {
		t.Errorf("LastID of an unknown topic = %d", got)
	}
}

func TestPublishValidatesWhatBecomesWireFormat(t *testing.T) {
	h := NewHub(HubConfig{MaxEventBytes: 1024})
	for _, topic := range []string{"", " ", "a b", "a\nb", "-lead", strings.Repeat("a", 129), "é", "a;b", "a?b"} {
		if _, err := h.Publish(topic, ev("x", `{}`)); !errors.Is(err, ErrBadEvent) {
			t.Errorf("topic %q: %v", topic, err)
		}
	}
	for _, topic := range []string{"global", "session/20261009-120000-abcdef", "a.b-c_d/e", "A"} {
		if _, err := h.Publish(topic, ev("x", `{}`)); err != nil {
			t.Errorf("topic %q: %v", topic, err)
		}
	}
	for _, typ := range []string{"", "X", "has space", "new\nline", "a:b", "1a", "gap\r", strings.Repeat("a", 65), "é", "a,b"} {
		if _, err := h.Publish("t", ev(typ, `{}`)); !errors.Is(err, ErrBadEvent) {
			t.Errorf("type %q: %v", typ, err)
		}
	}
	for _, data := range []string{`{`, `nope`, `{"a":}`, `{} {}`, "{\"a\":1}\n\ndata: x", `"\`} {
		if _, err := h.Publish("t", ev("x", data)); !errors.Is(err, ErrBadEvent) {
			t.Errorf("data %q: %v", data, err)
		}
	}
	if _, err := h.Publish("t", Event{Type: "x", Critical: true, Coalescable: true}); !errors.Is(err, ErrBadEvent) {
		t.Errorf("both flags: %v", err)
	}
	if _, err := h.Publish("t", Event{Type: "x", Key: strings.Repeat("k", 129)}); !errors.Is(err, ErrBadEvent) {
		t.Errorf("long key: %v", err)
	}
	if _, err := h.Publish("t", ev("x", `"`+strings.Repeat("a", 1100)+`"`)); !errors.Is(err, ErrEventTooLarge) {
		t.Errorf("large: %v", err)
	}
	// Data is made one line and inert: this is what stops an event from ending its frame early.
	s := mustSubscribe(t, h, "c", SubscribeOpts{})
	mustPublish(t, h, "c", ev("x", "{\n  \"a\": \"<script>& \",\n  \"b\": [1,\n2]\n}"))
	mustPublish(t, h, "c", Event{Type: "empty"})
	got, _ := drain(t, s)
	if len(got) != 2 {
		t.Fatalf("got %d events", len(got))
	}
	frame := string(appendEvent(nil, got[0]))
	if strings.Count(frame, "\n") != 4 || strings.ContainsAny(frame[:len(frame)-2], "\r") || strings.ContainsAny(string(got[0].Data), "<>& \n") {
		t.Errorf("frame is not one inert block: %q", frame)
	}
	if want := `{"a":"` + uesc("003c") + `script` + uesc("003e") + uesc("0026") + uesc("2028") + `","b":[1,2]}`; string(got[0].Data) != want {
		t.Errorf("data = %s", got[0].Data)
	}
	if string(got[1].Data) != `{}` {
		t.Errorf("empty data = %s", got[1].Data)
	}
	// Data handed to Publish is not shared with the caller.
	data := json.RawMessage(`{"a":1}`)
	mustPublish(t, h, "c", Event{Type: "x", Data: data})
	data[2] = 'Z'
	if e, _ := drain(t, s); string(e[0].Data) != `{"a":1}` {
		t.Errorf("a published event changed under the publisher: %s", e[0].Data)
	}
}

func TestSubscribersSeeLiveEventsOnlyUnlessTheyResume(t *testing.T) {
	h := NewHub(HubConfig{})
	for i := 0; i < 3; i++ {
		mustPublish(t, h, "t", ev("a", `{}`))
	}
	live := mustSubscribe(t, h, "t", SubscribeOpts{})
	resumed := mustSubscribe(t, h, "t", SubscribeOpts{Resume: true, After: 1})
	all := mustSubscribe(t, h, "t", SubscribeOpts{Resume: true, After: 0})
	mustPublish(t, h, "t", ev("a", `{}`))
	for name, c := range map[string]struct {
		s    *Stream
		want []uint64
	}{"live": {live, []uint64{4}}, "resumed after 1": {resumed, []uint64{2, 3, 4}}, "resumed from the start": {all, []uint64{1, 2, 3, 4}}} {
		got, err := drain(t, c.s)
		if err != nil || fmt.Sprint(ids(got)) != fmt.Sprint(c.want) {
			t.Errorf("%s: %v %v, want %v", name, ids(got), err, c.want)
		}
		for _, e := range got {
			if e.Type == "gap" {
				t.Errorf("%s: unexpected gap", name)
			}
		}
	}
}

func TestResumeReportsAGapWhenTheIDHasAgedOut(t *testing.T) {
	// The ring holds 6..10; ids up to 5 have gone.
	filled := func() *Hub {
		h := NewHub(HubConfig{ReplayEvents: 5})
		for i := 0; i < 10; i++ {
			mustPublish(t, h, "t", ev("a", `{}`))
		}
		return h
	}
	for _, c := range []struct {
		after   uint64
		want    []uint64
		wantGap string
	}{
		{after: 10, want: nil},
		{after: 9, want: []uint64{10}},
		{after: 5, want: []uint64{6, 7, 8, 9, 10}}, // the client has 5: nothing it needs is missing
		{after: 4, wantGap: "aged"},
		{after: 0, wantGap: "aged"},
		{after: 11, wantGap: "ahead"}, // an id from before a restart
		{after: 1 << 40, wantGap: "ahead"},
	} {
		h := filled()
		s := mustSubscribe(t, h, "t", SubscribeOpts{Resume: true, After: c.after})
		got, err := drain(t, s)
		if err != nil {
			t.Fatal(err)
		}
		if c.wantGap == "" {
			if fmt.Sprint(ids(got)) != fmt.Sprint(c.want) || len(got) != len(ids(got)) {
				t.Errorf("after %d: %v (%d events), want %v", c.after, ids(got), len(got), c.want)
			}
			continue
		}
		if len(got) != 1 || got[0].Type != "gap" || got[0].ID != 0 {
			t.Fatalf("after %d: %+v, want a single gap", c.after, got)
		}
		var g struct {
			Reason string
			Last   uint64
		}
		if err := json.Unmarshal(got[0].Data, &g); err != nil || g.Reason != c.wantGap || g.Last != 10 {
			t.Errorf("after %d: gap = %s (%v), want reason %s last 10", c.after, got[0].Data, err, c.wantGap)
		}
		// After a gap the stream carries on with live events.
		mustPublish(t, h, "t", ev("a", `{}`))
		if more, _ := drain(t, s); fmt.Sprint(ids(more)) != "[11]" {
			t.Errorf("after the gap: %v", ids(more))
		}
	}
	// The ring is also bounded by bytes.
	hb := NewHub(HubConfig{ReplayBytes: 1000, MaxEventBytes: 300})
	for i := 0; i < 20; i++ {
		mustPublish(t, hb, "t", ev("a", `"`+strings.Repeat("x", 100)+`"`))
	}
	s := mustSubscribe(t, hb, "t", SubscribeOpts{Resume: true, After: 0})
	if got, _ := drain(t, s); len(got) != 1 || got[0].Type != "gap" {
		t.Errorf("a ring bounded by bytes must report a gap from the start: %v", got)
	}
	s2 := mustSubscribe(t, hb, "t", SubscribeOpts{Resume: true, After: 19})
	if got, _ := drain(t, s2); fmt.Sprint(ids(got)) != "[20]" {
		t.Errorf("resume from the last but one in a byte-bounded ring: %v", got)
	}
	// A topic nobody wrote to resumes from 0 without a gap, and 5 is from the future.
	hn := NewHub(HubConfig{})
	s3 := mustSubscribe(t, hn, "fresh", SubscribeOpts{Resume: true, After: 0})
	if got, _ := drain(t, s3); len(got) != 0 {
		t.Errorf("a fresh topic: %v", got)
	}
	s4 := mustSubscribe(t, hn, "fresh2", SubscribeOpts{Resume: true, After: 5})
	if got, _ := drain(t, s4); len(got) != 1 || got[0].Type != "gap" {
		t.Errorf("a client from before a restart must be told: %v", got)
	}
}

func TestResumeDoesNotLoseOrRepeatEventsPublishedDuringSubscribe(t *testing.T) {
	const n = 3000
	h := NewHub(HubConfig{ReplayEvents: n + 10, Buffer: n + 10, BufferBytes: 32 << 20})
	var wg sync.WaitGroup
	wg.Add(1)
	start := make(chan struct{})
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < n; i++ {
			if _, err := h.Publish("t", ev("a", `{}`)); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	close(start)
	var s *Stream
	for h.LastID("t") < 100 { // subscribe while the publisher is busy
	}
	s = mustSubscribe(t, h, "t", SubscribeOpts{Resume: true, After: 0})
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), guard)
	defer cancel()
	var last uint64
	for last < n {
		e, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("after %d: %v", last, err)
		}
		if e.ID != last+1 {
			t.Fatalf("got id %d after %d: events were lost or repeated across the boundary between replay and live", e.ID, last)
		}
		last = e.ID
	}
}

func TestCoalescableEventsAreSupersededByKey(t *testing.T) {
	h := NewHub(HubConfig{Buffer: 4})
	s := mustSubscribe(t, h, "t", SubscribeOpts{})
	for i := 0; i < 100; i++ {
		e := coal("stream", "mgr")
		e.Data = json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))
		mustPublish(t, h, "t", e)
	}
	e := coal("stream", "w1")
	e.Data = json.RawMessage(`{"n":"w1"}`)
	mustPublish(t, h, "t", e)
	mustPublish(t, h, "t", crit("state"))
	got, err := drain(t, s)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing was lost: the older states of a value are simply no longer worth sending, so no gap is reported.
	if fmt.Sprint(ids(got)) != "[100 101 102]" || len(got) != 3 || string(got[0].Data) != `{"n":99}` {
		t.Errorf("queue = %+v", got)
	}
	// The same key under another type is another value; an event without a key is never merged.
	h2 := NewHub(HubConfig{Buffer: 10})
	s2 := mustSubscribe(t, h2, "t", SubscribeOpts{})
	for _, e := range []Event{coal("a", "k"), coal("b", "k"), coal("a", "k"), coal("a", ""), coal("a", ""), {Type: "a", Key: "k"}, {Type: "a", Key: "k"}} {
		mustPublish(t, h2, "t", e)
	}
	got, _ = drain(t, s2)
	if fmt.Sprint(ids(got)) != "[2 3 4 5 6 7]" {
		t.Errorf("ids = %v", ids(got))
	}
	// A new subscriber's queue is its own.
	if got, _ := drain(t, mustSubscribe(t, h2, "t", SubscribeOpts{})); len(got) != 0 {
		t.Error("a fresh subscriber saw old events")
	}
}

func TestSlowSubscribersLoseCoalescableEventsFirstAndAreToldOfAnyLoss(t *testing.T) {
	h := NewHub(HubConfig{Buffer: 4})
	s := mustSubscribe(t, h, "t", SubscribeOpts{})
	mustPublish(t, h, "t", crit("q1"))
	mustPublish(t, h, "t", crit("q2"))
	for i := 0; i < 50; i++ {
		mustPublish(t, h, "t", coal("tick", ""))
	}
	mustPublish(t, h, "t", crit("q3"))
	mustPublish(t, h, "t", crit("q4"))
	got, err := drain(t, s)
	if err != nil {
		t.Fatal(err)
	}
	// The loss is announced first, with how much; every critical event is there, in order; the newest ticks that fit are kept.
	if got[0].Type != "gap" || got[0].ID != 0 {
		t.Fatalf("first = %+v: a subscriber that lost events must be told", got[0])
	}
	var g struct {
		Reason  string
		Dropped int
	}
	_ = json.Unmarshal(got[0].Data, &g)
	if g.Reason != "overflow" || g.Dropped == 0 {
		t.Errorf("gap = %s", got[0].Data)
	}
	var types []string
	for _, e := range got[1:] {
		types = append(types, e.Type)
	}
	if fmt.Sprint(types) != "[q1 q2 q3 q4]" {
		t.Errorf("delivered %v: the critical events must all be there, and the ticks give way to them", types)
	}
	if _, err := drain(t, s); err != nil {
		t.Errorf("a subscriber whose losses were only coalescable is not cut off: %v", err)
	}
}

func TestOrdinaryEventsGiveWayToCriticalOnes(t *testing.T) {
	h := NewHub(HubConfig{Buffer: 2})
	s := mustSubscribe(t, h, "t", SubscribeOpts{})
	mustPublish(t, h, "t", ev("o1", `{}`))
	mustPublish(t, h, "t", ev("o2", `{}`))
	mustPublish(t, h, "t", ev("o3", `{}`)) // nothing queued may be given up for an ordinary event: it is the one lost
	mustPublish(t, h, "t", crit("q"))      // a critical event displaces the oldest ordinary one
	got, err := drain(t, s)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range got {
		types = append(types, e.Type)
	}
	if fmt.Sprint(types) != "[gap o2 q]" {
		t.Errorf("delivered %v, want [gap o2 q]", types)
	}
}

func TestASubscriberThatCannotHoldACriticalEventIsDisconnected(t *testing.T) {
	h := NewHub(HubConfig{Buffer: 2})
	s := mustSubscribe(t, h, "t", SubscribeOpts{})
	mustPublish(t, h, "t", crit("q1"))
	first, ok, _ := s.poll()
	if !ok || first.ID != 1 {
		t.Fatalf("first = %+v", first)
	}
	mustPublish(t, h, "t", crit("q2"))
	mustPublish(t, h, "t", crit("q3"))
	mustPublish(t, h, "t", crit("q4")) // does not fit, and nothing may be dropped for it
	if _, err := s.Next(context.Background()); !errors.Is(err, ErrLagged) {
		t.Fatalf("err = %v, want ErrLagged", err)
	}
	if got := s.Delivered(); got != 1 {
		t.Errorf("Delivered = %d: a client that reconnects resumes from the last event it was handed", got)
	}
	// Once cut off it receives nothing more, and later events cost it nothing.
	mustPublish(t, h, "t", crit("q5"))
	if _, err := s.Next(context.Background()); !errors.Is(err, ErrLagged) {
		t.Errorf("err = %v", err)
	}
	// The events it missed are in the ring for its reconnection.
	r := mustSubscribe(t, h, "t", SubscribeOpts{Resume: true, After: s.Delivered()})
	got, _ := drain(t, r)
	if fmt.Sprint(ids(got)) != "[2 3 4 5]" {
		t.Errorf("resumed after the lag: %v", ids(got))
	}
	// A critical event too big for the byte budget cannot be queued either.
	hb := NewHub(HubConfig{BufferBytes: 400, MaxEventBytes: 100})
	sb := mustSubscribe(t, hb, "t", SubscribeOpts{})
	for i := 0; i < 20; i++ {
		e := crit("big")
		e.Data = json.RawMessage(`"` + strings.Repeat("x", 80) + `"`)
		mustPublish(t, hb, "t", e)
	}
	if _, err := drain(t, sb); !errors.Is(err, ErrLagged) {
		t.Errorf("byte budget: %v", err)
	}
}

// Whatever the mix of events and however slowly a client reads, a client that is not cut off has every critical event, in order.
func TestCriticalEventsAreNeverDropped(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 300; trial++ {
		h := NewHub(HubConfig{Buffer: 1 + rng.Intn(12), ReplayEvents: 5000})
		s := mustSubscribe(t, h, "t", SubscribeOpts{})
		var published []Event
		var delivered []Event
		var lagged bool
		for i := 0; i < 200 && !lagged; i++ {
			var e Event
			switch rng.Intn(4) {
			case 0:
				e = crit("c")
			case 1:
				e = coal("k", fmt.Sprint(rng.Intn(3)))
			case 2:
				e = coal("t", "")
			default:
				e = ev("o", `{}`)
			}
			e.ID = mustPublish(t, h, "t", e)
			published = append(published, e)
			if rng.Intn(5) == 0 { // the client reads now and then
				for n := rng.Intn(4); n >= 0; n-- {
					got, ok, err := s.poll()
					if errors.Is(err, ErrLagged) {
						lagged = true
						break
					}
					if !ok {
						break
					}
					delivered = append(delivered, got)
				}
			}
		}
		got, err := drain(t, s)
		delivered = append(delivered, got...)
		lagged = lagged || errors.Is(err, ErrLagged)
		var last uint64
		seen := map[uint64]bool{}
		for _, d := range delivered {
			if d.ID == 0 {
				continue
			}
			if d.ID <= last {
				t.Fatalf("trial %d: id %d after %d", trial, d.ID, last)
			}
			last = d.ID
			seen[d.ID] = true
		}
		if lagged {
			continue
		}
		for _, p := range published {
			if p.Critical && !seen[p.ID] {
				t.Fatalf("trial %d: critical event %d was dropped for a client that was not disconnected", trial, p.ID)
			}
		}
	}
}

func TestStreamLifecycle(t *testing.T) {
	h := NewHub(HubConfig{MaxStreams: 2, MaxTopics: 3})
	_ = mustSubscribe(t, h, "a", SubscribeOpts{})
	b := mustSubscribe(t, h, "a", SubscribeOpts{})
	if _, err := h.Subscribe("a", SubscribeOpts{}); !errors.Is(err, ErrTooManyStreams) {
		t.Errorf("third stream: %v", err)
	}
	if h.Streams() != 2 {
		t.Errorf("Streams = %d", h.Streams())
	}
	b.Close()
	b.Close() // idempotent
	if h.Streams() != 1 {
		t.Errorf("Streams after Close = %d", h.Streams())
	}
	if _, err := b.Next(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("Next after Close: %v", err)
	}
	c := mustSubscribe(t, h, "a", SubscribeOpts{})
	_ = c

	// Topics are limited, and one that nothing was ever published to goes when its last subscriber does.
	h2 := NewHub(HubConfig{MaxTopics: 2})
	mustPublish(t, h2, "x", ev("a", `{}`))
	s := mustSubscribe(t, h2, "y", SubscribeOpts{})
	if _, err := h2.Publish("z", ev("a", `{}`)); !errors.Is(err, ErrTooManyTopics) {
		t.Errorf("third topic: %v", err)
	}
	if _, err := h2.Subscribe("z", SubscribeOpts{}); !errors.Is(err, ErrTooManyTopics) {
		t.Errorf("subscribing to a third topic: %v", err)
	}
	s.Close()
	mustPublish(t, h2, "z", ev("a", `{}`))

	// Blocked in Next, a stream is woken by Close from elsewhere.
	h3 := NewHub(HubConfig{})
	w := mustSubscribe(t, h3, "w", SubscribeOpts{})
	errc := make(chan error, 1)
	go func() { _, err := w.Next(context.Background()); errc <- err }()
	time.Sleep(20 * time.Millisecond)
	w.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("woken with %v", err)
		}
	case <-time.After(guard):
		t.Fatal("Close did not wake Next")
	}
	// Next honours its context.
	w2 := mustSubscribe(t, h3, "w2", SubscribeOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := w2.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Next with a deadline: %v", err)
	}
}

func TestClosingATopicDrainsItsSubscribers(t *testing.T) {
	h := NewHub(HubConfig{})
	s := mustSubscribe(t, h, "t", SubscribeOpts{})
	mustPublish(t, h, "t", crit("last-words"))
	h.CloseTopic("t")
	h.CloseTopic("t")
	h.CloseTopic("never-existed")
	got, err := drain(t, s)
	if len(got) != 1 || got[0].Type != "last-words" || !errors.Is(err, ErrTopicClosed) {
		t.Errorf("drained %v, %v", got, err)
	}
	if h.LastID("t") != 0 {
		t.Error("the ring of a closed topic is kept")
	}
	// The name can be used again, as a new topic.
	if id := mustPublish(t, h, "t", ev("a", `{}`)); id != 1 {
		t.Errorf("new topic id = %d", id)
	}
}

func TestClosingTheHubEndsEverything(t *testing.T) {
	h := NewHub(HubConfig{})
	s := mustSubscribe(t, h, "t", SubscribeOpts{})
	mustPublish(t, h, "t", ev("a", `{}`))
	h.Close()
	h.Close()
	if _, err := s.Next(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("Next after Close: %v", err)
	}
	if _, err := h.Publish("t", ev("a", `{}`)); !errors.Is(err, ErrClosed) {
		t.Errorf("Publish after Close: %v", err)
	}
	if _, err := h.Subscribe("t", SubscribeOpts{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Subscribe after Close: %v", err)
	}
}

func TestHubConfigKeepsItsLimitsConsistent(t *testing.T) {
	c := HubConfig{BufferBytes: 1000, MaxEventBytes: 5000, ReplayBytes: 400}
	c.fill()
	if c.MaxEventBytes > c.BufferBytes/2 || c.MaxEventBytes > c.ReplayBytes {
		t.Errorf("%+v: an event must fit an empty queue twice and the ring once", c)
	}
	d := HubConfig{}
	d.fill()
	if d.Heartbeat != 15*time.Second || d.WriteTimeout <= 0 || d.Buffer <= 0 || d.ReplayEvents <= 0 || d.MaxStreams <= 0 {
		t.Errorf("defaults = %+v", d)
	}
}

func TestManyPublishersAndSubscribersAtOnce(t *testing.T) {
	h := NewHub(HubConfig{Buffer: 16, MaxStreams: 64})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := h.Subscribe("t", SubscribeOpts{Resume: i%2 == 0})
			if err != nil {
				t.Error(err)
				return
			}
			defer s.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { <-stop; cancel() }()
			var last uint64
			for {
				e, err := s.Next(ctx)
				if err != nil {
					return
				}
				if e.ID != 0 && e.ID <= last {
					t.Errorf("ids out of order: %d after %d", e.ID, last)
					return
				}
				if e.ID != 0 {
					last = e.ID
				}
			}
		}(i)
	}
	var pub sync.WaitGroup
	for p := 0; p < 4; p++ {
		pub.Add(1)
		go func(p int) {
			defer pub.Done()
			for i := 0; i < 500; i++ {
				e := ev("a", `{}`)
				switch i % 3 {
				case 0:
					e.Critical = true
				case 1:
					e.Coalescable, e.Key = true, "k"
				}
				if _, err := h.Publish("t", e); err != nil {
					t.Error(err)
					return
				}
			}
		}(p)
	}
	pub.Wait()
	close(stop)
	wg.Wait()
	if h.Streams() != 0 {
		t.Errorf("%d streams left", h.Streams())
	}
}

// ---- over HTTP -----------------------------------------------------------------------------

// frame is one parsed block of a text/event-stream.
type frame struct {
	id, event, data string
	comment         bool
}

// readFrames parses an event stream until it ends.
func readFrames(r io.Reader) <-chan frame {
	ch := make(chan frame, 1024)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var cur frame
		started := false
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if started {
					ch <- cur
					cur, started = frame{}, false
				}
			case strings.HasPrefix(line, ":"):
				ch <- frame{comment: true, data: strings.TrimSpace(line[1:])}
			case strings.HasPrefix(line, "id: "):
				cur.id, started = line[4:], true
			case strings.HasPrefix(line, "event: "):
				cur.event, started = line[7:], true
			case strings.HasPrefix(line, "data: "):
				cur.data, started = line[6:], true
			case strings.HasPrefix(line, "retry: "):
				ch <- frame{event: "retry", data: line[7:]}
			default:
				ch <- frame{event: "garbage", data: line}
			}
		}
	}()
	return ch
}

// nextFrame waits for the next frame, ignoring heartbeats unless wanted.
func nextFrame(t testing.TB, ch <-chan frame, comments bool) frame {
	t.Helper()
	timeout := time.After(guard)
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatal("the stream ended")
			}
			if f.comment && !comments {
				continue
			}
			return f
		case <-timeout:
			t.Fatal("no frame")
		}
	}
}

// ended reports whether the stream ends (without waiting for more than the guard).
func ended(t testing.TB, ch <-chan frame) bool {
	t.Helper()
	timeout := time.After(guard)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

// serve runs the rig's server on a real loopback socket and returns its base URL.
func (rg *rig) serve() string {
	rg.t.Helper()
	ln, err := Listen("127.0.0.1:0", false)
	if err != nil {
		rg.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rg.srv.Serve(ctx, ln) }()
	rg.t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				rg.t.Errorf("Serve: %v", err)
			}
		case <-time.After(guard):
			rg.t.Error("Serve did not return")
		}
	})
	return "http://" + ln.Addr().String()
}

// open starts a stream request with the bearer token and returns its frames and a function that ends it.
func (rg *rig) open(base, path string, hdr map[string]string) (*http.Response, <-chan frame, func()) {
	rg.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	r.Header.Set("Authorization", "Bearer "+rg.srv.Token())
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		cancel()
		rg.t.Fatal(err)
	}
	var once sync.Once
	closeFn := func() { once.Do(func() { cancel(); resp.Body.Close() }) }
	rg.t.Cleanup(closeFn)
	if resp.StatusCode != 200 {
		return resp, nil, closeFn
	}
	return resp, readFrames(resp.Body), closeFn
}

func TestEventStreamOverARealSocket(t *testing.T) {
	rg := newRig(t, func(c *Config) { c.Hub.Heartbeat = 25 * time.Millisecond })
	base := rg.serve()
	resp, ch, closeFn := rg.open(base, "/api/events/session-1", nil)
	defer closeFn()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", resp.Header)
	}
	for k, v := range wantHeaders {
		if resp.Header.Get(k) != v {
			t.Errorf("%s = %q", k, resp.Header.Get(k))
		}
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("a stream without a CSP")
	}
	if f := nextFrame(t, ch, false); f.event != "retry" || f.data != "3000" {
		t.Errorf("first frame = %+v", f)
	}
	// Heartbeats keep an idle stream alive.
	if f := nextFrame(t, ch, true); !f.comment || f.data != "ping" {
		t.Errorf("heartbeat = %+v", f)
	}
	mustPublish(t, rg.srv.Hub(), "session-1", Event{Type: "say", Data: json.RawMessage(`{"text":"<hi> & bye"}`), Critical: true})
	f := nextFrame(t, ch, false)
	if want := `{"text":"` + uesc("003c") + `hi` + uesc("003e") + ` ` + uesc("0026") + ` bye"}`; f.id != "1" || f.event != "say" || f.data != want {
		t.Errorf("frame = %+v", f)
	}
	// Another topic's events do not arrive.
	mustPublish(t, rg.srv.Hub(), "session-2", ev("other", `{}`))
	mustPublish(t, rg.srv.Hub(), "session-1", ev("next", `{}`))
	if f := nextFrame(t, ch, false); f.event != "next" || f.id != "2" {
		t.Errorf("frame = %+v", f)
	}
	closeFn()
	waitFor(t, "the stream to be released", func() bool { return rg.srv.Hub().Streams() == 0 })
}

// waitFor polls a condition until it holds.
func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(guard)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEventStreamResumesFromLastEventID(t *testing.T) {
	rg := newRig(t, func(c *Config) { c.Hub.ReplayEvents = 5 })
	base := rg.serve()
	for i := 0; i < 8; i++ {
		mustPublish(t, rg.srv.Hub(), "t", ev("n", fmt.Sprintf(`{"i":%d}`, i+1)))
	}
	// EventSource sends Last-Event-ID by itself when it reconnects.
	_, ch, closeFn := rg.open(base, "/api/events/t", map[string]string{"Last-Event-ID": "6"})
	nextFrame(t, ch, false) // retry
	for _, want := range []string{"7", "8"} {
		if f := nextFrame(t, ch, false); f.id != want {
			t.Errorf("got %+v, want id %s", f, want)
		}
	}
	mustPublish(t, rg.srv.Hub(), "t", ev("n", `{}`))
	if f := nextFrame(t, ch, false); f.id != "9" {
		t.Errorf("live after replay: %+v", f)
	}
	closeFn()
	// A first connection can name where it stands (from a snapshot) with ?after=.
	_, ch, closeFn = rg.open(base, "/api/events/t?after=8", nil)
	nextFrame(t, ch, false)
	if f := nextFrame(t, ch, false); f.id != "9" {
		t.Errorf("after=8: %+v", f)
	}
	closeFn()
	// An id that has aged out is a gap: refetch a snapshot.
	_, ch, closeFn = rg.open(base, "/api/events/t", map[string]string{"Last-Event-ID": "2"})
	nextFrame(t, ch, false)
	f := nextFrame(t, ch, false)
	if f.event != "gap" || f.id != "" || !strings.Contains(f.data, `"reason":"aged"`) || !strings.Contains(f.data, `"last":9`) {
		t.Errorf("gap frame = %+v", f)
	}
	closeFn()
	// A client from before a restart is ahead of the topic.
	_, ch, closeFn = rg.open(base, "/api/events/t", map[string]string{"Last-Event-ID": "9000"})
	nextFrame(t, ch, false)
	if f := nextFrame(t, ch, false); f.event != "gap" || !strings.Contains(f.data, "ahead") {
		t.Errorf("ahead frame = %+v", f)
	}
	closeFn()
	for _, bad := range []string{"x", "-1", "1.5", "99999999999999999999999"} {
		resp, _, c := rg.open(base, "/api/events/t", map[string]string{"Last-Event-ID": bad})
		if resp.StatusCode != 400 {
			t.Errorf("Last-Event-ID %q = %d", bad, resp.StatusCode)
		}
		c()
	}
}

func TestHeadOnAStreamDoesNotOpenOne(t *testing.T) {
	rg := newRig(t, nil)
	rec := rg.do(req{method: "HEAD", target: "/api/events/t", header: rg.bearer()})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" || rec.Body.Len() != 0 || rg.srv.Hub().Streams() != 0 {
		t.Errorf("HEAD = %d %q, %d streams", rec.Code, rec.Header().Get("Content-Type"), rg.srv.Hub().Streams())
	}
}

func TestEventStreamEnds(t *testing.T) {
	rg := newRig(t, nil)
	base := rg.serve()

	// The topic closes: the client is told and the stream ends.
	_, ch, closeFn := rg.open(base, "/api/events/t", nil)
	nextFrame(t, ch, false)
	mustPublish(t, rg.srv.Hub(), "t", crit("last"))
	rg.srv.Hub().CloseTopic("t")
	if f := nextFrame(t, ch, false); f.event != "last" {
		t.Errorf("frame = %+v", f)
	}
	if f := nextFrame(t, ch, false); f.event != "closed" {
		t.Errorf("frame = %+v", f)
	}
	if !ended(t, ch) {
		t.Error("the stream did not end")
	}
	closeFn()

	// The client goes away: the stream is released.
	_, ch, closeFn = rg.open(base, "/api/events/t2", nil)
	nextFrame(t, ch, false)
	if n := rg.srv.Hub().Streams(); n != 1 {
		t.Errorf("Streams = %d", n)
	}
	closeFn()
	waitFor(t, "the handler to notice the client left", func() bool { return rg.srv.Hub().Streams() == 0 })

	// The limit on streams is a clear 503.
	rg2 := newRig(t, func(c *Config) { c.Hub.MaxStreams = 1 })
	base2 := rg2.serve()
	_, ch1, c1 := rg2.open(base2, "/api/events/a", nil)
	nextFrame(t, ch1, false)
	resp, _, c2 := rg2.open(base2, "/api/events/b", nil)
	if resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
		t.Errorf("second stream = %d", resp.StatusCode)
	}
	c2()
	c1()
}

func TestShutdownEndsStreamsInsteadOfWaitingForThem(t *testing.T) {
	rg := newRig(t, nil)
	ln, err := Listen("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rg.srv.Serve(ctx, ln) }()
	base := "http://" + ln.Addr().String()
	_, ch, closeFn := rg.open(base, "/api/events/t", nil)
	defer closeFn()
	nextFrame(t, ch, false)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v", err)
		}
	case <-time.After(guard):
		t.Fatal("Serve did not return")
	}
	if d := time.Since(start); d > shutdownGrace-500*time.Millisecond {
		t.Errorf("shutdown took %v: streams must be ended, not waited for until the grace runs out", d)
	}
	if !ended(t, ch) {
		t.Error("the stream is still open after shutdown")
	}
}

func TestReadTimeoutDoesNotCutAStream(t *testing.T) {
	rg := newRig(t, func(c *Config) { c.Hub.Heartbeat = 20 * time.Millisecond })
	rg.srv.readTimeout = 100 * time.Millisecond
	base := rg.serve()
	_, ch, closeFn := rg.open(base, "/api/events/t", nil)
	defer closeFn()
	nextFrame(t, ch, false)
	time.Sleep(400 * time.Millisecond) // four times the server's read timeout
	mustPublish(t, rg.srv.Hub(), "t", ev("late", `{}`))
	deadline := time.After(guard)
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatal("the stream was cut by the server's read timeout")
			}
			if f.event == "late" {
				return
			}
		case <-deadline:
			t.Fatal("no event")
		}
	}
}

func TestStreamEndsWhenItsCredentialDoes(t *testing.T) {
	rg := newRig(t, func(c *Config) { c.Hub.Heartbeat = 20 * time.Millisecond; c.SessionTTL = time.Hour })
	base := rg.serve()
	open := func(cookie string) (<-chan frame, func()) {
		_, ch, c := rg.open(base, "/api/events/t", map[string]string{"Authorization": "", "Cookie": cookie})
		nextFrame(t, ch, false)
		return ch, c
	}
	post := func(cookie, path string) int {
		r, _ := http.NewRequest("POST", base+path, nil)
		r.Header.Set("Cookie", cookie)
		r.Header.Set("Origin", base)
		r.Header.Set(RequestHeader, "1")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	login := func() string {
		r, _ := http.NewRequest("GET", base+"/?token="+rg.srv.Token(), nil)
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		for _, c := range resp.Cookies() {
			return c.Name + "=" + c.Value
		}
		t.Fatal("no cookie")
		return ""
	}
	// Logout.
	a := login()
	ch, closeFn := open(a)
	if post(a, "/api/auth/logout") != 200 {
		t.Fatal("logout failed")
	}
	if !ended(t, ch) {
		t.Error("a stream outlived its session's logout")
	}
	closeFn()
	// Rotation ends the streams of every session and of the old bearer token.
	a, b := login(), login()
	chA, closeA := open(a)
	chB, closeB := open(b)
	_, chT, closeT := rg.open(base, "/api/events/t", nil)
	nextFrame(t, chT, false)
	if post(a, "/api/auth/rotate") != 200 {
		t.Fatal("rotate failed")
	}
	if !ended(t, chB) || !ended(t, chT) {
		t.Error("a stream outlived the rotation")
	}
	_ = chA
	closeA()
	closeB()
	closeT()
	// Expiry is noticed at the next heartbeat.
	c := login()
	chC, closeC := open(c)
	defer closeC()
	rg.clock.Advance(2 * time.Hour)
	if !ended(t, chC) {
		t.Error("a stream outlived its session's expiry")
	}
}

// ---- the writer side, with a client that does not read ---------------------------------------

// gatedWriter is a response writer whose Write blocks until released.
type gatedWriter struct {
	mu        sync.Mutex
	hdr       http.Header
	status    int
	buf       bytes.Buffer
	gate      chan struct{}
	entered   chan struct{}
	deadlines []time.Time
	writes    int
	flushes   int
}

func newGatedWriter() *gatedWriter {
	return &gatedWriter{hdr: http.Header{}, gate: make(chan struct{}), entered: make(chan struct{}, 100)}
}
func (w *gatedWriter) Header() http.Header { return w.hdr }
func (w *gatedWriter) WriteHeader(c int)   { w.status = c }
func (w *gatedWriter) Write(p []byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-w.gate
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	return w.buf.Write(p)
}
func (w *gatedWriter) Flush() { w.mu.Lock(); w.flushes++; w.mu.Unlock() }
func (w *gatedWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	w.deadlines = append(w.deadlines, t)
	w.mu.Unlock()
	return nil
}
func (w *gatedWriter) SetReadDeadline(time.Time) error { return nil }
func (w *gatedWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func serveSSE(h *Hub, w http.ResponseWriter, target string, hdr map[string]string, ctx context.Context) <-chan struct{} {
	r := httptest.NewRequest("GET", target, nil).WithContext(ctx)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	done := make(chan struct{})
	handler := h.ServeSSE(func(r *http.Request) (string, bool) { return "t", true })
	go func() { defer close(done); handler.ServeHTTP(w, r) }()
	return done
}

func TestAClientThatCannotKeepUpWithCriticalEventsIsToldAndReconnects(t *testing.T) {
	h := NewHub(HubConfig{Buffer: 4, Heartbeat: time.Hour})
	w := newGatedWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := serveSSE(h, w, "/", nil, ctx)
	<-w.entered // the handler is inside its first write and stays there: this client does not read
	for i := 0; i < 20; i++ {
		mustPublish(t, h, "t", Event{Type: "q", Critical: true, Data: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))})
	}
	close(w.gate)
	select {
	case <-done:
	case <-time.After(guard):
		t.Fatal("the handler did not end")
	}
	out := w.text()
	if !strings.Contains(out, "event: lagged\ndata: {\"after\":0}\n\n") {
		t.Errorf("the client was not told: %q", out)
	}
	if strings.Contains(out, "event: q") {
		t.Errorf("events were delivered out of a queue that was thrown away: %q", out)
	}
	if h.Streams() != 0 {
		t.Errorf("the lagged stream still counts: %d", h.Streams())
	}
	// It reconnects with the id of the last event it saw and is given what it missed, in order.
	w2 := newGatedWriter()
	close(w2.gate)
	done2 := serveSSE(h, w2, "/", map[string]string{"Last-Event-ID": "0"}, ctx)
	waitFor(t, "the replay", func() bool { return strings.Count(w2.text(), "event: q") == 20 })
	cancel()
	<-done2
	var got []string
	for _, f := range strings.Split(w2.text(), "\n\n") {
		if strings.Contains(f, "event: q") {
			got = append(got, strings.SplitN(strings.TrimPrefix(f, "id: "), "\n", 2)[0])
		}
	}
	var want []string
	for i := 1; i <= 20; i++ {
		want = append(want, strconv.Itoa(i))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("replayed ids = %v, want 1..20 in order", got)
	}
}

func TestEveryWriteGetsAFreshDeadline(t *testing.T) {
	h := NewHub(HubConfig{WriteTimeout: 3 * time.Second, Heartbeat: 10 * time.Millisecond})
	w := newGatedWriter()
	close(w.gate)
	ctx, cancel := context.WithCancel(context.Background())
	done := serveSSE(h, w, "/", nil, ctx)
	waitFor(t, "heartbeats", func() bool { return strings.Count(w.text(), ": ping") >= 2 })
	mustPublish(t, h, "t", ev("a", `{}`))
	waitFor(t, "an event", func() bool { return strings.Contains(w.text(), "event: a") })
	cancel()
	<-done
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.deadlines) < w.writes {
		t.Errorf("%d deadlines for %d writes: every write needs its own", len(w.deadlines), w.writes)
	}
	for i := 1; i < len(w.deadlines); i++ {
		if w.deadlines[i].Before(w.deadlines[i-1]) {
			t.Errorf("deadline %d moved backwards", i)
		}
	}
	if d := time.Until(w.deadlines[len(w.deadlines)-1]); d > 3*time.Second {
		t.Errorf("the deadline is %v ahead, want at most the write timeout", d)
	}
	if w.flushes < w.writes {
		t.Errorf("%d flushes for %d writes: an event must not wait in a buffer", w.flushes, w.writes)
	}
}

// A real connection whose peer stops reading is cut off by the write deadline instead of holding the handler (and its memory) forever.
func TestAPeerThatStopsReadingIsCutOffByTheWriteDeadline(t *testing.T) {
	rg := newRig(t, func(c *Config) {
		c.Hub.WriteTimeout = 200 * time.Millisecond
		c.Hub.BufferBytes = 256 << 20
		c.Hub.Buffer = 1 << 16
		c.Hub.ReplayEvents = 4
		c.Hub.MaxEventBytes = 128 << 10
	})
	base := rg.serve()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	fmt.Fprintf(conn, "GET /api/events/t HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\n\r\n", strings.TrimPrefix(base, "http://"), rg.srv.Token())
	waitFor(t, "the stream to open", func() bool { return rg.srv.Hub().Streams() == 1 })
	payload := json.RawMessage(`"` + strings.Repeat("x", 100<<10) + `"`)
	stop := time.Now().Add(guard)
	for rg.srv.Hub().Streams() == 1 && time.Now().Before(stop) {
		// not critical: the queue is large, the point is the socket
		if _, err := rg.srv.Hub().Publish("t", Event{Type: "bulk", Data: payload, Coalescable: true}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if rg.srv.Hub().Streams() != 0 {
		t.Fatal("a peer that stopped reading still holds a stream")
	}
}
