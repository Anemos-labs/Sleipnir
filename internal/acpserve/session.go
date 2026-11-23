package acpserve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// modeDescriptions are the user-facing names of Sleipnir's permission modes.
var modeDescriptions = map[string]string{
	"plan":         "Plan: describe the work without making changes",
	"default":      "Default: ask before actions that need permission",
	"accept-edits": "Accept edits: allow file edits without asking",
	"bypass":       "Bypass: run unattended with the configured allow rules",
	"yolo":         "Yolo: allow all actions",
}

// effortChoices are the reasoning levels a turn can use.
var effortChoices = []struct{ Value, Name string }{
	{"low", "Low"},
	{"medium", "Medium"},
	{"high", "High"},
}

// swarmChoices are the team sizes exposed as a select option.
var swarmChoices = []struct{ Value, Name string }{
	{"0", "One agent"},
	{"8", "Manager and 8 workers"},
	{"16", "Manager and 16 workers"},
	{"32", "Manager and 32 workers"},
}

// Session is one ACP conversation. Its fields that a turn changes are
// protected by mu; the rest are fixed at setup.
type Session struct {
	srv          *Server
	ID           string
	Cwd          string
	Dir          string
	SystemPrompt string
	MCPFile      string
	Title        string
	ResumeID     string
	UpdatedAt    time.Time

	mu      sync.Mutex
	values  map[string]string
	replay  []map[string]any
	active  *Job
	retired bool // replaced by a load/resume; no new turn may be admitted

	saveMu sync.Mutex // serializes snapshot and file commit for this session
}

// sessionRecord is the on-disk form of a Session.
type sessionRecord struct {
	ID        string            `json:"id"`
	Cwd       string            `json:"cwd"`
	Dir       string            `json:"dir"`
	Title     string            `json:"title"`
	ResumeID  string            `json:"resume_id,omitempty"`
	System    string            `json:"system_prompt,omitempty"`
	Values    map[string]string `json:"values"`
	UpdatedAt time.Time         `json:"updated_at"`
	Replay    []map[string]any  `json:"replay,omitempty"`
}

// recordIDPattern bounds an id before it is joined to a path.
var recordIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,80}$`)

// newSession creates a session with the server's default options.
func newSession(s *Server, dir, cwd, mcpFile, system string) *Session {
	values := map[string]string{
		"model": s.model, "mode": s.cfg.Mode, "swarm": "0", "effort": "medium",
	}
	return &Session{
		srv: s, ID: s.newID(), Cwd: cwd, Dir: dir, MCPFile: mcpFile,
		SystemPrompt: system, UpdatedAt: s.now(),
		values: values, replay: []map[string]any{},
	}
}

// sessionFromRecord restores a session from its persisted record.
func sessionFromRecord(s *Server, rec *sessionRecord, dir, mcpFile string) *Session {
	values := map[string]string{
		"model": s.model, "mode": s.cfg.Mode, "swarm": "0", "effort": "medium",
	}
	for k, v := range rec.Values {
		values[k] = v
	}
	return &Session{
		srv: s, ID: rec.ID, Cwd: rec.Cwd, Dir: dir, MCPFile: mcpFile,
		Title: rec.Title, ResumeID: rec.ResumeID, UpdatedAt: rec.UpdatedAt,
		SystemPrompt: rec.System,
		values:       values, replay: rec.Replay,
	}
}

// setupResult is the result of session/new, session/load and session/resume.
func (s *Session) setupResult() map[string]any {
	return map[string]any{
		"sessionId":     s.ID,
		"configOptions": s.configOptions(),
		"modes":         s.modes(),
	}
}

// value returns an option's current value under the lock.
func (s *Session) value(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[id]
}

// setValue changes an option if the id and value are one this agent offers.
func (s *Session) setValue(id, value string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	allowed := map[string][]string{}
	for _, m := range s.srv.models {
		allowed["model"] = append(allowed["model"], m.Value)
	}
	for _, m := range s.srv.cfg.ModeOptions() {
		allowed["mode"] = append(allowed["mode"], m)
	}
	allowed["swarm"] = []string{"0", "8", "16", "32"}
	allowed["effort"] = []string{"low", "medium", "high"}
	values, ok := allowed[id]
	if !ok {
		return false
	}
	for _, v := range values {
		if v == value {
			s.values[id] = value
			s.UpdatedAt = s.srv.now()
			return true
		}
	}
	return false
}

// configOptions are the ACP selectors shown by the client.
func (s *Session) configOptions() []map[string]any {
	modelOpts := make([]map[string]any, 0, len(s.srv.models))
	for _, m := range s.srv.models {
		modelOpts = append(modelOpts, map[string]any{"value": m.Value, "name": m.Name})
	}
	modeOpts := make([]map[string]any, 0)
	for _, id := range s.srv.cfg.ModeOptions() {
		name, ok := modeDescriptions[id]
		if !ok {
			name = id
		}
		modeOpts = append(modeOpts, map[string]any{"value": id, "name": name})
	}
	swarmOpts := make([]map[string]any, 0, len(swarmChoices))
	for _, c := range swarmChoices {
		swarmOpts = append(swarmOpts, map[string]any{"value": c.Value, "name": c.Name})
	}
	effortOpts := make([]map[string]any, 0, len(effortChoices))
	for _, c := range effortChoices {
		effortOpts = append(effortOpts, map[string]any{"value": c.Value, "name": c.Name})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return []map[string]any{
		{"id": "model", "name": "Model", "category": "model", "type": "select",
			"currentValue": s.values["model"], "options": modelOpts},
		{"id": "mode", "name": "Permission mode", "category": "mode", "type": "select",
			"currentValue": s.values["mode"], "options": modeOpts},
		{"id": "swarm", "name": "Team", "category": "_sleipnir", "type": "select",
			"currentValue": s.values["swarm"], "options": swarmOpts},
		{"id": "effort", "name": "Reasoning", "category": "thought_level", "type": "select",
			"currentValue": s.values["effort"], "options": effortOpts},
	}
}

// modes is the legacy session-mode state kept in sync with the mode option.
func (s *Session) modes() map[string]any {
	available := make([]map[string]any, 0)
	for _, id := range s.srv.cfg.ModeOptions() {
		name, ok := modeDescriptions[id]
		if !ok {
			name = id
		}
		available = append(available, map[string]any{"id": id, "name": name})
	}
	return map[string]any{"currentModeId": s.value("mode"), "availableModes": available}
}

// cancelJob signals the session's active turn, if any. The turn stays active
// until its own completion path settles it, so another prompt cannot start
// while its child is still emitting updates or saving its result.
func (s *Session) cancelJob() *Job {
	s.mu.Lock()
	j := s.active
	s.mu.Unlock()
	if j != nil {
		j.cancel()
	}
	return j
}

// addReplay keeps a bounded copy of an update for session/load.
func (s *Session) addReplay(update map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replay = append(s.replay, update)
	if len(s.replay) > maxReplayUpdates {
		s.replay = append(s.replay[:0:0], s.replay[len(s.replay)-maxReplayUpdates:]...)
	}
	s.UpdatedAt = s.srv.now()
}

// record is a consistent snapshot for persistence.
func (s *Session) record() sessionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make(map[string]string, len(s.values))
	for k, v := range s.values {
		values[k] = v
	}
	replay := append([]map[string]any(nil), s.replay...)
	return sessionRecord{
		ID: s.ID, Cwd: s.Cwd, Dir: s.Dir, Title: s.Title, ResumeID: s.ResumeID,
		System: s.SystemPrompt, Values: values, UpdatedAt: s.UpdatedAt, Replay: replay,
	}
}

// save persists this session's metadata and replay. The per-session lock keeps
// an older snapshot from being committed after a newer one.
func (s *Session) save() {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.srv.save(s)
}

// recordPath is the file of a session's persisted state.
func (s *Server) recordPath(id string) string {
	return filepath.Join(s.stateDir, id+".json")
}

// save writes a session's record atomically.
func (s *Server) save(sess *Session) {
	rec := sess.record()
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	path := s.recordPath(rec.ID)
	tmp, err := os.CreateTemp(s.stateDir, ".acp-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(name, path)
}

// readRecord loads a session record by id.
func (s *Server) readRecord(id string) (*sessionRecord, error) {
	if !recordIDPattern.MatchString(id) {
		return nil, errors.New("invalid session id")
	}
	b, err := os.ReadFile(s.recordPath(id))
	if err != nil {
		return nil, err
	}
	var rec sessionRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, err
	}
	if rec.ID != id || rec.Dir == "" {
		return nil, errors.New("bad session record")
	}
	return &rec, nil
}

// records lists every readable session record, newest first.
func (s *Server) records() ([]*sessionRecord, error) {
	entries, err := os.ReadDir(s.stateDir)
	if err != nil {
		return nil, err
	}
	recs := make([]*sessionRecord, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if rec, err := s.readRecord(id); err == nil {
			recs = append(recs, rec)
		}
	}
	sortRecords(recs)
	return recs, nil
}

// systemText returns the text of an ACP systemPrompt. Only append mode and
// text blocks are supported; other shapes are ignored with a notice.
func systemText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var p struct {
		Mode    string `json:"mode"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Mode != "append" {
		return ""
	}
	parts := make([]string, 0, len(p.Content))
	for _, c := range p.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// promptText builds a goal from an ACP prompt: text and resources become
// plain text, in the order the client sent them.
func promptText(raw json.RawMessage) (string, error) {
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Name     string `json:"name"`
		URI      string `json:"uri"`
		Resource struct {
			URI  string `json:"uri"`
			Text string `json:"text"`
		} `json:"resource"`
	}
	if len(raw) == 0 {
		return "", errors.New("prompt is empty")
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("invalid prompt: %w", err)
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case "resource_link":
			if b.URI != "" {
				parts = append(parts, "Reference: "+b.URI+" ("+b.Name+")")
			}
		case "resource":
			if b.Resource.URI != "" {
				parts = append(parts, "File: "+b.Resource.URI+"\n"+b.Resource.Text)
			}
		}
	}
	goal := strings.Join(parts, "\n\n")
	if strings.TrimSpace(goal) == "" {
		return "", errors.New("prompt has no text the harness can use")
	}
	return goal, nil
}
