package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/runner"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// recordedRow is a recorded session as the Sessions view and the Resume dialog read it: wire.RecordedSession, plus whether it is
// being written now and how the end of its isolated run went.
type recordedRow struct {
	wire.RecordedSession
	Live        bool                 `json:"live,omitempty"`
	Integration *session.Integration `json:"integration,omitempty"`
}

// maxFirstShown bounds the first prompt of a row, in runes.
const maxFirstShown = 200

// rowOf turns a listing row into the page's.
func rowOf(r session.Recorded, now time.Time) recordedRow {
	first := r.First
	if utf8.RuneCountInString(first) > maxFirstShown {
		first = string([]rune(first)[:maxFirstShown]) + "…"
	}
	row := recordedRow{RecordedSession: wire.RecordedSession{
		ID: r.ID, Name: runner.Clean(r.Name), First: runner.Clean(first), Model: runner.Clean(r.Model), Cost: r.CostUSD,
		MB: mb(r.Bytes), AgeS: max(now.Sub(r.LastWritten).Seconds(), 0), Agents: r.Agents, Resumable: r.Resumable,
		Dur: r.Duration.Seconds(), Interrupted: r.Interrupted, Cwd: r.Cwd, LastWritten: r.LastWritten.UnixMilli(), Locked: r.Locked,
	}, Live: r.Live}
	if r.Integration != nil {
		in := *r.Integration
		in.Message, in.Hint, in.Branch = runner.Clean(in.Message), runner.Clean(in.Hint), runner.Clean(in.Branch)
		row.Integration = &in
	}
	return row
}

// mb is a size in mebibytes, to a hundredth.
func mb(bytes int64) float64 {
	v := float64(bytes) / (1 << 20)
	return float64(int64(v*100+0.5)) / 100
}

// registerRecorded adds the routes of the recorded sessions and the following of a session another process writes.
func (s *service) registerRecorded() {
	s.srv.HandleFunc("GET /api/recorded", s.handleRecorded, web.RouteOpts{WriteTimeout: 60 * time.Second})
	s.srv.HandleFunc("POST /api/recorded/prune", s.handlePrune, web.RouteOpts{WriteTimeout: 10 * time.Minute})
	s.srv.HandleFunc("POST /api/recorded/delete", s.handleDelete, web.RouteOpts{WriteTimeout: 10 * time.Minute})
	s.srv.HandleFunc("GET /api/recorded/{sid}/events", s.handleEvents, web.RouteOpts{WriteTimeout: 60 * time.Second})
	s.srv.HandleFunc("POST /api/recorded/{sid}/watch", s.handleWatch, web.RouteOpts{NoBody: true})
	s.srv.HandleFunc("DELETE /api/recorded/{sid}/watch", s.handleUnwatch, web.RouteOpts{NoBody: true})
	s.srv.HandleFunc("GET /api/recorded/watching", func(w http.ResponseWriter, _ *http.Request) {
		_ = web.WriteJSON(w, http.StatusOK, map[string]any{"tabs": s.watching()})
	}, web.RouteOpts{})
}

// handleRecorded is GET /api/recorded: the session directories no tab hosts, newest first, and their size in all.
func (s *service) handleRecorded(w http.ResponseWriter, r *http.Request) {
	now := s.o.Now()
	list, err := session.ListRecorded(s.home(), now)
	if err != nil {
		web.Logf(r, "listing the sessions: %v", err)
		web.Error(w, http.StatusInternalServerError, "internal", "the sessions could not be listed")
		return
	}
	hosted := s.hostedSIDs()
	rows := []recordedRow{}
	var total int64
	for _, rec := range list {
		if _, ok := hosted[rec.ID]; ok {
			continue
		}
		rows = append(rows, rowOf(rec, now))
		total += rec.Bytes
	}
	_ = web.WriteJSON(w, http.StatusOK, map[string]any{"recorded": rows, "mb": mb(total)})
}

// pruneRequest is wire.PruneRequest.
type pruneRequest = wire.PruneRequest

// planReply is the PrunePlan of a prune or a delete as the page reads it, plus what it left alone and why.
type planReply struct {
	List    []recordedRow `json:"list"`
	MB      float64       `json:"mb"`
	Applied bool          `json:"applied"`
	Error   string        `json:"error,omitempty"`
	Kept    []string      `json:"branchesKept,omitempty"`
	InUse   []string      `json:"inUse,omitempty"`
	Locked  []string      `json:"locked,omitempty"`
	Total   int           `json:"total,omitempty"`
	Newest  int           `json:"keptNewest,omitempty"`
}

// maxKeep bounds --keep.
const maxKeep = 1 << 20

// handlePrune is POST /api/recorded/prune: the plan of `sessions prune` (apply false), or its deletion (apply true, confirmed for
// exactly the sessions it deletes now).
func (s *service) handlePrune(w http.ResponseWriter, r *http.Request) {
	var body pruneRequest
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	older := strings.TrimSpace(body.OlderThan)
	if older == "" {
		older = "30d"
	}
	age, err := session.ParseAge(older)
	if err != nil || len(older) > 32 {
		web.Error(w, http.StatusBadRequest, "bad_age", "bad --older-than: "+strconv.Quote(clip(older, 32))+" is not an age (try 30d, 36h or 2w)")
		return
	}
	if body.Keep < 0 || body.Keep > maxKeep {
		web.Error(w, http.StatusBadRequest, "bad_flags", "--keep must be a number of sessions, 0 or more")
		return
	}
	now := s.o.Now()
	hosted := s.hostedSIDs()
	live := make([]string, 0, len(hosted))
	for sid := range hosted {
		live = append(live, sid)
	}
	sort.Strings(live)
	plan, err := session.PlanPrune(s.home(), now, age, body.Keep, live)
	if err != nil {
		web.Logf(r, "planning a prune: %v", err)
		web.Error(w, http.StatusInternalServerError, "internal", "the sessions could not be read")
		return
	}
	reply := planReply{List: []recordedRow{}, MB: mb(plan.Bytes), InUse: plan.InUse, Locked: plan.Locked, Total: plan.Total, Newest: plan.KeptNewest}
	ids := make([]string, 0, len(plan.Delete))
	for _, rec := range plan.Delete {
		reply.List = append(reply.List, rowOf(rec, now))
		ids = append(ids, rec.ID)
	}
	if !body.Apply {
		_ = web.WriteJSON(w, http.StatusOK, reply)
		return
	}
	if len(ids) == 0 {
		reply.Applied = true
		_ = web.WriteJSON(w, http.StatusOK, reply)
		return
	}
	sort.Strings(ids)
	if !s.confirmOr(w, r, "prune:"+runner.D16(ids), map[string]any{"plan": reply}) {
		return
	}
	s.remove(w, r, ids, reply.List)
}

// remove deletes confirmed sessions and answers with what went, what was kept and what could not be deleted.
func (s *service) remove(w http.ResponseWriter, r *http.Request, ids []string, rows []recordedRow) {
	byID := map[string]recordedRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	// a page that goes away does not stop a deletion halfway: each session's salvage finishes or leaves it whole
	done := session.RemoveRecorded(context.WithoutCancel(r.Context()), s.home(), ids)
	reply := planReply{List: []recordedRow{}, Applied: true}
	var failed []string
	var freed int64
	for _, rm := range done {
		if rm.Err != nil {
			web.Logf(r, "deleting session %s: %v", rm.ID, rm.Err)
			why := "could not be deleted"
			if strings.Contains(rm.Err.Error(), "in use") {
				why = "is in use by another process"
			}
			failed = append(failed, rm.ID+" "+why)
			continue
		}
		freed += rm.Bytes
		if row, ok := byID[rm.ID]; ok {
			reply.List = append(reply.List, row)
		} else {
			reply.List = append(reply.List, recordedRow{RecordedSession: wire.RecordedSession{ID: rm.ID, MB: mb(rm.Bytes)}})
		}
		for _, k := range rm.Kept {
			reply.Kept = append(reply.Kept, runner.Clean(fmt.Sprintf("kept Git branch %s: %s", k.Branch, k.Reason)))
		}
	}
	reply.MB = mb(freed)
	if len(failed) > 0 {
		reply.Error = fmt.Sprintf("%d of %d sessions could not be deleted: %s", len(failed), len(ids), strings.Join(failed, "; "))
	}
	web.Logf(r, "deleted %d recorded sessions", len(reply.List))
	s.publish(wire.Frame{Type: "recorded", Data: struct{}{}})
	_ = web.WriteJSON(w, http.StatusOK, reply)
}

// maxDelete bounds the sessions one delete names.
const maxDelete = 1000

// handleDelete is POST /api/recorded/delete: the sessions the person chose, with prune's safety.
func (s *service) handleDelete(w http.ResponseWriter, r *http.Request) {
	var body wire.DeleteRecordedRequest
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 || len(body.IDs) > maxDelete {
		web.Error(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("name 1 to %d sessions", maxDelete))
		return
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(body.IDs))
	for _, id := range body.IDs {
		if !session.ValidID(id) {
			web.Error(w, http.StatusBadRequest, "bad_request", "that is not a session id: "+strconv.Quote(clip(id, 40)))
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	hosted := s.hostedSIDs()
	var inTab, locked, missing []string
	root := session.SessionsDir(s.home())
	now := s.o.Now()
	rows := make([]recordedRow, 0, len(ids))
	list, _ := session.ListRecorded(s.home(), now)
	byID := map[string]session.Recorded{}
	for _, rec := range list {
		byID[rec.ID] = rec
	}
	for _, id := range ids {
		if _, ok := hosted[id]; ok {
			inTab = append(inTab, id)
			continue
		}
		rec, ok := byID[id]
		if !ok {
			if fi, err := os.Lstat(filepath.Join(root, id)); err != nil || !fi.IsDir() {
				missing = append(missing, id)
				continue
			}
			rec = session.Recorded{ID: id, Dir: filepath.Join(root, id)}
		}
		if session.Locked(rec.Dir) {
			locked = append(locked, id)
			continue
		}
		rows = append(rows, rowOf(rec, now))
	}
	switch {
	case len(missing) > 0:
		web.ErrorDetail(w, http.StatusNotFound, "not_found", "no such recorded session: "+strings.Join(missing, ", "), map[string]any{"ids": missing})
		return
	case len(inTab) > 0:
		web.ErrorDetail(w, http.StatusConflict, "hosted", "open in a tab: close it first ("+strings.Join(inTab, ", ")+")", map[string]any{"ids": inTab})
		return
	case len(locked) > 0:
		web.ErrorDetail(w, http.StatusConflict, "locked", "another process is writing it: "+strings.Join(locked, ", "), map[string]any{"ids": locked})
		return
	}
	if !s.srv.RequireConfirm(w, r, "delete:"+runner.D16(ids)) {
		return
	}
	s.remove(w, r, ids, rows)
}

// clip cuts s at n bytes for a message.
func clip(s string, n int) string {
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "") + "..."
	}
	return s
}

// sessionDir resolves a {sid} path parameter: a valid id of a session directory of the state directory.
func (s *service) sessionDir(sid string) (string, error) {
	if !session.ValidID(sid) {
		return "", werr(http.StatusBadRequest, "bad_request", "that is not a session id")
	}
	dir := filepath.Join(session.SessionsDir(s.home()), sid)
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return "", werr(http.StatusNotFound, "not_found", "no such recorded session")
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil {
		return "", werr(http.StatusNotFound, "not_found", "no such recorded session")
	}
	return dir, nil
}

// replayCache keeps the translations of the last few recorded sessions asked for, keyed by the log's size and time.
type replayCache struct {
	mu      sync.Mutex
	entries []replayEntry
}

// replayEntry is one translated log.
type replayEntry struct {
	dir  string
	size int64
	mod  time.Time
	evs  []json.RawMessage
}

// maxReplays is how many translated logs are kept.
const maxReplays = 4

// get returns the translation of a log, translating it when the cache does not have it as the log is now.
func (c *replayCache) get(ctx context.Context, dir string, translate func(context.Context, string) ([]json.RawMessage, error)) ([]json.RawMessage, error) {
	fi, err := os.Stat(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	for _, e := range c.entries {
		if e.dir == dir && e.size == fi.Size() && e.mod.Equal(fi.ModTime()) {
			c.mu.Unlock()
			return e.evs, nil
		}
	}
	c.mu.Unlock()
	evs, err := translate(ctx, dir)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, replayEntry{dir: dir, size: fi.Size(), mod: fi.ModTime(), evs: evs})
	if len(c.entries) > maxReplays {
		c.entries = c.entries[len(c.entries)-maxReplays:]
	}
	return evs, nil
}

// Page sizes of GET /api/recorded/{sid}/events.
const (
	defaultEventsPage = 2000
	maxEventsPage     = 5000
)

// handleEvents is GET /api/recorded/{sid}/events?from=&limit=: a recorded session's history in the page's UI events (the Replay
// view of a read-only tab plays it), a page at a time; next is where the following page starts ("" at the end).
func (s *service) handleEvents(w http.ResponseWriter, r *http.Request) {
	dir, err := s.sessionDir(r.PathValue("sid"))
	if err != nil {
		web.WriteError(w, err)
		return
	}
	q := r.URL.Query()
	from, limit := 0, defaultEventsPage
	if v := q.Get("from"); v != "" {
		if from, err = strconv.Atoi(v); err != nil || from < 0 {
			web.Error(w, http.StatusBadRequest, "bad_request", "from must be an event number")
			return
		}
	}
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 {
			web.Error(w, http.StatusBadRequest, "bad_request", "limit must be a number of events")
			return
		}
		limit = min(limit, maxEventsPage)
	}
	if s.o.Replay == nil {
		web.Error(w, http.StatusNotImplemented, "not_implemented", "the replay of a recorded session is not available in this build: sleipnir replay <id> in a terminal plays it")
		return
	}
	evs, err := s.replays.get(r.Context(), dir, s.o.Replay)
	if err != nil {
		web.Logf(r, "translating a recorded session: %v", err)
		web.Error(w, http.StatusInternalServerError, "internal", "the recorded session could not be read")
		return
	}
	page := []json.RawMessage{}
	next := ""
	if from < len(evs) {
		end := min(from+limit, len(evs))
		page = evs[from:end]
		if end < len(evs) {
			next = strconv.Itoa(end)
		}
	}
	_ = web.WriteJSON(w, http.StatusOK, map[string]any{"events": page, "next": next})
}
