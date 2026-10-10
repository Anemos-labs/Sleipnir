// Package wsvc serves the Workspace routes of `sleipnir web`: the project's files
// and their history as the session's checkpoint store records it, diffs between any two
// points with per-line authorship, a hunk's revert and its undo, a checkpoint's restore
// (preview, apply with an undo capture, undo), reviewed marks, the isolated team's
// worktrees, merge queue, verification output and the manual application of its
// verified work, `@` path completion, and the permission tester ("would it ask?").
//
// Every file is read through the checkpoint store's path rules: project-relative paths
// only, resolved under the session's root with the symlinks of their directory part
// checked against it, the file itself opened without following a symlink and without
// waiting on a FIFO, at most the store's size cap, and 2 MiB of text sent. .git, the
// session's state directory and every path the permission engine refuses an agent to
// read are never served. Mutations take the tab exclusively (no turn may run), write
// atomically through the store (which also records them, so /rewind sees them) and are
// confirmed with a scope that names what they change (restore, a hunk's revert, applying verified work; docs/WEB-API.md).
package wsvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Register adds the Workspace routes to srv, resolving tabs through h.
func Register(srv *web.Server, h seam.Host) {
	s := newService(srv, h)
	long := web.RouteOpts{WriteTimeout: 60 * time.Second}
	srv.HandleFunc("GET /api/sessions/{id}/ws/index", s.handleIndex, long)
	srv.HandleFunc("GET /api/sessions/{id}/ws/file", s.handleFile, long)
	srv.HandleFunc("GET /api/sessions/{id}/ws/diff", s.handleDiff, long)
	srv.HandleFunc("POST /api/sessions/{id}/ws/revert", s.handleRevert, long)
	srv.HandleFunc("POST /api/sessions/{id}/ws/revert/{rid}/undo", s.handleRevertUndo, web.RouteOpts{NoBody: true, WriteTimeout: 60 * time.Second})
	srv.HandleFunc("POST /api/sessions/{id}/ws/restore", s.handleRestore, long)
	srv.HandleFunc("POST /api/sessions/{id}/ws/restore/undo", s.handleRestoreUndo, web.RouteOpts{NoBody: true, WriteTimeout: 60 * time.Second})
	srv.HandleFunc("PUT /api/sessions/{id}/ws/reviewed", s.handleReviewed, web.RouteOpts{})
	srv.HandleFunc("GET /api/sessions/{id}/ws/worktrees", s.handleWorktrees, long)
	srv.HandleFunc("GET /api/sessions/{id}/ws/queue", s.handleQueue, web.RouteOpts{})
	srv.HandleFunc("GET /api/sessions/{id}/ws/verify/{task}", s.handleVerify, web.RouteOpts{})
	srv.HandleFunc("POST /api/sessions/{id}/ws/accept", s.handleAccept, web.RouteOpts{WriteTimeout: 5 * time.Minute})
	srv.HandleFunc("GET /api/sessions/{id}/complete", s.handleComplete, web.RouteOpts{})
	srv.HandleFunc("POST /api/sessions/{id}/permissions/check", s.handleCheck, web.RouteOpts{})
}

// service is the state of the Workspace routes: what lives only in this process (the
// reverts that can be undone, the latest restore of each tab, the log readers) and the
// bound on content-heavy work.
type service struct {
	srv *web.Server
	h   seam.Host
	now func() time.Time

	// heavy bounds the requests that read and diff file contents at once.
	heavy chan struct{}

	mu       sync.Mutex
	reverts  map[string]*revertRec  // revert id -> record
	restores map[string]*restoreRec // session dir -> the latest restore while it can be undone
	logs     map[string]*logCache   // session dir -> what the log says
	classes  map[string]*classCache // session dir -> permission classes of paths
	kinds    map[kindKey]string     // a file's version -> what the tree shows it as
}

// newService builds the routes' state.
func newService(srv *web.Server, h seam.Host) *service {
	return &service{
		srv: srv, h: h, now: time.Now, heavy: make(chan struct{}, 4),
		reverts: map[string]*revertRec{}, restores: map[string]*restoreRec{}, logs: map[string]*logCache{},
		classes: map[string]*classCache{}, kinds: map[kindKey]string{},
	}
}

// The shapes of the path parameters: a tab, a revert (the ids this package makes) and a task.
var (
	tabIDRE  = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	revIDRE  = regexp.MustCompile(`^v_[a-z2-7]{16}$`)
	taskIDRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`)
)

// werr builds the error of a route (the error body of docs/WEB-API.md).
func werr(status int, code, msg string) *wire.Error {
	return &wire.Error{Status: status, Code: code, Msg: msg}
}

// fail writes err as the route's error response.
func fail(w http.ResponseWriter, err error) { web.WriteError(w, err) }

// errBusy is the refusal of a change while a turn runs.
var errBusy = werr(http.StatusConflict, "busy", "a turn is running: wait for it to finish, or interrupt it")

// tabOf resolves the {id} of a request to the tab's session access and its current
// session. It writes the refusal and returns false when there is none.
func (s *service) tabOf(w http.ResponseWriter, r *http.Request) (seam.SessionAccess, *session.Session, bool) {
	id := r.PathValue("id")
	if !tabIDRE.MatchString(id) {
		fail(w, werr(http.StatusBadRequest, "bad_request", "that is not a session id"))
		return nil, nil, false
	}
	t, ok := s.h.Tab(id)
	if !ok || t == nil {
		fail(w, werr(http.StatusNotFound, "not_found", "no such session"))
		return nil, nil, false
	}
	acc := t.Access()
	if acc == nil {
		fail(w, werr(http.StatusConflict, "busy", "the session is starting: try again in a moment"))
		return nil, nil, false
	}
	sess := acc.Session()
	if sess == nil || sess.Ckpt == nil {
		fail(w, werr(http.StatusConflict, "busy", "the session is starting: try again in a moment"))
		return nil, nil, false
	}
	// Line authorship needs the write journal; a host enables it when it starts the
	// session, and a session it did not (or not yet) is journaled from here on.
	sess.EnableWriteJournal()
	return acc, sess, true
}

// exclusive runs fn with the tab's session while no turn runs (seam.SessionAccess.Exclusive),
// refusing with 409 busy when one does. An error fn returns is passed on.
func (s *service) exclusive(ctx context.Context, acc seam.SessionAccess, fn func(*session.Session) error) error {
	if acc.Busy() {
		return errBusy
	}
	return acc.Exclusive(ctx, func(sess *session.Session) error {
		if sess == nil || sess.Ckpt == nil {
			return werr(http.StatusConflict, "busy", "the session is starting: try again in a moment")
		}
		return fn(sess)
	})
}

// acquire takes a slot of content-heavy work, or fails when the request ends first.
func (s *service) acquire(ctx context.Context) (func(), error) {
	select {
	case s.heavy <- struct{}{}:
		return func() { <-s.heavy }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// d16 is the first 16 hex characters of the SHA-256 of the canonical JSON of v (sorted
// keys, no spaces), as confirmation scopes use it.
func d16(v map[string]string) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kj, _ := json.Marshal(k)
		vj, _ := json.Marshal(v[k])
		b.Write(kj)
		b.WriteByte(':')
		b.Write(vj)
	}
	b.WriteByte('}')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}

// randomID makes an unguessable id: prefix and 16 base32 characters.
func randomID(prefix string) (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + strings.ToLower(base32.StdEncoding.EncodeToString(b[:])), nil
}

// ---- checkpoint ids --------------------------------------------------------------------

// uiID is the id the page shows for a checkpoint: cp_0007 -> c07.
func uiID(cp string) string {
	n, err := strconv.Atoi(strings.TrimPrefix(cp, "cp_"))
	if err != nil || n <= 0 {
		return cp
	}
	if n < 10 {
		return "c0" + strconv.Itoa(n)
	}
	return "c" + strconv.Itoa(n)
}

// storeID is the store's id of a checkpoint the page names (c07, cp_0007).
func storeID(id string) (string, bool) {
	var digits string
	switch {
	case strings.HasPrefix(id, "cp_"):
		digits = id[3:]
	case strings.HasPrefix(id, "c"):
		digits = id[1:]
	default:
		return "", false
	}
	if digits == "" || len(digits) > 9 {
		return "", false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return "", false
	}
	return "cp_" + pad4(n), true
}

// pad4 formats n with at least four digits.
func pad4(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

// point is a moment of a file's history the page names: "live" (now), "base" (before
// the session) or a checkpoint (as it was when the checkpoint began).
type point struct {
	live, base bool
	cp         string // store id
}

// parsePoint reads at, from or to; empty means def.
func parsePoint(s, def string) (point, error) {
	if s == "" {
		s = def
	}
	switch s {
	case "live", "now":
		return point{live: true}, nil
	case "base", "start":
		return point{base: true}, nil
	}
	if id, ok := storeID(s); ok {
		return point{cp: id}, nil
	}
	return point{}, werr(http.StatusBadRequest, "bad_request", "a point in time is a checkpoint id (c07), base or live")
}

// name is the point as the page spells it.
func (p point) name() string {
	switch {
	case p.live:
		return "live"
	case p.base:
		return "base"
	}
	return uiID(p.cp)
}
