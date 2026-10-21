package preview

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The frame is a sandboxed document whose only scripts are the two it carries, named by their hashes; it is framed by the page only.
func TestTheFrameRunsOnlyItsOwnScriptsInASandbox(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/preview/mermaid", nil)
	w := httptest.NewRecorder()
	handleFrame(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "sandbox allow-scripts", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "script-src 'self'") || regexp.MustCompile(`script-src[^;]*unsafe-inline`).MatchString(csp) {
		t.Errorf("policy is not strict: %q", csp)
	}
	if w.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options %q", w.Header().Get("X-Frame-Options"))
	}
	body := w.Body.String()
	scripts := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(body, -1)
	if len(scripts) != 2 {
		t.Fatalf("%d scripts in the frame", len(scripts))
	}
	for _, s := range scripts {
		sum := sha256.Sum256([]byte(s[1]))
		if h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"; !strings.Contains(csp, h) {
			t.Errorf("a script of %d bytes is not named by the policy", len(s[1]))
		}
	}
	if !strings.Contains(scripts[0][1], `globalThis["mermaid"]`) || scripts[1][1] != driver {
		t.Error("the frame does not carry mermaid and its driver")
	}
}

// A browser that accepts gzip gets the same document compressed; one that refuses it gets it plain.
func TestTheFrameIsCompressedWhenAccepted(t *testing.T) {
	get := func(ae string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/preview/mermaid", nil)
		r.Header.Set("Accept-Encoding", ae)
		w := httptest.NewRecorder()
		handleFrame(w, r)
		return w
	}
	plain := get("").Body.Bytes()
	z := get("gzip, deflate, br")
	if z.Header().Get("Content-Encoding") != "gzip" || z.Body.Len() >= len(plain)/2 {
		t.Fatalf("gzip: encoding %q, %d of %d bytes", z.Header().Get("Content-Encoding"), z.Body.Len(), len(plain))
	}
	zr, err := gzip.NewReader(bytes.NewReader(z.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, plain) {
		t.Error("the compressed frame is not the plain one")
	}
	if get("gzip;q=0").Header().Get("Content-Encoding") != "" {
		t.Error("gzip;q=0 got gzip")
	}
}

func TestMermaidIsTheNamedRelease(t *testing.T) {
	zr, err := gzip.NewReader(bytes.NewReader(mermaidGz))
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := io.ReadAll(zr)
	if !bytes.Contains(lib, []byte(MermaidVersion)) {
		t.Errorf("mermaid.tiny.js.gz does not name release %s", MermaidVersion)
	}
}
