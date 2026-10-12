package main

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/update"
)

// init resolves the version of a build that was not given one with -ldflags: `go install ...@v0.2.28` says v0.2.28, a build from a
// checkout says what the Go toolchain stamped (a pseudo-version, or the short revision with +dirty for uncommitted changes).
func init() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		bi = nil
	}
	version, commit = resolveVersion(version, commit, bi)
}

// resolveVersion is the version and commit of a build: those given with -ldflags when they were (version not "dev"), else what the
// build info says. The module version wins when the toolchain stamped one (a tagged install or a pseudo-version); without one the short
// VCS revision stands in, with +dirty when the checkout had uncommitted changes. With neither (a test binary) the build stays "dev".
func resolveVersion(ldVersion, ldCommit string, bi *debug.BuildInfo) (v, c string) {
	v, c = ldVersion, ldCommit
	if bi == nil {
		return v, c
	}
	rev, dirty := vcsRevision(bi)
	if c == "none" && rev != "" {
		c = shortRevision(rev)
	}
	if v != "dev" {
		return v, c
	}
	if mv := bi.Main.Version; mv != "" && mv != "(devel)" {
		return mv, c
	}
	if rev != "" {
		v = shortRevision(rev)
		if dirty {
			v += "+dirty"
		}
	}
	return v, c
}

// vcsRevision is the build's VCS revision and whether its checkout was modified.
func vcsRevision(bi *debug.BuildInfo) (rev string, dirty bool) {
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	return rev, dirty
}

// shortRevision is the first twelve characters of a revision.
func shortRevision(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// releaseBuild reports whether v names a release (1.2.3 or v1.2.3, nothing after it): a build that can compare itself with the
// published releases and replace itself with one.
func releaseBuild(v string) bool {
	return releaseRE.MatchString(v)
}

var releaseRE = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// releaseVersion is version for the updater: the version itself for a release, "dev" for a build from source (which never compares
// itself with the releases nor replaces itself with one).
func releaseVersion() string {
	if releaseBuild(version) {
		return version
	}
	return "dev"
}

// shortVersion is the version as a top bar shows it: v1.2.3 for a release, the core of a pseudo-version with -dev ("v0.2.29-dev"),
// and the version itself for anything else (a revision, "dev").
func shortVersion(v string) string {
	if releaseBuild(v) {
		return "v" + strings.TrimPrefix(v, "v")
	}
	if m := pseudoRE.FindStringSubmatch(v); m != nil {
		return "v" + m[1] + "-dev"
	}
	return v
}

var pseudoRE = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)[-+]`)

// buildDescription is the full description of the build: version, commit, toolchain and platform ("v0.2.28 (3f2a9c1d0e4b, go1.25.1,
// linux/amd64)").
func buildDescription() string {
	parts := []string{}
	if commit != "" && commit != "none" && !strings.Contains(version, commit) {
		parts = append(parts, commit)
	}
	parts = append(parts, runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH)
	return version + " (" + strings.Join(parts, ", ") + ")"
}

// ---- other installed copies -----------------------------------------------------------------------------------------------------

// staleBinary is another installed copy of the program that is newer than the one running.
type staleBinary struct {
	Path    string // where it is
	Version string // the version it reports
	OnPath  bool   // whether a directory of $PATH holds it (after the running one, or it would be the one running)
}

// ldVersionRE finds the version given with -ldflags in a binary's build settings.
var ldVersionRE = regexp.MustCompile(`main\.version=([^\s'"]+)`)

// binaryVersion is the version a sleipnir binary reports, read from its build info without running it: the version given with
// -ldflags, else the module version the toolchain stamped. "" when the file is not a Go binary or names none.
func binaryVersion(path string) string {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == "-ldflags" {
			if m := ldVersionRE.FindStringSubmatch(s.Value); m != nil {
				return m[1]
			}
		}
	}
	if mv := bi.Main.Version; mv != "" && mv != "(devel)" {
		return mv
	}
	return ""
}

// installDirs are the directories where another copy of the program may be installed: each of $PATH (onPath true), then $GOBIN,
// $GOPATH/bin and ~/go/bin, each named once.
func installDirs(pathEnv, gobin, gopath, home string) (dirs []string, onPath map[string]bool) {
	onPath = map[string]bool{}
	seen := map[string]bool{}
	add := func(d string, path bool) {
		if d == "" || !filepath.IsAbs(d) {
			return
		}
		d = filepath.Clean(d)
		if path {
			onPath[d] = true
		}
		if seen[d] {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	for _, d := range filepath.SplitList(pathEnv) {
		add(d, true)
	}
	add(gobin, false)
	for _, p := range filepath.SplitList(gopath) {
		if p != "" {
			add(filepath.Join(p, "bin"), false)
		}
	}
	if home != "" {
		add(filepath.Join(home, "go", "bin"), false)
	}
	return dirs, onPath
}

// newerInstalled is the newest copy of the program in dirs that reports a later release than running, other than self (the running
// executable, symlinks resolved). Nothing is run and nothing leaves the machine; a file that cannot be read is passed over.
func newerInstalled(running, self string, dirs []string, onPath map[string]bool, read func(string) string) (staleBinary, bool) {
	if !releaseBuild(running) && pseudoRE.FindStringSubmatch(running) == nil {
		return staleBinary{}, false // a build that names no release cannot say what is newer than it
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r // compared with each copy's resolved path: macOS's /var is /private/var
	}
	var best staleBinary
	name := "sleipnir"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		r, err := filepath.EvalSymlinks(p)
		if err != nil || r == self {
			continue
		}
		v := read(p) // the path as found: the resolved one may name the same file differently (macOS's /var is /private/var)
		if v == "" || !update.Newer(running, v) || best.Version != "" && !update.Newer(best.Version, v) {
			continue
		}
		best = staleBinary{Path: p, Version: v, OnPath: onPath[d]}
	}
	return best, best.Version != ""
}

// staleNotice is the line a person reads at the start of `sleipnir web` and `sleipnir chat` when another installed copy of the program
// is newer than the running one, or "" when none is. It reads only local files and says nothing when anything fails.
func staleNotice() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	gopath := os.Getenv("GOPATH")
	dirs, onPath := installDirs(os.Getenv("PATH"), os.Getenv("GOBIN"), gopath, userHome())
	b, ok := newerInstalled(version, self, dirs, onPath, binaryVersion)
	if !ok {
		return ""
	}
	return staleText(version, tildePath(self), b.Version, tildePath(b.Path), b.OnPath)
}

// staleText is the sentence of staleNotice.
func staleText(running, self, newer, at string, onPath bool) string {
	where := "not on PATH"
	if onPath {
		where = "later on PATH"
	}
	return fmt.Sprintf("you are running %s from %s; %s is at %s (%s)", running, self, newer, at, where)
}
