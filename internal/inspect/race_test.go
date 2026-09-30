//go:build race

package inspect

// raceEnabled scales down the tests that stream tens of megabytes: the race
// detector makes them an order of magnitude slower without checking more.
const raceEnabled = true
