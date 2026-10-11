package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"
)

// A gateway that is down answers 502, 503 or 504, or nothing at all: a refused or reset connection, a stream cut halfway, a request
// that gets no byte back in time. Each of those is an outage that waiting cures. A request the endpoint refused, and an address that
// is wrong, are not: no wait makes a 400 succeed or a misspelt host resolve.
func TestOutageClassifiesWhatAWaitCures(t *testing.T) {
	post := func(err error) error {
		return &url.Error{Op: "Post", URL: "https://gw.example/v1/chat/completions", Err: err}
	}
	refused := post(&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)})
	reset := post(&net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)})
	slow := post(&net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}})
	httpErr := func(status int, body string) *Error { return HTTPError(status, http.Header{}, []byte(body)) }
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, c := range []struct {
		name string
		err  *Error
		want Outage
	}{
		{"nil", nil, NotOutage},
		{"bad gateway", httpErr(502, "<html>502 Bad Gateway</html>"), OutageAnswered},
		{"service unavailable", httpErr(503, `{"error":{"message":"Database is temporarily unavailable"}}`), OutageAnswered},
		{"gateway timeout", httpErr(504, ""), OutageAnswered},
		{"internal error", httpErr(500, ""), OutageAnswered},
		{"overloaded", httpErr(529, ""), OutageAnswered},
		{"rate limited", httpErr(429, ""), OutageAnswered},
		{"bad request", httpErr(400, `{"error":{"message":"invalid tool schema"}}`), NotOutage},
		{"context length", httpErr(400, `{"error":{"message":"maximum context length is 8192 tokens"}}`), NotOutage},
		{"unauthorized", httpErr(401, ""), NotOutage},
		{"forbidden", httpErr(403, ""), NotOutage},
		{"not found", httpErr(404, ""), NotOutage},
		{"request timeout", httpErr(408, ""), NotOutage},
		{"unprocessable", httpErr(422, ""), NotOutage},
		{"payment", httpErr(402, ""), NotOutage},
		{"connection refused", TransportError(context.Background(), refused), OutageUnanswered},
		{"connection reset", TransportError(context.Background(), reset), OutageUnanswered},
		{"dial timeout", TransportError(context.Background(), slow), OutageUnanswered},
		{"deadline of the request", TransportError(context.Background(), post(context.DeadlineExceeded)), OutageUnanswered},
		{"stream cut", &Error{Kind: ErrNetwork, Message: "the stream ended before [DONE] or a finish reason: the connection was cut"}, OutageUnanswered},
		{"no response from the server", &Error{Kind: ErrTimeout, Message: "no response from the server within 2m0s", Err: context.Canceled}, OutageUnanswered},
		{"no response from the server, again", &Error{Kind: ErrTimeout, Message: "no response (2 attempts; not retrying)", Err: context.Canceled, NoRetry: true}, OutageUnanswered},
		{"dns timeout", TransportError(context.Background(), post(&net.DNSError{Name: "gw.example", Err: "i/o timeout", IsTimeout: true})), OutageUnanswered},
		{"the caller cancelled", TransportError(cancelled, post(context.Canceled)), NotOutage},
		{"a host that does not exist", TransportError(context.Background(), post(&net.DNSError{Name: "gw.exmaple", Err: "no such host", IsNotFound: true})), NotOutage},
		{"an unknown certificate authority", TransportError(context.Background(), post(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}})), NotOutage},
		{"a certificate for another host", TransportError(context.Background(), post(x509.HostnameError{Host: "gw.example", Certificate: &x509.Certificate{}})), NotOutage},
		{"tls to a plain-text port", TransportError(context.Background(), post(tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"})), NotOutage},
		{"a key that would travel in clear", KeyTransportError(errors.New("refusing to send the key over http")), NotOutage},
		{"a response over a limit", LimitExceeded("stream size", 5), NotOutage},
		{"an in-band error with no status", &Error{Kind: ErrServer, Message: "error event: {}"}, NotOutage},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.err.Outage(); got != c.want {
				t.Errorf("%v: Outage() = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// The labels are what events and logs say.
func TestOutageLabels(t *testing.T) {
	for o, want := range map[Outage]string{NotOutage: "none", OutageAnswered: "answered", OutageUnanswered: "unanswered"} {
		if got := fmt.Sprint(o); got != want {
			t.Errorf("%d: %q, want %q", int(o), got, want)
		}
	}
}

// timeoutErr is a net.Error that timed out, as a dial that got no answer reports it.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
