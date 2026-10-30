package settings

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/plugin"
)

// fakeProgram writes a program named name into dir that prints "name 1.0" and returns its path.
func fakeProgram(t *testing.T, dir, name string) string {
	t.Helper()
	p, body := filepath.Join(dir, name), "#!/bin/sh\necho '"+name+" 1.0'\n"
	if runtime.GOOS == "windows" {
		p, body = p+".cmd", "@echo "+name+" 1.0\r\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// needsEnv is a test server whose needs are looked for only in bin.
func needsEnv(t *testing.T) (*testEnv, string) {
	e := newEnv(t, nil)
	bin := t.TempDir()
	e.svc.needEnv = func(env plugin.NeedEnv) plugin.NeedEnv {
		env.PATH, env.SkipDefaults = bin, true
		return env
	}
	return e, bin
}

// confirmed sends a request that needs a confirmation: the first answer is 428 with the scope, the repeat with an id for it succeeds.
func (e *testEnv) confirmed(method, path string, body, v any) {
	e.t.Helper()
	rec := e.do(method, path, body, nil)
	if rec.Code != http.StatusPreconditionRequired {
		e.t.Fatalf("%s %s without a confirmation: %d %s", method, path, rec.Code, rec.Body)
	}
	scope := rec.Header().Get("X-Confirm-Scope")
	e.ok(method, path, body, map[string]string{"X-Confirm": e.confirm(scope)}, v)
}

func needByID(t *testing.T, rows []needRow, id string) needRow {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no need %s in %+v", id, rows)
	return needRow{}
}

func TestNeedsListTheBuiltinProgramsWithTheirInstallCommands(t *testing.T) {
	e, _ := needsEnv(t)
	var v needsView
	e.ok("GET", "/api/needs", nil, nil, &v)
	b := needByID(t, v.Needs, "blender")
	if b.Found || b.Plugin != plugin.BuiltinSkills || b.Server {
		t.Errorf("blender = %+v", b)
	}
	for _, m := range b.Methods {
		if len(m.Argv) == 0 || m.Available {
			t.Errorf("blender's %s runs %v and no package manager is on PATH: %+v", m.ID, m.Argv, m)
		}
	}
	if s := needByID(t, v.Needs, "rea-mcp"); !s.Server || len(s.Methods) != 1 || !strings.Contains(s.Methods[0].Connect, `"rea"`) {
		t.Errorf("rea-mcp = %+v", s)
	}
	var pv pluginsView
	e.ok("GET", "/api/plugins", nil, nil, &pv)
	if len(pv.Needs) != len(v.Needs) {
		t.Errorf("the catalogue carries %d needs, /api/needs %d", len(pv.Needs), len(v.Needs))
	}
}

func TestLocateRemembersAProgramAndForgetDropsIt(t *testing.T) {
	e, _ := needsEnv(t)
	prog := fakeProgram(t, t.TempDir(), "my-blender")
	var row needRow
	e.confirmed("POST", "/api/needs/blender/locate", locateWrite{Path: prog}, &row)
	if !row.Found || row.Via != "located" || row.Path != prog || row.Version != "my-blender 1.0" {
		t.Fatalf("after locate: %+v", row)
	}
	var v needsView
	e.ok("GET", "/api/needs", nil, nil, &v)
	if b := needByID(t, v.Needs, "blender"); !b.Found || b.Path != prog {
		t.Errorf("listed after locate: %+v", b)
	}
	shim := filepath.Join(e.home, ".sleipnir", "bin", "blender")
	if runtime.GOOS == "windows" {
		shim += ".cmd"
	}
	if b, err := os.ReadFile(shim); err != nil || !strings.Contains(string(b), prog) {
		t.Errorf("launcher %s: %q, %v", shim, b, err)
	}
	e.ok("DELETE", "/api/needs/blender/locate", nil, nil, &row)
	if row.Found {
		t.Errorf("after forget: %+v", row)
	}
	if _, err := os.Stat(shim); !os.IsNotExist(err) {
		t.Errorf("the launcher stays: %v", err)
	}
}

func TestLocateRefusesWhatIsNotTheProgram(t *testing.T) {
	e, _ := needsEnv(t)
	plain := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"blender", plain, ""} {
		rec := e.do("POST", "/api/needs/blender/locate", locateWrite{Path: p}, nil)
		if sc := rec.Header().Get("X-Confirm-Scope"); sc != "" {
			rec = e.do("POST", "/api/needs/blender/locate", locateWrite{Path: p}, map[string]string{"X-Confirm": e.confirm(sc)})
		}
		if rec.Code == http.StatusOK {
			t.Errorf("located %q: %s", p, rec.Body)
		}
	}
	if rec := e.do("POST", "/api/needs/rea-mcp/locate", locateWrite{Path: plain}, nil); rec.Code == http.StatusOK {
		t.Errorf("a tool server was located: %s", rec.Body)
	}
	if rec := e.do("POST", "/api/needs/nothing/locate", locateWrite{Path: plain}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown need: %d", rec.Code)
	}
}

func TestInstallRunsOnlyADeclaredMethodAndConnectsTheServer(t *testing.T) {
	e, bin := needsEnv(t)
	if rec := e.do("POST", "/api/needs/rea-mcp/install", installWrite{Method: "connect"}, nil); rec.Code != http.StatusConflict {
		t.Errorf("connect without npx: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/api/needs/rea-mcp/install", installWrite{Method: "rm -rf"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("an undeclared method: %d %s", rec.Code, rec.Body)
	}
	npx := fakeProgram(t, bin, "npx")
	var v0 needsView
	e.ok("GET", "/api/needs", nil, nil, &v0)
	scope := needByID(t, v0.Needs, "rea-mcp").Methods[0].Scope
	rec := e.do("POST", "/api/needs/rea-mcp/install", installWrite{Method: "connect"}, nil)
	if rec.Code != http.StatusPreconditionRequired || rec.Header().Get("X-Confirm-Scope") != scope || !strings.Contains(rec.Body.String(), "tool server") {
		t.Fatalf("an install without a confirmation: %d %q %s (the listed scope is %q)", rec.Code, rec.Header().Get("X-Confirm-Scope"), rec.Body, scope)
	}
	if b, _ := os.ReadFile(config.UserConfigPath(e.home)); strings.Contains(string(b), "rea") {
		t.Fatalf("an unconfirmed install changed the configuration: %s", b)
	}
	var row needRow
	e.ok("POST", "/api/needs/rea-mcp/install", installWrite{Method: "connect"}, map[string]string{"X-Confirm": e.confirm(scope)}, &row)
	if row.Install == nil || row.Install.Method != "connect" {
		t.Fatalf("started: %+v", row)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var v needsView
		e.ok("GET", "/api/needs", nil, nil, &v)
		r := needByID(t, v.Needs, "rea-mcp")
		if r.Install != nil && !r.Install.Running {
			e.ok("GET", "/api/needs", nil, nil, &v) // the row above may have been found just before the install ended
			if r = needByID(t, v.Needs, "rea-mcp"); r.Install.Error != "" || !r.Found || r.Via != "config" {
				t.Fatalf("after install: %+v %+v", r, *r.Install)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the install did not finish: %+v", r)
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, _ := os.ReadFile(config.UserConfigPath(e.home))
	if !strings.Contains(string(b), `"rea"`) || !strings.Contains(string(b), filepath.ToSlash(npx)) && !strings.Contains(string(b), strings.ReplaceAll(npx, `\`, `\\`)) {
		t.Errorf("config = %s", b)
	}
}

// A confirmation covers what the install links when it ends (the commands and where they are found), and a command the page
// cannot show whole is not run from it.
func TestInstallIsConfirmedOnEverythingItChanges(t *testing.T) {
	n := plugin.Need{ID: "tool", Commands: []string{"tool"}}
	m := plugin.MethodStatus{ID: "brew", Argv: []string{"brew", "install", "tool"}, Available: true}
	other := n
	other.Commands = []string{"tool", "git"}
	if installScope(n, m) == installScope(other, m) {
		t.Error("a need that links another command shares the confirmation")
	}
	other = n
	other.Paths = map[string][]string{"unix": {"/tmp/*/tool"}}
	if installScope(n, m) == installScope(other, m) {
		t.Error("a need found somewhere else shares the confirmation")
	}
	if !shownWhole(plugin.Need{}, m) {
		t.Error("a plain command is not shown whole")
	}
	for _, a := range []string{strings.Repeat("x", 300), "a\x1b[2Kb", "a‮b"} {
		long := m
		long.Setup = [][]string{{"sh", "-c", a}}
		if shownWhole(plugin.Need{}, long) {
			t.Errorf("%q is shown whole", a)
		}
	}
	if shownWhole(plugin.Need{Commands: []string{"to\x1b[2Kol"}}, m) || shownWhole(plugin.Need{Paths: map[string][]string{"unix": {"/opt/\x1b[2K/tool"}}}, m) {
		t.Error("a linked command or a default location that loses characters is shown whole")
	}
	long := m
	long.Connect = "adds the \"srv\" tool server (srv " + strings.Repeat("--flag ", 80) + ") to your configuration"
	if shownWhole(plugin.Need{}, long) {
		t.Error("a tool server entry cut short is shown whole")
	}
	if got := pathsText(map[string][]string{"unix": {"/opt/tool"}, "darwin": {"/a", "/b"}}); got != " (looked for in darwin: /a, /b; unix: /opt/tool)" {
		t.Errorf("pathsText = %q", got)
	}
	if got := shownWords([]string{"sh", "-c", "echo hi", ""}); got != `sh -c "echo hi" ""` {
		t.Errorf("shownWords = %s", got)
	}
}
