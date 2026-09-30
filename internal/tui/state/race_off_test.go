//go:build !race

package state

// raceEnabled says the tests run under the race detector; see race_on_test.go.
const raceEnabled = false
