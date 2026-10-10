package web

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestNonLoopbackAddressesAreRefused(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:6969", ":6969", "[::]:6969", "192.168.1.10:6969", "example.com:80", "10.0.0.1:0", "169.254.1.1:6969", "localhost.evil.example:6969"} {
		_, err := New(Config{Addr: addr})
		if err == nil || !strings.Contains(err.Error(), "loopback") || !strings.Contains(err.Error(), "SSH") {
			t.Errorf("New(%q): %v, want a refusal that explains loopback and offers a tunnel", addr, err)
		}
		if ln, err := Listen(addr, false); err == nil {
			ln.Close()
			t.Errorf("Listen(%q) succeeded", addr)
		}
		// Asking for remote access explicitly is what allows it; the token is still required (there is no way to turn it off).
		srv, err := New(Config{Addr: addr, AllowNonLoopback: true})
		if err != nil {
			t.Errorf("New(%q) with remote access: %v", addr, err)
			continue
		}
		if rec := doDirect(srv, "GET", "/api/ping", "x:1"); rec != 401 {
			t.Errorf("remote access without a token = %d", rec)
		}
	}
	for _, addr := range []string{"", "no-port", "127.0.0.1", "127.0.0.1:", "127.0.0.1:99999", "127.0.0.1:-1", "127.0.0.1:http", "[::1", "127.0.0.1:6969:1"} {
		if addr == "" {
			if _, err := New(Config{}); err != nil {
				t.Errorf("the default address: %v", err)
			}
			continue
		}
		if _, err := New(Config{Addr: addr}); err == nil {
			t.Errorf("New(%q) accepted a malformed address", addr)
		}
		if _, err := Listen(addr, false); err == nil {
			t.Errorf("Listen(%q) accepted a malformed address", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "127.5.5.5:0", "[::1]:0"} {
		ln, err := Listen(addr, false)
		if err != nil {
			if strings.Contains(addr, "::1") || strings.Contains(err.Error(), "can't assign requested address") {
				t.Logf("%s is not configured on this machine: %v", addr, err)
				continue
			}
			t.Errorf("Listen(%q): %v", addr, err)
			continue
		}
		ln.Close()
	}
	if ln, err := Listen("0.0.0.0:0", true); err != nil {
		t.Errorf("a wildcard listener with remote access: %v", err)
	} else {
		ln.Close()
	}
	if DefaultAddr != "127.0.0.1:6969" {
		t.Errorf("DefaultAddr = %s", DefaultAddr)
	}
}

// doDirect sends a request with a Host to the handler and returns the status.
func doDirect(srv *Server, method, target, host string) int {
	hr := httptest.NewRequest(method, target, nil)
	hr.Host = host
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, hr)
	return rec.Code
}

func TestServeIsTheThirdCheckOfTheLoopbackRule(t *testing.T) {
	srv, err := New(Config{Addr: "127.0.0.1:0", UI: testUI})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Hub().Close()
	wild, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skip("cannot listen on the wildcard address")
	}
	defer wild.Close()
	if err := srv.Serve(context.Background(), wild); err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Errorf("Serve on a wildcard socket: %v", err)
	}
}

func TestListenWhenTheAddressIsInUseNamesTheWayOut(t *testing.T) {
	first, err := Listen("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, err = Listen(first.Addr().String(), false)
	if err == nil {
		t.Fatal("two listeners on one address")
	}
	for _, want := range []string{"already in use", "--addr 127.0.0.1:0", first.Addr().String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if !isAddrInUse(errors.New("listen tcp 127.0.0.1:80: bind: address already in use")) || !isAddrInUse(errors.New("Only one usage of each socket address (protocol/network address/port) is normally permitted")) {
		t.Error("isAddrInUse misses a known message")
	}
	if isAddrInUse(errors.New("permission denied")) {
		t.Error("isAddrInUse accepts anything")
	}
}

func TestServeAFullSessionOverARealSocket(t *testing.T) {
	rg := newRig(t, nil)
	base := rg.serve()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: guard}
	// The browser opens the URL that was printed: token in the query, cookie and a clean address afterwards.
	u := rg.srv.URL(mustAddr(t, base))
	resp, err := client.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<title>t</title>") || strings.Contains(resp.Request.URL.String(), "token") {
		t.Fatalf("page = %d via %s: %.80s", resp.StatusCode, resp.Request.URL, body)
	}
	if cs := jar.Cookies(mustURL(t, base)); len(cs) != 1 || cs[0].Name != CookieName {
		t.Fatalf("cookies = %v", cs)
	}
	get := func(path string) *http.Response {
		r, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r
	}
	if get("/api/ping").StatusCode != 200 || get("/js/app.js").StatusCode != 200 || get("/app/route").StatusCode != 200 {
		t.Error("the signed-in browser cannot read the page or the API")
	}
	post := func(headers map[string]string, body string) int {
		r, _ := http.NewRequest("POST", base+"/api/echo/9", strings.NewReader(body))
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	page := map[string]string{"Origin": base, "Content-Type": "application/json", RequestHeader: "1"}
	if c := post(page, `{"name":"n"}`); c != 200 {
		t.Errorf("POST from the page = %d", c)
	}
	evil := map[string]string{"Origin": "http://evil.example", "Content-Type": "application/json", RequestHeader: "1"}
	if c := post(evil, `{"name":"n"}`); c != 403 {
		t.Errorf("POST from another origin = %d", c)
	}
	plain := map[string]string{"Origin": "http://evil.example", "Content-Type": "text/plain"}
	if c := post(plain, `{"name":"n"}`); c != 403 {
		t.Errorf("simple cross-origin POST = %d", c)
	}
	// A request that names a different host is refused over the wire as well.
	r, _ := http.NewRequest("GET", base+"/api/ping", nil)
	r.Host = "evil.example"
	if resp, err := client.Do(r); err != nil || resp.StatusCode != 403 {
		t.Errorf("rebound Host over the wire: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	// A client without the cookie gets nothing.
	if resp, err := http.Get(base + "/api/ping"); err != nil || resp.StatusCode != 401 {
		t.Errorf("anonymous = %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
}

func mustAddr(t testing.TB, base string) net.Addr {
	t.Helper()
	a, err := net.ResolveTCPAddr("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustURL(t testing.TB, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestURLCarriesTheTokenOnceAndNamesTheBoundPort(t *testing.T) {
	rg := newRig(t, nil)
	tok := rg.srv.Token()
	for addr, want := range map[string]string{
		"127.0.0.1:6969":  "http://127.0.0.1:6969/?token=" + tok,
		"127.0.0.1:41234": "http://127.0.0.1:41234/?token=" + tok,
		"[::1]:8080":      "http://[::1]:8080/?token=" + tok,
		"0.0.0.0:7000":    "http://localhost:7000/?token=" + tok,
		"[::]:7000":       "http://localhost:7000/?token=" + tok,
	} {
		a, err := net.ResolveTCPAddr("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if got := rg.srv.URL(a); got != want {
			t.Errorf("URL(%s) = %s, want %s", addr, got, want)
		}
	}
	if strings.Count(rg.srv.URL(mustAddr(t, "http://127.0.0.1:1")), tok) != 1 {
		t.Error("the token must appear exactly once")
	}
}

func TestServeCanBeCalledOnceAndStopsCleanly(t *testing.T) {
	srv, err := New(Config{Addr: "127.0.0.1:0", UI: testUI})
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := Listen("127.0.0.1:0", false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	waitFor(t, "the server to serve", func() bool { return srv.served.Load() })
	ln2, _ := Listen("127.0.0.1:0", false)
	defer ln2.Close()
	if err := srv.Serve(context.Background(), ln2); err == nil {
		t.Error("Serve twice")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v", err)
		}
	case <-time.After(guard):
		t.Fatal("no shutdown")
	}
	if _, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		t.Error("still listening after shutdown")
	}
}

func TestShutdownLetsARequestInFlightFinish(t *testing.T) {
	rg := newRig(t, nil)
	ln, _ := Listen("127.0.0.1:0", false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rg.srv.Serve(ctx, ln) }()
	base := "http://" + ln.Addr().String()
	result := make(chan int, 1)
	go func() {
		r, _ := http.NewRequest("GET", base+"/api/slow", nil)
		r.Header.Set("Authorization", "Bearer "+rg.srv.Token())
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			result <- -1
			return
		}
		resp.Body.Close()
		result <- resp.StatusCode
	}()
	<-rg.entered
	cancel()
	time.Sleep(50 * time.Millisecond)
	close(rg.release)
	if code := <-result; code != 200 {
		t.Errorf("the request in flight = %d", code)
	}
	if err := <-done; err != nil {
		t.Errorf("Serve = %v", err)
	}
}

func TestPanicOverTheWireAnswers500AndTheConnectionLives(t *testing.T) {
	rg := newRig(t, nil)
	base := rg.serve()
	c := &http.Client{Timeout: guard}
	do := func(path string) (int, string) {
		r, _ := http.NewRequest("GET", base+path, nil)
		r.Header.Set("Authorization", "Bearer "+rg.srv.Token())
		resp, err := c.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := do("/api/boom"); code != 500 || strings.Contains(body, "kaboom") {
		t.Errorf("panic = %d %s", code, body)
	}
	if code, _ := do("/api/ping"); code != 200 {
		t.Errorf("after the panic = %d", code)
	}
	// A panic after the response started breaks the response: the client must not mistake it for a whole one.
	rg.srv.HandleFunc("GET /api/half", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "partial")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic("after the first write")
	}, RouteOpts{})
	r, _ := http.NewRequest("GET", base+"/api/half", nil)
	r.Header.Set("Authorization", "Bearer "+rg.srv.Token())
	resp, err := c.Do(r)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err == nil {
		t.Error("a response cut short by a panic looked complete")
	}
}

// rawRequest sends bytes to the server and returns the status line and what follows.
func rawRequest(t testing.TB, base string, raw string) string {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(guard))
	if _, err := io.WriteString(conn, raw); err != nil {
		return "write error: " + err.Error()
	}
	b, _ := io.ReadAll(bufio.NewReader(conn))
	return string(b)
}

func TestHeaderSizeIsCapped(t *testing.T) {
	rg := newRig(t, nil)
	base := rg.serve()
	host := strings.TrimPrefix(base, "http://")
	ok := rawRequest(t, base, fmt.Sprintf("GET /healthz HTTP/1.1\r\nHost: %s\r\nX-Pad: %s\r\nConnection: close\r\n\r\n", host, strings.Repeat("a", 8<<10)))
	if !strings.HasPrefix(ok, "HTTP/1.1 200") {
		t.Errorf("8 KiB of headers: %.60s", ok)
	}
	big := rawRequest(t, base, fmt.Sprintf("GET /healthz HTTP/1.1\r\nHost: %s\r\nX-Pad: %s\r\nConnection: close\r\n\r\n", host, strings.Repeat("a", 40<<10)))
	if !strings.HasPrefix(big, "HTTP/1.1 431") && !strings.HasPrefix(big, "HTTP/1.1 400") && big != "" && !strings.HasPrefix(big, "write error") {
		t.Errorf("40 KiB of headers: %.60s", big)
	}
}

func TestAClientThatNeverFinishesItsHeadersIsDropped(t *testing.T) {
	rg := newRig(t, nil)
	rg.srv.readHeaderTimeout = 150 * time.Millisecond
	base := rg.serve()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /healthz HTTP/1.1\r\nHost: x\r\n") // and nothing more
	_ = conn.SetReadDeadline(time.Now().Add(guard))
	start := time.Now()
	_, err = io.ReadAll(conn)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the connection was held %v", d)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Error("the server never closed the connection")
	}
}

func TestSlowRequestBodiesAreCutOff(t *testing.T) {
	rg := newRig(t, nil)
	rg.srv.readTimeout = 200 * time.Millisecond
	base := rg.serve()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /api/echo/1 HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\n%s: 1\r\nContent-Length: 100\r\n\r\n{\"name\":",
		strings.TrimPrefix(base, "http://"), rg.srv.Token(), RequestHeader)
	_ = conn.SetReadDeadline(time.Now().Add(guard))
	out, _ := io.ReadAll(conn)
	if strings.Contains(string(out), "200 OK") {
		t.Errorf("a body that never completes was answered: %.100s", out)
	}
}

func TestConcurrentRequestsAreRaceFree(t *testing.T) {
	rg := newRig(t, nil)
	base := rg.serve()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := &http.Client{Timeout: guard}
			for j := 0; j < 20; j++ {
				var r *http.Request
				switch (i + j) % 4 {
				case 0:
					r, _ = http.NewRequest("GET", base+"/api/ping", nil)
					r.Header.Set("Authorization", "Bearer "+rg.srv.Token())
				case 1:
					r, _ = http.NewRequest("GET", base+"/?token="+rg.srv.Token(), nil)
				case 2:
					r, _ = http.NewRequest("GET", base+"/api/ping", nil)
				default:
					r, _ = http.NewRequest("POST", base+"/api/confirm", strings.NewReader(`{"scope":"x"}`))
					r.Header.Set("Authorization", "Bearer "+rg.srv.Token())
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set(RequestHeader, "1")
				}
				resp, err := c.Do(r)
				if err != nil {
					t.Error(err)
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}(i)
	}
	wg.Wait()
}

func TestNewRejectsAUIWithoutAnIndex(t *testing.T) {
	if _, err := New(Config{Addr: "127.0.0.1:0", UI: fstest.MapFS{"other.txt": {Data: []byte("x")}}}); err == nil || !strings.Contains(err.Error(), "index.html") {
		t.Errorf("New with an empty UI: %v", err)
	}
}

func TestRoutesHookRunsOnceAfterTheBuiltins(t *testing.T) {
	calls := 0
	rg := newRig(t, func(c *Config) {
		inner := c.Routes
		c.Routes = func(s *Server) {
			calls++
			// built-in routes exist already: registering one again is a conflict, which is a programming error
			func() {
				defer func() {
					if recover() == nil {
						t.Error("the built-in /api/ping can be replaced")
					}
				}()
				s.HandleFunc("GET /api/ping", func(http.ResponseWriter, *http.Request) {}, RouteOpts{})
			}()
			inner(s)
		}
	})
	if calls != 1 {
		t.Errorf("Routes called %d times", calls)
	}
	if rg.srv.Handler() != http.Handler(rg.srv) || rg.srv.Hub() == nil {
		t.Error("Handler or Hub")
	}
}
