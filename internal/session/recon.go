package session

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/kv"
)

// ReconOptions controls the deterministic project survey that seeds the shared
// pin. The survey exists because of the pin-density rule (docs/CACHE-DESIGN.md
// section 8.1): the shared layer is read on every request of every agent, so it
// must be worth more than the exploration it replaces. It is therefore a small,
// ranked map, not an inventory: the layout, how to build and test, and the
// signatures of the files that matter most.
type ReconOptions struct {
	Root string
	// BudgetTokens bounds the whole survey (default 5000).
	BudgetTokens int
	Est          core.Estimator
	// MaxFiles caps how many files are considered (default 30000).
	MaxFiles int
	// NoGit skips git (file listing and churn ranking).
	NoGit bool
}

// Recon is the survey result.
type Recon struct {
	// Segments are ready to be the project part of the shared layer.
	Segments []kv.Segment
	Tokens   int
	Files    int
	// Languages lists source files per language, most common first.
	Languages []LangCount
	// Commands are the detected build/test/lint commands.
	Commands []string
}

// LangCount is a language and its file count.
type LangCount struct {
	Name  string
	Files int
}

// BuildRecon surveys root. It is a pure function of the tracked file tree and
// (for ranking only) recent git history, so unchanged projects give
// byte-identical text and never cause a shared-layer epoch.
func BuildRecon(ctx context.Context, o ReconOptions) (*Recon, error) {
	if o.Root == "" {
		return nil, fmt.Errorf("recon: root is required")
	}
	if o.BudgetTokens <= 0 {
		o.BudgetTokens = 5000
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = 30000
	}
	if o.Est == nil {
		o.Est = core.NewBytesEstimator()
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	files, err := listFiles(ctx, root, o.MaxFiles, !o.NoGit)
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	r := &Recon{Files: len(files)}
	langs := map[string]int{}
	for _, f := range files {
		if l := languageOf(f); l != "" && !isNoise(f) {
			langs[l]++
		}
	}
	for l, n := range langs {
		r.Languages = append(r.Languages, LangCount{l, n})
	}
	sort.Slice(r.Languages, func(i, j int) bool {
		if r.Languages[i].Files != r.Languages[j].Files {
			return r.Languages[i].Files > r.Languages[j].Files
		}
		return r.Languages[i].Name < r.Languages[j].Name
	})

	manifest, cmds := detectManifests(root, files)
	r.Commands = cmds

	var head strings.Builder
	head.WriteString(projectLine(root, r))
	head.WriteString("\n")
	head.WriteString(layoutText(files))
	if manifest != "" {
		head.WriteString("\n")
		head.WriteString(manifest)
	}
	if len(cmds) > 0 {
		head.WriteString("\nDetected commands (verify before relying on them):\n")
		for _, c := range cmds {
			head.WriteString("  " + c + "\n")
		}
	}
	// The layout gets at most 40% of the budget; the symbol map takes the rest.
	layoutBudget := o.BudgetTokens * 2 / 5
	layout := fitTokens(head.String(), layoutBudget, o.Est)
	r.Segments = append(r.Segments, kv.Segment{Key: "project", Text: layout, Vol: kv.VolEpoch})

	left := o.BudgetTokens - o.Est.Tokens(layout)
	if left > 200 {
		if m := symbolMap(ctx, root, files, left, o.Est, !o.NoGit); m != "" {
			r.Segments = append(r.Segments, kv.Segment{Key: "code map", Text: m, Vol: kv.VolEpoch})
		}
	}
	for _, s := range r.Segments {
		r.Tokens += o.Est.Tokens(s.Text)
	}
	return r, nil
}

func projectLine(root string, r *Recon) string {
	var parts []string
	for i, l := range r.Languages {
		if i == 4 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d", l.Name, l.Files))
	}
	return fmt.Sprintf("%s: %d files (%s)", filepath.Base(root), r.Files, strings.Join(parts, ", "))
}

// ---- file listing ----------------------------------------------------------------

var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "__pycache__": true, ".venv": true, "venv": true, ".tox": true, ".idea": true, ".vscode": true,
	".next": true, ".cache": true, "coverage": true, ".gradle": true, "bin": true, "obj": true, ".sleipnir": true,
	"testdata": true,
}

func listFiles(ctx context.Context, root string, max int, useGit bool) ([]string, error) {
	if useGit {
		if fs, ok := gitFiles(ctx, root, max); ok {
			return fs, nil
		}
	}
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are simply absent from the survey
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (skipDirs[name] || strings.HasPrefix(name, ".") && name != ".github") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // no symlinks: a survey must not leave the tree
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		if len(out) >= max {
			return fs.SkipAll
		}
		return nil
	})
	return out, err
}

func gitFiles(ctx context.Context, root string, max int) ([]string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	b, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var out []string
	for _, f := range bytes.Split(b, []byte{0}) {
		if len(f) == 0 {
			continue
		}
		p := string(f)
		st, err := os.Lstat(filepath.Join(root, p))
		if err != nil || !st.Mode().IsRegular() {
			continue // deleted in the worktree, or a symlink/submodule
		}
		if skipped(p) {
			continue
		}
		out = append(out, p)
		if len(out) >= max {
			break
		}
	}
	return out, true
}

// skipped drops files inside dependency and build directories even when git
// tracks them (vendored code is not this project's structure).
func skipped(p string) bool {
	for _, seg := range strings.Split(path.Dir(p), "/") {
		if skipDirs[seg] && seg != "bin" && seg != "obj" {
			return true
		}
	}
	return false
}

// ---- classification -------------------------------------------------------------

var extLang = map[string]string{
	".go": "go", ".py": "python", ".js": "js", ".jsx": "js", ".mjs": "js", ".cjs": "js", ".ts": "ts", ".tsx": "ts",
	".rs": "rust", ".java": "java", ".kt": "kotlin", ".rb": "ruby", ".php": "php", ".c": "c", ".h": "c",
	".cc": "cpp", ".cpp": "cpp", ".hpp": "cpp", ".cs": "csharp", ".swift": "swift", ".scala": "scala", ".sh": "shell",
	".lua": "lua", ".ex": "elixir", ".exs": "elixir", ".zig": "zig", ".dart": "dart", ".vue": "vue", ".svelte": "svelte",
}

func languageOf(p string) string { return extLang[strings.ToLower(path.Ext(p))] }

// isNoise marks files that add size but no structure: tests, generated and
// minified code, fixtures.
func isNoise(p string) bool {
	lp := strings.ToLower(p)
	base := path.Base(lp)
	switch {
	case strings.HasSuffix(base, "_test.go"), strings.HasSuffix(base, ".pb.go"), strings.Contains(base, ".generated."),
		strings.HasSuffix(base, ".min.js"), strings.HasSuffix(base, ".d.ts"), strings.HasPrefix(base, "test_"),
		strings.HasSuffix(base, "_test.py"), strings.Contains(base, ".test."), strings.Contains(base, ".spec."):
		return true
	}
	for _, seg := range strings.Split(path.Dir(lp), "/") {
		switch seg {
		case "testdata", "fixtures", "__tests__", "tests", "test", "mocks", "__snapshots__", "third_party", "generated":
			return true
		}
	}
	return false
}

// ---- layout ---------------------------------------------------------------------

// layoutText prints the top two directory levels with file counts, one line per
// top-level directory, so the model can orient without listing anything.
func layoutText(files []string) string {
	type dir struct {
		n    int
		subs map[string]int
	}
	tops := map[string]*dir{}
	var rootFiles []string
	for _, f := range files {
		parts := strings.Split(f, "/")
		if len(parts) == 1 {
			rootFiles = append(rootFiles, f)
			continue
		}
		d := tops[parts[0]]
		if d == nil {
			d = &dir{subs: map[string]int{}}
			tops[parts[0]] = d
		}
		d.n++
		if len(parts) > 2 {
			d.subs[parts[1]]++
		}
	}
	names := make([]string, 0, len(tops))
	for n := range tops {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("Layout (file counts):\n")
	for _, n := range names {
		d := tops[n]
		fmt.Fprintf(&sb, "  %s/ %d", n, d.n)
		if len(d.subs) > 0 {
			subs := make([]string, 0, len(d.subs))
			for s := range d.subs {
				subs = append(subs, s)
			}
			sort.Slice(subs, func(i, j int) bool {
				if d.subs[subs[i]] != d.subs[subs[j]] {
					return d.subs[subs[i]] > d.subs[subs[j]]
				}
				return subs[i] < subs[j]
			})
			const maxSubs = 14
			var items []string
			for i, s := range subs {
				if i == maxSubs {
					items = append(items, fmt.Sprintf("+%d more", len(subs)-maxSubs))
					break
				}
				items = append(items, fmt.Sprintf("%s(%d)", s, d.subs[s]))
			}
			sb.WriteString(": " + strings.Join(items, " "))
		}
		sb.WriteString("\n")
	}
	if len(rootFiles) > 0 {
		sort.Strings(rootFiles)
		const maxRoot = 24
		shown := rootFiles
		more := 0
		if len(shown) > maxRoot {
			shown, more = shown[:maxRoot], len(shown)-maxRoot
		}
		sb.WriteString("  root files: " + strings.Join(shown, " "))
		if more > 0 {
			fmt.Fprintf(&sb, " +%d more", more)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// ---- manifests and commands -----------------------------------------------------

var (
	reGoModule  = regexp.MustCompile(`(?m)^module\s+(\S+)`)
	reGoVersion = regexp.MustCompile(`(?m)^go\s+(\S+)`)
	reMakeRule  = regexp.MustCompile(`(?m)^([a-zA-Z][a-zA-Z0-9_.-]*)\s*:([^=]|$)`)
	rePyName    = regexp.MustCompile(`(?m)^name\s*=\s*["']([^"']+)["']`)
	reCargoName = regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`)
)

func readSmall(root, rel string, max int64) string {
	f, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	buf := make([]byte, min64(st.Size(), max))
	n, _ := f.Read(buf)
	return string(buf[:n])
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func has(files []string, name string) bool {
	i := sort.SearchStrings(files, name)
	return i < len(files) && files[i] == name
}

func detectManifests(root string, files []string) (text string, cmds []string) {
	var sb strings.Builder
	add := func(format string, a ...any) { fmt.Fprintf(&sb, format+"\n", a...) }

	if has(files, "go.mod") {
		mod := readSmall(root, "go.mod", 64<<10)
		name, ver := "", ""
		if m := reGoModule.FindStringSubmatch(mod); m != nil {
			name = m[1]
		}
		if m := reGoVersion.FindStringSubmatch(mod); m != nil {
			ver = m[1]
		}
		add("Go module %s (go %s)", name, ver)
		var mains []string
		seen := map[string]bool{}
		for _, f := range files {
			if strings.HasPrefix(f, "cmd/") && strings.HasSuffix(f, ".go") && strings.Count(f, "/") == 2 {
				if d := path.Dir(f); !seen[d] {
					seen[d] = true
					mains = append(mains, d)
				}
			}
		}
		if len(mains) > 0 {
			add("  binaries: %s", strings.Join(firstN(mains, 8), " "))
		}
		cmds = append(cmds, "go build ./...", "go vet ./...", "go test ./...")
	}
	if has(files, "package.json") {
		pj := readSmall(root, "package.json", 256<<10)
		scripts := jsonKeys(pj, "scripts")
		add("Node package (package.json); scripts: %s", strings.Join(firstN(scripts, 14), ", "))
		for _, s := range scripts {
			switch s {
			case "test", "lint", "build", "typecheck", "check", "format":
				cmds = append(cmds, "npm run "+s)
			}
		}
	}
	if has(files, "pyproject.toml") || has(files, "setup.py") || has(files, "requirements.txt") {
		py := readSmall(root, "pyproject.toml", 64<<10)
		if m := rePyName.FindStringSubmatch(py); m != nil {
			add("Python project %s", m[1])
		} else {
			add("Python project")
		}
		if strings.Contains(py, "pytest") || dirHas(files, "tests/") || dirHas(files, "test/") {
			cmds = append(cmds, "pytest")
		}
		if strings.Contains(py, "ruff") {
			cmds = append(cmds, "ruff check .")
		}
		if strings.Contains(py, "mypy") {
			cmds = append(cmds, "mypy .")
		}
	}
	if has(files, "Cargo.toml") {
		c := readSmall(root, "Cargo.toml", 64<<10)
		if m := reCargoName.FindStringSubmatch(c); m != nil {
			add("Rust crate %s", m[1])
		} else {
			add("Rust workspace")
		}
		cmds = append(cmds, "cargo build", "cargo test", "cargo clippy")
	}
	if has(files, "pom.xml") {
		add("Maven project")
		cmds = append(cmds, "mvn test")
	}
	if has(files, "build.gradle") || has(files, "build.gradle.kts") {
		add("Gradle project")
		cmds = append(cmds, "./gradlew test")
	}
	mk := ""
	for _, n := range []string{"Makefile", "makefile", "GNUmakefile"} {
		if has(files, n) {
			mk = readSmall(root, n, 128<<10)
			break
		}
	}
	if mk != "" {
		var targets []string
		seen := map[string]bool{}
		for _, m := range reMakeRule.FindAllStringSubmatch(mk, -1) {
			t := m[1]
			if t == "PHONY" || strings.HasPrefix(t, ".") || seen[t] {
				continue
			}
			seen[t] = true
			targets = append(targets, t)
		}
		if len(targets) > 0 {
			add("Makefile targets: %s", strings.Join(firstN(targets, 16), " "))
		}
		for _, t := range targets {
			switch t {
			case "test", "tests", "check", "lint", "build", "fmt", "vet", "typecheck", "ci":
				cmds = append(cmds, "make "+t)
			}
		}
	}
	if has(files, "Dockerfile") {
		add("Dockerfile present")
	}
	var wf []string
	for _, f := range files {
		if strings.HasPrefix(f, ".github/workflows/") {
			wf = append(wf, path.Base(f))
		}
	}
	if len(wf) > 0 {
		add("CI: %s", strings.Join(firstN(wf, 6), " "))
	}
	return sb.String(), dedupe(cmds)
}

func dirHas(files []string, prefix string) bool {
	i := sort.SearchStrings(files, prefix)
	return i < len(files) && strings.HasPrefix(files[i], prefix)
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	out := append([]string(nil), s[:n]...)
	return append(out, fmt.Sprintf("+%d more", len(s)-n))
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// jsonKeys returns the sorted keys of the object at top-level member name, with
// no dependency on encoding/json's strictness (package.json files are often
// hand-edited and tolerated loosely).
func jsonKeys(doc, name string) []string {
	i := strings.Index(doc, `"`+name+`"`)
	if i < 0 {
		return nil
	}
	j := strings.Index(doc[i:], "{")
	if j < 0 {
		return nil
	}
	depth, start := 0, i+j
	var keys []string
	inStr, esc := false, false
	strStart := 0
	expectKey := false
	for k := start; k < len(doc); k++ {
		c := doc[k]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
				if depth == 1 && expectKey {
					keys = append(keys, doc[strStart:k])
					expectKey = false
				}
			}
			continue
		}
		switch c {
		case '"':
			inStr, strStart = true, k+1
		case '{':
			depth++
			if depth == 1 {
				expectKey = true
			}
		case '}':
			depth--
			if depth == 0 {
				sort.Strings(keys)
				return keys
			}
		case ',':
			if depth == 1 {
				expectKey = true
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// ---- symbol map -----------------------------------------------------------------

var symbolRes = map[string][]*regexp.Regexp{
	"go": {
		regexp.MustCompile(`^func\s+(?:\(\s*(?:\w+\s+)?\*?(\w+)[^)]*\)\s*)?([A-Za-z_]\w*)`),
		regexp.MustCompile(`^type\s+([A-Za-z_]\w*)\s+(?:struct|interface)`),
	},
	"python": {regexp.MustCompile(`^(?:async\s+)?(?:def|class)\s+([A-Za-z_]\w*)`)},
	"js": {
		regexp.MustCompile(`^export\s+(?:default\s+)?(?:async\s+)?(?:function\*?|class|const|let|var)\s+([A-Za-z_$][\w$]*)`),
		regexp.MustCompile(`^(?:async\s+)?function\s+([A-Za-z_$][\w$]*)`),
	},
	"ts": {
		regexp.MustCompile(`^export\s+(?:default\s+)?(?:declare\s+)?(?:abstract\s+)?(?:async\s+)?(?:function\*?|class|const|let|interface|type|enum)\s+([A-Za-z_$][\w$]*)`),
	},
	"rust":   {regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:fn|struct|enum|trait)\s+([A-Za-z_]\w*)`)},
	"java":   {regexp.MustCompile(`^\s*(?:public|protected)\s+(?:abstract\s+|final\s+|static\s+)*(?:class|interface|enum|record)\s+([A-Za-z_]\w*)`)},
	"kotlin": {regexp.MustCompile(`^\s*(?:public\s+|internal\s+)?(?:data\s+|sealed\s+|abstract\s+|open\s+)*(?:class|interface|object|fun)\s+([A-Za-z_]\w*)`)},
	"csharp": {regexp.MustCompile(`^\s*(?:public|internal)\s+(?:static\s+|abstract\s+|sealed\s+|partial\s+)*(?:class|interface|enum|struct|record)\s+([A-Za-z_]\w*)`)},
	"ruby":   {regexp.MustCompile(`^\s*(?:class|module|def)\s+([A-Za-z_][\w:.]*)`)},
	"php":    {regexp.MustCompile(`^\s*(?:abstract\s+|final\s+)?(?:class|interface|trait|function)\s+([A-Za-z_]\w*)`)},
}

type ranked struct {
	file  string
	score float64
	lines int
	syms  []string
}

// symbolMap picks the most structurally important files and lists their
// top-level declarations, as many as the budget allows. Importance is package
// in-degree (how much of the project depends on it), recent churn, size and a
// bonus for entry points; tests, generated and vendored code are excluded.
func symbolMap(ctx context.Context, root string, files []string, budget int, est core.Estimator, useGit bool) string {
	var cand []string
	for _, f := range files {
		if languageOf(f) != "" && symbolRes[languageOf(f)] != nil && !isNoise(f) {
			cand = append(cand, f)
		}
	}
	if len(cand) == 0 {
		return ""
	}
	churn := map[string]int{}
	if useGit {
		churn = gitChurn(ctx, root)
	}
	goMod := ""
	if has(files, "go.mod") {
		if m := reGoModule.FindStringSubmatch(readSmall(root, "go.mod", 64<<10)); m != nil {
			goMod = m[1]
		}
	}
	indeg := map[string]int{}
	texts := map[string]string{}
	const maxRead = 256 << 10
	for _, f := range cand {
		t := readSmall(root, f, maxRead)
		if t == "" || strings.Contains(t[:min(len(t), 512)], "Code generated") {
			continue
		}
		texts[f] = t
		countImports(f, t, goMod, indeg)
	}

	var all []ranked
	for f, t := range texts {
		lang := languageOf(f)
		syms := extractSymbols(lang, t)
		if len(syms) == 0 {
			continue
		}
		lines := strings.Count(t, "\n") + 1
		score := 3*float64(indeg[importKey(f, lang)]) + 2*math.Log2(1+float64(churn[f])) + math.Log2(1+float64(lines))/2
		base := strings.ToLower(path.Base(f))
		if strings.HasPrefix(base, "main.") || base == "index.ts" || base == "index.js" || base == "app.py" || base == "lib.rs" || base == "__init__.py" && false {
			score += 3
		}
		if strings.Contains(f, "cmd/") {
			score += 1
		}
		all = append(all, ranked{f, score, lines, syms})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].file < all[j].file
	})

	var sb strings.Builder
	if pk := packageDocs(root, texts, indeg, budget/4, est); pk != "" {
		sb.WriteString(pk)
		sb.WriteString("\n")
	}
	sb.WriteString("Most central files and their top-level declarations (ranked by dependents and recent churn; read the file for details):\n")
	used := est.Tokens(sb.String())
	shown := 0
	for _, rk := range all {
		line := rk.file + ": " + strings.Join(limitSyms(rk.syms, symbolCap(rk.score)), ", ") + "\n"
		cost := est.Tokens(line)
		if used+cost > budget {
			if shown > 0 {
				break
			}
			continue
		}
		sb.WriteString(line)
		used += cost
		shown++
	}
	if rest := len(all) - shown; rest > 0 {
		fmt.Fprintf(&sb, "(+%d more source files not shown)\n", rest)
	}
	if shown == 0 {
		return ""
	}
	return sb.String()
}

var rePkgDoc = regexp.MustCompile(`(?m)^//\s*Package\s+\w+\s+(.*)$`)

// packageDocs lists the most depended-on Go packages with the first sentence of
// their package comment: one dense line each says what a package is for, which
// is exactly the orientation a fresh agent would otherwise spend calls on.
func packageDocs(root string, texts map[string]string, indeg map[string]int, budget int, est core.Estimator) string {
	type pk struct {
		dir string
		doc string
	}
	seen := map[string]bool{}
	var pks []pk
	files := make([]string, 0, len(texts))
	for f := range texts {
		if languageOf(f) == "go" {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	for _, f := range files {
		dir := path.Dir(f)
		if seen[dir] {
			continue
		}
		m := rePkgDoc.FindStringSubmatch(texts[f])
		if m == nil {
			continue
		}
		// The comment may continue over several lines; take the sentence.
		doc := m[1]
		if i := strings.Index(texts[f], m[0]); i >= 0 {
			rest := strings.TrimPrefix(texts[f][i+len(m[0]):], "\n")
			for _, ln := range strings.Split(rest, "\n") {
				if !strings.HasPrefix(ln, "//") || strings.TrimSpace(strings.TrimPrefix(ln, "//")) == "" || len(doc) > 200 {
					break
				}
				doc += " " + strings.TrimSpace(strings.TrimPrefix(ln, "//"))
			}
		}
		if i := strings.Index(doc, ". "); i > 0 {
			doc = doc[:i+1]
		}
		if len(doc) > 170 {
			doc = doc[:170] + "…"
		}
		seen[dir] = true
		pks = append(pks, pk{dir, doc})
	}
	if len(pks) == 0 {
		return ""
	}
	sort.SliceStable(pks, func(i, j int) bool {
		if indeg[pks[i].dir] != indeg[pks[j].dir] {
			return indeg[pks[i].dir] > indeg[pks[j].dir]
		}
		return pks[i].dir < pks[j].dir
	})
	var sb strings.Builder
	sb.WriteString("Packages (most depended-on first):\n")
	used := est.Tokens(sb.String())
	for _, p := range pks {
		line := p.dir + ": " + p.doc + "\n"
		if c := est.Tokens(line); used+c > budget {
			break
		} else {
			used += c
		}
		sb.WriteString(line)
	}
	return sb.String()
}

func symbolCap(score float64) int {
	switch {
	case score >= 12:
		return 20
	case score >= 6:
		return 12
	default:
		return 7
	}
}

func limitSyms(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	out := append([]string(nil), s[:n]...)
	return append(out, fmt.Sprintf("+%d", len(s)-n))
}

func extractSymbols(lang, text string) []string {
	res := symbolRes[lang]
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if len(line) > 240 {
			line = line[:240]
		}
		for _, re := range res {
			m := re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := m[len(m)-1]
			if lang == "go" && len(m) == 3 && m[1] != "" { // method: Type.Name
				name = m[1] + "." + m[2]
			}
			if name == "" || seen[name] {
				break
			}
			// Unexported Go/Python helpers, and methods of unexported types, are noise in
			// a map of a whole project.
			if lang == "go" {
				exported := isExportedGo(name)
				if i := strings.IndexByte(name, '.'); i >= 0 {
					exported = exported && isExportedGo(name[:i])
				}
				if !exported {
					break
				}
			}
			if lang == "python" && strings.HasPrefix(name, "_") {
				break
			}
			seen[name] = true
			out = append(out, name)
			break
		}
	}
	return out
}

func isExportedGo(name string) bool {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}

var (
	reGoImport = regexp.MustCompile(`"([^"\s]+)"`)
	reJSImport = regexp.MustCompile(`(?:from\s+|require\(\s*|import\s+)['"](\.[^'"]+)['"]`)
	rePyImport = regexp.MustCompile(`(?m)^\s*(?:from\s+([\w.]+)\s+import|import\s+([\w.]+))`)
)

// importKey is the identity a file is counted under: its Go package directory,
// or its extensionless path for JS/TS, or its module stem for Python.
func importKey(f, lang string) string {
	switch lang {
	case "go":
		return path.Dir(f)
	case "js", "ts":
		return strings.TrimSuffix(f, path.Ext(f))
	case "python":
		return strings.TrimSuffix(path.Base(f), ".py")
	}
	return f
}

func countImports(f, text, goMod string, indeg map[string]int) {
	switch languageOf(f) {
	case "go":
		if goMod == "" {
			return
		}
		blk := text
		if i := strings.Index(text, "\nimport"); i >= 0 {
			blk = text[i:]
			if j := strings.Index(blk, "\n)\n"); j >= 0 && strings.Contains(blk[:j], "(") {
				blk = blk[:j]
			} else if j := strings.Index(blk[1:], "\n"); j >= 0 {
				blk = blk[:j+1]
			}
		}
		for _, m := range reGoImport.FindAllStringSubmatch(blk, -1) {
			p := m[1]
			if p == goMod {
				indeg["."]++
			} else if strings.HasPrefix(p, goMod+"/") {
				indeg[strings.TrimPrefix(p, goMod+"/")]++
			}
		}
	case "js", "ts":
		dir := path.Dir(f)
		for _, m := range reJSImport.FindAllStringSubmatch(text, -1) {
			p := path.Clean(path.Join(dir, m[1]))
			indeg[strings.TrimSuffix(p, path.Ext(p))]++
			indeg[p+"/index"]++
		}
	case "python":
		for _, m := range rePyImport.FindAllStringSubmatch(text, -1) {
			mod := m[1]
			if mod == "" {
				mod = m[2]
			}
			if i := strings.LastIndexByte(mod, '.'); i >= 0 {
				mod = mod[i+1:]
			}
			indeg[mod]++
		}
	}
}

// gitChurn counts how often each file changed in recent history; recently active
// files are where an agent is most likely to work.
func gitChurn(ctx context.Context, root string) map[string]int {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "log", "--name-only", "--pretty=format:", "--since=180.days", "-n", "400")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	b, err := cmd.Output()
	out := map[string]int{}
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out[l]++
		}
	}
	return out
}

// fitTokens trims text to the budget on a line boundary.
func fitTokens(text string, budget int, est core.Estimator) string {
	if est.Tokens(text) <= budget {
		return text
	}
	lines := strings.SplitAfter(text, "\n")
	var sb strings.Builder
	for _, l := range lines {
		if est.Tokens(sb.String()+l) > budget-8 {
			sb.WriteString("(survey truncated)\n")
			break
		}
		sb.WriteString(l)
	}
	return sb.String()
}
