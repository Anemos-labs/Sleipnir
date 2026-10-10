package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// mutating are the routes that change something, with a body each accepts.
var mutating = []struct {
	method, path, body string
}{
	{"POST", "/api/recorded/prune", `{"olderThan":"30d","keep":20}`},
	{"POST", "/api/recorded/delete", `{"ids":["20260101-000000-aaaaaa"]}`},
	{"POST", "/api/recorded/20260101-000000-aaaaaa/watch", ``},
	{"DELETE", "/api/recorded/20260101-000000-aaaaaa/watch", ``},
	{"POST", "/api/schedule/jobs", `{"cron":"@daily","goal":"g","budgetUsd":1}`},
	{"PUT", "/api/schedule/jobs/j1", `{"cron":"@daily","goal":"g","budgetUsd":1}`},
	{"POST", "/api/schedule/jobs/j1/pause", `{"paused":true}`},
	{"DELETE", "/api/schedule/jobs/j1", ``},
	{"POST", "/api/schedule/jobs/j1/run", ``},
	{"POST", "/api/schedule/daemon", `{"action":"stop"}`},
	{"POST", "/api/doctor", `{"model":"m"}`},
	{"POST", "/api/update/install", ``},
}

// The envelope guards every route that changes something: no credential, another origin, another site, no custom header, a body
// that is not JSON or too large.
func TestToolsRoutesSecurityMatrix(t *testing.T) {
	rg := newRig(t, nil)
	rg.session(sidOld, time.Hour, "x")
	for _, rt := range mutating {
		cases := map[string]struct {
			q    req
			st   int
			code string
		}{
			"no credential":  {req{noAuth: true}, 401, "unauthenticated"},
			"another origin": {req{header: map[string]string{"Origin": "http://127.0.0.1:7000"}}, 403, "forbidden_origin"},
			"cross-site":     {req{header: map[string]string{"Sec-Fetch-Site": "cross-site"}}, 403, "forbidden_site"},
			"no header":      {req{header: map[string]string{"X-Sleipnir-Web": ""}}, 403, "csrf"},
		}
		if rt.body != "" {
			cases["text/plain"] = struct {
				q    req
				st   int
				code string
			}{req{header: map[string]string{"Content-Type": "text/plain"}}, 415, "unsupported_media_type"}
			cases["oversized"] = struct {
				q    req
				st   int
				code string
			}{req{raw: `{"goal":"` + strings.Repeat("a", 70<<10) + `"}`}, 413, "body_too_large"}
		} else {
			cases["a body where none is taken"] = struct {
				q    req
				st   int
				code string
			}{req{raw: `{}`}, 400, "bad_request"}
		}
		for name, tc := range cases {
			q := tc.q
			q.method, q.path = rt.method, rt.path
			if q.raw == "" && rt.body != "" {
				q.raw = rt.body
			}
			w := rg.do(q)
			if w.Code != tc.st || errCode(w) != tc.code {
				t.Errorf("%s %s, %s: %d %s, want %d %s", rt.method, rt.path, name, w.Code, w.Body.String(), tc.st, tc.code)
			}
		}
	}
	for _, path := range []string{"/api/recorded", "/api/schedule", "/api/schedule/next?cron=@daily", "/api/doctor/endpoints", "/api/update", "/api/recorded/watching"} {
		if w := rg.do(req{method: "GET", path: path, noAuth: true}); w.Code != 401 {
			t.Errorf("GET %s without a credential: %d", path, w.Code)
		}
	}
	if w := rg.do(req{method: "GET", path: "/api/recorded/prune"}); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on a POST route: %d", w.Code)
	}
}

// A key the server holds, planted in a session's log, a job's goal and output, and a doctor's output, never reaches a response,
// a frame or the log.
func TestNoSecretInAnyResponse(t *testing.T) {
	const name, key = "TOOLSTEST_API_KEY", "sk-test-planted-0123456789abcdefghij"
	t.Setenv(name, "")
	harden.Provide(name, key)
	t.Cleanup(func() { harden.Provide(name, "") })
	rg := newRig(t, nil)
	rg.session(sidOld, time.Hour, "use the key "+key+" please")
	rg.addJob(wire.JobRequest{Cron: "@daily", Goal: "print " + key, Dir: rg.project})
	run := decode[wire.RunStarted](t, rg.do(req{method: "POST", path: "/api/schedule/jobs/j1/run"}))
	rg.waitRun(run.ID)
	doc := decode[wire.RunStarted](t, rg.do(req{method: "POST", path: "/api/doctor", body: map[string]any{"model": "m"}}))
	rg.waitRun(doc.ID)
	var all strings.Builder
	for _, path := range []string{"/api/recorded", "/api/schedule", "/api/schedule/jobs/j1/log", "/api/doctor/endpoints", "/api/recorded/watching"} {
		w := rg.do(req{method: "GET", path: path})
		all.WriteString(w.Body.String())
	}
	all.WriteString(rg.do(req{method: "POST", path: "/api/recorded/prune", body: map[string]any{"olderThan": "0", "keep": 0}}).Body.String())
	b, _ := json.Marshal(rg.host.frames)
	all.Write(b)
	all.WriteString(rg.log())
	if strings.Contains(all.String(), key) {
		t.Fatal("a planted key reached a response, a frame or the log")
	}
	if !strings.Contains(all.String(), "[redacted]") {
		t.Error("nothing was masked: the plants did not reach the routes")
	}
}
