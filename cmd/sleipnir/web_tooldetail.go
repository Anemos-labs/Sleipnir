package main

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/web/translate"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// toolDetailSlots bounds the tool-call reads that scan a session's log at once: each reads the whole log, so a page that asks
// for many at once waits instead of making the server read the log many times over.
var toolDetailSlots = make(chan struct{}, 2)

// toolDetail is one tool call of the tab's session read whole from its log (translate.ToolDetail): the whole command and output
// that a tool row shows a line of. agent is the agent as the page names it (mgr for the main agent) and tid the call's id. 404
// no_call when the log holds no such call, 409 no_session when the tab has no session.
func (t *webTab) toolDetail(agent, tid string) (wire.ToolDetail, error) {
	agent, tid = strings.TrimSpace(agent), strings.TrimSpace(tid)
	if agent == "" || tid == "" || len(agent) > 128 || len(tid) > 256 {
		return wire.ToolDetail{}, werr(http.StatusBadRequest, "bad_request", "a tool call is named by its agent and its id")
	}
	s := t.session()
	if s == nil || s.Dir == "" {
		return wire.ToolDetail{}, werr(http.StatusConflict, "no_session", "the tab has no session to read")
	}
	toolDetailSlots <- struct{}{}
	defer func() { <-toolDetailSlots }()
	if s.Log != nil {
		_ = s.Log.Flush() // what the agent did a moment ago is in the file before it is read
	}
	d, err := translate.ToolDetail(eventsPath(s.Dir), s.Blobs, s.Root(), agent, tid)
	switch {
	case errors.Is(err, translate.ErrNoToolCall):
		return d, werr(http.StatusNotFound, "no_call", "this session's log holds no call "+clip(tid, 60)+" of "+clip(agent, 40)+": it may belong to an earlier run of the tab")
	case errors.Is(err, os.ErrNotExist):
		return d, werr(http.StatusNotFound, "no_call", "the session has no log yet")
	case err != nil:
		return d, err
	}
	return d, nil
}
