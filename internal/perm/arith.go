package perm

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// What an arithmetic expansion of a command line may assign. Bash arithmetic ($((n+1)), $((n=5))) assigns only integers, and only
// to the names it reaches: those its text names, and, since bash evaluates a variable's value as an expression, those named by the
// values of the variables it reads. When every name an expansion reads holds an integer, the names it may assign are exactly those
// of its text; the line's other variables keep their value (a test script's counter, n=$((n+1)), used to make the scratch directory
// it then deleted unknown).

// intSpecials are the shell's own variables that always hold an integer.
var intSpecials = map[string]bool{
	"RANDOM": true, "SRANDOM": true, "SECONDS": true, "EPOCHSECONDS": true, "LINENO": true, "PPID": true, "BASHPID": true,
	"UID": true, "EUID": true, "OPTIND": true,
}

// intVars are the names a command line holds an integer in, from a point on: every assignment to the name on the line is an
// integer (intValue), no builtin sets it, and the value is the index in cmds of its first unconditional assignment outside any
// loop, before which it holds what the environment gave it. It is nil when a builtin of the line may set any variable.
func intVars(cmds []shellparse.Simple) map[string]int {
	first, bad := map[string]int{}, map[string]bool{}
	for i, s := range cmds {
		for _, env := range assignedBy(s) {
			name, val, _ := strings.Cut(env, "=")
			name = strings.TrimSuffix(name, "+")
			if !intValue(val) {
				bad[name] = true
				continue
			}
			if _, ok := first[name]; !ok && unconditional(s) && s.Loop == 0 {
				first[name] = i
			}
		}
		if reassigns(s) {
			names, all := reassignedNames(s)
			if all {
				return nil
			}
			for _, n := range names {
				bad[n] = true
			}
		}
	}
	for n := range bad {
		delete(first, n)
	}
	return first
}

// intValue reports whether an assigned value (quotes removed) is an integer: a number, one arithmetic expansion, or the output of a
// command that prints only digits and punctuation (digitCommand).
func intValue(v string) bool {
	if n := strings.TrimPrefix(v, "-"); n != "" && strings.Trim(n, "0123456789") == "" {
		return true
	}
	if strings.HasPrefix(v, "$((") && strings.HasSuffix(v, "))") {
		return closes(v, 1) == len(v)-1
	}
	if strings.HasPrefix(v, "$(") {
		j := closes(v, 1)
		return j == len(v)-1 && digitCommand(v[2:j])
	}
	return false
}

// closes is the index of the parenthesis that closes the one at s[open], or -1.
func closes(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// digitCommand reports whether a substituted command prints only digits and punctuation, so its output names no variable: date with
// a +FORMAT made of numeric conversions (date +%s, date -u +%Y%m%dT%H%M%S has a T, and is not one).
func digitCommand(cmd string) bool {
	f := strings.Fields(cmd)
	if len(f) < 2 || f[0] != "date" {
		return false
	}
	format := false
	for i := 1; i < len(f); i++ {
		switch a := f[i]; {
		case a == "-u" || a == "--utc":
		case strings.HasPrefix(a, "+") && !format:
			format = numericFormat(a[1:])
		default:
			return false
		}
	}
	return format
}

// numericFormat reports whether a date format prints only digits and punctuation: numeric conversions (%s, %N, %Y, %m, %d, %H, %M,
// %S, %j and the like, with flags and a width) and the characters 0-9 : . _ -.
func numericFormat(f string) bool {
	if f == "" {
		return false
	}
	for i := 0; i < len(f); i++ {
		c := f[i]
		if c != '%' {
			if !(c >= '0' && c <= '9' || strings.IndexByte(":._-", c) >= 0) {
				return false
			}
			continue
		}
		i++
		for i < len(f) && strings.IndexByte("-_0^#", f[i]) >= 0 {
			i++
		}
		for i < len(f) && f[i] >= '0' && f[i] <= '9' {
			i++
		}
		if i >= len(f) || strings.IndexByte("sNYmdHMSjuwyeIklCgGVUW", f[i]) < 0 {
			return false
		}
	}
	return true
}

// arithAssigns are the names the arithmetic expansions exprs of the command at index at of the line may assign, given the line's
// integer variables (intVars): every name their text reads or writes. all is true when that is not known: an expansion reads a name
// that may hold anything (its value is evaluated as an expression too: x='D=0'; $((x)) assigns D), a positional or listed parameter,
// a ${...} form, or the output of a command other than a digitCommand, whose text joins the expression.
func arithAssigns(exprs []string, at int, ints map[string]int) (names []string, all bool) {
	for _, e := range exprs {
		if strings.ContainsRune(e, '`') || strings.Contains(e, "${") {
			return nil, true
		}
		for {
			i := strings.Index(e, "$(")
			if i < 0 {
				break
			}
			if strings.HasPrefix(e[i:], "$((") { // an expansion inside this one: also in exprs, and its names are read here too
				e = e[:i] + e[i+1:]
				continue
			}
			j := closes(e, i+1)
			if j < 0 || !digitCommand(e[i+2:j]) {
				return nil, true
			}
			e = e[:i] + "0" + e[j+1:]
		}
		for i := 0; i < len(e); {
			c := e[i]
			switch {
			case c == '$' && i+1 < len(e) && strings.IndexByte("#?$!", e[i+1]) >= 0:
				i += 2
			case c == '$' && i+1 < len(e) && (e[i+1] >= '0' && e[i+1] <= '9' || strings.IndexByte("@*-", e[i+1]) >= 0):
				return nil, true // the positional parameters ($1, $@, $*) or the shell's flags ($-): text the line does not know
			case c == '$' && i+1 < len(e) && !isNameByte(e[i+1]) && e[i+1] != '{':
				i++
			case c == '$':
				i++
			case isNameByte(c) && (c < '0' || c > '9') && (i == 0 || !isNameByte(e[i-1]) && e[i-1] != '#'):
				j := i
				for j < len(e) && isNameByte(e[j]) {
					j++
				}
				name := e[i:j]
				i = j
				if intSpecials[name] {
					continue
				}
				if k, ok := ints[name]; !ok || k >= at {
					return nil, true
				}
				names = append(names, name)
			case isNameByte(c): // the rest of a number: 0x1f, 16#ff, 10#08
				for i < len(e) && (isNameByte(e[i]) || e[i] == '#') {
					i++
				}
			default:
				i++
			}
		}
	}
	return names, false
}
