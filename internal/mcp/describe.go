package mcp

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Describe renders the entry for an approval prompt: what starting or
// contacting it would do, in enough detail to judge it. For a local server that
// is the whole command line (each argument quoted: a project's entry that runs
// `node -e "..."` must not hide behind the word "node"); for a remote one the
// scheme and host. Environment variables and headers are listed by name, never
// by value, together with the variables the entry asks the harness to expand.
// Everything is cleaned of control characters and terminal escapes and bounded,
// because the text comes from a file the reader does not control and is printed
// to a terminal.
//
// Describe is for entries that are about to be approved, which come from a
// repository and cannot hold the user's secrets. It prints arguments, which
// String deliberately does not, so do not use it for logs of trusted entries.
func (c ServerConfig) Describe() string {
	const maxArgs, argRunes, lineRunes = 16, 160, 600
	var b strings.Builder
	typ := c.EffectiveType()
	if typ == "" {
		typ = "invalid"
	}
	if typ == TypeStdio {
		b.WriteString("runs a local program: ")
		// Each word is quoted as Go quotes it, which shows a newline, a tab, a control or an invisible character as an escape
		// instead of folding or dropping it: the words read are the words run. A cut is marked.
		cmd := []string{shownWord(c.Command, argRunes)}
		for i, a := range c.Args {
			if i == maxArgs {
				break
			}
			cmd = append(cmd, quotedWord(a, argRunes))
		}
		line, _ := truncateRunes(strings.Join(cmd, " "), lineRunes)
		b.WriteString(line)
		if len(c.Args) > maxArgs {
			fmt.Fprintf(&b, " (+%d more arguments)", len(c.Args)-maxArgs)
		}
		if c.Cwd != "" {
			b.WriteString("; in " + oneLine(c.Cwd, argRunes))
		}
	} else {
		b.WriteString("connects to " + typ + " server " + redactURL(c.URL))
		if c.AllowPrivate {
			b.WriteString(" (private addresses allowed)")
		}
	}
	if names := sortedKeys(c.Env); len(names) > 0 {
		b.WriteString("; sets environment " + oneLine(strings.Join(names, ", "), argRunes))
	}
	if names := sortedKeys(c.Headers); len(names) > 0 {
		b.WriteString("; sends headers " + oneLine(strings.Join(names, ", "), argRunes))
	}
	if refs := c.EnvRefs(); len(refs) > 0 {
		b.WriteString("; reads your environment: $" + oneLine(strings.Join(refs, ", $"), argRunes))
	}
	return b.String()
}

// shownWord is a command word for an approval prompt: bare when it is plain (letters, digits and a few path characters), else
// quoted with every non-printing rune escaped; cut to max runes with a marker.
func shownWord(w string, max int) string {
	plain := w != ""
	for _, r := range w {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@+,", r)) {
			plain = false
			break
		}
	}
	if !plain {
		return quotedWord(w, max)
	}
	w, _ = truncateRunes(w, max)
	return w
}

// quotedWord is an argument for an approval prompt: always quoted, every non-printing rune escaped, cut to max runes with a marker.
// A word that is cut keeps its closing quote, and is cut between escapes, never inside one: the next word must not read as part of it.
func quotedWord(w string, max int) string {
	q := strconv.Quote(w)
	if max <= 0 || utf8.RuneCountInString(q) <= max {
		return q
	}
	room := max - 2 - utf8.RuneCountInString(truncMarker) // the two quotes and the marker
	if room < 1 {
		q, _ = truncateRunes(q, max)
		return q
	}
	var b strings.Builder
	used := 0
	for i, r := range w {
		size := utf8.RuneLen(r)
		if r == utf8.RuneError {
			_, size = utf8.DecodeRuneInString(w[i:])
		}
		piece := strconv.Quote(w[i : i+size])
		piece = piece[1 : len(piece)-1]
		n := utf8.RuneCountInString(piece)
		if used+n > room {
			break
		}
		b.WriteString(piece)
		used += n
	}
	return `"` + strings.TrimRight(b.String(), " ") + truncMarker + `"`
}
