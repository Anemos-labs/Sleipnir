// Package shellparse is a small, defensive analyser for shell command lines.
//
// It exists so the permission engine can answer "what would this string start?"
// without running it. It never executes anything and never touches the file
// system; every decision it makes is lexical.
//
// # Stance
//
// The analyser over-approximates. When a construct cannot be understood with
// confidence (unterminated quotes, case statements, function definitions,
// arithmetic commands, env -S, too much nesting, ...) Parse sets Parsed=false
// instead of guessing, and callers must treat such input as untrusted. Even
// then Commands holds a best-effort list, so a caller can still veto on what
// it did see: a command that is visibly bad stays bad when the rest is opaque.
//
// Words are reported with quotes and escapes removed ("r\m" and 'r'"m" both
// give rm; $'\x72m' gives rm) but expansions are NOT performed: "$HOME/x", a
// glob or a ${...} stays textual in Args, and it is the caller's job to treat
// such words as unresolved. Brace expansion is the exception because it is
// purely lexical: ~/.{ssh,aws}/x yields two words.
//
// Substitutions ($(...), backticks, <(...), >(...), and the strings handed to
// eval, sh -c, su -c and trap, and heredocs or here-strings fed to a shell) are
// parsed recursively and their commands are appended to Commands right after the
// command that contains them (Nested=true).
//
// Two conventions callers should know. The word list of a for/select loop is
// reported as a pseudo-command with Program "for" and the words after "in" as
// Args, so the files a loop walks are visible. A redirection that applies to a
// whole compound command ("done > out", "} > out", ") > out") appears as a
// command with no Program and just the Redirects.
//
// # Testing
//
// The parser is checked against bash itself: a differential test runs hundreds
// of snippets and generated compositions in a real bash and requires that every
// command that actually ran is present in Commands.
package shellparse

import (
	"strings"
)

// Limits keep hostile input from costing more than a bounded amount of work.
const (
	maxInput      = 1 << 20 // bytes; larger input is refused as unparsed
	maxDepth      = 12      // nesting of substitutions, eval and sh -c
	maxBraceWords = 256     // words one brace expression may expand into
	stepBudget    = 64      // lexer steps allowed per input byte (plus slack)
)

// Redirect is one redirection attached to a command. Op includes any file
// descriptor prefix exactly as written (">", ">>", "<", "2>", "&>", ">|",
// "2>&", "<<<", "<<") and Target is the following word with quotes removed. For
// heredocs the Target is the delimiter. For descriptor duplication (">&2",
// "2>&1", ">&-") Target is the descriptor or "-".
type Redirect struct {
	Op     string
	Target string
}

// Simple is one simple command: a program, its arguments and what is wrapped
// around it.
type Simple struct {
	// Program is the command word. Wrapper commands env, command, builtin,
	// nohup, time, exec, nice, ionice, setsid, stdbuf and timeout are peeled, so
	// Program is what would actually run; sudo/doas are NOT peeled (they change
	// privilege) - see Effective.
	Program string
	Args    []string
	// Env holds NAME=value assignments from the shell prefix and from an env
	// wrapper, in order.
	Env       []string
	Redirects []Redirect
	// Raw is the source text of the whole command, wrappers and redirections
	// included.
	Raw string
	// Wrappers lists the peeled wrapper programs, outermost first.
	Wrappers []string
	// Nested marks commands extracted from a substitution, eval or sh -c string
	// rather than written at the top level of the line.
	Nested bool
	// Piped marks a command that reads from the previous command's pipe.
	Piped bool
	// LoopVar is the variable of a for/select loop, on the pseudo-command "for" whose Args are the loop's word list ("f" in
	// `for f in a b c`); empty everywhere else, and for a loop with no word list.
	LoopVar string
}

// Analysis is the result of Parse.
type Analysis struct {
	// Commands is every simple command in source order, flattened across
	// ; && || | ( ) { } and newlines, followed (after each command) by the
	// commands found inside its substitutions.
	Commands []Simple

	HasSubshell            bool // ( ... ) grouping
	HasCommandSubstitution bool // $(...), `...` or a non-arithmetic $((...))
	HasProcessSubstitution bool // <(...) or >(...)
	PipesToShell           bool // curl ... | sh, ... | bash, ... | sudo bash
	Background             bool // some command is terminated by a single &
	HasHeredoc             bool
	Parsed                 bool   // false: too complex to trust, see Problem
	Problem                string // first reason Parsed became false
}

// state is shared by every lexer and parser working on one Parse call.
type state struct {
	an    Analysis
	steps int
	limit int
	dead  bool
}

// problem records only the first parse problem and marks the analysis incomplete.
func (st *state) problem(msg string) {
	if st.an.Parsed {
		st.an.Parsed = false
		st.an.Problem = msg
	}
}

// tick charges one unit of work and reports whether work may continue.
func (st *state) tick() bool {
	st.steps++
	if st.steps > st.limit {
		st.problem("input too complex")
		st.dead = true
	}
	return !st.dead
}

// Parse analyses cmd. It never panics and never returns Commands whose Raw is
// not a substring of the text they came from.
func Parse(cmd string) Analysis {
	st := &state{an: Analysis{Parsed: true}, limit: stepBudget*len(cmd) + 10_000}
	switch {
	case len(cmd) > maxInput:
		st.problem("input too large")
		return st.an
	case strings.IndexByte(cmd, 0) >= 0:
		// A NUL ends the string for the shell, so what runs is not what was
		// written; refuse to guess.
		st.problem("NUL byte in input")
		return st.an
	}
	l := newLexer(cmd, st, 0)
	toks, _ := l.lexUntil(false)
	st.an.Commands = parseTokens(st, cmd, toks, 0, true, false)
	st.an.PipesToShell = pipesToShell(st.an.Commands)
	return st.an
}

// Fields splits s into words using shell quoting rules and nothing else: no
// operators, substitutions or wrappers are interpreted. It reports false when
// s contains any of those, or an unterminated quote. It is meant for
// tokenising rule patterns, where "git commit -m 'a b'" must give four words.
func Fields(s string) ([]string, bool) {
	if len(s) > maxInput || strings.IndexByte(s, 0) >= 0 {
		return nil, false
	}
	st := &state{an: Analysis{Parsed: true}, limit: stepBudget*len(s) + 10_000}
	l := newLexer(s, st, 0)
	toks, _ := l.lexUntil(false)
	if !st.an.Parsed || st.an.HasCommandSubstitution || st.an.HasProcessSubstitution {
		return nil, false
	}
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if t.kind != tkWord {
			return nil, false
		}
		out = append(out, t.text)
	}
	return out, true
}

// Quote returns w quoted so that Fields (and a POSIX shell) read it back as one
// word. Words made only of safe characters are returned unchanged.
func Quote(w string) string {
	if w == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(w); i++ {
		c := w[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("_-+=/.,:@%", c) >= 0 || c >= 0x80) {
			safe = false
			break
		}
	}
	if safe {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

// Join quotes and joins words into one command line.
func Join(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = Quote(w)
	}
	return strings.Join(q, " ")
}
