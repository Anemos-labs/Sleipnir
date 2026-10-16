package swarm

import (
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// managerSleepWaits is how long a sleep in the manager's shell lasts before it counts as a wait: the guards on the manager's
// wait (stalledWaitReason) refuse it, and the stall sweep counts the manager as waiting while it runs. A manager that slept in the
// shell to watch a long simulation of its own (sleep 300, then sleep 1800) went past every one of them, with a task in review, an
// accepted audit of what was missing and twelve idle workers. A shorter sleep (a server's moment to start) is no wait.
const managerSleepWaits = time.Minute

// shellSleep is how long a shell command sleeps in its own shell, by the sleep commands it runs in line ("sleep 300; date",
// "build && sleep 1m 30"): what it waits for. A sleep whose duration is not a number (a variable) counts for nothing, and so does
// one that may not run in line (in a loop, whose count is not known, in an if, a pipe or the background: shellparse.Simple.Cond)
// unless it is a link of an && chain, which runs in order.
func shellSleep(command string) time.Duration {
	var total time.Duration
	for _, c := range shellparse.Parse(command).Commands {
		if shellparse.ProgramName(c.Program) != "sleep" || c.Nested || c.Child || c.Deferred || c.Cond && !c.ChainLink {
			continue
		}
		for _, a := range c.Args {
			total += sleepArg(a)
		}
	}
	return total
}

// sleepArg reads one argument of sleep as GNU sleep does: a number with an optional unit (s, m, h, d). Anything else is 0.
func sleepArg(a string) time.Duration {
	unit := time.Second
	switch {
	case strings.HasSuffix(a, "s"):
		a = strings.TrimSuffix(a, "s")
	case strings.HasSuffix(a, "m"):
		a, unit = strings.TrimSuffix(a, "m"), time.Minute
	case strings.HasSuffix(a, "h"):
		a, unit = strings.TrimSuffix(a, "h"), time.Hour
	case strings.HasSuffix(a, "d"):
		a, unit = strings.TrimSuffix(a, "d"), 24*time.Hour
	}
	f, err := strconv.ParseFloat(a, 64)
	if err != nil || f < 0 || f > 1e6 {
		return 0
	}
	return time.Duration(f * float64(unit))
}

// managerSleeps reports whether a tool call of the manager is a wait in the shell: a bash command that sleeps managerSleepWaits
// or longer.
func managerSleeps(tool string, input []byte) bool {
	return tool == "bash" && shellSleep(jsonString(input, "command")) >= managerSleepWaits
}
