package env

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/rl"
)

const testToken = "test-token-not-a-secret"

// serverFixture is a runner fixture behind an httptest server.
type serverFixture struct {
	*runnerFixture
	srv  *Server
	ts   *httptest.Server
	root string
}

func newServerFixture(t *testing.T, mut ...func(*ServeOptions)) *serverFixture {
	t.Helper()
	rf := newRunnerFixture(t, quickVerify)
	rf.h.Default = FakeScript{Steps: []FakeStep{FakeWrite("mathx.go", fixedMath)}, Final: "fixed"}
	root := filepath.Join(t.TempDir(), "served")
	// The policy the fixture's requests name (rolloutReq) is one the operator allows.
	o := ServeOptions{Root: root, Token: testToken, Tasks: []rl.Task{rf.task}, MaxRuns: 2,
		PolicyHosts: []string{"vllm:8000"}, PolicyKeyEnvs: []string{"VLLM_KEY"}}
	for _, m := range mut {
		m(&o)
	}
	srv, err := NewServer(rf.r, o)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return &serverFixture{runnerFixture: rf, srv: srv, ts: ts, root: root}
}

func (f *serverFixture) do(method, path, token string, body any) *http.Response {
	f.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.ts.URL+path, rd)
	if err != nil {
		f.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// stream reads an NDJSON response into generic lines.
func stream(t *testing.T, resp *http.Response) []map[string]json.RawMessage {
	t.Helper()
	var lines []map[string]json.RawMessage
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad NDJSON line %q: %v", sc.Text(), err)
		}
		lines = append(lines, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func lineType(l map[string]json.RawMessage) string {
	var s string
	if err := json.Unmarshal(l["type"], &s); err != nil {
		return "<unparseable type>"
	}
	return s
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func (f *serverFixture) rolloutReq(extra map[string]any) map[string]any {
	req := map[string]any{
		"task":   f.task.ID,
		"policy": map[string]any{"model": "my-policy", "base_url": "http://vllm:8000/v1", "api_key_env": "VLLM_KEY", "sampling": map[string]any{"temperature": 0.8}},
		"group":  3,
		"seed":   11,
	}
	for k, v := range extra {
		req[k] = v
	}
	return req
}

func TestServerAuthentication(t *testing.T) {
	f := newServerFixture(t)
	tests := []struct {
		name   string
		token  string
		header string
		want   int
	}{
		{"no header", "", "", 401},
		{"wrong token", "nope", "", 401},
		{"prefix of the right token", testToken[:5], "", 401},
		{"longer token", testToken + "x", "", 401},
		{"empty bearer", "", "Bearer ", 401},
		{"basic scheme", "", "Basic " + testToken, 401},
		{"right token", testToken, "", 200},
		{"scheme is case-insensitive", "", "bearer " + testToken, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", f.ts.URL+"/v1/runs/nothing", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			// 200 is never returned for an unknown run; an authenticated request
			// reaches the handler (404) while an unauthenticated one is stopped (401).
			got := resp.StatusCode
			if tc.want == 200 {
				if got != 404 {
					t.Fatalf("authenticated request got %d, want 404 from the handler", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
			if resp.Header.Get("WWW-Authenticate") == "" {
				t.Error("no WWW-Authenticate challenge")
			}
		})
	}
	// Health checks need no token; everything else does.
	if resp := f.do("GET", "/healthz", "", nil); resp.StatusCode != 200 {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
	if resp := f.do("POST", "/v1/rollouts", "", f.rolloutReq(nil)); resp.StatusCode != 401 {
		t.Errorf("rollouts without token: %d", resp.StatusCode)
	}
	if len(f.h.Calls()) != 0 {
		t.Error("an unauthenticated request ran a rollout")
	}
}

func TestServerStreamsProgressThenEpisodes(t *testing.T) {
	f := newServerFixture(t)
	resp := f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(nil))
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("status %d type %q: %s", resp.StatusCode, resp.Header.Get("Content-Type"), body(t, resp))
	}
	lines := stream(t, resp)
	if len(lines) < 8 {
		t.Fatalf("only %d lines", len(lines))
	}
	if lineType(lines[0]) != "accepted" {
		t.Fatalf("first line: %v", lines[0])
	}
	var runID string
	if err := json.Unmarshal(lines[0]["run_id"], &runID); err != nil {
		t.Fatal(err)
	}
	if !runIDRe.MatchString(runID) {
		t.Fatalf("run id %q", runID)
	}
	// Order: progress events, then episodes, then the summary, then done.
	phase := 0
	var episodes []rl.Episode
	seenRunStart, seenRolloutDone := false, false
	for _, l := range lines[1:] {
		switch typ := lineType(l); typ {
		case "progress":
			if phase != 0 {
				t.Fatal("progress after episodes started")
			}
			var p Progress
			if err := json.Unmarshal(l["progress"], &p); err != nil {
				t.Fatal(err)
			}
			seenRunStart = seenRunStart || p.Type == "run.start"
			seenRolloutDone = seenRolloutDone || p.Type == "rollout.done"
		case "episode":
			phase = 1
			var ep rl.Episode
			if err := json.Unmarshal(l["episode"], &ep); err != nil {
				t.Fatal(err)
			}
			episodes = append(episodes, ep)
		case "summary":
			phase = 2
		case "done":
			if phase != 2 {
				t.Fatal("done before the summary")
			}
			phase = 3
		default:
			t.Fatalf("unexpected line type %q: %v", typ, l)
		}
	}
	if phase != 3 || !seenRunStart || !seenRolloutDone {
		t.Fatalf("stream did not complete: phase %d start %v done %v", phase, seenRunStart, seenRolloutDone)
	}
	if len(episodes) != 3 {
		t.Fatalf("%d episodes", len(episodes))
	}
	for i, ep := range episodes {
		if ep.Sample != i || ep.Outcome.Verifier == nil || !ep.Outcome.Verifier.Pass || ep.Group != f.task.ID+"@"+runID {
			t.Errorf("episode %d: %+v", i, ep.Outcome)
		}
		if ep.Policy.Model != "my-policy" || ep.Policy.Endpoint != "http://vllm:8000" {
			t.Errorf("policy %+v", ep.Policy)
		}
	}
	// The run is on disk under the server root, laid out as documented.
	for _, name := range []string{"manifest.json", "summary.json", f.task.ID + "/0/episode.json"} {
		if !exists(filepath.Join(f.root, runID, name)) {
			t.Errorf("missing %s", name)
		}
	}
	calls := f.h.Calls()
	if len(calls) != 3 || calls[0].Seed != SampleSeed(11, f.task.ID, calls[0].Sample) && calls[1].Seed != SampleSeed(11, f.task.ID, calls[1].Sample) {
		t.Errorf("calls: %+v", calls)
	}

	// GET /v1/runs/{id}
	resp = f.do("GET", "/v1/runs/"+runID, testToken, nil)
	var st RunStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil || st.Status != "done" || st.Summary == nil || st.Summary.Completed != 3 || st.Manifest == nil {
		t.Fatalf("status: %+v %v", st, err)
	}
	// One episode by id.
	resp = f.do("GET", "/v1/runs/"+runID+"/episodes/"+f.task.ID+"/1", testToken, nil)
	var ep rl.Episode
	if err := json.NewDecoder(resp.Body).Decode(&ep); err != nil || ep.Sample != 1 {
		t.Fatalf("episode fetch: %+v %v", ep, err)
	}
	if resp := f.do("GET", "/v1/runs/"+runID+"/episodes/"+f.task.ID+"/9", testToken, nil); resp.StatusCode != 404 {
		t.Errorf("missing episode: %d", resp.StatusCode)
	}
}

func TestServerInlineTaskAndRoleOptions(t *testing.T) {
	f := newServerFixture(t)
	inline := f.task
	inline.ID = "inline-1"
	req := f.rolloutReq(map[string]any{
		"task": inline, "group": 1, "swarm": map[string]any{"agents": 4}, "capture": true, "target_price": "anthropic-sonnet",
		"role_models": map[string]string{"manager": "big-model"}, "run_id": "my-run.1",
	})
	lines := stream(t, f.do("POST", "/v1/rollouts", testToken, req))
	if lineType(lines[len(lines)-1]) != "done" {
		t.Fatalf("last line: %v", lines[len(lines)-1])
	}
	c := f.h.Calls()[0]
	if c.Task != "inline-1" {
		t.Fatalf("call %+v", c)
	}
	var mf Manifest
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(f.root, "my-run.1", "manifest.json"))), &mf); err != nil {
		t.Fatal(err)
	}
	if mf.RunID != "my-run.1" || !mf.Config.Swarm || mf.Config.Agents != 4 || !mf.Config.Capture || mf.Config.TargetPrice != "anthropic-sonnet" || mf.Config.RoleModels["manager"] != "big-model" {
		t.Fatalf("manifest config: %+v", mf.Config)
	}
	// A swarm boolean works too, and reusing a run id resumes that run.
	lines = stream(t, f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(map[string]any{"task": inline, "group": 1, "swarm": true, "run_id": "my-run.1"})))
	if len(f.h.Calls()) != 1 {
		t.Errorf("resumed run re-ran finished rollouts: %d calls", len(f.h.Calls()))
	}
	_ = lines
}

func TestServerRejectsBadRequests(t *testing.T) {
	f := newServerFixture(t, func(o *ServeOptions) { o.MaxBodyBytes = 64 << 10; o.MaxGroup = 8 })
	valid := func() map[string]any { return f.rolloutReq(nil) }
	tests := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
		errHas string
	}{
		{"wrong method", "GET", "/v1/rollouts", nil, 405, "POST"},
		{"invalid json", "POST", "/v1/rollouts", "{not json", 400, "invalid request"},
		{"empty body", "POST", "/v1/rollouts", "", 400, "invalid request"},
		{"trailing data", "POST", "/v1/rollouts", `{"task":"mathx-max","policy":{"model":"m"}} {"x":1}`, 400, "trailing"},
		{"unknown field", "POST", "/v1/rollouts", map[string]any{"task": "mathx-max", "policy": map[string]any{"model": "m"}, "sudo": true}, 400, "unknown field"},
		{"no task", "POST", "/v1/rollouts", map[string]any{"policy": map[string]any{"model": "m"}}, 400, "task"},
		{"unknown task id", "POST", "/v1/rollouts", map[string]any{"task": "nope", "policy": map[string]any{"model": "m"}}, 404, "unknown task"},
		{"invalid inline task", "POST", "/v1/rollouts", map[string]any{"task": map[string]any{"id": "x"}, "policy": map[string]any{"model": "m"}}, 400, "kind"},
		{"inline task with unknown field", "POST", "/v1/rollouts", map[string]any{"task": map[string]any{"id": "x", "evil": 1}, "policy": map[string]any{"model": "m"}}, 400, "unknown field"},
		{"no model", "POST", "/v1/rollouts", map[string]any{"task": "mathx-max", "policy": map[string]any{}}, 400, "policy.model"},
		{"group negative", "POST", "/v1/rollouts", func() any { r := valid(); r["group"] = -1; return r }(), 400, "group"},
		{"group too large", "POST", "/v1/rollouts", func() any { r := valid(); r["group"] = 9; return r }(), 400, "group"},
		{"bad swarm", "POST", "/v1/rollouts", func() any { r := valid(); r["swarm"] = "yes"; return r }(), 400, "swarm"},
		{"swarm agents out of range", "POST", "/v1/rollouts", func() any { r := valid(); r["swarm"] = map[string]any{"agents": 9999}; return r }(), 400, "swarm"},
		{"run id traversal", "POST", "/v1/rollouts", func() any { r := valid(); r["run_id"] = "../../etc"; return r }(), 400, "run_id"},
		{"run id slash", "POST", "/v1/rollouts", func() any { r := valid(); r["run_id"] = "a/b"; return r }(), 400, "run_id"},
		{"run id dotdot", "POST", "/v1/rollouts", func() any { r := valid(); r["run_id"] = ".."; return r }(), 400, "run_id"},
		{"rewards unsupported", "POST", "/v1/rollouts", func() any { r := valid(); r["rewards"] = "r.json"; return r }(), 400, "rewards"},
		{"oversized body", "POST", "/v1/rollouts", strings.Repeat("x", 100<<10), 413, "exceeds"},
		{"run id in status is validated", "GET", "/v1/runs/..%2f..%2fetc", nil, 400, "invalid run id"},
		{"status of missing run", "GET", "/v1/runs/doesnotexist", nil, 404, "no such run"},
		{"status wrong method", "DELETE", "/v1/runs/x", nil, 405, "GET"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do(tc.method, tc.path, testToken, tc.body)
			b := body(t, resp)
			if resp.StatusCode != tc.want || !strings.Contains(b, tc.errHas) {
				t.Fatalf("status %d (want %d): %s", resp.StatusCode, tc.want, b)
			}
		})
	}
	if len(f.h.Calls()) != 0 {
		t.Fatal("a rejected request ran a rollout")
	}
	// Nothing was created on disk for rejected requests.
	if ents, _ := os.ReadDir(f.root); len(ents) != 0 {
		t.Errorf("rejected requests left %d run directories", len(ents))
	}
}

func TestServerConfinesInlineTasksToRepoRoots(t *testing.T) {
	f0 := newRunnerFixture(t, quickVerify)
	allowed := filepath.Dir(f0.repo.Dir)
	f := newServerFixture(t, func(o *ServeOptions) { o.RepoRoots = []string{allowed} })
	outside := f.task
	outside.ID = "outside"
	outside.Repo.Path = "/etc"
	if resp := f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(map[string]any{"task": outside})); resp.StatusCode != 403 {
		t.Fatalf("path outside the roots: %d %s", resp.StatusCode, body(t, resp))
	}
	// A symlink inside the root that points outside does not fool the check.
	link := filepath.Join(allowed, "sneaky-link")
	if err := os.Symlink("/etc", link); err == nil {
		defer func() { _ = os.Remove(link) }()
		via := f.task
		via.ID = "via-link"
		via.Repo.Path = link
		if resp := f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(map[string]any{"task": via})); resp.StatusCode != 403 {
			t.Fatalf("symlink out of the roots: %d", resp.StatusCode)
		}
	}
	urlTask := f.task
	urlTask.ID = "url"
	urlTask.Repo.Path, urlTask.Repo.URL = "", "https://example.com/x.git"
	if resp := f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(map[string]any{"task": urlTask})); resp.StatusCode != 403 {
		t.Fatalf("URL repo: %d", resp.StatusCode)
	}
	// The registry is trusted; an inline task under the root is fine.
	inside := f.task
	inside.ID = "inside"
	lines := stream(t, f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(map[string]any{"task": inside, "group": 1})))
	if lineType(lines[len(lines)-1]) != "done" {
		t.Fatalf("inside the root: %v", lines[len(lines)-1])
	}
	lines = stream(t, f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(map[string]any{"group": 1})))
	if lineType(lines[len(lines)-1]) != "done" {
		t.Fatalf("registry task: %v", lines[len(lines)-1])
	}
}

func TestServerRewardsPathsAreConfined(t *testing.T) {
	rewardsDir := filepath.Join(t.TempDir(), "rewards")
	mustWrite(t, filepath.Join(rewardsDir, "good.json"), `{"outcome": 1}`)
	secret := filepath.Join(t.TempDir(), "secret.json")
	mustWrite(t, secret, "secret")
	if err := os.Symlink(secret, filepath.Join(rewardsDir, "link.json")); err != nil {
		t.Fatal(err)
	}
	var seen atomic.Value
	f := newServerFixture(t, func(o *ServeOptions) {
		o.RewardsDir = rewardsDir
		o.NewScorer = func(p string) (Scorer, error) {
			seen.Store(p)
			return func(ep *rl.Episode, task *rl.Task, runDir string) error {
				ep.Reward = rl.Reward{Total: 42, Components: map[string]float64{"custom": 42}}
				return nil
			}, nil
		}
	})
	for _, bad := range []string{"../secret.json", "/etc/passwd", "link.json", "sub/../../secret.json", "missing.json", "..", "a\x00b"} {
		resp := f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(map[string]any{"rewards": bad}))
		if resp.StatusCode != 400 {
			t.Errorf("rewards %q: status %d", bad, resp.StatusCode)
		}
	}
	if seen.Load() != nil {
		t.Fatalf("NewScorer was called with %v for a rejected path", seen.Load())
	}
	lines := stream(t, f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(map[string]any{"rewards": "good.json", "group": 1})))
	var ep rl.Episode
	for _, l := range lines {
		if lineType(l) == "episode" {
			if err := json.Unmarshal(l["episode"], &ep); err != nil {
				t.Fatal(err)
			}
		}
	}
	real, _ := filepath.EvalSymlinks(filepath.Join(rewardsDir, "good.json"))
	if seen.Load() != real || ep.Reward.Total != 42 {
		t.Fatalf("scorer path %v reward %v", seen.Load(), ep.Reward)
	}
}

func TestServerLimitsConcurrentRuns(t *testing.T) {
	f := newServerFixture(t, func(o *ServeOptions) { o.MaxRuns = 1 })
	f.h.Default = FakeScript{Steps: []FakeStep{FakeHangForever()}}
	f.task.Budget.WallS = 60
	started := make(chan struct{})
	go func() {
		resp := f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(map[string]any{"group": 1, "run_id": "slow"}))
		close(started)
		_, _ = io.Copy(io.Discard, resp.Body)
	}()
	eventually(t, 30*time.Second, func() bool { return len(f.h.Calls()) == 1 }, "the first run to start")
	resp := f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(map[string]any{"group": 1}))
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("second concurrent run: %d %s", resp.StatusCode, body(t, resp))
	}
	// While it runs, GET reports it as running.
	resp = f.do("GET", "/v1/runs/slow", testToken, nil)
	var st RunStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "running" {
		t.Fatalf("status of a live run: %+v", st)
	}
	// The same run id cannot be started twice.
	// (The slot limit answers first here, which is also a refusal.)
	close(f.h.Release)
	<-started
}

func TestServerClientDisconnectCancelsTheRun(t *testing.T) {
	f := newServerFixture(t)
	f.h.Default = FakeScript{Steps: []FakeStep{FakeWrite("marker", "x"), FakeHang()}}
	f.task.Budget.WallS = 300
	ctx, cancel := context.WithCancel(context.Background())
	body, _ := json.Marshal(f.rolloutReq(map[string]any{"group": 2, "run_id": "dropped"}))
	req, _ := http.NewRequestWithContext(ctx, "POST", f.ts.URL+"/v1/rollouts", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the accepted line, then hang up.
	br := bufio.NewReader(resp.Body)
	if line, err := br.ReadString('\n'); err != nil || !strings.Contains(line, "accepted") {
		t.Fatalf("first line %q %v", line, err)
	}
	eventually(t, 30*time.Second, func() bool { return len(f.h.Calls()) >= 1 }, "a rollout to start")
	cancel()
	_ = resp.Body.Close()
	eventually(t, 30*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(f.root, "dropped", "manifest.json"))
		return err == nil && strings.Contains(string(b), `"status": "cancelled"`)
	}, "the run to be cancelled and its manifest finished")
	eventually(t, 30*time.Second, func() bool {
		ents, _ := os.ReadDir(filepath.Join(f.m.Root(), "ws"))
		return len(ents) == 0
	}, "workspaces to be cleaned up")
	// The slot is free again.
	f.h.Default = FakeScript{}
	lines := stream(t, f.do("POST", "/v1/rollouts", testToken, f.rolloutReq(map[string]any{"group": 1})))
	if lineType(lines[len(lines)-1]) != "done" {
		t.Fatalf("server did not recover: %v", lines[len(lines)-1])
	}
}

func TestServerNoTokenNeededOnlyOnLoopback(t *testing.T) {
	f := newRunnerFixture(t)
	opts := func(root string) ServeOptions { return ServeOptions{Root: root, Tasks: []rl.Task{f.task}} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, addr := range []string{":0", "0.0.0.0:0", "[::]:0", "192.0.2.1:0"} {
		err := Serve(ctx, addr, f.r, opts(filepath.Join(t.TempDir(), "s")))
		if !errors.Is(err, ErrNeedToken) {
			t.Errorf("Serve(%q) without a token: %v", addr, err)
		}
	}
	// With a token any address is allowed; loopback needs none.
	for _, tc := range []struct {
		addr  string
		token string
	}{{"127.0.0.1:0", ""}, {"localhost:0", ""}, {"127.0.0.1:0", "tok"}} {
		ctx, cancel := context.WithCancel(context.Background())
		listening := make(chan net.Addr, 1)
		o := opts(filepath.Join(t.TempDir(), "s"))
		o.Token = tc.token
		o.OnListen = func(a net.Addr) { listening <- a }
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, tc.addr, f.r, o) }()
		select {
		case a := <-listening:
			resp, err := http.Get("http://" + a.String() + "/healthz")
			if err != nil || resp.StatusCode != 200 {
				t.Errorf("healthz on %s: %v", tc.addr, err)
			}
			_ = resp.Body.Close()
		case err := <-done:
			t.Fatalf("Serve(%q, token %q) failed: %v", tc.addr, tc.token, err)
		case <-time.After(10 * time.Second):
			t.Fatal("no listener")
		}
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve(%q) returned %v on shutdown", tc.addr, err)
		}
	}
	// A pre-made listener is checked the same way.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skip("cannot listen on all interfaces here")
	}
	defer func() { _ = ln.Close() }()
	o := opts(filepath.Join(t.TempDir(), "s"))
	o.Listener = ln
	if err := Serve(ctx, "ignored", f.r, o); !errors.Is(err, ErrNeedToken) {
		t.Errorf("wildcard listener without a token: %v", err)
	}
}

func TestServerGracefulShutdownFinishesManifests(t *testing.T) {
	f := newRunnerFixture(t)
	f.h.Default = FakeScript{Steps: []FakeStep{FakeWrite("x", "y"), FakeHang()}}
	f.task.Budget.WallS = 300
	root := filepath.Join(t.TempDir(), "s")
	listening := make(chan net.Addr, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, "127.0.0.1:0", f.r, ServeOptions{
			Root: root, Tasks: []rl.Task{f.task}, ShutdownTimeout: 20 * time.Second, OnListen: func(a net.Addr) { listening <- a },
		})
	}()
	addr := (<-listening).String()
	reqBody, _ := json.Marshal(map[string]any{"task": f.task.ID, "policy": map[string]any{"model": "m"}, "group": 2, "run_id": "inflight"})
	respCh := make(chan []byte, 1)
	go func() {
		resp, err := http.Post("http://"+addr+"/v1/rollouts", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			respCh <- nil
			return
		}
		b, _ := io.ReadAll(resp.Body)
		respCh <- b
	}()
	eventually(t, 30*time.Second, func() bool { return len(f.h.Calls()) >= 1 }, "a rollout to start")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if b, err := os.ReadFile(filepath.Join(root, "inflight", "manifest.json")); err != nil || !strings.Contains(string(b), `"status": "cancelled"`) {
		t.Fatalf("manifest after shutdown: %v %s", err, b)
	}
	if _, err := os.Stat(filepath.Join(root, "inflight", "summary.json")); err != nil {
		t.Fatalf("summary after shutdown: %v", err)
	}
	<-respCh
	if _, err := http.Get("http://" + addr + "/healthz"); err == nil {
		t.Error("server still accepting after shutdown")
	}
}

func TestServerRefusesBadConfiguration(t *testing.T) {
	f := newRunnerFixture(t)
	if _, err := NewServer(f.r, ServeOptions{}); err == nil {
		t.Error("missing root accepted")
	}
	if _, err := NewServer(&Runner{}, ServeOptions{Root: t.TempDir()}); err == nil {
		t.Error("runner without harness accepted")
	}
	bad := f.task
	bad.Verifier.Cmd = ""
	if _, err := NewServer(f.r, ServeOptions{Root: t.TempDir(), Tasks: []rl.Task{bad}}); err == nil {
		t.Error("invalid registry task accepted")
	}
}

func TestLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:80": true, "127.5.5.5:80": true, "[::1]:80": true, "localhost:80": true, "localhost": true, "::1": true,
		":80": false, "0.0.0.0:80": false, "[::]:80": false, "192.168.1.5:80": false, "10.0.0.1": false, "8.8.8.8:53": false, "": false,
	} {
		if got := loopbackAddr(addr); got != want {
			t.Errorf("loopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

// The mux cleans "../" out of request paths before a handler sees them; the
// handler must still refuse them if it is ever reached another way.
func TestServerEpisodePathValidation(t *testing.T) {
	f := newServerFixture(t)
	for _, path := range []string{
		"/v1/runs/x/episodes/../secret/0", "/v1/runs/x/episodes/a%2fb/0", "/v1/runs/x/episodes/.hidden/0",
		"/v1/runs/x/episodes/mathx-max/../../0", "/v1/runs/x/episodes/mathx-max/-1", "/v1/runs/x/episodes/mathx-max/1.json",
		"/v1/runs/x/other/mathx-max/0", "/v1/runs/x/episodes/mathx-max/0000000000000",
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.URL.Path = strings.ReplaceAll(path, "%2f", "/")
		rec := httptest.NewRecorder()
		f.srv.handleRuns(rec, req)
		if rec.Code != 400 && rec.Code != 404 {
			t.Errorf("%s -> %d", path, rec.Code)
		}
		if rec.Code == 200 {
			t.Errorf("%s served a file", path)
		}
	}
}

func TestResolveUnder(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a", "b.json"), "x")
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "f"), "x")
	if err := os.Symlink(outside, filepath.Join(root, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if p, err := resolveUnder(root, "a/b.json"); err != nil || !strings.HasSuffix(p, filepath.Join("a", "b.json")) {
		t.Fatalf("%q %v", p, err)
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", "dirlink/f", "nope.json", "a\x00b"} {
		if _, err := resolveUnder(root, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	_ = fmt.Sprint
}

// A request names the policy endpoint and the variable that holds its key. Neither may be a
// way for a client to make the server send one of its credentials somewhere: the operator
// lists the hosts and the variables, loopback is always fine, and nothing else is.
func TestServerPolicyEndpointAndKeyMustBeAllowedByTheOperator(t *testing.T) {
	f := newServerFixture(t)
	policy := func(base, key string) map[string]any {
		p := map[string]any{"model": "my-policy"}
		if base != "" {
			p["base_url"] = base
		}
		if key != "" {
			p["api_key_env"] = key
		}
		return f.rolloutReq(map[string]any{"policy": p})
	}
	for _, tc := range []struct {
		name      string
		base, key string
		want      int
		errHas    string
	}{
		{"another host", "https://attacker.example/v1", "VLLM_KEY", 403, "policy.base_url host"},
		{"another port of an allowed host", "http://vllm:9999/v1", "VLLM_KEY", 403, "policy.base_url host"},
		{"a lookalike of localhost", "http://localhost.attacker.example/v1", "", 403, "policy.base_url host"},
		{"a lookalike of a loopback address", "http://127.0.0.1.attacker.example/v1", "", 403, "policy.base_url host"},
		{"unspecified address", "http://0.0.0.0:8000/v1", "", 403, "policy.base_url host"},
		{"credentials in the URL", "http://user:pw@vllm:8000/v1", "", 400, "must not carry credentials"},
		{"not http", "ftp://vllm:8000/v1", "", 400, "http or https"},
		{"no host", "http:///v1", "", 400, "http or https"},
		{"an unlisted key variable", "http://vllm:8000/v1", "HEIMDALL_API_KEY", 403, "policy.api_key_env"},
		{"a key variable that only looks like the listed one", "http://vllm:8000/v1", "VLLM_KEY2", 403, "policy.api_key_env"},
		{"an unlisted key at a loopback host", "http://127.0.0.1:8000/v1", "HEIMDALL_API_KEY", 403, "policy.api_key_env"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do("POST", "/v1/rollouts", testToken, policy(tc.base, tc.key))
			if b := body(t, resp); resp.StatusCode != tc.want || !strings.Contains(b, tc.errHas) {
				t.Fatalf("status %d (want %d): %s", resp.StatusCode, tc.want, b)
			}
		})
	}
	if len(f.h.Calls()) != 0 {
		t.Fatal("a refused request ran a rollout")
	}
	if ents, _ := os.ReadDir(f.root); len(ents) != 0 {
		t.Errorf("refused requests left %d run directories", len(ents))
	}

	// What is allowed.
	for _, tc := range []struct{ base, key string }{
		{"http://vllm:8000/v1", "VLLM_KEY"},
		{"HTTP://VLLM:8000/v1", "VLLM_KEY"}, // hosts are not case sensitive
		{"http://127.0.0.1:8000/v1", ""},
		{"http://127.0.0.1:8000/v1", "VLLM_KEY"},
		{"http://[::1]:8000/v1", ""},
		{"http://localhost:8000/v1", ""},
		{"https://LOCALHOST/v1", ""},
		{"", ""}, // no endpoint named: the harness reports it
	} {
		if code, err := f.srv.checkPolicy(PolicyRequest{Model: "m", BaseURL: tc.base, APIKeyEnv: tc.key}); err != nil {
			t.Errorf("%q with key %q refused (%d): %v", tc.base, tc.key, code, err)
		}
	}
}

// With no lists at all, a server takes policies on loopback and no key: nothing else is
// reachable through a request.
func TestServerPolicyDefaultsAreLoopbackAndNoKey(t *testing.T) {
	f := newServerFixture(t, func(o *ServeOptions) { o.PolicyHosts, o.PolicyKeyEnvs = nil, nil })
	for _, tc := range []struct {
		base, key string
		ok        bool
	}{
		{"http://127.0.0.1:8000/v1", "", true},
		{"http://localhost:8000/v1", "", true},
		{"http://vllm:8000/v1", "", false},
		{"https://api.example.com/v1", "", false},
		{"http://127.0.0.1:8000/v1", "ANY_KEY", false},
	} {
		_, err := f.srv.checkPolicy(PolicyRequest{Model: "m", BaseURL: tc.base, APIKeyEnv: tc.key})
		if (err == nil) != tc.ok {
			t.Errorf("%q with key %q: allowed=%v, want %v (%v)", tc.base, tc.key, err == nil, tc.ok, err)
		}
	}
}
