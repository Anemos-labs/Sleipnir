// Package app is where the terminal interface's parts meet: the state folded from a session's event log (internal/tui/state), the
// widgets that draw it (internal/tui/widget), the renderers that put it on a terminal (internal/tui/render) and the input that
// drives it (internal/tui/input). It holds the programs a person runs (the cockpit of `sleipnir watch`, `sleipnir replay`, the
// inline chat) and the headless recorder that turns a recorded session into the pictures of the README.
//
// A program is a pure function of a snapshot of the state, the animation frame and the size of the screen: View draws, Update
// applies a key, and nothing in either reads a clock, a terminal or the file system. That is what makes a recording a function of
// a log: the same log and the same options give the same bytes, on any machine, so the pictures can be checked in CI and cannot
// drift from the code that draws them.
package app
