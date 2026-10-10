package main

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// webURL is what the first line of `sleipnir web` looks like: the bound port on loopback and a token of 256 bits.
var webURL = regexp.MustCompile(`^http://127\.0\.0\.1:([1-9]\d*)/\?token=([A-Za-z0-9_-]{43})$`)

// runningWeb is a `sleipnir web` process that has printed its address.
type runningWeb struct {
	t      *testing.T
	cmd    *exec.Cmd
	url    string // the line that was printed
	base   string // http://127.0.0.1:PORT
	token  string
	stderr *bytes.Buffer
	done   chan error
	rest   chan string // standard output after the first line, once the process has ended
}

// startWeb starts the command with --addr 127.0.0.1:0 and the given extra flags and waits for the address.
func (w *world) startWeb(t *testing.T, args ...string) *runningWeb {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("an interrupt cannot be sent to a process on Windows")
	}
	c := w.cmd(append([]string{"web", "--addr", "127.0.0.1:0"}, args...)...)
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	r := &runningWeb{t: t, cmd: c, stderr: &bytes.Buffer{}, done: make(chan error, 1), rest: make(chan string, 1)}
	c.Stderr = r.stderr
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill() })
	br := bufio.NewReader(stdout)
	lines := make(chan string, 1)
	go func() {
		line, _ := br.ReadString('\n')
		lines <- strings.TrimRight(line, "\r\n")
		rest, _ := io.ReadAll(br)
		r.rest <- string(rest)
	}()
	go func() { r.done <- c.Wait() }()
	select {
	case r.url = <-lines:
	case <-time.After(e2eGuard):
		t.Fatalf("sleipnir web printed no address in %v:\n%s", e2eGuard, r.stderr.String())
	}
	m := webURL.FindStringSubmatch(r.url)
	if m == nil {
		t.Fatalf("the first line is %q: want the bound port on loopback and a 256-bit token", r.url)
	}
	r.base, r.token = "http://127.0.0.1:"+m[1], m[2]
	return r
}

// stop sends an interrupt and returns what the process wrote to standard output after the first line.
func (r *runningWeb) stop() string {
	r.t.Helper()
	if err := r.cmd.Process.Signal(os.Interrupt); err != nil {
		r.t.Fatal(err)
	}
	select {
	case err := <-r.done:
		if err != nil {
			r.t.Errorf("sleipnir web after an interrupt: %v\n%s", err, r.stderr.String())
		}
	case <-time.After(e2eGuard):
		r.t.Fatalf("sleipnir web did not stop after an interrupt:\n%s", r.stderr.String())
	}
	select {
	case s := <-r.rest:
		return s
	case <-time.After(e2eGuard):
		r.t.Fatal("standard output did not end")
		return ""
	}
}

// noFollow is an HTTP client that shows redirects instead of following them.
func noFollow() *http.Client {
	return &http.Client{Timeout: e2eGuard, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestWebServesTheInterfaceBehindATokenAndStopsOnInterrupt(t *testing.T) {
	w := newWorld(t, "")
	r := w.startWeb(t)
	hc := noFollow()
	do := func(method, path string, hdr map[string]string, body string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(method, r.base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			if k == "Host" {
				req.Host = v
				continue
			}
			req.Header.Set(k, v)
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	headers := func(resp *http.Response, label string) {
		t.Helper()
		csp := resp.Header.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "font-src 'self'", "connect-src 'self'", "frame-ancestors 'none'", "form-action 'none'", "base-uri 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", label, csp, want)
			}
		}
		for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Cross-Origin-Resource-Policy": "same-origin"} {
			if got := resp.Header.Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", label, k, got, v)
			}
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: the API is same-origin only", label)
		}
	}

	// Nothing is served without the token, and the health probe says nothing else.
	resp, body := do("GET", "/", map[string]string{"Accept": "text/html"}, "")
	if resp.StatusCode != 401 || strings.Contains(body, r.token) {
		t.Errorf("GET / without a token = %d", resp.StatusCode)
	}
	headers(resp, "401")
	if resp, body := do("GET", "/healthz", nil, ""); resp.StatusCode != 200 || strings.TrimSpace(body) != `{"ok":true}` {
		t.Errorf("GET /healthz = %d %q", resp.StatusCode, body)
	}
	if resp, _ := do("GET", "/api/ping?token="+r.token, nil, ""); resp.StatusCode != 401 {
		t.Errorf("the token in an API URL = %d: it is accepted on / only", resp.StatusCode)
	}

	// The printed address signs the browser in and sends it to an address without the token.
	resp, _ = do("GET", "/?token="+r.token, nil, "")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("GET /?token= = %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	headers(resp, "redirect")
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		cookie = c
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || strings.Contains(cookie.Value, r.token) {
		t.Fatalf("session cookie = %+v", cookie)
	}
	session := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	page, body := do("GET", "/", session, "")
	if page.StatusCode != 200 || !strings.HasPrefix(page.Header.Get("Content-Type"), "text/html") || !strings.Contains(body, "<html") {
		t.Errorf("GET / with the session = %d %s %.60q", page.StatusCode, page.Header.Get("Content-Type"), body)
	}
	headers(page, "page")
	// The page's own files come out of the binary.
	if css := regexp.MustCompile(`href="([^"]+\.css)"`).FindStringSubmatch(body); css != nil {
		if resp, b := do("GET", "/"+strings.TrimPrefix(css[1], "/"), session, ""); resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") || len(b) == 0 {
			t.Errorf("GET %s = %d %s", css[1], resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	if resp, _ := do("GET", "/no/such/file.js", session, ""); resp.StatusCode != 404 {
		t.Errorf("a missing file = %d", resp.StatusCode)
	}

	// The API, by cookie and by bearer token (a script).
	ping, body := do("GET", "/api/ping", session, "")
	if ping.StatusCode != 200 || strings.TrimSpace(body) != `{"ok":true}` || !strings.HasPrefix(ping.Header.Get("Content-Type"), "application/json") {
		t.Errorf("GET /api/ping = %d %q", ping.StatusCode, body)
	}
	headers(ping, "api")
	bearer := map[string]string{"Authorization": "Bearer " + r.token}
	if resp, _ := do("GET", "/api/ping", bearer, ""); resp.StatusCode != 200 {
		t.Errorf("bearer = %d", resp.StatusCode)
	}
	if resp, _ := do("GET", "/api/ping", map[string]string{"Authorization": "Bearer " + r.token + "x"}, ""); resp.StatusCode != 401 {
		t.Errorf("wrong bearer = %d", resp.StatusCode)
	}
	if resp, _ := do("GET", "/api/ping", map[string]string{"Host": "evil.example"}, ""); resp.StatusCode != 403 {
		t.Errorf("a rebound Host = %d", resp.StatusCode)
	}
	if resp, _ := do("GET", "/api/nothing", session, ""); resp.StatusCode != 404 {
		t.Errorf("an unknown API path = %d", resp.StatusCode)
	}

	// Writes: the page's own origin, the custom header and JSON; nothing from anywhere else.
	own := map[string]string{"Cookie": session["Cookie"], "Origin": r.base, "X-Sleipnir-Web": "1", "Content-Type": "application/json"}
	if resp, _ := do("POST", "/api/ping", own, "{}"); resp.StatusCode != 405 {
		t.Errorf("POST to a GET route = %d", resp.StatusCode)
	}
	if resp, _ := do("POST", "/api/confirm", own, `{"scope":"trust add"}`); resp.StatusCode != 200 {
		t.Errorf("POST /api/confirm from the page = %d", resp.StatusCode)
	}
	evil := map[string]string{"Cookie": session["Cookie"], "Origin": "http://evil.example", "X-Sleipnir-Web": "1", "Content-Type": "application/json"}
	if resp, _ := do("POST", "/api/confirm", evil, `{"scope":"x"}`); resp.StatusCode != 403 {
		t.Errorf("POST from another origin = %d", resp.StatusCode)
	}
	simple := map[string]string{"Cookie": session["Cookie"], "Origin": "http://evil.example", "Content-Type": "text/plain"}
	if resp, _ := do("POST", "/api/confirm", simple, `{"scope":"x"}`); resp.StatusCode != 403 {
		t.Errorf("a simple cross-origin POST = %d", resp.StatusCode)
	}
	otherPort := map[string]string{"Cookie": session["Cookie"], "Origin": "http://127.0.0.1:1", "X-Sleipnir-Web": "1", "Content-Type": "application/json"}
	if resp, _ := do("POST", "/api/confirm", otherPort, `{"scope":"x"}`); resp.StatusCode != 403 {
		t.Errorf("POST from another port of this host = %d", resp.StatusCode)
	}
	noType := map[string]string{"Cookie": session["Cookie"], "Origin": r.base, "X-Sleipnir-Web": "1"}
	if resp, _ := do("POST", "/api/confirm", noType, `{"scope":"x"}`); resp.StatusCode != 415 {
		t.Errorf("POST without a content type = %d", resp.StatusCode)
	}
	if resp, _ := do("POST", "/api/confirm", own, `{"scope":"x","extra":1}`); resp.StatusCode != 400 {
		t.Errorf("POST with an unknown field = %d", resp.StatusCode)
	}
	if resp, _ := do("POST", "/api/confirm", own, `{"scope":"`+strings.Repeat("x", 4096)+`"}`); resp.StatusCode != 413 {
		t.Errorf("POST over the body cap = %d", resp.StatusCode)
	}

	// Logging out ends the session.
	if resp, _ := do("POST", "/api/auth/logout", map[string]string{"Cookie": session["Cookie"], "Origin": r.base, "X-Sleipnir-Web": "1"}, ""); resp.StatusCode != 200 {
		t.Errorf("logout = %d", resp.StatusCode)
	}
	if resp, _ := do("GET", "/api/ping", session, ""); resp.StatusCode != 401 {
		t.Errorf("after logout = %d", resp.StatusCode)
	}

	// Ctrl-C stops the server, and that is a success.
	rest := r.stop()
	if rest != "" {
		t.Errorf("standard output after the address: %q: the address is the only thing on it", rest)
	}
	stderr := r.stderr.String()
	for _, want := range []string{"sessions started in the page work in " + w.project, "Ctrl-C to stop", "token for this run only"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, r.token) {
		t.Errorf("the token is on standard error:\n%s", stderr)
	}
	if _, err := hc.Get(r.base + "/healthz"); err == nil {
		t.Error("still serving after the interrupt")
	}
}

func TestWebTokenIsNewForEveryRunAndNeverTakenFromTheOutside(t *testing.T) {
	w := newWorld(t, "")
	w.extra = append(w.extra, "SLEIPNIR_WEB_TOKEN=chosen-by-the-environment", "SLEIPNIR_TOKEN=chosen-by-the-environment")
	first := w.startWeb(t)
	firstToken := first.token
	first.stop()
	second := w.startWeb(t)
	defer second.stop()
	if firstToken == second.token || strings.Contains(second.token, "chosen") {
		t.Errorf("tokens %q and %q: every run has its own, made by the process", firstToken, second.token)
	}
	// The first run's token means nothing to the second.
	req, _ := http.NewRequest("GET", second.base+"/api/ping", nil)
	req.Header.Set("Authorization", "Bearer "+firstToken)
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("an earlier run's token = %d", resp.StatusCode)
	}
	// There is no flag for it.
	assertRun(t, w.run("", "web", "--token", "abc"), 2, nil, []string{"flag provided but not defined: -token", "sleipnir web -h lists the flags", "!Usage"})
}

func TestWebAddressInUseNamesTheWayOut(t *testing.T) {
	w := newWorld(t, "")
	r := w.startWeb(t)
	defer r.stop()
	port := strings.TrimPrefix(r.base, "http://127.0.0.1:")
	assertRun(t, w.run("", "web", "--addr", "127.0.0.1:"+port), 1, []string{"!http://"}, []string{"already in use", "--addr 127.0.0.1:0", "127.0.0.1:" + port})
}

func TestWebRefusesWhatItShouldNotServe(t *testing.T) {
	w := newWorld(t, "")
	missing := filepath.Join(w.tmp, "missing")
	for _, tc := range []struct {
		name   string
		args   []string
		code   int
		stderr []string
	}{
		{"a wildcard address", []string{"web", "--addr", "0.0.0.0:0"}, 1, []string{"not a loopback address", "SSH"}},
		{"all interfaces", []string{"web", "--addr", ":0"}, 1, []string{"not a loopback address"}},
		{"a public address", []string{"web", "--addr", "192.0.2.1:0"}, 1, []string{"not a loopback address"}},
		{"a name that is not local", []string{"web", "--addr", "example.com:80"}, 1, []string{"not a loopback address"}},
		{"an address without a port", []string{"web", "--addr", "127.0.0.1"}, 1, []string{"bad address"}},
		{"an argument", []string{"web", "extra"}, 1, []string{"takes no arguments", "sleipnir web -h lists the flags"}},
		{"an unknown mode", []string{"web", "--mode", "yeet"}, 1, []string{"--mode must be one of default, accept-edits, plan, bypass, yolo"}},
		{"an unknown isolation", []string{"web", "--isolation", "docker"}, 1, []string{"--isolation must be one of none, worktree"}},
		{"a negative budget", []string{"web", "--budget-usd", "-1"}, 1, []string{"--budget-usd must not be negative"}},
		{"a negative team", []string{"web", "--swarm", "-2"}, 1, []string{"--swarm must not be negative"}},
		{"both resume flags", []string{"web", "--resume", "latest", "--continue"}, 1, []string{"--resume and --continue are alternatives"}},
		{"a working directory that is not one", []string{"web", "--cwd", missing}, 1, []string{"is not a directory"}},
		{"a malformed flag", []string{"web", "--swarm", "many"}, 2, []string{"invalid value", "swarm"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRun(t, w.run("", tc.args...), tc.code, []string{"!http://"}, tc.stderr)
		})
	}
}

func TestWebHelpListsTheChatFlagsItTakesAsDefaults(t *testing.T) {
	w := newWorld(t, "")
	r := w.run("", "web", "-h")
	assertRun(t, r, 0, nil, []string{
		"usage: sleipnir web [flags]", "token", "loopback", "SSH", "defaults of the sessions started in the page",
		"-addr", "-open", "-cwd", "-model", "-mode", "-swarm", "-budget-usd", "-isolation", "-verify", "-commit", "-mailman", "-role-model",
		"-allow", "-trust-project", "-no-mcp", "-resume", "-continue", "127.0.0.1:6969", "8 workers",
	})
	if strings.Contains(r.stderr, "-token") {
		t.Errorf("the help offers a --token flag:\n%s", r.stderr)
	}
	for _, line := range strings.Split(r.stderr, "\n") {
		if !strings.HasPrefix(line, " ") && len(line) > 78 {
			t.Errorf("a prose line of %d characters: %q", len(line), line)
		}
	}
}
