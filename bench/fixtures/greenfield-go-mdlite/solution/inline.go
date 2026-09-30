package mdlite

import (
	"html"
	"strings"
)

func isSpace(c byte) bool { return c == ' ' || c == '\t' }

func isWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// inline renders the inline syntax of one line of text.
func inline(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if next, ok := construct(&b, s, i); ok {
			i = next
			continue
		}
		b.WriteString(html.EscapeString(s[i : i+1])) // literal; every special character is ASCII
		i++
	}
	return b.String()
}

// construct renders the code span, link, strong or emphasis that starts at s[i], if there is one. It returns the
// index after it.
func construct(b *strings.Builder, s string, i int) (int, bool) {
	switch s[i] {
	case '`':
		return codeSpan(b, s, i)
	case '[':
		return link(b, s, i)
	case '*', '_':
		return emphasis(b, s, i)
	}
	return 0, false
}

// codeSpanEnd returns the index of the backtick that closes the code span opened at s[i], or -1.
func codeSpanEnd(s string, i int) int {
	n := strings.IndexByte(s[i+1:], '`')
	if n <= 0 { // none, or two backticks in a row
		return -1
	}
	return i + 1 + n
}

func codeSpan(b *strings.Builder, s string, i int) (int, bool) {
	j := codeSpanEnd(s, i)
	if j < 0 {
		return 0, false
	}
	b.WriteString("<code>" + html.EscapeString(s[i+1:j]) + "</code>")
	return j + 1, true
}

func link(b *strings.Builder, s string, i int) (int, bool) {
	n := strings.IndexByte(s[i+1:], ']')
	if n <= 0 {
		return 0, false
	}
	closeText := i + 1 + n
	if closeText+1 >= len(s) || s[closeText+1] != '(' {
		return 0, false
	}
	m := strings.IndexByte(s[closeText+2:], ')')
	if m <= 0 {
		return 0, false
	}
	url := s[closeText+2 : closeText+2+m]
	if strings.ContainsAny(url, " \t") {
		return 0, false
	}
	b.WriteString(`<a href="` + html.EscapeString(url) + `">` + inline(s[i+1:closeText]) + `</a>`)
	return closeText + 2 + m + 1, true
}

// findCloser returns the index of the first occurrence of delim at or after from, or -1. Code spans are skipped
// as a whole and, for a single "*", so are two asterisks in a row.
func findCloser(s string, from int, delim string) int {
	for k := from; k < len(s); {
		if s[k] == '`' {
			if end := codeSpanEnd(s, k); end >= 0 {
				k = end + 1
				continue
			}
		}
		if delim == "*" && strings.HasPrefix(s[k:], "**") {
			k += 2
			continue
		}
		if strings.HasPrefix(s[k:], delim) {
			return k
		}
		k++
	}
	return -1
}

func emphasis(b *strings.Builder, s string, i int) (int, bool) {
	delim, name := s[i:i+1], "em"
	if strings.HasPrefix(s[i:], "**") {
		delim, name = "**", "strong"
	}
	from := i + len(delim)
	if from >= len(s) || isSpace(s[from]) {
		return 0, false
	}
	if delim == "_" && i > 0 && isWord(s[i-1]) {
		return 0, false
	}
	k := findCloser(s, from, delim)
	if k <= from || isSpace(s[k-1]) {
		return 0, false
	}
	end := k + len(delim)
	if delim == "_" && end < len(s) && isWord(s[end]) {
		return 0, false
	}
	b.WriteString("<" + name + ">" + inline(s[from:k]) + "</" + name + ">")
	return end, true
}
