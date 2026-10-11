package taskgen

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/doc"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The Go side of rebuild tasks: a scope of package directories, their stubs,
// their API and the order they import each other in.

// goStubBody is the body every function and method of a Go stub gets.
const goStubBody = "{\n\tpanic(\"not implemented\")\n}"

// goStubLit is the body a function literal of a Go stub gets.
const goStubLit = "{ panic(\"not implemented\") }"

// goModuleRe finds the module directive of a go.mod file.
var goModuleRe = regexp.MustCompile("(?m)^[ \t]*module[ \t]+[\"`]?([^\\s\"`]+)")

// goModulePath is the module path a go.mod file declares, "" when it declares none.
func goModulePath(gomod []byte) string {
	if m := goModuleRe.FindSubmatch(gomod); m != nil {
		return string(m[1])
	}
	return ""
}

// goMajorRe matches the major-version suffix of a module path.
var goMajorRe = regexp.MustCompile(`/v[0-9]+$`)

// escapeModulePath is a module path as the module cache spells it: every upper-case letter becomes '!' and its lower
// case.
func escapeModulePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// goIgnoredDir reports whether the go command ignores the directory rel (relative to a module's root): one of its
// elements is testdata or vendor, or starts with "." or "_".
func goIgnoredDir(rel string) bool {
	if rel == "." || rel == "" {
		return false
	}
	for _, el := range strings.Split(rel, "/") {
		if el == "testdata" || el == "vendor" || strings.HasPrefix(el, ".") || strings.HasPrefix(el, "_") {
			return true
		}
	}
	return false
}

// relDir is dir relative to root, both repository-relative ("." for the root).
func relDir(root, dir string) string {
	switch {
	case root == "." || root == "":
		return dir
	case dir == root:
		return "."
	}
	return strings.TrimPrefix(dir, root+"/")
}

// rebuildGo resolves a Go scope at commit and prepares what the task needs. A scope entry is a package directory
// relative to the repository ("." for its root), "dir/..." for that directory and every package below it, or the
// import path of a package of a module in the repository. Every package must belong to one module.
func rebuildGo(ctx context.Context, r *repo, commit string, files []string, opts RebuildOptions, gate func(int) error) (*rebuildLib, error) {
	isFile := map[string]bool{}
	byDir := map[string][]string{}
	isDir := map[string]bool{".": true}
	for _, f := range files {
		isFile[f] = true
		d := path.Dir(f)
		byDir[d] = append(byDir[d], f)
		for ; d != "." && !isDir[d]; d = path.Dir(d) {
			isDir[d] = true
		}
	}
	hasSources := func(d string) bool {
		for _, f := range byDir[d] {
			if strings.HasSuffix(f, ".go") && !strings.HasSuffix(f, "_test.go") {
				return true
			}
		}
		return false
	}
	moduleOf := func(dir string) (string, bool) {
		for d := dir; ; d = path.Dir(d) {
			if isFile[path.Join(d, "go.mod")] {
				return d, true
			}
			if d == "." {
				return "", false
			}
		}
	}
	modPaths := map[string]string{}
	modulePath := func(modDir string) string {
		if mp, ok := modPaths[modDir]; ok {
			return mp
		}
		b, _ := r.show(ctx, commit, path.Join(modDir, "go.mod"))
		modPaths[modDir] = goModulePath(b)
		return modPaths[modDir]
	}

	var dirs []string
	seen := map[string]bool{}
	mod := ""
	for _, entry := range opts.Scope {
		e := strings.TrimSpace(entry)
		recursive := e == "..." || strings.HasSuffix(e, "/...")
		e = strings.TrimSuffix(strings.TrimSuffix(e, "..."), "/")
		e = strings.TrimPrefix(e, "./")
		if e == "" {
			e = "."
		}
		e = path.Clean(e)
		dir := e
		if !isDir[dir] {
			// The import path of a package of one of the repository's modules.
			best := ""
			for _, f := range files {
				if path.Base(f) != "go.mod" {
					continue
				}
				md := path.Dir(f)
				mp := modulePath(md)
				if mp != "" && (e == mp || strings.HasPrefix(e, mp+"/")) && len(mp) > len(best) {
					best, dir = mp, path.Join(md, strings.TrimPrefix(strings.TrimPrefix(e, mp), "/"))
				}
			}
			if best == "" || !isDir[dir] {
				return nil, reject(ReasonRebuildScope, "%q is neither a directory of the repository nor a package of a Go module in it", entry)
			}
		}
		md, ok := moduleOf(dir)
		if !ok {
			return nil, reject(ReasonRebuildScope, "%q is in no Go module (no go.mod at or above it)", entry)
		}
		if mod == "" {
			mod = md
		} else if mod != md {
			return nil, reject(ReasonRebuildScope, "the scope spans several Go modules (%s and %s)", mod, md)
		}
		var found []string
		for d := range isDir {
			inside := d == dir || dir == "." || strings.HasPrefix(d, dir+"/")
			if d != dir && (!recursive || !inside) {
				continue
			}
			if m, _ := moduleOf(d); m != md || goIgnoredDir(relDir(md, d)) || !hasSources(d) {
				continue
			}
			found = append(found, d)
		}
		if len(found) == 0 {
			return nil, reject(ReasonRebuildScope, "%q has no Go package (no non-test .go file)", entry)
		}
		sort.Strings(found)
		for _, d := range found {
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	modPath := modulePath(mod)
	if modPath == "" {
		return nil, reject(ReasonRebuildScope, "%s declares no module path", path.Join(mod, "go.mod"))
	}
	importPath := func(d string) string {
		if rel := relDir(mod, d); rel != "." {
			return modPath + "/" + rel
		}
		return modPath
	}

	// Each package's sources, and its tests and testdata (the hidden suite).
	testdata := map[string][]string{}
	var paths []string
	for _, f := range files {
		for d := path.Dir(f); d != "."; d = path.Dir(d) {
			if path.Base(d) == "testdata" && seen[path.Dir(d)] {
				testdata[path.Dir(d)] = append(testdata[path.Dir(d)], f)
				paths = append(paths, f)
				break
			}
		}
	}
	for _, d := range dirs {
		for _, f := range byDir[d] {
			if strings.HasSuffix(f, ".go") {
				paths = append(paths, f)
			}
		}
	}
	content, err := r.readFiles(ctx, commit, paths)
	if err != nil {
		return nil, err
	}
	lib := &rebuildLib{lang: Go, root: mod, module: modPath, sources: map[string][]byte{}, hidden: map[string][]byte{}}
	srcs := map[string]map[string][]byte{}  // dir -> source files
	tests := map[string]map[string][]byte{} // dir -> test files
	total := 0
	for _, d := range dirs {
		srcs[d], tests[d] = map[string][]byte{}, map[string][]byte{}
		p := &rebuildPkg{Name: importPath(d), Dir: d}
		for _, f := range byDir[d] {
			if !strings.HasSuffix(f, ".go") {
				continue
			}
			b := content[f]
			if strings.HasSuffix(f, "_test.go") {
				lib.hidden[f], tests[d][f] = b, b
				continue
			}
			if !utf8.Valid(b) {
				return nil, reject(ReasonRebuildScope, "%s is not UTF-8", f)
			}
			lib.sources[f], srcs[d][f] = b, b
			p.Files = append(p.Files, f)
			p.LOC += countLOC(b, "//")
		}
		sort.Strings(p.Files)
		for _, f := range testdata[d] {
			lib.hidden[f] = content[f]
		}
		total += p.LOC
		lib.pkgs = append(lib.pkgs, p)
	}
	if err := gate(total); err != nil {
		return nil, err
	}

	// Package names, the imports between the packages, and what their own tests use.
	inScope := map[string]bool{}
	for _, p := range lib.pkgs {
		inScope[p.Name] = true
	}
	names := map[string]string{}
	testIdents := map[string]map[string]bool{}
	for _, p := range lib.pkgs {
		name, imports, err := goPackageInfo(srcs[p.Dir])
		if err != nil {
			return nil, reject(ReasonRebuildScope, "%v", err)
		}
		names[p.Dir] = name
		for _, ip := range imports {
			if inScope[ip] && ip != p.Name {
				p.Imports = append(p.Imports, ip)
			}
		}
		testIdents[p.Dir] = goTestIdents(tests[p.Dir], name)
	}
	lib.pkgs = dependencyOrder(lib.pkgs)

	switch opts.Tier {
	case RebuildStubs:
		lib.stubs = map[string][]byte{}
		for _, p := range lib.pkgs {
			stubs, err := goStubPackage(srcs[p.Dir], testIdents[p.Dir])
			if err != nil {
				return nil, reject(ReasonRebuildScope, "stubbing %s: %v", p.Name, err)
			}
			for f, b := range stubs {
				lib.stubs[f] = b
			}
		}
	case RebuildSpec:
		var b strings.Builder
		b.WriteString(apiPreamble(Go, len(lib.pkgs)))
		for _, p := range lib.pkgs {
			sec, err := goAPISection(p, names[p.Dir], srcs[p.Dir], testIdents[p.Dir])
			if err != nil {
				return nil, reject(ReasonRebuildScope, "the API of %s: %v", p.Name, err)
			}
			b.WriteString(sec)
		}
		lib.api = b.String()
	}

	var patterns []string
	for _, p := range lib.pkgs {
		rel := relDir(mod, p.Dir)
		if rel != "." {
			rel = "./" + rel
		}
		patterns = append(patterns, rel)
		lib.protect = append(lib.protect, dirGlob(p.Dir, "*_test.go"), dirGlob(p.Dir, "testdata/**"))
	}
	cwd := ""
	if mod != "." {
		cwd = mod
	}
	lib.command = GoSuiteCommand(cwd, patterns...)
	lib.setup = []string{"go mod download"}
	if mod != "." {
		lib.setup = []string{"cd " + shq(mod) + " && go mod download"}
	}
	lib.manifests = []string{"go.mod", "go.sum", "go.work", "go.work.sum"}
	return lib, nil
}

// goPackageInfo reads the package clause and imports of a package's source files: the package's name is the one most
// of its files declare (a file of another package, such as a generator behind an ignore build constraint, is not
// part of it), and its imports are those of its files.
func goPackageInfo(files map[string][]byte) (string, []string, error) {
	fset := token.NewFileSet()
	count := map[string]int{}
	parsed := map[string]*ast.File{}
	for _, p := range sortedPaths(files) {
		f, err := parser.ParseFile(fset, p, files[p], parser.ImportsOnly)
		if err != nil {
			return "", nil, err
		}
		parsed[p] = f
		count[f.Name.Name]++
	}
	name := ""
	for n, c := range count {
		if name == "" || c > count[name] || (c == count[name] && n < name) {
			name = n
		}
	}
	seen := map[string]bool{}
	var imports []string
	for _, p := range sortedPaths(files) {
		f := parsed[p]
		if f.Name.Name != name {
			continue
		}
		for _, s := range f.Imports {
			ip, err := strconv.Unquote(s.Path.Value)
			if err == nil && !seen[ip] {
				seen[ip] = true
				imports = append(imports, ip)
			}
		}
	}
	sort.Strings(imports)
	return name, imports, nil
}

// goTestIdents is every identifier the package's own test files (package name, not name_test) mention: the
// unexported declarations among them must survive in the stubs and be documented in API.md.
func goTestIdents(tests map[string][]byte, name string) map[string]bool {
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, p := range sortedPaths(tests) {
		f, err := parser.ParseFile(fset, p, tests[p], parser.SkipObjectResolution)
		if err != nil || f.Name.Name != name {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				out[id.Name] = true
			}
			return true
		})
	}
	return out
}

// goStubPackage returns the stub of each source file of a Go package. A stub keeps the file's package clause, its
// build constraints and comments outside function bodies, every constant, variable and type declaration, every
// method, and every function that is exported, that kept code refers to or that the package's tests use (keep); the
// body of each kept function, method and function literal becomes a panic, init functions and the other unexported
// functions are removed, and so are the imports nothing kept uses. The stubs are gofmt-formatted.
func goStubPackage(files map[string][]byte, keep map[string]bool) (map[string][]byte, error) {
	fset := token.NewFileSet()
	parsed := map[string]*ast.File{}
	groups := map[string][]string{}
	for _, p := range sortedPaths(files) {
		f, err := parser.ParseFile(fset, p, files[p], parser.ParseComments)
		if err != nil {
			return nil, err
		}
		parsed[p] = f
		groups[f.Name.Name] = append(groups[f.Name.Name], p)
	}
	out := map[string][]byte{}
	for _, paths := range groups {
		refs := map[string]bool{}
		for k := range keep {
			refs[k] = true
		}
		decls := map[string]bool{}
		for _, p := range paths {
			goKeptRefs(parsed[p], refs)
			for _, d := range parsed[p].Decls {
				goDeclNames(d, decls)
			}
		}
		for _, p := range paths {
			stub, err := goStubFile(fset, parsed[p], files[p], refs, decls)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			out[p] = stub
		}
	}
	return out, nil
}

// goKeptRefs adds to refs every identifier the code a stub keeps mentions: declarations other than functions, and the
// signatures of functions and methods (function literal bodies excluded).
func goKeptRefs(f *ast.File, refs map[string]bool) {
	var idents func(n ast.Node)
	idents = func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				idents(x.Type)
				return false
			case *ast.Ident:
				refs[x.Name] = true
			}
			return true
		})
	}
	for _, d := range f.Decls {
		switch x := d.(type) {
		case *ast.FuncDecl:
			if x.Recv != nil {
				idents(x.Recv)
			}
			idents(x.Type)
		case *ast.GenDecl:
			idents(x)
		}
	}
}

// goDeclNames adds the names a top-level declaration declares (methods excluded).
func goDeclNames(d ast.Decl, names map[string]bool) {
	switch x := d.(type) {
	case *ast.FuncDecl:
		if x.Recv == nil {
			names[x.Name.Name] = true
		}
	case *ast.GenDecl:
		for _, s := range x.Specs {
			switch s := s.(type) {
			case *ast.ValueSpec:
				for _, n := range s.Names {
					names[n.Name] = true
				}
			case *ast.TypeSpec:
				names[s.Name.Name] = true
			}
		}
	}
}

// goDropped reports whether a stub leaves out a function: an init function, or an unexported function nothing kept
// refers to (main stays in package main).
func goDropped(fd *ast.FuncDecl, refs map[string]bool, pkg string) bool {
	if fd.Recv != nil {
		return false
	}
	n := fd.Name.Name
	switch {
	case n == "init":
		return true
	case n == "main" && pkg == "main":
		return false
	case ast.IsExported(n):
		return false
	}
	return !refs[n]
}

// textEdit replaces src[start:end] with text.
type textEdit struct {
	start, end int
	text       string
}

// applyEdits applies non-overlapping edits to src.
func applyEdits(src []byte, edits []textEdit) []byte {
	sort.Slice(edits, func(a, b int) bool { return edits[a].start < edits[b].start })
	var b bytes.Buffer
	at := 0
	for _, e := range edits {
		if e.start < at {
			continue // inside an earlier edit
		}
		b.Write(src[at:e.start])
		b.WriteString(e.text)
		at = e.end
	}
	b.Write(src[at:])
	return b.Bytes()
}

// goStubFile stubs one file of a package (see goStubPackage); decls are the names the package declares.
func goStubFile(fset *token.FileSet, f *ast.File, src []byte, refs, decls map[string]bool) ([]byte, error) {
	tf := fset.File(f.Pos())
	off := func(p token.Pos) int { return tf.Offset(p) }
	var edits []textEdit
	for _, d := range f.Decls {
		switch x := d.(type) {
		case *ast.FuncDecl:
			if goDropped(x, refs, f.Name.Name) {
				start := x.Pos()
				if x.Doc != nil {
					start = x.Doc.Pos()
				}
				edits = append(edits, textEdit{off(start), off(x.End()), ""})
				continue
			}
			if x.Body != nil {
				edits = append(edits, textEdit{off(x.Body.Lbrace), off(x.Body.Rbrace) + 1, goStubBody})
			}
		case *ast.GenDecl:
			ast.Inspect(x, func(n ast.Node) bool {
				if fl, ok := n.(*ast.FuncLit); ok {
					edits = append(edits, textEdit{off(fl.Body.Lbrace), off(fl.Body.Rbrace) + 1, goStubLit})
					return false
				}
				return true
			})
		}
	}
	stub := applyEdits(src, edits)
	stub, err := goPruneImports(f, stub, decls)
	if err != nil {
		return nil, err
	}
	out, err := format.Source(stub)
	if err != nil {
		return nil, fmt.Errorf("the stub does not parse: %w", err)
	}
	return out, nil
}

// goAssumedName is the name a package is assumed to have from its import path, as goimports assumes it: the last
// element, the one before it when that is a major version (v2), without a "go-" prefix and cut at the first
// character that cannot be in an identifier.
func goAssumedName(importPath string) string {
	base := path.Base(importPath)
	if strings.HasPrefix(base, "v") {
		if _, err := strconv.Atoi(base[1:]); err == nil {
			if dir := path.Dir(importPath); dir != "." {
				base = path.Base(dir)
			}
		}
	}
	base = strings.TrimPrefix(base, "go-")
	if i := strings.IndexFunc(base, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }); i >= 0 {
		base = base[:i]
	}
	return base
}

// goImportNames names the imports of the original file orig: an explicit name, else the assumed one when the file
// uses it, else (for a single import whose assumed name the file does not use) the one package name the file uses
// that no other import, and no declaration of the package, accounts for. An import that cannot be named is absent.
func goImportNames(orig *ast.File, decls map[string]bool) map[string]string {
	bases := map[string]bool{} // the identifiers the file qualifies a name with that it does not declare itself
	ast.Inspect(orig, func(n ast.Node) bool {
		if se, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := se.X.(*ast.Ident); ok && id.Obj == nil && !decls[id.Name] {
				bases[id.Name] = true
			}
		}
		return true
	})
	names := map[string]string{}
	claimed := map[string]bool{}
	var unknown []string
	for _, s := range orig.Imports {
		ip, err := strconv.Unquote(s.Path.Value)
		if err != nil {
			continue
		}
		switch {
		case s.Name != nil:
			names[ip] = s.Name.Name
			claimed[s.Name.Name] = true
		case bases[goAssumedName(ip)]:
			names[ip] = goAssumedName(ip)
			claimed[names[ip]] = true
		default:
			unknown = append(unknown, ip)
		}
	}
	if len(unknown) == 1 {
		var left []string
		for b := range bases {
			if !claimed[b] {
				left = append(left, b)
			}
		}
		if len(left) == 1 {
			names[unknown[0]] = left[0]
		}
	}
	return names
}

// goPruneImports removes from stub (the stubbed text of orig) the imports nothing in it uses. Blank and dot imports,
// cgo's "C" and imports whose name cannot be told are kept.
func goPruneImports(orig *ast.File, stub []byte, decls map[string]bool) ([]byte, error) {
	names := goImportNames(orig, decls)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", stub, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("the stub does not parse: %w", err)
	}
	used := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if se, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := se.X.(*ast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	tf := fset.File(f.Pos())
	off := func(p token.Pos) int { return tf.Offset(p) }
	var edits []textEdit
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		var drop []*ast.ImportSpec
		for _, s := range gd.Specs {
			is := s.(*ast.ImportSpec)
			ip, err := strconv.Unquote(is.Path.Value)
			if err != nil || ip == "C" {
				continue
			}
			name, known := names[ip]
			if is.Name != nil {
				name, known = is.Name.Name, true
			}
			if !known || name == "_" || name == "." || used[name] {
				continue
			}
			drop = append(drop, is)
		}
		if len(drop) == 0 {
			continue
		}
		if len(drop) == len(gd.Specs) {
			start := gd.Pos()
			if gd.Doc != nil {
				start = gd.Doc.Pos()
			}
			edits = append(edits, textEdit{off(start), off(gd.End()), ""})
			continue
		}
		for _, is := range drop {
			start, end := is.Pos(), is.End()
			if is.Doc != nil {
				start = is.Doc.Pos()
			}
			if is.Comment != nil {
				end = is.Comment.End()
			}
			edits = append(edits, textEdit{off(start), off(end), ""})
		}
	}
	return applyEdits(stub, edits), nil
}

// goAPISection documents one package for API.md: its import path and directory, its package documentation, and every
// exported declaration with its documentation, as go doc shows them (function bodies and unexported fields left
// out), followed by the unexported declarations its own tests use (uses).
func goAPISection(p *rebuildPkg, name string, files map[string][]byte, uses map[string]bool) (string, error) {
	fset := token.NewFileSet()
	var astFiles []*ast.File
	for _, fp := range sortedPaths(files) {
		f, err := parser.ParseFile(fset, fp, files[fp], parser.ParseComments)
		if err != nil {
			return "", err
		}
		if f.Name.Name == name {
			astFiles = append(astFiles, f)
		}
	}
	// The unexported declarations the tests use come from a parse doc has not filtered.
	internal, err := goTestUsedDecls(files, name, uses)
	if err != nil {
		return "", err
	}
	dp, err := doc.NewFromFiles(fset, astFiles, p.Name)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Package `%s`\n\nDirectory `%s`, package `%s`.\n\n", p.Name, dirLabel(p.Dir), dp.Name)
	if strings.TrimSpace(dp.Doc) != "" {
		b.Write(dp.Markdown(dp.Doc))
		b.WriteString("\n")
	}
	var werr error
	write := func(node ast.Node, text string) {
		code, err := goPrintDecl(fset, node)
		if err != nil {
			werr = err
			return
		}
		b.WriteString("```go\n" + code + "\n```\n\n")
		if strings.TrimSpace(text) != "" {
			b.Write(dp.Markdown(text))
			b.WriteString("\n")
		}
	}
	values := func(vs []*doc.Value) {
		for _, v := range vs {
			write(v.Decl, v.Doc)
		}
	}
	funcs := func(fs []*doc.Func) {
		for _, f := range fs {
			write(f.Decl, f.Doc)
		}
	}
	values(dp.Consts)
	values(dp.Vars)
	funcs(dp.Funcs)
	for _, t := range dp.Types {
		write(t.Decl, t.Doc)
		values(t.Consts)
		values(t.Vars)
		funcs(t.Funcs)
		funcs(t.Methods)
	}
	if werr != nil {
		return "", werr
	}
	if internal != "" {
		b.WriteString("### Unexported declarations the package's tests use\n\n" + internal)
	}
	return b.String(), nil
}

// goTestUsedDecls prints the unexported declarations of a package that its tests use (uses): functions and methods
// as signatures, types in full (also an exported type with an unexported field the tests use), constants and
// variables as declared.
func goTestUsedDecls(files map[string][]byte, name string, uses map[string]bool) (string, error) {
	if len(uses) == 0 {
		return "", nil
	}
	fset := token.NewFileSet()
	var b strings.Builder
	for _, fp := range sortedPaths(files) {
		f, err := parser.ParseFile(fset, fp, files[fp], parser.ParseComments)
		if err != nil {
			return "", err
		}
		if f.Name.Name != name {
			continue
		}
		for _, d := range f.Decls {
			var node ast.Node
			switch x := d.(type) {
			case *ast.FuncDecl:
				if !ast.IsExported(x.Name.Name) && uses[x.Name.Name] && x.Name.Name != "init" {
					node = x
				}
			case *ast.GenDecl:
				var specs []ast.Spec
				for _, s := range x.Specs {
					if goSpecUsed(s, uses) {
						specs = append(specs, s)
					}
				}
				if len(specs) > 0 {
					c := *x
					c.Specs = specs
					if len(specs) == 1 && x.Tok != token.IMPORT {
						c.Lparen, c.Rparen = token.NoPos, token.NoPos
					}
					node = &c
				}
			}
			if node == nil {
				continue
			}
			code, err := goPrintDecl(fset, node)
			if err != nil {
				return "", err
			}
			b.WriteString("```go\n" + code + "\n```\n\n")
		}
	}
	return b.String(), nil
}

// goSpecUsed reports whether a declaration spec declares an unexported name in uses, or is a type with an
// unexported field or method in uses.
func goSpecUsed(s ast.Spec, uses map[string]bool) bool {
	switch s := s.(type) {
	case *ast.ValueSpec:
		for _, n := range s.Names {
			if !ast.IsExported(n.Name) && uses[n.Name] {
				return true
			}
		}
	case *ast.TypeSpec:
		if !ast.IsExported(s.Name.Name) && uses[s.Name.Name] {
			return true
		}
		var fields *ast.FieldList
		switch t := s.Type.(type) {
		case *ast.StructType:
			fields = t.Fields
		case *ast.InterfaceType:
			fields = t.Methods
		}
		if fields != nil {
			for _, fl := range fields.List {
				for _, n := range fl.Names {
					if !ast.IsExported(n.Name) && uses[n.Name] {
						return true
					}
				}
			}
		}
	}
	return false
}

// goPrintDecl prints a declaration for API.md as go doc does: a function as its signature, a function literal in a
// variable's value with an empty body, with the comments of the specs, fields and methods it holds (and a note where
// fields were filtered out) and without its own doc comment, which API.md shows as text.
func goPrintDecl(fset *token.FileSet, node ast.Node) (string, error) {
	switch x := node.(type) {
	case *ast.FuncDecl:
		c := *x
		c.Body, c.Doc = nil, nil
		node = &c
	case *ast.GenDecl:
		c := *x
		c.Doc = nil
		node = &c
	}
	ast.Inspect(node, func(n ast.Node) bool {
		if fl, ok := n.(*ast.FuncLit); ok {
			fl.Body = &ast.BlockStmt{Lbrace: fl.Body.Lbrace, Rbrace: fl.Body.Lbrace + 1}
			return false
		}
		return true
	})
	// A node printed without the file's comments carries the comments attached to it (doc and line comments of specs
	// and fields).
	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := cfg.Fprint(&buf, fset, node); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// dirLabel names a repository-relative directory in prose: "./" for the root, "dir/" otherwise.
func dirLabel(d string) string {
	if d == "." || d == "" {
		return "./"
	}
	return d + "/"
}

// dirGlob is a protected-path glob for pattern inside the repository-relative directory dir, anchored at the
// repository root.
func dirGlob(dir, pattern string) string {
	if dir == "." || dir == "" {
		return "/" + pattern
	}
	return strings.TrimPrefix(escapeGlob(dir), "/") + "/" + pattern
}

// countLOC counts the lines of src that are neither blank nor only a comment introduced by comment.
func countLOC(src []byte, comment string) int {
	n := 0
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, comment) {
			n++
		}
	}
	return n
}
