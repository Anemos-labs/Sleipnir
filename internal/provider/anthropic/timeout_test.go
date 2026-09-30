package anthropic_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/anthropic"
)

const startEvent = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_slow\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"

const textStart = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"

const tail = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// slowServer runs script against every request; done is closed when a handler
// notices the client went away.
type slowServer struct {
	*httptest.Server
	gone chan struct{}
	hits atomic.Int32
}

func newSlow(t *testing.T, script func(w http.ResponseWriter, fl http.Flusher, r *http.Request)) *slowServer {
	t.Helper()
	s := &slowServer{gone: make(chan struct{}, 8)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		// Drain the request first: net/http only watches for a client disconnect
		// (and cancels r.Context()) once the body has been read to EOF.
		io.Copy(io.Discard, r.Body)
		fl, _ := w.(http.Flusher)
		script(w, fl, r)
		select {
		case <-r.Context().Done():
			s.gone <- struct{}{}
		default:
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func sseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
}

func TestStreamIdleTimeout(t *testing.T) {
	t.Run("a stream that goes silent after its first event", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			w.Write([]byte(startEvent))
			fl.Flush()
			<-r.Context().Done()
		})
		c := anthropic.New(anthropic.Config{BaseURL: s.URL, StreamIdleTimeout: 150 * time.Millisecond})
		start := time.Now()
		_, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
		pe := wantKind(t, err, provider.ErrTimeout, true)
		if d := time.Since(start); d < 100*time.Millisecond || d > 3*time.Second {
			t.Errorf("took %v", d)
		}
		if !strings.Contains(pe.Message, "no data") {
			t.Errorf("message = %q", pe.Message)
		}
		select {
		case <-s.gone:
		case <-time.After(3 * time.Second):
			t.Error("the connection must be closed when the watchdog fires")
		}
	})
	t.Run("pings keep a slow stream alive", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			w.Write([]byte(startEvent + textStart))
			fl.Flush()
			for i := 0; i < 12; i++ { // 12 x 40ms = 480ms of silence between real events, in 40ms steps
				time.Sleep(40 * time.Millisecond)
				w.Write([]byte("event: ping\ndata: {\"type\": \"ping\"}\n\n"))
				fl.Flush()
			}
			w.Write([]byte(tail))
		})
		c := anthropic.New(anthropic.Config{BaseURL: s.URL, StreamIdleTimeout: 150 * time.Millisecond})
		resp, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
		if err != nil || resp.Turn.PlainText() != "partial" {
			t.Fatalf("%v %+v", err, resp)
		}
	})
	t.Run("comment lines count as activity", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			w.Write([]byte(startEvent + textStart))
			for i := 0; i < 10; i++ {
				time.Sleep(40 * time.Millisecond)
				w.Write([]byte(": keep-alive\n"))
				fl.Flush()
			}
			w.Write([]byte("\n" + tail))
		})
		c := anthropic.New(anthropic.Config{BaseURL: s.URL, StreamIdleTimeout: 150 * time.Millisecond})
		if _, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a server that never answers", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) { <-r.Context().Done() })
		c := anthropic.New(anthropic.Config{BaseURL: s.URL, StreamIdleTimeout: 150 * time.Millisecond})
		start := time.Now()
		_, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
		wantKind(t, err, provider.ErrTimeout, true)
		if time.Since(start) > 3*time.Second {
			t.Errorf("took %v", time.Since(start))
		}
	})
	t.Run("a non-streaming call has its own timeout", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) { <-r.Context().Done() })
		c := anthropic.New(anthropic.Config{BaseURL: s.URL, RequestTimeout: 150 * time.Millisecond, StreamIdleTimeout: time.Hour})
		start := time.Now()
		_, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m"), NoStream: true}, nil)
		wantKind(t, err, provider.ErrTimeout, true)
		if time.Since(start) > 3*time.Second {
			t.Errorf("took %v", time.Since(start))
		}
	})
	t.Run("a non-streaming body that stalls", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"msg_x","type":"message","content":[`))
			fl.Flush()
			<-r.Context().Done()
		})
		c := anthropic.New(anthropic.Config{BaseURL: s.URL, RequestTimeout: 150 * time.Millisecond})
		_, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m"), NoStream: true}, nil)
		wantKind(t, err, provider.ErrTimeout, true)
	})
}

func TestCancellation(t *testing.T) {
	t.Run("mid-stream", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			w.Write([]byte(startEvent + textStart))
			fl.Flush()
			<-r.Context().Done()
		})
		c := anthropic.New(anthropic.Config{BaseURL: s.URL, StreamIdleTimeout: time.Minute})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		start := time.Now()
		_, err := c.Do(ctx, &provider.Request{Prompt: hello("m")}, func(e provider.Event) {
			if e.Kind == provider.EvText {
				cancel()
			}
		})
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("cancellation took %v", d)
		}
		pe := wantKind(t, err, provider.ErrNetwork, true)
		if pe.Message != "request cancelled" || !errors.Is(err, context.Canceled) {
			t.Errorf("message = %q, is Canceled: %v", pe.Message, errors.Is(err, context.Canceled))
		}
		select {
		case <-s.gone:
		case <-time.After(3 * time.Second):
			t.Error("the server never saw the disconnect")
		}
	})
	t.Run("before the request is sent", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := anthropic.New(anthropic.Config{BaseURL: s.URL}).Do(ctx, &provider.Request{Prompt: hello("m")}, nil)
		pe := wantKind(t, err, provider.ErrNetwork, true)
		if pe.Message != "request cancelled" || s.hits.Load() != 0 {
			t.Errorf("message %q, hits %d", pe.Message, s.hits.Load())
		}
	})
	t.Run("while waiting for the response headers", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) { <-r.Context().Done() })
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(80*time.Millisecond, cancel)
		_, err := anthropic.New(anthropic.Config{BaseURL: s.URL, StreamIdleTimeout: time.Minute}).Do(ctx, &provider.Request{Prompt: hello("m")}, nil)
		pe := wantKind(t, err, provider.ErrNetwork, true)
		if pe.Message != "request cancelled" {
			t.Errorf("message = %q", pe.Message)
		}
	})
	t.Run("a deadline is a timeout, not a cancellation", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) { <-r.Context().Done() })
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := anthropic.New(anthropic.Config{BaseURL: s.URL, StreamIdleTimeout: time.Minute}).Do(ctx, &provider.Request{Prompt: hello("m")}, nil)
		wantKind(t, err, provider.ErrTimeout, true)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("errors.Is(err, DeadlineExceeded) = false: %v", err)
		}
	})
}

func TestNetworkFailures(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		_, err = anthropic.New(anthropic.Config{BaseURL: "http://" + addr}).Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
		wantKind(t, err, provider.ErrNetwork, true)
	})
	t.Run("the connection drops mid-stream", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			w.Write([]byte(startEvent + textStart))
			fl.Flush()
			hj, _ := w.(http.Hijacker)
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close() // no terminating chunk: an abrupt EOF
			}
		})
		_, err := anthropic.New(anthropic.Config{BaseURL: s.URL}).Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
		wantKind(t, err, provider.ErrNetwork, true)
	})
	t.Run("the connection drops before any header", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			hj, _ := w.(http.Hijacker)
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
			}
		})
		_, err := anthropic.New(anthropic.Config{BaseURL: s.URL}).Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
		wantKind(t, err, provider.ErrNetwork, true)
	})
	t.Run("a response body that is not a stream at all", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("<html>captive portal</html>"))
		})
		_, err := anthropic.New(anthropic.Config{BaseURL: s.URL}).Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
		wantKind(t, err, provider.ErrNetwork, true)
	})
}

func TestHugeResponsesAreBounded(t *testing.T) {
	// An error body from a hostile or broken server must not be read without limit.
	s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		w.WriteHeader(500)
		chunk := []byte(strings.Repeat("x", 1<<16))
		for i := 0; i < 64; i++ { // 4 MiB
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	_, err := anthropic.New(anthropic.Config{BaseURL: s.URL}).Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
	pe := wantKind(t, err, provider.ErrServer, true)
	if len(pe.Raw) > 1<<20 || len(pe.Message) > 600 {
		t.Errorf("raw %d bytes, message %d bytes", len(pe.Raw), len(pe.Message))
	}
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }
