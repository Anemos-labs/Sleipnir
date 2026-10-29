package mdfile

import (
	"errors"
	"strings"
)

// shellCtx follows the quoting of a POSIX shell command as its text is copied, so that an argument substituted into it is
// spelled for the place it lands: a word of its own outside quotes, the inside of the quotes a template already put around the
// placeholder ('$1', "$1"), or refused where no spelling is known to stay literal (backquotes, $'...', after a backslash).
type shellCtx struct {
	stack []shellFrame
	prev  byte
	esc   bool
	// word is the unquoted word being read in a $( ); unsure is set for good once one of them is "case", or a ${ opens there: a
	// pattern's ")" (or one in ${v:-)}) does not end the substitution, which this tracker cannot tell, so no placeholder after it is
	// spelled for a guessed context.
	word   []byte
	unsure bool
	// inWord says the last byte was part of a word (not a separator): a "#" that starts a word starts a comment, whose quotes are
	// text up to the newline.
	inWord, comment bool
}

// shellFrame is one quoting context: kind is 'n' (unquoted), 's' ('...'), 'd' ("..."), 'a' ($'...') or 'b' (`...`); sub marks
// the unquoted text of a $( ) and parens counts the parentheses open inside it.
type shellFrame struct {
	kind   byte
	sub    bool
	parens int
}

// errShellContext refuses a placeholder whose quoting context has no literal spelling.
var errShellContext = errors.New("puts an argument inside backquotes, $'...', a comment, after a case inside $( ) or right after a backslash, where it cannot be quoted safely; write the placeholder unquoted or inside plain quotes ('$1', \"$1\")")

// feed advances the context over the template text s, copied to the command as it is.
func (c *shellCtx) feed(s string) {
	if len(c.stack) == 0 {
		c.stack = []shellFrame{{kind: 'n'}}
	}
	for i := 0; i < len(s); i++ {
		c.step(s[i])
	}
}

// step advances the context over one byte of template text.
func (c *shellCtx) step(ch byte) {
	prev := c.prev
	c.prev = ch
	if c.comment {
		if ch == '\n' {
			c.comment, c.inWord, c.word = false, false, c.word[:0]
		}
		return
	}
	top := &c.stack[len(c.stack)-1]
	if c.esc {
		c.esc, c.prev = false, 0
		if ch != '\n' { // a backslash-newline joins the lines and is no character; any other escaped one is a quoted part of a word
			c.inWord = true
			if top.kind == 'n' && top.sub {
				c.word = append(c.word, 0) // \case is not the keyword
			}
		}
		return
	}
	sep := strings.IndexByte(" \t\n;&|()<>", ch) >= 0
	if top.kind == 'n' && top.sub {
		if ch == '{' && prev == '$' {
			c.unsure = true
		}
		if ch == '\\' {
			// the escape decides what follows
		} else if !sep { // a word ends only where the shell ends one: ignore-case is not case
			c.word = append(c.word, ch)
		} else {
			if string(c.word) == "case" {
				c.unsure = true
			}
			c.word = c.word[:0]
		}
	}
	opened := false // a $( ) began here: its first word is a new one, whatever the quotes around it (a # there starts a comment)
	push := func(f shellFrame) {
		c.stack = append(c.stack, f)
		opened = opened || f.sub
	}
	closed := false // a quote or a $( ) ended here: the word it is part of goes on (x$(printf a)#b is one word)
	pop := func() {
		if len(c.stack) > 1 {
			c.stack, closed = c.stack[:len(c.stack)-1], true
		}
	}
	switch top.kind {
	case 's':
		if ch == '\'' {
			pop()
		}
	case 'a':
		switch ch {
		case '\\':
			c.esc = true
		case '\'':
			pop()
		}
	case 'd':
		switch {
		case ch == '\\':
			c.esc = true
		case ch == '"':
			pop()
		case ch == '`':
			push(shellFrame{kind: 'b'})
		case ch == '(' && prev == '$':
			push(shellFrame{kind: 'n', sub: true})
		}
	default: // 'n', 'b'
		switch {
		case ch == '#' && !c.inWord:
			c.comment = true
		case ch == '\\':
			c.esc = true
		case ch == '\'' && prev == '$':
			push(shellFrame{kind: 'a'})
		case ch == '\'':
			push(shellFrame{kind: 's'})
		case ch == '"':
			push(shellFrame{kind: 'd'})
		case ch == '`' && top.kind == 'b':
			pop()
		case ch == '`':
			push(shellFrame{kind: 'b'})
		case ch == '(' && prev == '$':
			push(shellFrame{kind: 'n', sub: true})
		case ch == '(':
			top.parens++
		case ch == ')' && top.parens > 0:
			top.parens--
		case ch == ')' && top.sub:
			pop()
		}
	}
	if ch != '\\' {
		c.inWord = !opened && (closed || !sep || (top.kind != 'n' && top.kind != 'b'))
	}
}

// quote spells words, the argument words a placeholder stands for, for the context the command has reached, or fails where
// none is known to stay literal. After it the context is as before: the words never open or close a quote.
func (c *shellCtx) quote(words []string) (string, error) {
	if len(c.stack) == 0 {
		c.stack = []shellFrame{{kind: 'n'}}
	}
	if c.esc || c.unsure || c.comment || (string(c.word) == "case" && c.stack[len(c.stack)-1].sub) {
		return "", errShellContext
	}
	for _, f := range c.stack {
		if f.kind == 'b' || f.kind == 'a' {
			return "", errShellContext
		}
	}
	c.prev, c.inWord = 0, true
	switch c.stack[len(c.stack)-1].kind {
	case 's':
		return strings.ReplaceAll(strings.Join(words, " "), "'", `'\''`), nil
	case 'd':
		var b strings.Builder
		for _, r := range strings.Join(words, " ") {
			if r == '\\' || r == '"' || r == '$' || r == '`' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		return b.String(), nil
	}
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = ShellQuote(w)
	}
	return strings.Join(q, " "), nil
}
