package input

import (
	"unicode"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// The buffer is a []rune. Three kinds of rune are not ordinary text:
//
//   - '\n' separates lines;
//   - '\t' (only ever from pasted or recalled text: the Tab key never inserts one) is drawn as spaces up to the next multiple
//     of tabWidth, so its width depends on where it sits;
//   - a paste chip is one private-use rune from a range that is stripped from every incoming text, so text cannot forge one.
//     The view draws its label in its place; to every editing command it is one atomic rune.
//
// A cluster is what a cursor may sit between: a base rune and the zero-width runes that follow it (combining marks, variation
// selectors, skin-tone modifiers), and across a zero-width joiner the rune it joins to, so an emoji sequence is one cluster.
// The newline, the tab and a chip are clusters of their own.

const (
	chipBase = 0x10f000 // first chip rune; the range up to chipBase+0xfff is reserved
	maxChips = 1000     // chips one prompt can hold
	// maxChipBytes is the most text all the chips of one prompt hold between them (16 MiB); a paste that would go past it is put
	// in the buffer as text, cut short, like a paste that finds no chip free.
	maxChipBytes = 16 << 20
	tabWidth     = 4
	zwj          = 0x200d
)

func isChip(r rune) bool    { return r >= chipBase && r < chipBase+0x1000 }
func chipRune(id int) rune  { return chipBase + rune(id) }
func chipID(r rune) int     { return int(r - chipBase) }
func isSpecial(r rune) bool { return r == '\n' || r == '\t' || isChip(r) }

// isMark reports a rune that attaches to the one before it: zero width and not a control of ours.
func isMark(r rune) bool { return !isSpecial(r) && cell.RuneWidth(r) == 0 }

// nextBoundary is the end of the cluster that starts at i (i itself is returned at the end of the buffer).
func nextBoundary(buf []rune, i int) int {
	n := len(buf)
	if i >= n {
		return n
	}
	j := i + 1
	if isSpecial(buf[i]) {
		return j
	}
	for j < n && isMark(buf[j]) {
		joiner := buf[j] == zwj
		j++
		if joiner && j < n && !isSpecial(buf[j]) && !isMark(buf[j]) {
			j++ // a joined rune belongs to the same cluster
		}
	}
	return j
}

// maxCluster bounds how far back prevBoundary looks for the start of a cluster, so that finding it costs the same in a line of
// a million runes as in a short one (every key press finds it). A cluster longer than this, a base followed by hundreds of
// combining marks, can have its cursor placed inside it: nothing real has such a cluster.
const maxCluster = 256

// prevBoundary is the start of the cluster that contains the rune before i.
func prevBoundary(buf []rune, i int) int {
	if i <= 0 {
		return 0
	}
	if i > len(buf) {
		i = len(buf)
	}
	s := i - 1
	for s > 0 && i-s < maxCluster && !isSpecial(buf[s-1]) { // clusters never contain a special rune: start just after the last one
		s--
	}
	last := s
	for p := s; p < i; p = nextBoundary(buf, p) {
		last = p
	}
	return last
}

// isBoundary reports whether a cursor may sit at i.
func isBoundary(buf []rune, i int) bool {
	if i <= 0 || i >= len(buf) {
		return i == 0 || i == len(buf)
	}
	return nextBoundary(buf, prevBoundary(buf, i)) == i
}

// snap moves i to the nearest boundary at or before it, and into range.
func snap(buf []rune, i int) int {
	switch {
	case i <= 0:
		return 0
	case i >= len(buf):
		return len(buf)
	case isBoundary(buf, i):
		return i
	}
	return prevBoundary(buf, i)
}

// Word motion works on classes of rune.
type class uint8

const (
	clsSpace class = iota // whitespace, newline included
	clsPunct              // everything that is not a letter, digit, mark or '_'
	clsWord               // letters, digits, combining marks and '_'
	clsChip               // a paste chip: a word of its own
)

func classOf(r rune) class {
	switch {
	case isChip(r):
		return clsChip
	case unicode.IsSpace(r):
		return clsSpace
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
		return clsWord
	}
	return clsPunct
}

// wordEnd is where Alt+F from i lands: after the next word. Whitespace and punctuation are skipped, as readline does.
func wordEnd(buf []rune, i int) int {
	n := len(buf)
	for i < n && (classOf(buf[i]) == clsSpace || classOf(buf[i]) == clsPunct) {
		i++
	}
	if i < n && classOf(buf[i]) == clsChip {
		return snap(buf, i+1)
	}
	for i < n && classOf(buf[i]) == clsWord {
		i++
	}
	return snap(buf, i)
}

// wordStart is where Alt+B from i lands: the start of the previous word.
func wordStart(buf []rune, i int) int {
	for i > 0 && (classOf(buf[i-1]) == clsSpace || classOf(buf[i-1]) == clsPunct) {
		i--
	}
	if i > 0 && classOf(buf[i-1]) == clsChip {
		return snap(buf, i-1)
	}
	for i > 0 && classOf(buf[i-1]) == clsWord {
		i--
	}
	return snap(buf, i)
}

// lineStart is the index of the first rune of the line that contains i.
func lineStart(buf []rune, i int) int {
	for i > 0 && buf[i-1] != '\n' {
		i--
	}
	return i
}

// lineEnd is the index of the newline that ends the line containing i, or len(buf).
func lineEnd(buf []rune, i int) int {
	for i < len(buf) && buf[i] != '\n' {
		i++
	}
	return i
}

// advance is the column after drawing r at col. chipW is the label width when r is a chip.
func advance(col int, r rune, chipW int) int {
	switch {
	case r == '\t':
		return col + tabWidth - col%tabWidth
	case r == '\n':
		return col
	case isChip(r):
		return col + chipW
	}
	return col + cell.RuneWidth(r)
}
