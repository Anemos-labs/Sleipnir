//go:build race

package state

// raceEnabled says the tests run under the race detector, which makes the long folds several times slower: the tests that need a
// very long log use a shorter one there (their doc comments say by how much).
const raceEnabled = true
