package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// uiFS is the browser UI: plain files, no build step, filled in internal/web/ui. The all: prefix
// includes files whose names start with a dot or underscore; loadAssets leaves dot files out.
//
//go:embed all:ui
var uiFS embed.FS

// embeddedUI returns the UI embedded in the binary, rooted at its index.html.
func embeddedUI() fs.FS {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic("web: embedded ui: " + err.Error()) // the directory is part of the build
	}
	return sub
}

// maxAssetBytes bounds the UI held in memory.
const maxAssetBytes = 64 << 20

// asset is one file of the UI, held in memory with its content type and entity tag.
type asset struct {
	data  []byte
	ctype string
	etag  string
}

// assetSet is the UI by absolute URL path ("/js/app.js").
type assetSet struct {
	files map[string]*asset
}

// contentTypes maps file extensions to the types the UI is served with; anything else is
// application/octet-stream, which nosniff keeps inert.
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json; charset=utf-8",
	".webmanifest": "application/manifest+json; charset=utf-8",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".ico":         "image/x-icon",
	".woff2":       "font/woff2",
	".woff":        "font/woff",
	".txt":         "text/plain; charset=utf-8",
	".md":          "text/markdown; charset=utf-8",
}

// loadAssets reads the UI into memory. Files and directories whose names start with a dot are
// left out; the UI must have an index.html.
func loadAssets(fsys fs.FS) (*assetSet, error) {
	set := &assetSet{files: map[string]*asset{}}
	total := 0
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if total += len(data); total > maxAssetBytes {
			return fmt.Errorf("web: the UI is larger than %d MiB", maxAssetBytes>>20)
		}
		ct := contentTypes[strings.ToLower(path.Ext(p))]
		if ct == "" {
			ct = "application/octet-stream"
		}
		sum := sha256.Sum256(data)
		set.files["/"+p] = &asset{data: data, ctype: ct, etag: `"` + hex.EncodeToString(sum[:16]) + `"`}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("web: loading the UI: %w", err)
	}
	if set.files["/index.html"] == nil {
		return nil, errors.New("web: the UI has no index.html")
	}
	return set, nil
}

// handleAssets serves the UI. "/" is index.html. A path with no file extension that names no file
// is a route of the single-page app and gets index.html as well; a path with an extension that
// names no file is a plain 404, so that a missing script is not served as a page.
func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Path
	if name == "/" {
		name = "/index.html"
	}
	a := s.assets.files[name]
	if a == nil {
		if path.Ext(name) != "" {
			s.miss(w, r)
			return
		}
		a = s.assets.files["/index.html"]
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", a.etag)
	if etagMatches(r.Header.Get("If-None-Match"), a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(a.data)))
	_, _ = w.Write(a.data)
}

// etagMatches reports whether an If-None-Match header lists etag (or is "*"), comparing weakly.
func etagMatches(header, etag string) bool {
	for _, v := range strings.Split(header, ",") {
		v = strings.TrimSpace(v)
		if v == "*" || strings.TrimPrefix(v, "W/") == etag {
			return true
		}
	}
	return false
}
