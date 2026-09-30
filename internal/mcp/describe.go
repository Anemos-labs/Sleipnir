package mcp

import (
	"fmt"
	"strconv"
	"strings"
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
		cmd := []string{oneLine(c.Command, argRunes)}
		for i, a := range c.Args {
			if i == maxArgs {
				break
			}
			cmd = append(cmd, strconv.Quote(oneLine(a, argRunes)))
		}
		b.WriteString(oneLine(strings.Join(cmd, " "), lineRunes))
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
