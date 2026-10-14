package perm

import (
	"os"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// expansion is what the engine knows about the variables of one command line, besides the harness's own ($HOME, $TMPDIR and $PWD):
// the values the line itself assigned (vars, see evaluator.noteAssignments), and the names the line spells other than as an expansion
// (off: an assignment, read, unset, export, a loop variable), whose value may then be anything the line put in it. A special parameter
// of fragmentParams is read as an opaque piece of a file name unless the line names it so.
type expansion struct {
	vars map[string]string
	off  map[string]bool
}

// fragmentParams are the shell's special parameters whose value is a number (EPOCHREALTIME a decimal one): it never holds a "/" and is
// never "." or "..", so "$TMPDIR/x-$$.json" is a file in $TMPDIR whatever the number is. The value is true for the parameters a POSIX
// shell always sets ($$, $?, $#, $PPID); the others may be empty ($! before a background job; the names bash adds, when the shell is
// another sh), and a name part that an empty value would turn into "", "." or ".." is not known.
var fragmentParams = map[string]bool{
	"$": true, "?": true, "#": true, "PPID": true,
	"!": false, "BASHPID": false, "RANDOM": false, "SRANDOM": false, "SECONDS": false, "EPOCHSECONDS": false, "EPOCHREALTIME": false,
	"UID": false, "EUID": false, "LINENO": false,
}

// envFragments are the names of fragmentParams that a shell other than bash takes from the environment: one that the harness's own
// environment defines (and so passes to its commands) is not taken as a number.
var envFragments = []string{"BASHPID", "RANDOM", "SRANDOM", "SECONDS", "EPOCHSECONDS", "EPOCHREALTIME", "UID", "EUID", "LINENO"}

// definedFragments returns the names of envFragments that the process environment defines.
func definedFragments() map[string]bool {
	var out map[string]bool
	for _, n := range envFragments {
		if _, ok := os.LookupEnv(n); ok {
			if out == nil {
				out = map[string]bool{}
			}
			out[n] = true
		}
	}
	return out
}

// harnessNames are the variables whose expansion the engine knows without the line assigning them: $HOME, $TMPDIR and $PWD from its
// configuration and the working directory, and the named special parameters. A line that assigns one of them (PWD=/etc; cat "$PWD/x")
// makes it unknown for that line.
var harnessNames = []string{"HOME", "TMPDIR", "PWD", "PPID", "BASHPID", "RANDOM", "SRANDOM", "SECONDS", "EPOCHSECONDS", "EPOCHREALTIME",
	"UID", "EUID", "LINENO"}

// harnessOff is expansion.off for a command line: the harness's names (harnessNames) that the line spells other than as an expansion
// (mentioned), or that a builtin may set by naming them in an operand, after quote removal (read P""WD is read PWD; assignedNames), and
// all of them when such an operand is only known when the line runs (read "$n") or the builtin runs text (eval, source).
func harnessOff(cmd string, cmds []shellparse.Simple) map[string]bool {
	off := mentioned(cmd, harnessNames)
	set := func(n string) {
		if off == nil {
			off = map[string]bool{}
		}
		off[n] = true
	}
	for _, c := range cmds {
		names, all := assignedNames(c)
		for _, a := range names {
			all = all || strings.ContainsAny(a, "$`")
			for _, n := range harnessNames {
				if a == n || strings.HasPrefix(a, n+"=") || strings.HasPrefix(a, n+"[") {
					set(n)
				}
			}
		}
		if all {
			for _, n := range harnessNames {
				set(n)
			}
		}
	}
	return off
}

// assignedNames are the operands of a builtin that name a variable it may set (read NAME, unset NAME, getopts opts NAME, mapfile NAME,
// let NAME=1, coproc NAME, printf -v NAME, wait -p NAME, the names of a declaration and, with declare -n, the names it refers to); all
// is true for a builtin that runs text (eval, source, .).
func assignedNames(c shellparse.Simple) (names []string, all bool) {
	args := c.Args
	switch shellparse.ProgramName(c.Program) {
	case "eval", "source", ".":
		return nil, true
	case "read", "unset", "getopts", "mapfile", "readarray", "let", "coproc":
		return args, false
	case "printf":
		if len(args) > 0 && strings.HasPrefix(args[0], "-v") {
			return args[:min(2, len(args))], false
		}
	case "wait":
		for _, a := range args {
			if strings.HasPrefix(a, "-") && strings.Contains(a, "p") {
				return args, false
			}
		}
	case "export", "readonly", "declare", "typeset", "local":
		nameref := false
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				nameref = nameref || strings.Contains(a, "n")
				continue
			}
			name, val, _ := strings.Cut(a, "=")
			names = append(names, name)
			if nameref {
				names = append(names, val)
			}
		}
	}
	return names, false
}

// mentioned returns the names among names that cmd spells other than as an expansion ($NAME, or "${" and the name): NAME=x, export
// NAME, read NAME, unset NAME, for NAME in, ${!NAME} and ${#NAME}. It is lexical and over-approximates: a name inside a quoted string
// counts too.
func mentioned(cmd string, names []string) map[string]bool {
	var out map[string]bool
	for _, n := range names {
		for i := 0; i < len(cmd); {
			j := strings.Index(cmd[i:], n)
			if j < 0 {
				break
			}
			j += i
			end := j + len(n)
			i = end
			if j > 0 && isNameByte(cmd[j-1]) || end < len(cmd) && isNameByte(cmd[end]) {
				continue // part of a longer name
			}
			rest := cmd[end:]
			if j >= 1 && cmd[j-1] == '$' || j >= 2 && cmd[j-2:j] == "${" && !strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, ":=") {
				continue // an expansion (${NAME=x} and ${NAME:=x} assign)
			}
			if out == nil {
				out = map[string]bool{}
			}
			out[n] = true
			break
		}
	}
	return out
}

// expandWordVars expands the parts of a shell word that a permission check can know: a leading tilde, $HOME / ${HOME}, $PWD / ${PWD},
// $TMPDIR / ${TMPDIR} when the session has a private scratch directory, the variables the same command line assigned a known value to
// (x.vars), and the special parameters of fragmentParams, which stay in the result as written ("$$") as a piece of a name. dynamic
// reports that something else (another variable, a substitution, ~user) is left, in which case the word does not name a file the engine
// can judge; partial is then the text before the first unknown piece, so "~/.ssh/$KEY" is still seen to be inside ~/.ssh.
func (rs *resolver) expandWordVars(w, cwd string, x expansion) (out, partial string, dynamic bool) {
	switch {
	case w == "~" || strings.HasPrefix(w, "~/"):
		if rs.home == "" || x.off["HOME"] {
			return "", "", true
		}
		w = rs.home + w[1:]
	case w == "~+" || strings.HasPrefix(w, "~+/"):
		if cwd == "" || x.off["PWD"] {
			return "", "", true
		}
		w = cwd + w[2:]
	case strings.HasPrefix(w, "~"):
		return "", "", true // ~user, ~-, ~1
	}
	if !strings.ContainsAny(w, "$`") {
		return w, "", false
	}
	// b is the word with each special parameter as written; e is the same word with every parameter that may be empty left out, to see
	// what a name part becomes when it is.
	var b, e strings.Builder
	frag := false
	put := func(s string) { b.WriteString(s); e.WriteString(s) }
	for i := 0; i < len(w); {
		c := w[i]
		if c == '`' {
			return "", b.String(), true
		}
		if c != '$' {
			b.WriteByte(c)
			e.WriteByte(c)
			i++
			continue
		}
		j := i + 1
		var name string
		switch {
		case j < len(w) && w[j] == '{':
			k := strings.IndexByte(w[j:], '}')
			if k < 0 {
				return "", b.String(), true
			}
			name = w[j+1 : j+k]
			j += k + 1
		case j < len(w) && strings.IndexByte("$?#!", w[j]) >= 0:
			name = w[j : j+1]
			j++
		default:
			k := j
			for k < len(w) && isNameByte(w[k]) {
				k++
			}
			name = w[j:k]
			j = k
		}
		switch {
		case name == "HOME" && !x.off[name]:
			if rs.home == "" {
				return "", b.String(), true
			}
			put(rs.home)
		case name == "TMPDIR" && !x.off[name]:
			if rs.tmp == "" {
				return "", b.String(), true
			}
			put(rs.tmp)
		case name == "PWD" && !x.off[name]:
			if cwd == "" {
				return "", b.String(), true
			}
			put(cwd)
		case rs.fragment(name) && !x.off[name]:
			frag = true
			b.WriteString("$" + name)
			if fragmentParams[name] {
				e.WriteString("0") // never empty
			}
		default:
			v, ok := x.vars[name]
			if !ok {
				return "", b.String(), true
			}
			put(v)
		}
		i = j
	}
	if frag && !partsNameEntries(b.String(), e.String()) {
		return "", "", true
	}
	return b.String(), "", false
}

// fragment reports whether name is a special parameter that expands to a number in the session's shell (fragmentParams, less the
// names the harness's environment defines).
func (rs *resolver) fragment(name string) bool {
	_, ok := fragmentParams[name]
	return ok && !rs.envFrag[name]
}

// partsNameEntries reports whether every name part of a word that holds a special parameter (full, as written) still names an entry
// when the parameters that may be empty are (empty): a part that would then be "", "." or ".." is the directory itself or its parent.
// Both strings have the same parts, since a parameter holds no "/".
func partsNameEntries(full, empty string) bool {
	fp, ep := strings.Split(full, "/"), strings.Split(empty, "/")
	if len(fp) != len(ep) {
		return false
	}
	for i := range fp {
		if fp[i] != ep[i] && (ep[i] == "" || ep[i] == "." || ep[i] == "..") {
			return false
		}
	}
	return true
}

// expansion is what the evaluator knows of the variables of the line it judges.
func (ev *evaluator) expansion() expansion { return expansion{vars: ev.vars, off: ev.off} }

// knownWord is a word with the parts the check knows put in (expandWordVars, from the directory the command line starts in); when the
// rest is only known as the command runs, it is the known beginning and false.
func (ev *evaluator) knownWord(w string) (string, bool) {
	out, partial, dyn := ev.rs.expandWordVars(w, ev.startDir(), ev.expansion())
	if dyn {
		return partial, false
	}
	return out, true
}
