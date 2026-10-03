package widget

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The inline parser: the text of one paragraph, heading or table cell in, a flat list of styled runs out. It follows
// CommonMark's algorithm (flanking rules, a delimiter stack with the "rule of three", brackets that become links only when a
// destination follows) for emphasis, strong, strikethrough, code spans, links, images, autolinks, escapes and entities, and
// adds GFM's bare URLs. What it cannot make sense of stays literal: an unterminated `*`, a `[` without a destination, a tag.
// Everything is linear in the text (code-span closers are looked up in a table, emphasis uses openers-bottom, link
// destinations are bounded), so a hostile paragraph cannot take long.

// Flags of a run.
const (
	mdBold uint8 = 1 << iota
	mdItalic
	mdStrike
	mdCodeSpan
	mdLinkText
	mdURLNote // the " (url)" shown after a link whose text is not its address
	mdImage
)

type mdRun struct {
	text  string
	flags uint8
	br    bool // a hard line break (text is empty)
}

const (
	mdMaxDest    = 4096 // longest link destination and title scanned
	mdMaxAutolnk = 2048
	mdMaxURLShow = 160 // cells of a url shown after its link text
)

type inlineKind uint8

const (
	tkText inlineKind = iota
	tkDelim
	tkCode
	tkSoft
	tkHard
	tkOpen // "[" or "![" waiting for a "]"; literal text unless a link closes it
	tkLinkStart
	tkLinkEnd
	tkAuto // a bare or <bracketed> URL, shown as a link
)

type inlineTok struct {
	kind      inlineKind
	text      string
	url       string
	ch        byte
	n, orig   int // delimiter characters left, and in the run
	canOpen   bool
	canClose  bool
	openUses  []uint8
	closeUses []uint8
	img       bool
	active    bool
	delimMark int
}

type inlineParser struct {
	s        string
	ellipsis string
	toks     []inlineTok
	txt      strings.Builder
	delims   []int
	brackets []int
	ticks    map[int][]int // length of a backtick run -> starts of the runs, built on first use
}

// mdInline parses s (no control characters; "\n" separates the lines of a paragraph) into runs. ellipsis is what a cut-off URL
// ends with.
func mdInline(s, ellipsis string) []mdRun {
	p := &inlineParser{s: s, ellipsis: ellipsis}
	p.scan()
	p.flush()
	p.processEmphasis(0)
	return p.runs()
}

var mdSpecial = func() (t [256]bool) {
	for _, c := range []byte("\\`*_~[]!<&\nh") {
		t[c] = true
	}
	return
}()

// flush emits buffered inline text as one token and resets the text builder.
func (p *inlineParser) flush() {
	if p.txt.Len() > 0 {
		p.toks = append(p.toks, inlineTok{kind: tkText, text: p.txt.String()})
		p.txt.Reset()
	}
}

// add flushes pending inline text, appends a token, and returns its index.
func (p *inlineParser) add(t inlineTok) int {
	p.flush()
	p.toks = append(p.toks, t)
	return len(p.toks) - 1
}

// mdASCIIPunct recognizes ASCII punctuation ranges used in Markdown escape handling.
func mdASCIIPunct(c byte) bool {
	return (c >= '!' && c <= '/') || (c >= ':' && c <= '@') || (c >= '[' && c <= '`') || (c >= '{' && c <= '~')
}

func (p *inlineParser) scan() {
	s := p.s
	for i := 0; i < len(s); {
		start := i
		for i < len(s) && !mdSpecial[s[i]] {
			i++
		}
		if i > start {
			p.txt.WriteString(s[start:i])
			if i >= len(s) {
				return
			}
		}
		switch c := s[i]; c {
		case '\\':
			switch {
			case i+1 < len(s) && s[i+1] == '\n':
				p.add(inlineTok{kind: tkHard})
				i += 2
				for i < len(s) && s[i] == ' ' {
					i++
				}
			case i+1 < len(s) && mdASCIIPunct(s[i+1]):
				p.txt.WriteByte(s[i+1])
				i += 2
			default:
				p.txt.WriteByte('\\')
				i++
			}
		case '`':
			i = p.codeSpan(i)
		case '*', '_', '~':
			i = p.delimRun(i)
		case '[':
			p.add(inlineTok{kind: tkOpen, text: "[", active: true, delimMark: len(p.delims)})
			p.brackets = append(p.brackets, len(p.toks)-1)
			i++
		case '!':
			if i+1 < len(s) && s[i+1] == '[' {
				p.add(inlineTok{kind: tkOpen, text: "![", img: true, active: true, delimMark: len(p.delims)})
				p.brackets = append(p.brackets, len(p.toks)-1)
				i += 2
			} else {
				p.txt.WriteByte('!')
				i++
			}
		case ']':
			i = p.closeBracket(i)
		case '<':
			i = p.autolink(i)
		case '&':
			i = p.entity(i)
		case 'h':
			i = p.bareURL(i)
		case '\n':
			t := p.txt.String()
			trimmed := strings.TrimRight(t, " ")
			p.txt.Reset()
			p.txt.WriteString(trimmed)
			if len(t)-len(trimmed) >= 2 {
				p.add(inlineTok{kind: tkHard})
			} else {
				p.add(inlineTok{kind: tkSoft})
			}
			i++
			for i < len(s) && s[i] == ' ' {
				i++
			}
		}
	}
}

// ---- code spans ----

func (p *inlineParser) tickRuns() map[int][]int {
	if p.ticks != nil {
		return p.ticks
	}
	p.ticks = map[int][]int{}
	s := p.s
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		p.ticks[j-i] = append(p.ticks[j-i], i)
		i = j
	}
	return p.ticks
}

func (p *inlineParser) codeSpan(i int) int {
	s := p.s
	j := i
	for j < len(s) && s[j] == '`' {
		j++
	}
	n := j - i
	runs := p.tickRuns()[n]
	k := sort.SearchInts(runs, j)
	if k >= len(runs) {
		p.txt.WriteString(s[i:j])
		return j
	}
	end := runs[k]
	content := strings.ReplaceAll(s[j:end], "\n", " ")
	if len(content) >= 2 && content[0] == ' ' && content[len(content)-1] == ' ' && strings.Trim(content, " ") != "" {
		content = content[1 : len(content)-1]
	}
	p.add(inlineTok{kind: tkCode, text: content})
	return end + n
}

// ---- emphasis ----

// mdLastRune returns the final rune or a newline sentinel for empty text.
func mdLastRune(s string) rune {
	if s == "" {
		return '\n'
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

// mdFirstRune returns the first rune or a newline sentinel for empty text.
func mdFirstRune(s string) rune {
	if s == "" {
		return '\n'
	}
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

// mdPunct includes Unicode punctuation and symbols when classifying Markdown delimiters.
func mdPunct(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }

func (p *inlineParser) delimRun(i int) int {
	s := p.s
	c := s[i]
	j := i
	for j < len(s) && s[j] == c {
		j++
	}
	n := j - i
	if c == '~' && n != 2 {
		p.txt.WriteString(s[i:j])
		return j
	}
	before, after := mdLastRune(s[:i]), mdFirstRune(s[j:])
	spaceBefore, spaceAfter := unicode.IsSpace(before), unicode.IsSpace(after)
	punctBefore, punctAfter := mdPunct(before), mdPunct(after)
	left := !spaceAfter && (!punctAfter || spaceBefore || punctBefore)
	right := !spaceBefore && (!punctBefore || spaceAfter || punctAfter)
	canOpen, canClose := left, right
	if c == '_' {
		canOpen = left && (!right || punctBefore)
		canClose = right && (!left || punctAfter)
	}
	if !canOpen && !canClose {
		p.txt.WriteString(s[i:j])
		return j
	}
	idx := p.add(inlineTok{kind: tkDelim, ch: c, n: n, orig: n, canOpen: canOpen, canClose: canClose})
	p.delims = append(p.delims, idx)
	return j
}

// processEmphasis matches the delimiters above bottom (an index into p.delims) into emphasis, strong and strikethrough, and
// drops them all from the stack: what is left unmatched is literal text.
func (p *inlineParser) processEmphasis(bottom int) {
	ds := p.delims[bottom:]
	m := len(ds)
	if m == 0 {
		return
	}
	prev, next := make([]int, m), make([]int, m)
	for k := range ds {
		prev[k], next[k] = k-1, k+1
	}
	remove := func(k int) {
		a, b := prev[k], next[k]
		if a >= 0 {
			next[a] = b
		}
		if b < m {
			prev[b] = a
		}
	}
	bottoms := map[int]int{}
	key := func(t *inlineTok) int {
		k := int(t.ch)*6 + t.orig%3
		if t.canOpen {
			k += 3
		}
		return k
	}
	for cl := 0; cl < m; {
		c := &p.toks[ds[cl]]
		if !c.canClose {
			cl = next[cl]
			continue
		}
		low, ok := bottoms[key(c)]
		if !ok {
			low = -1
		}
		op, found := prev[cl], false
		for op > low {
			o := &p.toks[ds[op]]
			odd := (c.canOpen || o.canClose) && c.orig%3 != 0 && (o.orig+c.orig)%3 == 0
			if o.ch == c.ch && o.canOpen && !odd {
				found = true
				break
			}
			op = prev[op]
		}
		if found {
			o := &p.toks[ds[op]]
			use := 1
			if c.n >= 2 && o.n >= 2 {
				use = 2
			}
			o.n -= use
			c.n -= use
			o.openUses = append(o.openUses, uint8(use))
			c.closeUses = append(c.closeUses, uint8(use))
			next[op], prev[cl] = cl, op // everything between them is now literal
			if o.n == 0 {
				remove(op)
			}
			if c.n == 0 {
				old := cl
				cl = next[cl]
				remove(old)
			}
			continue
		}
		bottoms[key(c)] = prev[cl]
		old := cl
		cl = next[cl]
		if !c.canOpen {
			remove(old)
		}
	}
	p.delims = p.delims[:bottom]
}
