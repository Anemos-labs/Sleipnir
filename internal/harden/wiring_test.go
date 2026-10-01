package harden

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

// The hardening only helps if the binary asks for it before it does anything else: a key read, a
// command started or a goroutine spawned ahead of harden.Process is exposed. The call lives in
// cmd/sleipnir/main.go, which no test of this package exercises, so this one reads the source: the
// first statement of main must be a call to harden.Process.
func TestMainCallsProcessFirst(t *testing.T) {
	path := filepath.Join("..", "..", "cmd", "sleipnir", "main.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Skipf("cannot read the command's source: %v", err)
	}
	name := ""
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != "github.com/anemos-labs/sleipnir/internal/harden" {
			continue
		}
		name = "harden"
		if imp.Name != nil {
			name = imp.Name.Name
		}
	}
	if name == "" {
		t.Fatal("cmd/sleipnir/main.go does not import internal/harden")
	}
	var main *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "main" && fd.Recv == nil {
			main = fd
		}
	}
	if main == nil || main.Body == nil || len(main.Body.List) == 0 {
		t.Fatal("cmd/sleipnir/main.go has no func main with a body")
	}
	stmt, ok := main.Body.List[0].(*ast.ExprStmt)
	if !ok {
		t.Fatalf("the first statement of main is %T, want a call to harden.Process", main.Body.List[0])
	}
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok {
		t.Fatalf("the first statement of main is %T, want a call to harden.Process", stmt.X)
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		t.Fatalf("the first statement of main calls %T, want harden.Process", call.Fun)
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != name || sel.Sel.Name != "Process" {
		t.Fatalf("the first statement of main is not %s.Process(...)", name)
	}
}
