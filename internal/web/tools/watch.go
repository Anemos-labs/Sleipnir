package tools

import (
	"context"
	"net/http"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/runner"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Watching a session another process writes (PARITY.md A7): a read-only tab whose events come from following the session's log as
// it is written (a run of the runner, a daemon job, a chat in a terminal), as `sleipnir watch` follows it. The tab has no composer
// and no question buttons: the run belongs to the other process. Its id is "w-" and the session id; its summary says headless.

// watch is one followed session.
type watch struct {
	tab    wire.TabSummary
	cancel context.CancelFunc
	done   chan struct{}
}

// maxWatches bounds the sessions followed at once.
const maxWatches = 8

// watchTab is the id of the read-only tab of a session.
func watchTab(sid string) string { return "w-" + sid }

// watching lists the followed sessions' tabs, oldest first.
func (s *service) watching() []wire.TabSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]wire.TabSummary, 0, len(s.watches))
	for _, w := range s.watches {
		out = append(out, w.tab)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// handleWatch is POST /api/recorded/{sid}/watch: follow a session read-only in a tab of its own (201 {tab}; 200 when it is followed
// already). A session hosted in a tab is 409 hosted (detail {tab}: switch to it).
func (s *service) handleWatch(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	dir, err := s.sessionDir(sid)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	if tab, ok := s.hostedSIDs()[sid]; ok {
		web.ErrorDetail(w, http.StatusConflict, "hosted", "this session is open in a tab", map[string]string{"tab": tab})
		return
	}
	if s.o.Follow == nil {
		web.Error(w, http.StatusNotImplemented, "not_implemented", "following a session is not available in this build: sleipnir watch <id> in a terminal follows it")
		return
	}
	s.mu.Lock()
	if cur, ok := s.watches[sid]; ok {
		s.mu.Unlock()
		_ = web.WriteJSON(w, http.StatusOK, map[string]any{"tab": cur.tab})
		return
	}
	if len(s.watches) >= maxWatches {
		s.mu.Unlock()
		web.Error(w, http.StatusConflict, "limit", "too many sessions are followed: close one first")
		return
	}
	name := "watching " + sid
	if m, err := session.LoadMeta(dir); err == nil && m.Name != "" {
		name = runner.Clean(m.Name)
	}
	sum := session.Summarize(filepath.Join(dir, "events.jsonl"))
	ctx, cancel := context.WithCancel(context.Background())
	wt := &watch{
		tab:    wire.TabSummary{ID: watchTab(sid), SID: sid, Name: name, Cwd: sum.Cwd, Headless: true, CreatedAt: s.o.Now().UnixMilli(), Order: 1000 + len(s.watches)},
		cancel: cancel, done: make(chan struct{}),
	}
	s.watches[sid] = wt
	s.mu.Unlock()
	s.publish(wire.Frame{Type: "tab", Tab: wt.tab.ID, Data: wire.TabFrame{Op: "add", Tab: wt.tab}, Critical: true})
	go func() {
		defer close(wt.done)
		if err := s.o.Follow(ctx, wt.tab.ID, dir, s.publish); err != nil && ctx.Err() == nil {
			s.srv.Logf("following session %s: %v", sid, err)
		}
		if ctx.Err() == nil { // the session ended: the tab stays, no longer live
			s.mu.Lock()
			wt.tab.Headless = false
			tab := wt.tab
			s.mu.Unlock()
			s.publish(wire.Frame{Type: "tab", Tab: tab.ID, Data: wire.TabFrame{Op: "update", Tab: tab}, Critical: true})
		}
	}()
	_ = web.WriteJSON(w, http.StatusCreated, map[string]any{"tab": wt.tab})
}

// handleUnwatch is DELETE /api/recorded/{sid}/watch: stop following and remove the tab.
func (s *service) handleUnwatch(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	if !session.ValidID(sid) {
		web.Error(w, http.StatusBadRequest, "bad_request", "that is not a session id")
		return
	}
	s.mu.Lock()
	wt, found := s.watches[sid]
	delete(s.watches, sid)
	s.mu.Unlock()
	if !found {
		web.Error(w, http.StatusNotFound, "not_found", "this session is not followed")
		return
	}
	wt.cancel()
	select {
	case <-wt.done:
	case <-time.After(5 * time.Second):
	}
	s.publish(wire.Frame{Type: "tab", Tab: wt.tab.ID, Data: wire.TabFrame{Op: "remove", Tab: wt.tab}, Critical: true})
	ok(w)
}

// stopWatches stops following every session.
func (s *service) stopWatches() {
	s.mu.Lock()
	all := s.watches
	s.watches = map[string]*watch{}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, wt := range all {
		wt.cancel()
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-wt.done:
			case <-time.After(5 * time.Second):
			}
		}()
	}
	wg.Wait()
}
