package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxHTTPBody caps the response of an http hook.
const maxHTTPBody = 256 << 10

// postHook runs an http hook: the payload is POSTed as JSON and the response
// body is read as the hook's output, exactly like a command's stdout. A 2xx
// status is success; any other status is a failure that does not block (a hook
// cannot block through a status code, as in Claude Code).
//
// The payload holds tool inputs and results, which may be sensitive, so http
// hooks are off unless Runner.AllowHTTP says otherwise, plain http is refused
// except to a loopback address, redirects are not followed (a 3xx would replay
// the payload to another host), and the response is size-capped.
func (r *Runner) postHook(ctx context.Context, h Hook, payload []byte, timeout time.Duration) outcome {
	start := time.Now()
	res := outcome{exit: -1}
	u, err := url.Parse(h.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		res.startErr = errors.New("the URL is not an absolute http or https URL")
		return res
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		res.startErr = errors.New("plain http is only allowed to a loopback address; use https")
		return res
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, h.URL, bytes.NewReader(payload))
	if err != nil {
		res.startErr = err
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "sleipnir-hooks")
	for k, v := range h.Headers {
		req.Header.Set(k, v)
	}
	client := r.HTTPClient
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		switch {
		case errors.Is(cctx.Err(), context.DeadlineExceeded):
			res.timedOut = true
		case ctx.Err() != nil:
			res.canceled = true
		default:
			res.startErr = fmt.Errorf("request failed: %s", oneLine(errText(err)))
		}
		res.dur = time.Since(start)
		return res
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
	if err != nil {
		res.startErr = fmt.Errorf("reading the response failed: %s", oneLine(errText(err)))
		res.dur = time.Since(start)
		return res
	}
	if len(body) > maxHTTPBody {
		body, res.stdoutCut = body[:maxHTTPBody], true
	}
	res.dur = time.Since(start)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		res.exit, res.stdout = 0, body
		return res
	}
	res.exit = 1
	res.stderr = []byte(fmt.Sprintf("HTTP status %d: %s", resp.StatusCode, oneLine(string(body[:min(len(body), 300)]))))
	return res
}

// errText is an error's text without the URL net/http adds ("Post
// \"https://...\": ..."), which may carry a token in its query string.
func errText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
