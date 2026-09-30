package session

// Mailman mode (swarm.mailman, Options.Mailman): worker mail is digested by a mailman
// agent instead of being delivered message by message (docs/SWARM-PROTOCOL.md section 5).
// The swarm does the work (internal/swarm/mailman.go); the session's part is to decide
// whether it is on and to make the surroundings agree: the mailman's role name is
// reserved from project agent definitions (ext.go) and the permission engine holds the
// role to the plan profile (buildPerm), on top of the swarm's own restriction to the
// mail tool. Its model is the session's unless RoleModels names one for "mailman".

// mailmanOn reports whether this session routes worker mail through a mailman: the
// option when it is set, the configuration otherwise, and only for a swarm.
func (s *Session) mailmanOn() bool {
	if !s.opts.Swarm {
		return false
	}
	if s.opts.Mailman != nil {
		return *s.opts.Mailman
	}
	return s.cfg != nil && s.cfg.Swarm.Mailman
}
