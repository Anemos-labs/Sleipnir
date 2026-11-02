package scan

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// maxFileBytes is the largest file the inventory splits into functions; a bigger one is scanned as a whole, of kind large.
// maxReadBytes is the largest it reads at all: a bigger one is listed with its size only.
const (
	maxFileBytes = 1 << 20
	maxReadBytes = 64 << 20
)

// File kinds: what a file is when it is not code a person wrote. Every tracked file is scanned; the kind says how to read it.
const (
	KindVendored  = "vendored"  // code the project does not own or build by hand: vendor/, node_modules/, dist/
	KindGenerated = "generated" // written by a tool: minified bundles, protobuf output, lock files
	KindData      = "data"      // data files: fixtures, JSON that is not configuration
	KindLarge     = "large"     // over 1 MiB: scanned whole, its functions not split out
	KindOther     = "other"     // text in no language the inventory knows: docs, templates, scripts without an extension
	KindBinary    = "binary"    // not text: executables, archives, images, compiled objects
	KindSymlink   = "symlink"   // a link: its target is what it holds
	KindSubmodule = "submodule" // another repository, pinned at a commit
)

// langs maps a file extension to the language the inventory counts it as. "config" files have no functions but are read
// for the config lane.
var langs = map[string]string{
	".go": "go", ".js": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".jsx": "javascript", ".ts": "typescript",
	".tsx": "typescript", ".mts": "typescript", ".py": "python", ".rb": "ruby", ".php": "php", ".java": "java", ".kt": "kotlin",
	".kts": "kotlin", ".scala": "scala", ".cs": "csharp", ".rs": "rust", ".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp",
	".cxx": "cpp", ".hpp": "cpp", ".swift": "swift", ".m": "objc", ".lua": "lua", ".sh": "shell", ".bash": "shell",
	".ex": "elixir", ".exs": "elixir", ".erl": "erlang", ".dart": "dart", ".vue": "vue", ".svelte": "svelte", ".sol": "solidity",
	".sql": "sql", ".pl": "perl",
	".yml": "config", ".yaml": "config", ".toml": "config", ".ini": "config", ".conf": "config", ".tf": "config",
	".hcl": "config", ".xml": "config", ".properties": "config", ".gradle": "config", ".json": "config",
}

// configNames are config files known by their whole name.
var configNames = map[string]bool{"dockerfile": true, "containerfile": true, "makefile": true, "procfile": true, ".env.example": true,
	"docker-compose.yml": true, "nginx.conf": true, "caddyfile": true, "vagrantfile": true, "jenkinsfile": true}

// skipDirs are directories of code the project does not own, or does not write by hand: their files are of kind vendored.
var skipDirs = []string{"vendor/", "node_modules/", "third_party/", "third-party/", "external/", "bower_components/", "dist/",
	"build/", "out/", "target/", ".git/", ".sleipnir/", "__pycache__/", ".venv/", "venv/", "site-packages/", "Pods/", ".next/",
	"coverage/", ".yarn/"}

// generated matches files a tool wrote.
var generated = regexp.MustCompile(`(\.min\.(js|css)$|\.pb\.go$|_generated\.go$|\.gen\.go$|_pb2\.py$|\.g\.dart$|\.designer\.cs$|` +
	`(^|/)(package-lock\.json|yarn\.lock|pnpm-lock\.yaml|go\.sum|Cargo\.lock|poetry\.lock|composer\.lock|Gemfile\.lock)$)`)

// testFile matches test files.
var testFile = regexp.MustCompile(`(_test\.go$|\.test\.[jt]sx?$|\.spec\.[jt]sx?$|(^|/)test_[^/]*\.py$|_test\.py$|_spec\.rb$|` +
	`(^|/)(tests?|__tests__|spec|testdata|fixtures)/|Test\.java$|Tests?\.cs$)`)

// TakeInventory lists the files of the commit the scan reads (below focus, a directory of the repository, when set) and the
// functions in them. Only what git has at that commit counts: the work tree may hold anything, the commit is what was pinned.
func TakeInventory(ctx context.Context, repo *gitx.Repo, commit, focus string) (Inventory, error) {
	return takeInventory(ctx, repo, commit, focus, nil)
}

// inventory is the inventory of only the given files of the commit (a change's), in the order git lists them.
func inventory(ctx context.Context, repo *gitx.Repo, commit string, files []string) (Inventory, error) {
	only := map[string]bool{}
	for _, f := range files {
		only[f] = true
	}
	return takeInventory(ctx, repo, commit, "", only)
}

func takeInventory(ctx context.Context, repo *gitx.Repo, commit, focus string, only map[string]bool) (Inventory, error) {
	args := []string{"ls-tree", "-r", "-z", "-l", "--end-of-options", commit}
	if focus = strings.Trim(path.Clean("/"+focus), "/"); focus != "" {
		args = append(args, "--", focus)
	}
	res, err := repo.Git(ctx, args...)
	if err != nil {
		return Inventory{}, err
	}
	inv := Inventory{}
	for _, ent := range strings.Split(res.Stdout, "\x00") {
		meta, p, ok := strings.Cut(ent, "\t")
		if !ok || only != nil && !only[p] {
			continue
		}
		f := strings.Fields(meta) // mode type object size
		if len(f) < 2 {
			continue
		}
		// nothing tracked is left out: what no person wrote, what is not code and what cannot be read as text are scanned
		// too, marked by kind, so a hunter knows how to read them
		switch {
		case f[1] == "commit":
			inv.Files = append(inv.Files, File{Path: p, Lang: KindSubmodule, Kind: KindSubmodule})
			continue
		case f[1] != "blob" || len(f) < 4:
			continue
		case f[0] == "120000":
			inv.Files = append(inv.Files, File{Path: p, Lang: KindSymlink, Kind: KindSymlink, Lines: 1})
			continue
		}
		size, _ := strconv.Atoi(f[3])
		lang, kind := classify(p, size)
		file := File{Path: p, Lang: lang, Kind: kind, Bytes: size, Test: testFile.MatchString(p)}
		if size > maxReadBytes {
			// too big to count here: listed with its size, read in parts by whoever has its shard
			file.Kind = KindLarge
			inv.Files = append(inv.Files, file)
			continue
		}
		data, err := repo.Show(ctx, commit, p)
		if err != nil {
			inv.Skipped = addSkip(inv.Skipped, "unreadable")
			continue
		}
		if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			file.Lang, file.Kind = KindBinary, KindBinary
			inv.Files = append(inv.Files, file)
			continue
		}
		file.Lines = bytes.Count(data, []byte{'\n'}) + boolInt(len(data) > 0 && data[len(data)-1] != '\n')
		// functions are split out of code a person wrote, of a size a parser reads in a moment
		if kind == "" && size <= maxFileBytes {
			for _, fn := range Functions(lang, p, data) {
				file.Funcs++
				file.Complexity += fn.Complexity
				inv.Functions = append(inv.Functions, fn)
			}
		}
		inv.Files = append(inv.Files, file)
	}
	return inv, nil
}

func addSkip(m map[string]int, reason string) map[string]int {
	if m == nil {
		m = map[string]int{}
	}
	m[reason]++
	return m
}

// classify is a path's language (text when it has none the inventory knows) and its kind: empty for code a person wrote, else
// what it is (vendored, generated, data, large, other).
func classify(p string, size int) (lang, kind string) {
	low := strings.ToLower(p)
	base := strings.ToLower(path.Base(p))
	lang = "text"
	if configNames[base] || strings.HasPrefix(base, "dockerfile") || strings.HasPrefix(base, ".env") {
		lang = "config"
	} else if l, ok := langs[path.Ext(low)]; ok {
		lang = l
	}
	for _, d := range skipDirs {
		if strings.HasPrefix(low, strings.ToLower(d)) || strings.Contains(low, "/"+strings.ToLower(d)) {
			return lang, KindVendored
		}
	}
	switch {
	case generated.MatchString(p):
		return lang, KindGenerated
	case lang == "config" && path.Ext(low) == ".json" && !strings.Contains(base, "config") && !strings.Contains(base, "settings") &&
		base != "package.json" && base != "tsconfig.json" && base != "composer.json" && !strings.Contains(base, "policy"):
		return lang, KindData
	case size > maxFileBytes:
		return lang, KindLarge
	case lang == "text":
		return lang, KindOther
	}
	return lang, ""
}

// Functions finds the functions of one file: exactly for Go, by their usual shapes for the other languages.
func Functions(lang, file string, data []byte) []Func {
	switch lang {
	case "go":
		if fs, ok := goFunctions(file, data); ok {
			return fs
		}
	case "config", "sql", "":
		return nil
	}
	return heuristicFunctions(lang, file, data)
}

// goFunctions parses a Go file. Complexity is McCabe's: one, plus one per if, loop, case and short-circuit operator.
func goFunctions(file string, data []byte) ([]Func, bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, false
	}
	var out []Func
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		name := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			name = recvName(fd.Recv.List[0].Type) + "." + name
		}
		c := 1
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
				c++
			case *ast.CaseClause:
				if n.List != nil {
					c++
				}
			case *ast.CommClause:
				if n.Comm != nil {
					c++
				}
			case *ast.BinaryExpr:
				if n.Op == token.LAND || n.Op == token.LOR {
					c++
				}
			}
			return true
		})
		out = append(out, Func{File: file, Name: name, Line: fset.Position(fd.Pos()).Line, End: fset.Position(fd.End()).Line, Complexity: c})
	}
	return out, true
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	}
	return "?"
}

// funcStart matches the first line of a function in the languages without a parser here; the first group that matched is the
// name.
var funcStart = map[string]*regexp.Regexp{
	"python": regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)\s*\(`),
	"ruby":   regexp.MustCompile(`^\s*def\s+(?:self\.)?([A-Za-z_]\w*[?!=]?)`),
	"javascript": regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)\s*\(|` +
		`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*=>|[A-Za-z_$][\w$]*\s*=>)|` +
		`^\s*(?:static\s+|async\s+|get\s+|set\s+|public\s+|private\s+|protected\s+)*([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*(?::\s*[^{]+)?\{\s*$`),
	"php":    regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract)\s+)*function\s+&?([A-Za-z_]\w*)\s*\(`),
	"rust":   regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:const\s+)?(?:async\s+)?(?:unsafe\s+)?(?:extern\s+"[^"]*"\s+)?fn\s+([A-Za-z_]\w*)`),
	"kotlin": regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|override|suspend|inline|open|operator)\s+)*fun\s+(?:<[^>]*>\s*)?(?:[\w.]+\.)?([A-Za-z_]\w*)\s*\(`),
	"swift":  regexp.MustCompile(`^\s*(?:(?:public|private|fileprivate|internal|open|static|override|mutating|@\w+)\s+)*func\s+([A-Za-z_]\w*)`),
	"elixir": regexp.MustCompile(`^\s*defp?\s+([a-z_]\w*[?!]?)`),
	"lua":    regexp.MustCompile(`^\s*(?:local\s+)?function\s+([\w.:]+)\s*\(`),
	"shell":  regexp.MustCompile(`^\s*(?:function\s+)?([A-Za-z_][\w-]*)\s*\(\)\s*\{?`),
	"dart":   regexp.MustCompile(`^\s*(?:[\w<>?,\s]+\s+)?([A-Za-z_]\w*)\s*\([^;]*\)\s*(?:async\s*)?\{\s*$`),
	"perl":   regexp.MustCompile(`^\s*sub\s+([A-Za-z_]\w*)`),
	"erlang": regexp.MustCompile(`^([a-z]\w*)\s*\(.*\)\s*(?:when\s.*)?->`),
	// C-like languages: a line that names a function and opens its body, not a control statement.
	"clike": regexp.MustCompile(`^\s*(?:[\w:<>\[\],*&\s]+?\s+[*&]*)?([A-Za-z_~][\w:~]*)\s*\([^;]*\)\s*(?:const\s*)?(?:noexcept\s*)?(?:throws\s+[\w.,\s]+)?(?:->\s*[\w:<>]+\s*)?\{?\s*$`),
}

// branch counts the decisions on a line, for a complexity score where there is no parser: the keywords that branch or loop,
// and the short-circuit and conditional operators.
var branch = regexp.MustCompile(`\b(if|elif|elsif|for|foreach|while|case|when|catch|except|rescue|unless|until|guard)\b|&&|\|\||\?\?|\band\b|\bor\b`)

var notFunc = map[string]bool{"if": true, "for": true, "while": true, "switch": true, "catch": true, "return": true, "else": true,
	"sizeof": true, "do": true, "new": true, "using": true, "lock": true, "fixed": true, "foreach": true, "elif": true, "synchronized": true,
	"function": true, "typeof": true, "await": true, "delete": true}

// heuristicWork bounds the lines heuristicFunctions reads for one file.
const heuristicWork = 4 << 20

// heuristicFunctions finds functions by the shape of their first line, and ends each where its body closes: at the brace that
// matches the first one opened, or, in the languages that indent their blocks, at the next line indented no deeper.
func heuristicFunctions(lang, file string, data []byte) []Func {
	re := funcStart[lang]
	if re == nil {
		switch lang {
		case "typescript", "vue", "svelte":
			re = funcStart["javascript"]
		default:
			re = funcStart["clike"]
		}
	}
	indented := lang == "python"
	keyword := lang == "ruby" || lang == "elixir" || lang == "lua"
	lines := strings.Split(string(data), "\n")
	var out []Func
	// Each body is found by reading on from its first line, and the bodies of the indented and keyword languages may overlap (or,
	// without their closing line, run to the end of the file): the lines read are counted, and a file that costs more than
	// heuristicWork of them keeps the functions found so far.
	work := 0
	for i := 0; i < len(lines) && work < heuristicWork; i++ {
		m := re.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		name := ""
		for _, g := range m[1:] {
			if g != "" {
				name = g
				break
			}
		}
		if name == "" || notFunc[name] {
			continue
		}
		var end int
		switch {
		case indented:
			end = indentEnd(lines, i)
		case keyword:
			var read int
			end, read = keywordEnd(lines, i)
			work += read
		default:
			end = braceEnd(lines, i)
		}
		work += max(end, i) - i + 1
		if end < 0 {
			continue // a declaration without a body
		}
		c := 1
		for _, l := range lines[i+1 : end+1] {
			c += len(branch.FindAllStringIndex(stripLineComment(l), -1))
		}
		out = append(out, Func{File: file, Name: name, Line: i + 1, End: end + 1, Complexity: c})
		if !indented && !keyword {
			i = end // nested functions are part of the one that holds them
		}
	}
	return out
}

func stripLineComment(l string) string {
	t := strings.TrimSpace(l)
	if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "--") {
		return ""
	}
	return l
}

// braceEnd is the line where the body opened at or right after line i closes; -1 when no body opens within two lines.
func braceEnd(lines []string, i int) int {
	depth, opened := 0, false
	for j := i; j < len(lines); j++ {
		inStr := rune(0)
		for k, r := range lines[j] {
			if inStr != 0 {
				if r == inStr && (k == 0 || lines[j][k-1] != '\\') {
					inStr = 0
				}
				continue
			}
			switch r {
			case '"', '\'', '`':
				inStr = r
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			case ';':
				if !opened && j <= i+2 {
					return -1
				}
			}
			if r == '/' && k+1 < len(lines[j]) && lines[j][k+1] == '/' {
				break
			}
		}
		if opened && depth <= 0 {
			return j
		}
		if !opened && j >= i+2 {
			return -1
		}
	}
	if opened {
		return len(lines) - 1
	}
	return -1
}

// indentEnd is the last line of the block that line i opens, by indentation.
func indentEnd(lines []string, i int) int {
	base := indentOf(lines[i])
	end := i
	for j := i + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			continue
		}
		if indentOf(lines[j]) <= base {
			break
		}
		end = j
	}
	return end
}

// keywordEnd is the line of the "end" that closes the block line i opens: the first one indented as deep as the opening line.
// read is how many lines it looked at to find it.
func keywordEnd(lines []string, i int) (end, read int) {
	base := indentOf(lines[i])
	for j := i + 1; j < len(lines); j++ {
		t := strings.TrimSpace(lines[j])
		if (t == "end" || strings.HasPrefix(t, "end ") || strings.HasPrefix(t, "end)")) && indentOf(lines[j]) <= base {
			return j, j - i
		}
	}
	return indentEnd(lines, i), len(lines) - i
}

func indentOf(l string) int {
	n := 0
	for _, r := range l {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// minShards is how many shards a small repository is still split into, so a team has something to share from the start.
const minShards = 8

// MakeShards splits the inventory into shards of at most d's size, keeping files of one directory together where they fit:
// the files are taken in path order, and a shard is closed when the next file would make it too big, or when the next file is
// in another directory and the shard is already a third full. A config file, and one no person wrote (vendored, generated,
// data, other), weighs a quarter of its lines; a binary, a submodule or a file too big to count a quarter of a shard. A repository too
// small to fill minShards shards of d's size gets smaller ones (down to a file each), so the whole team can map it at once.
func MakeShards(inv Inventory, d Depth) []Shard {
	files := append([]File(nil), inv.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	tf, tl := 0, 0
	for _, f := range files {
		tf += f.Funcs
		tl += f.Lines
	}
	d.ShardFuncs = min(d.ShardFuncs, max((tf+minShards-1)/minShards, 1))
	d.ShardLines = min(d.ShardLines, max((tl+minShards-1)/minShards, 1))
	var out []Shard
	var cur Shard
	weight := func(f File) int {
		switch {
		case f.Kind == KindBinary || f.Kind == KindSubmodule || f.Kind == KindLarge && f.Lines == 0:
			// read with tools, not line by line: a binary is worth a quarter of a shard
			return max(d.ShardLines/4, 1)
		case f.Lang == "config" || f.Kind != "" && f.Kind != KindLarge:
			return f.Lines / 4
		}
		return f.Lines
	}
	flush := func() {
		if len(cur.Files) > 0 {
			cur.ID = fmt.Sprintf("S%d", len(out)+1)
			out = append(out, cur)
		}
		cur = Shard{}
	}
	lastDir := ""
	for _, f := range files {
		dir := path.Dir(f.Path)
		w := weight(f)
		full := len(cur.Files) > 0 && (cur.Funcs+f.Funcs > d.ShardFuncs || cur.Lines+w > d.ShardLines)
		moved := len(cur.Files) > 0 && dir != lastDir && (cur.Funcs*3 >= d.ShardFuncs || cur.Lines*3 >= d.ShardLines)
		if full || moved {
			flush()
		}
		cur.Files = append(cur.Files, f.Path)
		cur.Funcs += f.Funcs
		cur.Lines += w
		cur.Complexity += f.Complexity
		lastDir = dir
	}
	flush()
	return out
}
