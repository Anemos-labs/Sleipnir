package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// errSelectCancelled is what a menu returns when the person backs out (Esc, Ctrl-C, Ctrl-D).
var errSelectCancelled = errors.New("cancelled")

// arrowOK says whether menus can be driven with the arrow keys: the process has a terminal on both ends. Elsewhere (a pipe, a test) the
// menus keep their typed form, numbers and words.
var arrowOK = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

// rawMode puts the terminal in raw mode and returns what puts it back; a test replaces it.
var rawMode = func() (func(), error) {
	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return nil, err
	}
	return func() { term.Restore(int(os.Stdin.Fd()), old) }, nil
}

// selectRows shows labels as a menu with one row highlighted: up and down (or ctrl-p, ctrl-n) move, Enter chooses, Esc quits. With
// searchable, typing narrows the rows to those whose key holds every word typed; without it, j and k also move and a digit jumps to that
// row. It returns the index into labels. Raw mode lasts only while the menu is up.
func selectRows(in *bufio.Reader, out io.Writer, title string, labels, keys []string, searchable bool, rows int) (int, error) {
	restore, err := rawMode()
	if err != nil {
		return 0, err
	}
	defer restore()
	var (
		query   []rune
		cur     int
		drawn   int
		visible []int
	)
	filter := func() {
		words := strings.Fields(strings.ToLower(string(query)))
		visible = visible[:0]
		for i := range labels {
			ok := true
			for _, w := range words {
				if !strings.Contains(strings.ToLower(keys[i]), w) {
					ok = false
				}
			}
			if ok {
				visible = append(visible, i)
			}
		}
		if cur >= len(visible) {
			cur = max(len(visible)-1, 0)
		}
	}
	draw := func() {
		if drawn > 0 {
			fmt.Fprintf(out, "\x1b[%dA", drawn)
		}
		fmt.Fprint(out, "\r\x1b[J")
		lines := 0
		line := func(s string) { fmt.Fprint(out, s, "\r\n"); lines++ }
		head := title
		if searchable {
			head += "  search: " + string(query) + "_"
		}
		line(head)
		start := 0
		if cur >= rows {
			start = cur - rows + 1
		}
		for r := start; r < len(visible) && r < start+rows; r++ {
			if r == cur {
				line("\x1b[7m > " + labels[visible[r]] + " \x1b[0m")
			} else {
				line("   " + labels[visible[r]])
			}
		}
		switch {
		case len(visible) == 0:
			line("   nothing matches")
		case len(visible) > rows:
			line(fmt.Sprintf("   %d of %d", cur+1, len(visible)))
		}
		hint := "up/down move, enter chooses, esc quits"
		if searchable {
			hint = "type to search, " + hint
		} else {
			hint += ", a number jumps"
		}
		line("\x1b[2m" + hint + "\x1b[0m")
		drawn = lines
	}
	filter()
	for {
		draw()
		b, err := in.ReadByte()
		if err != nil {
			return 0, errSelectCancelled
		}
		switch {
		case b == 3 || b == 4: // ctrl-c, ctrl-d
			fmt.Fprintf(out, "\x1b[%dA\r\x1b[J", drawn)
			return 0, errSelectCancelled
		case b == '\r' || b == '\n':
			if len(visible) == 0 {
				continue
			}
			fmt.Fprintf(out, "\x1b[%dA\r\x1b[J%s %s\r\n", drawn, title, strings.TrimSpace(labels[visible[cur]]))
			return visible[cur], nil
		case b == 0x1b:
			if in.Buffered() == 0 {
				fmt.Fprintf(out, "\x1b[%dA\r\x1b[J", drawn)
				return 0, errSelectCancelled
			}
			next, _ := in.ReadByte()
			if next != '[' && next != 'O' {
				continue
			}
			c, _ := in.ReadByte()
			switch c {
			case 'A':
				cur = max(cur-1, 0)
			case 'B':
				cur = min(cur+1, max(len(visible)-1, 0))
			case '5', '6': // page up, page down: "5~" and "6~"
				in.ReadByte()
				if c == '5' {
					cur = max(cur-rows, 0)
				} else {
					cur = min(cur+rows, max(len(visible)-1, 0))
				}
			}
		case b == 16 || (!searchable && b == 'k'):
			cur = max(cur-1, 0)
		case b == 14 || (!searchable && b == 'j'):
			cur = min(cur+1, max(len(visible)-1, 0))
		case b == 0x7f || b == 0x08:
			if searchable && len(query) > 0 {
				query = query[:len(query)-1]
				cur = 0
				filter()
			}
		case !searchable && b >= '1' && b <= '9':
			if n := int(b - '0'); n <= len(visible) {
				cur = n - 1
			}
		case searchable && b >= ' ' && b != 0x7f:
			in.UnreadByte()
			r, _, _ := in.ReadRune()
			query = append(query, r)
			cur = 0
			filter()
		}
	}
}
