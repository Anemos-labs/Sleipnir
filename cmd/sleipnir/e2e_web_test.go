package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
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
	for _, want := range []string{"sessions started in the page work in " + physicalDir(w.project), "Ctrl-C to stop", "token for this run only"} {
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

// A fake browser opener records the address it is started with. --open must hand it a launch code that works once, not the run token.
func TestWebOpenHandsTheOpenerASingleUseCodeNotTheToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("an interrupt cannot be sent to a process on Windows")
	}
	w := newWorld(t, "")
	bin := filepath.Join(w.tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	argv := filepath.Join(w.tmp, "opened.txt")
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" > '" + argv + ".part' && mv '" + argv + ".part' '" + argv + "'\n"
	if err := os.WriteFile(filepath.Join(bin, opener), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	w.extra = append(w.extra, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := w.startWeb(t, "--open")
	var opened string
	deadline := time.Now().Add(e2eGuard)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(argv); err == nil {
			opened = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if opened == "" {
		t.Fatalf("the opener was not started:\n%s", r.stderr.String())
	}
	m := regexp.MustCompile(`^http://127\.0\.0\.1:([1-9]\d*)/\?token=([A-Za-z0-9_-]{20,})$`).FindStringSubmatch(opened)
	if m == nil || "http://127.0.0.1:"+m[1] != r.base {
		t.Fatalf("the opener got %q, want the address of %s with a code", opened, r.base)
	}
	code := m[2]
	if code == r.token || strings.Contains(opened, r.token) {
		t.Fatalf("the opener's argument list holds the run token: %s", opened)
	}
	hc := noFollow()
	get := func(path string, hdr map[string]string) *http.Response {
		req, _ := http.NewRequest("GET", r.base+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	// As a bearer credential it is worth nothing.
	if resp := get("/api/ping", map[string]string{"Authorization": "Bearer " + code}); resp.StatusCode != 401 {
		t.Errorf("the launch code as a bearer token = %d", resp.StatusCode)
	}
	// It opens one session on the page URL ...
	resp := get("/?token="+code, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" || len(resp.Cookies()) != 1 {
		t.Fatalf("the launch code = %d, Location %q, %d cookies", resp.StatusCode, resp.Header.Get("Location"), len(resp.Cookies()))
	}
	// ... and then it is spent, while the run token is untouched.
	if resp := get("/?token="+code, nil); resp.StatusCode != 401 {
		t.Errorf("the launch code twice = %d", resp.StatusCode)
	}
	if resp := get("/?token="+r.token, nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("the run token after the launch code = %d", resp.StatusCode)
	}
	r.stop()
	if strings.Contains(r.stderr.String(), code) || strings.Contains(r.stderr.String(), r.token) {
		t.Errorf("a credential is on standard error:\n%s", r.stderr.String())
	}
}

// signIn opens the printed address and returns the session cookie it was exchanged for, as a Cookie header value.
func (r *runningWeb) signIn() string {
	r.t.Helper()
	resp, err := noFollow().Get(r.url)
	if err != nil {
		r.t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		return c.Name + "=" + c.Value
	}
	r.t.Fatalf("no cookie from %d", resp.StatusCode)
	return ""
}

// api is a request of the page: the cookie, and for a write its origin, the custom header and JSON.
func (r *runningWeb) api(cookie, method, path string, body any, hdr ...string) (int, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.base+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", r.base)
		req.Header.Set("X-Sleipnir-Web", "1")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := noFollow().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// pageStream opens the page's stream with the cookie from the hello's id.
func (r *runningWeb) pageStream(cookie string, after uint64) *frameLog {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, r.base+"/api/stream?after="+strconv.FormatUint(after, 10), nil)
	req.Header.Set("Cookie", cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		r.t.Fatalf("stream = %d", resp.StatusCode)
	}
	r.t.Cleanup(func() { resp.Body.Close() })
	return readFrames(r.t, resp.Body)
}

// noTokenIn fails when a file under dir holds the run token or the cookie's value.
func noTokenIn(t *testing.T, dir string, secrets ...string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err != nil || fi.Size() > 8<<20 || !fi.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, s := range secrets {
			if s != "" && bytes.Contains(b, []byte(s)) {
				t.Errorf("%s holds a credential of the server", p)
			}
		}
		return nil
	})
}

// The shop fixture: a team on the mock model whose frontend asks to install a package. The page signs in with the printed address,
// opens the stream from the hello's id, sees the question, is refused an answer in the instant it appeared, answers after the floor,
// and the worker goes on. No credential of the server reaches a file of the state directory or the project.
func TestWebFixtureShopAsksAndTakesTheAnswer(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the shop fixture needs git")
	}
	w := newWorld(t, "")
	r := w.startWeb(t, "--fixture", "shop")
	cookie := r.signIn()
	if code, _ := r.api("", "GET", "/api/hello", nil); code != 401 {
		t.Errorf("hello without the cookie = %d", code)
	}
	code, body := r.api(cookie, "GET", "/api/hello", nil)
	var hello struct {
		Tabs        []wire.TabSummary
		StreamAfter uint64
	}
	if code != 200 || json.Unmarshal(body, &hello) != nil || len(hello.Tabs) != 1 {
		t.Fatalf("hello = %d %s", code, body)
	}
	tab := hello.Tabs[0].ID
	frames := r.pageStream(cookie, hello.StreamAfter)
	if code, _ := r.api(cookie, "GET", "/api/sessions/"+tab+"/snapshot", nil); code != 200 {
		t.Errorf("snapshot = %d", code)
	}
	ask := frames.waitEv(tab, "ask", nil)
	q := ask["q"].(map[string]any)
	if q["agent"] != "fe-1" || fmt.Sprint(q["cmd"]) != "npm install --save-dev vitest" || q["kind"] != "command" || !strings.HasPrefix(fmt.Sprint(q["cwd"]), "worktree ") {
		t.Fatalf("the question: %v (the command whole, and the worker's tree named rather than its absolute path)", q)
	}
	qid := fmt.Sprint(q["id"])
	// An answer sent the moment the question is seen is too soon (the floor is 350 ms; the host's unit tests hold it to the
	// millisecond with a longer one). On a machine loaded enough to take longer than that, it is simply the answer.
	code, body = r.api(cookie, "POST", "/api/questions/"+qid+"/answer", wire.AnswerRequest{Choice: 1})
	switch {
	case code == 409 && strings.Contains(string(body), "too_soon"):
		time.Sleep(400 * time.Millisecond)
		if code, body := r.api(cookie, "POST", "/api/questions/"+qid+"/answer", wire.AnswerRequest{Choice: 1}); code != 200 {
			t.Fatalf("the answer after the floor = %d %s", code, body)
		}
	case code != 200:
		t.Fatalf("the answer = %d %s", code, body)
	}
	if code, _ := r.api(cookie, "POST", "/api/questions/"+qid+"/answer", wire.AnswerRequest{Choice: 1}); code != 409 {
		t.Errorf("a second answer = %d", code)
	}
	frames.waitEv(tab, "answer", map[string]any{"qid": qid, "by": "you", "choice": 1})
	frames.waitEv(tab, "state", map[string]any{"id": "fe-1"})
	out := r.stop()
	if out != "" {
		t.Errorf("standard output after the address: %q", out)
	}
	noTokenIn(t, w.state, r.token, strings.TrimPrefix(cookie, "sleipnir_web="))
	noTokenIn(t, w.project, r.token)
}

// The orders fixture: a single agent's turn reaches the page; a restart as a team starts a new generation; a second tab opens in a
// project of the list; the last tab cannot be closed; Ctrl-C ends the server with status 0.
func TestWebFixtureOrdersTurnRestartAndClose(t *testing.T) {
	w := newWorld(t, "")
	r := w.startWeb(t, "--fixture", "orders")
	cookie := r.signIn()
	_, body := r.api(cookie, "GET", "/api/hello", nil)
	var hello struct {
		Tabs        []wire.TabSummary
		StreamAfter uint64
	}
	if err := json.Unmarshal(body, &hello); err != nil || len(hello.Tabs) != 1 {
		t.Fatalf("hello %s", body)
	}
	tab := hello.Tabs[0].ID
	frames := r.pageStream(cookie, 0)
	frames.waitEv(tab, "say", map[string]any{"who": "you", "text": "add pagination to /orders"})
	frames.waitEv(tab, "final", nil)
	if code, body := r.api(cookie, "POST", "/api/sessions/"+tab+"/messages", wire.MessageRequest{Text: "and the pagination docs"}); code != 200 {
		t.Fatalf("message = %d %s", code, body)
	}
	frames.waitEv(tab, "say", map[string]any{"who": "you", "text": "and the pagination docs"})
	two := 2
	if code, body := r.api(cookie, "POST", "/api/sessions/"+tab+"/restart", wire.RestartRequest{Kind: "swarm", Swarm: &two, Fresh: true}); code != 202 {
		t.Fatalf("restart = %d %s", code, body)
	}
	frames.waitFor("reset to gen 2", func(f frame) bool { return f.event == "reset" && strings.Contains(string(f.data), `"gen":2`) })
	frames.waitFor("meta of a team of 2", func(f frame) bool { return f.event == "meta" && strings.Contains(string(f.data), `"swarm":2`) })
	zero := 0
	code, body := r.api(cookie, "POST", "/api/sessions", wire.NewSessionRequest{Cwd: physicalDir(w.project), Swarm: &zero})
	if code != 201 {
		t.Fatalf("a new session = %d %s", code, body)
	}
	if code, _ := r.api(cookie, "POST", "/api/sessions", wire.NewSessionRequest{Cwd: w.tmp}); code != 403 {
		t.Errorf("a directory that is not a project = %d", code)
	}
	var created struct{ Tab wire.TabSummary }
	_ = json.Unmarshal(body, &created)
	if code, body := r.api(cookie, "DELETE", "/api/sessions/"+tab, nil); code != 200 {
		t.Errorf("closing the first tab = %d %s", code, body)
	}
	if code, body := r.api(cookie, "DELETE", "/api/sessions/"+created.Tab.ID, nil); code != 409 || !strings.Contains(string(body), "last") {
		t.Errorf("closing the last tab = %d %s", code, body)
	}
	r.stop()
	if strings.Contains(r.stderr.String(), r.token) {
		t.Error("the token is on standard error")
	}
}

// physicalDir is dir with its symbolic links resolved, the spelling the server's own working directory has (a process that starts in a
// directory reaches it by its real path: on macOS the temporary directory is a link, /var -> /private/var).
func physicalDir(dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}
