package perm

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// A for loop over words that are written out, `for f in p1/p1.go p2/p2.go; do cat "$f"; done`, names its files in the list, and its body
// mentions only "$f": the engine could not tell which path that was, so the commonest first command of a model (read these files, list
// those directories) asked a question every time, and was refused outright in a run with nobody to ask. bindLoopVars gives the commands after
// the loop header one copy for each word of the list, with the variable replaced by it, so that each is judged as the command it will be.
//
// It binds only what it can be sure of. The words must be plain (no variable, substitution, quote, space or shell syntax: a glob is fine, the
// list is expanded by the shell exactly as the engine expands it); at most maxLoopWords of them; and nothing in the line may be able to change
// the variable or the way it is split: an assignment to it, read, mapfile, eval, declare and the like, printf -v, an IFS. Whatever it does not
// bind stays dynamic, and asks, as before.
const (
	maxLoopWords = 16
	maxCopies    = 32
)

// mutatesVariables are the builtins that can change a variable (or run text that can) by something other than a plain assignment.
var mutatesVariables = map[string]bool{
	"read": true, "mapfile": true, "readarray": true, "declare": true, "typeset": true, "local": true, "export": true, "readonly": true,
	"unset": true, "let": true, "eval": true, "source": true, ".": true, "getopts": true, "((": true,
}

func bindLoopVars(cmd string, cmds []shellparse.Simple) []shellparse.Simple {
	loops := 0
	for _, s := range cmds {
		if s.LoopVar != "" {
			loops++
		}
	}
	if loops == 0 || strings.Contains(cmd, "IFS") {
		return cmds
	}
	// What decides whether a variable can be trusted to be one of the loop's words is the whole line, not what comes before a use: a loop in a
	// subshell or a pipeline does not leave its variable behind, so a value given outside it (export f=/etc/shadow; (for f in a; do :; done);
	// cat $f) would still be what a later $f reads. So no binding at all if anything in the line can change a variable by a builtin, and none
	// for a variable that anything else assigns, or that two loops set.
	assigned, loopCount := map[string]bool{}, map[string]int{}
	for _, s := range cmds {
		name := shellparse.ProgramName(s.Program)
		if mutatesVariables[name] || strings.HasPrefix(s.Program, "((") || name == "printf" && len(s.Args) > 0 && strings.HasPrefix(s.Args[0], "-v") {
			return cmds
		}
		for _, env := range s.Env {
			n, _, _ := strings.Cut(env, "=")
			assigned[strings.TrimSuffix(n, "+")] = true
		}
		if s.LoopVar != "" {
			loopCount[s.LoopVar]++
		}
	}
	bound := map[string][]string{}
	var out []shellparse.Simple
	for _, s := range cmds {
		if s.Program == "for" && s.LoopVar != "" {
			if v := s.LoopVar; bindableName(v) && !assigned[v] && loopCount[v] == 1 && plainWords(s.Args) {
				bound[v] = s.Args
			}
			out = append(out, s)
			continue
		}
		out = append(out, expandBound(s, bound)...)
	}
	return out
}

// bindableName is a variable name that a script makes up for itself: lower case, not one of the environment's (PATH, HOME, ...).
func bindableName(n string) bool {
	if n == "" || n[0] >= 'A' && n[0] <= 'Z' {
		return false
	}
	for i := 0; i < len(n); i++ {
		if c := n[i]; !(c == '_' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// plainWords says whether a loop's words can stand for themselves: each is short and made of what a file name is made of.
func plainWords(words []string) bool {
	if len(words) == 0 || len(words) > maxLoopWords {
		return false
	}
	for _, w := range words {
		if w == "" || strings.ContainsAny(w, "$`\"'\\ \t\n;&|<>(){}!#") || strings.HasPrefix(w, "~") && w != "~" && !strings.HasPrefix(w, "~/") {
			return false
		}
	}
	return true
}

// expandBound is s once for each way its arguments can read the bound variables, or s alone when none of them does (or when there would be
// more than maxCopies of it, or an argument uses the variable in a form that is not a plain substitution).
func expandBound(s shellparse.Simple, bound map[string][]string) []shellparse.Simple {
	if len(bound) == 0 {
		return []shellparse.Simple{s}
	}
	// the variables the arguments mention, in order of first mention
	var names []string
	for _, a := range s.Args {
		for name := range bound {
			if strings.Contains(a, "$"+name) || strings.Contains(a, "${"+name+"}") {
				if !containsName(names, name) {
					names = append(names, name)
				}
			}
		}
	}
	if len(names) == 0 {
		return []shellparse.Simple{s}
	}
	copies := 1
	for _, n := range names {
		copies *= len(bound[n])
		if copies > maxCopies {
			return []shellparse.Simple{s}
		}
	}
	out := make([]shellparse.Simple, 0, copies)
	pick := make([]int, len(names))
	for {
		c := s
		c.Args = make([]string, len(s.Args))
		ok := true
		for i, a := range s.Args {
			c.Args[i] = a
			for j, n := range names {
				var done bool
				if c.Args[i], done = substituteVar(c.Args[i], n, bound[n][pick[j]]); !done {
					ok = false
				}
			}
		}
		if !ok {
			return []shellparse.Simple{s} // a form of the variable that is not a plain $f: it stays dynamic
		}
		out = append(out, c)
		// the next combination
		k := len(pick) - 1
		for k >= 0 {
			if pick[k]++; pick[k] < len(bound[names[k]]) {
				break
			}
			pick[k] = 0
			k--
		}
		if k < 0 {
			return out
		}
	}
}

func containsName(names []string, n string) bool {
	for _, x := range names {
		if x == n {
			return true
		}
	}
	return false
}

// substituteVar replaces $name and ${name} in w by value. It reports false when w uses the variable in another form (${name%.go}, ${name:-x}),
// which it leaves alone.
func substituteVar(w, name, value string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(w); {
		if w[i] != '$' {
			b.WriteByte(w[i])
			i++
			continue
		}
		rest := w[i+1:]
		switch {
		case strings.HasPrefix(rest, "{"+name+"}"):
			b.WriteString(value)
			i += len(name) + 3
		case strings.HasPrefix(rest, "{"+name):
			return w, false // ${name%...}, ${name:-...}, ${name/...}
		case strings.HasPrefix(rest, name) && (len(rest) == len(name) || !isNameByte(rest[len(name)])):
			b.WriteString(value)
			i += len(name) + 1
		default:
			b.WriteByte('$')
			i++
		}
	}
	return b.String(), true
}

func isNameByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
