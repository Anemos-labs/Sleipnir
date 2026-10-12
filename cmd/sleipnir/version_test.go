package main

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// A build given no version with -ldflags names itself from its build info: the module version a tagged install stamps, else the short
// revision (with +dirty), and "dev" when there is neither.
func TestResolveVersion(t *testing.T) {
	rev := "3f2a9c1d0e4b5a6978877665544332211"
	vcs := []debug.BuildSetting{{Key: "vcs.revision", Value: rev}, {Key: "vcs.modified", Value: "true"}}
	for _, tc := range []struct {
		name      string
		ldv, ldc  string
		bi        *debug.BuildInfo
		wantV, wC string
	}{
		{"ldflags win", "0.2.28", "abc", &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}}, "0.2.28", "abc"},
		{"no build info", "dev", "none", nil, "dev", "none"},
		{"go install @tag", "dev", "none", &debug.BuildInfo{Main: debug.Module{Version: "v0.2.28"}}, "v0.2.28", "none"},
		{"checkout without a module version", "dev", "none", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: vcs}, "3f2a9c1d0e4b+dirty", "3f2a9c1d0e4b"},
		{"clean checkout", "dev", "none", &debug.BuildInfo{Settings: vcs[:1]}, "3f2a9c1d0e4b", "3f2a9c1d0e4b"},
		{"test binary", "dev", "none", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev", "none"},
	} {
		v, c := resolveVersion(tc.ldv, tc.ldc, tc.bi)
		if v != tc.wantV || c != tc.wC {
			t.Errorf("%s: %q %q, want %q %q", tc.name, v, c, tc.wantV, tc.wC)
		}
	}
}

// The top bar's label is short; only a release compares itself with the releases.
func TestShortVersionAndReleaseBuild(t *testing.T) {
	for in, want := range map[string]string{"0.2.28": "v0.2.28", "v0.2.28": "v0.2.28", "v0.2.29-0.20261012101010-3f2a9c1d0e4b+dirty": "v0.2.29-dev", "dev": "dev", "3f2a9c1d0e4b+dirty": "3f2a9c1d0e4b+dirty"} {
		if got := shortVersion(in); got != want {
			t.Errorf("shortVersion(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]bool{"0.2.28": true, "v1.0.0": true, "dev": false, "v0.2.29-0.2026-abc": false, "abc+dirty": false} {
		if releaseBuild(in) != want {
			t.Errorf("releaseBuild(%q) != %v", in, want)
		}
	}
	t.Setenv("SLEIPNIR_NO_UPDATE_CHECK", "")
	old := version
	defer func() { version = old }()
	version = "v0.2.29-0.20261012101010-3f2a9c1d0e4b+dirty"
	if releaseVersion() != "dev" || updateChecks() {
		t.Errorf("a pseudo-version is a build from source")
	}
	version = "v0.2.28"
	if releaseVersion() != "v0.2.28" {
		t.Errorf("a tagged install is a release")
	}
	if d := buildDescription(); !strings.HasPrefix(d, "v0.2.28 (") || !strings.Contains(d, "go1.") {
		t.Errorf("buildDescription = %q", d)
	}
}

// Another installed copy that reports a later release is named, with where it is; an older one, the running one and unreadable files
// are not.
func TestNewerInstalledCopyIsNamed(t *testing.T) {
	root := t.TempDir()
	mk := func(dir string) string {
		d := filepath.Join(root, dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(d, "sleipnir")
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	self, old, gobin, junk := mk("local/bin"), mk("old/bin"), mk("go/bin"), mk("junk/bin")
	versions := map[string]string{old: "v0.2.5", gobin: "v0.2.28", junk: ""}
	read := func(p string) string { return versions[p] }
	pathEnv := strings.Join([]string{filepath.Dir(self), filepath.Dir(old), filepath.Dir(junk), "relative/bin"}, string(os.PathListSeparator))
	dirs, onPath := installDirs(pathEnv, "", "", root)
	if len(dirs) != 4 || !onPath[filepath.Dir(old)] || onPath[filepath.Dir(gobin)] {
		t.Fatalf("dirs %v onPath %v", dirs, onPath)
	}
	b, ok := newerInstalled("v0.2.10", self, dirs, onPath, read)
	if !ok || b.Path != gobin || b.Version != "v0.2.28" || b.OnPath {
		t.Fatalf("newer = %+v %v", b, ok)
	}
	want := "you are running v0.2.10 from ~/.local/bin/sleipnir; v0.2.28 is at ~/go/bin/sleipnir (not on PATH)"
	if got := staleText("v0.2.10", "~/.local/bin/sleipnir", b.Version, "~/go/bin/sleipnir", b.OnPath); got != want {
		t.Errorf("staleText = %q", got)
	}
	if _, ok := newerInstalled("v0.2.28", self, dirs, onPath, read); ok {
		t.Error("nothing is newer than the newest")
	}
	if _, ok := newerInstalled("dev", self, dirs, onPath, read); ok {
		t.Error("a build that names no release is never told of a newer one")
	}
	if b, ok := newerInstalled("v0.2.10", gobin, dirs, onPath, read); ok {
		t.Errorf("the running copy is not another one: %+v", b)
	}
	if binaryVersion(junk) != "" {
		t.Error("a file that is not a Go binary names no version")
	}
	if exe, err := os.Executable(); err == nil {
		_ = binaryVersion(exe) // a real Go binary is read without failing
	}
}
