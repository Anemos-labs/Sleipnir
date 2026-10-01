package openaichat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

func hang(w http.ResponseWriter, fl http.Flusher, r *http.Request) { <-r.Context().Done() }

func TestDefaultTimeouts(t *testing.T) {
	c := New(Config{})
	if c.cfg.FirstByteTimeout != 120*time.Second || c.cfg.StreamIdleTimeout != 60*time.Second || c.cfg.RequestTimeout != 10*time.Minute {
		t.Fatalf("defaults: first byte %v, idle %v, request %v", c.cfg.FirstByteTimeout, c.cfg.StreamIdleTimeout, c.cfg.RequestTimeout)
	}
	// Setting only the idle timeout bounds the wait for the first byte by it too.
	c = New(Config{StreamIdleTimeout: 7 * time.Second})
	if c.cfg.FirstByteTimeout != 7*time.Second || c.cfg.StreamIdleTimeout != 7*time.Second {
		t.Fatalf("idle only: first byte %v, idle %v", c.cfg.FirstByteTimeout, c.cfg.StreamIdleTimeout)
	}
	// Both can be set, and a reasoning model gets a first byte longer than its idle gap.
	c = New(Config{FirstByteTimeout: 5 * time.Minute, StreamIdleTimeout: 30 * time.Second})
	if c.cfg.FirstByteTimeout != 5*time.Minute || c.cfg.StreamIdleTimeout != 30*time.Second {
		t.Fatalf("both: first byte %v, idle %v", c.cfg.FirstByteTimeout, c.cfg.StreamIdleTimeout)
	}
	// A non-positive value is "unset", never "expire at once".
	c = New(Config{FirstByteTimeout: -1, StreamIdleTimeout: -5, RequestTimeout: -1})
	if c.cfg.FirstByteTimeout != 120*time.Second || c.cfg.StreamIdleTimeout != 60*time.Second || c.cfg.RequestTimeout != 10*time.Minute {
		t.Fatalf("negative values: %+v", c.cfg)
	}
}

func TestASilentServerEndsTheRequestAtTheFirstByteDeadline(t *testing.T) {
	s := newScripted(t, hang)
	start := time.Now()
	_, err := call(t, s.URL, Config{FirstByteTimeout: 150 * time.Millisecond, StreamIdleTimeout: time.Hour}, nil)
	pe := wantProviderError(t, err, provider.ErrTimeout, true)
	if d := time.Since(start); d < 120*time.Millisecond || d > 3*time.Second {
		t.Errorf("took %v", d)
	}
	if !strings.Contains(pe.Message, "no response from the server within 150ms") {
		t.Errorf("message = %q", pe.Message)
	}
	waitGone(t, s) // the connection is closed, not abandoned
}

func TestIdleTimeoutAloneBoundsTheWaitForTheFirstByte(t *testing.T) {
	s := newScripted(t, hang)
	start := time.Now()
	_, err := call(t, s.URL, Config{StreamIdleTimeout: 150 * time.Millisecond}, nil)
	wantProviderError(t, err, provider.ErrTimeout, true)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}

// Response headers are not the first byte: a server that sends headers at once and
// then thinks for a while is held to the first-byte deadline, not the idle one.
func TestHeadersAloneDoNotEndTheFirstByteWait(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		fl.Flush()
		time.Sleep(350 * time.Millisecond) // longer than idle (100ms), shorter than first byte (2s)
		fmt.Fprint(w, frame("answer"), finish("stop"))
	})
	resp, err := call(t, s.URL, Config{FirstByteTimeout: 2 * time.Second, StreamIdleTimeout: 100 * time.Millisecond}, nil)
	if err != nil || resp.Turn.PlainText() != "answer" {
		t.Fatalf("a slow start inside the first-byte deadline must work: %v %v", resp, err)
	}
	// ...and it is still bounded.
	s2 := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		fl.Flush()
		<-r.Context().Done()
	})
	start := time.Now()
	_, err = call(t, s2.URL, Config{FirstByteTimeout: 250 * time.Millisecond, StreamIdleTimeout: time.Hour}, nil)
	pe := wantProviderError(t, err, provider.ErrTimeout, true)
	if d := time.Since(start); d < 200*time.Millisecond || d > 3*time.Second || !strings.Contains(pe.Message, "no response") {
		t.Fatalf("headers then silence: took %v, %q", d, pe.Message)
	}
}

func TestAStreamThatGoesSilentAfterItsFirstByteIsAnIdleTimeout(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		fmt.Fprint(w, frame("partial"))
		fl.Flush()
		<-r.Context().Done()
	})
	start := time.Now()
	_, err := call(t, s.URL, Config{FirstByteTimeout: 30 * time.Second, StreamIdleTimeout: 150 * time.Millisecond}, nil)
	pe := wantProviderError(t, err, provider.ErrTimeout, true)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the idle deadline waited for the first-byte one: %v", d)
	}
	if !strings.Contains(pe.Message, "no data from the server for 150ms") || pe.NoRetry {
		t.Errorf("message = %q noretry=%v", pe.Message, pe.NoRetry)
	}
}

func TestKeepAliveCommentsCountAsActivity(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		for i := 0; i < 35; i++ { // 1.75s of comments, longer than the idle timeout (which is thirty times the gap between them,
			// so that a scheduling stall on a loaded machine cannot pass for silence)
			fmt.Fprint(w, ": PROCESSING\n\n")
			fl.Flush()
			time.Sleep(50 * time.Millisecond)
		}
		fmt.Fprint(w, frame("done"), finish("stop"))
	})
	resp, err := call(t, s.URL, Config{FirstByteTimeout: 1500 * time.Millisecond, StreamIdleTimeout: 1500 * time.Millisecond}, nil)
	if err != nil || resp.Turn.PlainText() != "done" {
		t.Fatalf("%v %v", resp, err)
	}
}

func TestASilentRequestIsNotRetriedForEver(t *testing.T) {
	s := newScripted(t, hang)
	c := New(Config{Name: "t", BaseURL: s.URL, FirstByteTimeout: 80 * time.Millisecond})
	req := &provider.Request{Prompt: secRevPrompt("hi")}
	// What the agent's retry loop does: the same *Request, again and again while the error is retryable.
	attempts := 0
	var last *provider.Error
	for attempts < 6 {
		attempts++
		_, err := c.Do(context.Background(), req, nil)
		last = wantProviderError(t, err, provider.ErrTimeout, attempts < provider.MaxSilentAttempts)
		if !last.Retryable() {
			break
		}
	}
	if attempts != provider.MaxSilentAttempts || !last.NoRetry {
		t.Fatalf("stopped after %d attempts (want %d): %+v", attempts, provider.MaxSilentAttempts, last)
	}
	// A different request to the same client is a fresh start.
	_, err := c.Do(context.Background(), &provider.Request{Prompt: secRevPrompt("other")}, nil)
	wantProviderError(t, err, provider.ErrTimeout, true)
}

func TestAnAnsweredRequestClearsTheSilenceCount(t *testing.T) {
	var n atomic.Int32
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		if n.Add(1) == 2 { // the second attempt is answered
			sse(w)
			fmt.Fprint(w, frame("ok"), finish("stop"))
			return
		}
		<-r.Context().Done()
	})
	c := New(Config{Name: "t", BaseURL: s.URL, FirstByteTimeout: 80 * time.Millisecond})
	req := &provider.Request{Prompt: secRevPrompt("hi")}
	if _, err := c.Do(context.Background(), req, nil); err == nil { // silent: count 1, retryable
		t.Fatal("expected a timeout")
	}
	if _, err := c.Do(context.Background(), req, nil); err != nil { // answered: count back to 0
		t.Fatal(err)
	}
	n.Store(2) // the attempts after this one are silent again
	_, err := c.Do(context.Background(), req, nil)
	wantProviderError(t, err, provider.ErrTimeout, true) // silent again, but only once since the last answer
}

func TestCancellationAndDeadlinesAreNotTheWatchdog(t *testing.T) {
	t.Run("the caller cancels while waiting", func(t *testing.T) {
		s := newScripted(t, hang)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(80*time.Millisecond, cancel)
		_, err := New(Config{Name: "t", BaseURL: s.URL, FirstByteTimeout: time.Minute}).Do(ctx, &provider.Request{Prompt: secRevPrompt("hi")}, nil)
		pe := wantProviderError(t, err, provider.ErrNetwork, true)
		if pe.Message != "request cancelled" || !errors.Is(err, context.Canceled) {
			t.Fatalf("%q", pe.Message)
		}
	})
	t.Run("the caller's deadline is a timeout", func(t *testing.T) {
		s := newScripted(t, hang)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := New(Config{Name: "t", BaseURL: s.URL, FirstByteTimeout: time.Minute}).Do(ctx, &provider.Request{Prompt: secRevPrompt("hi")}, nil)
		pe := wantProviderError(t, err, provider.ErrTimeout, true)
		if !errors.Is(err, context.DeadlineExceeded) || pe.NoRetry {
			t.Fatalf("%v", err)
		}
	})
	t.Run("the caller cancels mid-stream", func(t *testing.T) {
		s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sse(w)
			fmt.Fprint(w, frame("partial"))
			fl.Flush()
			<-r.Context().Done()
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := New(Config{Name: "t", BaseURL: s.URL, StreamIdleTimeout: time.Minute}).Do(ctx, &provider.Request{Prompt: secRevPrompt("hi")}, func(e provider.Event) {
			if e.Kind == provider.EvText {
				cancel()
			}
		})
		pe := wantProviderError(t, err, provider.ErrNetwork, true)
		if pe.Message != "request cancelled" {
			t.Fatalf("%q", pe.Message)
		}
		waitGone(t, s)
	})
}

func TestNonStreamingCallsHaveTheirOwnTimeout(t *testing.T) {
	s := newScripted(t, hang)
	req := &provider.Request{Prompt: secRevPrompt("hi"), NoStream: true}
	start := time.Now()
	_, err := New(Config{Name: "t", BaseURL: s.URL, RequestTimeout: 150 * time.Millisecond, StreamIdleTimeout: time.Hour, FirstByteTimeout: time.Hour}).Do(context.Background(), req, nil)
	wantProviderError(t, err, provider.ErrTimeout, true)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
	// A body that stalls halfway.
	s2 := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"g","choices":[`)
		fl.Flush()
		<-r.Context().Done()
	})
	_, err = New(Config{Name: "t", BaseURL: s2.URL, RequestTimeout: 150 * time.Millisecond}).Do(context.Background(), req, nil)
	wantProviderError(t, err, provider.ErrTimeout, true)
}

func TestWarmRequestsAreBoundedToo(t *testing.T) {
	s := newScripted(t, hang)
	_, err := New(Config{Name: "t", BaseURL: s.URL, RequestTimeout: 120 * time.Millisecond}).Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hi"), Warm: true}, nil)
	wantProviderError(t, err, provider.ErrTimeout, true)
}
