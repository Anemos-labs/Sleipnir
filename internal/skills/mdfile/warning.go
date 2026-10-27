package mdfile

import (
	"fmt"
	"strconv"
	"strings"
)

// Warning reports something the loaders noticed while discovering definitions:
// a file that could not be loaded, a name collision, a value that was
// truncated. Loaders never fail as a whole because one file is bad; they
// return what loaded and a list of Warnings for the user to read.
type Warning struct {
	// Path locates the problem the way the user knows it: relative to the
	// project root or "~/..." under the home directory, never an absolute
	// machine path.
	Path string
	// Name is the definition the warning is about, when it has one.
	Name string
	Msg  string
	// Skipped says the definition was not loaded. A warning without it is
	// advisory: the definition is in use, possibly with a value adjusted.
	Skipped bool
}

// String renders "path: message" (with the name in brackets when it adds
// something), without a trailing newline.
func (w Warning) String() string {
	s := w.Msg
	if w.Name != "" {
		s = fmt.Sprintf("[%s] %s", oneLine(w.Name), s)
	}
	if w.Path != "" {
		s = oneLine(w.Path) + ": " + s
	}
	if w.Skipped {
		s += " (skipped)"
	}
	return s
}

// Warnf builds an advisory Warning.
func Warnf(path, name, format string, args ...any) Warning {
	return Warning{Path: oneLine(path), Name: oneLine(name), Msg: fmt.Sprintf(format, args...)}
}

// Skipf builds a Warning for a definition that was not loaded.
func Skipf(path, name, format string, args ...any) Warning {
	return Warning{Path: oneLine(path), Name: oneLine(name), Msg: fmt.Sprintf(format, args...), Skipped: true}
}

// oneLine quotes a path or name that holds a line break, so a file name cannot start a line of its own in a warning: a directory
// named "x\nwarning: ..." shows as the quoted string it is.
func oneLine(s string) string {
	if strings.ContainsAny(s, "\n\r\v\f\u0085\u2028\u2029") {
		return strconv.Quote(s)
	}
	return s
}
