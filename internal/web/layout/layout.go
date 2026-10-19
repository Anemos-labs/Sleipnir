// Package layout serves the card layout of the web page: where each card of each page sits, how large it is, whether it is hidden,
// and its options. The layout belongs to the person, not to a session or a project, so it is kept in their home
// (`~/.sleipnir/web-layout.json`, mode 0600) and follows them from one browser to another; the page keeps a copy in the browser's
// storage and works from it when the server cannot be reached.
//
// The routes are GET /api/layout (the stored document; nothing stored is an empty document of version 0), PUT /api/layout (the whole
// document, replaced) and DELETE /api/layout (?page=ID forgets one page, no page forgets every page). The document is checked before it
// is written: page and card ids are short words of letters, digits and `.:_-`, places and sizes are small non-negative integers, and
// card options are a few strings, numbers and booleans. A write goes through a temporary file and a rename, so a reader never sees
// half a document.
package layout

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/web"
)

// FileName is the name of the layout file in the user's ~/.sleipnir directory.
const FileName = "web-layout.json"

// Limits on a stored document. They bound the file and the work of reading it; a real page uses a few dozen cards on a few dozen pages.
const (
	// MaxBody is the largest PUT body, in bytes.
	MaxBody = 256 << 10
	// MaxPages is the most pages a document may hold.
	MaxPages = 200
	// MaxCards is the most cards one page may hold.
	MaxCards = 200
	// MaxOpts is the most options one card may hold.
	MaxOpts = 16
	// MaxCoord is the largest place or size of a card, in grid units.
	MaxCoord = 1000
	// MaxOptText is the longest string value of a card option, in bytes.
	MaxOptText = 200
)

// idPattern is the shape of a page id, a card id and an option name.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$`)

// Card is one card of a page: its id, its place (x, y) and size (w, h) in grid units, whether it is hidden (kept for its place, shown in
// the page's Hidden tray), and its options (density and the like, decided by the page).
type Card struct {
	ID     string         `json:"id"`
	X      int            `json:"x"`
	Y      int            `json:"y"`
	W      int            `json:"w"`
	H      int            `json:"h"`
	Hidden bool           `json:"hidden,omitempty"`
	Opts   map[string]any `json:"opts,omitempty"`
}

// Document is the whole layout: its format version (the page's) and the cards of each page by page id.
type Document struct {
	Version int               `json:"version"`
	Pages   map[string][]Card `json:"pages"`
}

// Options configure the routes: the user's home directory (the file is <Home>/.sleipnir/web-layout.json).
type Options struct {
	Home string
}

// store reads and writes the layout file; writes are serialised within the process.
type store struct {
	path string
	mu   sync.Mutex
}

// Register adds GET, PUT and DELETE /api/layout to srv. An empty Home is the current user's home directory.
func Register(srv *web.Server, o Options) {
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	s := &store{path: filepath.Join(o.Home, ".sleipnir", FileName)}
	srv.HandleFunc("GET /api/layout", s.handleGet, web.RouteOpts{})
	srv.HandleFunc("PUT /api/layout", s.handlePut, web.RouteOpts{MaxBody: MaxBody})
	srv.HandleFunc("DELETE /api/layout", s.handleDelete, web.RouteOpts{NoBody: true})
}

// Validate checks a document: a version of at least 1, at most MaxPages pages of at most MaxCards cards, ids of the id shape, no card
// twice on a page, places and sizes within 0..MaxCoord (sizes at least 1), and options of the id shape holding strings of at most
// MaxOptText bytes, numbers or booleans.
func Validate(d *Document) error {
	if d.Version < 1 {
		return errors.New("version must be 1 or more")
	}
	if len(d.Pages) > MaxPages {
		return fmt.Errorf("at most %d pages", MaxPages)
	}
	for page, cards := range d.Pages {
		if !idPattern.MatchString(page) {
			return fmt.Errorf("page id %q is not a short word of letters, digits and .:_-", clip(page))
		}
		if len(cards) > MaxCards {
			return fmt.Errorf("page %s: at most %d cards", page, MaxCards)
		}
		seen := map[string]bool{}
		for _, c := range cards {
			if !idPattern.MatchString(c.ID) {
				return fmt.Errorf("page %s: card id %q is not a short word of letters, digits and .:_-", page, clip(c.ID))
			}
			if seen[c.ID] {
				return fmt.Errorf("page %s: card %s is listed twice", page, c.ID)
			}
			seen[c.ID] = true
			if c.X < 0 || c.Y < 0 || c.X > MaxCoord || c.Y > MaxCoord || c.W < 1 || c.H < 1 || c.W > MaxCoord || c.H > MaxCoord {
				return fmt.Errorf("page %s: card %s has a place or size outside 0..%d", page, c.ID, MaxCoord)
			}
			if len(c.Opts) > MaxOpts {
				return fmt.Errorf("page %s: card %s has more than %d options", page, c.ID, MaxOpts)
			}
			for k, v := range c.Opts {
				if !idPattern.MatchString(k) {
					return fmt.Errorf("page %s: card %s: option name %q is not a short word", page, c.ID, clip(k))
				}
				switch x := v.(type) {
				case bool, float64:
				case string:
					if len(x) > MaxOptText {
						return fmt.Errorf("page %s: card %s: option %s is longer than %d bytes", page, c.ID, k, MaxOptText)
					}
				default:
					return fmt.Errorf("page %s: card %s: option %s must be a string, a number or a boolean", page, c.ID, k)
				}
			}
		}
	}
	return nil
}

// clip bounds text a request supplied before it goes into an error message.
func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// empty is the answer when nothing is stored.
func empty() Document { return Document{Version: 0, Pages: map[string][]Card{}} }

// read returns the stored document; a missing file, or one that does not read as a valid document, is the empty document (the page
// then keeps its own copy and writes it back).
func (s *store) read() Document {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return empty()
	}
	var d Document
	if json.Unmarshal(b, &d) != nil || Validate(&d) != nil {
		return empty()
	}
	if d.Pages == nil {
		d.Pages = map[string][]Card{}
	}
	return d
}

// write replaces the file with d through a temporary file in the same directory and a rename; the directory is made (0700) when
// missing, and the file is readable by the user only.
func (s *store) write(d Document) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".web-layout-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// handleGet answers the stored document.
func (s *store) handleGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	d := s.read()
	s.mu.Unlock()
	_ = web.WriteJSON(w, http.StatusOK, d)
}

// handlePut replaces the stored document with the body, after Validate (400 bad_layout when it fails); 204 on success.
func (s *store) handlePut(w http.ResponseWriter, r *http.Request) {
	var d Document
	if !web.DecodeJSON(w, r, &d) {
		return
	}
	if d.Pages == nil {
		d.Pages = map[string][]Card{}
	}
	if err := Validate(&d); err != nil {
		web.Error(w, http.StatusBadRequest, "bad_layout", err.Error())
		return
	}
	s.mu.Lock()
	err := s.write(d)
	s.mu.Unlock()
	if err != nil {
		web.Error(w, http.StatusInternalServerError, "internal", "the layout could not be written")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDelete forgets one page (?page=ID) or, without a page, the whole file; 204 either way, also when nothing was stored.
func (s *store) handleDelete(w http.ResponseWriter, r *http.Request) {
	page := r.URL.Query().Get("page")
	if page != "" && !idPattern.MatchString(page) {
		web.Error(w, http.StatusBadRequest, "bad_layout", "page id is not a short word of letters, digits and .:_-")
		return
	}
	s.mu.Lock()
	var err error
	if page == "" {
		if e := os.Remove(s.path); e != nil && !errors.Is(e, fs.ErrNotExist) {
			err = e
		}
	} else if d := s.read(); d.Version > 0 {
		if _, ok := d.Pages[page]; ok {
			delete(d.Pages, page)
			err = s.write(d)
		}
	}
	s.mu.Unlock()
	if err != nil {
		web.Error(w, http.StatusInternalServerError, "internal", "the layout could not be written")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
