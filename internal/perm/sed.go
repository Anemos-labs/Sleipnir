package perm

import "strings"

// `sed -n '120,160p' file` is how models read a range of lines, and in a run with no one to ask it was refused every time
// (sed can write files and run programs, so it is not on the read-only allowlist). The allowlist takes it back for the one
// shape that cannot do either: line selection. The script must be made of address commands and nothing else:
//
//	ADDR[,ADDR][!]CMD     with ADDR a line number, $, first~step, /regex/ (and +N, ~N after the comma) and CMD one of
//	                      p d = q Q (q and Q may carry an exit code), commands separated by ; or a newline.
//
// Everything that has an effect beyond printing or deleting lines of the stream is refused: s (its e and w flags run
// programs and write files), y, w, W, r, R, e, a, i, c, b, t, T, labels, blocks, n, N, D, G, H, x and the rest, -i and
// -f (the script lives in a file we cannot see), and any option this function does not know.

// sedSwitches are the options that only change how the stream is read or printed.
var sedSwitches = map[string]bool{
	"-n": true, "--quiet": true, "--silent": true, "-s": true, "--separate": true, "-E": true, "-r": true,
	"--regexp-extended": true, "-u": true, "--unbuffered": true, "-z": true, "--null-data": true, "--posix": true,
	"--sandbox": true,
}

func analyseSed(args []string) safeAnalysis {
	var scripts, files []string
	haveScript := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			files = append(files, args[i+1:]...)
			i = len(args)
		case sedSwitches[a]:
		case a == "-e" || a == "--expression":
			if i+1 >= len(args) {
				return safeAnalysis{why: "sed -e without a script"}
			}
			scripts, haveScript = append(scripts, args[i+1]), true
			i++
		case strings.HasPrefix(a, "--expression="):
			scripts, haveScript = append(scripts, strings.TrimPrefix(a, "--expression=")), true
		case strings.HasPrefix(a, "--"):
			return safeAnalysis{why: "sed option " + a + " can write files or run programs"}
		case len(a) > 1 && a[0] == '-':
			// A cluster of short options: -n, -ne, -nE. Only the switches above may be in it, and e takes the rest (or the
			// next word) as its script.
			for j := 1; j < len(a); j++ {
				c := a[j]
				if c == 'e' {
					script := a[j+1:]
					if script == "" {
						if i+1 >= len(args) {
							return safeAnalysis{why: "sed -e without a script"}
						}
						script = args[i+1]
						i++
					}
					scripts, haveScript = append(scripts, script), true
					break
				}
				if !sedSwitches["-"+string(c)] {
					return safeAnalysis{why: "sed option -" + string(c) + " can write files or run programs"}
				}
			}
		default:
			files = append(files, a)
		}
	}
	if !haveScript {
		if len(files) == 0 {
			return safeAnalysis{why: "sed without a script"}
		}
		scripts, files = []string{files[0]}, files[1:]
	}
	for _, s := range scripts {
		if why := sedScriptProblem(s); why != "" {
			return safeAnalysis{why: "sed script " + quote(s) + ": " + why}
		}
	}
	var uses []pathUse
	for _, f := range files {
		if f != "-" {
			uses = append(uses, pathUse{raw: f})
		}
	}
	return safeAnalysis{ok: true, uses: uses}
}

// sedScriptProblem returns why a script is more than line selection, or "" when it is nothing more.
func sedScriptProblem(s string) string {
	p := &sedParser{s: s}
	for {
		p.skip(" \t\n;")
		if p.done() {
			return ""
		}
		if why := p.command(); why != "" {
			return why
		}
	}
}

type sedParser struct {
	s string
	i int
}

func (p *sedParser) done() bool { return p.i >= len(p.s) }

func (p *sedParser) skip(set string) {
	for !p.done() && strings.IndexByte(set, p.s[p.i]) >= 0 {
		p.i++
	}
}

// command parses ADDR[,ADDR][!]CMD.
func (p *sedParser) command() string {
	had, why := p.address()
	if why != "" {
		return why
	}
	if !p.done() && p.s[p.i] == ',' {
		if !had {
			return "a range needs a first address"
		}
		p.i++
		p.skip(" \t")
		if !p.done() && (p.s[p.i] == '+' || p.s[p.i] == '~') {
			p.i++
			if !p.number() {
				return "an address after + or ~ must be a number"
			}
		} else if had, why := p.address(); why != "" || !had {
			return firstNonEmptyString(why, "a range needs a second address")
		}
	}
	p.skip(" \t")
	for !p.done() && p.s[p.i] == '!' {
		p.i++
		p.skip(" \t")
	}
	if p.done() {
		return "an address without a command"
	}
	c := p.s[p.i]
	p.i++
	switch c {
	case 'p', 'd', '=':
	case 'q', 'Q':
		p.skip(" \t")
		p.number() // an optional exit code
	default:
		return "the command " + quote(string(c)) + " is not plain line selection (only p, d, =, q and Q are)"
	}
	p.skip(" \t")
	if !p.done() && p.s[p.i] != ';' && p.s[p.i] != '\n' {
		return "unexpected " + quote(string(p.s[p.i])) + " after the command"
	}
	return ""
}

func firstNonEmptyString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// address parses a line number, $, first~step or /regex/ (with an optional I or M flag). had says whether there was
// one: a command may have none (every line), a range may not.
func (p *sedParser) address() (had bool, why string) {
	p.skip(" \t")
	if p.done() {
		return false, ""
	}
	switch c := p.s[p.i]; {
	case c == '$':
		p.i++
		return true, ""
	case c >= '0' && c <= '9':
		p.number()
		if !p.done() && p.s[p.i] == '~' {
			p.i++
			if !p.number() {
				return true, "first~step needs a step"
			}
		}
		return true, ""
	case c == '/':
		p.i++
		closed := false
		for !p.done() && !closed {
			switch p.s[p.i] {
			case '\\':
				p.i += 2 // an escaped character, including \/ and \n
			case '\n':
				return true, "a newline inside a regular expression"
			case '/':
				p.i++
				closed = true
			default:
				p.i++
			}
		}
		if !closed {
			return true, "unterminated regular expression"
		}
		for !p.done() && (p.s[p.i] == 'I' || p.s[p.i] == 'M') {
			p.i++
		}
		return true, ""
	}
	return false, ""
}

func (p *sedParser) number() bool {
	start := p.i
	for !p.done() && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	return p.i > start
}
