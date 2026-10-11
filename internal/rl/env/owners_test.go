package env

import (
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Ownership matches with Verifier.Protected's globs and reports every owning role,
// sorted; a path nobody owns has no owners.
func TestOwnershipNamesTheRolesThatOwnAPath(t *testing.T) {
	o, err := CompileOwnership(map[string][]string{
		"frontend": {"*.css", "/web/"},
		"docs":     {"docs/**", "*.css"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"web/app.js", []string{"frontend"}},
		{"web/deep/x.go", []string{"frontend"}},
		{"WEB/App.JS", []string{"frontend"}}, // case is ignored, as for protected paths
		{"internal/web/x.go", nil},           // "/web/" is anchored at the root
		{"src/theme.css", []string{"docs", "frontend"}},
		{"docs/guide.md", []string{"docs"}},
		{"main.go", nil},
		{"", nil},
	} {
		if got := o.OwnersOf(tc.path); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("OwnersOf(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	if got := o.Roles(); !reflect.DeepEqual(got, []string{"docs", "frontend"}) {
		t.Errorf("Roles() = %v", got)
	}

	if o, err := CompileOwnership(nil); o != nil || err != nil {
		t.Errorf("no owns: %v, %v; want nil, nil", o, err)
	}
	var none *Ownership
	if none.OwnersOf("web/app.css") != nil || none.Roles() != nil {
		t.Error("a nil Ownership owns something")
	}
	if _, err := CompileOwnership(map[string][]string{"frontend": {"web/[a"}}); err == nil || !strings.Contains(err.Error(), `team.owns["frontend"]`) {
		t.Errorf("bad glob: %v", err)
	}
}

// The globs task generators give the frontend role cover the paths rl.IsUIPath calls
// UI work, and no other path of these.
func TestUIGlobsCoverWhatIsUIPathAccepts(t *testing.T) {
	m, err := CompileGlobs(rl.UIGlobs())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"index.html", "docs/page.htm", "src/app.css", "src/theme.scss", "src/theme.sass", "src/theme.less",
		"src/App.jsx", "src/App.tsx", "src/App.vue", "src/App.svelte", "src/pages/index.astro", "SRC/INDEX.HTML",
		"templates/base.txt", "static/logo.png", "public/robots.txt", "internal/web/server.go", "pkg/ui/model.go",
		"frontend/package.json", "src/components/button.js", "styles/tokens.json", "src/Components/Button.js",
		`src\components\button.js`, "./web/main.go",
		"main.go", "cmd/web.go", "internal/webhook/x.go", "src/app.js", "src/app.ts", "README.md", "styles",
		"src/html/parser.go", "data.json",
	} {
		if got, want := m.Match(p), rl.IsUIPath(p); got != want {
			t.Errorf("%q: the UI globs match = %v, IsUIPath = %v", p, got, want)
		}
	}
}
