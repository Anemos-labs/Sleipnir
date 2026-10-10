package web

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The shipped page is wired to the live API: the mock's sample data, its simulation and its test hooks stay under internal/web/uidev
// (outside the embed) and must not be reachable from index.html. These tests fail when one of those files is embedded again, when
// index.html loads one, when a shipped script touches the sample pack or the simulation, or when a sentence of the sample data
// shows up in a shipped file.

// mockOnly are the files of the mock that only exist for its simulation and sample data (UI-WIRING.md 9).
var mockOnly = []string{"data.js", "outputs.js", "cli-spec.js", "10-fixtures.js", "11-data-adapter.js", "40-scripts.js", "95-kit.js", "zz-test-hooks.js", "hooks-real.js"}

func TestSampleDataAndTheSimulationAreNotShipped(t *testing.T) {
	files := embeddedFiles(t)
	html := files["index.html"]
	if html == "" {
		t.Fatal("no index.html is embedded")
	}
	for name := range files {
		for _, m := range mockOnly {
			if path.Base(name) == m {
				t.Errorf("%s is embedded: the mock-only files live in internal/web/uidev", name)
			}
		}
	}
	scripts := regexp.MustCompile(`(?i)<script\b[^>]*\bsrc\s*=\s*["']([^"']+)["']`).FindAllStringSubmatch(html, -1)
	if len(scripts) == 0 {
		t.Fatal("index.html loads no script")
	}
	loaded := map[string]bool{}
	for _, s := range scripts {
		loaded[path.Base(s[1])] = true
		for _, m := range mockOnly {
			if path.Base(s[1]) == m {
				t.Errorf("index.html loads %s", s[1])
			}
		}
	}
	for _, need := range []string{"api.js", "11-data-live.js", "live.js", "99-app.js"} {
		if !loaded[need] {
			t.Errorf("index.html does not load %s: the page would not reach the server", need)
		}
	}
	if strings.Contains(html, "mockchip") || strings.Contains(html, "MOCK · sample data") {
		t.Error("index.html still shows the MOCK chip (D-07)")
	}
	reach := regexp.MustCompile(`\bSL\.FX\b|\bSLDATA\b|\bSLCLISPEC\b|\bSL\.scripts\b|window\.__SL\b|\bSL\.test\s*=`)
	for name, src := range files {
		if path.Ext(name) != ".js" && path.Ext(name) != ".html" {
			continue
		}
		if loc := reach.FindStringIndex(src); loc != nil {
			t.Errorf("%s:%d reaches the sample pack, the simulation or the test hooks: %q", name, strings.Count(src[:loc[0]], "\n")+1, src[loc[0]:loc[1]])
		}
	}
}

// literals returns the string literals of at least n characters in a JavaScript source (quotes of the three kinds, escapes kept).
func literals(src string, n int) []string {
	var out []string
	for _, re := range []*regexp.Regexp{regexp.MustCompile(`'((?:[^'\\\n]|\\.)*)'`), regexp.MustCompile(`"((?:[^"\\\n]|\\.)*)"`), regexp.MustCompile("`((?:[^`\\\\]|\\\\.)*)`")} {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			if s := strings.TrimSpace(m[1]); len(s) >= n && !strings.ContainsAny(s, "<>{}") {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestNoSentenceOfTheSampleDataIsShipped(t *testing.T) {
	dir := filepath.Join("uidev", "mock", "js")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("internal/web/uidev/mock/js is not part of this checkout")
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	sample := map[string]bool{}
	for _, f := range []string{"10-fixtures.js", "40-scripts.js", "data.js", "outputs.js"} {
		for _, s := range literals(read(filepath.Join(dir, f)), 20) {
			sample[s] = true
		}
	}
	// The UI's own copy: what the mock's kept modules and its shell markup already said (any text of them), and the four tables
	// 11-data-live.js keeps as the page's copy (the modes, the built-in roles, the layer notes and the keys list).
	var own strings.Builder
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	pack := map[string]bool{"10-fixtures.js": true, "40-scripts.js": true, "data.js": true, "outputs.js": true, "cli-spec.js": true, "11-data-adapter.js": true, "95-kit.js": true, "zz-test-hooks.js": true}
	for _, e := range entries {
		if !pack[e.Name()] {
			own.WriteString(read(filepath.Join(dir, e.Name())))
		}
	}
	own.WriteString(read(filepath.Join("uidev", "mock", "index-mock.html")))
	files := embeddedFiles(t)
	live := files["js/11-data-live.js"]
	for _, table := range []string{"MODES", "ROLES", "LAYERS", "SHORTCUTS"} {
		m := regexp.MustCompile(`(?s)const ` + table + ` = [\[{](.*?)\n  [\]}];`).FindStringSubmatch(live)
		if m == nil {
			t.Fatalf("11-data-live.js has no %s table", table)
		}
		own.WriteString(m[1])
	}
	ownText := own.String()
	n := 0
	for s := range sample {
		if strings.Contains(ownText, s) {
			continue
		}
		n++
		for name, src := range files {
			if strings.Contains(src, s) {
				t.Errorf("%s ships a sentence of the sample data: %.80q", name, s)
			}
		}
	}
	if n < 100 {
		t.Fatalf("only %d sample sentences to look for: the scan is not looking at the sample data", n)
	}
}
