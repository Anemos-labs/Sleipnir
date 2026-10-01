package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// optionKeysRead returns, per function of providers.go, the provider option keys it
// reads: optBool(p.Options, "k") and friends, and p.Options["k"].
func optionKeysRead(t *testing.T) map[string]map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "providers.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	isOptions := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Options"
	}
	str := func(e ast.Expr) (string, bool) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}
	readers := map[string]bool{"optBool": true, "optString": true, "optInt": true, "optSeconds": true, "optStrings": true}
	out := map[string]map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		keys := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && readers[id.Name] && len(x.Args) == 2 && isOptions(x.Args[0]) {
					if k, ok := str(x.Args[1]); ok {
						keys[k] = true
					}
				}
			case *ast.IndexExpr:
				if isOptions(x.X) {
					if k, ok := str(x.Index); ok {
						keys[k] = true
					}
				}
			}
			return true
		})
		if len(keys) > 0 {
			out[fn.Name.Name] = keys
		}
	}
	return out
}

func keyList(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The list of options the configuration validator accepts (config.ProviderOptionNames)
// and the options the client builders read must be the same set: an option that is
// read but not listed would be reported as unknown to the person who set it, and one
// that is listed but not read would be accepted and ignored.
func TestProviderOptionsMatchTheBuilders(t *testing.T) {
	read := optionKeysRead(t)
	for _, fn := range []string{"buildChat", "buildAnthropic", "BuildProvider"} {
		if len(read[fn]) == 0 {
			var have []string
			for name := range read {
				have = append(have, name)
			}
			sort.Strings(have)
			t.Fatalf("found no option reads in %s: the scan needs updating (functions with reads: %v)", fn, have)
		}
	}
	for dialect, builder := range map[string]string{config.DialectOpenAIChat: "buildChat", config.DialectAnthropic: "buildAnthropic"} {
		got := map[string]bool{}
		for k := range read[builder] {
			got[k] = true
		}
		for k := range read["BuildProvider"] { // read for every dialect
			got[k] = true
		}
		want := config.ProviderOptionNames(dialect)
		if strings.Join(keyList(got), ",") != strings.Join(want, ",") {
			t.Errorf("dialect %s: the builders read %v\nbut internal/config/options.go lists %v", dialect, keyList(got), want)
		}
	}
}

// What the built-in providers carry must pass the validator, or every session on
// them would start with a warning about the harness's own defaults.
func TestBuiltinProviderOptionsAreValid(t *testing.T) {
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{}
	for name, p := range builtinProviders {
		cfg.Providers[name] = p
	}
	for _, is := range cfg.Validate() {
		if strings.Contains(is.Path, ".options") {
			t.Errorf("a built-in provider's options draw an issue: %s: %s", is.Path, is.Message)
		}
	}
}
