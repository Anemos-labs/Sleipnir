package session

import (
	"encoding/json"
	"path/filepath"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
)

// keepPermissions writes the permission mode and the allow rules given so far into the log when a turn ends and they are not what was written
// last. A resumed session reads them back (restorePermissions): what a person said yes to once is not asked again after --continue.
func (s *Session) keepPermissions() {
	if s.Perm == nil || s.Log == nil {
		return
	}
	st := map[string]any{"mode": string(s.Perm.Mode()), "allow": s.Perm.Granted()}
	b, _ := json.Marshal(st)
	s.mu.Lock()
	same := s.lastPerm == string(b)
	s.lastPerm = string(b)
	s.mu.Unlock()
	if !same {
		s.Log.Emit("", events.TypePermState, st)
	}
}

// restorePermissions gives a resumed session the rules its log ends with. A mode the person chose on this command line (anything but the default)
// wins over the one in the log, and bypass and yolo are never brought back: they are said again, on purpose, each time.
func (s *Session) restorePermissions() {
	var last struct {
		Mode  string   `json:"mode"`
		Allow []string `json:"allow"`
	}
	found := false
	_ = events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
		if e.Type == events.TypePermState && json.Unmarshal(e.Data, &last) == nil {
			found = true
		}
		return nil
	})
	if !found {
		return
	}
	if m := perm.Mode(last.Mode); s.Perm.Mode() == perm.ModeDefault && m != perm.ModeBypass && m != perm.ModeYolo && m != "" && m != perm.ModeDefault {
		s.Perm.SetMode(m)
		s.restoredMode = last.Mode
	}
	for _, r := range last.Allow {
		if rule, err := perm.ParseRule(perm.Allow, r); err == nil {
			s.Perm.AddRule(perm.ScopeSession, rule)
			s.restoredRules++
		}
	}
}

// RestoredPermissions says what a resumed session got back from its log: the mode (when it was not the default) and how many allow rules.
func (s *Session) RestoredPermissions() (mode string, rules int) {
	return s.restoredMode, s.restoredRules
}
