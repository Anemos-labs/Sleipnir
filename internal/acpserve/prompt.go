package acpserve

import (
	"context"
	"encoding/json"
	"strings"
)

// promptParams are the fields of an ACP session/prompt request this agent uses.
type promptParams struct {
	SessionID string          `json:"sessionId"`
	Prompt    json.RawMessage `json:"prompt"`
}

// prompt starts a turn for a session, streams its updates and answers with the
// stop reason. One session runs one turn at a time.
func (s *Server) prompt(ctx context.Context, m *message) {
	var p promptParams
	if err := decodeParams(m, &p); err != nil {
		s.reply(m.ID, nil, fail(codeInvalidParams, "invalid session/prompt params: %v", err))
		return
	}
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		s.reply(m.ID, nil, fail(codeNotFound, "unknown session %s", p.SessionID))
		return
	}
	goal, err := promptText(p.Prompt)
	if err != nil {
		s.reply(m.ID, nil, fail(codeInvalidParams, "%v", err))
		return
	}
	if text := strings.TrimSpace(sess.SystemPrompt); text != "" {
		goal = "System instructions from the client:\n" + text + "\n\nGoal:\n" + goal
	}
	s.mu.Lock()
	running := 0
	for _, other := range s.sessions {
		if other.isActive() {
			running++
		}
	}
	if running >= s.cfg.MaxJobs {
		s.mu.Unlock()
		s.reply(m.ID, nil, fail(codeInvalidParams, "%d turns are running, the most this server runs at once", running))
		return
	}
	j := &Job{sess: sess, srv: s, goal: goal, done: make(chan struct{}), state: turnRunning}
	args, dir, err := j.runArgs(sess, goal, sess.resumeID())
	if err != nil {
		s.mu.Unlock()
		s.reply(m.ID, nil, fail(codeInvalidParams, "%v", err))
		return
	}
	j.args, j.dir = args, dir
	if !sess.setActive(j) {
		s.mu.Unlock()
		s.reply(m.ID, nil, fail(codeInvalidParams, "session %s is already processing a prompt", p.SessionID))
		return
	}
	s.mu.Unlock()
	if err := j.start(); err != nil {
		sess.clearActive(j)
		s.reply(m.ID, nil, fail(codeInternal, "could not start sleipnir: %v", err))
		return
	}
	select {
	case <-j.done:
	case <-ctx.Done():
		j.cancel()
		<-j.done
	}
	reason := j.stopReasonValue()
	s.reply(m.ID, map[string]any{"stopReason": reason}, nil)
}

// cancel ends the active turn of a session. The turn still sends its final
// updates and answers with the cancelled stop reason.
func (s *Server) cancel(m *message) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeParams(m, &p); err != nil {
		return // a notification gets no answer
	}
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess != nil {
		sess.cancelJob()
	}
}

// isActive reports whether the session has an in-flight turn.
func (s *Session) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil
}

// setActive installs j as the session's turn unless one is already running.
func (s *Session) setActive(j *Job) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		return false
	}
	s.active = j
	return true
}

// clearActive removes j if it is still the session's turn.
func (s *Session) clearActive(j *Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == j {
		s.active = nil
	}
}

// resumeID is the Sleipnir session to continue from.
func (s *Session) resumeID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResumeID
}

// closeSession and shutdown also remove the temporary MCP config.
func (s *Server) discardMCP(sess *Session) {
	path := sess.MCPFile
	sess.MCPFile = ""
	removeMCPFile(path)
}
