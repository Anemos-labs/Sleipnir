package plugin

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "write the golden files of the plugin contract fixtures")

// contractView is what Sleipnir reads of a plugin, with the plugin's directory written as <DIR> so the golden file does not depend
// on where the repository is.
type contractView struct {
	Manifest Manifest                   `json:"manifest"`
	Hooks    map[string]json.RawMessage `json:"hooks"`
	MCP      map[string]json.RawMessage `json:"mcp"`
	Problems []string                   `json:"problems"`
	State    string                     `json:"state"`
	Reason   string                     `json:"reason,omitempty"`
}

// readContract reads the plugin in dir the way a session does: the manifest, its hooks and MCP servers expanded with its default
// settings, Check's findings, and the state it resolves to when a person has turned it on.
func readContract(t *testing.T, dir string) contractView {
	t.Helper()
	known := []string{}
	for _, m := range Builtin() {
		known = append(known, m.ID)
	}
	m, problems := Check(dir, known)
	v := contractView{Manifest: m, Problems: []string{}}
	for _, p := range problems {
		v.Problems = append(v.Problems, p.String())
	}
	if m.ID == "" {
		return v
	}
	var err error
	if v.Hooks, err = HookEntries(m, nil); err != nil {
		t.Fatalf("%s: hooks: %v", dir, err)
	}
	if v.MCP, err = MCPServers(m, nil); err != nil {
		t.Fatalf("%s: mcp: %v", dir, err)
	}
	in := Input{Choices: map[string][]Choice{m.ID: {{Enabled: true, Layer: LayerUser}}}}
	set, _ := Resolve(append(Builtin(), m), in)
	st, _ := set.Get(m.ID)
	v.State, v.Reason = st.State, st.Reason
	return v
}

// render is the view as stable JSON with the plugin's directory replaced.
func (v contractView) render(t *testing.T, dir string) []byte {
	t.Helper()
	v.Manifest.Dir = "<DIR>"
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(dir)
	return append([]byte(strings.ReplaceAll(string(b), string(quoted[1:len(quoted)-1]), "<DIR>")), '\n')
}

// TestPluginContract holds every plugin API version to what it promised: each fixture plugin of testdata/contract/v<N> is read
// exactly as its golden file says (testdata/contract/README.md says what to do when this fails).
func TestPluginContract(t *testing.T) {
	versions, err := filepath.Glob(filepath.Join("testdata", "contract", "v*"))
	if err != nil || len(versions) == 0 {
		t.Fatalf("no contract fixtures: %v", err)
	}
	for v := MinAPIVersion; v <= APIVersion; v++ {
		if _, err := os.Stat(filepath.Join("testdata", "contract", "v"+strconv.Itoa(v))); err != nil {
			t.Errorf("plugin API %d is read but has no fixtures in testdata/contract/v%d", v, v)
		}
	}
	for _, vdir := range versions {
		plugins, _ := os.ReadDir(vdir)
		for _, p := range plugins {
			if !p.IsDir() {
				continue
			}
			dir, _ := filepath.Abs(filepath.Join(vdir, p.Name()))
			t.Run(filepath.Base(vdir)+"/"+p.Name(), func(t *testing.T) {
				view := readContract(t, dir)
				if view.State != StateOn {
					t.Errorf("a plugin of a supported API version does not start when it is turned on: %s (%s)", view.State, view.Reason)
				}
				got := view.render(t, dir)
				golden := filepath.Join(dir, "golden.json")
				if *update {
					if err := os.WriteFile(golden, got, 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("%v (go test ./internal/plugin -run TestPluginContract -update writes it for new fixtures)", err)
				}
				if string(got) != string(want) {
					t.Errorf("this Sleipnir reads a plugin of plugin API %s differently from what that version promised; installed plugins would break.\n"+
						"See internal/plugin/testdata/contract/README.md.\ngot:\n%s\nwant:\n%s", filepath.Base(vdir), got, want)
				}
			})
		}
	}
}

// TestDocumentedExamplePluginKeepsWorking holds the example of docs/PLUGINS.md to the contract: people copy it.
func TestDocumentedExamplePluginKeepsWorking(t *testing.T) {
	dir, _ := filepath.Abs(filepath.Join("..", "..", "docs", "examples", "plugins", "hello"))
	view := readContract(t, dir)
	if view.State != StateOn {
		t.Fatalf("the example plugin does not start: %s (%s)", view.State, view.Reason)
	}
	for _, p := range view.Problems {
		if strings.HasPrefix(p, "error") {
			t.Errorf("the example plugin has a problem: %s", p)
		}
	}
	if len(view.Hooks["SessionStart"]) == 0 || !strings.Contains(string(view.Hooks["SessionStart"]), "Hello from the hello plugin") {
		t.Errorf("the example's hook lost its setting: %s", view.Hooks["SessionStart"])
	}
}

// TestNewerPluginAPIIsRefusedWithAReason: a plugin written for an API this Sleipnir does not read is in error, with a reason that
// says to update Sleipnir, and the plugins that need it wait instead of starting without it.
func TestNewerPluginAPIIsRefusedWithAReason(t *testing.T) {
	dir, _ := filepath.Abs(filepath.Join("testdata", "contract", "newer", "api-99"))
	m, problems := Check(dir, nil)
	if m.API != 99 {
		t.Fatalf("api = %d", m.API)
	}
	var sawAPI, sawField bool
	for _, p := range problems {
		sawAPI = sawAPI || p.Error && strings.Contains(p.What, "update Sleipnir")
		sawField = sawField || !p.Error && strings.Contains(p.What, `"brand_new_field"`)
	}
	if !sawAPI || !sawField {
		t.Errorf("problems = %v", problems)
	}
	dep := Manifest{ID: "needs-future", External: true, API: 1, Requires: []string{m.ID}}
	in := Input{Choices: map[string][]Choice{m.ID: {{Enabled: true, Layer: LayerUser}}, dep.ID: {{Enabled: true, Layer: LayerUser}}}}
	set, _ := Resolve(append(Builtin(), m, dep), in)
	st, _ := set.Get(m.ID)
	if st.State != StateError || !strings.Contains(st.Reason, "update Sleipnir") {
		t.Errorf("newer plugin = %s (%s)", st.State, st.Reason)
	}
	if d, _ := set.Get(dep.ID); d.State != StatePending {
		t.Errorf("a plugin that needs it = %s (%s)", d.State, d.Reason)
	}
	off, _ := Resolve(append(Builtin(), m), Input{})
	if st, _ := off.Get(m.ID); st.State != StateOff {
		t.Errorf("a newer plugin nobody turned on is simply off: %s", st.State)
	}
}

// TestRenamedBuiltinKeepsRequiresWorking: a plugin whose requires names a built-in plugin by an id it no longer has is read with
// the id it has now.
func TestRenamedBuiltinKeepsRequiresWorking(t *testing.T) {
	old := RenamedPlugins
	RenamedPlugins = map[string]string{"skill-files": Skills}
	t.Cleanup(func() { RenamedPlugins = old })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(`{"id":"uses-old","api":1,"requires":["skill-files"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	set, _ := Resolve(append(Builtin(), m), Input{Choices: map[string][]Choice{m.ID: {{Enabled: true, Layer: LayerUser}}}})
	if st, _ := set.Get(m.ID); st.State != StateOn {
		t.Errorf("state = %s (%s)", st.State, st.Reason)
	}
}

// TestCheckFindsWhatWouldGoWrong: the mistakes a plugin author makes are found before the plugin is installed.
func TestCheckFindsWhatWouldGoWrong(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(ManifestFile, `{"id":"oops","api":1,"requries":["skills"],"requires":["no-such-plugin"],"config":{"type":"object","properties":{"n":{"type":"list"}}}}`)
	write("hooks.json", `{"SessionStart": [`)
	write("skills/nodesc/SKILL.md", "---\nname: nodesc\n---\nbody\n")
	write("commands/open.md", "---\ndescription: never closed\n")
	_, problems := Check(dir, []string{"skills"})
	var got []string
	for _, p := range problems {
		got = append(got, p.String())
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{`unknown field "requries"`, `requires "no-such-plugin"`, `setting "n" has type "list"`, "error: hooks:", "skills/nodesc/SKILL.md", "commands/open.md"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
}
