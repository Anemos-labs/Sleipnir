package rl

import "testing"

func TestIsUIPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		// UI extensions, anywhere.
		{"index.html", true},
		{"docs/page.htm", true},
		{"src/app.css", true},
		{"src/theme.scss", true},
		{"src/theme.sass", true},
		{"src/theme.less", true},
		{"src/App.jsx", true},
		{"src/App.tsx", true},
		{"src/App.vue", true},
		{"src/App.svelte", true},
		{"src/pages/index.astro", true},
		{"SRC/INDEX.HTML", true}, // case is ignored
		// UI directories, as directory components only.
		{"templates/base.txt", true},
		{"static/logo.png", true},
		{"public/robots.txt", true},
		{"internal/web/server.go", true},
		{"pkg/ui/model.go", true},
		{"frontend/package.json", true},
		{"src/components/button.js", true},
		{"styles/tokens.json", true},
		{"src/Components/Button.js", true},
		{`src\components\button.js`, true}, // Windows separators
		{"/abs/repo/static/app.js", true},
		{"./web/main.go", true},
		// Not UI work.
		{"", false},
		{"  ", false},
		{"main.go", false},
		{"cmd/web.go", false}, // a file named web, not a directory
		{"internal/webhook/x.go", false},
		{"src/app.js", false},
		{"src/app.ts", false},
		{"README.md", false},
		{"styles", false},             // a file called styles
		{"src/html/parser.go", false}, // html is not a UI directory name
		{"data.json", false},
	} {
		if got := IsUIPath(tc.path); got != tc.want {
			t.Errorf("IsUIPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
