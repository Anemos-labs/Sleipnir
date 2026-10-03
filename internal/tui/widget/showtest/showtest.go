// Package showtest holds the test helpers of the signature widgets in internal/tui/widget (the prompt stack, the sparkline,
// the horse, the swarm cockpit): turning lines into plain text or into text that shows their styles, building a golden file
// that reads as a sequence of pictures, and the repo's -update convention for rewriting golden files.
//
// It knows only internal/tui/cell, so the widget package's own tests can import it without an import cycle.
package showtest

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// Flatten is the plain text of lines: one row per line, joined with "\n", no trailing newline, styles dropped.
func Flatten(ls []cell.Line) string {
	rows := make([]string, len(ls))
	for i, l := range ls {
		rows[i] = l.Plain()
	}
	return strings.Join(rows, "\n")
}

// MaxWidth is the width in cells of the widest line (0 for no lines).
func MaxWidth(ls []cell.Line) int {
	w := 0
	for _, l := range ls {
		if lw := l.Width(); lw > w {
			w = lw
		}
	}
	return w
}

// FlattenStyled is the text of lines with every styled run wrapped as {style|text}; a run in the default style is written
// as it is. A style reads fg[/bg][+attrs]: the colour by its name in names (a map from colour to a short name such as "G0"
// or "good", so that a golden file reads), or as #rrggbb, aN (16 colours) or xN (256 colours), or - for the terminal's own;
// attrs are b bold, d dim, i italic, u underline, r reverse, s strike. Neighbouring runs of the same style are merged. Spaces
// in the default style at the end of a row are left out (a golden file has no trailing whitespace; they show nothing).
func FlattenStyled(ls []cell.Line, names map[cell.Color]string) string {
	rows := make([]string, len(ls))
	for i, l := range ls {
		var b strings.Builder
		var cur cell.Style
		var text strings.Builder
		flush := func() {
			if text.Len() == 0 {
				return
			}
			if cur == (cell.Style{}) {
				b.WriteString(text.String())
			} else {
				b.WriteString("{" + StyleName(cur, names) + "|" + text.String() + "}")
			}
			text.Reset()
		}
		for _, sp := range l {
			if sp.Text == "" {
				continue
			}
			if sp.Style != cur {
				flush()
				cur = sp.Style
			}
			text.WriteString(sp.Text)
		}
		flush()
		rows[i] = strings.TrimRight(b.String(), " ")
	}
	return strings.Join(rows, "\n")
}

// StyleName writes a style the way FlattenStyled does.
func StyleName(st cell.Style, names map[cell.Color]string) string {
	s := colourName(st.FG, names)
	if st.BG != (cell.Color{}) {
		s += "/" + colourName(st.BG, names)
	}
	if st.Attr != 0 {
		s += "+"
		for _, a := range []struct {
			bit cell.Attr
			ch  string
		}{{cell.Bold, "b"}, {cell.Dim, "d"}, {cell.Italic, "i"}, {cell.Underline, "u"}, {cell.Reverse, "r"}, {cell.Strike, "s"}} {
			if st.Attr&a.bit != 0 {
				s += a.ch
			}
		}
	}
	return s
}

func colourName(c cell.Color, names map[cell.Color]string) string {
	if n, ok := names[c]; ok {
		return n
	}
	switch c.Kind {
	case cell.KindANSI:
		return "a" + strconv.Itoa(int(c.N))
	case cell.KindIndexed:
		return "x" + strconv.Itoa(int(c.N))
	case cell.KindRGB:
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	return "-"
}

// Picture is lines drawn for a golden file: every line padded to width cells and closed with a bar, so that trailing spaces
// are visible and a line that is too wide pushes its bar out of line. Lines are not cut.
func Picture(ls []cell.Line, width int) string {
	rows := make([]string, len(ls))
	for i, l := range ls {
		p := l.Plain()
		if n := width - l.Width(); n > 0 {
			p += strings.Repeat(" ", n)
		}
		rows[i] = p + "|"
	}
	return strings.Join(rows, "\n")
}

// Doc builds a golden file as a sequence of titled pictures, so that a reviewer reads an animation as text.
type Doc struct{ sb strings.Builder }

// Note adds a line of commentary.
func (d *Doc) Note(format string, args ...any) {
	fmt.Fprintf(&d.sb, "# "+format+"\n", args...)
}

// Add adds a picture under a title; width is where the bar on the right of every line is drawn.
func (d *Doc) Add(title string, ls []cell.Line, width int) {
	fmt.Fprintf(&d.sb, "\n== %s (width %d, %d lines)\n", title, width, len(ls))
	if len(ls) > 0 {
		d.sb.WriteString(Picture(ls, width))
		d.sb.WriteByte('\n')
	}
}

// AddText adds a picture given as text (for example a FlattenStyled result). Blank rows at its end are dropped, so that a golden
// file never ends in a blank line.
func (d *Doc) AddText(title, text string) {
	fmt.Fprintf(&d.sb, "\n== %s\n%s\n", title, strings.TrimRight(text, "\n"))
}

// String returns the accumulated visual fixture document.
func (d *Doc) String() string { return d.sb.String() }

// RegisterUpdateFlag defines the -update flag of the repo's golden convention (go test ./pkg -update rewrites the golden
// files) unless some other file of the test binary has already defined it. Call it from an init function of the test
// package, which runs after the package-level variables of that package and of the packages it tests.
func RegisterUpdateFlag() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite golden files under testdata/")
	}
}

// Updating reports whether -update was given (to whoever defined the flag).
func Updating() bool {
	f := flag.Lookup("update")
	if f == nil {
		return false
	}
	if g, ok := f.Value.(flag.Getter); ok {
		b, _ := g.Get().(bool)
		return b
	}
	return f.Value.String() == "true"
}

// Golden compares got with the golden file at path (relative to the test's directory) or, with -update, rewrites it. A
// difference fails the test with the first differing line, as it was and as it is.
func Golden(t testing.TB, path, got string) {
	t.Helper()
	path = filepath.FromSlash(path)
	if !strings.HasSuffix(got, "\n") {
		got += "\n"
	}
	if Updating() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (create it with: go test -run %s -update): %v", path, t.Name(), err)
	}
	if want := string(raw); want != got {
		t.Fatalf("output differs from %s\n%s\nIf the change is intended, read the diff of the golden file and run: go test -run %s -update",
			path, FirstDiff(want, got), t.Name())
	}
}

// FirstDiff names the first line where want and got differ, as it was and as it is, and how many lines differ in all.
func FirstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	first, n := -1, 0
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			if first < 0 {
				first = i
			}
			n++
		}
	}
	if first < 0 {
		return "the lines are the same but the ends differ"
	}
	at := func(ls []string) string {
		if first < len(ls) {
			return ls[first]
		}
		return "(end of file)"
	}
	return fmt.Sprintf("  line %d:\n    want: %s\n    got:  %s\n  (%d line(s) differ in all)", first+1, at(wl), at(gl), n)
}
