package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ProxyRefusedError is an HTTPS proxy's answer other than 200 to the CONNECT that opens a tunnel to the endpoint. Go's transport
// reports such an answer as a bare "Forbidden", which reads as a dropped connection; this keeps the proxy, the host it was asked
// for and the status, so the failure is classified as what it is: the network in between said no.
type ProxyRefusedError struct {
	Proxy  string // the proxy's host:port, without credentials
	Target string // the host:port the tunnel was asked for
	Status int    // the status of the proxy's answer
}

// Error says which proxy refused which host, and with what status.
func (e *ProxyRefusedError) Error() string {
	return fmt.Sprintf("the proxy %s refused a connection to %s (%d %s)", e.Proxy, e.Target, e.Status, http.StatusText(e.Status))
}

// Permanent reports whether repeating the request through the same proxy cannot help: every refusal except the proxy's own
// gateway failures (502, 503, 504), which say the upstream did not answer it this time.
func (e *ProxyRefusedError) Permanent() bool {
	return e.Status != http.StatusBadGateway && e.Status != http.StatusServiceUnavailable && e.Status != http.StatusGatewayTimeout
}

// CheckProxyConnect is an http.Transport OnProxyConnectResponse hook: a CONNECT answered with anything but 200 becomes a
// *ProxyRefusedError, which TransportError turns into a failure that is not retried when it is permanent.
func CheckProxyConnect(_ context.Context, proxyURL *url.URL, req *http.Request, res *http.Response) error {
	if res.StatusCode == http.StatusOK {
		return nil
	}
	e := &ProxyRefusedError{Status: res.StatusCode}
	if proxyURL != nil {
		e.Proxy = proxyURL.Host
	}
	if req != nil && req.URL != nil {
		e.Target = req.URL.Host
	}
	if e.Target == "" && req != nil {
		e.Target = req.Host
	}
	return e
}

// proxyRefusal is the *Error of a refused CONNECT: an auth failure when the proxy wants credentials (407), a network failure
// otherwise; either way not retried when the refusal is permanent, with words that point at the proxy's rules.
func proxyRefusal(pe *ProxyRefusedError, err error) *Error {
	msg := pe.Error()
	kind := ErrNetwork
	switch {
	case pe.Status == http.StatusProxyAuthRequired:
		kind = ErrAuth
		msg += "; the proxy wants credentials (HTTPS_PROXY with user:password, or the proxy's own setup)"
	case pe.Permanent():
		msg += "; the proxy does not allow " + strings.TrimSuffix(pe.Target, ":443") + " (allow the host in the proxy's rules, unset HTTPS_PROXY if it should not apply, or use another endpoint)"
	}
	return &Error{Kind: kind, Message: SanitizeText(msg, 0), Err: err, NoRetry: pe.Permanent()}
}
