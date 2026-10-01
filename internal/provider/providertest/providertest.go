// Package providertest is the scripted-stream harness that the provider adapters'
// tests share, and the contract every adapter owes its caller.
//
// It runs a real provider.Provider against a scripted response body, without a
// network: the body is a reader that hands out the bytes in the chunks the test
// asks for, so every way a stream can be cut up by the transport (mid-line,
// mid-escape, mid-UTF-8 sequence) can be put through the decoder on purpose. It
// records what the caller can observe (the events and the outcome), and it states
// the contract every adapter owes its caller whatever the endpoint sends: the
// properties Check asserts are the ones the agent, the swarm's warm gate and the
// event log rely on.
package providertest

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// hangGuard is how long one scripted request may take before the test calls it a
// hang. It is a guard against a complexity bomb or a loop that never ends, not a
// timing: a decode of a few kilobytes takes microseconds, and three race-enabled
// suites on a loaded machine take seconds, so minutes cannot be reached by a
// healthy decoder. While fuzzing, an input that takes half a minute is a finding
// (the fuzzing engine would otherwise wait on it and report nothing), so the guard
// is short there and the input is saved as a crasher.
func hangGuard() time.Duration {
	if f := flag.Lookup("test.fuzz"); f != nil && f.Value.String() != "" {
		return 30 * time.Second
	}
	return 3 * time.Minute
}

// Adapter is one provider under test.
type Adapter struct {
	// Name labels failures.
	Name string
	// New builds the provider around hc, which answers every request it sends.
	New func(hc *http.Client) provider.Provider
	// Model is the model id the prompt names.
	Model string
	// Capture asks for token ids and logprobs.
	Capture bool
	// ContentType of the scripted reply; empty means text/event-stream.
	ContentType string
	// Status of the scripted reply; zero means 200.
	Status int
}

// Outcome is everything a caller can observe of one request.
type Outcome struct {
	Events []provider.Event
	Resp   *provider.Response
	Err    error
}

// Chunking decides where a body of n bytes is cut into reads.
type Chunking func(n int) []int

// Whole delivers the body in one piece.
func Whole(int) []int { return nil }

// SplitAt delivers the body as two pieces, cut at k.
func SplitAt(k int) Chunking { return func(int) []int { return []int{k} } }

// Every delivers the body in pieces of size bytes.
func Every(size int) Chunking {
	return func(n int) []int {
		var cuts []int
		for k := size; k < n; k += size {
			cuts = append(cuts, k)
		}
		return cuts
	}
}

// Cuts delivers the body cut at exactly these offsets (sorted, deduplicated and
// clipped to the body by the reader).
func Cuts(offsets ...int) Chunking { return func(int) []int { return offsets } }

// chunkReader hands out data in pieces that never span a cut, then reports EOF, or
// fail (when set) once failAt bytes have been delivered.
type chunkReader struct {
	data   []byte
	cuts   []int // ascending, each in (0, len(data))
	pos    int
	failAt int // -1: never
	fail   error
}

func newChunkReader(data []byte, cuts []int) *chunkReader {
	cl := append([]int(nil), cuts...)
	sort.Ints(cl)
	out := cl[:0]
	prev := 0
	for _, c := range cl {
		if c > prev && c < len(data) {
			out = append(out, c)
			prev = c
		}
	}
	return &chunkReader{data: data, cuts: out, failAt: -1}
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.failAt >= 0 && c.pos >= c.failAt {
		return 0, c.fail
	}
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	end := len(c.data)
	for _, cut := range c.cuts {
		if cut > c.pos {
			end = cut
			break
		}
	}
	if c.failAt >= 0 && c.failAt < end {
		end = c.failAt
	}
	n := copy(p, c.data[c.pos:end])
	c.pos += n
	return n, nil
}

// roundTripper answers every request with a 200 whose body is mk().
type roundTripper struct {
	mk          func() io.Reader
	contentType string
	status      int
}

func (t roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	h := http.Header{}
	h.Set("Content-Type", t.contentType)
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), StatusCode: status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: io.NopCloser(t.mk()), ContentLength: -1, Request: r,
	}, nil
}

// Run sends one streaming request whose reply is data, delivered as chunking says,
// and returns what the caller saw.
func (a Adapter) Run(tb testing.TB, data []byte, chunking Chunking) Outcome {
	tb.Helper()
	if chunking == nil {
		chunking = Whole
	}
	return a.RunReader(tb, func() io.Reader { return newChunkReader(data, chunking(len(data))) })
}

// RunFailing is Run for a body whose connection breaks after failAt bytes with err.
func (a Adapter) RunFailing(tb testing.TB, data []byte, failAt int, err error) Outcome {
	tb.Helper()
	return a.RunReader(tb, func() io.Reader {
		c := newChunkReader(data, nil)
		c.failAt, c.fail = failAt, err
		return c
	})
}

// RunReader is Run for a body the test builds itself. mk is called once per
// request.
func (a Adapter) RunReader(tb testing.TB, mk func() io.Reader) Outcome {
	tb.Helper()
	ct := a.ContentType
	if ct == "" {
		ct = "text/event-stream"
	}
	hc := &http.Client{Transport: roundTripper{mk: mk, contentType: ct, status: a.Status}}
	p := a.New(hc)
	req := &provider.Request{
		Prompt:  &core.Prompt{Model: a.Model, Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("hi")}}}, Params: core.Params{MaxTokens: 256}},
		Capture: a.Capture,
	}
	var (
		mu   sync.Mutex
		evs  []provider.Event
		resp *provider.Response
		err  error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err = p.Do(context.Background(), req, func(e provider.Event) {
			mu.Lock()
			defer mu.Unlock()
			evs = append(evs, detach(e))
		})
	}()
	select {
	case <-done:
	case <-time.After(hangGuard()):
		tb.Fatalf("%s: the decoder did not finish within %v (a hang)", a.Name, hangGuard())
	}
	mu.Lock()
	defer mu.Unlock()
	return Outcome{Events: evs, Resp: detachResp(resp), Err: err}
}

// detach makes an event comparable across runs: the clock reading goes, and the
// pointers are replaced by copies of what they point to.
func detach(e provider.Event) provider.Event {
	e.Elapsed = 0
	if e.Block != nil {
		b := *e.Block
		e.Block = &b
	}
	if e.Usage != nil {
		u := *e.Usage
		e.Usage = &u
	}
	return e
}

func detachResp(r *provider.Response) *provider.Response {
	if r == nil {
		return nil
	}
	c := *r
	c.TTFB, c.Total = 0, 0
	if r.CostUSD != nil {
		v := *r.CostUSD
		c.CostUSD = &v
	}
	return &c
}

// Diff says how two outcomes differ, or returns "" when a caller could not tell
// them apart. Errors are compared by what a caller reads from them.
func (o Outcome) Diff(p Outcome) string {
	if d := diffErr(o.Err, p.Err); d != "" {
		return d
	}
	if !reflect.DeepEqual(o.Resp, p.Resp) {
		return fmt.Sprintf("responses differ:\n a: %s\n b: %s", dumpResp(o.Resp), dumpResp(p.Resp))
	}
	if len(o.Events) != len(p.Events) {
		return fmt.Sprintf("%d events vs %d events:\n a: %s\n b: %s", len(o.Events), len(p.Events), dumpEvents(o.Events), dumpEvents(p.Events))
	}
	for i := range o.Events {
		if !reflect.DeepEqual(o.Events[i], p.Events[i]) {
			return fmt.Sprintf("event %d differs:\n a: %s\n b: %s", i, dumpEvent(o.Events[i]), dumpEvent(p.Events[i]))
		}
	}
	return ""
}

func diffErr(a, b error) string {
	switch {
	case a == nil && b == nil:
		return ""
	case a == nil || b == nil:
		return fmt.Sprintf("errors differ: a=%v b=%v", a, b)
	}
	pa, oka := provider.AsError(a)
	pb, okb := provider.AsError(b)
	if oka != okb {
		return fmt.Sprintf("error types differ: a=%T b=%T", a, b)
	}
	if !oka {
		if a.Error() != b.Error() {
			return fmt.Sprintf("errors differ: a=%v b=%v", a, b)
		}
		return ""
	}
	if pa.Kind != pb.Kind || pa.Status != pb.Status || pa.Message != pb.Message || pa.RetryAfter != pb.RetryAfter ||
		pa.NoRetry != pb.NoRetry || !bytes.Equal(pa.Raw, pb.Raw) {
		return fmt.Sprintf("errors differ:\n a: %#v\n b: %#v", *pa, *pb)
	}
	return ""
}

func dumpResp(r *provider.Response) string {
	if r == nil {
		return "<nil>"
	}
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Sprintf("%+v", *r)
	}
	return string(b)
}

func dumpEvent(e provider.Event) string {
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Sprintf("%+v", e)
	}
	return string(b)
}

func dumpEvents(evs []provider.Event) string {
	var sb strings.Builder
	for i, e := range evs {
		if i > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(dumpEvent(e))
		if sb.Len() > 4000 {
			sb.WriteString(" ...")
			break
		}
	}
	return sb.String()
}

// Inert reports whether s is text that is safe to log and to print: it is what
// provider.SanitizeText leaves alone when its length cap is out of the way (no
// escape sequence, control character, hidden character, line break or run of white
// space, and valid UTF-8).
func Inert(s string) bool {
	return utf8.ValidString(s) && provider.SanitizeText(s, len(s)+64) == s
}

// maxMessage is the longest error message an adapter may produce: the sanitiser's
// cap plus the few short labels adapters append after it (a request id).
const maxMessage = provider.MaxErrorText + 256

// Check asserts the contract of an adapter on one outcome, whatever the endpoint
// sent:
//
//   - a failure is a *provider.Error with a defined kind, a message that says
//     something and is inert and bounded, a status that is zero or an HTTP status, a
//     bounded raw body, a retry delay within its clamp, and no response;
//   - a success has a response whose identifiers are inert and bounded, counters
//     within their clamp, a cost that can be believed or none, a defined stop
//     reason, and only valid JSON wherever the block is replayed verbatim;
//   - on a success the caller saw exactly one EvStart (the warm gate waits for it)
//     and exactly one EvUsage, last, carrying the response's usage (the cost
//     accounting reads it);
//   - on any outcome the events are well formed and EvStart comes at most once.
//
// What is deliberately not asserted: that the model's own words are free of
// control characters. They are replayed into the next prompt verbatim, and the
// terminal sink cleans them where they are displayed (session/sink.go).
func Check(tb testing.TB, o Outcome) {
	tb.Helper()
	starts := 0
	for i, e := range o.Events {
		if e.Kind < provider.EvStart || e.Kind > provider.EvReset {
			tb.Errorf("event %d has undefined kind %d", i, e.Kind)
		}
		switch e.Kind {
		case provider.EvStart:
			starts++
			if starts > 1 {
				tb.Errorf("EvStart twice")
			}
			checkID(tb, "EvStart.RequestID", e.RequestID)
		case provider.EvBlockDone:
			if e.Block == nil {
				tb.Errorf("EvBlockDone %d without a block", i)
			}
		case provider.EvUsage:
			if e.Usage == nil {
				tb.Errorf("EvUsage %d without usage", i)
			}
			checkID(tb, "EvUsage.RequestID", e.RequestID)
		}
	}
	if o.Err != nil {
		if o.Resp != nil {
			tb.Errorf("both a response and an error: %v", o.Err)
		}
		checkError(tb, o.Err)
		return
	}
	if o.Resp == nil {
		tb.Fatalf("neither a response nor an error")
	}
	checkResponse(tb, o)
}

func checkID(tb testing.TB, what, s string) {
	tb.Helper()
	if !Inert(s) || len(s) > 256 {
		tb.Errorf("%s is not inert or too long: %q", what, truncate(s))
	}
}

func truncate(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

func checkError(tb testing.TB, err error) {
	tb.Helper()
	pe, ok := provider.AsError(err)
	if !ok {
		tb.Errorf("the error is not a *provider.Error: %T %v", err, err)
		return
	}
	if pe == nil {
		tb.Errorf("a nil *provider.Error inside a non-nil error")
		return
	}
	if pe.Kind < provider.ErrUnknown || pe.Kind > provider.ErrPayment {
		tb.Errorf("undefined error kind %d", pe.Kind)
		return // String() would index out of range
	}
	if !Inert(pe.Message) || pe.Message == "" {
		tb.Errorf("error message is empty or not inert: %q", truncate(pe.Message))
	}
	if pe.Status != 0 && (pe.Status < 100 || pe.Status > 599) {
		tb.Errorf("Status %d is not an HTTP status", pe.Status)
	}
	if len(pe.Message) > maxMessage {
		tb.Errorf("error message is %d bytes (limit %d)", len(pe.Message), maxMessage)
	}
	if s := pe.Error(); !Inert(s) || len(s) > maxMessage+64 {
		tb.Errorf("Error() is not inert or too long (%d bytes): %q", len(s), truncate(s))
	}
	if len(pe.Raw) > provider.MaxRawBytes {
		tb.Errorf("error Raw is %d bytes (limit %d)", len(pe.Raw), provider.MaxRawBytes)
	}
	if pe.RetryAfter < 0 || pe.RetryAfter > provider.DefaultMaxRetryAfter {
		tb.Errorf("RetryAfter %v is outside [0, %v]", pe.RetryAfter, provider.DefaultMaxRetryAfter)
	}
}

func checkResponse(tb testing.TB, o Outcome) {
	tb.Helper()
	r := o.Resp
	starts := 0
	for _, e := range o.Events {
		if e.Kind == provider.EvStart {
			starts++
		}
	}
	checkID(tb, "Response.ID", r.ID)
	checkID(tb, "Response.Model", r.Model)
	checkID(tb, "Response.Provider", r.Provider)
	checkID(tb, "Turn.Model", r.Turn.Model)
	if r.Turn.Role != core.RoleAssistant || r.Turn.Origin != core.OriginModel {
		tb.Errorf("turn is %s/%v, want an assistant turn of the model", r.Turn.Role, r.Turn.Origin)
	}
	u := r.Usage
	for name, n := range map[string]int{
		"input": u.InputTokens, "cache read": u.CacheReadTokens, "cache write 5m": u.CacheWrite5mTokens,
		"cache write 1h": u.CacheWrite1hTokens, "output": u.OutputTokens, "reasoning": u.ReasoningTokens,
	} {
		if n < 0 || n > provider.MaxUsageTokens {
			tb.Errorf("%s tokens %d are outside [0, %d]", name, n, provider.MaxUsageTokens)
		}
	}
	if u.ReasoningTokens > u.OutputTokens {
		tb.Errorf("reasoning tokens %d exceed output tokens %d", u.ReasoningTokens, u.OutputTokens)
	}
	if r.CostUSD != nil && provider.ValidCost(r.CostUSD) == nil {
		tb.Errorf("a cost that cannot be believed was passed on: %v", *r.CostUSD)
	}
	if len(r.RawUsage) > 0 && !json.Valid(r.RawUsage) {
		tb.Errorf("RawUsage is not JSON: %q", truncate(string(r.RawUsage)))
	}
	switch r.Stop {
	case core.StopEnd, core.StopToolUse, core.StopMaxTokens, core.StopPause, core.StopRefusal, core.StopOther:
	default:
		tb.Errorf("undefined stop reason %q", r.Stop)
	}
	for i, b := range r.Turn.Blocks {
		if b.Kind == "" {
			tb.Errorf("block %d has no kind", i)
		}
		// What goes back to the endpoint verbatim must be JSON: a block that is not
		// would corrupt the next request of the conversation.
		if len(b.Wire) > 0 && !json.Valid(b.Wire) {
			tb.Errorf("block %d (%s): Wire is not JSON: %q", i, b.Kind, truncate(string(b.Wire)))
		}
		if b.Kind == core.BlockToolUse {
			if len(b.Input) == 0 || !json.Valid(b.Input) {
				tb.Errorf("block %d: tool input is not JSON: %q", i, truncate(string(b.Input)))
			}
		} else if len(b.Input) > 0 && !json.Valid(b.Input) {
			tb.Errorf("block %d (%s): Input is not JSON: %q", i, b.Kind, truncate(string(b.Input)))
		}
	}

	if starts != 1 {
		tb.Errorf("a response that completed announced its start %d times, want once: %s", starts, dumpEvents(o.Events))
	}
	if n := len(o.Events); n == 0 || o.Events[n-1].Kind != provider.EvUsage {
		tb.Errorf("a response that completed did not end with EvUsage: %s", dumpEvents(o.Events))
	} else if last := o.Events[n-1]; last.Usage == nil || *last.Usage != r.Usage {
		tb.Errorf("EvUsage carries %+v, the response %+v", last.Usage, r.Usage)
	}
	usages := 0
	for _, e := range o.Events {
		if e.Kind == provider.EvUsage {
			usages++
		}
	}
	if usages != 1 {
		tb.Errorf("%d EvUsage events, want 1", usages)
	}
}
