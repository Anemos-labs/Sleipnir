package provider

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ReachTimeout bounds Reach: long enough for a TLS handshake through a proxy, short enough that a person who just started a
// session hears about an endpoint that cannot be reached before they have typed their first message.
const ReachTimeout = 8 * time.Second

// Reach reports whether the endpoint at baseURL can be reached from here: a GET of the base URL, without a key, through the
// proxy of the environment the adapters use. Any answer at all, whatever its status, is reachable (nil); a failure to get one
// is the *Error the first model request would have met (a proxy that refuses the host, a name that does not resolve, a
// refused connection). A base URL that is not http or https is not checked.
func Reach(ctx context.Context, hc *http.Client, baseURL string) *Error {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil
	}
	if hc == nil {
		tr := &http.Transport{Proxy: ProxyFromEnvironment, OnProxyConnectResponse: CheckProxyConnect, TLSHandshakeTimeout: ReachTimeout}
		defer tr.CloseIdleConnections()
		hc = &http.Client{Transport: tr}
	}
	// Redirects are not followed: an answer that sends the client elsewhere is an answer.
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	rctx, cancel := context.WithTimeout(ctx, ReachTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil
	}
	res, err := c.Do(req)
	if err != nil {
		return TransportError(ctx, err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	return nil
}
