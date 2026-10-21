// Package preview serves the diagram frame of `sleipnir web`: a document that draws mermaid diagrams for the page's previews of
// files (a .mmd file, a mermaid block of a Markdown file).
//
// Mermaid is a large third-party renderer, so it never runs in the page. The frame (GET /api/preview/mermaid) is served with a
// policy that makes it a sandboxed document of an opaque origin: its scripts run, but it cannot read the page, the cookie or the
// API (a request from it is cross-site, which the server refuses), it loads nothing, and only this server's page may frame it.
// Its only scripts are inline and named by their hashes: the vendored mermaid build (mermaid/, MIT, see mermaid/LICENSE) and the
// driver (frame.js) that answers the page's messages with SVG text. The page shows that SVG as an image, where nothing runs.
package preview

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/web"
)

// MermaidVersion is the release of @mermaid-js/tiny that mermaid/mermaid.tiny.js.gz holds.
const MermaidVersion = "12.2.0"

//go:embed mermaid/mermaid.tiny.js.gz
var mermaidGz []byte

//go:embed frame.js
var driver string

// Register adds GET /api/preview/mermaid, the diagram frame, to srv.
func Register(srv *web.Server) {
	srv.HandleFunc("GET /api/preview/mermaid", handleFrame, web.RouteOpts{})
}

// frame is the document, built once: its bytes, the same gzip-compressed, and its policy.
type frame struct {
	html, gz []byte
	policy   string
	err      error
}

var (
	frameOnce sync.Once
	built     frame
)

// buildFrame puts the renderer and the driver into one document and names both scripts in its policy by their hashes.
func buildFrame() frame {
	zr, err := gzip.NewReader(bytes.NewReader(mermaidGz))
	if err != nil {
		return frame{err: err}
	}
	lib, err := io.ReadAll(zr)
	if err != nil {
		return frame{err: err}
	}
	var b bytes.Buffer
	b.WriteString("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>diagram</title></head><body><script>")
	b.Write(lib)
	b.WriteString("</script><script>")
	b.WriteString(driver)
	b.WriteString("</script></body></html>")
	var z bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&z, gzip.BestCompression)
	_, _ = zw.Write(b.Bytes())
	if err := zw.Close(); err != nil {
		return frame{err: err}
	}
	return frame{html: b.Bytes(), gz: z.Bytes(), policy: framePolicy(string(lib), driver)}
}

// framePolicy is the policy of the frame: the two scripts by hash, inline styles (mermaid styles the SVG it draws with a style
// element), data: images, nothing loaded from anywhere, framed by this origin only, sandboxed with scripts allowed and no origin.
func framePolicy(scripts ...string) string {
	var hs []string
	for _, s := range scripts {
		sum := sha256.Sum256([]byte(s))
		hs = append(hs, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return "default-src 'none'; script-src " + strings.Join(hs, " ") + "; style-src 'unsafe-inline'; img-src data:; " +
		"frame-ancestors 'self'; form-action 'none'; base-uri 'none'; sandbox allow-scripts"
}

// handleFrame serves the diagram frame, compressed when the browser accepts gzip.
func handleFrame(w http.ResponseWriter, r *http.Request) {
	frameOnce.Do(func() { built = buildFrame() })
	if built.err != nil {
		web.Error(w, http.StatusInternalServerError, "internal", "the diagram renderer could not be read")
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", built.policy)
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("Vary", "Accept-Encoding")
	body := built.html
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		h.Set("Content-Encoding", "gzip")
		body = built.gz
	}
	_, _ = w.Write(body)
}

// acceptsGzip reports whether an Accept-Encoding value lists gzip without refusing it (q=0).
func acceptsGzip(v string) bool {
	for _, part := range strings.Split(v, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
			return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
		}
	}
	return false
}
