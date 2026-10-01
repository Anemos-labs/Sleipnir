package repocheck

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The Windows job tests every package except the ones scripts/windows-excluded.txt names. A package that is renamed or removed
// leaves an entry that excludes nothing, and a reason that nobody wrote is a package that nobody will port; the workflow has to
// keep using the list, or the list is a file about nothing.

type excludedPackage struct {
	path, reason string
	line         int
}

func windowsExcluded(t *testing.T) []excludedPackage {
	t.Helper()
	var out []excludedPackage
	for i, ln := range lines(read(t, "scripts/windows-excluded.txt")) {
		path, reason, _ := strings.Cut(ln, "#")
		if strings.TrimSpace(path) == "" {
			continue
		}
		out = append(out, excludedPackage{path: strings.TrimSpace(path), reason: strings.TrimSpace(reason), line: i + 1})
	}
	return out
}

func TestWindowsExclusionsNameRealPackagesWithReasons(t *testing.T) {
	dir := root(t)
	entries := windowsExcluded(t)
	if len(entries) == 0 {
		t.Fatal("scripts/windows-excluded.txt names no package: delete it, the job and scripts/windows-packages.sh, or list what is excluded")
	}
	for _, e := range entries {
		goFiles, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(e.path), "*.go"))
		if len(goFiles) == 0 {
			t.Errorf("scripts/windows-excluded.txt:%d names %q, which has no Go files: a renamed or removed package leaves an exclusion that excludes nothing", e.line, e.path)
		}
		if e.reason == "" {
			t.Errorf("scripts/windows-excluded.txt:%d: %q has no reason after a #; say what its tests assume that Windows does not give", e.line, e.path)
		}
		if strings.HasPrefix(e.path, "./") || strings.HasSuffix(e.path, "/") || strings.Contains(e.path, "...") {
			t.Errorf("scripts/windows-excluded.txt:%d: %q is not a module-relative package path (internal/perm, not ./internal/perm or a pattern)", e.line, e.path)
		}
	}
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.path
	}
	if !sort.StringsAreSorted(paths) {
		t.Error("scripts/windows-excluded.txt is not sorted: keep one line per package in order, so that a change to it is one line in a diff")
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Errorf("scripts/windows-excluded.txt names %q twice", p)
		}
		seen[p] = true
	}
}

func TestTheWindowsJobTestsWhatTheListLeavesIn(t *testing.T) {
	text := read(t, ".github/workflows/ci.yml")
	job := regexp.MustCompile(`(?s)\n  windows:\n.*?(?:\n  [a-z][a-z-]*:\n|$)`).FindString(text)
	if job == "" {
		t.Fatal(".github/workflows/ci.yml has no `windows` job: delete scripts/windows-excluded.txt and scripts/windows-packages.sh with it, or restore the job")
	}
	if !strings.Contains(job, "sh scripts/windows-packages.sh") || !strings.Contains(job, "go test") {
		t.Error("the windows job of .github/workflows/ci.yml does not run `go test` on what `sh scripts/windows-packages.sh` prints: the list of excluded packages is not what decides what it tests")
	}
	if !strings.Contains(job, `-gt 0`) {
		t.Error("the windows job does not check that the list of packages is not empty: a script that failed would leave `go test` with nothing to name, and the job would pass for it")
	}
	if _, err := os.Stat(filepath.Join(root(t), "scripts", "windows-packages.sh")); err != nil {
		t.Errorf("scripts/windows-packages.sh: %v", err)
	}
}
