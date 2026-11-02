package session

import "github.com/anemos-labs/sleipnir/internal/swarm"

// StopRequest reports whether a message asks to stop the whole team now (swarm.StopRequest): Run stops the team instead of running the
// manager on it.
func StopRequest(text string) bool { return swarm.StopRequest(text) }

// StopTeam stops the whole team on the person's behalf (swarm.StopAll): words is what they said ("" for a command or a button).
// It returns the line for the person and whether there was a team to stop.
func (s *Session) StopTeam(words string) (string, bool) {
	if s == nil || s.Swarm == nil {
		return "", false
	}
	rep := s.Swarm.StopAll(words)
	return rep.Line(), true
}
