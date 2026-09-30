package anthropic_test

import (
	"io"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/providertest"
)

// Every recorded stream decodes to what it says when it arrives in one piece, and
// the contract of an adapter holds for it.
func TestRecordedStreamsDecodeAsTheySay(t *testing.T) {
	for _, s := range streams {
		t.Run(s.name, func(t *testing.T) {
			o := adapter().Run(t, []byte(s.body), providertest.Whole)
			providertest.Check(t, o)
			s.want(t, o)
		})
	}
}

// The streams the package's own tests replay keep the contract too, however they
// end (a complete message, a cut one, an error, a malformed frame).
func TestFixtureStreamsKeepTheContract(t *testing.T) {
	for name, body := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			providertest.Check(t, adapter().Run(t, body, providertest.Whole))
			providertest.Check(t, adapter().Run(t, []byte(crlf(string(body))), providertest.Whole))
		})
	}
}

// How the transport cuts a stream up must not show in what the caller sees: the
// bytes are the same, so the events, the response and the error are the same. The
// hand-written streams go through every two-piece split (which includes every cut
// through a line, an escape, a CR LF pair and a multi-byte character); all streams,
// those and every fixture the package keeps (each also with CRLF line ends), go
// through one-byte reads, reads of small sizes and a seeded sample of many-piece
// splits.
func TestStreamIsIdenticalWhereverItIsCut(t *testing.T) {
	type stream struct {
		name       string
		body       []byte
		exhaustive bool
	}
	var all []stream
	for _, s := range streams {
		all = append(all, stream{s.name, []byte(s.body), true})
	}
	fx := fixtures(t)
	names := make([]string, 0, len(fx))
	for n := range fx {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		all = append(all, stream{"fixture " + n, fx[n], false}, stream{"fixture " + n + " (CRLF)", []byte(crlf(string(fx[n]))), false})
	}
	for _, s := range all {
		t.Run(s.name, func(t *testing.T) {
			a, body := adapter(), s.body
			whole := a.Run(t, body, providertest.Whole)
			compare := func(label string, c providertest.Chunking) {
				t.Helper()
				o := a.Run(t, body, c)
				providertest.Check(t, o)
				if d := whole.Diff(o); d != "" {
					t.Fatalf("%s: %s", label, d)
				}
			}
			if s.exhaustive {
				for k := 1; k < len(body); k++ {
					compare("split at byte "+strconv.Itoa(k), providertest.SplitAt(k))
				}
			}
			compare("one byte at a time", providertest.Every(1))
			for _, size := range []int{2, 3, 5, 8} {
				compare("reads of "+strconv.Itoa(size)+" bytes", providertest.Every(size))
			}
			rng := rand.New(rand.NewSource(int64(len(body)))) // fixed: a failure repeats
			for i := 0; i < 40; i++ {
				var cuts []int
				for n := 2 + rng.Intn(12); n > 0; n-- {
					cuts = append(cuts, 1+rng.Intn(len(body)))
				}
				compare("cuts "+joinInts(cuts), providertest.Cuts(cuts...))
			}
		})
	}
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// A stream that stops before the API said the message was over is a dropped
// connection, not a short answer: the agent retries the first and accepts the
// second as the model's whole reply. Every prefix of a complete message that
// stops short of the event that carries its stop reason fails, and fails as
// something the agent retries.
func TestTruncatedStreamsAreNeverMistakenForAnswers(t *testing.T) {
	for _, s := range streams {
		if !strings.Contains(s.body, "message_stop") || strings.Contains(s.body, "\r\n") || !strings.Contains(s.body, "event: ") {
			continue // the stream that ends in an error has no end to cut short; the variants add nothing here
		}
		t.Run(s.name, func(t *testing.T) {
			a, body := adapter(), []byte(s.body)
			// The message is over once message_delta has carried its stop reason: a prefix
			// that holds it (and only the message_stop event is missing) is accepted.
			end := strings.Index(s.body, `"stop_reason":"`) // first non-null stop reason
			end += strings.IndexAny(s.body[end:], "\r\n")   // the data line is whole without its line end
			for k := 0; k < len(body); k++ {
				o := a.Run(t, body[:k], providertest.Whole)
				providertest.Check(t, o)
				if k < end {
					// A cut inside a frame that is named as one of the API's own events is a malformed
					// frame (a server error); a cut between frames is a dropped connection. Both are
					// retried.
					pe, _ := provider.AsError(o.Err)
					if pe == nil || !pe.Retryable() || (pe.Kind != provider.ErrNetwork && pe.Kind != provider.ErrServer) {
						t.Fatalf("prefix of %d bytes (the message is over at %d) must be a retryable failure, got %v", k, end, o.Err)
					}
				}
			}
		})
	}
}

// FuzzStream feeds arbitrary bytes through the real request path (Client.Do over a
// scripted transport) and asserts what an adapter owes its caller whatever the
// endpoint sends: no panic, no hang, a *provider.Error of a defined kind with an
// inert bounded message when it fails, a well-formed response when it does not
// (providertest.Check), and the same outcome however the transport cuts the bytes
// up. A connection that breaks after any number of bytes must end in a contract
// outcome as well.
func FuzzStream(f *testing.F) {
	// The recorded streams are the seed corpus under testdata/fuzz/FuzzStream; here are
	// the package's fixtures, and frames no endpoint should send.
	for _, body := range fixtures(f) {
		f.Add(body, uint16(len(body)/2))
	}
	for _, in := range []string{
		"", "\n", "data:", "event: message_stop\n", "event: message_stop\ndata: {}\n\n", ": ping\n\n",
		"event: error\ndata: {\"type\":\"error\",\"error\":null}\n\n",
		"event: error\ndata: not json\n\n",
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"\\u001b]52;c;AAAA\\u0007\",\"model\":\"m\\u202e\",\"usage\":{\"input_tokens\":\"Infinity\",\"output_tokens\":-5}}}\n\nevent: message_stop\ndata: {}\n\n",
		"event: message_start\ndata: {}\n\nevent: content_block_start\ndata: {\"index\":-3,\"content_block\":{\"type\":\"tool_use\",\"id\":\"\",\"name\":\"x\",\"input\":[1]}}\n\nevent: message_stop\ndata: {}\n\n",
		"event: message_start\ndata: {}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"a\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"b\"}}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"x\\u001b[2J\"},\"usage\":{}}\n\nevent: message_stop\ndata: {}\n\n",
	} {
		f.Add([]byte(in), uint16(3))
	}
	f.Fuzz(func(t *testing.T, in []byte, cut uint16) {
		a := adapter()
		whole := a.Run(t, in, providertest.Whole)
		providertest.Check(t, whole)

		at := int(cut) % (len(in) + 1)
		if d := whole.Diff(a.Run(t, in, providertest.SplitAt(at))); d != "" {
			t.Fatalf("a split at byte %d changed the outcome: %s", at, d)
		}
		if len(in) <= 2048 {
			if d := whole.Diff(a.Run(t, in, providertest.Every(1))); d != "" {
				t.Fatalf("one byte at a time changed the outcome: %s", d)
			}
		}
		providertest.Check(t, a.RunFailing(t, in, at, io.ErrUnexpectedEOF))
	})
}
