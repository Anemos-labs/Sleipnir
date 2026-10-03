package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// sseTransport is the legacy HTTP+SSE transport (MCP 2024-11-05), which many
// servers still speak.
//
// The client opens a GET on the SSE URL. The server's first event, "endpoint",
// names the URL to POST client messages to; every server message arrives as a
// "message" event on the same stream. There is no session header: the session
// is whatever the endpoint URL says.
//
// The endpoint is chosen by the server, so it is the one place a server can
// aim the client's requests, and its credentials, somewhere else. It must be
// on the same origin as the SSE URL, or the stream is abandoned.
type sseTransport struct {
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

	mu        sync.Mutex
	endpoint  *url.URL
	ready     chan struct{} // closed once the endpoint is known
	readyOnce sync.Once
	failCh    chan struct{} // closed when the stream ended
	failed    bool
	failErr   error // why it ended, for the errors of calls that were waiting
	closeOnce sync.Once
}

// NewSSETransport returns a legacy HTTP+SSE transport.
func NewSSETransport(o HTTPOptions) (Transport, error) {
	u, hdr, max, err := o.prepare()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &sseTransport{
		u: u, client: newHTTPClient(o.Net), hdr: hdr, max: max, redact: o.Redact, onDrop: o.OnDrop,
		ctx: ctx, cancel: cancel, ready: make(chan struct{}), failCh: make(chan struct{}),
	}, nil
}

// Start implements Transport: it opens the event stream.
func (t *sseTransport) Start(h Handler) error {
	t.h = h
	t.wg.Add(1)
	go t.stream()
	return nil
}

// fail records and signals the first legacy SSE failure and notifies the close handler unless its
// context was already cancelled.
func (t *sseTransport) fail(err error) {
	t.mu.Lock()
	first := !t.failed
	t.failed = true
	if first {
		t.failErr = err
	}
	t.mu.Unlock()
	if !first {
		return
	}
	close(t.failCh)
	if t.ctx.Err() == nil && t.h.Closed != nil {
		t.h.Closed(err)
	}
}

func (t *sseTransport) stream() {
	defer t.wg.Done()
	req, err := http.NewRequestWithContext(t.ctx, http.MethodGet, t.u.String(), nil)
	if err != nil {
		t.fail(errors.New("could not build request"))
		return
	}
	for k, v := range t.hdr {
		req.Header[k] = append([]string(nil), v...)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := t.client.Do(req)
	if err != nil {
		t.fail(fmt.Errorf("connecting to the event stream: %w", netError(t.redact, err)))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			t.fail(fmt.Errorf("server redirected to %s; redirects are not followed", redirectHost(resp)))
			return
		}
		t.fail(httpStatusError(resp.StatusCode))
		return
	}
	if ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); ct != "text/event-stream" {
		t.fail(fmt.Errorf("event stream has unexpected content type %q", clipForError(ct)))
		return
	}
	rd := newSSEReader(resp.Body, t.max)
	for {
		ev, err := rd.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errors.New("server closed the event stream")
			} else if !errors.Is(err, ErrMessageTooLarge) {
				err = fmt.Errorf("event stream failed: %w", netError(t.redact, err))
			}
			t.fail(err)
			return
		}
		switch ev.Event {
		case "endpoint":
			if err := t.setEndpoint(ev.Data); err != nil {
				t.fail(err)
				return
			}
		case "", "message":
			if t.h.Message != nil && ev.Data != "" {
				t.h.Message([]byte(ev.Data))
			}
		}
	}
}

// setEndpoint records the POST URL the server announced, after vetting it. Only
// the first announcement counts: a server that could re-point the client
// mid-session would defeat the same-origin check.
func (t *sseTransport) setEndpoint(data string) error {
	ref, err := url.Parse(strings.TrimSpace(data))
	if err != nil {
		return errors.New("server announced an invalid message endpoint")
	}
	ep := t.u.ResolveReference(ref)
	if !sameOrigin(t.u, ep) {
		return fmt.Errorf("server announced a message endpoint on another origin (%s); refusing to send requests there", redactURL(ep.String()))
	}
	t.readyOnce.Do(func() {
		t.mu.Lock()
		t.endpoint = ep
		t.mu.Unlock()
		close(t.ready)
	})
	return nil
}

// Send implements Transport. It waits for the endpoint announcement, so the
// first message (initialize) is bounded by the caller's startup timeout.
func (t *sseTransport) Send(ctx context.Context, msg []byte) error {
	select {
	case <-t.ready:
	case <-t.failCh:
		t.mu.Lock()
		defer t.mu.Unlock()
		return &closedError{cause: t.failErr}
	case <-t.ctx.Done():
		return &closedError{}
	case <-ctx.Done():
		return ctx.Err()
	}
	t.mu.Lock()
	ep := t.endpoint
	t.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.String(), bytes.NewReader(msg))
	if err != nil {
		return errors.New("could not build request")
	}
	for k, v := range t.hdr {
		req.Header[k] = append([]string(nil), v...)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return netError(t.redact, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return fmt.Errorf("server redirected to %s; redirects are not followed", redirectHost(resp))
	}
	return httpStatusError(resp.StatusCode)
}

// Close implements Transport.
func (t *sseTransport) Close() error {
	t.closeOnce.Do(func() {
		t.cancel()
		t.wg.Wait()
		t.client.CloseIdleConnections()
	})
	return nil
}
