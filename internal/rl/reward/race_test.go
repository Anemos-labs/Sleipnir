//go:build race

package reward

// underRace is true when the race detector is on. It slows everything down five
// to ten times, so tests that measure growth use smaller inputs then.
const underRace = true
