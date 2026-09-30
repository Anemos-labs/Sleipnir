package openaichat_test

import (
	"encoding/json"
	"io"
	"math/rand"
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
			o := adapter(s.capture).Run(t, []byte(s.body), providertest.Whole)
			providertest.Check(t, o)
			s.want(t, o)
		})
	}
}

// How the transport cuts a stream up must not show in what the caller sees: the
// bytes are the same, so the events, the response and the error are the same. This
// puts the stream through every two-piece split (which includes every cut through
// a line, an escape and a multi-byte character), through one-byte reads, through
// reads of every small size, and through a seeded sample of many-piece splits.
func TestStreamIsIdenticalWhereverItIsCut(t *testing.T) {
	for _, s := range streams {
		t.Run(s.name, func(t *testing.T) {
			a, body := adapter(s.capture), []byte(s.body)
			whole := a.Run(t, body, providertest.Whole)
			compare := func(label string, c providertest.Chunking) {
				t.Helper()
				o := a.Run(t, body, c)
				providertest.Check(t, o)
				if d := whole.Diff(o); d != "" {
					t.Fatalf("%s: %s", label, d)
				}
			}
			for k := 1; k < len(body); k++ {
				compare("split at byte "+itoa(k), providertest.SplitAt(k))
			}
			compare("one byte at a time", providertest.Every(1))
			for size := 2; size <= 9; size++ {
				compare("reads of "+itoa(size)+" bytes", providertest.Every(size))
			}
			rng := rand.New(rand.NewSource(int64(len(body)))) // fixed: a failure repeats
			for i := 0; i < 200; i++ {
				var cuts []int
				for n := 2 + rng.Intn(12); n > 0; n-- {
					cuts = append(cuts, 1+rng.Intn(len(body)))
				}
				compare("cuts "+join(cuts), providertest.Cuts(cuts...))
			}
		})
	}
}

// A stream that stops before the endpoint said it was finished is a dropped
// connection, not a short answer: the agent retries the first and accepts the
// second as the model's whole reply. "Said it was finished" is [DONE] or a finish
// reason; the test finds where a stream does that with a parser of its own.
func TestTruncatedStreamsAreNeverMistakenForAnswers(t *testing.T) {
	for _, s := range streams {
		t.Run(s.name, func(t *testing.T) {
			a, body := adapter(s.capture), []byte(s.body)
			whole := a.Run(t, body, providertest.Whole)
			end := terminalEnd(t, s.body)
			for k := 0; k < len(body); k++ {
				o := a.Run(t, body[:k], providertest.Whole)
				providertest.Check(t, o)
				switch {
				case whole.Err != nil:
					// A stream that ends in an error is an error however much of it arrives... unless
					// the error itself is what is cut off, in which case it is a dropped connection.
					if o.Err == nil {
						t.Fatalf("prefix of %d bytes of a failing stream decoded as an answer: %q", k, o.Resp.Turn.PlainText())
					}
				case k < end:
					if o.Err == nil {
						t.Fatalf("prefix of %d bytes, %d short of the end of the stream, decoded as a complete answer: %q", k, end-k, o.Resp.Turn.PlainText())
					}
					if pe, _ := provider.AsError(o.Err); pe.Kind != provider.ErrNetwork || !pe.Retryable() {
						t.Fatalf("a dropped connection must be a retryable network error: %v", o.Err)
					}
				default:
					if o.Err != nil {
						t.Fatalf("prefix of %d bytes holds the whole answer (it ends at %d) but failed: %v", k, end, o.Err)
					}
				}
			}
		})
	}
}

// terminalEnd is the offset at which a stream has said that it is finished: the end
// of the first frame that carries a finish reason, or of the [DONE] line.
func terminalEnd(t *testing.T, body string) int {
	t.Helper()
	off := 0
	for _, line := range strings.SplitAfter(body, "\n") {
		off += len(line)
		payload, found := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data:")
		if !found {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			return off - len(line) + len(strings.TrimRight(line, "\r\n"))
		}
		var f struct {
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &f) != nil {
			continue
		}
		for _, c := range f.Choices {
			if c.FinishReason != nil && *c.FinishReason != "" {
				return off - len(line) + len(strings.TrimRight(line, "\r\n"))
			}
		}
	}
	t.Fatalf("stream has no terminal frame")
	return 0
}

func itoa(n int) string { return strconv.Itoa(n) }

func join(ns []int) string {
	var sb strings.Builder
	for i, n := range ns {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(itoa(n))
	}
	return sb.String()
}

// FuzzStream feeds arbitrary bytes through the real request path (Client.Do over a
// scripted transport) and asserts what an adapter owes its caller whatever the
// endpoint sends: no panic, no hang, a *provider.Error of a defined kind with an
// inert bounded message when it fails, a well-formed response when it does not
// (providertest.Check), and the same outcome however the transport cuts the bytes
// up. A connection that breaks after any number of bytes must end in a contract
// outcome as well.
func FuzzStream(f *testing.F) {
	// The recorded streams themselves are the seed corpus under testdata/fuzz/FuzzStream;
	// here are the same with CRLF line ends, and frames no endpoint should send.
	for _, s := range streams {
		f.Add([]byte(crlf(s.body)), uint16(len(s.body)/2), s.capture)
	}
	for _, in := range []string{
		"", "\n", "data:", "data: [DONE]", "data: {}\n\n", ": ping\n\n", "event: error\ndata: upstream timed out\n\n",
		"data: {\"error\":{\"message\":{\"detail\":1},\"code\":500}}\n\n", "data: {\"error\":\"boom\"}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":5}}]}\n\ndata: [DONE]\n\n",
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":-1,\"function\":{\"arguments\":\"{\"}}]}}]}\n\ndata: [DONE]\n\n",
		"data: {\"choices\":[{\"delta\":{\"reasoning_details\":[{\"index\":-7,\"data\":\"a\"}]}}]}\n\ndata: [DONE]\n\n",
		"data: {\"usage\":{\"prompt_tokens\":\"Infinity\",\"completion_tokens\":-1,\"cost\":1e999}}\n\ndata: [DONE]\n\n",
		"data: {\"id\":\"\x1b]52;c;AAAA\x07\",\"model\":\"m\xff\",\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\n",
	} {
		f.Add([]byte(in), uint16(3), false)
	}
	f.Fuzz(func(t *testing.T, in []byte, cut uint16, capture bool) {
		a := adapter(capture)
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
