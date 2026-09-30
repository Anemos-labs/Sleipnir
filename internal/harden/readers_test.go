package harden

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// MoveKeys takes the provider keys out of the environment, which only works if nothing reads them
// with os.Getenv any more: a reader that does would see an empty variable and report "no key" for a
// key that is set. This test reads the production source and holds the line: an os.Getenv or
// os.LookupEnv whose argument names a credential (or is an expression that could) is an error.
// Read credentials with harden.Secret; a variable that is not one goes on the list below with the
// reason.

// credentialName is what makes a literal variable name a credential.
var credentialName = regexp.MustCompile(`(?i)(api[_-]?key|secret|token|passw|credential|auth|cookie|_key$|^key$)`)

// notCredentials lists the reads whose argument is not a literal name, by file and the source text
// of the argument, with why each is safe. The literal names that are read plainly are checked by
// name instead (credentialName).
var notCredentials = map[string]string{
	"cmd/sleipnir/provider.go:v":                        "<PROVIDER>_BASE_URL: a URL the provider's key may be sent to, judged by provider.CheckEndpoint",
	"internal/session/providers.go:envBaseURLVar(name)": "<PROVIDER>_BASE_URL, as above",
	"internal/tools/web/fetch.go:k":                     "HTTPS_PROXY and friends: never moved (LooksSecret exempts them, and net/http reads them itself)",
}

// testSupport is code that lives outside _test.go files so that several test packages can use it,
// and is never linked into the binary.
var testSupport = []string{"internal/mcp/mcptest/"}

func TestReadersOfCredentialsUseSecret(t *testing.T) {
	root := filepath.Join("..", "..")
	var problems []string
	used := map[string]bool{}
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/harden/") {
				return nil // this package is the reader
			}
			for _, p := range testSupport {
				if strings.HasPrefix(rel, p) {
					return nil
				}
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			f, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				return nil
			}
			consts := stringConsts(f)
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv") {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "os" {
					return true
				}
				arg := call.Args[0]
				pos := fset.Position(call.Pos())
				text := string(src[fset.Position(arg.Pos()).Offset:fset.Position(arg.End()).Offset])
				name, literal := "", false
				switch a := arg.(type) {
				case *ast.BasicLit:
					if v, err := strconv.Unquote(a.Value); err == nil {
						name, literal = v, true
					}
				case *ast.Ident:
					if v, ok := consts[a.Name]; ok {
						name, literal = v, true
					}
				}
				switch {
				case literal && credentialName.MatchString(name):
					problems = append(problems, rel+":"+strconv.Itoa(pos.Line)+": os."+sel.Sel.Name+"("+text+") reads a credential; use harden.Secret")
				case literal:
					// an ordinary variable
				default:
					key := rel + ":" + text
					if _, ok := notCredentials[key]; ok {
						used[key] = true
						return true
					}
					problems = append(problems, rel+":"+strconv.Itoa(pos.Line)+": os."+sel.Sel.Name+"("+text+") reads a variable whose name is not known here; if it can be a credential use harden.Secret, otherwise add \""+key+"\" to notCredentials with the reason")
				}
				return true
			})
			return nil
		})
	}
	for k := range notCredentials {
		if !used[k] {
			problems = append(problems, "notCredentials lists "+k+", which no longer exists: remove it")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// stringConsts collects the file's package-level string constants.
func stringConsts(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i < len(vs.Values) {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, err := strconv.Unquote(lit.Value); err == nil {
							out[name.Name] = v
						}
					}
				}
			}
		}
	}
	return out
}
