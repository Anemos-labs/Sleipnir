package session

import "github.com/anemos-labs/sleipnir/internal/tools/shell"

// Jobs lists the background jobs the session's agents started (a dev server, a watcher, a long test run), oldest first, with the
// local addresses their output named. A session without a shell has none.
func (s *Session) Jobs() []shell.JobInfo {
	if s == nil {
		return nil
	}
	return s.shell.Jobs()
}

// StopJob ends one of those jobs and everything it started, for the person; the agents stop theirs with bash_kill.
func (s *Session) StopJob(id string) error {
	if s == nil {
		return (*shell.Manager)(nil).StopJob(id)
	}
	return s.shell.StopJob(id)
}
