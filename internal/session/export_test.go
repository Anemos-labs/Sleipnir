package session

// TrustWrite is trustWrite, for the tests of package session_test: what the session's merge or memory tool calls before a write, and
// the renewal it calls with the files once they are written.
func (s *Session) TrustWrite() func(paths []string, by string) { return s.trustWrite() }
