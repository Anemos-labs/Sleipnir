package plugin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// program writes a program named name into dir that prints "name 1.0" (and, given a marker, creates it) and returns its path.
func program(t *testing.T, dir, name, marker string) string {
	t.Helper()
	p, body := filepath.Join(dir, name), "#!/bin/sh\necho '"+name+" 1.0'\n"
	if marker != "" {
		body += ": > '" + marker + "'\n"
	}
	if runtime.GOOS == "windows" {
		p, body = p+".cmd", "@echo "+name+" 1.0\r\n"
		if marker != "" {
			body += "@type nul > \"" + marker + "\"\r\n"
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuiltinNeedsAreValid(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Builtin() {
		for _, n := range m.Needs {
			if err := validNeed(n); err != nil {
				t.Errorf("%s: %v", m.ID, err)
			}
			if seen[n.ID] {
				t.Errorf("need %s is declared twice", n.ID)
			}
			seen[n.ID] = true
			for _, im := range n.Install {
				for _, a := range im.Run {
					if strings.Contains(a, "@latest") {
						t.Errorf("%s/%s runs an unpinned %s", n.ID, im.ID, a)
					}
				}
			}
		}
	}
	for _, id := range []string{"blender", "blender-mcp", "rea", "rea-mcp", "gh"} {
		if !seen[id] {
			t.Errorf("no built-in plugin needs %s", id)
		}
	}
}

func TestExternalPluginDeclaresNeeds(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"id":"cad","needs":[{"id":"openscad","title":"OpenSCAD","commands":["openscad"],"install":[{"id":"brew","title":"Homebrew","run":["brew","install","openscad"]}]}]}`)
	m, err := LoadDir(dir)
	if err != nil || len(m.Needs) != 1 || m.Needs[0].Install[0].Run[2] != "openscad" {
		t.Fatalf("%+v, %v", m.Needs, err)
	}
	for _, bad := range []string{
		`{"id":"cad","needs":[{"id":"x","commands":["/bin/sh"]}]}`,
		`{"id":"cad","needs":[{"id":"x"}]}`,
		`{"id":"cad","needs":[{"id":"x","commands":["x"],"install":[{"id":"m"}]}]}`,
		`{"id":"cad","needs":[{"id":"x","commands":["x"],"install":[{"id":"m","connect":true}]}]}`,
	} {
		write(bad)
		if _, err := LoadDir(dir); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestCheckNeedsFindsDefaultLocationsAndPrepareLinksThem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the default location uses a unix path")
	}
	root := t.TempDir()
	program(t, filepath.Join(root, "opt", "tool-1.0"), "tool", "")
	newer := program(t, filepath.Join(root, "opt", "tool-2.0"), "tool", "")
	n := Need{ID: "tool", Title: "Tool", Commands: []string{"tool"}, Paths: map[string][]string{"unix": {root + "/opt/tool-*/tool"}}}
	state := t.TempDir()
	env := NeedEnv{State: state, PATH: t.TempDir()}
	st := CheckNeeds(env, []OwnedNeed{{Need: n}})[0]
	if !st.Found || st.Via != "default" || st.Path != newer {
		t.Fatalf("found %+v", st)
	}
	set, _ := Resolve([]Manifest{{ID: "p", Category: CatTools, Needs: []Need{n}}}, Input{})
	bin := PrepareNeeds(state, set)
	if bin != NeedBin(state) {
		t.Fatalf("bin %q", bin)
	}
	if b, err := os.ReadFile(filepath.Join(bin, "tool")); err != nil || !strings.Contains(string(b), newer) {
		t.Errorf("launcher: %q %v", b, err)
	}
	if st := CheckNeeds(env, []OwnedNeed{{Need: n}})[0]; st.Via != "default" || st.Path != newer {
		t.Errorf("the launcher is not taken for the program: %+v", st)
	}
	env.SkipDefaults = true
	if st := CheckNeeds(env, []OwnedNeed{{Need: n}})[0]; st.Found {
		t.Errorf("SkipDefaults still found %+v", st)
	}
	if _, err := LocateNeed(context.Background(), env, n, filepath.Join(bin, "tool")); err == nil {
		t.Error("the launcher itself was located")
	}
	// The program goes away: the next session start removes its launcher.
	if err := os.RemoveAll(filepath.Join(root, "opt")); err != nil {
		t.Fatal(err)
	}
	PrepareNeeds(state, set)
	if _, err := os.Stat(filepath.Join(bin, "tool")); !os.IsNotExist(err) {
		t.Errorf("a stale launcher stays: %v", err)
	}
}

func TestInstallNeedRunsTheDeclaredArgvAndChecksTheResult(t *testing.T) {
	bin, marker := t.TempDir(), filepath.Join(t.TempDir(), "ran")
	program(t, bin, "pm", marker)
	n := Need{ID: "tool", Title: "Tool", Commands: []string{"tool"}, Install: []InstallMethod{{ID: "pm", Title: "pm", Run: []string{"pm", "install", "tool"}}, {ID: "elsewhere", Title: "x", OS: []string{"plan9"}, Run: []string{"pm"}}}}
	env := NeedEnv{State: t.TempDir(), Home: t.TempDir(), PATH: bin, SkipDefaults: true}
	var out bytes.Buffer
	_, err := InstallNeed(context.Background(), env, n, "pm", &out)
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("the command did not run: %v\n%s", statErr, out.String())
	}
	if err == nil || !strings.Contains(err.Error(), "still not found") {
		t.Errorf("an install that leaves the program missing: %v", err)
	}
	if !strings.Contains(out.String(), "$ pm install tool") {
		t.Errorf("output %q", out.String())
	}
	if _, err := InstallNeed(context.Background(), env, n, "elsewhere", &out); err == nil {
		t.Error("a method for another operating system ran")
	}
	if _, err := InstallNeed(context.Background(), env, n, "nothing", &out); err == nil {
		t.Error("an undeclared method ran")
	}
	program(t, bin, "tool", "")
	if st, err := InstallNeed(context.Background(), env, n, "pm", &out); err != nil || !st.Found {
		t.Errorf("after the program appears: %+v %v", st, err)
	}
}

func TestLocateFollowsAnAppBundleAndChecksTheVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("application bundles are macOS's")
	}
	app := filepath.Join(t.TempDir(), "Blender.app")
	inner := program(t, filepath.Join(app, "Contents", "MacOS"), "Blender", "")
	env := NeedEnv{State: t.TempDir()}
	n := Need{ID: "blender", Title: "Blender", Commands: []string{"blender"}, VersionArgs: []string{"--version"}}
	st, err := LocateNeed(context.Background(), env, n, app)
	if err != nil || st.Path != inner || st.Version != "Blender 1.0" {
		t.Fatalf("%+v %v", st, err)
	}
	failing := filepath.Join(t.TempDir(), "nope")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LocateNeed(context.Background(), env, n, failing); err == nil {
		t.Error("a program whose version command fails was taken")
	}
}
