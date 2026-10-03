// Package term is the terminal boundary of the UI: what the terminal in front of us can do (Detect), how big it is and when
// that changes (GetSize, WatchResize), and raw mode (MakeRaw). It never writes to the terminal; the escape sequences that
// make use of these capabilities belong to internal/tui/render.
//
// Detection is conservative. A capability is on only when the environment says so or the terminal is known to have it, and a
// stream that is not a terminal is always plain: no colour, no animation, no cursor control.
package term

import (
	"os"
	"strconv"
	"strings"

	xterm "golang.org/x/term"
)

// ColorDepth is how many colours the terminal can show.
type ColorDepth uint8

const (
	ColorNone      ColorDepth = iota // no colour at all (NO_COLOR, a pipe, a dumb terminal)
	ColorANSI16                      // the 16 standard colours
	ColorANSI256                     // the 256-colour palette
	ColorTrueColor                   // 24-bit RGB
)

// String names the depth: "none", "ansi16", "ansi256" or "truecolor".
func (d ColorDepth) String() string {
	switch d {
	case ColorANSI16:
		return "ansi16"
	case ColorANSI256:
		return "ansi256"
	case ColorTrueColor:
		return "truecolor"
	}
	return "none"
}

// DefaultWidth and DefaultHeight are the size assumed when nothing can say how big the terminal is (a pipe with no COLUMNS
// and LINES in the environment).
const (
	DefaultWidth  = 80
	DefaultHeight = 24
)

// Caps is what the terminal can do. The zero value is not useful (no size); get one from Detect or FromEnv.
type Caps struct {
	Color          ColorDepth // colours the terminal shows; ColorNone when NO_COLOR is set or the stream is not a terminal
	Unicode        bool       // the locale is UTF-8 and the terminal's font is expected to have the glyphs the UI draws
	Width, Height  int        // size in cells; always positive (DefaultWidth x DefaultHeight when unknown)
	SyncOutput     bool       // DEC private mode 2026 (synchronized output) is known to be supported
	BracketedPaste bool       // DEC private mode 2004 is expected to work
	Anim           bool       // animation is allowed: a real terminal and no opt-out (see FromEnv)
	Dumb           bool       // no cursor control at all: not a terminal, or TERM=dumb. Output must be plain lines
}

// Plain returns the caps of a plain stream: what a pipe gets, and what --plain asks for. The size and Unicode are kept (a
// plain transcript may still hold UTF-8); colour, animation, synchronized output and bracketed paste are off and Dumb is set.
func (c Caps) Plain() Caps {
	c.Color = ColorNone
	c.SyncOutput, c.BracketedPaste, c.Anim = false, false, false
	c.Dumb = true
	return c
}

// Detect reads the capabilities of the terminal behind f (normally os.Stdout) from the environment, which it gets through env
// (os.Getenv in production, a map in tests; nil means an empty environment). It asks the operating system two things about f:
// is it a terminal, and how big. A nil file, a pipe, a regular file or a terminal whose size cannot be read are not errors:
// the first three are plain (Dumb, no colour, no animation) and the last falls back to COLUMNS and LINES or
// DefaultWidth x DefaultHeight. Detect does not change the terminal and does not write to it.
func Detect(env func(string) string, f *os.File) Caps {
	tty, w, h := false, 0, 0
	if f != nil {
		fd := int(f.Fd())
		if xterm.IsTerminal(fd) {
			tty = true
			if cw, ch, err := xterm.GetSize(fd); err == nil {
				w, h = cw, ch
			}
		}
	}
	if tty && f != nil && env != nil && env("TERM") == "" && enableVT(f) {
		// a Windows console that says nothing of itself but understands sequences (it has just been asked to): a terminal, not a dumb one
		inner := env
		env = func(k string) string {
			if k == "TERM" {
				return "xterm-256color"
			}
			return inner(k)
		}
	}
	return FromEnv(env, tty, w, h)
}

// FromEnv is Detect with the operating system's answers given instead of asked for, so that it is a pure function of its
// inputs. tty says whether the output is a terminal; width and height are its size (zero or negative when unknown).
//
// The rules, in the order they apply:
//
//   - not a tty, TERM=dumb, or no TERM and no sign of a modern terminal (WT_SESSION, ConEmuANSI, ANSICON, TERM_PROGRAM):
//     Dumb. A dumb stream has no colour, no animation, no synchronized output and no bracketed paste.
//   - colour: NO_COLOR (any non-empty value) means none. Otherwise COLORTERM=truecolor or 24bit, a TERM that names truecolor
//     (xterm-direct, xterm-kitty, xterm-ghostty, alacritty, wezterm, foot) or a TERM_PROGRAM that is known to have it is
//     truecolor; a TERM with "256color" is 256 colours; xterm, screen, tmux, vt100, rxvt, linux, ansi and cygwin are 16;
//     anything else is none unless COLORTERM is set at all.
//   - Unicode: the first of LC_ALL, LC_CTYPE, LANG that is set decides (it must name UTF-8). With none of them set (Windows)
//     the answer is yes in Windows Terminal. The Linux console (TERM=linux) is never Unicode: its fonts lack most of the
//     glyphs the UI draws.
//   - Anim: not Dumb, no NO_COLOR, SLEIPNIR_ANIM not 0/false/off/no, and REDUCE_MOTION unset or 0/false/off/no (any other
//     value asks for less motion).
//   - SyncOutput: only for terminals known to implement mode 2026 (kitty, WezTerm, iTerm2, ghostty, foot, alacritty) and
//     never inside tmux or screen, which would have to forward it. The environment cannot tell the version of alacritty; the
//     heuristic assumes 0.13 or later (2023), and an older one ignores the unknown mode.
//   - BracketedPaste: every terminal except the Linux console and the VT100/VT220/ansi family.
func FromEnv(env func(string) string, tty bool, width, height int) Caps {
	if env == nil {
		env = func(string) string { return "" }
	}
	c := Caps{Width: width, Height: height}
	if c.Width <= 0 {
		c.Width = envInt(env, "COLUMNS", DefaultWidth)
	}
	if c.Height <= 0 {
		c.Height = envInt(env, "LINES", DefaultHeight)
	}

	name := strings.ToLower(env("TERM"))
	c.Unicode = unicodeLocale(env, name)
	c.Dumb = !tty || name == "dumb" || (name == "" && !modernHint(env))
	if c.Dumb {
		return c
	}

	c.Color = colorDepth(env, name)
	c.SyncOutput = syncOutput(env, name)
	c.BracketedPaste = name != "linux" && !basicVT(name)
	c.Anim = env("NO_COLOR") == "" && animAllowed(env) // NO_COLOR turns the motion off as well (docs/UX.md, principle 4)
	return c
}

func envInt(env func(string) string, key string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(env(key))); err == nil && n > 0 {
		return n
	}
	return def
}

// modernHint reports environment variables that only terminals with escape-sequence support set, for the case that TERM is
// missing (which is normal on Windows).
func modernHint(env func(string) string) bool {
	return env("WT_SESSION") != "" || env("ConEmuANSI") == "ON" || env("ANSICON") != "" || env("TERM_PROGRAM") != ""
}

func unicodeLocale(env func(string) string, term string) bool {
	if term == "linux" {
		return false
	}
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := strings.ToLower(env(k)); v != "" {
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return env("WT_SESSION") != ""
}

func colorDepth(env func(string) string, term string) ColorDepth {
	if env("NO_COLOR") != "" {
		return ColorNone
	}
	switch strings.ToLower(env("COLORTERM")) {
	case "truecolor", "24bit":
		return ColorTrueColor
	}
	switch {
	case strings.Contains(term, "truecolor"), strings.Contains(term, "24bit"), strings.Contains(term, "direct"):
		return ColorTrueColor
	}
	switch term {
	case "xterm-kitty", "xterm-ghostty", "ghostty", "alacritty", "wezterm", "foot", "foot-extra", "contour":
		return ColorTrueColor
	}
	switch env("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm", "ghostty", "vscode", "Hyper", "rio", "contour":
		return ColorTrueColor
	}
	if strings.Contains(term, "256color") {
		return ColorANSI256
	}
	if env("TERM_PROGRAM") == "Apple_Terminal" {
		return ColorANSI256
	}
	for _, p := range []string{"xterm", "screen", "tmux", "vt100", "vt220", "rxvt", "linux", "ansi", "cygwin"} {
		if strings.HasPrefix(term, p) {
			return ColorANSI16
		}
	}
	if strings.Contains(term, "color") || strings.Contains(term, "ansi") {
		return ColorANSI16
	}
	if env("COLORTERM") != "" || env("WT_SESSION") != "" || env("ConEmuANSI") == "ON" || env("ANSICON") != "" {
		return ColorANSI16
	}
	return ColorNone
}

// basicVT reports the terminal families that predate bracketed paste.
func basicVT(term string) bool {
	return strings.HasPrefix(term, "vt") || term == "ansi" || term == "cygwin"
}

func animAllowed(env func(string) string) bool {
	if isOff(env("SLEIPNIR_ANIM")) {
		return false
	}
	if rm := env("REDUCE_MOTION"); rm != "" && !isOff(rm) {
		return false
	}
	return true
}

// isOff reports whether a switch's value says "off": 0, false, off or no, in any case. An empty value is not a choice.
func isOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

// syncOutput reports whether the terminal is known to implement synchronized output. Unknown terminals are off: a terminal
// that does not know mode 2026 ignores it, but the list is the contract that nothing is sent to a terminal that might not.
func syncOutput(env func(string) string, term string) bool {
	if env("TMUX") != "" || env("STY") != "" {
		return false // a multiplexer sits between us and the terminal that might support it
	}
	switch term {
	case "xterm-kitty", "xterm-ghostty", "ghostty", "wezterm", "foot", "foot-extra", "foot-direct", "alacritty", "alacritty-direct":
		return true
	}
	switch env("TERM_PROGRAM") {
	case "WezTerm", "iTerm.app", "ghostty", "kitty":
		return true
	}
	return env("KITTY_WINDOW_ID") != "" || env("LC_TERMINAL") == "iTerm2" || env("ALACRITTY_WINDOW_ID") != "" ||
		env("ALACRITTY_LOG") != "" || env("ALACRITTY_SOCKET") != ""
}
