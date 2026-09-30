// Package render turns styled lines (internal/tui/cell) into terminal bytes, in two modes: Inline, for a chat that lives in the
// terminal's scrollback with a small live region redrawn below it, and Screen, for a full-screen view on the alternate screen.
//
// # What the renderers guarantee
//
// Both write only escape sequences they composed themselves. They send colours down-sampled to what the terminal can show, write
// only what changed since the last frame, reset the rendition at the end of every row, hide the cursor while they draw, and wrap
// each frame in synchronized output when the terminal is known to support it (Caps.SyncOutput).
//
// With Caps.Dumb or Caps.Color == ColorNone the inline renderer writes no escape sequence at all: plain lines, as for a pipe or a
// CI log, which is also what a terminal with NO_COLOR gets. The full-screen renderer cannot exist without cursor addressing and
// refuses to start on a dumb terminal; with no colour it keeps bold, reverse and the other attributes.
//
// Each renderer has one mutex and one write path: every method may be called from any goroutine, and a frame is written with a
// single Write call, so frames never interleave. No renderer starts a goroutine, so nothing outlives Close or Leave. A failed
// Write is remembered, reported by the next Flush or Close (or by the full-screen calls themselves), and ends all output: a
// renderer writing to a closed pipe does not retry.
//
// # Text is data
//
// A Span's text may come from a model, a file, a web page or another agent, and a terminal acts on some bytes instead of showing
// them: cursor movement and screen clears overwrite what the reader thinks they saw, OSC 52 writes the clipboard, OSC 8 forges a
// link, a title sequence sets the window title. So text is cleaned when it enters a renderer, before anything else happens to it,
// and nothing that came from outside can reach the terminal as a control. The cleaning removes:
//
//   - every escape sequence, whole, payload included: CSI (colours, cursor, erase), OSC (titles, clipboard, hyperlinks), DCS, APC,
//     PM and SOS strings, character set selections and the two-byte forms. A sequence that never ends (64 bytes for CSI, 4096 for a
//     string) loses only its introducer, so one stray ESC cannot swallow the rest of the text;
//   - every other C0 control (NUL, BEL, backspace, line feed, vertical tab, form feed, carriage return, shift in and out, ...) and
//     DEL. A line break inside a Span is dropped, not honoured: a cell.Line is one row before wrapping;
//   - every C1 control (U+0080 to U+009F, which xterm acts on in UTF-8 mode: single-character CSI, OSC, DCS). Only the control
//     itself goes; text after it stays, as harmless text;
//   - the line and paragraph separators U+2028 and U+2029, and a zero-width rune at the start of a line, which has nothing to
//     attach to.
//
// A tab becomes spaces up to the next multiple of 8 columns, counted from the start of the line. Invalid UTF-8 becomes U+FFFD, one
// cell per bad byte, which is also how cell measures it. Not removed: Unicode format characters (bidirectional overrides,
// zero-width space and joiner). They are text, they cannot move the cursor, and deciding which are acceptable is the job of the
// sanitiser that runs upstream (tools.SanitizeForTerminal). A Cell of the full-screen grid is cleaned the same way and cut to one
// character.
//
// # Widths
//
// Every width is cell.RuneWidth, a table of the ranges that matter in practice. A terminal that draws a rune with another width
// (emoji presentation sequences, grapheme clusters joined by terminals that support them) wraps a row the renderer thinks fits, and
// the renderer's count of rows is then short by one for each such row: the next redraw moves up too few rows and leaves the top
// of the old live region on screen. Nothing in this package can see that; the cure is to measure the terminal once at start-up
// with a cursor position report (see cell.RuneWidth), which is not built yet.
package render
