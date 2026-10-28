package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

func TestBuiltinIDsAreValidUniqueAndOrdered(t *testing.T) {
	seen := map[string]bool{}
	ms := Builtin()
	for _, m := range ms {
		if !ValidID(m.ID) {
			t.Errorf("invalid id %q", m.ID)
		}
		if seen[m.ID] {
			t.Errorf("duplicate id %q", m.ID)
		}
		seen[m.ID] = true
		if m.Title == "" || m.Description == "" {
			t.Errorf("%s: title and description are required", m.ID)
		}
		if m.Core && m.CoreReason == "" {
			t.Errorf("%s: a core plugin says why it is core", m.ID)
		}
	}
	again := Builtin()
	sortManifests(again)
	if !reflect.DeepEqual(ms, again) {
		t.Error("Builtin is not in catalogue order")
	}
	for _, m := range ms {
		for _, r := range m.Requires {
			if !seen[r] {
				t.Errorf("%s requires unknown %q", m.ID, r)
			}
		}
	}
}

func TestStandardProfileKeepsTodaysDefaults(t *testing.T) {
	s, warns := Resolve(Builtin(), Input{})
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	if s.Profile != DefaultProfile {
		t.Errorf("profile = %q", s.Profile)
	}
	for _, id := range []string{FS, Shell, Web, MCP, Skills, BuiltinSkills, Swarm, AutoDocs, ManagerApproves, PushProgress, "view:cache", "core:kernel"} {
		if !s.On(id) {
			st, _ := s.Get(id)
			t.Errorf("%s should be on: %+v", id, st)
		}
	}
	if s.On(Mailman) {
		t.Error("mailman is off by default, as swarm.mailman was")
	}
}

func TestMinimalProfileKeepsOnlyItsPlugins(t *testing.T) {
	s, _ := Resolve(Builtin(), Input{Profile: "minimal"})
	if !s.On(FS) || !s.On(Shell) || !s.On("core:agent-loop") {
		t.Error("minimal keeps files, shell and core")
	}
	for _, id := range []string{Web, Swarm, Skills, "view:cache"} {
		if s.On(id) {
			t.Errorf("%s should be off in minimal", id)
		}
	}
	st, _ := s.Get(Web)
	if st.Layer != LayerProfile {
		t.Errorf("web layer = %q, want profile", st.Layer)
	}
}

func TestHigherLayerWinsAndCoreStaysOn(t *testing.T) {
	in := Input{}
	in.Add(Web, false, LayerFlag)
	in.Add(Web, true, LayerUser)
	in.Add("core:kernel", false, LayerFlag)
	in.Add(Mailman, true, LayerProject)
	in.Add(Mailman, false, LayerProject) // a later choice of the same layer wins
	s, _ := Resolve(Builtin(), in)
	if s.On(Web) {
		t.Error("the flag turned web off")
	}
	if st, _ := s.Get(Web); st.Layer != LayerFlag {
		t.Errorf("layer = %s", st.Layer)
	}
	if !s.On("core:kernel") {
		t.Error("core cannot be turned off")
	}
	if s.On(Mailman) {
		t.Error("the later project choice turns the mailman off")
	}
}

func TestPendingWhenARequirementIsOff(t *testing.T) {
	in := Input{}
	in.Add(Swarm, false, LayerUser)
	in.Add(Mailman, true, LayerUser)
	s, _ := Resolve(Builtin(), in)
	st, _ := s.Get(Mailman)
	if st.State != StatePending || !strings.Contains(st.Reason, "needs swarm") {
		t.Fatalf("mailman = %s (%s), want pending on swarm", st.State, st.Reason)
	}
	if s.On(Mailman) {
		t.Error("a pending plugin is not on")
	}
	// transitively: built-in skills need skills, which need the skill tool
	in2 := Input{}
	in2.Add(SkillTool, false, LayerUser)
	s2, _ := Resolve(Builtin(), in2)
	for _, id := range []string{Skills, BuiltinSkills} {
		if st, _ := s2.Get(id); st.State != StatePending {
			t.Errorf("%s = %s, want pending", id, st.State)
		}
	}
}

func TestCycleIsAnErrorNotAHang(t *testing.T) {
	cat := []Manifest{
		{ID: "a", Title: "a", Description: "a", Requires: []string{"b"}},
		{ID: "b", Title: "b", Description: "b", Requires: []string{"a"}},
		{ID: "c", Title: "c", Description: "c", Requires: []string{"x-service"}},
		{ID: "d", Title: "d", Description: "d", Provides: []string{"x-service"}},
	}
	s, warns := Resolve(cat, Input{})
	if len(warns) == 0 || !strings.Contains(warns[0], "cycle") {
		t.Errorf("warnings = %v", warns)
	}
	if st, _ := s.Get("a"); st.State != StateError {
		t.Errorf("a = %s", st.State)
	}
	if !s.On("c") {
		t.Error("c's requirement is provided by d")
	}
	if got := s.Order(); indexOf(got, "d") > indexOf(got, "c") {
		t.Errorf("order %v: d must come before c", got)
	}
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

func TestUnknownIDsWarnWithASuggestion(t *testing.T) {
	in := Input{Profile: "nope"}
	in.Add("mailmen", true, LayerUser)
	_, warns := Resolve(Builtin(), in)
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, `did you mean "mailman"`) || !strings.Contains(joined, `unknown profile "nope"`) {
		t.Errorf("warnings = %v", warns)
	}
}

func TestNilSetReportsOn(t *testing.T) {
	var s *Set
	if !s.On(Web) || s.List() != nil {
		t.Error("a nil set is everything on")
	}
}

func TestKernelActivatesInOrderAndDisposesInReverse(t *testing.T) {
	cat := []Manifest{
		{ID: "base", Title: "b", Description: "b"},
		{ID: "top", Title: "t", Description: "t", Requires: []string{"base"}},
		{ID: "bad", Title: "x", Description: "x"},
		{ID: "needsbad", Title: "n", Description: "n", Requires: []string{"bad"}},
	}
	var log []string
	act := func(id string) Activator {
		return func(sc *Scope) error {
			log = append(log, "on "+id)
			sc.OnDispose(func() { log = append(log, "off "+id+" 1") })
			sc.OnDispose(func() { log = append(log, "off "+id+" 2") })
			return nil
		}
	}
	k := NewKernel(map[string]Activator{"top": act("top"), "base": act("base"), "needsbad": act("needsbad"),
		"bad": func(*Scope) error { return errors.New("boom") }})
	var states []string
	k.OnState = func(id, st, _ string) { states = append(states, id+"="+st) }
	s, _ := Resolve(cat, Input{})
	if err := k.Start(s); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Get("bad"); st.State != StateError {
		t.Errorf("bad = %s", st.State)
	}
	if got := k.Active(); !reflect.DeepEqual(got, []string{"base", "top"}) {
		t.Errorf("active = %v (needsbad must wait on bad)", got)
	}
	in := Input{}
	in.Add("top", false, LayerUser)
	next, _ := Resolve(cat, in)
	if err := k.Reconcile(next); err != nil {
		t.Fatal(err)
	}
	if err := k.Stop(); err != nil {
		t.Fatal(err)
	}
	want := []string{"on base", "on top", "off top 2", "off top 1", "off base 2", "off base 1"}
	if !reflect.DeepEqual(log, want) {
		t.Errorf("log = %v\nwant  %v", log, want)
	}
	if !strings.Contains(strings.Join(states, " "), "bad=error base=on top=on top=off") {
		t.Errorf("states = %v", states)
	}
}

func TestCoreFailureStopsStart(t *testing.T) {
	cat := []Manifest{{ID: "core:x", Title: "x", Description: "x", Core: true, CoreReason: "r"}}
	k := NewKernel(map[string]Activator{"core:x": func(*Scope) error { panic("no") }})
	s, _ := Resolve(cat, Input{})
	if err := k.Start(s); !errors.Is(err, ErrCore) {
		t.Errorf("err = %v", err)
	}
}

func TestMarkRestart(t *testing.T) {
	run, _ := Resolve(Builtin(), Input{})
	in := Input{}
	in.Add(Web, false, LayerUser)
	next, _ := Resolve(Builtin(), in)
	m, any := MarkRestart(run, next)
	if !any {
		t.Fatal("web changed")
	}
	if st, _ := m.Get(Web); st.State != StateRestart || !strings.Contains(st.Reason, "turns off") {
		t.Errorf("web = %+v", st)
	}
	if st, _ := next.Get(Web); st.State != StateOff {
		t.Error("MarkRestart must not change its input")
	}
}

func TestMarkRestartKeepsFlagDecisions(t *testing.T) {
	in := Input{}
	in.Add(Mailman, true, LayerFlag)
	run, _ := Resolve(Builtin(), in)
	next, _ := Resolve(Builtin(), Input{}) // the files alone: the mailman is off in the standard profile
	m, any := MarkRestart(run, next)
	if any {
		t.Error("a plugin a flag turned on does not wait for a restart")
	}
	if st, _ := m.Get(Mailman); st.State != StateOn || st.Layer != LayerFlag {
		t.Errorf("mailman = %+v, want on by flag", st)
	}
}

func TestFromConfigReadsRowsLegacyAndLayers(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(config.UserConfigPath(home), `{"swarm":{"mailman":true},"plugins":{"enabled":{"web":false,"view:cache":false}}}`)
	write(config.ProjectConfigPath(root), `{"plugins":{"enabled":{"web":true,"shell":false}}}`)
	cfg, rep, err := config.Load(config.LoadOpts{Cwd: root, Root: root, Home: home, UntrustedProject: true})
	if err != nil {
		t.Fatal(err)
	}
	s, warns := Resolve(Builtin(), FromConfig(cfg, rep))
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
	if !s.On(Mailman) {
		t.Error("swarm.mailman=true still turns the mailman on")
	}
	if s.On(Web) {
		t.Error("an untrusted project cannot turn web back on")
	}
	if s.On(Shell) {
		t.Error("a project may turn a plugin off")
	}
	if st, _ := s.Get(Shell); st.Layer != LayerProject {
		t.Errorf("shell layer = %s", st.Layer)
	}
	if s.On("view:cache") {
		t.Error("view:cache row with a colon is read")
	}
}

func TestSaveWritesAMinimalDifference(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"swarm":{"mailman":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f, tr := false, true
	if err := Save(p, "", Web, &f, nil); err != nil {
		t.Fatal(err)
	}
	if err := Save(p, "", Mailman, &f, nil); err != nil { // equal to the default: removed, and the old key folded in
		t.Fatal(err)
	}
	if err := Save(p, "", "ext-thing", &tr, map[string]any{"color": "red"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	got := string(b)
	for _, want := range []string{`"web": false`, `"ext-thing": true`, `"color": "red"`} {
		if !strings.Contains(got, want) {
			t.Errorf("file lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "mailman") {
		t.Errorf("mailman should be gone:\n%s", got)
	}
	if err := Save(p, "", Web, &tr, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), `"web"`) {
		t.Errorf("back to the default removes the row:\n%s", b)
	}
	if err := SaveProfile(p, "minimal"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), `"profile": "minimal"`) {
		t.Errorf("profile not written:\n%s", b)
	}
}

func TestSummary(t *testing.T) {
	got := Summary([]Contribution{{KindTool, "read"}, {KindTool, "write"}, {KindView, "cache"}})
	if got != "tool: read, write; view: cache" {
		t.Errorf("Summary = %q", got)
	}
}

func TestPluginFilesExpandTheDirectoryAndSettings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(`{"SessionStart":[{"hooks":[{"type":"command","command":"echo ${setting:greeting} ${setting:who} from ${SLEIPNIR_PLUGIN_ROOT}"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{ID: "hello", Dir: dir, Config: &Schema{Type: "object", Properties: map[string]Field{
		"greeting": {Type: "string", Default: "Hello"}, "who": {Type: "string"}}}}
	h, err := HookEntries(m, map[string]any{"who": `a "quoted" name`})
	if err != nil {
		t.Fatal(err)
	}
	got := string(h["SessionStart"])
	q, _ := json.Marshal(dir) // the directory as JSON string text (backslashes escaped on Windows)
	if !strings.Contains(got, `echo Hello a \"quoted\" name from `+string(q[1:len(q)-1])) {
		t.Errorf("expanded = %s", got)
	}
}

// An untrusted project may turn off a built-in plugin, but not the hooks plugin or an external one, whose hooks may be the
// user's guards.
func TestProjectMayNotTurnOffGuards(t *testing.T) {
	for id, keep := range map[string]bool{Hooks: true, "acme-guard": true, Mailman: false, RolePrefix + "docs": false} {
		if got := config.KeepEnabledAgainstProject(id); got != keep {
			t.Errorf("%s: kept=%v, want %v", id, got, keep)
		}
	}
}
