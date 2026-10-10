package web

import (
	"bytes"
	"io/fs"
	"net/http/httptest"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func TestAssetsAreServedWithTheirTypes(t *testing.T) {
	rg := newRig(t, nil)
	cookie := map[string]string{"Cookie": rg.login()}
	for p, ct := range map[string]string{
		"/":                         "text/html; charset=utf-8",
		"/index.html":               "text/html; charset=utf-8",
		"/js/app.js":                "text/javascript; charset=utf-8",
		"/nested/deeper/module.mjs": "text/javascript; charset=utf-8",
		"/app.css":                  "text/css; charset=utf-8",
		"/fonts/f.woff2":            "font/woff2",
		"/img/a.png":                "image/png",
		"/favicon.ico":              "image/x-icon",
		"/data.json":                "application/json; charset=utf-8",
		"/icon.svg":                 "image/svg+xml",
		"/nested/deeper/page.txt":   "text/plain; charset=utf-8",
		"/unknown.bin":              "application/octet-stream",
		"/notes.md":                 "text/markdown; charset=utf-8",
		"/fonts/OFL-barlow.txt":     "text/plain; charset=utf-8",
	} {
		rec := rg.do(req{target: p, header: cookie})
		if rec.Code != 200 || rec.Header().Get("Content-Type") != ct {
			t.Errorf("GET %s = %d %q, want %q", p, rec.Code, rec.Header().Get("Content-Type"), ct)
		}
		if rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("ETag") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: caching headers %v", p, rec.Header())
		}
		if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) || rec.Body.Len() == 0 {
			t.Errorf("GET %s: Content-Length %s for %d bytes", p, rec.Header().Get("Content-Length"), rec.Body.Len())
		}
		checkEnvelopeHeaders(t, p, rec)
	}
	if rec := rg.do(req{target: "/js/app.js", header: cookie}); rec.Body.String() != "console.log(1)" {
		t.Errorf("body = %q", rec.Body.String())
	}
	if rec := rg.do(req{method: "HEAD", target: "/js/app.js", header: cookie}); rec.Code != 200 {
		t.Errorf("HEAD = %d", rec.Code)
	}
}

func TestConditionalRequestsGet304(t *testing.T) {
	rg := newRig(t, nil)
	h := map[string]string{"Cookie": rg.login()}
	rec := rg.do(req{target: "/app.css", header: h})
	etag := rec.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) || len(etag) < 20 {
		t.Fatalf("ETag = %q", etag)
	}
	etags := map[string]bool{}
	for _, p := range []string{"/app.css", "/js/app.js", "/index.html", "/data.json"} {
		etags[rg.do(req{target: p, header: h}).Header().Get("ETag")] = true
	}
	if len(etags) != 4 {
		t.Errorf("different files share ETags: %v", etags)
	}
	for name, inm := range map[string]string{"exact": etag, "weak": "W/" + etag, "a list": `"nope", ` + etag, "star": "*"} {
		rec := rg.do(req{target: "/app.css", header: mergeHeaders(h, map[string]string{"If-None-Match": inm})})
		if rec.Code != 304 || rec.Body.Len() != 0 {
			t.Errorf("If-None-Match %s = %d with %d bytes", name, rec.Code, rec.Body.Len())
		}
	}
	for _, inm := range []string{`"other"`, "", `"` + strings.Trim(etag, `"`) + `x"`} {
		if rec := rg.do(req{target: "/app.css", header: mergeHeaders(h, map[string]string{"If-None-Match": inm})}); rec.Code != 200 {
			t.Errorf("If-None-Match %q = %d", inm, rec.Code)
		}
	}
	// The tag is a function of the content.
	again := newRig(t, nil)
	if got := again.do(req{target: "/app.css", header: map[string]string{"Cookie": again.login()}}).Header().Get("ETag"); got != etag {
		t.Errorf("the ETag changed between servers: %s %s", got, etag)
	}
	other := newRig(t, func(c *Config) {
		c.UI = fstest.MapFS{"index.html": {Data: []byte("x")}, "app.css": {Data: []byte("body{color:red}")}}
	})
	if got := other.do(req{target: "/app.css", header: map[string]string{"Cookie": other.login()}}).Header().Get("ETag"); got == etag {
		t.Error("different content has the same ETag")
	}
}

func TestHiddenFilesAreNeverServed(t *testing.T) {
	rg := newRig(t, nil)
	h := map[string]string{"Cookie": rg.login()}
	for _, p := range []string{"/.hidden", "/.git/config", "/js/.env", "/.git", "/.git/", "/js/.env/x"} {
		rec := rg.do(req{target: p, header: h})
		if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "[core]") || strings.Contains(rec.Body.String(), "KEY=") {
			t.Errorf("GET %s leaked a hidden file: %q", p, rec.Body.String())
		}
	}
	for _, p := range []string{"/.hidden", "/js/.env"} {
		if rec := rg.do(req{target: p, header: h}); rec.Code != 404 {
			t.Errorf("GET %s = %d", p, rec.Code)
		}
	}
}

func TestSinglePageAppRoutesGetTheIndex(t *testing.T) {
	rg := newRig(t, nil)
	h := map[string]string{"Cookie": rg.login()}
	index := rg.do(req{target: "/", header: h}).Body.String()
	for _, p := range []string{"/sessions", "/sessions/20261009-120000-abcdef", "/settings/models", "/a/b/c/d", "/js/", "/fonts", "/img"} {
		rec := rg.do(req{target: p, header: h})
		if rec.Code != 200 || rec.Body.String() != index || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("GET %s = %d %s: a route of the app gets the page", p, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	// A path that names a file that is not there is a 404, not a page served as a script.
	for _, p := range []string{"/js/missing.js", "/missing.css", "/fonts/missing.woff2", "/img/x.png", "/favicon.png"} {
		if rec := rg.do(req{target: p, header: h}); rec.Code != 404 || errCode(rec) != "not_found" {
			t.Errorf("GET %s = %d", p, rec.Code)
		}
	}
	// The API is not part of the app's routes.
	if rec := rg.do(req{target: "/api/anything", header: h}); rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("GET /api/anything = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestTheEmbeddedUIIsServedByDefault(t *testing.T) {
	srv, err := New(Config{Addr: testHost})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Hub().Close()
	embedded := embeddedUI()
	n := 0
	err = fs.WalkDir(embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(path.Base(p), ".") || strings.Contains(p, "/.") {
			return err
		}
		want, err := fs.ReadFile(embedded, p)
		if err != nil {
			return err
		}
		hr := httptest.NewRequest("GET", "/"+p, nil)
		hr.Host = testHost
		hr.Header.Set("Authorization", "Bearer "+srv.Token())
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, hr)
		if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("GET /%s = %d, %d bytes of %d", p, rec.Code, rec.Body.Len(), len(want))
		}
		if ct := rec.Header().Get("Content-Type"); ct == "application/octet-stream" && !strings.HasSuffix(p, ".bin") {
			t.Errorf("GET /%s is served as %s: add its type to contentTypes", p, ct)
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("the embedded UI is empty")
	}
}

func TestLoadAssetsRules(t *testing.T) {
	if _, err := loadAssets(fstest.MapFS{"a.txt": {Data: []byte("x")}}); err == nil || !strings.Contains(err.Error(), "index.html") {
		t.Errorf("no index.html: %v", err)
	}
	set, err := loadAssets(fstest.MapFS{"index.html": {Data: []byte("x")}, ".DS_Store": {Data: []byte("x")}, "d/.keep": {Data: []byte("")}, "d/f.js": {Data: []byte("1")}, ".hidden/x.js": {Data: []byte("1")}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for k := range set.files {
		names = append(names, k)
	}
	if len(names) != 2 || set.files["/d/f.js"] == nil || set.files["/index.html"] == nil {
		t.Errorf("assets = %v", names)
	}
}

// ---- the UI that is embedded in the binary ---------------------------------------------------

// embeddedFiles returns the embedded UI by path, text files only.
func embeddedFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	embedded := embeddedUI()
	err := fs.WalkDir(embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch path.Ext(p) {
		case ".html", ".js", ".css", ".svg", ".mjs", ".json":
			b, err := fs.ReadFile(embedded, p)
			if err != nil {
				return err
			}
			out[p] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The UI is plain files served under a strict Content Security Policy. These tests read the embedded sources and hold the line the
// policy alone cannot hold: a page that works in a test can still be dead in a browser if it needs an inline script, an inline handler,
// an inline style element or something from another origin.
func TestTheEmbeddedUIWorksUnderTheContentSecurityPolicy(t *testing.T) {
	files := embeddedFiles(t)
	html := files["index.html"]
	if len(files) <= 1 && len(html) < 1024 {
		t.Skip("the UI is only the placeholder")
	}
	all := embeddedUI()
	exists := func(p string) bool {
		_, err := fs.Stat(all, p)
		return err == nil
	}

	// index.html
	scripts := regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`).FindAllStringSubmatch(html, -1)
	if len(scripts) == 0 {
		t.Error("index.html loads no script")
	}
	for _, s := range scripts {
		if !regexp.MustCompile(`(?i)\bsrc\s*=`).MatchString(s[1]) || strings.TrimSpace(s[2]) != "" {
			t.Errorf("script must be an external file with no inline body (script-src 'self'): %.100q", s[0])
		}
	}
	for name, pat := range map[string]string{
		"an inline <style> element (style-src 'self')": `(?i)<style\b`,
		"an inline event handler":                      `(?i)\son[a-z]+\s*=`,
		"a javascript: URL":                            `(?i)javascript:`,
		"<base> (base-uri 'none')":                     `(?i)<base\b`,
		"a frame, object or embed":                     `(?i)<(iframe|frame|object|embed)\b`,
		"a remote resource":                            `(?i)\b(src|href|action|poster|srcset)\s*=\s*["']?\s*(https?:)?//`,
		"data: where only images may be":               `(?i)<(script|iframe|object|embed)\b[^>]*\s(src|data)\s*=\s*["']?data:|<link\b[^>]*rel\s*=\s*["']?stylesheet[^>]*href\s*=\s*["']?data:`,
		"a meta refresh":                               `(?i)<meta[^>]+http-equiv\s*=\s*["']?refresh`,
	} {
		if m := regexp.MustCompile(pat).FindString(html); m != "" {
			t.Errorf("index.html has %s: %.100q", name, m)
		}
	}
	if !strings.Contains(html, `name="viewport"`) || !strings.Contains(html, "<title>") || !strings.Contains(html, `lang="en"`) {
		t.Error("index.html needs a viewport (phone width), a title and a language")
	}
	// Everything the page references is in the binary.
	ref := regexp.MustCompile(`(?i)<(?:script|link|img|source)\b[^>]*\s(?:src|href)\s*=\s*["']([^"']+)["']`)
	for _, m := range ref.FindAllStringSubmatch(html, -1) {
		u := m[1]
		if strings.HasPrefix(u, "data:") || strings.HasPrefix(u, "#") {
			continue
		}
		u = strings.SplitN(strings.SplitN(u, "?", 2)[0], "#", 2)[0]
		if !exists(strings.TrimPrefix(path.Clean("/"+u), "/")) {
			t.Errorf("index.html references %q, which is not embedded", m[1])
		}
	}

	// style sheets
	urlRef := regexp.MustCompile(`url\(\s*['"]?([^)'"]+)['"]?\s*\)`)
	for name, src := range files {
		if path.Ext(name) != ".css" {
			continue
		}
		if regexp.MustCompile(`(?i)@import\s+(url\()?\s*['"]?(https?:)?//`).MatchString(src) {
			t.Errorf("%s imports a remote style sheet", name)
		}
		for _, m := range urlRef.FindAllStringSubmatch(src, -1) {
			u := strings.TrimSpace(m[1])
			switch {
			case strings.HasPrefix(u, "data:"), strings.HasPrefix(u, "#"), strings.HasPrefix(u, "var("):
			case strings.HasPrefix(u, "http:"), strings.HasPrefix(u, "https:"), strings.HasPrefix(u, "//"):
				t.Errorf("%s: url(%s) loads from another origin", name, u)
			default:
				target := strings.SplitN(strings.SplitN(u, "?", 2)[0], "#", 2)[0]
				if !strings.HasPrefix(target, "/") {
					target = path.Join(path.Dir(name), target)
				}
				if !exists(strings.TrimPrefix(path.Clean("/"+target), "/")) {
					t.Errorf("%s: url(%s) is not embedded", name, u)
				}
			}
		}
	}

	// scripts
	forbidden := map[string]*regexp.Regexp{
		"eval":                                  regexp.MustCompile(`\beval\s*\(`),
		"new Function":                          regexp.MustCompile(`\bnew\s+Function\b`),
		"setTimeout(string)":                    regexp.MustCompile(`\bsetTimeout\s*\(\s*['"` + "`" + `]`),
		"setInterval(string)":                   regexp.MustCompile(`\bsetInterval\s*\(\s*['"` + "`" + `]`),
		"document.write":                        regexp.MustCompile(`\bdocument\s*\.\s*write(ln)?\s*\(`),
		"importScripts":                         regexp.MustCompile(`\bimportScripts\s*\(`),
		"WebSocket":                             regexp.MustCompile(`\bnew\s+WebSocket\b`),
		"XMLHttpRequest":                        regexp.MustCompile(`\bXMLHttpRequest\b`),
		"sendBeacon":                            regexp.MustCompile(`\bsendBeacon\b`),
		"a javascript: URL":                     regexp.MustCompile(`javascript:\s*[a-zA-Z(]`),
		"an inline handler in markup it builds": regexp.MustCompile(`<[a-zA-Z][^<>]*\son(click|dblclick|input|change|submit|keydown|keyup|keypress|load|error|mouse[a-z]+|focus|blur|toggle|pointer[a-z]+)\s*=`),
		"an inline <style> element in markup it builds": regexp.MustCompile(`<style[\s>]`),
		"srcdoc":           regexp.MustCompile(`\bsrcdoc\b`),
		"a remote request": regexp.MustCompile(`\b(fetch|EventSource|Worker|SharedWorker)\s*\(\s*['"` + "`" + `]\s*(https?:)?//`),
		"a remote import":  regexp.MustCompile(`\bimport\s*\(?\s*['"]\s*(https?:)?//|from\s+['"](https?:)?//`),
		"a remote script":  regexp.MustCompile(`\.src\s*=\s*['"` + "`" + `]\s*(https?:)?//`),
	}
	for name, src := range files {
		if path.Ext(name) != ".js" && path.Ext(name) != ".mjs" {
			continue
		}
		for what, re := range forbidden {
			if loc := re.FindStringIndex(src); loc != nil {
				t.Errorf("%s:%d uses %s", name, strings.Count(src[:loc[0]], "\n")+1, what)
			}
		}
	}
	// What the page reaches over the network is this server's API and nothing else.
	for name, src := range files {
		for _, m := range regexp.MustCompile(`\b(?:fetch|EventSource)\s*\(\s*(?:['"`+"`"+`]([^'"`+"`"+`]*)['"`+"`"+`])`).FindAllStringSubmatch(src, -1) {
			if u := m[1]; strings.Contains(u, "://") || strings.HasPrefix(u, "//") {
				t.Errorf("%s reaches %q", name, u)
			}
		}
	}
	// Module imports stay inside the binary (classic scripts have none; "import" in their strings is sample code).
	imp := regexp.MustCompile(`(?m)^\s*import\s+(?:[^'"]*?from\s+)?['"]([^'"]+)['"]`)
	modules := regexp.MustCompile(`(?i)<script\b[^>]*type\s*=\s*["']module["']`).MatchString(html)
	for name, src := range files {
		if !modules || (path.Ext(name) != ".js" && path.Ext(name) != ".mjs") {
			continue
		}
		for _, m := range imp.FindAllStringSubmatch(src, -1) {
			target := m[1]
			if !strings.HasPrefix(target, "./") && !strings.HasPrefix(target, "../") && !strings.HasPrefix(target, "/") {
				t.Errorf("%s imports %q: only modules of this binary are allowed", name, target)
				continue
			}
			p := path.Clean(path.Join(path.Dir(name), target))
			if strings.HasPrefix(target, "/") {
				p = strings.TrimPrefix(path.Clean(target), "/")
			}
			if !exists(p) {
				t.Errorf("%s imports %q, which is not embedded", name, target)
			}
		}
	}
	// Vector images are inert.
	for name, src := range files {
		if path.Ext(name) == ".svg" && (regexp.MustCompile(`(?i)<script|\son[a-z]+\s*=`).MatchString(src)) {
			t.Errorf("%s is not inert", name)
		}
	}
}

// The UI is developed against scripts/web-ui-dev.mjs, which serves the files with the policy of this server. A policy that differs
// would let the page work in development and fail in the binary.
func TestTheDevelopmentServerSendsTheSamePolicy(t *testing.T) {
	src, err := os.ReadFile("../../scripts/web-ui-dev.mjs")
	if err != nil {
		t.Skip("scripts/web-ui-dev.mjs is not part of this checkout")
	}
	m := regexp.MustCompile(`(?s)export const CSP = \[(.*?)\]\.join\('; '\)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("scripts/web-ui-dev.mjs has no CSP list in the expected form")
	}
	var parts []string
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1) {
		parts = append(parts, string(q[1]))
	}
	if got := strings.Join(parts, "; "); got != contentSecurityPolicy {
		t.Errorf("scripts/web-ui-dev.mjs sends\n  %s\nbut the server sends\n  %s", got, contentSecurityPolicy)
	}
}
