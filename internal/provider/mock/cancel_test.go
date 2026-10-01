package mock

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A request whose client has gone away stops waiting for the time its reply would have taken. The mock models latency by waiting, and
// the demo that is left early (or a test that ends) closes the server, which waits for the requests it is serving: with a reply
// that was going to take a minute, that was a minute.
func TestACancelledRequestDoesNotHoldTheServer(t *testing.T) {
	reached := make(chan struct{}, 1)
	srv := New(Config{FirstToken: time.Hour}, func(c *Call) Reply {
		select {
		case reached <- struct{}{}:
		default:
		}
		return Reply{Text: "hi"}
	})
	ts := srv.Start()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-reached: // the server has the request and is about to wait for the first token
	case <-time.After(2 * time.Minute):
		t.Fatal("the server never saw the request")
	}
	cancel()
	<-sent

	closed := make(chan struct{})
	go func() {
		ts.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Minute):
		t.Fatal("the server is still waiting for the first token of a reply nobody is waiting for")
	}
}

func TestPauseEndsWhenTheContextDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		pause(ctx, time.Hour)
		pause(context.Background(), 0)
		pause(context.Background(), -time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("pause ignored a cancelled context")
	}
}
