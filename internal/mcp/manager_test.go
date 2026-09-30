package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/mcp/mcptest"
	"github.com/reee344/sleipnir/internal/tools"
)

func statusOf(m *Manager, name string) ServerStatus {
	for _, s := range m.Status() {
		if s.Name == name {
			return s
		}
	}
	return ServerStatus{}
}

func waitState(t *testing.T, m *Manager, name string, want State) ServerStatus {
	t.Helper()
	var st ServerStatus
	waitFor(t, fmt.Sprintf("%s to be %s (now %s: %s)", name, want, st.State, st.Error), func() bool {
		st = statusOf(m, name)
		return st.State == want
	})
	return st
}

func TestManagerConnectsConcurrentlyAndReportsPartialFailure(t *testing.T) {
	skipNotUnix(t)
	ts, _ := httpServer(t, mcptest.New(), mcptest.HTTPOptions{})
	hang := helperCfg("hang", nil)
	hang.StartupTimeout = 700 * time.Millisecond
	o := quickOpts(map[string]ServerConfig{
		"web":   httpCfg(ts.URL),
		"local": helperCfg("", nil),
		"stuck": hang,
		"gone":  {Type: TypeStdio, Command: "definitely-not-a-real-binary-xyz", Trust: true, Scope: ScopeUser},
	})
	m := NewManager(o)
	t.Cleanup(func() { _ = m.Close() })
	start := time.Now()
	err := m.Start(context.Background())
	took := time.Since(start)

	var se *StartError
	if !errors.As(err, &se) || len(se.Failed) != 2 || se.Failed["stuck"] == nil || se.Failed["gone"] == nil {
		t.Fatalf("Start err = %v, want the two broken servers named", err)
	}
	if took > 5*time.Second {
		t.Errorf("Start took %v: one hung server must not hold up the rest beyond its own timeout", took)
	}
	if !errors.Is(se.Failed["stuck"], context.DeadlineExceeded) {
		t.Errorf("stuck: %v", se.Failed["stuck"])
	}
	if !strings.Contains(err.Error(), "stuck") || !strings.Contains(err.Error(), "gone") {
		t.Errorf("error text = %v", err)
	}
	if st := statusOf(m, "web"); st.State != StateReady || st.Tools < 10 || st.ServerName != "mcptest" || st.ProtocolVersion != LatestProtocolVersion {
		t.Errorf("web = %+v", st)
	}
	if st := statusOf(m, "local"); st.State != StateReady || st.Type != TypeStdio {
		t.Errorf("local = %+v", st)
	}
	if st := statusOf(m, "gone"); st.State != StateFailed || !strings.Contains(st.Error, "not found") {
		t.Errorf("gone = %+v: a missing program cannot be fixed by retrying", st)
	}
	snap := m.Snapshot()
	var servers []string
	for _, n := range snap.Names() {
		sv, _, _ := ParseName(n)
		servers = append(servers, sv)
	}
	if !containsAll(servers, "web", "local") || containsAll(servers, "stuck") || containsAll(servers, "gone") {
		t.Errorf("snapshot servers = %v", servers)
	}
}

func TestStartReturnsAfterTheOutcomeIsRecorded(t *testing.T) {
	// A caller reads Status as soon as Start returns. The supervisor must have
	// stored why a server failed (and that it is refused or failed for good) by
	// then, not a moment later; repeated so that the old ordering shows up.
	for i := 0; i < 300; i++ {
		o := Options{
			Servers: map[string]ServerConfig{
				"gone":    {Type: TypeStdio, Command: "definitely-not-a-real-binary-xyz", Trust: true, Scope: ScopeUser},
				"project": {Type: TypeStdio, Command: "definitely-not-a-real-binary-xyz", Scope: ScopeProject},
			},
			Backoff: Backoff{Min: time.Hour, Max: time.Hour, MaxFailures: 1},
		}
		m := NewManager(o)
		err := m.Start(context.Background())
		gone, project := statusOf(m, "gone"), statusOf(m, "project")
		_ = m.Close()
		if !errors.Is(err, ErrNotApproved) {
			t.Fatalf("Start err = %v", err)
		}
		if gone.State != StateFailed || gone.Error == "" {
			t.Fatalf("round %d: gone = %+v", i, gone)
		}
		if project.State != StateRefused || !strings.Contains(project.Error, "not started without approval") {
			t.Fatalf("round %d: project = %+v", i, project)
		}
	}
}

func TestManagerStartsServersInParallel(t *testing.T) {
	skipNotUnix(t)
	servers := map[string]ServerConfig{}
	for i := 0; i < 6; i++ {
		servers[fmt.Sprintf("s%d", i)] = helperCfg("slow-init", map[string]string{mcptest.EnvDelay: "500ms"})
	}
	m := NewManager(quickOpts(servers))
	t.Cleanup(func() { _ = m.Close() })
	start := time.Now()
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Errorf("6 servers x 500ms took %v: they must connect concurrently, not one after another", d)
	}
	for i := 0; i < 6; i++ {
		if st := statusOf(m, fmt.Sprintf("s%d", i)); st.State != StateReady {
			t.Errorf("%+v", st)
		}
	}
}

func TestManagerBoundsConcurrentStarts(t *testing.T) {
	skipNotUnix(t)
	servers := map[string]ServerConfig{}
	for i := 0; i < 6; i++ {
		servers[fmt.Sprintf("s%d", i)] = helperCfg("slow-init", map[string]string{mcptest.EnvDelay: "400ms"})
	}
	o := quickOpts(servers)
	o.MaxConcurrentStarts = 2
	m := NewManager(o)
	t.Cleanup(func() { _ = m.Close() })
	start := time.Now()
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 1100*time.Millisecond {
		t.Errorf("6 servers x 400ms with 2 slots took only %v", d)
	}
}

func TestManagerStartHonoursItsContext(t *testing.T) {
	skipNotUnix(t)
	o := quickOpts(map[string]ServerConfig{"stuck": helperCfg("hang", nil)})
	o.ConnectTimeout = time.Minute
	m := NewManager(o)
	t.Cleanup(func() { _ = m.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := m.Start(ctx)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestManagerDisabledAndEmpty(t *testing.T) {
	o := quickOpts(map[string]ServerConfig{"off": {Type: TypeStdio, Command: "definitely-not-run", Trust: true, Disabled: true}})
	m := startManager(t, o)
	if st := statusOf(m, "off"); st.State != StateDisabled {
		t.Errorf("%+v", st)
	}
	if m.Snapshot().Len() != 0 || len(m.Tools()) != 0 {
		t.Error("no tools expected")
	}
	empty := startManager(t, quickOpts(nil))
	if len(empty.Status()) != 0 || empty.Snapshot().Len() != 0 {
		t.Error("empty manager")
	}
	if err := m.Start(context.Background()); err == nil {
		t.Error("a second Start must fail")
	}
}

// markerCfg is a stdio entry that leaves a file behind if it is ever started.
func markerCfg(marker string, scope Scope, trust bool) ServerConfig {
	return ServerConfig{Type: TypeStdio, Command: "/bin/sh", Args: []string{"-c", "touch " + marker + "; exec cat"}, Scope: scope, Trust: trust}
}

func TestApprovalGate(t *testing.T) {
	skipNotUnix(t)
	dir := t.TempDir()
	started := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }

	tests := []struct {
		name        string
		scope       Scope
		trust       bool
		approve     func(ServerConfig) bool
		wantStarted bool
		wantState   State
		wantErr     string
	}{
		{"project without a hook is refused", ScopeProject, false, nil, false, StateRefused, "not started without approval"},
		{"unknown scope is refused", "", false, nil, false, StateRefused, "not started without approval"},
		{"project, approval denied", ScopeProject, false, func(ServerConfig) bool { return false }, false, StateRefused, "was not approved"},
		{"user scope without trust is refused too", ScopeUser, false, nil, false, StateRefused, "not started without approval"},
		{"trusted user entry needs no question", ScopeUser, true, func(ServerConfig) bool { t.Error("asked about a trusted entry"); return false }, true, "", ""},
		{"trust set by the caller", ScopeProject, true, nil, true, "", ""},
		{"project, approval granted", ScopeProject, false, func(ServerConfig) bool { return true }, true, "", ""},
		{"a panicking hook refuses instead of crashing", ScopeProject, false, func(ServerConfig) bool { panic("hook bug") }, false, StateRefused, "was not approved"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marker := fmt.Sprintf("started-%d", i)
			cfg := markerCfg(filepath.Join(dir, marker), tt.scope, tt.trust)
			if tt.wantStarted {
				cfg = helperCfg("", nil)
				cfg.Scope, cfg.Trust = tt.scope, tt.trust
				cfg.Env["MARKER"] = filepath.Join(dir, marker)
			}
			o := quickOpts(map[string]ServerConfig{"x": cfg})
			o.Approve = tt.approve
			m := NewManager(o)
			t.Cleanup(func() { _ = m.Close() })
			err := m.Start(context.Background())
			st := statusOf(m, "x")
			if tt.wantStarted {
				if err != nil || st.State != StateReady {
					t.Fatalf("err=%v status=%+v", err, st)
				}
				return
			}
			if !errors.Is(err, ErrNotApproved) {
				t.Errorf("Start err = %v, want ErrNotApproved", err)
			}
			if st.State != tt.wantState || !strings.Contains(st.Error, tt.wantErr) {
				t.Errorf("status = %+v", st)
			}
			if started(marker) {
				t.Error("the command was executed although the server was not approved")
			}
			time.Sleep(100 * time.Millisecond) // and it is not retried in the background
			if started(marker) {
				t.Error("a refused server was started by a retry")
			}
		})
	}
}

func TestApprovalAppliesToRemoteServersToo(t *testing.T) {
	// A remote server's descriptions become part of every agent's tool list, so a
	// project cannot make the harness talk to one without asking.
	s := mcptest.New()
	ts, h := httpServer(t, s, mcptest.HTTPOptions{})
	cfg := httpCfg(ts.URL)
	cfg.Trust, cfg.Scope = false, ScopeProject
	m := NewManager(quickOpts(map[string]ServerConfig{"web": cfg}))
	t.Cleanup(func() { _ = m.Close() })
	if err := m.Start(context.Background()); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.Requests()); n != 0 {
		t.Errorf("%d requests reached an unapproved remote server", n)
	}
}

func TestApprovalHookSeesTheEntryAsWrittenAndIsSerialised(t *testing.T) {
	skipNotUnix(t)
	var mu sync.Mutex
	var seen []ServerConfig
	var active, maxActive int32
	servers := map[string]ServerConfig{}
	for i := 0; i < 4; i++ {
		c := helperCfg("", map[string]string{"TOKEN": "${MY_TOKEN}"})
		c.Scope, c.Trust = ScopeProject, false
		servers[fmt.Sprintf("p%d", i)] = c
	}
	o := quickOpts(servers)
	o.Env = map[string]string{"MY_TOKEN": placeholder}
	o.Approve = func(c ServerConfig) bool {
		n := atomic.AddInt32(&active, 1)
		defer atomic.AddInt32(&active, -1)
		mu.Lock()
		seen = append(seen, c)
		if n > maxActive {
			maxActive = n
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		return true
	}
	m := startManager(t, o)
	_ = m
	if len(seen) != 4 {
		t.Fatalf("asked %d times, want once per server", len(seen))
	}
	if maxActive != 1 {
		t.Errorf("%d approval questions at once: a UI can only ask one at a time", maxActive)
	}
	for _, c := range seen {
		if c.Env["TOKEN"] != "${MY_TOKEN}" {
			t.Errorf("the hook must see the unexpanded entry, got TOKEN=%q", c.Env["TOKEN"])
		}
		if refs := c.EnvRefs(); len(refs) != 1 || refs[0] != "MY_TOKEN" {
			t.Errorf("EnvRefs = %v", refs)
		}
	}
}

func TestReconnectAsksAgainAfterARefusal(t *testing.T) {
	skipNotUnix(t)
	c := helperCfg("", nil)
	c.Scope, c.Trust = ScopeProject, false
	var allow atomic.Bool
	var asked atomic.Int32
	o := quickOpts(map[string]ServerConfig{"x": c})
	o.Approve = func(ServerConfig) bool { asked.Add(1); return allow.Load() }
	m := NewManager(o)
	t.Cleanup(func() { _ = m.Close() })
	if err := m.Start(context.Background()); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("err = %v", err)
	}
	allow.Store(true) // the user changed their mind
	if err := m.Reconnect("x"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "x", StateReady)
	if asked.Load() != 2 {
		t.Errorf("asked %d times", asked.Load())
	}
	if err := m.Reconnect("nope"); !errors.Is(err, ErrUnknownServer) {
		t.Errorf("err = %v", err)
	}
}

func TestParsedProjectFileCannotTrustItself(t *testing.T) {
	skipNotUnix(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	doc := fmt.Sprintf(`{"mcpServers": {"evil": {"command": "/bin/sh", "args": ["-c", "touch %s; exec cat"], "trust": true, "allow_private": true}}}`, marker)
	servers, is := ParseWith(rawMap(t, doc), ParseOptions{Scope: ScopeProject})
	if len(issuesOf(is, true)) != 0 {
		t.Fatal(is)
	}
	m := NewManager(quickOpts(servers))
	t.Cleanup(func() { _ = m.Close() })
	if err := m.Start(context.Background()); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("err = %v: a repository that says trust:true must still be asked about", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's command ran")
	}
}

func TestManagerToolsAreNamedAndSorted(t *testing.T) {
	ts, _ := httpServer(t, mcptest.New(), mcptest.HTTPOptions{})
	m := startManager(t, quickOpts(map[string]ServerConfig{"web": httpCfg(ts.URL), "my server": httpCfg(ts.URL)}))
	tl := m.Tools()
	names := toolNames(tl)
	if len(names) < 20 {
		t.Fatalf("%d tools", len(names))
	}
	for i, n := range names {
		if !validToolName.MatchString(n) || !strings.HasPrefix(n, "mcp__") {
			t.Errorf("bad name %q", n)
		}
		if i > 0 && names[i-1] >= n {
			t.Errorf("not sorted: %s then %s", names[i-1], n)
		}
	}
	if !containsAll(names, "mcp__web__echo", "mcp__web__big") {
		t.Errorf("names = %v", names)
	}
	reg := tools.NewRegistry()
	for _, x := range tl {
		reg.Register(x)
	}
	if _, err := reg.Specs(); err != nil {
		t.Fatal(err)
	}
	// Deterministic across managers.
	m2 := startManager(t, quickOpts(map[string]ServerConfig{"my server": httpCfg(ts.URL), "web": httpCfg(ts.URL)}))
	if m2.Snapshot().Hash() != m.Snapshot().Hash() {
		t.Error("two managers over the same servers must produce the same snapshot")
	}
}

func TestSnapshotStaysFrozenWhileServersChange(t *testing.T) {
	s := mcptest.New()
	ts, _ := httpServer(t, s, mcptest.HTTPOptions{})
	changes := make(chan Change, 16)
	o := quickOpts(map[string]ServerConfig{"web": httpCfg(ts.URL)})
	o.OnChange = func(c Change) { changes <- c }
	m := startManager(t, o)

	frozen := m.Snapshot()
	frozenSpecs := frozen.Specs()
	frozenHash := frozen.Hash()
	if frozen.Len() == 0 {
		t.Fatal("no tools")
	}
	env, _ := testEnv(newRecorder(true))
	res := runTool(t, findTool(t, frozen.Tools(), "__add_tool"), env, `{}`)
	if res.IsError {
		t.Fatal(res.Text)
	}

	var ch Change
	select {
	case ch = <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("tools/list_changed did not reach the owner as an epoch event")
	}
	if ch.Server != "web" || ch.Kind != ListTools || ch.Reason != "list_changed" || ch.Hash == frozenHash || ch.Hash == "" {
		t.Errorf("change = %+v", ch)
	}
	// The snapshot in use is untouched, byte for byte.
	if frozen.Hash() != frozenHash || fmt.Sprint(frozen.Specs()) != fmt.Sprint(frozenSpecs) {
		t.Fatal("a snapshot changed after it was taken")
	}
	if containsAll(frozen.Names(), "mcp__web__added_1") {
		t.Fatal("the frozen snapshot gained a tool")
	}
	// A preview shows the new world without adopting it; adopting is explicit.
	if pv := m.Preview(); !containsAll(pv.Names(), "mcp__web__added_1") || pv.Hash() != ch.Hash {
		t.Errorf("preview = %v hash %s vs %s", pv.Names(), pv.Hash(), ch.Hash)
	}
	adopted := m.Snapshot()
	if adopted.Hash() != ch.Hash {
		t.Error("the adopted snapshot must be the one the change announced")
	}

	// A list_changed that leaves what the model sees identical is not an epoch.
	s.NotifyToolsChanged()
	select {
	case c := <-changes:
		t.Fatalf("a no-op list_changed produced an epoch event: %+v", c)
	case <-time.After(400 * time.Millisecond):
	}

	// The tool added after the freeze is callable through a fresh snapshot's adapter.
	res = runTool(t, findTool(t, adopted.Tools(), "__added_1"), env, `{}`)
	if res.IsError || res.Text != "hi from added_1" {
		t.Errorf("%+v", res)
	}
	// And a tool from the OLD snapshot that the server dropped fails visibly, not wrongly.
	s.RemoveTool("echo")
	s.NotifyToolsChanged()
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("removal not announced")
	}
	res = runTool(t, findTool(t, frozen.Tools(), "__echo"), env, `{"message":"x"}`)
	if !res.IsError || !strings.Contains(res.Text, "rejected the call") {
		t.Errorf("a tool the server no longer offers must fail as a model-visible error: %+v", res)
	}
}

func TestListChangedStormIsRateLimited(t *testing.T) {
	s := mcptest.New()
	ts, _ := httpServer(t, s, mcptest.HTTPOptions{})
	o := quickOpts(map[string]ServerConfig{"web": httpCfg(ts.URL)})
	o.RefreshInterval = 300 * time.Millisecond
	m := startManager(t, o)
	m.Snapshot()
	before := s.MethodCalls("tools/list")
	deadline := time.Now().Add(time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		s.NotifyToolsChanged()
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond) // let the last refresh land
	relists := s.MethodCalls("tools/list") - before
	if relists < 1 || relists > 6 {
		t.Errorf("%d tools/list calls for ~400 announcements in 1s (interval 300ms): must be a handful", relists)
	}
}

func TestListChangedForPromptsAndResources(t *testing.T) {
	s := mcptest.New()
	ts, _ := httpServer(t, s, mcptest.HTTPOptions{})
	changes := make(chan Change, 8)
	o := quickOpts(map[string]ServerConfig{"web": httpCfg(ts.URL)})
	o.OnChange = func(c Change) { changes <- c }
	m := startManager(t, o)
	m.Snapshot()
	if got := len(m.Prompts()); got != 2 {
		t.Fatalf("prompts = %d", got)
	}
	s.NotifyPromptsChanged()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case c := <-changes:
			if c.Kind == ListPrompts {
				return
			}
		case <-time.After(20 * time.Millisecond):
			s.NotifyPromptsChanged()
		case <-deadline:
			t.Fatal("prompts change never reported")
		}
	}
}

func TestOnChangePanicIsContained(t *testing.T) {
	s := mcptest.New()
	ts, _ := httpServer(t, s, mcptest.HTTPOptions{})
	var n atomic.Int32
	o := quickOpts(map[string]ServerConfig{"web": httpCfg(ts.URL)})
	o.OnChange = func(Change) {
		if n.Add(1) == 1 {
			panic("owner bug")
		}
	}
	m := startManager(t, o)
	m.Snapshot()
	env, _ := testEnv(newRecorder(true))
	tl := findTool(t, m.Preview().Tools(), "__add_tool")
	runTool(t, tl, env, `{}`)
	waitFor(t, "first change", func() bool { return n.Load() >= 1 })
	runTool(t, tl, env, `{}`)
	waitFor(t, "second change (the refresher survived the panic)", func() bool { return n.Load() >= 2 })
}

func TestCrashRestartsWithBackoffAndTheSnapshotDoesNotMove(t *testing.T) {
	skipNotUnix(t)
	changes := make(chan Change, 8)
	o := quickOpts(map[string]ServerConfig{"srv": helperCfg("", nil)})
	o.OnChange = func(c Change) { changes <- c }
	m := startManager(t, o)
	snap := m.Snapshot()
	env, _ := testEnv(newRecorder(true))
	crash := findTool(t, snap.Tools(), "__crash")

	res := runTool(t, crash, env, `{}`)
	if !res.IsError || !strings.Contains(res.Text, "connection was lost") || !strings.Contains(res.Text, "exit status 3") {
		t.Errorf("crash result = %q", res.Text)
	}
	// The next call rides the restart: it waits for the new process instead of failing.
	res = runTool(t, findTool(t, snap.Tools(), "__echo"), env, `{"message":"after the crash"}`)
	if res.IsError || res.Text != "after the crash" {
		t.Fatalf("call after a crash = %+v", res)
	}
	st := statusOf(m, "srv")
	if st.State != StateReady || st.Restarts < 1 {
		t.Errorf("status = %+v", st)
	}
	if m.Preview().Hash() != snap.Hash() {
		t.Error("a crash and restart must not change the tool snapshot: it would rewrite the cached prefix for nothing")
	}
	select {
	case c := <-changes:
		t.Errorf("a restart into an identical tool list must not fire OnChange: %+v", c)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRestartIntoADifferentToolListIsAnEpochEvent(t *testing.T) {
	s := mcptest.New()
	ts, h := httpServer(t, s, mcptest.HTTPOptions{JSONOnly: true, NoStream: true})
	changes := make(chan Change, 8)
	o := quickOpts(map[string]ServerConfig{"web": httpCfg(ts.URL)})
	o.OnChange = func(c Change) { changes <- c }
	m := startManager(t, o)
	m.Snapshot()

	s.AddTool(mcptest.Tool{Name: "brand_new", Description: "arrived while we were away", Handler: func(context.Context, *mcptest.Call) *mcptest.Result { return mcptest.TextResult("new") }})
	h.ExpireSessions() // the server "restarted": our session is gone
	env, _ := testEnv(newRecorder(true))
	// The next call trips over the dead session; the manager re-initialises.
	runTool(t, findTool(t, m.Preview().Tools(), "__echo"), env, `{"message":"x"}`)
	select {
	case c := <-changes:
		if c.Reason != "reconnected" || c.Kind != ListTools {
			t.Errorf("change = %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a server that came back with different tools must be reported")
	}
	if !containsAll(m.Preview().Names(), "mcp__web__brand_new") {
		t.Errorf("preview = %v", m.Preview().Names())
	}
}

func TestCrashLoopIsBoundedByBackoffAndParked(t *testing.T) {
	skipNotUnix(t)
	var mu sync.Mutex
	var logs []string
	o := quickOpts(map[string]ServerConfig{"flaky": helperCfg("exit", nil)})
	o.Backoff = Backoff{Min: 40 * time.Millisecond, Max: 200 * time.Millisecond, MaxFailures: 4}
	o.Logf = func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }
	m := NewManager(o)
	t.Cleanup(func() { _ = m.Close() })
	start := time.Now()
	err := m.Start(context.Background())
	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v", err)
	}
	st := waitState(t, m, "flaky", StateFailed)
	took := time.Since(start)
	// 4 attempts with delays 40, 80, 160 between them.
	if took < 250*time.Millisecond {
		t.Errorf("gave up after %v: the delays 40+80+160ms were not observed", took)
	}
	if !strings.Contains(st.Error, "exit status 3") || !strings.Contains(st.Error, "missing configuration") {
		t.Errorf("status error = %q", st.Error)
	}
	mu.Lock()
	attempts := 0
	for _, l := range logs {
		if strings.Contains(l, "failure ") {
			attempts++
		}
	}
	mu.Unlock()
	if attempts != 4 {
		t.Errorf("%d attempts logged, want 4 (the crash loop guard)", attempts)
	}
	// Parked: nothing more happens by itself...
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	again := 0
	for _, l := range logs {
		if strings.Contains(l, "failure ") {
			again++
		}
	}
	mu.Unlock()
	if again != 4 {
		t.Errorf("a parked server kept retrying: %d attempts", again)
	}
	// ...until told to try again.
	if err := m.Reconnect("flaky"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "another round of attempts", func() bool {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, l := range logs {
			if strings.Contains(l, "failure ") {
				n++
			}
		}
		return n > 4
	})
}

func TestStableConnectionsResetTheFailureCount(t *testing.T) {
	skipNotUnix(t)
	o := quickOpts(map[string]ServerConfig{"srv": helperCfg("", nil)})
	o.Backoff = Backoff{Min: 10 * time.Millisecond, Max: 20 * time.Millisecond, MaxFailures: 2}
	o.StableAfter = 50 * time.Millisecond
	m := startManager(t, o)
	env, _ := testEnv(newRecorder(true))
	for i := 0; i < 5; i++ {
		tl := findTool(t, m.Preview().Tools(), "__crash")
		time.Sleep(120 * time.Millisecond) // long enough to count as stable
		runTool(t, tl, env, `{}`)
		waitState(t, m, "srv", StateReady)
	}
	if st := statusOf(m, "srv"); st.State != StateReady {
		t.Errorf("%+v: five crashes, each after a stable spell, must not exhaust MaxFailures=2", st)
	}
}

func TestCallsDuringARestartWaitOrFailAsConfigured(t *testing.T) {
	skipNotUnix(t)
	for _, wait := range []time.Duration{3 * time.Second, -1} {
		t.Run(fmt.Sprintf("wait=%v", wait), func(t *testing.T) {
			o := quickOpts(map[string]ServerConfig{"srv": helperCfg("", nil)})
			o.Backoff = Backoff{Min: 300 * time.Millisecond, Max: 300 * time.Millisecond, MaxFailures: 5}
			o.ReconnectWait = wait
			m := startManager(t, o)
			snap := m.Snapshot()
			env, _ := testEnv(newRecorder(true))
			runTool(t, findTool(t, snap.Tools(), "__crash"), env, `{}`)
			res := runTool(t, findTool(t, snap.Tools(), "__echo"), env, `{"message":"hello"}`)
			if wait > 0 {
				if res.IsError || res.Text != "hello" {
					t.Errorf("a call landing in the restart window should wait for it: %+v", res)
				}
				return
			}
			if !res.IsError || !strings.Contains(res.Text, "not connected") || !strings.Contains(res.Text, "restarting") {
				t.Errorf("with waiting disabled the call must fail at once, visibly: %+v", res)
			}
		})
	}
}

func TestManagerCloseStopsEveryServer(t *testing.T) {
	needProc(t)
	servers := map[string]ServerConfig{}
	pidFiles := map[string]string{}
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("s%d", i)
		pf := filepath.Join(dir, name+".pid")
		pidFiles[name] = pf
		servers[name] = helperCfg("grandchild", map[string]string{mcptest.EnvPidFile: pf})
	}
	m := NewManager(quickOpts(servers))
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var pids []int
	for name, pf := range pidFiles {
		var pid int
		waitFor(t, name+" grandchild", func() bool {
			b, err := os.ReadFile(pf)
			if err != nil {
				return false
			}
			_, err = fmt.Sscan(string(b), &pid)
			return err == nil && pid > 0
		})
		pids = append(pids, pid)
	}
	start := time.Now()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Close took %v", d)
	}
	for _, pid := range pids {
		waitFor(t, "grandchild to die", func() bool { return !processAlive(pid) })
	}
	for _, st := range m.Status() {
		if st.State != StateClosed {
			t.Errorf("%+v", st)
		}
	}
	m.Close() // idempotent
	env, _ := testEnv(newRecorder(true))
	if res := runTool(t, findTool(t, m.Preview().Tools(), "__echo"), env, `{"message":"x"}`); !res.IsError {
		t.Error("a closed manager must not run tools")
	}
}

func TestPromptsAreSlashCommands(t *testing.T) {
	ts, _ := httpServer(t, mcptest.New(), mcptest.HTTPOptions{})
	m := startManager(t, quickOpts(map[string]ServerConfig{"web": httpCfg(ts.URL)}))
	ps := m.Prompts()
	if len(ps) != 2 || ps[0].Command != "/mcp__web__greet" || ps[1].Command != "/mcp__web__review" {
		t.Fatalf("prompts = %+v", ps)
	}
	greet := ps[0]
	if greet.Server != "web" || greet.Name != "greet" || len(greet.Arguments) != 1 || !greet.Arguments[0].Required {
		t.Errorf("%+v", greet)
	}
	ctx := context.Background()
	if _, err := m.GetPrompt(ctx, "/mcp__web__greet", nil); err == nil || !strings.Contains(err.Error(), `needs the argument "name"`) {
		t.Errorf("missing argument: %v", err)
	}
	r, err := m.GetPrompt(ctx, "/mcp__web__greet", map[string]string{"name": "Ada"})
	if err != nil || len(r.Messages) != 1 || r.Messages[0].Role != "user" || !strings.Contains(r.Messages[0].Content.Text, "greet Ada") {
		t.Fatalf("%+v %v", r, err)
	}
	if got := r.Text(); got != "Please greet Ada warmly." {
		t.Errorf("Text() = %q", got)
	}
	r, err = m.GetPrompt(ctx, "/mcp__web__review", nil)
	if err != nil || len(r.Messages) != 2 || r.Messages[1].Role != "assistant" {
		t.Fatalf("%+v %v", r, err)
	}
	if got := r.Text(); got != "Review the diff.\n\nassistant: Send it over." {
		t.Errorf("Text() = %q", got)
	}
	if _, err := m.GetPrompt(ctx, "/mcp__web__nope", nil); err == nil {
		t.Error("unknown prompt")
	}
}

func TestPromptNameCollisionsAreHashed(t *testing.T) {
	s := mcptest.New()
	render := func(map[string]string) []map[string]any {
		return []map[string]any{{"role": "user", "content": mcptest.Text("x")}}
	}
	s.SetPrompts(mcptest.Prompt{Name: "a.b", Render: render}, mcptest.Prompt{Name: "a_b", Render: render}, mcptest.Prompt{Name: "ok", Render: render})
	ts, _ := httpServer(t, s, mcptest.HTTPOptions{})
	m := startManager(t, quickOpts(map[string]ServerConfig{"web": httpCfg(ts.URL)}))
	seen := map[string]bool{}
	for _, p := range m.Prompts() {
		if seen[p.Command] {
			t.Errorf("duplicate command %s", p.Command)
		}
		seen[p.Command] = true
	}
	if len(seen) != 3 || !seen["/mcp__web__ok"] {
		t.Errorf("commands = %v", seen)
	}
}

func TestResources(t *testing.T) {
	for name, mk := range map[string]func(t *testing.T) *Manager{
		"http": func(t *testing.T) *Manager {
			ts, _ := httpServer(t, mcptest.New(), mcptest.HTTPOptions{})
			return startManager(t, quickOpts(map[string]ServerConfig{"srv": httpCfg(ts.URL)}))
		},
		"stdio": func(t *testing.T) *Manager {
			skipNotUnix(t)
			return startManager(t, quickOpts(map[string]ServerConfig{"srv": helperCfg("", nil)}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := mk(t)
			ctx := context.Background()
			rs, err := m.ListResources(ctx, "srv")
			if err != nil || len(rs) != 2 || rs[0].URI != "file:///readme.md" || rs[0].MIMEType != "text/markdown" {
				t.Fatalf("%+v %v", rs, err)
			}
			tpl, err := m.ListResourceTemplates(ctx, "srv")
			if err != nil || len(tpl) != 1 || tpl[0].URITemplate != "file:///{path}" {
				t.Fatalf("%+v %v", tpl, err)
			}
			rd, err := m.ReadResource(ctx, "srv", "file:///readme.md")
			if err != nil || len(rd.Contents) != 1 || rd.Contents[0].Text != "# Readme\nhello" {
				t.Fatalf("%+v %v", rd, err)
			}
			bl, err := m.ReadResource(ctx, "srv", "mem://blob")
			if err != nil || len(bl.Contents[0].Blob) != 4 || bl.Contents[0].Blob[3] != 3 {
				t.Fatalf("%+v %v", bl, err)
			}
			_, err = m.ReadResource(ctx, "srv", "file:///missing")
			var rpc *RPCError
			if !errors.As(err, &rpc) || rpc.Code != CodeResourceNotFound {
				t.Errorf("err = %v", err)
			}
			if _, err := m.ListResources(ctx, "nope"); !errors.Is(err, ErrUnknownServer) {
				t.Errorf("err = %v", err)
			}
		})
	}
	t.Run("a server without resources says so", func(t *testing.T) {
		s := mcptest.New()
		s.Capabilities = map[string]any{"tools": map[string]any{}}
		ts, _ := httpServer(t, s, mcptest.HTTPOptions{})
		m := startManager(t, quickOpts(map[string]ServerConfig{"srv": httpCfg(ts.URL)}))
		if _, err := m.ListResources(context.Background(), "srv"); !errors.Is(err, ErrUnsupported) {
			t.Errorf("err = %v", err)
		}
		if len(m.Prompts()) != 0 {
			t.Error("no prompt capability, no prompts")
		}
	})
}

func TestStatusIsFreeOfSecrets(t *testing.T) {
	skipNotUnix(t)
	cfg := helperCfg("secret-stderr", map[string]string{mcptest.EnvSecret: placeholder + "-status"})
	o := quickOpts(map[string]ServerConfig{"leaky": cfg})
	o.Backoff.MaxFailures = 1
	var logs []string
	var mu sync.Mutex
	o.Logf = func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }
	m := NewManager(o)
	t.Cleanup(func() { _ = m.Close() })
	_ = m.Start(context.Background())
	waitState(t, m, "leaky", StateFailed)
	st := statusOf(m, "leaky")
	if strings.Contains(st.Error, placeholder) {
		t.Errorf("status leaks: %q", st.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range logs {
		if strings.Contains(l, placeholder) {
			t.Errorf("log leaks: %q", l)
		}
	}
}

func TestManagerRoundTripOfSnapshotHashAcrossSpecs(t *testing.T) {
	ts, _ := httpServer(t, mcptest.New(), mcptest.HTTPOptions{})
	m := startManager(t, quickOpts(map[string]ServerConfig{"web": httpCfg(ts.URL)}))
	snap := m.Snapshot()
	b, err := core.MarshalStable(snap.Specs())
	if err != nil || core.HashBytes(b) != snap.Hash() {
		t.Errorf("the hash is not the hash of the marshalled specs: %v", err)
	}
	var back []core.ToolSpec
	if err := json.Unmarshal(b, &back); err != nil || len(back) != snap.Len() {
		t.Errorf("specs do not round-trip: %v", err)
	}
}
