package wsvc

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

// svgV is the SVG picture an agent wrote in turn v.
func svgV(v string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><text>` + v + `</text></svg>`
}

// mediaEnv is a single agent that wrote pic.svg in turn 1 and wrote it again in turn 2, in a project with a PNG, a picture
// the agents may not read and a Go file.
func mediaEnv(t *testing.T) *env {
	t.Helper()
	root := project(t, map[string]string{
		"main.go":               "package main\n",
		"shot.png":              "\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR" + strings.Repeat("\x00", 64),
		"secrets/face.png":      "\x89PNG\r\n\x1a\n",
		".sleipnir/config.json": `{"permissions":{"deny":["Read(./secrets/**)"]}}`,
	})
	sc := &soloScript{turns: [][][]mock.ToolCall{
		{{call("w1", "write", map[string]any{"path": "pic.svg", "content": svgV("one")})}},
		{{call("w2", "write", map[string]any{"path": "pic.svg", "content": svgV("two")})}},
	}}
	client, model := startMock(t, sc.respond)
	e := newEnv(t, root, options(t, root, client, model))
	for _, goal := range []string{"draw", "draw again"} {
		if _, err := e.sess.Run(context.Background(), goal); err != nil {
			t.Fatal(err)
		}
	}
	if readFile(t, filepath.Join(root, "pic.svg")) != svgV("two") {
		t.Fatal("the scripted agent did not write the picture")
	}
	return e
}

// The bytes of a picture come with their type and a policy that sandboxes them; a checkpoint's version is the one it began
// with; another kind of file, a denied path and a path outside the project are refused.
func TestRawServesMediaWithTheirTypeAndASandbox(t *testing.T) {
	e := mediaEnv(t)
	w := e.get("/ws/raw?path=shot.png", nil)
	expect(t, w, http.StatusOK, "")
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type %q", ct)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
		t.Errorf("the policy of a served picture does not sandbox it: %q", csp)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("a live file lost the envelope's headers: %v", w.Header())
	}
	if !strings.HasPrefix(w.Body.String(), "\x89PNG") {
		t.Errorf("body %q", w.Body.String())
	}

	w = e.get("/ws/raw?path=pic.svg", nil)
	expect(t, w, http.StatusOK, "")
	if w.Header().Get("Content-Type") != "image/svg+xml" || w.Body.String() != svgV("two") {
		t.Errorf("live svg: %q %q", w.Header().Get("Content-Type"), w.Body.String())
	}
	var idx struct {
		Cps []struct{ ID string } `json:"cps"`
	}
	expect(t, e.get("/ws/index", &idx), http.StatusOK, "")
	if len(idx.Cps) < 2 {
		t.Fatalf("checkpoints: %+v", idx.Cps)
	}
	// the second turn's checkpoint began with turn 1's picture, and what a checkpoint holds can be kept by the browser
	w = e.get("/ws/raw?path=pic.svg&at="+idx.Cps[1].ID, nil)
	expect(t, w, http.StatusOK, "")
	if w.Body.String() != svgV("one") || !strings.Contains(w.Header().Get("Cache-Control"), "max-age") {
		t.Errorf("at %s: %q, Cache-Control %q", idx.Cps[1].ID, w.Body.String(), w.Header().Get("Cache-Control"))
	}
	expect(t, e.get("/ws/raw?path=pic.svg&at=base", nil), http.StatusNotFound, "no_file")

	expect(t, e.get("/ws/raw?path=main.go", nil), http.StatusUnsupportedMediaType, "not_media")
	expect(t, e.get("/ws/raw?path=secrets/face.png", nil), http.StatusForbidden, "denied")
	expect(t, e.get("/ws/raw?path=../x.png", nil), http.StatusBadRequest, "bad_path")
	expect(t, e.get("/ws/raw?path=gone.png", nil), http.StatusNotFound, "no_file")
}

// A video seeks: a range of its bytes is answered with 206 and that range.
func TestRawAnswersRanges(t *testing.T) {
	e := mediaEnv(t)
	writeFile(t, filepath.Join(e.root, "clip.webm"), "0123456789")
	w := e.do(request{method: http.MethodGet, path: e.tabPath("/ws/raw?path=clip.webm"), header: map[string]string{"Range": "bytes=2-5"}})
	if w.Code != http.StatusPartialContent || w.Body.String() != "2345" || w.Header().Get("Content-Type") != "video/webm" {
		t.Errorf("range: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
}

// A PDF may be framed by the page (a browser's viewer does not run in a sandbox) and by nothing else.
func TestRawLetsThePageFrameAPDF(t *testing.T) {
	e := mediaEnv(t)
	writeFile(t, filepath.Join(e.root, "doc.pdf"), "%PDF-1.4\n")
	w := e.get("/ws/raw?path=doc.pdf", nil)
	expect(t, w, http.StatusOK, "")
	if w.Header().Get("X-Frame-Options") != "SAMEORIGIN" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Errorf("pdf headers: %v", w.Header())
	}
}

func TestMediaTypeKnowsTheBrowsersFormats(t *testing.T) {
	for p, want := range map[string]string{"a/B.PNG": "image/png", "x.svg": "image/svg+xml", "v.mp4": "video/mp4", "s.mp3": "audio/mpeg", "d.pdf": "application/pdf"} {
		if got, ok := MediaType(p); !ok || got != want {
			t.Errorf("%s: %q %v", p, got, ok)
		}
	}
	for _, p := range []string{"x.html", "x.go", "x", ".env", "x.png.txt"} {
		if _, ok := MediaType(p); ok {
			t.Errorf("%s is served", p)
		}
	}
}
