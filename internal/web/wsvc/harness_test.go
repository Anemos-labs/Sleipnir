package wsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/testutil"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/webtest"
)

// Sessions start goroutines (agents, the swarm, the mock provider's server); a test that
// leaves one behind fails the package.
func TestMain(m *testing.M) { os.Exit(testutil.CheckLeaks(m)) }

// env is a real session on the mock provider, hosted by the fake host as a tab, with the
// Workspace routes registered on a real server whose handler the tests drive directly.
type env struct {
	t     *testing.T
	root  string
	sess  *session.Session
	host  *webtest.Host
	tab   *webtest.Tab
	srv   *web.Server
	token string
}

// git runs git in dir with a fixed identity.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Skipf("git unavailable or failed: %v %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// project writes files into a new git repository with one commit.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeFile(t, filepath.Join(dir, name), body)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

// writeFile writes a file and its parents.
func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readFile reads a file of the test.
func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// startMock serves a scripted model.
func startMock(t *testing.T, r mock.Responder) (*openaichat.Client, cost.Model) {
	t.Helper()
	agent.RetryBase = time.Millisecond
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, r)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	prof := openaichat.DefaultProfile("mock", ts.URL)
	client := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Profile: &prof})
	model := cost.Model{ID: "mock-1", ContextTokens: 1_000_000, MaxOutput: 4096, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1}}
	return client, model
}

// call is one scripted tool call.
func call(id, name string, args any) mock.ToolCall {
	b, _ := json.Marshal(args)
	return mock.ToolCall{ID: id, Name: name, Args: string(b)}
}

// turns counts the assistant messages a call carries (the step of a conversation).
func turns(c *mock.Call) int {
	n := 0
	for _, m := range c.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

// options are the session options of a test: bypass mode (no questions), its own home
// and state directory.
func options(t *testing.T, root string, client *openaichat.Client, model cost.Model) session.Options {
	return session.Options{
		Cwd: root, Root: root, Home: t.TempDir(), Dir: t.TempDir(),
		Provider: client, ModelInfo: &model, Model: "mock-1",
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true,
		// a person is there to be asked, as in a tab (who answers no: nothing in these tests asks)
		Prompter: func(context.Context, perm.Request) perm.Decision { return perm.Decision{Reason: "no"} },
	}
}

// newEnv builds a session from o and hosts it.
func newEnv(t *testing.T, root string, o session.Options) *env {
	t.Helper()
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	host := webtest.NewHost()
	tab, err := host.AddTab("t", root)
	if err != nil {
		t.Fatal(err)
	}
	tab.SessionFn = func() *session.Session { return s }
	srv, err := web.New(web.Config{Addr: "127.0.0.1:0", Routes: func(srv *web.Server) { Register(srv, host) }})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, root: root, sess: s, host: host, tab: tab, srv: srv, token: srv.Token()}
}

// tabPath is a route of the env's tab.
func (e *env) tabPath(rest string) string { return "/api/sessions/" + e.tab.TabID() + rest }

// request is one request the tests send; zero fields are the page's defaults.
type request struct {
	method, path string
	body         any
	raw          []byte // a body sent as is
	confirm      string
	origin       string
	noToken      bool
	contentType  string
	noHeader     bool
	header       map[string]string
}

// do sends a request through the server's whole envelope.
func (e *env) do(q request) *httptest.ResponseRecorder {
	e.t.Helper()
	var body *bytes.Reader
	switch {
	case q.raw != nil:
		body = bytes.NewReader(q.raw)
	case q.body != nil:
		b, err := json.Marshal(q.body)
		if err != nil {
			e.t.Fatal(err)
		}
		body = bytes.NewReader(b)
	default:
		body = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(q.method, "http://127.0.0.1"+q.path, body)
	if q.raw == nil && q.body == nil {
		r.Body = http.NoBody
		r.ContentLength = 0
	}
	r.Host = "127.0.0.1"
	if !q.noToken {
		r.Header.Set("Authorization", "Bearer "+e.token)
	}
	if q.method != http.MethodGet {
		if !q.noHeader {
			r.Header.Set(web.RequestHeader, web.RequestHeaderValue)
		}
		if q.raw != nil || q.body != nil {
			ct := q.contentType
			if ct == "" {
				ct = "application/json"
			}
			r.Header.Set("Content-Type", ct)
		}
	}
	if q.origin != "" {
		r.Header.Set("Origin", q.origin)
	}
	if q.confirm != "" {
		r.Header.Set(web.ConfirmHeader, q.confirm)
	}
	for k, v := range q.header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(w, r)
	return w
}

// get sends a GET of a tab route and decodes a 200 answer into out.
func (e *env) get(rest string, out any) *httptest.ResponseRecorder {
	e.t.Helper()
	w := e.do(request{method: http.MethodGet, path: e.tabPath(rest)})
	if out != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			e.t.Fatalf("%s: %v\n%s", rest, err, w.Body)
		}
	}
	return w
}

// post sends a JSON POST of a tab route (with a confirmation for scope, when given) and
// decodes a 200 answer into out.
func (e *env) post(rest string, body any, scope string, out any) *httptest.ResponseRecorder {
	e.t.Helper()
	q := request{method: http.MethodPost, path: e.tabPath(rest), body: body}
	if scope != "" {
		q.confirm = e.confirm(scope)
	}
	w := e.do(q)
	if out != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			e.t.Fatalf("%s: %v\n%s", rest, err, w.Body)
		}
	}
	return w
}

// confirm obtains a confirmation id for a scope.
func (e *env) confirm(scope string) string {
	e.t.Helper()
	w := e.do(request{method: http.MethodPost, path: "/api/confirm", body: map[string]string{"scope": scope}})
	if w.Code != http.StatusOK {
		e.t.Fatalf("confirm %s: %d %s", scope, w.Code, w.Body)
	}
	var c struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || c.ID == "" {
		e.t.Fatalf("confirm: %v %s", err, w.Body)
	}
	return c.ID
}

// code reads the error code of an answer.
func code(w *httptest.ResponseRecorder) string {
	var b struct{ Code string }
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	return b.Code
}

// expect fails the test when the answer's status or code is not the one wanted.
func expect(t *testing.T, w *httptest.ResponseRecorder, status int, errCode string) {
	t.Helper()
	if w.Code != status || (errCode != "" && code(w) != errCode) {
		t.Fatalf("got %d %s, want %d %s", w.Code, w.Body, status, errCode)
	}
}

// soloScript is a single agent that, turn by turn, makes the replies of turns[turn]: each
// reply is a batch of tool calls, made after the results of the previous one; then it
// says done.
type soloScript struct {
	mu    sync.Mutex
	turns [][][]mock.ToolCall
	turn  int // the next turn
	step  int // the next reply of the current turn
}

// respond answers the mock's calls.
func (sc *soloScript) respond(c *mock.Call) mock.Reply {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if last := c.Messages[len(c.Messages)-1]; last.Role == "user" {
		sc.step = 0
		sc.turn++
	}
	t := sc.turn - 1
	if t >= 0 && t < len(sc.turns) && sc.step < len(sc.turns[t]) {
		calls := sc.turns[t][sc.step]
		sc.step++
		return mock.Reply{Text: "working", ToolCalls: calls}
	}
	return mock.Reply{Text: "done"}
}
