// Package input is the input side of the terminal interface (docs/UX.md): it turns the bytes a terminal in raw mode sends into
// keys, edits a multi-line prompt with them, and gives that prompt history, reverse search, completion and paste handling.
// The prompt comes back as lines of styled cells (internal/tui/cell) for a renderer to draw, with the cursor's place in them.
//
// There are three parts, and the caller wires them together:
//
//   - Decoder: bytes to Keys. A pure state machine: Feed for what was read, Flush when nothing more is coming.
//   - Editor: Keys to Events (Submit, Interrupt, EOF, ...), and View for what to draw. History, the completers (SlashCommands,
//     Paths) and the paste chips live behind it.
//   - History: the prompts sent so far, in memory and in a file that several sessions may share.
//
// The package starts no goroutines, reads no clock and does no terminal I/O, so there is nothing to shut down and nothing that
// depends on timing; the caller owns raw mode, the read loop and the one timer it needs, the wait for the byte after an ESC:
//
//	var dec input.Decoder
//	ed := input.NewEditor(input.Options{History: hist, Completer: input.SlashCommands(cmds)})
//	ed.SetWidth(width) // and again on every resize: Up and Down then walk wrapped rows
//	for {
//		keys := dec.Feed(readSomeBytes()) // readSomeBytes gives up after dec.Grace() when that is not zero
//		if gotNothingWithinGrace {
//			keys = dec.Flush() // a lone ESC becomes the Esc key
//		}
//		for _, k := range keys {
//			for _, ev := range ed.Handle(k) {
//				switch ev := ev.(type) {
//				case input.Submit: // ev.Text, with pastes expanded
//				case input.Interrupt, input.EOF, input.Unhandled: // the caller's keys
//				}
//			}
//		}
//		render(ed.View(width)) // lines, and where the cursor goes
//	}
//
// # Untrusted text
//
// What the user types, what they paste, what history holds and what a completer offers is all data that may have been written
// by someone else (a paste can carry escape sequences; a file name can hold a newline). One function cleans every text on its
// way into the editor: control characters, escape sequences, invisible and reordering characters and invalid UTF-8 are removed
// before anything reaches the buffer, so neither the buffer nor the view can hold a byte a terminal would act on. History files
// are private to the user (mode 0600 in a directory of 0700), are only ever read as data, and nothing from them is used as a
// command or a format string.
//
// # Terminal setup that this package assumes
//
// Raw mode with signals off (so that Ctrl+C, Ctrl+Z and Ctrl+\ arrive as keys), bracketed paste on (CSI ? 2004 h) so that a
// paste arrives as one key and not as typing, and, where the terminal has it, a modifier-reporting keyboard mode (xterm's
// modifyOtherKeys 2 or the kitty protocol's "disambiguate" flag) so that Shift+Enter can be told from Enter. Without the last
// the terminal sends Shift+Enter as a plain Enter, and Alt+Enter or a backslash before Enter is how to start a new line.
package input
