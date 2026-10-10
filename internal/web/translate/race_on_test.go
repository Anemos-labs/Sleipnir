//go:build race

package translate

// raceEnabled says the tests run under the race detector, which makes long runs several times slower: the flood tests use a
// tenth of their events there.
const raceEnabled = true
