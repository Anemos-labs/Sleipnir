package session

import (
	"testing"
	"time"
)

// Nothing set is the default, a negative number is none (a rollout, whose runner repeats the whole rollout and whose clock a wait
// would spend), and a number is itself.
func TestOutagePatienceDefaultsAndCanBeSwitchedOff(t *testing.T) {
	for _, c := range []struct{ in, want time.Duration }{
		{0, DefaultOutagePatience},
		{-1, 0},
		{-time.Hour, 0},
		{30 * time.Second, 30 * time.Second},
	} {
		if got := outagePatience(c.in); got != c.want {
			t.Errorf("outagePatience(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	if DefaultOutagePatience < time.Minute || DefaultOutagePatience > 30*time.Minute {
		t.Errorf("DefaultOutagePatience = %v: long enough for an outage of minutes, short enough for a person to notice a dead endpoint", DefaultOutagePatience)
	}
}
