package taskgen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
)

// Mutation is one small, syntactically valid change to a source file that is
// likely to change behaviour: the seed of a "find the bug" task.
type Mutation struct {
	File       string `json:"file"`
	Op         string `json:"op"`
	Line       int    `json:"line"`
	Start, End int    `json:"-"` // byte range replaced in the original file
	Old, New   string `json:"-"`
}

// MutationInfo is the part of a Mutation recorded in Task.Meta.
type MutationInfo struct {
	File string `json:"file"`
	Op   string `json:"op"`
	Line int    `json:"line"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

func (m Mutation) info() *MutationInfo {
	return &MutationInfo{File: m.File, Op: m.Op, Line: m.Line, Old: m.Old, New: m.New}
}

// Apply returns src with the mutation applied. It refuses when the text at the
// recorded position is not what the mutation expects.
func (m Mutation) Apply(src []byte) ([]byte, error) {
	if m.Start < 0 || m.End < m.Start || m.End > len(src) {
		return nil, fmt.Errorf("mutation range %d..%d is outside the file", m.Start, m.End)
	}
	if string(src[m.Start:m.End]) != m.Old {
		return nil, fmt.Errorf("file does not contain %q at offset %d", m.Old, m.Start)
	}
	out := make([]byte, 0, len(src)+len(m.New))
	out = append(out, src[:m.Start]...)
	out = append(out, m.New...)
	out = append(out, src[m.End:]...)
	return out, nil
}

var goCmpFlip = map[token.Token]string{
	token.LSS: "<=", token.LEQ: "<", token.GTR: ">=", token.GEQ: ">", token.EQL: "!=", token.NEQ: "==",
}

// GoMutations lists the mutations available in one Go file: comparison
// operators flipped, +1/-1 boundaries swapped, && and || exchanged, nil checks
// dropped, conditions negated and boolean returns inverted. Positions come from
// the AST, so nothing inside a string or comment is ever touched. Generated
// files yield none. The result is sorted by position.
func GoMutations(file string, src []byte) ([]Mutation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "Code generated") && strings.Contains(c.Text, "DO NOT EDIT") {
				return nil, nil
			}
		}
	}
	var out []Mutation
	add := func(op string, start, end token.Pos, repl string) {
		s, e := fset.Position(start), fset.Position(end)
		if s.Line != e.Line || s.Offset < 0 || e.Offset > len(src) || e.Offset < s.Offset {
			return // keep every mutation on one line so that the patch stays tiny
		}
		out = append(out, Mutation{File: file, Op: op, Line: s.Line, Start: s.Offset, End: e.Offset, Old: string(src[s.Offset:e.Offset]), New: repl})
	}
	isNil := func(e ast.Expr) bool { id, ok := e.(*ast.Ident); return ok && id.Name == "nil" }
	isOne := func(e ast.Expr) bool { l, ok := e.(*ast.BasicLit); return ok && l.Kind == token.INT && l.Value == "1" }

	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BinaryExpr:
			opEnd := x.OpPos + token.Pos(len(x.Op.String()))
			if repl, ok := goCmpFlip[x.Op]; ok {
				add("cmp-flip", x.OpPos, opEnd, repl)
			}
			switch x.Op {
			case token.ADD:
				if isOne(x.X) || isOne(x.Y) {
					add("boundary", x.OpPos, opEnd, "-")
				}
			case token.SUB:
				if isOne(x.Y) {
					add("boundary", x.OpPos, opEnd, "+")
				}
			case token.LAND:
				add("logic-swap", x.OpPos, opEnd, "||")
			case token.LOR:
				add("logic-swap", x.OpPos, opEnd, "&&")
			}
		case *ast.IfStmt:
			if be, ok := x.Cond.(*ast.BinaryExpr); ok && (be.Op == token.NEQ || be.Op == token.EQL) && (isNil(be.X) || isNil(be.Y)) {
				repl := "false" // `if x == nil { handle }`: the check is gone
				if be.Op == token.NEQ {
					repl = "true" // `if x != nil { use(x) }`: always taken
				}
				add("nil-check", x.Cond.Pos(), x.Cond.End(), repl)
			} else if _, isNot := x.Cond.(*ast.UnaryExpr); !isNot {
				text := string(src[fset.Position(x.Cond.Pos()).Offset:fset.Position(x.Cond.End()).Offset])
				add("negate", x.Cond.Pos(), x.Cond.End(), "!("+text+")")
			}
		case *ast.UnaryExpr:
			if x.Op == token.NOT {
				add("negate", x.OpPos, x.OpPos+1, "")
			}
		case *ast.ReturnStmt:
			if len(x.Results) == 1 {
				if id, ok := x.Results[0].(*ast.Ident); ok && (id.Name == "true" || id.Name == "false") {
					repl := "true"
					if id.Name == "true" {
						repl = "false"
					}
					add("return-bool", id.Pos(), id.End(), repl)
				}
			}
		}
		return true
	})
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Start != out[b].Start {
			return out[a].Start < out[b].Start
		}
		return out[a].Op < out[b].Op
	})
	// The same range with the same replacement can arise twice (a condition that
	// is a lone comparison is both "cmp-flip" and "negate" of different spans, but
	// two ops may coincide); keep the first.
	seen := map[string]bool{}
	dedup := out[:0]
	for _, m := range out {
		k := fmt.Sprintf("%d-%d-%s", m.Start, m.End, m.New)
		if !seen[k] {
			seen[k] = true
			dedup = append(dedup, m)
		}
	}
	return dedup, nil
}

// goPackageDirHasTests reports whether any of the repository files listed in
// files is a Go test in dir.
func dirHasSuffixFile(files []string, dir, suffix string) bool {
	prefix := dir + "/"
	if dir == "." {
		prefix = ""
	}
	for _, f := range files {
		if strings.HasPrefix(f, prefix) && strings.HasSuffix(f, suffix) && !strings.Contains(strings.TrimPrefix(f, prefix), "/") {
			return true
		}
	}
	return false
}

var _ = bytes.Equal
