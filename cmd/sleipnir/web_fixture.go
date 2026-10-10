package main

// The hidden --fixture backend of `sleipnir web`: tabs whose model is internal/provider/mock with a script, in projects made in a
// scratch directory, so that the whole harness runs end to end with a model that does the same thing every time. It reproduces the
// sessions of the reference page (internal/web/uidev/mock) closely enough for the parity screenshots and the end-to-end tests:
//
//	shop    a manager and eight workers under a standing goal with a plan: three scouts survey, two backends, a frontend, a tester
//	        and a reviewer build in worktrees of their own and merge through the verifying queue; the frontend asks to run
//	        `npm install --save-dev vitest` (a question); the cart's backend meets a provider cache outage (a cache break); workers
//	        mail each other.
//	orders  a single agent in accept-edits mode: one turn that edits two files and runs the tests.
//	docs    two documentation workers whose questions nobody answers in time (an ask timeout): a refused command.
//	all     the three as tabs.
//
// Nothing reaches the network: the provider is a loopback server of this process, and the `npm` the frontend runs is a script of
// the fixture. The flag requires a state directory of its own (SLEIPNIR_HOME), never the person's; the projects are made in the
// temporary directory and removed when the server stops.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// fixtureNames are the fixtures --fixture takes.
var fixtureNames = []string{"shop", "orders", "docs", "all"}

// fixtureAllowed refuses a fixture outside a scratch state directory, an unknown name, or an address that is not loopback.
func fixtureAllowed(name, addr string) error {
	known := false
	for _, n := range fixtureNames {
		known = known || n == name
	}
	if !known {
		return fmt.Errorf("--fixture is one of %s, got %q", strings.Join(fixtureNames, ", "), name)
	}
	home := os.Getenv("SLEIPNIR_HOME")
	if home == "" {
		return errors.New("--fixture needs SLEIPNIR_HOME set to a scratch state directory")
	}
	if uh, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == filepath.Join(uh, ".sleipnir") {
		return errors.New("--fixture refuses the real state directory: set SLEIPNIR_HOME to a scratch directory")
	}
	if host, _, err := splitHostPort(addr); err != nil || !(host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return errors.New("--fixture serves on a loopback address only (--addr 127.0.0.1:0)")
	}
	return nil
}

// splitHostPort is net.SplitHostPort without brackets in the host.
func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", errors.New("no port")
	}
	return strings.Trim(addr[:i], "[]"), addr[i+1:], nil
}

// fixtureScenario is one tab of a fixture.
type fixtureScenario struct {
	name    string
	project string
	base    session.Options
	args    []string
	goal    string // a standing goal set once the session has started
	message string // a first message sent once the session has started
	askFor  time.Duration
}

// webFixture is the running fixture: its mock providers and its scenarios.
type webFixture struct {
	dir       string
	servers   []*httptest.Server
	scenarios []fixtureScenario
}

// fixtureModel is the model of the fixtures: round prices, a cache that behaves like OpenAI's.
func fixtureModel() cost.Model {
	return cost.Model{ID: "mock-1", ContextTokens: 1_000_000, MaxOutput: 8192, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 0.50, OutputPerM: 2.00, CacheReadPerM: 0.125, CacheWrite5mPerM: 0.50, CacheWrite1hPerM: 0.50}}
}

// newWebFixture makes the projects and the mock providers of a fixture.
func newWebFixture(ctx context.Context, name string) (*webFixture, error) {
	// The projects are made in the temporary directory: the state directory is a protected path (writes there always ask).
	dir, err := os.MkdirTemp("", "sleipnir-fixture-")
	if err != nil {
		return nil, err
	}
	fx := &webFixture{dir: dir}
	names := []string{name}
	if name == "all" {
		names = []string{"shop", "orders", "docs"}
	}
	for _, n := range names {
		var sc fixtureScenario
		var err error
		switch n {
		case "shop":
			sc, err = fx.shop()
		case "orders":
			sc, err = fx.orders()
		case "docs":
			sc, err = fx.docs()
		}
		if err != nil {
			fx.close()
			return nil, fmt.Errorf("--fixture %s: %w", n, err)
		}
		fx.scenarios = append(fx.scenarios, sc)
	}
	return fx, nil
}

// serve starts a mock provider with a responder and returns the session options that use it.
func (fx *webFixture) serve(project string, r mock.Responder, srvOut **mock.Server) session.Options {
	m := fixtureModel()
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}, Price: m.Price,
		FirstToken: 120 * time.Millisecond, DecodePer: 2 * time.Millisecond}, r)
	if srvOut != nil {
		*srvOut = srv
	}
	ts := srv.Start()
	fx.servers = append(fx.servers, ts)
	prof := openaichat.DefaultProfile("mock", ts.URL)
	client := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Profile: &prof, Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
	cfg := config.Defaults()
	bin := filepath.Join(fx.dir, "bin")
	env := append([]string{}, os.Environ()...)
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + bin + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
		}
	}
	return session.Options{Provider: client, ModelInfo: &m, Model: m.ID, Config: cfg, Root: project, Cwd: project,
		NoWeb: true, Offline: true, NoMCP: true, TrustProject: true, ShellEnv: env}
}

// start opens the fixture's tabs and gives each its first goal or message.
func (fx *webFixture) start(h *webHostImpl) {
	for _, sc := range fx.scenarios {
		t, err := h.addTab(sc.name, sc.project, sc.base)
		if err != nil {
			h.logf("fixture %s: %v", sc.name, err)
			continue
		}
		sc := sc
		h.track(func() {
			if err := t.startGen(startSpec{args: sc.args, name: sc.name, goalText: sc.goal}); err != nil {
				h.logf("fixture %s did not start: %s", sc.name, clip(err.Error(), 400))
				return
			}
			if sc.message != "" {
				_, _ = t.Send(h.ctx, wire.MessageRequest{Text: sc.message})
			}
		})
	}
}

// close stops the fixture's providers and removes its projects.
func (fx *webFixture) close() {
	for _, ts := range fx.servers {
		ts.CloseClientConnections()
		ts.Close()
	}
	fx.servers = nil
	if fx.dir != "" {
		_ = os.RemoveAll(fx.dir)
	}
}

// writeProject writes files under root (and makes it a git repository with one commit when git is set).
func writeProject(root string, files map[string]string, git bool) error {
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") || strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			return err
		}
	}
	if !git {
		return nil
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-qm", "the project as it was"}} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=Sleipnir fixture", "-c", "user.email=fixture@sleipnir.invalid", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// fixtureCall is a mock tool call with JSON arguments.
func fixtureCall(id, name string, args any) mock.ToolCall {
	b, _ := json.Marshal(args)
	return mock.ToolCall{ID: id, Name: name, Args: string(b)}
}

// fixtureWho reads which agent asks, as its prompt names it ("you: be-1 (backend)").
var fixtureWho = regexp.MustCompile(`you: (\S+) \((\w+)\)`)

// whoIs is the agent and role of a call ("" for the single agent).
func whoIs(c *mock.Call) (id, role string) {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if m := fixtureWho.FindStringSubmatch(c.Messages[i].Content); m != nil {
			return m[1], m[2]
		}
	}
	return "", ""
}

// isJudge reports whether a call is the standing goal's judge.
func isJudge(c *mock.Call) bool {
	return strings.Contains(c.System, "You judge whether a coding agent")
}

// isCompactor reports whether a call asks for a compaction patch.
func isCompactor(c *mock.Call) bool { return strings.Contains(c.LastUser(), "<compactor-task>") }

// steps counts the requests of each agent of a script.
type steps struct {
	mu    sync.Mutex
	n     map[string]int
	marks map[string]bool
}

// next returns the agent's step and counts it.
func (s *steps) next(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n == nil {
		s.n, s.marks = map[string]int{}, map[string]bool{}
	}
	n := s.n[id]
	s.n[id]++
	return n
}

// again makes the agent's next request the same step.
func (s *steps) again(id string) {
	s.mu.Lock()
	s.n[id]--
	s.mu.Unlock()
}

// mark records a milestone; marked reads it.
func (s *steps) mark(name string) {
	s.mu.Lock()
	if s.marks == nil {
		s.n, s.marks = map[string]int{}, map[string]bool{}
	}
	s.marks[name] = true
	s.mu.Unlock()
}

// marked reports whether a milestone was recorded.
func (s *steps) marked(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.marks[name]
}

// begun reports whether an agent has made a request.
func (s *steps) begun(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n[id] > 0
}

// waitReply is a short wait in tool time: the agent's next request is the same step again.
func (s *steps) waitReply(id, why string) mock.Reply {
	s.again(id)
	return mock.Reply{Text: why, ToolCalls: []mock.ToolCall{fixtureCall(fmt.Sprintf("w%d", time.Now().UnixNano()%100000), "bash", map[string]any{"command": "sleep 1"})}}
}

// judgeDone is the judge's verdict once the work is done.
var judgeDone = mock.Reply{Text: `{"verdict":"done","reason":"every step of the plan has evidence: the pieces were merged and the checks passed","left":[]}`}

// ---- shop ----------------------------------------------------------------------------------------------------------------------

// shopFiles is the shop project of the fixture.
var shopFiles = map[string]string{
	"AGENTS.md":       "# Shop\n\nMoney is integer cents. One DefaultPort, in shop/catalogue. Every Go file starts with its package clause.\n",
	"docs/api.md":     "# API\n\nGET /items?limit&after lists items; POST /cart/lines adds a line; GET /cart/total is integer cents.\n",
	"docs/design.md":  "# Design\n\nCursor paging by the last id. One rounding, at the end of a total. Handlers keep no rules of their own.\n",
	"data/items.json": "[{\"id\":\"sku-001\",\"name\":\"mug\",\"price_cents\":1200},{\"id\":\"sku-002\",\"name\":\"tee\",\"price_cents\":2500}]\n",
	"verify.sh":       "#!/bin/sh\n# the shop's checks: every Go file starts with its package clause\nfor f in $(find . -name '*.go' -not -path './.git/*'); do head -1 \"$f\" | grep -q '^package ' || { echo \"$f: no package clause\"; exit 1; }; done\necho ok\n",
	"checks.sh":       "#!/bin/sh\necho 'cart: 3 checks passed'\n",
	"bin/npm":         "#!/bin/sh\necho 'added 1 package, and audited 2 packages in 1s'\n",
	".gitignore":      "bin/\n",
}

// shop makes the shop scenario.
func (fx *webFixture) shop() (fixtureScenario, error) {
	project := filepath.Join(fx.dir, "shop")
	files := map[string]string{}
	for k, v := range shopFiles {
		if !strings.HasPrefix(k, "bin/") {
			files[k] = v
		}
	}
	if err := writeProject(project, files, true); err != nil {
		return fixtureScenario{}, err
	}
	if err := writeProject(fx.dir, map[string]string{"bin/npm": shopFiles["bin/npm"]}, false); err != nil {
		return fixtureScenario{}, err
	}
	sc := &shopFixture{}
	base := fx.serve(project, sc.respond, &sc.srv)
	return fixtureScenario{
		name: "shop", project: project, base: base,
		args: []string{"--cwd", project, "--swarm", "8", "--mode", "accept-edits", "--isolation", "worktree", "--verify", "sh verify.sh",
			"--allow", "Bash(sh verify.sh)", "--allow", "Bash(sh checks.sh)", "--allow", "Bash(sleep:*)"},
		goal: "Build the shop: a catalogue with paging, a cart that totals in whole cents, the web handlers for both, and smoke tests",
	}, nil
}

// shopFixture is the model of the shop team.
type shopFixture struct {
	steps
	srv *mock.Server
}

// respond is the model of every agent of the shop team, and its judge.
func (f *shopFixture) respond(c *mock.Call) mock.Reply {
	switch {
	case isJudge(c):
		return judgeDone
	case isCompactor(c):
		return mock.Reply{Text: `{"keep_from":"t1","spine":[],"mask":[],"notes":[],"promote":[]}`}
	}
	id, role := whoIs(c)
	if id == "" {
		return mock.Reply{Text: "nothing to do"}
	}
	n := f.next(id)
	switch role {
	case "manager":
		return f.manager(n)
	case "scout":
		return f.scout(id, n)
	case "backend":
		if id == "be-1" {
			return f.catalogue(id, n)
		}
		return f.cart(id, n)
	case "frontend":
		return f.web(id, n)
	case "tester":
		return f.tester(id, n)
	case "reviewer":
		return f.reviewer(id, n)
	}
	return mock.Reply{Text: "nothing to do"}
}

// shopPlan is the plan of the shop goal, with the state of each step at the manager's step n.
func shopPlan(n int) mock.ToolCall {
	steps := []string{"Survey the API, the data and the design rules", "Catalogue endpoint with cursor paging", "Cart with a total in whole cents",
		"Web handlers for the catalogue and the cart", "Smoke tests", "Review the merged result against AGENTS.md"}
	items := make([]map[string]string, len(steps))
	for i, s := range steps {
		st := "pending"
		switch {
		case n == 0 && i == 0, n == 1 && i >= 1 && i <= 4, n == 2 && i == 5:
			st = "doing"
		case n == 1 && i == 0, n == 2 && i < 5, n >= 3:
			st = "done"
		}
		items[i] = map[string]string{"step": s, "status": st}
	}
	return fixtureCall(fmt.Sprintf("p%d", n), "plan", map[string]any{"items": items})
}

// manager plans, hands the surveys out, then the building, and accepts what is merged.
func (f *shopFixture) manager(n int) mock.Reply {
	switch n {
	case 0:
		calls := []mock.ToolCall{shopPlan(0)}
		titles := []string{"Survey the API (docs/api.md)", "Survey the data (data/items.json)", "Survey the design rules (docs/design.md)"}
		for i, t := range titles {
			calls = append(calls, fixtureCall(fmt.Sprintf("t%d", i+1), "task", map[string]any{"action": "create", "title": t, "role": "scout"}))
		}
		for i := range titles {
			calls = append(calls, fixtureCall(fmt.Sprintf("s%d", i+1), "spawn", map[string]any{"role": "scout", "task": fmt.Sprintf("T%d", i+1)}))
		}
		calls = append(calls, fixtureCall("w1", "wait", map[string]any{"until": []string{"T1", "T2", "T3"}, "timeout_sec": 120}))
		return mock.Reply{Text: "I'll have three scouts map the API, the data and the design rules first; the building waits for what they find.", ToolCalls: calls}
	case 1:
		calls := []mock.ToolCall{shopPlan(1)}
		for i := 1; i <= 3; i++ {
			calls = append(calls, fixtureCall(fmt.Sprintf("a%d", i), "task", map[string]any{"action": "accept", "id": fmt.Sprintf("T%d", i)}))
		}
		ws := []struct {
			title, role string
			files       []string
		}{
			{"Catalogue: GET /items with cursor paging, and the one DefaultPort", "backend", []string{"shop/catalogue/**"}},
			{"Cart: lines, and a total in whole cents", "backend", []string{"shop/cart/**"}},
			{"Web: the handlers for the catalogue and the cart", "frontend", []string{"shop/web/**", "package.json"}},
			{"Smoke tests: fill a cart and read the total", "tester", []string{"tests/**"}},
			{"Review what was merged against AGENTS.md", "reviewer", nil},
		}
		for i, w := range ws {
			args := map[string]any{"action": "create", "title": w.title, "role": w.role}
			if w.files != nil {
				args["files"] = w.files
			}
			calls = append(calls, fixtureCall(fmt.Sprintf("c%d", i+1), "task", args))
		}
		for i, w := range ws {
			calls = append(calls, fixtureCall(fmt.Sprintf("sp%d", i+1), "spawn", map[string]any{"role": w.role, "task": fmt.Sprintf("T%d", 4+i)}))
		}
		calls = append(calls, fixtureCall("w2", "wait", map[string]any{"until": []string{"T4", "T5", "T6", "T7"}, "timeout_sec": 600}))
		return mock.Reply{Text: "The surveys agree. Two backends, a frontend and a tester build in worktrees of their own; a reviewer joins them and waits for the merged result.", ToolCalls: calls}
	case 2:
		f.mark("pieces merged")
		calls := []mock.ToolCall{shopPlan(2)}
		for i := 4; i <= 7; i++ {
			calls = append(calls, fixtureCall(fmt.Sprintf("b%d", i), "task", map[string]any{"action": "accept", "id": fmt.Sprintf("T%d", i)}))
		}
		calls = append(calls, fixtureCall("w3", "wait", map[string]any{"until": []string{"T8"}, "timeout_sec": 300}))
		return mock.Reply{Text: "The four pieces are merged and verified. The reviewer reads the result.", ToolCalls: calls}
	case 3:
		return mock.Reply{Text: "Reviewed.", ToolCalls: []mock.ToolCall{shopPlan(3), fixtureCall("ar", "task", map[string]any{"action": "accept", "id": "T8"})}}
	}
	return mock.Reply{Text: "Done: the catalogue pages by a stable cursor, the cart totals in whole cents, the web handlers serve both, and the smoke tests pass on the merged result."}
}

// scout reads its documents and reports.
func (f *shopFixture) scout(id string, n int) mock.Reply {
	reads := map[string]string{"sc-1": "docs/api.md", "sc-2": "data/items.json", "sc-3": "docs/design.md"}
	tasks := map[string]string{"sc-1": "T1", "sc-2": "T2", "sc-3": "T3"}
	switch n {
	case 0:
		return mock.Reply{Text: "Reading what the survey is about.", ToolCalls: []mock.ToolCall{fixtureCall("r"+id, "read", map[string]any{"path": reads[id]})}}
	case 1:
		return mock.Reply{Text: "Reporting.", ToolCalls: []mock.ToolCall{fixtureCall("d"+id, "task", map[string]any{"action": "done", "id": tasks[id], "text": "read " + reads[id] + ": the rules are in it"})}}
	}
	return mock.Reply{Text: "Done."}
}

// catalogue (be-1) tells the web what it serves and writes the catalogue.
func (f *shopFixture) catalogue(id string, n int) mock.Reply {
	switch n {
	case 0:
		return mock.Reply{Text: "Reading the API and the items first.", ToolCalls: []mock.ToolCall{fixtureCall("r1", "read", map[string]any{"path": "docs/api.md"}), fixtureCall("r2", "read", map[string]any{"path": "data/items.json"})}}
	case 1:
		if !f.begun("fe-1") {
			return f.waitReply(id, "The web worker is not here yet.")
		}
		return mock.Reply{Text: "Telling the web what I serve, then the catalogue.", ToolCalls: []mock.ToolCall{
			fixtureCall("m1", "mail", map[string]any{"to": "fe-1", "kind": "contract", "text": "catalogue: GET /items answers {items:[{id,name,price_cents}], next}; the port is catalogue.DefaultPort"}),
			fixtureCall("w1", "write", map[string]any{"path": "shop/catalogue/items.go", "content": "package catalogue\n\n// DefaultPort is the shop's one port.\nconst DefaultPort = 8080\n"})}}
	case 2:
		return mock.Reply{Text: "The checks.", ToolCalls: []mock.ToolCall{fixtureCall("v1", "bash", map[string]any{"command": "sh verify.sh"})}}
	case 3:
		return mock.Reply{Text: "Done.", ToolCalls: []mock.ToolCall{fixtureCall("d1", "task", map[string]any{"action": "done", "id": "T4", "text": "the catalogue with cursor paging and the one DefaultPort"})}}
	}
	return mock.Reply{Text: "The catalogue is merged."}
}

// cart (be-2) meets a cache outage of the provider: its next requests find nothing of the prefix they were promised.
func (f *shopFixture) cart(id string, n int) mock.Reply {
	switch n {
	case 0:
		return mock.Reply{Text: "The rules for totals first.", ToolCalls: []mock.ToolCall{fixtureCall("r1", "read", map[string]any{"path": "docs/design.md"})}}
	case 1:
		return mock.Reply{Text: "The cart.", ToolCalls: []mock.ToolCall{fixtureCall("w1", "write", map[string]any{"path": "shop/cart/cart.go", "content": "package cart\n\n// Total is the sum of the lines in whole cents.\nfunc Total(lines []int) int {\n\tt := 0\n\tfor _, l := range lines {\n\t\tt += l\n\t}\n\treturn t\n}\n"})}}
	case 2:
		if f.srv != nil {
			f.srv.CacheOutage(3 * time.Second)
		}
		return mock.Reply{Text: "The cart's checks.", ToolCalls: []mock.ToolCall{fixtureCall("c1", "bash", map[string]any{"command": "sh checks.sh"})}}
	case 3:
		return mock.Reply{Text: "And the shop's.", ToolCalls: []mock.ToolCall{fixtureCall("c2", "bash", map[string]any{"command": "sh verify.sh"})}}
	case 4:
		return mock.Reply{Text: "Done.", ToolCalls: []mock.ToolCall{fixtureCall("d1", "task", map[string]any{"action": "done", "id": "T5", "text": "the cart: lines and a total in whole cents"})}}
	}
	return mock.Reply{Text: "The cart is merged."}
}

// web (fe-1) asks to install a test runner: the question of the fixture.
func (f *shopFixture) web(id string, n int) mock.Reply {
	switch n {
	case 0:
		return mock.Reply{Text: "The handlers follow the API document.", ToolCalls: []mock.ToolCall{fixtureCall("r1", "read", map[string]any{"path": "docs/api.md"})}}
	case 1:
		return mock.Reply{Text: "The handlers need a test runner.", ToolCalls: []mock.ToolCall{fixtureCall("n1", "bash", map[string]any{"command": "npm install --save-dev vitest"})}}
	case 2:
		return mock.Reply{Text: "The handlers.", ToolCalls: []mock.ToolCall{fixtureCall("w1", "write", map[string]any{"path": "shop/web/handlers.go", "content": "package web\n\n// Routes lists the handlers of the shop.\nvar Routes = []string{\"/items\", \"/cart/lines\", \"/cart/total\"}\n"})}}
	case 3:
		return mock.Reply{Text: "The checks.", ToolCalls: []mock.ToolCall{fixtureCall("v1", "bash", map[string]any{"command": "sh verify.sh"})}}
	case 4:
		return mock.Reply{Text: "Done.", ToolCalls: []mock.ToolCall{fixtureCall("d1", "task", map[string]any{"action": "done", "id": "T6", "text": "the web handlers"})}}
	}
	return mock.Reply{Text: "The web handlers are merged."}
}

// tester (ts-1) asks the cart's author a question by mail and writes the smoke script.
func (f *shopFixture) tester(id string, n int) mock.Reply {
	switch n {
	case 0:
		return mock.Reply{Text: "The API first.", ToolCalls: []mock.ToolCall{fixtureCall("r1", "read", map[string]any{"path": "docs/api.md"})}}
	case 1:
		if !f.begun("be-2") {
			return f.waitReply(id, "The cart's author is not here yet.")
		}
		return mock.Reply{Text: "One question for the cart, and the script meanwhile.", ToolCalls: []mock.ToolCall{
			fixtureCall("m1", "mail", map[string]any{"to": "be-2", "kind": "request", "text": "cart: for 3 at 333 cents, is the total 999? I assume one rounding at the end."}),
			fixtureCall("w1", "write", map[string]any{"path": "tests/smoke.sh", "content": "#!/bin/sh\necho 'smoke: cart total 999'\n"})}}
	case 2:
		return mock.Reply{Text: "The checks.", ToolCalls: []mock.ToolCall{fixtureCall("v1", "bash", map[string]any{"command": "sh verify.sh"})}}
	case 3:
		return mock.Reply{Text: "Done.", ToolCalls: []mock.ToolCall{fixtureCall("d1", "task", map[string]any{"action": "done", "id": "T7", "text": "a smoke script"})}}
	}
	return mock.Reply{Text: "The smoke script is merged."}
}

// reviewer (rv-1) waits for the merged result and checks it.
func (f *shopFixture) reviewer(id string, n int) mock.Reply {
	switch n {
	case 0:
		return mock.Reply{Text: "The conventions I will hold them to.", ToolCalls: []mock.ToolCall{fixtureCall("r0", "read", map[string]any{"path": "AGENTS.md"})}}
	case 1:
		if !f.marked("pieces merged") {
			return f.waitReply(id, "Nothing is merged yet; I wait for the writers.")
		}
		return mock.Reply{Text: "Everything is merged; the shop's checks.", ToolCalls: []mock.ToolCall{fixtureCall("v1", "bash", map[string]any{"command": "sh verify.sh"})}}
	case 2:
		return mock.Reply{Text: "Reporting.", ToolCalls: []mock.ToolCall{fixtureCall("d1", "task", map[string]any{"action": "done", "id": "T8", "text": "the conventions hold"})}}
	}
	return mock.Reply{Text: "Reviewed: the conventions hold."}
}

// ---- orders --------------------------------------------------------------------------------------------------------------------

// orders makes the single agent scenario.
func (fx *webFixture) orders() (fixtureScenario, error) {
	project := filepath.Join(fx.dir, "orders-api")
	files := map[string]string{
		"orders/orders.go":      "package orders\n\n// List returns every order.\nfunc List() []int { return []int{1, 2, 3} }\n",
		"orders/orders_test.go": "package orders\n",
		"test.sh":               "#!/bin/sh\necho 'ok  orders  0.01s'\n",
	}
	if err := writeProject(project, files, true); err != nil {
		return fixtureScenario{}, err
	}
	var st steps
	base := fx.serve(project, func(c *mock.Call) mock.Reply {
		if isJudge(c) {
			return judgeDone
		}
		if strings.Contains(c.LastUser(), "<compactor-task>") {
			return mock.Reply{Text: `{"keep_from":"t1","spine":[],"mask":[],"notes":[],"promote":[]}`}
		}
		if c.Messages[len(c.Messages)-1].Role != "tool" && !strings.Contains(c.LastUser(), "pagination") {
			return mock.Reply{Text: "Tell me what to change in the orders API."}
		}
		switch st.next("main") {
		case 0:
			return mock.Reply{Text: "Looking at the orders first.", ToolCalls: []mock.ToolCall{fixtureCall("r1", "read", map[string]any{"path": "orders/orders.go"})}}
		case 1:
			return mock.Reply{Text: "Paging: a page and a size, and a test for the last page.", ToolCalls: []mock.ToolCall{
				fixtureCall("w1", "write", map[string]any{"path": "orders/orders.go", "content": "package orders\n\n// List returns one page of the orders.\nfunc List(page, size int) []int {\n\tall := []int{1, 2, 3}\n\tfrom := page * size\n\tif from >= len(all) {\n\t\treturn nil\n\t}\n\treturn all[from:min(from+size, len(all))]\n}\n"}),
				fixtureCall("w2", "write", map[string]any{"path": "orders/orders_test.go", "content": "package orders\n\nimport \"testing\"\n\n// TestLastPage checks the last page.\nfunc TestLastPage(t *testing.T) {\n\tif got := List(1, 2); len(got) != 1 {\n\t\tt.Fatal(got)\n\t}\n}\n"})}}
		case 2:
			return mock.Reply{Text: "The tests.", ToolCalls: []mock.ToolCall{fixtureCall("b1", "bash", map[string]any{"command": "sh test.sh"})}}
		}
		return mock.Reply{Text: "Done: /orders pages by page and size, and the tests pass."}
	}, nil)
	return fixtureScenario{name: "orders-api", project: project, base: base,
		args:    []string{"--cwd", project, "--swarm", "0", "--mode", "accept-edits", "--allow", "Bash(sh test.sh)"},
		message: "add pagination to /orders"}, nil
}

// ---- docs ----------------------------------------------------------------------------------------------------------------------

// docs makes the scenario of the documentation workers whose question nobody answers in time.
func (fx *webFixture) docs() (fixtureScenario, error) {
	project := filepath.Join(fx.dir, "docs-sweep")
	files := map[string]string{"docs/index.md": "# Docs\n", "docs/usage.md": "# Usage\n"}
	if err := writeProject(project, files, true); err != nil {
		return fixtureScenario{}, err
	}
	var st steps
	base := fx.serve(project, func(c *mock.Call) mock.Reply {
		if isJudge(c) {
			return judgeDone
		}
		id, role := whoIs(c)
		n := st.next(id)
		switch role {
		case "manager":
			switch n {
			case 0:
				calls := []mock.ToolCall{}
				for i, p := range []string{"docs/index.md", "docs/usage.md"} {
					calls = append(calls, fixtureCall(fmt.Sprintf("t%d", i+1), "task", map[string]any{"action": "create", "title": "Sweep " + p, "role": "docs", "files": []string{p}}))
				}
				calls = append(calls, fixtureCall("s1", "spawn", map[string]any{"role": "docs", "task": "T1"}), fixtureCall("s2", "spawn", map[string]any{"role": "docs", "task": "T2"}),
					fixtureCall("w1", "wait", map[string]any{"until": []string{"T1", "T2"}, "timeout_sec": 300}))
				return mock.Reply{Text: "Two docs workers sweep a page each.", ToolCalls: calls}
			case 1:
				return mock.Reply{Text: "Accepting.", ToolCalls: []mock.ToolCall{fixtureCall("a1", "task", map[string]any{"action": "accept", "id": "T1"}), fixtureCall("a2", "task", map[string]any{"action": "accept", "id": "T2"})}}
			}
			return mock.Reply{Text: "The docs are swept; one lint run was refused because nobody could approve it."}
		case "docs":
			task := map[string]string{"dc-1": "T1", "dc-2": "T2"}[id]
			switch n {
			case 0:
				return mock.Reply{Text: "Linting the docs first.", ToolCalls: []mock.ToolCall{fixtureCall("l1", "bash", map[string]any{"command": "npm run lint:md docs/"})}}
			case 1:
				return mock.Reply{Text: "The lint was refused; I fix the page by hand.", ToolCalls: []mock.ToolCall{fixtureCall("w1", "write", map[string]any{"path": map[string]string{"dc-1": "docs/index.md", "dc-2": "docs/usage.md"}[id], "content": "# Docs\n\nSwept.\n"})}}
			case 2:
				return mock.Reply{Text: "Done.", ToolCalls: []mock.ToolCall{fixtureCall("d1", "task", map[string]any{"action": "done", "id": task, "text": "swept"})}}
			}
			return mock.Reply{Text: "Swept."}
		}
		return mock.Reply{Text: "nothing to do"}
	}, nil)
	base.AskTimeout = 20 * time.Second
	return fixtureScenario{name: "docs-sweep", project: project, base: base, askFor: 20 * time.Second,
		args: []string{"--cwd", project, "--swarm", "2", "--mode", "accept-edits"},
		goal: "Sweep the docs: fix the headings of every page"}, nil
}
