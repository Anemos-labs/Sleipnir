package taskgen

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
)

// hiddenFile is a test file (or fixture) of the mined commit.
type hiddenFile struct {
	Path string
	Kind Kind
	Lang string
	Data []byte
}

// plan is the setup and test command chosen for a task.
type plan struct {
	Lang  string
	Setup []string
	Cmd   string
}

// shq quotes s for a POSIX shell.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var goTestFuncRe = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\s*\(\s*\w+\s+\*testing\.T\s*\)`)
var goTopLevelRe = regexp.MustCompile(`(?m)^(?:func|type|var|const)\s`)

// goTestChunks maps each Test function of a Go source file to its text (from
// its func line to the next top-level declaration).
func goTestChunks(src string) map[string]string {
	starts := goTopLevelRe.FindAllStringIndex(src, -1)
	out := map[string]string{}
	for i, s := range starts {
		end := len(src)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		// Trailing blank lines belong to whatever follows, not to this function.
		chunk := strings.TrimRight(src[s[0]:end], " \t\r\n")
		if m := goTestFuncRe.FindStringSubmatch(chunk); m != nil {
			out[m[1]] = chunk
		}
	}
	return out
}

// changedGoTests returns the Test functions that are new or different in next
// compared with prev, sorted.
func changedGoTests(prev, next string) []string {
	old, cur := goTestChunks(prev), goTestChunks(next)
	var names []string
	for n, chunk := range cur {
		if o, ok := old[n]; !ok || o != chunk {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// nearestDirWith returns the closest directory at or above dir that has file at
// rev ("." for the root), or "", false.
func (r *repo) nearestDirWith(ctx context.Context, rev, dir, file string) (string, bool) {
	for {
		p := file
		if dir != "." {
			p = dir + "/" + file
		}
		if r.exists(ctx, rev, p) {
			return dir, true
		}
		if dir == "." || dir == "/" || dir == "" {
			return "", false
		}
		dir = path.Dir(dir)
	}
}

// planTests picks the setup and the test command for hidden test files. It
// restricts the run to those tests where the language makes that reliable, so
// the verifier neither takes long nor depends on unrelated tests.
func planTests(ctx context.Context, r *repo, parent, commit string, hidden []hiddenFile, deleted []string, override string, setupOverride []string, hasSetupOverride bool) (plan, error) {
	var testFiles []hiddenFile
	langs := map[string]int{}
	for _, h := range hidden {
		if h.Kind == Test {
			testFiles = append(testFiles, h)
			langs[h.Lang]++
		}
	}
	if len(testFiles) == 0 {
		return plan{}, fmt.Errorf("no test files")
	}
	lang := ""
	for l, n := range langs {
		if lang == "" || n > langs[lang] || (n == langs[lang] && l < lang) {
			lang = l
		}
	}
	if len(langs) > 1 {
		return plan{}, fmt.Errorf("tests in several languages (%s)", strings.Join(sortedKeys(langs), ", "))
	}

	var p plan
	var err error
	switch lang {
	case Go:
		p, err = planGo(ctx, r, parent, commit, testFiles)
	case Python:
		p, err = planPython(testFiles)
	case JS, TS:
		p, err = planJS(ctx, r, parent, testFiles)
	case Rust:
		p, err = planRust(ctx, r, parent, testFiles)
	case Java:
		p, err = planJava(ctx, r, parent, testFiles)
	default:
		err = fmt.Errorf("unsupported language %q", lang)
	}
	if err != nil && override == "" {
		return plan{}, err
	}
	if override != "" {
		p.Cmd = expandTemplate(override, testFiles, p)
		p.Lang = lang
	}
	if hasSetupOverride {
		p.Setup = setupOverride
	}
	// A test file the commit deleted would still be in the parent's tree and
	// could clash with the new tests (moved test functions are the classic case),
	// so the verifier removes it before running.
	if len(deleted) > 0 {
		sort.Strings(deleted)
		args := make([]string, len(deleted))
		for i, d := range deleted {
			args[i] = shq(d)
		}
		p.Cmd = "rm -f -- " + strings.Join(args, " ") + " && " + p.Cmd
	}
	p.Lang = lang
	return p, nil
}

// sortedKeys returns string map keys in lexical order.
func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// expandTemplate fills {files}, {dirs} and {tests} in a user-supplied command.
func expandTemplate(tmpl string, files []hiddenFile, p plan) string {
	var names, dirs []string
	seen := map[string]bool{}
	for _, f := range files {
		names = append(names, shq(f.Path))
		if d := path.Dir(f.Path); !seen[d] {
			seen[d] = true
			dirs = append(dirs, shq(d))
		}
	}
	tmpl = strings.ReplaceAll(tmpl, "{files}", strings.Join(names, " "))
	tmpl = strings.ReplaceAll(tmpl, "{dirs}", strings.Join(dirs, " "))
	return tmpl
}

func planGo(ctx context.Context, r *repo, parent, commit string, files []hiddenFile) (plan, error) {
	mod := ""
	seenDirs := map[string]bool{}
	var dirs []string
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		d := path.Dir(f.Path)
		m, ok := r.nearestDirWith(ctx, parent, d, "go.mod")
		if !ok {
			if _, ok2 := r.nearestDirWith(ctx, commit, d, "go.mod"); !ok2 {
				return plan{}, fmt.Errorf("no go.mod above %s", d)
			}
			// The module is created by the commit itself: run from its directory.
			m, _ = r.nearestDirWith(ctx, commit, d, "go.mod")
		}
		if mod == "" {
			mod = m
		} else if mod != m {
			return plan{}, fmt.Errorf("tests span several Go modules (%s and %s)", mod, m)
		}
		if !seenDirs[d] {
			seenDirs[d] = true
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	var pkgs []string
	for _, d := range dirs {
		rel := "."
		if mod == "." || mod == "" {
			if d != "." {
				rel = "./" + d
			}
		} else if d == mod {
			rel = "."
		} else {
			rel = "./" + strings.TrimPrefix(d, mod+"/")
		}
		pkgs = append(pkgs, rel)
	}
	// -run: the Test functions the commit added or changed.
	var names []string
	nameSeen := map[string]bool{}
	parsedAll := true
	for _, f := range files {
		if !strings.HasSuffix(f.Path, "_test.go") {
			continue
		}
		var prev string
		if b, err := r.show(ctx, parent, f.Path); err == nil {
			prev = string(b)
		}
		for _, n := range changedGoTests(prev, string(f.Data)) {
			if !nameSeen[n] {
				nameSeen[n] = true
				names = append(names, n)
			}
		}
	}
	if len(names) == 0 {
		parsedAll = false
	}
	sort.Strings(names)
	cmd := "go test -count=1"
	if parsedAll && len(names) <= 40 {
		cmd += " -run " + shq("^("+strings.Join(names, "|")+")$")
	}
	cmd += " " + strings.Join(pkgs, " ")
	setup := "go mod download"
	if mod != "." && mod != "" {
		cmd = "cd " + shq(mod) + " && " + cmd
		setup = "cd " + shq(mod) + " && " + setup
	}
	return plan{Setup: []string{setup}, Cmd: cmd}, nil
}

// planPython builds a sorted pytest command for Python files other than conftest.py, preferring
// python3 when available.
func planPython(files []hiddenFile) (plan, error) {
	py := "python3"
	if _, err := exec.LookPath("python3"); err != nil {
		py = "python"
	}
	var args []string
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".py") && path.Base(f.Path) != "conftest.py" {
			args = append(args, shq(f.Path))
		}
	}
	if len(args) == 0 {
		return plan{}, fmt.Errorf("no runnable Python test file (only conftest.py)")
	}
	sort.Strings(args)
	return plan{Cmd: py + " -m pytest -q -x -p no:cacheprovider " + strings.Join(args, " ")}, nil
}

// planJS requires repository test-script metadata and builds npm setup and test commands, using
// npm ci when a lockfile exists.
func planJS(ctx context.Context, r *repo, parent string, files []hiddenFile) (plan, error) {
	if !r.exists(ctx, parent, "package.json") {
		return plan{}, fmt.Errorf("no package.json at the repository root")
	}
	pkg, err := r.show(ctx, parent, "package.json")
	if err != nil || !regexp.MustCompile(`"test"\s*:`).Match(pkg) {
		return plan{}, fmt.Errorf(`package.json has no "test" script`)
	}
	var args []string
	for _, f := range files {
		args = append(args, shq(f.Path))
	}
	sort.Strings(args)
	setup := "npm install --no-audit --no-fund"
	if r.exists(ctx, parent, "package-lock.json") {
		setup = "npm ci --no-audit --no-fund"
	}
	return plan{Setup: []string{setup}, Cmd: "npm test --silent -- " + strings.Join(args, " ")}, nil
}

func planRust(ctx context.Context, r *repo, parent string, files []hiddenFile) (plan, error) {
	crate := ""
	var stems []string
	onlyIntegration := true
	for _, f := range files {
		d := path.Dir(f.Path)
		if path.Base(d) == "tests" && path.Ext(f.Path) == ".rs" {
			c := path.Dir(d)
			if crate == "" {
				crate = c
			} else if crate != c {
				return plan{}, fmt.Errorf("tests span several crates")
			}
			stems = append(stems, strings.TrimSuffix(path.Base(f.Path), ".rs"))
		} else {
			onlyIntegration = false
		}
	}
	if crate == "" {
		if c, ok := r.nearestDirWith(ctx, parent, path.Dir(files[0].Path), "Cargo.toml"); ok {
			crate = c
		} else {
			return plan{}, fmt.Errorf("no Cargo.toml above the tests")
		}
	}
	cmd := "cargo test --offline"
	if onlyIntegration {
		sort.Strings(stems)
		for _, s := range stems {
			cmd += " --test " + shq(s)
		}
	}
	setup := "cargo fetch"
	if crate != "." && crate != "" {
		cmd = "cd " + shq(crate) + " && " + cmd
		setup = "cd " + shq(crate) + " && " + setup
	}
	return plan{Setup: []string{setup}, Cmd: cmd}, nil
}

func planJava(ctx context.Context, r *repo, parent string, files []hiddenFile) (plan, error) {
	var classes []string
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".java") {
			classes = append(classes, strings.TrimSuffix(path.Base(f.Path), ".java"))
		}
	}
	sort.Strings(classes)
	switch {
	case r.exists(ctx, parent, "pom.xml"):
		return plan{Setup: []string{"mvn -q -B dependency:go-offline"},
			Cmd: "mvn -q -B -o test -Dtest=" + shq(strings.Join(classes, ",")) + " -DfailIfNoTests=false -Dsurefire.failIfNoSpecifiedTests=false"}, nil
	case r.exists(ctx, parent, "gradlew"):
		cmd := "./gradlew --offline test"
		for _, c := range classes {
			cmd += " --tests " + shq("*"+c)
		}
		return plan{Cmd: cmd}, nil
	}
	return plan{}, fmt.Errorf("no pom.xml or gradlew at the repository root")
}
