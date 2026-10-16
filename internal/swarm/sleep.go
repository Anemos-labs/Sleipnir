package swarm

import (
	"errors"
	"math"
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
// "build && sleep 1m 30"): what it waits for, at most longestSleep. A sleep with an operand that is not a duration (a variable,
// "nan", none at all) fails at once, as GNU sleep does, and counts for nothing; so does one that may not run in line (in a loop,
// whose count is not known, in an if, a pipe or the background: shellparse.Simple.Cond) unless it is a link of an && chain, which
// runs in order.
func shellSleep(command string) time.Duration {
	var total time.Duration
	for _, c := range shellparse.Parse(command).Commands {
		if shellparse.ProgramName(c.Program) != "sleep" || c.Nested || c.Child || c.Deferred || c.Cond && !c.ChainLink || len(c.Args) == 0 {
			continue
		}
		var this time.Duration
		valid := true
		for _, a := range c.Args {
			d, ok := sleepArg(a)
			if !ok {
				valid = false
				break
			}
			this = min(this+d, longestSleep) // saturated: a sum of long operands must not overflow
		}
		if valid {
			total = min(total+this, longestSleep)
		}
	}
	return total
}

// longestSleep is what a sleep counts for at most: a longer one, or "inf", which GNU sleep takes as forever, is saturated rather
// than discarded (it is a wait all the same) and never overflows a time.Duration.
const longestSleep = 365 * 24 * time.Hour

// sleepArg reads one argument of sleep as GNU sleep does: a number with an optional unit (s, m, h, d), at most longestSleep. ok is
// false for anything else (a variable, "nan", a negative number), which makes GNU sleep fail.
func sleepArg(a string) (d time.Duration, ok bool) {
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
	if err != nil && !errors.Is(err, strconv.ErrRange) || math.IsNaN(f) || f < 0 {
		return 0, false
	}
	if f*unit.Seconds() >= longestSleep.Seconds() { // also +Inf, and a number too big for a float (ErrRange)
		return longestSleep, true
	}
	return time.Duration(f * float64(unit)), true
}

// managerSleeps reports whether a tool call of the manager is a wait in the shell: a bash command that sleeps managerSleepWaits
// or longer.
func managerSleeps(tool string, input []byte) bool {
	return tool == "bash" && shellSleep(jsonString(input, "command")) >= managerSleepWaits
}
