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

// Strays lists the process groups the agents' finished commands left running (a server started with "server &" inside an
// ordinary command), oldest first. StopJob stops one by its id. A session without a shell has none.
func (s *Session) Strays() []shell.StrayInfo {
	if s == nil {
		return nil
	}
	return s.shell.Strays()
}

// JobOutput is the end of a background job's output, at most max bytes, and how many earlier bytes it leaves out; reading it
// does not move any agent's read position.
func (s *Session) JobOutput(id string, max int) (string, int64, error) {
	if s == nil {
		return (*shell.Manager)(nil).JobOutput(id, max)
	}
	return s.shell.JobOutput(id, max)
}

// JobCommand is a background job's whole command, as the agent wrote it.
func (s *Session) JobCommand(id string) (string, error) {
	if s == nil {
		return (*shell.Manager)(nil).JobCommand(id)
	}
	return s.shell.JobCommand(id)
}
