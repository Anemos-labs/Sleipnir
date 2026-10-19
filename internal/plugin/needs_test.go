package plugin

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestPrepareNeedsReturnsTheBinBeforeAnythingIsLocated(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	set, _ := Resolve([]Manifest{{ID: "p", Category: CatTools}}, Input{})
	if bin := PrepareNeeds(state, set); bin != NeedBin(state) {
		t.Fatalf("bin %q: a program located after the session starts must still be on its PATH", bin)
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

func TestInstallNeedRunsSetupFirstAndStopsAtAFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake programs are shell scripts")
	}
	bin, setupRan, pmRan := t.TempDir(), filepath.Join(t.TempDir(), "setup"), filepath.Join(t.TempDir(), "pm")
	program(t, bin, "remote", setupRan)
	program(t, bin, "pm", pmRan)
	if err := os.WriteFile(filepath.Join(bin, "broken"), []byte("#!/bin/sh\necho no remote\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	n := Need{ID: "tool", Title: "Tool", Commands: []string{"tool"}, Install: []InstallMethod{
		{ID: "pm", Title: "pm", Setup: [][]string{{"remote", "add", "--if-not-exists", "src"}}, Run: []string{"pm", "install", "tool"}},
		{ID: "bad", Title: "bad", Setup: [][]string{{"broken"}}, Run: []string{"pm", "install", "tool"}},
		{ID: "gone", Title: "gone", Setup: [][]string{{"absent"}}, Run: []string{"pm", "install", "tool"}},
	}}
	if err := validNeed(n); err != nil {
		t.Fatal(err)
	}
	env := NeedEnv{State: t.TempDir(), Home: t.TempDir(), PATH: bin, SkipDefaults: true}
	st := CheckNeeds(env, []OwnedNeed{{Need: n}})[0]
	if len(st.Methods) != 3 || len(st.Methods[0].Setup) != 1 || st.Methods[0].Setup[0][0] != "remote" {
		t.Fatalf("the setup commands are not reported: %+v", st.Methods)
	}
	if st.Methods[2].Available || !strings.Contains(st.Methods[2].Reason, "absent") {
		t.Errorf("a method whose setup program is missing: %+v", st.Methods[2])
	}
	var out bytes.Buffer
	_, _ = InstallNeed(context.Background(), env, n, "pm", &out)
	if i, j := strings.Index(out.String(), "$ remote add"), strings.Index(out.String(), "$ pm install"); i < 0 || j < i {
		t.Errorf("setup must run before the install: %q", out.String())
	}
	for _, m := range []string{setupRan, pmRan} {
		if _, err := os.Stat(m); err != nil {
			t.Errorf("%s did not run", filepath.Base(m))
		}
	}
	_ = os.Remove(pmRan)
	out.Reset()
	if _, err := InstallNeed(context.Background(), env, n, "bad", &out); err == nil || !strings.Contains(err.Error(), "broken failed") {
		t.Errorf("a failed setup: %v", err)
	}
	if _, err := os.Stat(pmRan); err == nil {
		t.Error("the install ran after its setup failed")
	}
	if err := validNeed(Need{ID: "x", Commands: []string{"x"}, Install: []InstallMethod{{ID: "m", Setup: [][]string{{}}, Run: []string{"pm"}}}}); err == nil {
		t.Error("an empty setup command was accepted")
	}
}

func TestInstallNeedWorksOutTheMethodsEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake programs are shell scripts")
	}
	bin, seen := t.TempDir(), filepath.Join(t.TempDir(), "seen")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("probe", "echo banner\necho SLEIPNIR_TEST_DIR=/first\necho SLEIPNIR_TEST_DIR=/addons\necho done\n")
	write("silent", "echo nothing here\n")
	write("leaky", "echo SLEIPNIR_TEST_DIR=hunter2\nexit 3\n")
	write("pm", "echo \"$SLEIPNIR_TEST_DIR\" > '"+seen+"'\n")
	n := Need{ID: "tool", Title: "Tool", Commands: []string{"tool"}, Install: []InstallMethod{
		{ID: "pm", Title: "pm", Env: map[string][]string{"SLEIPNIR_TEST_DIR": {"probe", "--where"}}, Run: []string{"pm"}},
		{ID: "quiet", Title: "quiet", Env: map[string][]string{"SLEIPNIR_TEST_DIR": {"silent"}}, Run: []string{"pm"}},
		{ID: "leaky", Title: "leaky", Env: map[string][]string{"SLEIPNIR_TEST_DIR": {"leaky"}}, Run: []string{"pm"}},
	}}
	if err := validNeed(n); err != nil {
		t.Fatal(err)
	}
	env := NeedEnv{State: t.TempDir(), Home: t.TempDir(), PATH: bin, SkipDefaults: true}
	if st := CheckNeeds(env, []OwnedNeed{{Need: n}})[0]; st.Methods[0].Env["SLEIPNIR_TEST_DIR"][0] != "probe" {
		t.Fatalf("the environment's commands are not reported: %+v", st.Methods[0])
	}
	var out bytes.Buffer
	_, _ = InstallNeed(context.Background(), env, n, "pm", &out)
	if b, err := os.ReadFile(seen); err != nil || strings.TrimSpace(string(b)) != "/addons" {
		t.Errorf("the command got %q (%v), not the last NAME=value line\n%s", b, err, out.String())
	}
	if o := out.String(); strings.Contains(o, "/addons") || strings.Contains(o, "banner") || !strings.Contains(o, "set SLEIPNIR_TEST_DIR") {
		t.Errorf("a worked-out value or its command's output reached the install's output: %q", o)
	}
	if _, err := InstallNeed(context.Background(), env, n, "quiet", &out); err == nil || !strings.Contains(err.Error(), "printed no SLEIPNIR_TEST_DIR= line") {
		t.Errorf("a command that prints no value: %v", err)
	}
	out.Reset()
	if _, err := InstallNeed(context.Background(), env, n, "leaky", &out); err == nil || strings.Contains(err.Error()+out.String(), "hunter2") {
		t.Errorf("a failed value command's output reached the install's output: %v %q", err, out.String())
	}
	t.Setenv("SLEIPNIR_TEST_DIR", "")
	_, _ = InstallNeed(context.Background(), env, n, "quiet", &out)
	if b, _ := os.ReadFile(seen); strings.TrimSpace(string(b)) != "" {
		t.Errorf("a variable set to empty was replaced: %q", b)
	}
	noProbe := NeedEnv{State: t.TempDir(), Home: t.TempDir(), PATH: t.TempDir(), SkipDefaults: true}
	program(t, noProbe.PATH, "pm", "")
	if st := CheckNeeds(noProbe, []OwnedNeed{{Need: n}})[0]; !st.Methods[0].Available {
		t.Errorf("a method whose variable is already set needs its command: %+v", st.Methods[0])
	}
	t.Setenv("SLEIPNIR_TEST_DIR", "/mine")
	_, _ = InstallNeed(context.Background(), env, n, "quiet", &out)
	if b, _ := os.ReadFile(seen); strings.TrimSpace(string(b)) != "/mine" {
		t.Errorf("a variable already set was replaced: %q", b)
	}
	if err := validNeed(Need{ID: "x", Commands: []string{"x"}, Install: []InstallMethod{{ID: "m", Env: map[string][]string{"A B": {"probe"}}, Run: []string{"pm"}}}}); err == nil {
		t.Error("a bad variable name was accepted")
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
	if st := CheckNeeds(env, []OwnedNeed{{Need: n}})[0]; st.Path != inner || st.Version != "Blender 1.0" {
		t.Errorf("a refused file replaced the earlier record: %+v", st)
	}
}

func TestCappedMarksOnlyOutputPastItsLimit(t *testing.T) {
	exact := &capped{max: 4}
	_, _ = exact.Write([]byte("ab"))
	_, _ = exact.Write([]byte("cd"))
	if exact.over || exact.buf.String() != "abcd" {
		t.Errorf("output exactly at the limit: over=%v %q", exact.over, exact.buf.String())
	}
	_, _ = exact.Write([]byte("e"))
	if !exact.over || exact.buf.String() != "abcd" {
		t.Errorf("output past the limit: over=%v %q", exact.over, exact.buf.String())
	}
}

func TestInstallStepDoesNotWaitForABackgroundChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake program is a shell script")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep program")
	}
	bin := t.TempDir() // the step's PATH: the script names sleep by its full path
	if err := os.WriteFile(filepath.Join(bin, "spawner"), []byte("#!/bin/sh\n'"+sleep+"' 60 &\necho started\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var out bytes.Buffer
	err = runStep(context.Background(), NeedEnv{PATH: bin}, []string{"spawner"}, nil, &out)
	if d := time.Since(start); d < stepWaitDelay/2 || d > stepWaitDelay+10*time.Second {
		t.Errorf("the step waited %v for a background child holding its output", d)
	}
	if err != nil || !strings.Contains(out.String(), "started") {
		t.Errorf("a command that exited 0 with a child still running: %v %q", err, out.String())
	}
}
