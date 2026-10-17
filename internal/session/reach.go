package session

import (
	"context"
	"net/url"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

// checkReach asks whether the main model's endpoint can be reached at all, once, when an interactive session starts, and says
// so with a notice when it cannot: without it a session reads as ready until its first request has spent its retries on an
// endpoint the network never let it talk to. It runs beside the start and never holds it up; a session closed by the time
// the answer comes says nothing.
func (s *Session) checkReach() {
	if s.Provider == nil {
		return
	}
	base := s.Provider.Profile().BaseURL
	e := provider.Reach(context.Background(), nil, base)
	if e == nil {
		return
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	host := base
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		host = u.Host
	}
	msg := "cannot reach " + host + ": " + e.Message
	if !e.Retryable() {
		msg += "; requests to it will fail until that changes"
	}
	s.notice("", msg)
}
