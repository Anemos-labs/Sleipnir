package mdfile

import "fmt"

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
		s = fmt.Sprintf("[%s] %s", w.Name, s)
	}
	if w.Path != "" {
		s = w.Path + ": " + s
	}
	if w.Skipped {
		s += " (skipped)"
	}
	return s
}

// Warnf builds an advisory Warning.
func Warnf(path, name, format string, args ...any) Warning {
	return Warning{Path: path, Name: name, Msg: fmt.Sprintf(format, args...)}
}

// Skipf builds a Warning for a definition that was not loaded.
func Skipf(path, name, format string, args ...any) Warning {
	return Warning{Path: path, Name: name, Msg: fmt.Sprintf(format, args...), Skipped: true}
}
