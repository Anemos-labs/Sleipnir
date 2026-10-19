package state

import (
	"sort"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Event types of the plugin set (internal/session/plugins.go).
const (
	TypePlugins     = "plugins"      // the profile and the plugins that are not on, once at the start
	TypePluginState = "plugin.state" // one plugin pending or in error, with the reason
)

// Plugins is what a session's log says about its plugins.
type Plugins struct {
	Profile string
	// Off lists the plugins that were not on when the session started, sorted.
	Off []string
	// Problems maps a plugin that is pending or in error to "state: reason".
	Problems map[string]string
	T        time.Time
}

// onPlugins is the plugins event: the session's profile and its plugins that are off.
func (s *State) onPlugins(e events.Event, t time.Time) {
	var p struct {
		Profile string   `json:"profile"`
		Off     []string `json:"off"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	pl := Plugins{Profile: clean(p.Profile, textID), T: t, Problems: map[string]string{}}
	for i, id := range p.Off {
		if i >= 256 {
			break
		}
		pl.Off = append(pl.Off, clean(id, textID))
	}
	sort.Strings(pl.Off)
	if s.plugins != nil {
		pl.Problems = s.plugins.Problems
	}
	s.plugins = &pl
}

// onPluginState is a plugin that is pending or failed.
func (s *State) onPluginState(e events.Event, t time.Time) {
	var p struct {
		ID, State, Reason string
	}
	if !s.decode(e.Data, maxPayload, &p) || p.ID == "" {
		return
	}
	if s.plugins == nil {
		s.plugins = &Plugins{Problems: map[string]string{}, T: t}
	}
	if len(s.plugins.Problems) < 256 {
		s.plugins.Problems[clean(p.ID, textID)] = clean(p.State, textID) + ": " + clean(p.Reason, textLine)
	}
}

// Plugins returns what the log said about the session's plugins (nil when it said nothing: an older log).
func (s *State) Plugins() *Plugins {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.plugins == nil {
		return nil
	}
	c := *s.plugins
	c.Off = append([]string(nil), s.plugins.Off...)
	c.Problems = map[string]string{}
	for k, v := range s.plugins.Problems {
		c.Problems[k] = v
	}
	return &c
}
