package session

import (
	"encoding/json"
	"path/filepath"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// SetEffort updates current and future agents without replacing conversations.
// The requested preference is saved with the session and mapped per model.
func (s *Session) SetEffort(value string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := s.effort.Set(value)
	if s.Log != nil {
		_, _ = s.Log.Emit("", "session.effort", map[string]string{"effort": want})
	}
	return want
}

// Effort returns the requested setting and its current mapping for the primary
// model. Other team roles map the same preference onto their own model's range.
func (s *Session) Effort() (requested, applied string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	requested = s.effort.Requested()
	applied = requested
	if s.Provider != nil {
		applied = s.effort.Params(s.Provider, s.Model.ID, core.Params{}).Effort
	}
	return requested, applied
}

// restoreEffort restores the most recent saved preference. Endpoint corrections
// are intentionally rediscovered because a provider's accepted levels can change.
func (s *Session) restoreEffort() {
	_ = events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
		var state struct {
			Effort *string `json:"effort"`
		}
		if e.Type == "session.effort" && json.Unmarshal(e.Data, &state) == nil && state.Effort != nil {
			s.effort.Set(*state.Effort)
		}
		return nil
	})
}
