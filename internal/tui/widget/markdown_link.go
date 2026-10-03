package widget

import (
	"html"
	"strings"
	"unicode"
)

// Links, images, autolinks, bare URLs and character references: the parts of inline parsing that look past the next delimiter.
// (The parser's overview is in markdown_inline.go.)

func (p *inlineParser) closeBracket(i int) int {
	s := p.s
	if len(p.brackets) == 0 {
		p.txt.WriteByte(']')
		return i + 1
	}
	p.flush()
	top := p.brackets[len(p.brackets)-1]
	open := &p.toks[top]
	p.brackets = p.brackets[:len(p.brackets)-1]
	if open.active && i+1 < len(s) && s[i+1] == '(' {
		if url, end, ok := p.destination(i + 2); ok {
			p.processEmphasis(open.delimMark)
			open.kind, open.url = tkLinkStart, url
			p.toks = append(p.toks, inlineTok{kind: tkLinkEnd, img: open.img})
			if !open.img { // a link may not contain a link: the brackets before this one can no longer be links
				for _, b := range p.brackets {
					if !p.toks[b].img {
						p.toks[b].active = false
					}
				}
			}
			return end
		}
	}
	p.txt.WriteByte(']')
	return i + 1
}

// mdSpaceOrNL recognizes the space and newline bytes accepted by link parsing.
func mdSpaceOrNL(c byte) bool { return c == ' ' || c == '\n' }

// destination parses "url", "<url>" and an optional title after "(" (which is at i-1), up to and including ")". It returns
// the url and the index after the ")".
func (p *inlineParser) destination(i int) (url string, end int, ok bool) {
	s := p.s
	limit := min(len(s), i+mdMaxDest)
	for i < limit && mdSpaceOrNL(s[i]) {
		i++
	}
	var dest string
	if i < limit && s[i] == '<' {
		j := i + 1
		for j < limit && s[j] != '>' && s[j] != '\n' && s[j] != '<' {
			j++
		}
		if j >= limit || s[j] != '>' {
			return "", 0, false
		}
		dest, i = s[i+1:j], j+1
	} else {
		depth, j := 0, i
	scan:
		for j < limit {
			switch c := s[j]; {
			case mdSpaceOrNL(c):
				break scan
			case c == '\\' && j+1 < limit && mdASCIIPunct(s[j+1]):
				j++
			case c == '(':
				depth++
			case c == ')':
				if depth == 0 {
					break scan
				}
				depth--
			}
			j++
		}
		if depth != 0 {
			return "", 0, false
		}
		dest, i = s[i:j], j
	}
	save := i
	for i < limit && mdSpaceOrNL(s[i]) {
		i++
	}
	if i < limit && i > save && (s[i] == '"' || s[i] == '\'' || s[i] == '(') {
		closer := s[i]
		if closer == '(' {
			closer = ')'
		}
		j := i + 1
		for j < limit && s[j] != closer {
			if s[j] == '\\' {
				j++
			}
			j++
		}
		if j >= limit {
			return "", 0, false
		}
		i = j + 1
		for i < limit && mdSpaceOrNL(s[i]) {
			i++
		}
	}
	if i >= limit || s[i] != ')' {
		return "", 0, false
	}
	return mdUnescape(dest), i + 1, true
}

// mdUnescape resolves backslash escapes of ASCII punctuation.
func mdUnescape(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && mdASCIIPunct(s[i+1]) {
			i++
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// autolink handles "<" : a <scheme:address> or <mail@address> becomes a link; any other "<" (a tag, a comparison) is text.
func (p *inlineParser) autolink(i int) int {
	s := p.s
	limit := min(len(s), i+mdMaxAutolnk)
	j := i + 1
	for j < limit && s[j] != '>' && s[j] != '<' && s[j] != ' ' && s[j] != '\n' {
		j++
	}
	if j >= limit || s[j] != '>' || j == i+1 {
		p.txt.WriteByte('<')
		return i + 1
	}
	cand := s[i+1 : j]
	if isURIAutolink(cand) || isEmailAutolink(cand) {
		p.add(inlineTok{kind: tkAuto, text: cand, url: cand})
		return j + 1
	}
	p.txt.WriteByte('<')
	return i + 1
}

func isURIAutolink(s string) bool {
	colon := strings.IndexByte(s, ':')
	if colon < 2 || colon > 32 {
		return false
	}
	for i := 0; i < colon; i++ {
		c := s[i]
		alpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !alpha && (i == 0 || !((c >= '0' && c <= '9') || c == '+' || c == '.' || c == '-')) {
			return false
		}
	}
	return true
}

// isEmailAutolink requires one internal at sign and excludes slash, colon, and backslash; it is a
// lightweight Markdown check.
func isEmailAutolink(s string) bool {
	at := strings.IndexByte(s, '@')
	if at < 1 || at == len(s)-1 || strings.ContainsAny(s, "/:\\") || strings.Count(s, "@") != 1 {
		return false
	}
	return true
}

// bareURL recognises http:// and https:// addresses in running text (GFM's autolink extension) at a word boundary. The address
// ends at white space or "<"; trailing punctuation, and a ")" with no "(" before it, are not part of it.
func (p *inlineParser) bareURL(i int) int {
	s := p.s
	rest := s[i:]
	if !(strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://")) {
		p.txt.WriteByte('h')
		return i + 1
	}
	if b := mdLastRune(s[:i]); i > 0 && (unicode.IsLetter(b) || unicode.IsDigit(b)) {
		p.txt.WriteByte('h')
		return i + 1
	}
	j := i
	inBrackets := len(p.brackets) > 0 // inside [...], the "]" that closes the link text ends the address
	for j < len(s) && s[j] != ' ' && s[j] != '\n' && s[j] != '<' && !(inBrackets && s[j] == ']') {
		j++
	}
	for j > i {
		c := s[j-1]
		if strings.IndexByte("?!.,:*_~'\";", c) >= 0 {
			j--
			continue
		}
		if c == ')' && strings.Count(s[i:j], "(") < strings.Count(s[i:j], ")") {
			j--
			continue
		}
		break
	}
	scheme := strings.Index(s[i:], "://") + 3
	if j-i <= scheme {
		p.txt.WriteByte('h')
		return i + 1
	}
	p.add(inlineTok{kind: tkAuto, text: s[i:j], url: s[i:j]})
	return j
}

// ---- entities ----

// entity decodes &amp; &#35; &#x23; style references; anything else is a literal "&". A reference that decodes to a control
// character (&#27;) is dropped: nothing in the output may be one.
func (p *inlineParser) entity(i int) int {
	s := p.s
	limit := min(len(s), i+34)
	j := i + 1
	for j < limit && s[j] != ';' && s[j] != ' ' && s[j] != '&' && s[j] != '\n' {
		j++
	}
	if j >= limit || s[j] != ';' || j == i+1 {
		p.txt.WriteByte('&')
		return i + 1
	}
	ref := s[i : j+1]
	dec := html.UnescapeString(ref)
	if dec == ref {
		p.txt.WriteByte('&')
		return i + 1
	}
	p.txt.WriteString(mdStripControls(dec))
	return j + 1
}

// mdStripControls removes every control character and invisible rune from s (no escape-sequence logic: an ESC is just dropped).
func mdStripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) || invisibleRune(r) || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, s)
}
