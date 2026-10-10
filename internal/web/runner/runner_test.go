package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/web/clispec"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Every command of the spec has a mode the runner knows, and the request of each is answered as its mode says: tty_only refused
// with the page's equivalent, priv only with a confirmation, the others started.
func TestRunnerModes(t *testing.T) {
	rg := newRig(t, Options{})
	spec, err := clispec.Spec()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range spec.Commands {
		pos := map[string]string{}
		for _, p := range c.Positional {
			if p.Required {
				pos[p.Name] = "1"
			}
		}
		plan, err := rg.runs.Plan(t.Context(), c.Path, pos, nil, "")
		switch c.Mode {
		case "tty_only":
			we, _ := err.(*wire.Error)
			if we == nil || we.Status != 403 || we.Code != "tty_only" || we.Msg == "" {
				t.Errorf("%v: %v, want 403 tty_only with a sentence", c.Path, err)
			}
		case "run", "net", "priv", "server":
			if err != nil || plan.Mode != c.Mode || strings.Join(plan.Args[:len(c.Path)], " ") != strings.Join(c.Path, " ") {
				t.Errorf("%v: %+v %v", c.Path, plan, err)
			}
			if plan != nil && plan.Net != (c.Mode == "net") {
				t.Errorf("%v: keys for %s: %v", c.Path, c.Mode, plan.Net)
			}
		default:
			t.Errorf("%v: unknown mode %q", c.Path, c.Mode)
		}
	}
	for _, tc := range []struct {
		path  []string
		flags map[string]any
		mode  string
		code  string
	}{
		{[]string{"inspect"}, nil, "", "tty_only"},
		{[]string{"inspect"}, map[string]any{"json": true}, "run", ""},
		{[]string{"replay"}, map[string]any{"final": true}, "run", ""},
		{[]string{"replay"}, map[string]any{"record": "x.svg"}, "run", ""},
		{[]string{"sessions", "prune"}, nil, "run", ""},
		{[]string{"sessions", "prune"}, map[string]any{"yes": true}, "priv", ""},
		{[]string{"update"}, map[string]any{"check": true}, "net", ""},
		{[]string{"update"}, nil, "priv", ""},
		{[]string{"run"}, map[string]any{"mode": "yolo"}, "priv", ""},
		{[]string{"run"}, map[string]any{"mode": "plan"}, "net", ""},
		{[]string{"schedule", "add"}, map[string]any{"cron": "@daily", "mode": "bypass"}, "", "dangerous_mode"},
		{[]string{"web"}, nil, "", "tty_only"},
		{[]string{"nonsense"}, nil, "", "unknown_command"},
	} {
		pos := map[string]string{}
		if tc.path[0] == "schedule" {
			pos["GOAL"] = "g"
		}
		plan, err := rg.runs.Plan(t.Context(), tc.path, pos, tc.flags, "")
		if tc.code != "" {
			if we, _ := err.(*wire.Error); we == nil || we.Code != tc.code {
				t.Errorf("%v %v: %v, want %s", tc.path, tc.flags, err, tc.code)
			}
			continue
		}
		if err != nil || plan.Mode != tc.mode {
			t.Errorf("%v %v: %+v %v, want mode %s", tc.path, tc.flags, plan, err, tc.mode)
		}
	}
}

// Requests the command does not take are refused before anything runs.
func TestRunnerRefusesWhatTheCommandDoesNotTake(t *testing.T) {
	rg := newRig(t, Options{})
	for name, tc := range map[string]struct {
		body any
		code string
		st   int
	}{
		"no command":         {map[string]any{"path": []string{}}, "bad_flags", 400},
		"unknown flag":       {map[string]any{"path": []string{"sim"}, "flags": map[string]any{"bogus": 1}}, "bad_flags", 400},
		"bad int":            {map[string]any{"path": []string{"sim"}, "flags": map[string]any{"agents": "many"}}, "bad_flags", 400},
		"bool given text":    {map[string]any{"path": []string{"sim"}, "flags": map[string]any{"json": "maybe"}}, "bad_flags", 400},
		"dash positional":    {map[string]any{"path": []string{"recon"}, "pos": map[string]string{"DIR": "--help"}}, "bad_flags", 400},
		"unknown positional": {map[string]any{"path": []string{"recon"}, "pos": map[string]string{"X": "a"}}, "bad_flags", 400},
		"missing required":   {map[string]any{"path": []string{"friction"}}, "bad_flags", 400},
		"swarm needs N":      {map[string]any{"path": []string{"swarm"}, "pos": map[string]string{"N": "zero"}}, "bad_flags", 400},
		"NUL":                {map[string]any{"path": []string{"recon"}, "pos": map[string]string{"DIR": "a\x00b"}}, "bad_flags", 400},
		"unknown tab":        {map[string]any{"path": []string{"sim"}, "tab": "nope"}, "no_session", 404},
		"unknown field":      {map[string]any{"path": []string{"sim"}, "shell": "rm -rf /"}, "bad_json", 400},
		"chat":               {map[string]any{"path": []string{"chat"}}, "tty_only", 403},
	} {
		w := rg.do(req{method: "POST", path: "/api/runs", body: tc.body})
		if w.Code != tc.st || errCode(w) != tc.code {
			t.Errorf("%s: %d %s, want %d %s", name, w.Code, w.Body.String(), tc.st, tc.code)
		}
	}
	if kids := children(); len(kids) > 0 {
		t.Errorf("a refused request started a process: %v", kids)
	}
}

// The argument vector is built from the spec: path, flags in the spec's order as --name=value, then the positionals (swarm's N first).
func TestArgumentVector(t *testing.T) {
	rg := newRig(t, Options{})
	plan, err := rg.runs.Plan(t.Context(), []string{"swarm"}, map[string]string{"N": "3", "GOAL": "add a login page"},
		map[string]any{"verify": "go test ./...", "budget-usd": 2.5, "allow": []any{"tests", "Bash(go vet:*)"}, "trust-project": true}, "shop")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"swarm", "3", "--allow=tests", "--allow=Bash(go vet:*)", "--budget-usd=2.5", "--trust-project", "--verify=go test ./...", "add a login page"}
	if strings.Join(plan.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv %q\nwant %q", plan.Args, want)
	}
	if plan.Dir != rg.host.tabs[0].Cwd {
		t.Errorf("a run of a tab runs in its directory: %q", plan.Dir)
	}
	if plan.Cmdline != `sleipnir swarm 3 --allow=tests "--allow=Bash(go vet:*)" --budget-usd=2.5 --trust-project "--verify=go test ./..." "add a login page"` {
		t.Errorf("cmdline %s", plan.Cmdline)
	}
	plan, _ = rg.runs.Plan(t.Context(), []string{"trust", "add"}, nil, nil, "")
	if strings.Join(plan.Args, " ") != "trust add --yes" {
		t.Errorf("trust add asks on a terminal: the page's confirmation is the yes: %q", plan.Args)
	}
	plan, _ = rg.runs.Plan(t.Context(), []string{"sessions", "prune"}, nil, map[string]any{"older-than": "30d", "keep": "20"}, "")
	if strings.Join(plan.Args, " ") != "sessions prune" {
		t.Errorf("defaults are left out of the command line: %q", plan.Args)
	}
}

// A privileged run needs a confirmation for exactly its argument vector: without one 428 says which, a confirmation for another
// vector or a spent one is refused.
func TestPrivilegedRunNeedsItsConfirmation(t *testing.T) {
	rg := newRig(t, Options{})
	body := map[string]any{"path": []string{"sessions", "prune"}, "flags": map[string]any{"yes": true}}
	w := rg.do(req{method: "POST", path: "/api/runs", body: body})
	scope := w.Header().Get("X-Confirm-Scope")
	if w.Code != http.StatusPreconditionRequired || scope != "run:"+D16([]string{"sessions", "prune", "--yes"}) {
		t.Fatalf("no confirmation: %d %q %s", w.Code, scope, w.Body.String())
	}
	var e struct {
		Detail struct {
			Scope   string   `json:"scope"`
			Argv    []string `json:"argv"`
			Cmdline string   `json:"cmdline"`
		} `json:"detail"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	if e.Detail.Scope != scope || e.Detail.Cmdline != "sleipnir sessions prune --yes" {
		t.Errorf("detail %+v", e.Detail)
	}
	other := rg.confirm("run:" + D16([]string{"sessions", "prune"}))
	if w := rg.do(req{method: "POST", path: "/api/runs", body: body, header: map[string]string{"X-Confirm": other}}); w.Code != http.StatusForbidden {
		t.Errorf("a confirmation for another vector: %d", w.Code)
	}
	id := rg.confirm(scope)
	run := rg.start(body, map[string]string{"X-Confirm": id})
	rg.wait(run, 30*time.Second)
	if w := rg.do(req{method: "POST", path: "/api/runs", body: body, header: map[string]string{"X-Confirm": id}}); w.Code != http.StatusForbidden || errCode(w) != "confirm_invalid" {
		t.Errorf("a spent confirmation: %d %s", w.Code, w.Body.String())
	}
	if got := rg.waitLine(run, "argv:", time.Second); got != "argv: sessions|prune|--yes" {
		t.Errorf("ran %q", got)
	}
}

// Output arrives in run frames, out and err apart; the end is one critical frame with the exit and the time; the run is listed and
// its lines can be fetched again.
func TestRunStreamsOutputAndEnds(t *testing.T) {
	rg := newRig(t, Options{}, "RUNNER_FAKE_LINES=12")
	id := rg.start(map[string]any{"path": []string{"sim"}}, nil)
	end := rg.wait(id, 30*time.Second)
	if end.Result.Exit != 0 || end.Result.Canceled {
		t.Errorf("result %+v", end.Result)
	}
	lines := rg.lines(id)
	var outs, errs int
	for _, l := range lines {
		switch l.K {
		case "out":
			outs++
		case "err":
			errs++
		default:
			t.Errorf("kind %q: a line of a run is out or err", l.K)
		}
	}
	if outs != 12 || errs != 1 {
		t.Errorf("%d out, %d err lines: %+v", outs, errs, lines)
	}
	if _, critical := rg.host.runFrames(id); !critical {
		t.Error("the end of a run is critical")
	}
	w := rg.do(req{method: "GET", path: "/api/runs"})
	var list struct {
		Runs []RunInfo `json:"runs"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Runs) != 1 || list.Runs[0].ID != id || list.Runs[0].Running || list.Runs[0].Exit == nil || list.Runs[0].Cmdline != "sleipnir sim" {
		t.Errorf("runs %s", w.Body.String())
	}
	w = rg.do(req{method: "GET", path: "/api/runs/" + id + "/output?from=10"})
	var out RunOutput
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || len(out.Lines) != 3 || out.Next != 13 || out.Running || out.Result == nil {
		t.Errorf("output from 10: %s", w.Body.String())
	}
	for _, bad := range []string{"/api/runs/r_nope/output", "/api/runs/" + id + "/output?from=-1"} {
		if w := rg.do(req{method: "GET", path: bad}); w.Code != 400 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	if w := rg.do(req{method: "GET", path: "/api/runs/r_aaaaaaaaaaaaaaaa/output"}); w.Code != 404 {
		t.Errorf("unknown run: %d", w.Code)
	}
}

// A stop ends the command and what it started (its process group): nothing is left behind.
func TestRunnerKillsProcessGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the test reads /proc to see the processes")
	}
	rg := newRig(t, Options{Grace: time.Second})
	id := rg.start(map[string]any{"path": []string{"recon"}}, nil)
	line := rg.waitLine(id, "grandchild ", 30*time.Second)
	pid, err := strconv.Atoi(strings.TrimPrefix(line, "grandchild "))
	if err != nil || !alive(pid) {
		t.Fatalf("grandchild %q alive=%v", line, alive(pid))
	}
	if w := rg.do(req{method: "DELETE", path: "/api/runs/" + id}); w.Code != 200 {
		t.Fatalf("stop: %d %s", w.Code, w.Body.String())
	}
	end := rg.wait(id, 30*time.Second)
	if !end.Result.Canceled {
		t.Errorf("result %+v", end.Result)
	}
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		t.Errorf("the command's child %d outlived the stop", pid)
	}
	if w := rg.do(req{method: "DELETE", path: "/api/runs/" + id}); w.Code != 200 {
		t.Errorf("stopping an ended run is no error: %d", w.Code)
	}
	if w := rg.do(req{method: "DELETE", path: "/api/runs/r_aaaaaaaaaaaaaaaa"}); w.Code != 404 {
		t.Errorf("unknown run: %d", w.Code)
	}
}

// A server runs until it is stopped; at most MaxRuns go at once.
func TestServerModeAndTheCap(t *testing.T) {
	rg := newRig(t, Options{MaxRuns: 2, Grace: time.Second})
	a := rg.start(map[string]any{"path": []string{"mock"}}, nil)
	b := rg.start(map[string]any{"path": []string{"mock"}, "keep": true}, nil)
	rg.waitLine(a, "serving", 30*time.Second)
	rg.waitLine(b, "serving", 30*time.Second)
	if w := rg.do(req{method: "POST", path: "/api/runs", body: map[string]any{"path": []string{"sim"}}}); w.Code != http.StatusConflict || errCode(w) != "busy" {
		t.Errorf("a third run: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-rg.runs.Done(a):
		t.Fatal("a server ended by itself")
	case <-time.After(200 * time.Millisecond):
	}
	var list struct {
		Runs []RunInfo `json:"runs"`
	}
	_ = json.Unmarshal(rg.do(req{method: "GET", path: "/api/runs"}).Body.Bytes(), &list)
	kept := 0
	for _, r := range list.Runs {
		if r.Keep && r.Running {
			kept++
		}
	}
	if kept != 1 {
		t.Errorf("the kept run: %+v", list.Runs)
	}
	for _, id := range []string{a, b} {
		rg.do(req{method: "DELETE", path: "/api/runs/" + id})
		if end := rg.wait(id, 30*time.Second); !end.Result.Canceled {
			t.Errorf("%s: %+v", id, end.Result)
		}
	}
	rg.wait(rg.start(map[string]any{"path": []string{"sim"}}, nil), 30*time.Second)
}

// Lines are cut at the line limit, a run's lines at the run limit, and a run that outlives its limit is stopped (a server has none).
func TestOutputAndTimeLimits(t *testing.T) {
	rg := newRig(t, Options{MaxLines: 50, MaxLineBytes: 64, Timeout: 4 * time.Second, Grace: time.Second}, "RUNNER_FAKE_LINES=200", "RUNNER_FAKE_SLEEP=10m")
	id := rg.start(map[string]any{"path": []string{"sim"}}, nil)
	rg.wait(id, 30*time.Second)
	lines := rg.lines(id)
	if len(lines) != 51 || !strings.Contains(lines[50].T, "more lines were not shown") {
		t.Errorf("%d lines, last %+v", len(lines), lines[len(lines)-1])
	}
	id = rg.start(map[string]any{"path": []string{"friction"}, "pos": map[string]string{"PATH": "x"}}, nil)
	rg.wait(id, 30*time.Second)
	lines = rg.lines(id)
	if len(lines) != 2 || len(lines[0].T) > 64+len(" …[line cut]") || !strings.HasSuffix(lines[0].T, "…[line cut]") || lines[1].T != "after" {
		t.Errorf("a long line: %+v", lines)
	}
	server := rg.start(map[string]any{"path": []string{"mock"}, "flags": map[string]any{"addr": "127.0.0.1:0"}}, nil)
	slow := rg.start(map[string]any{"path": []string{"run"}, "pos": map[string]string{"PROMPT": "wait"}}, nil)
	end := rg.wait(slow, 30*time.Second)
	if end.Result.Canceled || end.Result.Exit == 0 || !strings.Contains(rg.waitLine(slow, "stopped after", time.Second), "4s") {
		t.Errorf("a run past its limit: %+v %+v", end.Result, rg.lines(slow))
	}
	select {
	case <-rg.runs.Done(server):
		t.Fatal("a server has no time limit")
	default:
	}
	rg.do(req{method: "DELETE", path: "/api/runs/" + server})
	rg.wait(server, 30*time.Second)
}

// The keys the server holds reach a "net" command through its environment only, never its arguments; a "run" command does not get
// them; and a key a command prints never reaches the page.
func TestRunnerNoKeyInArgv(t *testing.T) {
	const name, key = "RUNNERTEST_API_KEY", "sk-test-runner-0123456789abcdefghij"
	t.Setenv(name, "")
	harden.Provide(name, key)
	t.Cleanup(func() { harden.Provide(name, "") })
	rg := newRig(t, Options{})
	net := rg.start(map[string]any{"path": []string{"models"}}, nil)
	rg.wait(net, 30*time.Second)
	run := rg.start(map[string]any{"path": []string{"config"}}, nil)
	rg.wait(run, 30*time.Second)
	if got := rg.waitLine(net, "key:", time.Second); got != "key: [redacted]" {
		t.Errorf("net command: %q (the key reaches it, and is masked in the page)", got)
	}
	if got := rg.waitLine(run, "key:", time.Second); strings.TrimSpace(got) != "key:" {
		t.Errorf("a run command got a key: %q", got)
	}
	if got := rg.waitLine(net, "argv:", time.Second); strings.Contains(got, "sk-") {
		t.Errorf("argv %q", got)
	}
	rg.host.mu.Lock()
	b, _ := json.Marshal(rg.host.frames)
	rg.host.mu.Unlock()
	if strings.Contains(string(b), key) || strings.Contains(rg.log(), key) {
		t.Error("the key reached a frame or the log")
	}
	for _, path := range []string{"/api/runs", "/api/runs/" + net + "/output", "/api/cli"} {
		if w := rg.do(req{method: "GET", path: path}); strings.Contains(w.Body.String(), key) {
			t.Errorf("%s holds the key", path)
		}
	}
}

// The envelope guards every route of the runner.
func TestRunnerRoutesSecurityMatrix(t *testing.T) {
	rg := newRig(t, Options{})
	body := map[string]any{"path": []string{"version"}}
	for name, tc := range map[string]struct {
		q    req
		st   int
		code string
	}{
		"no credential":        {req{method: "POST", path: "/api/runs", body: body, noAuth: true}, 401, "unauthenticated"},
		"no credential, GET":   {req{method: "GET", path: "/api/runs", noAuth: true}, 401, "unauthenticated"},
		"cli without auth":     {req{method: "GET", path: "/api/cli", noAuth: true}, 401, "unauthenticated"},
		"another origin":       {req{method: "POST", path: "/api/runs", body: body, header: map[string]string{"Origin": "http://127.0.0.1:7000"}}, 403, "forbidden_origin"},
		"no custom header":     {req{method: "POST", path: "/api/runs", body: body, header: map[string]string{"X-Sleipnir-Web": ""}}, 403, "csrf"},
		"text/plain":           {req{method: "POST", path: "/api/runs", body: body, header: map[string]string{"Content-Type": "text/plain"}}, 415, "unsupported_media_type"},
		"oversized body":       {req{method: "POST", path: "/api/runs", raw: `{"path":["version"],"pos":{"X":"` + strings.Repeat("a", 70<<10) + `"}}`}, 413, "body_too_large"},
		"cross-site":           {req{method: "POST", path: "/api/runs", body: body, header: map[string]string{"Sec-Fetch-Site": "cross-site"}}, 403, "forbidden_site"},
		"stop with a body":     {req{method: "DELETE", path: "/api/runs/r_aaaaaaaaaaaaaaaa", body: body}, 400, "bad_request"},
		"wrong method on runs": {req{method: "PUT", path: "/api/runs", body: body}, 405, "method_not_allowed"},
	} {
		w := rg.do(tc.q)
		if w.Code != tc.st || errCode(w) != tc.code {
			t.Errorf("%s: %d %s, want %d %s", name, w.Code, w.Body.String(), tc.st, tc.code)
		}
	}
}

// GET /api/cli serves the spec, every command with a mode.
func TestCLIRoute(t *testing.T) {
	rg := newRig(t, Options{})
	w := rg.do(req{method: "GET", path: "/api/cli"})
	var spec wire.CLISpec
	if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil || w.Code != 200 || len(spec.Commands) < 60 {
		t.Fatalf("%d %v %d", w.Code, err, len(spec.Commands))
	}
	for _, c := range spec.Commands {
		if c.Mode == "" {
			t.Errorf("%v has no mode", c.Path)
		}
	}
}

// The confirmation scope of a vector is what a page computes with JSON.stringify and SHA-256.
func TestD16IsJSONStringifyThenSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte(`["sessions","prune","--yes"]`))
	if got := D16([]string{"sessions", "prune", "--yes"}); got != hex.EncodeToString(sum[:])[:16] {
		t.Errorf("D16 = %s", got)
	}
	for in, want := range map[string]string{
		`a"b\c`: `"a\"b\\c"`, "\b\f\n\r\t\x01": `"\b\f\n\r\t\u0001"`, "<&>": `"<&>"`, "é ": "\"é \"",
	} {
		if got := JSONString(in); got != want {
			t.Errorf("JSONString(%q) = %s, want %s", in, got, want)
		}
	}
}

// Past either limit (lines or bytes) a line is neither sent nor kept: the page's frames and a reattach show the same lines, and the
// closing line counts exactly the ones left out.
func TestOutputLimitsAgreeLiveAndKept(t *testing.T) {
	for name, o := range map[string]Options{
		"bytes": {MaxLines: 20000, MaxBytes: 64 << 10},
		"lines": {MaxLines: 100, MaxBytes: 4 << 20},
	} {
		t.Run(name, func(t *testing.T) {
			rg := newRig(t, o)
			const n, width = 6000, 300
			started, err := rg.runs.Start(Spec{Path: []string{"x"}, Func: func(_ context.Context, stdout, _ io.Writer) int {
				line := strings.Repeat("y", width) + "\n"
				for range n {
					_, _ = io.WriteString(stdout, line)
				}
				return 0
			}})
			if err != nil {
				t.Fatal(err)
			}
			rg.wait(started.ID, 30*time.Second)
			live := rg.lines(started.ID)
			out, ok := rg.runs.Output(started.ID, 0)
			if !ok {
				t.Fatal("no output")
			}
			if len(live) != len(out.Lines) {
				t.Fatalf("sent %d lines, kept %d", len(live), len(out.Lines))
			}
			shown := len(live) - 1
			note := live[len(live)-1].T
			if want := fmt.Sprintf("[%d more lines were not shown]", n-shown); note != want || out.Lines[len(out.Lines)-1].T != want {
				t.Errorf("closing line %q, want %q (%d shown)", note, want, shown)
			}
			if shown*width > o.MaxBytes || shown > o.MaxLines {
				t.Errorf("%d lines of %d bytes passed the limits %+v", shown, width, o)
			}
		})
	}
}
