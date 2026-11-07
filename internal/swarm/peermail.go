package swarm

import (
	"context"
	"errors"

	"github.com/anemos-labs/sleipnir/internal/agent"
)

// RunManagerMail runs the manager on a message from outside the team (another session of the same program, framed as mail by
// the host): the message is queued for the manager's first step and read as mail, never as the person's input. Unlike
// RunManager it is not the person writing: the count of automatic runs is not reset and a stop the person asked for is not
// lifted. When the manager is already running (a wake), the message joins that run instead and nil, nil is returned; a run of
// its own would have to wait for it. An empty mail runs the manager on what already waits in its inbox.
func (s *Swarm) RunManagerMail(ctx context.Context, mail string) (*agent.Result, error) {
	if s.Stopped() {
		return nil, errors.New("the person stopped this team: it takes no message until they write to it")
	}
	a, err := s.StartManager()
	if err != nil {
		return nil, err
	}
	m := s.get(a.ID())
	if m == nil {
		return nil, errors.New("the manager is gone")
	}
	if !s.reserveManager(m) {
		if mail != "" {
			a.Send(mail) // the running manager reads it with its next step
		}
		return nil, nil
	}
	return s.runManager(ctx, m, "", mail)
}
