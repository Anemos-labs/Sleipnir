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
}

// shellFrame is one quoting context: kind is 'n' (unquoted), 's' ('...'), 'd' ("..."), 'a' ($'...') or 'b' (`...`); sub marks
// the unquoted text of a $( ) and parens counts the parentheses open inside it.
type shellFrame struct {
	kind   byte
	sub    bool
	parens int
}

// errShellContext refuses a placeholder whose quoting context has no literal spelling.
var errShellContext = errors.New("puts an argument inside backquotes, $'...' or right after a backslash, where it cannot be quoted safely; write the placeholder unquoted or inside plain quotes ('$1', \"$1\")")

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
	if c.esc {
		c.esc, c.prev = false, 0
		return
	}
	top := &c.stack[len(c.stack)-1]
	push := func(f shellFrame) { c.stack = append(c.stack, f) }
	pop := func() {
		if len(c.stack) > 1 {
			c.stack = c.stack[:len(c.stack)-1]
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
}

// quote spells words, the argument words a placeholder stands for, for the context the command has reached, or fails where
// none is known to stay literal. After it the context is as before: the words never open or close a quote.
func (c *shellCtx) quote(words []string) (string, error) {
	if len(c.stack) == 0 {
		c.stack = []shellFrame{{kind: 'n'}}
	}
	if c.esc {
		return "", errShellContext
	}
	for _, f := range c.stack {
		if f.kind == 'b' || f.kind == 'a' {
			return "", errShellContext
		}
	}
	c.prev = 0
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
