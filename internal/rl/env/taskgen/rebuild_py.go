package taskgen

import (
	"bytes"
	"context"
	_ "embed" // the Python side of rebuild tasks
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// The Python side of rebuild tasks: a scope of packages, their modules' stubs,
// API and imports, and which module each test file exercises. The analysis is
// rebuild_py.py, run with python3: it parses the sources with the ast module
// and runs none of them.

//go:embed rebuild_py.py
var rebuildPyScript string

// pyModuleIn is a module of the scope as rebuild_py.py reads it.
type pyModuleIn struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Package bool   `json:"package"`
	Source  string `json:"source"`
}

// pyTestIn is a test file as rebuild_py.py reads it; Package is the dotted name of the package its directory is, for
// its relative imports.
type pyTestIn struct {
	Path    string `json:"path"`
	Package string `json:"package"`
	Source  string `json:"source"`
}

// pyAnswer is what rebuild_py.py found (see the script's documentation).
type pyAnswer struct {
	Modules map[string]pyModuleOut `json:"modules"`
	Tests   map[string]pyTestOut   `json:"tests"`
	Errors  []string               `json:"errors"`
}

// pyModuleOut is one module's stub, documentation, API and in-scope imports.
type pyModuleOut struct {
	Stub    string       `json:"stub"`
	Doc     string       `json:"doc"`
	API     []pyAPIEntry `json:"api"`
	Imports []string     `json:"imports"`
}

// pyAPIEntry is a declaration of a module's API: a def, class, var or import, its code (a signature, or the
// statement), its docstring and, for a class, its members. Tests marks a name documented because the tests use it.
type pyAPIEntry struct {
	Kind    string       `json:"kind"`
	Name    string       `json:"name"`
	Code    string       `json:"code"`
	Doc     string       `json:"doc"`
	Tests   bool         `json:"tests"`
	Members []pyAPIEntry `json:"members"`
}

// pyTestOut is how often a test file refers to each in-scope module, and the names it uses of each.
type pyTestOut struct {
	Refs  map[string]int      `json:"refs"`
	Names map[string][]string `json:"names"`
}

// runPyRebuild runs rebuild_py.py with python (isolated: no user site, no PYTHON* variables, nothing from the
// script's directory) on the modules and tests.
func runPyRebuild(ctx context.Context, python string, modules []pyModuleIn, tests []pyTestIn) (*pyAnswer, error) {
	dir, err := os.MkdirTemp("", "sleipnir-rebuild-py-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "rebuild_py.py")
	if err := os.WriteFile(script, []byte(rebuildPyScript), 0o600); err != nil {
		return nil, err
	}
	req, err := json.Marshal(map[string]any{"modules": modules, "tests": tests})
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, python, "-I", script)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(req)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s rebuild_py.py: %v: %s", python, err, tail(strings.TrimSpace(stderr.String()), 2000))
	}
	var a pyAnswer
	if err := json.Unmarshal(out.Bytes(), &a); err != nil {
		return nil, fmt.Errorf("%s rebuild_py.py: unreadable answer: %v", python, err)
	}
	return &a, nil
}

// pyNameRe matches a dotted Python package or module name.
var pyNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// pyIdentRe matches one Python identifier.
var pyIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// pyRoots are the directories, in order, a package of the scope is looked for in.
var pyRoots = []string{".", "src", "lib", "python"}

// pyScopePkg is a scope entry found in the tree: the package directory dir (or the single module file file) under
// the import root root.
type pyScopePkg struct {
	name, dir, file, root string
}

// pyModuleName is the module a .py file under the import root root is, and whether it is a package's __init__.py;
// "" when a part of its path is not an identifier.
func pyModuleName(root, file string) (string, bool) {
	rel := relDir(root, strings.TrimSuffix(file, ".py"))
	parts := strings.Split(rel, "/")
	pkg := false
	if parts[len(parts)-1] == "__init__" {
		parts, pkg = parts[:len(parts)-1], true
	}
	if len(parts) == 0 {
		return "", false
	}
	for _, p := range parts {
		if !pyIdentRe.MatchString(p) {
			return "", false
		}
	}
	return strings.Join(parts, "."), pkg
}

// rebuildPython resolves a Python scope at commit and prepares what the task needs. A scope entry is a package (or
// module) name, found under the repository root, src/, lib/ or python/; every entry must belong to one project (the
// nearest directory at or above them with pyproject.toml, setup.py or setup.cfg, else the repository root).
func rebuildPython(ctx context.Context, r *repo, commit string, files []string, opts RebuildOptions, gate func(int) error) (*rebuildLib, error) {
	isFile := map[string]bool{}
	for _, f := range files {
		isFile[f] = true
	}
	under := func(dir, f string) bool { return dir == "." || strings.HasPrefix(f, dir+"/") }
	hasPy := func(dir string) bool {
		for _, f := range files {
			if under(dir, f) && strings.HasSuffix(f, ".py") {
				return true
			}
		}
		return false
	}
	var scope []pyScopePkg
	for _, entry := range opts.Scope {
		name := strings.TrimSpace(entry)
		if !pyNameRe.MatchString(name) {
			return nil, reject(ReasonRebuildScope, "%q is not a Python package name", entry)
		}
		rel := strings.ReplaceAll(name, ".", "/")
		var hit *pyScopePkg
		for _, root := range pyRoots {
			if dir := path.Join(root, rel); isFile[path.Join(dir, "__init__.py")] {
				hit = &pyScopePkg{name: name, dir: dir, root: root}
				break
			}
		}
		for _, root := range pyRoots {
			if f := path.Join(root, rel) + ".py"; hit == nil && isFile[f] {
				hit = &pyScopePkg{name: name, file: f, root: root}
			}
		}
		for _, root := range pyRoots {
			if dir := path.Join(root, rel); hit == nil && hasPy(dir) {
				hit = &pyScopePkg{name: name, dir: dir, root: root} // a namespace package
			}
		}
		if hit == nil {
			return nil, reject(ReasonRebuildScope, "no Python package or module %q under the repository root, src/, lib/ or python/", entry)
		}
		scope = append(scope, *hit)
	}
	project := ""
	for _, s := range scope {
		p := "."
		for d := s.root; ; d = path.Dir(d) {
			if isFile[path.Join(d, "pyproject.toml")] || isFile[path.Join(d, "setup.py")] || isFile[path.Join(d, "setup.cfg")] {
				p = d
				break
			}
			if d == "." {
				break
			}
		}
		if project == "" {
			project = p
		} else if project != p {
			return nil, reject(ReasonRebuildScope, "the scope spans several Python projects (%s and %s)", dirLabel(project), dirLabel(p))
		}
	}

	// The scope's modules.
	inPkg := func(f string) (pyScopePkg, bool) {
		for _, s := range scope {
			if s.file == f || (s.file == "" && under(s.dir, f)) {
				return s, true
			}
		}
		return pyScopePkg{}, false
	}
	lib := &rebuildLib{lang: Python, root: project, sources: map[string][]byte{}, hidden: map[string][]byte{}}
	var modFiles, testFiles []string
	for _, f := range files {
		lang, kind := Classify(f)
		if lang != Python || !under(project, f) {
			continue
		}
		s, ok := inPkg(f)
		switch {
		case ok && kind == Source:
			if name, _ := pyModuleName(s.root, f); name != "" {
				modFiles = append(modFiles, f)
			}
		case kind == Test || kind == TestSupport:
			testFiles = append(testFiles, f)
		}
	}
	if len(modFiles) == 0 {
		return nil, reject(ReasonRebuildScope, "the scope has no Python source")
	}
	content, err := r.readFiles(ctx, commit, append(append([]string(nil), modFiles...), testFiles...))
	if err != nil {
		return nil, err
	}
	var mods []pyModuleIn
	byName := map[string]*rebuildPkg{}
	total := 0
	for _, f := range modFiles {
		s, _ := inPkg(f)
		name, pkg := pyModuleName(s.root, f)
		b := content[f]
		if !utf8.Valid(b) {
			return nil, reject(ReasonRebuildScope, "%s is not UTF-8", f)
		}
		lib.sources[f] = b
		p := &rebuildPkg{Name: name, Dir: path.Dir(f), Files: []string{f}, LOC: countLOC(b, "#")}
		lib.pkgs = append(lib.pkgs, p)
		byName[name] = p
		total += p.LOC
		mods = append(mods, pyModuleIn{Name: name, Path: f, Package: pkg, Source: string(b)})
	}
	if err := gate(total); err != nil {
		return nil, err
	}
	var tests []pyTestIn
	for _, f := range testFiles {
		b := content[f]
		if !utf8.Valid(b) {
			continue
		}
		ctxPkg := ""
		if s, ok := inPkg(f); ok {
			if name, _ := pyModuleName(s.root, path.Join(path.Dir(f), "__init__.py")); name != "" {
				ctxPkg = name
			}
		}
		tests = append(tests, pyTestIn{Path: f, Package: ctxPkg, Source: string(b)})
	}
	python := opts.Python
	if python == "" {
		python = "python3"
	}
	ans, err := runPyRebuild(ctx, python, mods, tests)
	if err != nil {
		return nil, err
	}
	if len(ans.Errors) > 0 {
		return nil, reject(ReasonRebuildScope, "%s", strings.Join(ans.Errors, "; "))
	}
	for _, m := range mods {
		out, ok := ans.Modules[m.Name]
		if !ok {
			return nil, fmt.Errorf("rebuild_py.py did not answer for %s", m.Path)
		}
		p := byName[m.Name]
		for _, imp := range out.Imports {
			if _, ok := byName[imp]; ok && imp != m.Name {
				p.Imports = append(p.Imports, imp)
			}
		}
	}
	lib.pkgs = dependencyOrder(lib.pkgs)
	order := map[string]int{}
	for i, p := range lib.pkgs {
		order[p.Name] = i
	}

	// The hidden suite: the test files inside the scope's packages and those that refer to its modules, and the other
	// test files inside the packages (data, helpers).
	var suite []string
	lib.testUnit = map[string]string{}
	lib.fallback = lib.pkgs[len(lib.pkgs)-1].Name
	if _, ok := byName[scope[0].name]; ok {
		lib.fallback = scope[0].name
	}
	hiddenDirs := map[string]bool{}
	for _, t := range tests {
		_, inside := inPkg(t.Path)
		refs := ans.Tests[t.Path].Refs
		if !inside && len(refs) == 0 {
			continue
		}
		lib.hidden[t.Path] = content[t.Path]
		if _, kind := Classify(t.Path); kind != Test || path.Base(t.Path) == "conftest.py" {
			continue
		}
		suite = append(suite, relDir(project, t.Path))
		hiddenDirs[path.Dir(t.Path)] = true
		lib.testUnit[t.Path] = pyTestUnit(t.Path, refs, order, lib.fallback)
	}
	for _, f := range files {
		if _, kind := Classify(f); kind == TestSupport && !strings.HasSuffix(f, ".py") {
			if _, ok := inPkg(f); ok {
				b, err := r.show(ctx, commit, f)
				if err != nil {
					return nil, err
				}
				lib.hidden[f] = b
			}
		}
	}
	if len(suite) == 0 {
		return nil, reject(ReasonRebuildScope, "no test file refers to the scope's modules")
	}
	sort.Strings(suite)

	switch opts.Tier {
	case RebuildStubs:
		lib.stubs = map[string][]byte{}
		for _, m := range mods {
			lib.stubs[m.Path] = []byte(ans.Modules[m.Name].Stub)
		}
	case RebuildSpec:
		var b strings.Builder
		b.WriteString(apiPreamble(Python, len(lib.pkgs)))
		for _, p := range lib.pkgs {
			b.WriteString(pyAPISection(p, ans.Modules[p.Name]))
		}
		lib.api = b.String()
	}

	cwd := ""
	if project != "." {
		cwd = project
	}
	lib.command = PythonSuiteCommand(cwd, suite...)
	if lib.pytestINI, err = pytestINIAt(ctx, r, commit, project); err != nil {
		return nil, reject(ReasonRebuildScope, "the reference's pytest configuration: %v", err)
	}
	// Test files are protected in the scope's packages, at any depth, and next to the hidden tests.
	for _, s := range scope {
		top, _, _ := strings.Cut(s.name, ".")
		lib.tops = append(lib.tops, top)
		if s.dir != "" {
			lib.protect = append(lib.protect, dirGlob(s.dir, "**/test_*.py"), dirGlob(s.dir, "**/*_test.py"))
		}
	}
	lib.tops = dedupe(lib.tops)
	dirs := make([]string, 0, len(hiddenDirs))
	for d := range hiddenDirs {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		lib.protect = append(lib.protect, dirGlob(d, "test_*.py"), dirGlob(d, "*_test.py"))
	}
	lib.protect = dedupe(lib.protect)
	lib.manifests = []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements*.txt", "Pipfile", "Pipfile.lock",
		"poetry.lock", "uv.lock", "tox.ini", "MANIFEST.in"}
	return lib, nil
}

// pyTestUnit chooses the module a test file exercises among those it refers to: the one its name is after
// (test_core.py, core_test.py: a module named core), else the one it refers to most, the later in dependency order
// on a tie; fallback when it refers to none.
func pyTestUnit(file string, refs map[string]int, order map[string]int, fallback string) string {
	stem := strings.TrimSuffix(path.Base(file), ".py")
	stem = strings.TrimSuffix(strings.TrimPrefix(stem, "test_"), "_test")
	better := func(a, b string) bool { // a over b
		if refs[a] != refs[b] {
			return refs[a] > refs[b]
		}
		return order[a] > order[b]
	}
	named, best := "", ""
	for m := range refs {
		if _, ok := order[m]; !ok {
			continue
		}
		last := m[strings.LastIndex(m, ".")+1:]
		if last == stem && (named == "" || better(m, named)) {
			named = m
		}
		if best == "" || better(m, best) {
			best = m
		}
	}
	switch {
	case named != "":
		return named
	case best != "":
		return best
	}
	return fallback
}

// pytestINIAt is the reference's pytest configuration of the project in dir (see PytestINIFromDir), read from rev.
func pytestINIAt(ctx context.Context, r *repo, rev, dir string) (string, error) {
	return pytestINI(func(name string) (string, bool, error) {
		b, err := r.show(ctx, rev, path.Join(dir, name))
		if err != nil {
			return "", false, nil // absent at rev
		}
		return string(b), true, nil
	})
}

// pyAPISection documents one module for API.md: its name and file, its docstring, and its API as Python skeletons
// (signatures with their docstrings), then what of the rest its tests use.
func pyAPISection(p *rebuildPkg, m pyModuleOut) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Module `%s`\n\nFile `%s`.\n\n", p.Name, p.Files[0])
	if d := strings.TrimSpace(m.Doc); d != "" {
		b.WriteString(d + "\n\n")
	}
	var public, tested []pyAPIEntry
	for _, e := range m.API {
		if e.Tests {
			tested = append(tested, e)
		} else {
			public = append(public, e)
		}
	}
	block := func(es []pyAPIEntry) {
		for _, e := range es {
			b.WriteString("```python\n" + pySkeleton(e, "") + "\n```\n\n")
		}
	}
	block(public)
	if len(tested) > 0 {
		b.WriteString("### Not public; the module's tests use it\n\n")
		block(tested)
	}
	return b.String()
}

// pySkeleton renders an API entry as Python: a def or class line with its docstring (or "..."), a class's members
// indented under it.
func pySkeleton(e pyAPIEntry, indent string) string {
	ind := func(s, by string) string {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			if l != "" {
				lines[i] = by + l
			}
		}
		return strings.Join(lines, "\n")
	}
	switch e.Kind {
	case "def", "class":
		var b strings.Builder
		b.WriteString(ind(e.Code, indent) + ":")
		inner := indent + "    "
		doc := strings.TrimSpace(e.Doc)
		switch {
		case doc != "":
			b.WriteString("\n" + inner + `"""` + strings.ReplaceAll(doc, "\n", "\n"+inner) + `"""`)
		case len(e.Members) == 0:
			b.WriteString(" ...")
		}
		for _, m := range e.Members {
			b.WriteString("\n\n" + pySkeleton(m, inner))
		}
		return b.String()
	}
	return ind(e.Code, indent)
}

// readFiles reads the content of paths at rev with one git process. A path rev does not have is an error.
func (r *repo) readFiles(ctx context.Context, rev string, paths []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(paths))
	if len(paths) == 0 {
		return out, nil
	}
	var in bytes.Buffer
	for _, p := range paths {
		if strings.ContainsAny(p, "\n\x00") {
			return nil, fmt.Errorf("cannot read %q", p)
		}
		in.WriteString(rev + ":" + p + "\n")
	}
	raw, err := r.gitIn(ctx, in.Bytes(), nil, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		nl := bytes.IndexByte(raw, '\n')
		if nl < 0 {
			return nil, errors.New("git cat-file: short output")
		}
		head := strings.Fields(string(raw[:nl]))
		raw = raw[nl+1:]
		if len(head) != 3 || head[1] != "blob" {
			return nil, fmt.Errorf("git cat-file: %s at %s: %s", p, rev, strings.Join(head, " "))
		}
		var size int
		if _, err := fmt.Sscan(head[2], &size); err != nil || size > len(raw) {
			return nil, errors.New("git cat-file: bad object size")
		}
		out[p] = raw[:size:size]
		raw = raw[size:]
		if len(raw) > 0 && raw[0] == '\n' {
			raw = raw[1:]
		}
	}
	return out, nil
}
