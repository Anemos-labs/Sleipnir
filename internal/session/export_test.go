package session

// RenewTrust is renewTrust, for the tests of package session_test: what the session's merge or memory tool calls with the files they wrote.
func (s *Session) RenewTrust(paths []string, by string) { s.renewTrust(paths, by) }
