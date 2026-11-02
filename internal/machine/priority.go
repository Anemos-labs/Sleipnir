package machine

import "os/exec"

// lowNice is the nice value of the agents' commands at low priority: below every program the person starts at the default 0,
// far from the idle class (19) that a busy machine would starve.
const lowNice = 10

// Start starts cmd, at low priority when the budget says so (Settings.Priority), so that a machine busy with the agents'
// builds still gives the person's own programs the CPU and the disk first. The priority is inherited by everything the
// command starts. A nil budget starts cmd as it is.
func (b *Budget) Start(cmd *exec.Cmd) error {
	if b == nil || !b.low {
		return cmd.Start()
	}
	return startLow(cmd)
}
