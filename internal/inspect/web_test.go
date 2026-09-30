package inspect

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// The UI is a set of files served from the binary under a strict Content
// Security Policy. These tests read the embedded sources and hold the line the
// policy alone cannot: no way to build markup from a string, no inline script,
// style or handler, and nothing fetched from anywhere but this server.

func assets(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(webFS, "web", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := webFS.ReadFile(p)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(p, "web/")] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 10 {
		t.Fatalf("only %d embedded assets: %v", len(out), keys(out))
	}
	return out
}

func keys(m map[string]string) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	return k
}

func TestIndexHTMLHasNoInlineScriptStyleOrHandlers(t *testing.T) {
	a := assets(t)
	html := a["index.html"]
	scripts := regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`).FindAllStringSubmatch(html, -1)
	if len(scripts) != 1 {
		t.Fatalf("index.html has %d script elements, want the one module", len(scripts))
	}
	for _, s := range scripts {
		if !regexp.MustCompile(`\bsrc="/js/app\.js"`).MatchString(s[1]) || strings.TrimSpace(s[2]) != "" {
			t.Errorf("script must be an external file with no inline body: %q", s[0])
		}
	}
	for name, pat := range map[string]string{
		"inline <style>":            `(?i)<style\b`,
		"style attribute":           `(?i)\sstyle\s*=`,
		"inline event handler":      `(?i)\son[a-z]+\s*=`,
		"javascript: URL":           `(?i)javascript:`,
		"data: URL":                 `(?i)\b(src|href)\s*=\s*["']data:`,
		"remote URL":                `(?i)(src|href|action)\s*=\s*["'](https?:)?//`,
		"<base>":                    `(?i)<base\b`,
		"<iframe>/<object>/<embed>": `(?i)<(iframe|object|embed)\b`,
		"<form>":                    `(?i)<form\b`,
	} {
		if regexp.MustCompile(pat).MatchString(html) {
			t.Errorf("index.html contains %s", name)
		}
	}
	if !strings.Contains(html, `name="viewport"`) || !strings.Contains(html, "<title>") || !strings.Contains(html, `lang="en"`) {
		t.Error("index.html needs a viewport (phone width), a title and a language")
	}
	if !strings.Contains(html, `rel="stylesheet" href="/app.css"`) {
		t.Error("the stylesheet must be an external file")
	}
	if !regexp.MustCompile(`(?i)<meta name="color-scheme" content="light dark">`).MatchString(html) {
		t.Error("index.html must declare light and dark support")
	}
}

func TestJavaScriptCannotBuildMarkupFromStrings(t *testing.T) {
	a := assets(t)
	forbidden := map[string]*regexp.Regexp{
		"innerHTML":                regexp.MustCompile(`innerHTML`),
		"outerHTML":                regexp.MustCompile(`outerHTML`),
		"insertAdjacentHTML":       regexp.MustCompile(`insertAdjacentHTML`),
		"document.write":           regexp.MustCompile(`document\s*\.\s*write`),
		"eval":                     regexp.MustCompile(`\beval\s*\(`),
		"new Function":             regexp.MustCompile(`new\s+Function\b`),
		"Function constructor":     regexp.MustCompile(`\bFunction\s*\(`),
		"setTimeout(string)":       regexp.MustCompile(`setTimeout\s*\(\s*['"` + "`" + `]`),
		"setInterval(string)":      regexp.MustCompile(`setInterval\s*\(\s*['"` + "`" + `]`),
		"inline style attr":        regexp.MustCompile(`setAttribute\(\s*['"]style['"]`),
		"cssText":                  regexp.MustCompile(`\.cssText\b`),
		"srcdoc":                   regexp.MustCompile(`srcdoc`),
		"createContextualFragment": regexp.MustCompile(`createContextualFragment`),
		"DOMParser":                regexp.MustCompile(`DOMParser`),
		"javascript: URL":          regexp.MustCompile(`javascript:`),
		"import()":                 regexp.MustCompile(`\bimport\s*\(`),
		"Worker":                   regexp.MustCompile(`new\s+(Shared)?Worker\b`),
		"WebSocket":                regexp.MustCompile(`new\s+WebSocket\b`),
		"XMLHttpRequest":           regexp.MustCompile(`XMLHttpRequest`),
		"sendBeacon":               regexp.MustCompile(`sendBeacon`),
		"postMessage":              regexp.MustCompile(`postMessage`),
		"window.open":              regexp.MustCompile(`window\s*\.\s*open\b`),
		"location assignment":      regexp.MustCompile(`\blocation\s*(\.\s*(href|assign|replace))?\s*=[^=]`),
	}
	for name, src := range a {
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		for what, re := range forbidden {
			if loc := re.FindStringIndex(src); loc != nil {
				line := strings.Count(src[:loc[0]], "\n") + 1
				t.Errorf("%s:%d uses %s", name, line, what)
			}
		}
	}
	// The one place a hash is written is the router, through history or location.hash.
	if !strings.Contains(a["js/state.js"], "history.pushState") {
		t.Error("state.js should navigate with history.pushState/replaceState")
	}
}

func TestNothingIsFetchedFromAnotherOrigin(t *testing.T) {
	a := assets(t)
	remote := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`)
	for name, src := range a {
		for _, m := range remote.FindAllString(src, -1) {
			// The SVG namespace is an identifier, not a request.
			if strings.Contains(m, "www.w3.org") {
				continue
			}
			t.Errorf("%s mentions the remote address %q: the inspector must work offline", name, m)
		}
	}
	css := a["app.css"]
	for _, bad := range []string{"@import", "@font-face", "expression(", "behavior:"} {
		if strings.Contains(css, bad) {
			t.Errorf("app.css contains %s", bad)
		}
	}
	for _, m := range regexp.MustCompile(`url\(([^)]*)\)`).FindAllStringSubmatch(css, -1) {
		if !strings.HasPrefix(strings.Trim(m[1], `"' `), "#") {
			t.Errorf("app.css url(%s): only fragment references (SVG patterns) are allowed", m[1])
		}
	}
	// fetch() goes to this server's API only.
	for name, src := range a {
		for _, m := range regexp.MustCompile(`fetch\(\s*([^,)]*)`).FindAllStringSubmatch(src, -1) {
			if !strings.Contains(m[1], "'/api/'") {
				t.Errorf("%s fetches %s: only /api/ is allowed", name, m[1])
			}
		}
	}
}

func TestEveryModuleImportResolvesInsideTheBinary(t *testing.T) {
	a := assets(t)
	imp := regexp.MustCompile(`(?m)^\s*import\s+(?:[^'"]*?from\s+)?['"]([^'"]+)['"]`)
	for name, src := range a {
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		for _, m := range imp.FindAllStringSubmatch(src, -1) {
			target := m[1]
			if !strings.HasPrefix(target, "./") && !strings.HasPrefix(target, "../") {
				t.Errorf("%s imports %q: only relative modules from this binary are allowed", name, target)
				continue
			}
			p := path.Clean(path.Join(path.Dir(name), target))
			if _, ok := a[p]; !ok {
				t.Errorf("%s imports %q, which is not embedded (%s)", name, target, p)
			}
		}
	}
	if _, ok := a["js/app.js"]; !ok {
		t.Error("js/app.js missing")
	}
}

func TestStylesheetSupportsBothThemesAndPhones(t *testing.T) {
	css := assets(t)["app.css"]
	for _, want := range []string{
		"prefers-color-scheme: dark", `:root:not([data-theme="light"])`, `:root[data-theme="dark"]`, // dark by the OS, and by the viewer's toggle both ways
		"prefers-reduced-motion", "max-width: 600px", "overflow-x: hidden", "--c-read", "--c-write", "--c-fresh", "--g0", "--g6",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	// Both dark declarations must define the same tokens.
	tokens := func(block string) map[string]bool {
		m := map[string]bool{}
		for _, x := range regexp.MustCompile(`--[a-z0-9-]+\s*:`).FindAllString(block, -1) {
			m[strings.TrimSpace(strings.TrimSuffix(x, ":"))] = true
		}
		return m
	}
	mediaStart := strings.Index(css, "@media (prefers-color-scheme: dark)")
	mediaEnd := strings.Index(css[mediaStart:], "\n}\n") + mediaStart
	attrStart := strings.Index(css, `:root[data-theme="dark"] {`)
	attrEnd := strings.Index(css[attrStart:], "\n}\n") + attrStart
	mt, at := tokens(css[mediaStart:mediaEnd]), tokens(css[attrStart:attrEnd])
	for k := range mt {
		if !at[k] {
			t.Errorf("dark token %s is set for the OS preference but not for the theme toggle", k)
		}
	}
	for k := range at {
		if !mt[k] {
			t.Errorf("dark token %s is set for the theme toggle but not for the OS preference", k)
		}
	}
	if len(mt) < 25 {
		t.Errorf("only %d dark tokens found: the parser of this test is out of date", len(mt))
	}
}

func TestFaviconIsInertSVG(t *testing.T) {
	svg := assets(t)["favicon.svg"]
	if strings.Contains(strings.ToLower(svg), "<script") || regexp.MustCompile(`(?i)\son[a-z]+=`).MatchString(svg) || strings.Contains(svg, "href=") {
		t.Errorf("favicon.svg is not inert: %s", svg)
	}
}
