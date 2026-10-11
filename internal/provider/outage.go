package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
)

// Outage says how a failure bears on waiting for an endpoint: whether trying the same request again later, rather than at
// once, is what cures it. An agent waits out an outage for a while (agent.Config.OutagePatience) instead of giving up after
// the attempts every failure gets, and a rollout stops its wall clock while the policy endpoint is in one.
type Outage int

const (
	// NotOutage is a failure that waiting does not cure: the request is wrong (a status below 500 other than 429, a prompt
	// over the context window), the endpoint is misaddressed (a host name that does not resolve, a certificate that is not
	// accepted, a key that would cross the network in clear), the caller stopped, or the response itself failed (a stream
	// past a size limit, a body that does not parse).
	NotOutage Outage = iota
	// OutageAnswered is an endpoint that answered that it is down or overloaded: a status of 500 or more, or 429.
	OutageAnswered
	// OutageUnanswered is an endpoint that did not answer: the connection was refused, reset or cut before the response
	// ended, or no response came in time. An endpoint that went away fails this way, and so does one that was never there,
	// which only the caller can tell apart (agent.Config.WaitUnreachable).
	OutageUnanswered
)

// String returns the wire label of a defined class ("none", "answered", "unanswered").
func (o Outage) String() string {
	switch o {
	case OutageAnswered:
		return "answered"
	case OutageUnanswered:
		return "unanswered"
	}
	return "none"
}

// Outage classifies the failure; a nil error is NotOutage. A status decides when there is one. Without one, a network failure
// or a timeout is OutageUnanswered, the watchdog's "no response at all, again" included (Retryable says no to that one, but
// it is an endpoint that went silent), unless it is the caller's own cancellation or a sign of a wrong address.
func (e *Error) Outage() Outage {
	switch {
	case e == nil:
		return NotOutage
	case e.Status >= 500 || e.Status == http.StatusTooManyRequests:
		return OutageAnswered
	case e.Status != 0:
		return NotOutage
	case e.Kind == ErrTimeout:
		return OutageUnanswered
	case e.Kind != ErrNetwork || e.NoRetry || errors.Is(e.Err, context.Canceled) || misaddressed(e.Err):
		return NotOutage
	}
	return OutageUnanswered
}

// misaddressed reports whether a transport failure says the endpoint's address or identity is wrong rather than that it is
// away: a host name that does not exist, a certificate that is not accepted, a TLS handshake answered in plain text. Waiting
// does not change any of them.
func misaddressed(err error) bool {
	if err == nil {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsNotFound {
		return true
	}
	var (
		verify   *tls.CertificateVerificationError
		record   tls.RecordHeaderError
		unknown  x509.UnknownAuthorityError
		hostname x509.HostnameError
		invalid  x509.CertificateInvalidError
	)
	return errors.As(err, &verify) || errors.As(err, &record) || errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid)
}
