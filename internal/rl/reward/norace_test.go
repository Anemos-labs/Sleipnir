//go:build !race

package reward

// underRace is true when the race detector is on (see race_test.go).
const underRace = false
