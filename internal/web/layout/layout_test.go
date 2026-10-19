package layout

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/anemos-labs/sleipnir/internal/web"
)

const testHost = "127.0.0.1:6970"

// rig is a server with the layout routes and a home of its own.
type rig struct {
	t    *testing.T
	srv  *web.Server
	home string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	srv, err := web.New(web.Config{Addr: testHost, UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	Register(srv, Options{Home: home})
	return &rig{t: t, srv: srv, home: home}
}

// do sends a request as the page does; hdr overrides headers (an empty value removes one).
func (r *rig) do(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://"+testHost+path, rd)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+r.srv.Token())
	if method != http.MethodGet {
		req.Header.Set(web.RequestHeader, web.RequestHeaderValue)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	r.srv.ServeHTTP(rec, req)
	return rec
}

func (r *rig) get() Document {
	r.t.Helper()
	rec := r.do(http.MethodGet, "/api/layout", "", nil)
	if rec.Code != http.StatusOK {
		r.t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	var d Document
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		r.t.Fatal(err)
	}
	return d
}

func (r *rig) file() string { return filepath.Join(r.home, ".sleipnir", FileName) }

const doc = `{"version":2,"pages":{"cockpit":[{"id":"hero","x":0,"y":0,"w":4,"h":8},{"id":"gov","x":9,"y":15,"w":3,"h":3,"hidden":true}],` +
	`"cache":[{"id":"cache.hit","x":0,"y":0,"w":12,"h":6,"opts":{"density":"compact"}},{"id":"gov","x":0,"y":6,"w":3,"h":3}]}}`

// Nothing stored is an empty document; a PUT is read back as written, in a file of mode 0600; DELETE ?page forgets that page and
// DELETE forgets the file.
func TestLayoutRoundTrip(t *testing.T) {
	r := newRig(t)
	if d := r.get(); d.Version != 0 || len(d.Pages) != 0 {
		t.Fatalf("nothing stored: %+v", d)
	}
	if rec := r.do(http.MethodPut, "/api/layout", doc, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	d := r.get()
	if d.Version != 2 || len(d.Pages) != 2 || len(d.Pages["cockpit"]) != 2 || !d.Pages["cockpit"][1].Hidden || d.Pages["cache"][0].Opts["density"] != "compact" {
		t.Fatalf("read back: %+v", d)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(r.file())
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, want 0600", st.Mode().Perm())
		}
	}
	if rec := r.do(http.MethodDelete, "/api/layout?page=cache", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE page: %d %s", rec.Code, rec.Body)
	}
	if d := r.get(); len(d.Pages) != 1 || d.Pages["cockpit"] == nil {
		t.Fatalf("after DELETE ?page=cache: %+v", d)
	}
	if rec := r.do(http.MethodDelete, "/api/layout?page=nothing", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE of a page not stored: %d", rec.Code)
	}
	if rec := r.do(http.MethodDelete, "/api/layout", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d", rec.Code)
	}
	if _, err := os.Stat(r.file()); !os.IsNotExist(err) {
		t.Fatalf("the file is still there: %v", err)
	}
	if rec := r.do(http.MethodDelete, "/api/layout", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE with nothing stored: %d", rec.Code)
	}
	// no temporary file is left behind
	ents, _ := os.ReadDir(filepath.Dir(r.file()))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".web-layout-") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
}

// A write needs the page's header and a JSON body; a document that does not check out is refused with 400 bad_layout and the stored
// one is kept.
func TestLayoutRefusals(t *testing.T) {
	r := newRig(t)
	if rec := r.do(http.MethodPut, "/api/layout", doc, map[string]string{web.RequestHeader: ""}); rec.Code != http.StatusForbidden {
		t.Fatalf("no X-Sleipnir-Web: %d", rec.Code)
	}
	if rec := r.do(http.MethodDelete, "/api/layout", "", map[string]string{web.RequestHeader: ""}); rec.Code != http.StatusForbidden {
		t.Fatalf("DELETE without X-Sleipnir-Web: %d", rec.Code)
	}
	if rec := r.do(http.MethodPut, "/api/layout", doc, nil); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	bad := map[string]string{
		"version 0":       `{"version":0,"pages":{}}`,
		"bad page id":     `{"version":2,"pages":{"../x":[]}}`,
		"bad card id":     `{"version":2,"pages":{"a":[{"id":"<b>","x":0,"y":0,"w":1,"h":1}]}}`,
		"twice":           `{"version":2,"pages":{"a":[{"id":"x","x":0,"y":0,"w":1,"h":1},{"id":"x","x":1,"y":0,"w":1,"h":1}]}}`,
		"negative":        `{"version":2,"pages":{"a":[{"id":"x","x":-1,"y":0,"w":1,"h":1}]}}`,
		"zero size":       `{"version":2,"pages":{"a":[{"id":"x","x":0,"y":0,"w":0,"h":1}]}}`,
		"huge":            `{"version":2,"pages":{"a":[{"id":"x","x":0,"y":0,"w":1,"h":5000}]}}`,
		"object option":   `{"version":2,"pages":{"a":[{"id":"x","x":0,"y":0,"w":1,"h":1,"opts":{"k":{"a":1}}}]}}`,
		"long option":     `{"version":2,"pages":{"a":[{"id":"x","x":0,"y":0,"w":1,"h":1,"opts":{"k":"` + strings.Repeat("a", MaxOptText+1) + `"}}]}}`,
		"unknown field":   `{"version":2,"pages":{},"extra":1}`,
		"unknown in card": `{"version":2,"pages":{"a":[{"id":"x","x":0,"y":0,"w":1,"h":1,"z":3}]}}`,
	}
	for name, body := range bad {
		rec := r.do(http.MethodPut, "/api/layout", body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if d := r.get(); len(d.Pages) != 2 {
		t.Fatalf("a refused write changed the stored layout: %+v", d)
	}
	if rec := r.do(http.MethodDelete, "/api/layout?page=../x", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("DELETE of a bad page id: %d", rec.Code)
	}
	if rec := r.do(http.MethodPut, "/api/layout", `{"version":2,"pages":{}}`+strings.Repeat(" ", MaxBody), nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body over the cap: %d", rec.Code)
	}
}

// A file that does not read as a layout is answered as nothing stored, and the next PUT replaces it.
func TestLayoutUnreadableFile(t *testing.T) {
	r := newRig(t)
	if err := os.MkdirAll(filepath.Dir(r.file()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.file(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := r.get(); d.Version != 0 || len(d.Pages) != 0 {
		t.Fatalf("unreadable: %+v", d)
	}
	if rec := r.do(http.MethodPut, "/api/layout", doc, nil); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	if d := r.get(); d.Version != 2 {
		t.Fatalf("after PUT: %+v", d)
	}
}

// Concurrent writes are serialised: the file always holds one whole document.
func TestLayoutConcurrentWrites(t *testing.T) {
	r := newRig(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := r.do(http.MethodPut, "/api/layout", doc, nil); rec.Code != http.StatusNoContent {
				t.Errorf("PUT: %d", rec.Code)
			}
		}()
	}
	wg.Wait()
	b, err := os.ReadFile(r.file())
	if err != nil {
		t.Fatal(err)
	}
	var d Document
	if err := json.Unmarshal(b, &d); err != nil || Validate(&d) != nil {
		t.Fatalf("the file is not one whole document: %v", err)
	}
}
