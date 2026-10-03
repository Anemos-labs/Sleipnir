package provider

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A failure to reach the endpoint is worded for a person: the address and what to check, not Go's `Post "url": dial tcp ...: connect:
// connection refused`.
func TestAnEndpointThatCannotBeReachedIsSaidInPlainWords(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/v1/chat/completions", nil)
	_, derr := http.DefaultClient.Do(req)
	if derr == nil {
		t.Skip("something answered on a port that was just closed")
	}
	pe := TransportError(context.Background(), derr)
	if pe.Kind != ErrNetwork {
		t.Fatalf("kind %v", pe.Kind)
	}
	for _, want := range []string{"cannot connect to " + addr, "connection refused", "is the server running"} {
		if !strings.Contains(pe.Message, want) {
			t.Errorf("message %q lacks %q", pe.Message, want)
		}
	}
	if strings.Contains(pe.Message, "Post") || strings.Contains(pe.Message, "dial tcp") {
		t.Errorf("message %q still says Go's words", pe.Message)
	}

	// Exercise DNS error translation without depending on an external resolver's
	// timeout, proxy configuration, or response to the reserved .invalid domain.
	derr = &url.Error{Op: "Post", URL: "http://no-such-host.invalid/v1/x", Err: &net.DNSError{
		Name: "no-such-host.invalid", Err: "no such host", IsNotFound: true,
	}}
	pe = TransportError(context.Background(), derr)
	if !strings.Contains(pe.Message, "cannot find no-such-host.invalid") {
		t.Errorf("a host that does not exist: %q", pe.Message)
	}
}
