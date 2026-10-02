package main

// The commands other than chat, run the way a person runs them: as a process, with its
// arguments, its streams and its exit status. Each test asserts the shape of what a command
// prints on which stream, and the status, and never how long it took.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

// assertRun checks a command's status and that each stream contains (or, with a leading !,
// does not contain) the given texts.
func assertRun(t *testing.T, r result, code int, stdout, stderr []string) {
	t.Helper()
	if r.code != code {
		t.Errorf("exit status %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.stdout, r.stderr)
	}
	check := func(stream, text string, wants []string) {
		for _, want := range wants {
			if neg, ok := strings.CutPrefix(want, "!"); ok {
				if strings.Contains(text, neg) {
					t.Errorf("%s has %q:\n%s", stream, neg, text)
				}
			} else if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q:\n%s", stream, want, text)
			}
		}
	}
	check("stdout", r.stdout, stdout)
	check("stderr", r.stderr, stderr)
}

// decode unmarshals s into v or fails the test.
func decode(t *testing.T, what, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("%s is not JSON (%v):\n%s", what, err, s)
	}
}

// The status of a command says how it ended: 0 for success and for help that was asked for,
// 1 for a command that failed, 2 for a command line that could not be understood.
func TestCommandExitStatus(t *testing.T) {
	m := startModel(t)
	w := newWorld(t, m.url())
	for _, tc := range []struct {
		name           string
		args           []string
		code           int
		stdout, stderr []string
	}{
		{"no command", nil, 2, nil, []string{"Usage:", "Commands:"}},
		{"an unknown command", []string{"bogus"}, 2, []string{"!Commands:"}, []string{`unknown command "bogus"`, "Commands:"}},
		{"help", []string{"help"}, 0, []string{"Commands:", "chat", "run"}, []string{"!Commands:"}},
		{"--help", []string{"--help"}, 0, []string{"Commands:"}, nil},
		{"-h", []string{"-h"}, 0, []string{"Commands:"}, nil},
		{"version", []string{"version"}, 0, []string{"sleipnir dev (none)"}, nil},
		{"--version", []string{"--version"}, 0, []string{"sleipnir dev (none)"}, nil},
		{"-v", []string{"-v"}, 0, []string{"sleipnir dev (none)"}, nil},
		{"run -h", []string{"run", "-h"}, 0, nil, []string{"usage: sleipnir run", "-budget-usd"}},
		{"chat -h", []string{"chat", "-h"}, 0, nil, []string{"Usage of chat", "-resume"}},
		{"an unknown flag of run", []string{"run", "--bogus", "x"}, 2, nil, []string{"flag provided but not defined: -bogus", "usage: sleipnir run"}},
		{"an unknown flag of chat", []string{"chat", "--bogus"}, 2, nil, []string{"flag provided but not defined: -bogus", "Usage of chat"}},
		{"an unknown flag of init", []string{"init", "--bogus"}, 2, nil, []string{"flag provided but not defined: -bogus"}},
		{"a malformed value of a flag", []string{"run", "--max-steps", "many", "x"}, 2, nil, []string{"invalid value", "max-steps"}},
		{"run with no goal", []string{"run"}, 1, nil, []string{"sleipnir: run: a prompt is required"}},
		{"swarm with a bad count", []string{"swarm", "many", "goal"}, 1, nil, []string{"the first argument is the number of workers"}},
		{"doctor without a model", []string{"doctor"}, 1, nil, []string{"sleipnir: doctor: --model is required"}},
		{"doctor without an endpoint", []string{"doctor", "--model", "m", "--provider", "custom"}, 1, nil, []string{`provider "custom" needs --base-url`}},
		{"init --local-url without --user", []string{"init", "--local-url", "http://127.0.0.1:1/v1"}, 1, nil, []string{"--local-url goes with --user"}},
		{"demo with too few topics", []string{"demo", "--topics", "1"}, 1, nil, []string{"demo: --topics must be between 2 and 32"}},
		{"sim with an unknown mode", []string{"sim", "--mode", "nope"}, 1, nil, []string{`sim: unknown --mode "nope"`}},
		{"sim with an unknown provider", []string{"sim", "--provider", "nope"}, 1, nil, []string{`sim: unknown --provider "nope"`}},
		{"inspect with no sessions to show", []string{"inspect"}, 1, nil, []string{"inspect: no sessions yet"}},
		{"inspect of two things", []string{"inspect", "a", "b"}, 1, nil, []string{"inspect: at most one session or directory is taken", "usage: sleipnir inspect"}},
		{"models with an unknown provider", []string{"models", "--provider", "nope"}, 1, nil, []string{`models: unknown provider "nope"`}},
		{"sessions with nothing recorded", []string{"sessions", "--dir", filepath.Join(w.tmp, "none")}, 0, nil, []string{"no sessions yet"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRun(t, w.run("", tc.args...), tc.code, tc.stdout, tc.stderr)
		})
	}
}

// init writes the starter files of a project, never overwrites, and with --user writes the
// person's own configuration with a private mode.
func TestInit(t *testing.T) {
	t.Run("a project", func(t *testing.T) {
		w := newWorld(t, "")
		if err := os.RemoveAll(filepath.Join(w.project, ".sleipnir")); err != nil {
			t.Fatal(err)
		}
		r := w.run("", "init", "--model", "heimdall/vendor/model")
		assertRun(t, r, 0, nil, []string{"wrote ", "config.json", "AGENTS.md", "sleipnir init --user", "next: sleipnir doctor"})
		if r.stdout != "" {
			t.Errorf("init printed on stdout: %q", r.stdout)
		}
		var cfg struct {
			Models      struct{ Default string }
			Permissions struct{ Deny []string }
			Swarm       struct {
				MaxAgents int `json:"max_agents"`
			}
		}
		b, err := os.ReadFile(filepath.Join(w.project, ".sleipnir", "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		decode(t, "config.json", string(b), &cfg)
		if cfg.Models.Default != "heimdall/vendor/model" || len(cfg.Permissions.Deny) != 2 || cfg.Swarm.MaxAgents != 12 {
			t.Errorf("config.json: %s", b)
		}
		if _, err := os.Stat(filepath.Join(w.project, "AGENTS.md")); err != nil {
			t.Error(err)
		}
		if b, _ := os.ReadFile(filepath.Join(w.project, ".gitignore")); !strings.Contains(string(b), ".sleipnir/config.local.json") {
			t.Errorf(".gitignore: %q", b)
		}
		// Never overwrites.
		assertRun(t, w.run("", "init"), 1, nil, []string{"already exists", "sleipnir config"})
		// The result loads.
		assertRun(t, w.run("", "config"), 0, nil, []string{"configuration is valid"})
	})
	t.Run("the user's own", func(t *testing.T) {
		w := newWorld(t, "")
		r := w.run("", "init", "--user", "--model", "heimdall/vendor/model", "--local-url", "http://127.0.0.1:8000/v1")
		assertRun(t, r, 0, nil, []string{"wrote ", filepath.Join(w.home, ".sleipnir", "config.json")})
		path := filepath.Join(w.home, ".sleipnir", "config.json")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Models      struct{ Default string }
			Permissions struct{ Mode string }
			Providers   map[string]struct {
				BaseURL string `json:"base_url"`
				Options map[string]any
			}
		}
		decode(t, "the user's config.json", string(b), &cfg)
		if cfg.Models.Default != "heimdall/vendor/model" || cfg.Permissions.Mode != "default" ||
			cfg.Providers["local"].BaseURL != "http://127.0.0.1:8000/v1" || cfg.Providers["local"].Options["capture_tokens"] != true {
			t.Errorf("the user's config.json: %s", b)
		}
		if runtime.GOOS != "windows" {
			if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("the mode of the user's configuration is %v (%v), want 0600", fi.Mode().Perm(), err)
			}
		}
		assertRun(t, w.run("", "init", "--user"), 1, nil, []string{"already exists"})
	})
}

// config says where each value came from, prints the merged result with --json, and stops
// on a file it cannot use.
func TestConfig(t *testing.T) {
	m := startModel(t)
	w := newWorld(t, m.url())

	r := w.run("", "config")
	assertRun(t, r, 0, []string{"!{"}, []string{
		"layers (lowest precedence first):", "user", "config.json (found)", "project", "(not found)",
		"providers", "models", "configuration is valid", "swarm.isolation: none", "swarm.mailman: off",
	})

	r = w.run("", "config", "--json")
	assertRun(t, r, 0, nil, nil)
	if r.stderr != "" {
		t.Errorf("config --json printed on stderr: %q", r.stderr)
	}
	var cfg struct {
		Models    struct{ Default string }
		Providers map[string]struct {
			BaseURL string `json:"base_url"`
		}
		Permissions struct{ Mode string }
	}
	decode(t, "config --json", r.stdout, &cfg)
	if cfg.Models.Default != "mock/mock-1" || cfg.Providers["mock"].BaseURL != m.url() || cfg.Permissions.Mode != "default" {
		t.Errorf("config --json: %s", r.stdout)
	}

	// A project file that cannot be used stops the command, and says where and what.
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"a wrong type", `{"swarm": {"max_agents": "lots"}}`, []string{"config.json:1:", "swarm.max_agents", "expected an integer"}},
		{"a syntax error", "{\n  \"swarm\": {\n    \"max_agents\": 3,,\n  }\n}\n", []string{"config.json:3:"}},
		// Not "allow": the keys a project may not set without trust are dropped before they are
		// read, so a typo in one is not reported.
		{"a rule that does not parse", `{"permissions": {"deny": ["Bash(git status"]}}`, []string{"permissions.deny[0]", "missing closing parenthesis"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(w.project, ".sleipnir", "config.json"), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			assertRun(t, w.run("", "config"), 1, nil, append([]string{"sleipnir: "}, tc.want...))
			assertRun(t, w.run("", "config", "--json"), 1, []string{"!{"}, tc.want)
			// Nothing runs on a configuration that cannot be read.
			assertRun(t, w.run("", "run", "--no-web", "@hello"), 1, nil, tc.want)
		})
	}
}

// run prints the answer on stdout and the progress and the summary on stderr, so that its
// output can be redirected; --json and --quiet change what goes where.
func TestRunOutput(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	m.on("@tool", runs("echo from-the-tool"))
	w := newWorld(t, m.url())

	t.Run("text", func(t *testing.T) {
		r := w.run("", "run", "@hello")
		assertRun(t, r, 0, nil, []string{"sleipnir dev · mock-1 · session ", "── ", " · 1 steps · $", " · cache hit ", " · 0 compactions · "})
		if strings.TrimSpace(r.stdout) != "hi there" {
			t.Errorf("stdout is %q: the answer and nothing else", r.stdout)
		}
	})
	t.Run("quiet", func(t *testing.T) {
		r := w.run("", "run", "--quiet", "@hello")
		if r.code != 0 || r.stderr != "" {
			t.Errorf("quiet: exit %d\nstdout:\n%q\nstderr:\n%q", r.code, r.stdout, r.stderr)
		}
		// docs/CLI.md: "--quiet prints only the final answer". It printed nothing: the sink is agent.NopSink
		// and the result's text was never written.
		if strings.TrimSpace(r.stdout) != "hi there" {
			t.Errorf("run --quiet prints %q on stdout, want the final answer", r.stdout)
		}
	})
	t.Run("the prompt from standard input", func(t *testing.T) {
		for _, args := range [][]string{{"run", "-"}, {"run"}} {
			r := w.run("@hello\n", args...)
			if r.code != 0 || strings.TrimSpace(r.stdout) != "hi there" {
				t.Errorf("%v: exit %d\nstdout:\n%q\nstderr:\n%s", args, r.code, r.stdout, r.stderr)
			}
		}
	})
	t.Run("flags may follow the prompt", func(t *testing.T) {
		r := w.run("", "run", "@hello", "--verbose")
		if r.code != 0 || strings.TrimSpace(r.stdout) != "hi there" {
			t.Errorf("exit %d\nstdout:\n%q\nstderr:\n%q", r.code, r.stdout, r.stderr)
		}
	})
	t.Run("json", func(t *testing.T) {
		r := w.run("", "run", "--json", "@tool")
		if r.code != 0 || r.stderr != "" {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
		}
		types := map[string]int{}
		var last map[string]any
		sc := bufio.NewScanner(strings.NewReader(r.stdout))
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var ev map[string]any
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				t.Fatalf("stdout must be JSON lines only, got %q", sc.Text())
			}
			types[fmt.Sprint(ev["type"])]++
			last = ev
		}
		for _, want := range []string{"text", "tool_start", "tool_end", "response", "result"} {
			if types[want] == 0 {
				t.Errorf("no %q event in:\n%s", want, r.stdout)
			}
		}
		if types["result"] != 1 || last["type"] != "result" {
			t.Fatalf("the result must be the last event, once:\n%s", r.stdout)
		}
		if !strings.Contains(fmt.Sprint(last["text"]), "from-the-tool") || last["steps"] != float64(2) || last["stop"] != "end_turn" || last["error"] != "" {
			t.Errorf("result event: %v", last)
		}
		if dir, _ := last["dir"].(string); dir == "" || !exists(filepath.Join(dir, "events.jsonl")) {
			t.Errorf("the result names no session directory with a log: %v", last["dir"])
		}
	})
	t.Run("a spent budget is an error", func(t *testing.T) {
		// The first request costs more than this, so the second one (after the tool) is not made.
		r := w.run("", "run", "--budget-usd", "0.001", "@tool")
		assertRun(t, r, 1, nil, []string{"sleipnir: stopped: the budget of $0.00 is exhausted", "raise it with --budget-usd"})
	})
	t.Run("sessions lists what ran", func(t *testing.T) {
		r := w.run("", "sessions")
		assertRun(t, r, 0, nil, []string{"can be continued: sleipnir chat --resume"})
		if n := strings.Count(r.stdout, "mock-1"); n < 5 {
			t.Errorf("sessions lists %d sessions of mock-1, want at least the five runs above:\n%s", n, r.stdout)
		}
		if !regexp.MustCompile(`(?m)^↺ \d{8}-\d{6}-[0-9a-f]+ +mock-1 +\$\d+\.\d{4} +@hello$`).MatchString(r.stdout) {
			t.Errorf("no line of the expected shape in:\n%s", r.stdout)
		}
	})
}

// With no one to ask, a run refuses what needs approval and tells the model.
func TestRunUnattendedRefusesWhatNeedsApproval(t *testing.T) {
	m := startModel(t)
	m.on("@write", writes("approved.txt"))
	w := newWorld(t, m.url())
	r := w.run("", "run", "@write")
	assertRun(t, r, 0, nil, []string{"✗ write approved.txt", "!allow?"})
	if exists(filepath.Join(w.project, "approved.txt")) {
		t.Error("the write was done")
	}
	if res := m.toolResults(); len(res) != 1 || !strings.Contains(res[0], "approval required") {
		t.Errorf("the model was told %q", res)
	}
}

// A run with nobody to ask says at its end which commands it refused for that, and how to let them through; --allow is the answer, given
// before the run (a dogfood session could not run the project's tests, and the only trace was a line cut off in the middle of its advice).
func TestRunUnattendedSaysWhatItRefusedAndAllowAnswersIt(t *testing.T) {
	m := startModel(t)
	m.on("@touch", runs("touch ran.marker"))
	w := newWorld(t, m.url())
	r := w.run("", "run", "@touch")
	assertRun(t, r, 0, nil, []string{"refused, because this run had no one to ask:", "touch ran.marker", "--allow 'Bash(touch:*)'", "--allow tests"})
	if exists(filepath.Join(w.project, "ran.marker")) {
		t.Fatal("the command ran although nothing could approve it")
	}

	r = w.run("", "run", "--allow", "Bash(touch:*)", "@touch")
	assertRun(t, r, 0, nil, []string{"!refused, because", "!approval required", "✓ bash touch ran.marker"})
	if !exists(filepath.Join(w.project, "ran.marker")) {
		t.Error("the command that --allow approved did not run")
	}

	// A rule that cannot be read stops the run before it starts, and says which.
	r = w.run("", "run", "--allow", "Bash(touch", "@touch")
	if r.code == 0 || !strings.Contains(r.stderr, "closing parenthesis") {
		t.Errorf("a malformed rule must be refused with its reason (status %d):\n%s", r.code, r.stderr)
	}
}

// recon prints the survey that seeds the prompt, and its size on stderr.
func TestRecon(t *testing.T) {
	w := newWorld(t, "")
	if err := os.WriteFile(filepath.Join(w.project, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRun(t, w.run("", "recon"), 0, []string{"## project", "Layout"}, []string{"tokens,", "files considered]"})
}

// demo runs a scripted team on the built-in mock and leaves a session that inspect reads.
func TestDemoAndInspect(t *testing.T) {
	w := newWorld(t, "")
	dir := filepath.Join(w.tmp, "demo")
	r := w.run("", "demo", "--topics", "2", "--dir", dir)
	assertRun(t, r, 0, []string{
		"Sleipnir demo:", "a 2-topic handbook", "agents ", "model requests", "hit ratio", "cost ",
		"Workspace: ", "Recorded session: ", "Try it on a real model",
	}, nil)
	session := filepath.Join(dir, "session")
	for _, p := range []string{filepath.Join(dir, "handbook"), filepath.Join(session, "events.jsonl")} {
		if !exists(p) {
			t.Errorf("demo left no %s", p)
		}
	}

	t.Run("inspect --json", func(t *testing.T) {
		r := w.run("", "inspect", "--json", session)
		assertRun(t, r, 0, nil, nil)
		var sum struct {
			Session struct {
				ID, Model string
				Swarm     bool
			}
		}
		decode(t, "inspect --json", r.stdout, &sum)
		if sum.Session.ID == "" || sum.Session.Model != "demo-model" || !sum.Session.Swarm {
			t.Errorf("inspect --json: %s", r.stdout)
		}
	})

	t.Run("the dashboard", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("an interrupt cannot be sent to a process on Windows")
		}
		c := w.cmd("inspect", "--addr", "127.0.0.1:0", session)
		stdout, err := c.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		c.Stderr = &stderr
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		t.Cleanup(func() { _ = c.Process.Kill() })

		// The first line on stdout is the address.
		urlc := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(stdout).ReadString('\n')
			urlc <- strings.TrimSpace(line)
		}()
		var url string
		select {
		case url = <-urlc:
		case <-time.After(e2eGuard):
			t.Fatalf("inspect printed no address in %v:\n%s", e2eGuard, stderr.String())
		}
		if !regexp.MustCompile(`^http://127\.0\.0\.1:[1-9]\d*/$`).MatchString(url) {
			t.Fatalf("the address is %q: want the port that was bound, on loopback", url)
		}
		hc := &http.Client{Timeout: e2eGuard}
		get := func(path string) (int, string, string) {
			resp, err := hc.Get(url + path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
		}
		if code, typ, body := get(""); code != 200 || !strings.HasPrefix(typ, "text/html") || !strings.Contains(body, "<html") {
			t.Errorf("GET /: %d %s %.80q", code, typ, body)
		}
		if code, typ, body := get("api/summary"); code != 200 || !strings.HasPrefix(typ, "application/json") || !strings.Contains(body, `"demo-model"`) {
			t.Errorf("GET /api/summary: %d %s %.120q", code, typ, body)
		}
		if code, _, _ := get("no-such-page"); code != 404 {
			t.Errorf("GET /no-such-page: %d, want 404", code)
		}
		if resp, err := hc.Post(url, "text/plain", strings.NewReader("x")); err != nil || resp.StatusCode != 405 {
			t.Errorf("POST /: %v %v, want 405 (the server answers GET only)", resp, err)
		} else {
			resp.Body.Close()
		}

		// Ctrl-C stops the dashboard, and that is a success.
		if err := c.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("inspect after an interrupt: %v\n%s", err, stderr.String())
			}
		case <-time.After(e2eGuard):
			t.Fatalf("inspect did not stop after an interrupt:\n%s", stderr.String())
		}
		if s := stderr.String(); !strings.Contains(s, "serving "+session) || !strings.Contains(s, "Ctrl-C to stop") {
			t.Errorf("stderr: %q", s)
		}
	})
}

// A marketplace-shaped catalogue (Heimdall's and OpenRouter's): prices are per token, as decimal
// strings, and architecture.modality says what a model takes and gives. The mock provider's own
// catalogue has no modality, so `models` would print it only with --all; this one has the fields
// `models` filters on.
const catalogue = `{"data":[
 {"id":"vendor/chat-small","context_length":131072,"architecture":{"modality":"text->text"},
  "pricing":{"prompt":"0.0000005","completion":"0.000002","input_cache_read":"0.0000001"},"supported_parameters":["tools","reasoning_effort"]},
 {"id":"vendor/chat-large","context_length":1048576,"architecture":{"modality":"text+image->text"},
  "pricing":{"prompt":"0.000003","completion":"0.000015"},"supported_parameters":["tools"]},
 {"id":"vendor/embedder","context_length":8192,"architecture":{"modality":"text->embedding"},
  "pricing":{"prompt":"0.0000001","completion":"0"}}
]}`

// models prints a catalogue as a table, chat models only unless --all.
func TestModels(t *testing.T) {
	var broken atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path != "/v1/models":
			http.NotFound(rw, r)
		case broken.Load():
			http.Error(rw, "no", http.StatusInternalServerError)
		default:
			io.WriteString(rw, catalogue)
		}
	}))
	defer ts.Close()
	w := newWorld(t, "")
	base := []string{"models", "--provider", "custom", "--base-url", ts.URL + "/v1"}

	r := w.run("", base...)
	assertRun(t, r, 0, []string{"vendor/chat-large", "vendor/chat-small", "!vendor/embedder"}, nil)
	if r.stderr != "" {
		t.Errorf("stderr: %q", r.stderr)
	}
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if len(lines) != 3 || !regexp.MustCompile(`^MODEL +CONTEXT +\$/M IN +\$/M CACHED +\$/M OUT +TOOLS +REASONING$`).MatchString(lines[0]) {
		t.Fatalf("a header and two models, sorted by id, wanted:\n%s", r.stdout)
	}
	if !regexp.MustCompile(`^vendor/chat-large +1\.0M +3\.0000 +3\.0000 +15\.0000 +true +false$`).MatchString(lines[1]) ||
		!regexp.MustCompile(`^vendor/chat-small +131k +0\.5000 +0\.1000 +2\.0000 +true +true$`).MatchString(lines[2]) {
		t.Errorf("rows:\n%s", r.stdout)
	}
	assertRun(t, w.run("", append(base, "--all")...), 0, []string{"vendor/chat-large", "vendor/chat-small", "vendor/embedder"}, nil)
	r = w.run("", append(base, "--filter", "small")...)
	assertRun(t, r, 0, []string{"vendor/chat-small", "!vendor/chat-large"}, nil)

	// A catalogue that cannot be fetched is an error, on stderr, with status 1.
	broken.Store(true)
	assertRun(t, w.run("", base...), 1, []string{"!MODEL"}, []string{"sleipnir: ", "http 500"})
}

// A catalogue in the OpenAI style says nothing about what its models take and give. Every model of one was filtered out as "not a
// chat model", and the command printed a header and an empty table.
func TestModelsOfACatalogueThatSaysNothingAboutModalities(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(rw, r)
			return
		}
		io.WriteString(rw, `{"object":"list","data":[{"id":"qwen3:8b","object":"model","owned_by":"library"},{"id":"gpt-x","object":"model","owned_by":"openai"}]}`)
	}))
	defer ts.Close()
	w := newWorld(t, "")
	r := w.run("", "models", "--provider", "custom", "--base-url", ts.URL+"/v1")
	assertRun(t, r, 0, []string{"MODEL", "gpt-x", "qwen3:8b"}, nil)
}

// doctor probes an endpoint with real requests and reports what it found; here the endpoint
// is the mock, which is cache-faithful and streams.
func TestDoctorAgainstTheMock(t *testing.T) {
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, nil)
	ts := srv.Start()
	defer ts.Close()
	w := newWorld(t, "")
	base := []string{"doctor", "--model", "mock-1", "--provider", "custom", "--base-url", ts.URL + "/v1"}

	r := w.run("", base...)
	assertRun(t, r, 0, []string{
		"Endpoint probe for mock-1", "streaming", "usage reported", "prefix cache works", "cache granularity",
	}, []string{"probing mock-1 at " + ts.URL + "/v1", "✓ basic", "✓ cache-warm"})
	for _, re := range []string{`streaming +yes`, `usage reported +yes`, `prefix cache works +yes \(\d+ of \d+ repeat requests hit`} {
		if !regexp.MustCompile(re).MatchString(r.stdout) {
			t.Errorf("the report lacks /%s/:\n%s", re, r.stdout)
		}
	}

	r = w.run("", append(base, "--json")...)
	assertRun(t, r, 0, nil, nil)
	var rep struct {
		Model string
		Steps []struct {
			Name string
			OK   bool
		}
		Findings struct {
			Streaming     bool
			UsageReported bool `json:"usage_reported"`
			CacheWorks    bool `json:"cache_works"`
		}
	}
	decode(t, "doctor --json", r.stdout, &rep)
	if rep.Model != "mock-1" || len(rep.Steps) < 4 || !rep.Findings.Streaming || !rep.Findings.UsageReported || !rep.Findings.CacheWorks {
		t.Errorf("doctor --json: %s", r.stdout)
	}
	for _, s := range rep.Steps {
		if !s.OK {
			t.Errorf("step %q of the probe failed:\n%s", s.Name, r.stdout)
		}
	}
}

// docs/CLI.md: doctor "exits 1 if the probe fails". An endpoint that refuses every connection
// is a probe that failed.
func TestDoctorOfAnEndpointThatIsDown(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	down := ts.URL
	ts.Close() // nothing listens there now
	w := newWorld(t, "")
	r := w.run("", "doctor", "--model", "m", "--provider", "custom", "--base-url", down+"/v1")
	assertRun(t, r, 1, []string{"Endpoint probe for m"}, []string{"✗ basic", "connection refused", "sleipnir: doctor: the endpoint did not answer"})
	// --json reports the same, and the status still says it failed
	r = w.run("", "doctor", "--model", "m", "--provider", "custom", "--base-url", down+"/v1", "--json")
	assertRun(t, r, 1, []string{`"steps"`}, []string{"sleipnir: doctor: "})
}

// The simulator prints its assumptions with its numbers, in a table or as JSON. Its numbers
// are tested in internal/kv/sim; this is the command around it (a small swarm keeps it quick
// under the race detector).
func TestSim(t *testing.T) {
	w := newWorld(t, "")
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"compare", []string{"--mode", "compare"}, []string{"workload: 4 workers", "naive whole-history", "sleipnir layered", "provider: explicit-cache"}},
		{"compare, marketplace cache", []string{"--mode", "compare", "--provider", "marketplace"}, []string{"workload: 4 workers", "sleipnir layered", "provider: "}},
		{"scenarios", []string{"--mode", "scenarios"}, []string{"scenario", "typical", "short tasks", "long tasks", "cold launches", "small repo", "bloated pins", "huge exploration"}},
		{"pins", []string{"--mode", "pins"}, []string{"pins(k)", "shared/role"}},
		{"agents", []string{"--mode", "agents"}, []string{"workers", "naive+sum"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := w.run("", append([]string{"sim", "--agents", "4"}, tc.args...)...)
			assertRun(t, r, 0, tc.want, nil)
			if r.stderr != "" {
				t.Errorf("stderr: %q", r.stderr)
			}
		})
	}
	t.Run("scenarios as JSON, and the same every time", func(t *testing.T) {
		r := w.run("", "sim", "--agents", "4", "--mode", "scenarios", "--json")
		assertRun(t, r, 0, nil, nil)
		var rows []struct {
			Scenario string
			Agents   int
		}
		decode(t, "sim --json", r.stdout, &rows)
		if len(rows) != 7 || rows[0].Scenario != "typical" || rows[0].Agents != 4 {
			t.Errorf("rows: %+v", rows)
		}
		if again := w.run("", "sim", "--agents", "4", "--mode", "scenarios", "--json"); again.stdout != r.stdout {
			t.Errorf("the same arguments gave different output:\n%s\n--- and ---\n%s", r.stdout, again.stdout)
		}
	})
}
