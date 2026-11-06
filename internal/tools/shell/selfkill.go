package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// fullLineMatch finds a pkill or pgrep that matches whole command lines (-f, --full): the pattern is then also found in the
// command line of the shell that runs it, which holds the same text.
var fullLineMatch = regexp.MustCompile(`\bp(?:kill|grep)\b[^;&|\n]*?(?:\s-[a-zA-Z]*f[a-zA-Z]*\b|\s--full\b)`)

// selfKillNote explains a command whose shell was ended by SIGTERM or SIGKILL (signal 15 or 9) the harness did not send,
// when it ran pkill -f or pgrep -f: the pattern it was given is in its own shell's command line, so it most likely ended
// itself before doing the rest of its work. It takes the signal, not the exit code, since a shell that exits normally
// with 143 (passing on a child's status) was not signalled. It is empty otherwise.
func selfKillNote(command string, signal int, reason killReason) string {
	if reason != killNone || (signal != 15 && signal != 9) || !fullLineMatch.MatchString(command) {
		return ""
	}
	return "[note: the command ended on a signal the harness did not send. pkill -f and pgrep -f match whole command lines, " +
		"and the shell running this command has the pattern in its own, so it most likely ended itself. Use a pattern " +
		"that cannot match itself, such as '[s]erver.rb' for 'server.rb', or kill by pid]"
}

// selfLine is the harness's own command line as ps -o args shows it, and selfName the name pkill and killall match without -f:
// the kernel's name for the process where it says (/proc/self/comm), else the base of argv[0].
var (
	selfLine = sync.OnceValue(func() string { return strings.Join(os.Args, " ") })
	selfName = sync.OnceValue(func() string {
		if b, err := os.ReadFile("/proc/self/comm"); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			return strings.TrimSpace(string(b))
		}
		return filepath.Base(os.Args[0])
	})
)

// signalPrograms are the programs that send a signal to processes they are given.
var signalPrograms = map[string]bool{"kill": true, "pkill": true, "killall": true}

// signalWord finds a signalling program named anywhere in a command line's text, for a line too complex to parse.
var signalWord = regexp.MustCompile(`(^|[^\w.-])(kill|pkill|killall)([^\w.-]|$)`)

// endsHarness names what in command would signal the harness's own process (pid, its command line and its name), "" when
// nothing does. It is a guard against the common ways an agent ends processes by name, not a sandbox: a command line that
// sends a signal (kill, pkill, killall, also under sudo or run by xargs) and picks processes by a pattern the harness also
// matches (that of pkill, or of a pgrep or grep whose output picks them: ps | grep x | xargs kill, kill $(pgrep -f x)), or by
// the harness's pid. A line too complex to read whole that names a signalling program is named as well. An agent that ends
// stray processes of its project by name otherwise ends the harness too when the project's path or name is in the harness's
// command line, and with it every agent of the session.
func endsHarness(command string, pid int, line, name string) string {
	an := shellparse.Parse(command)
	if !an.Parsed {
		if signalWord.MatchString(command) {
			return "a signalling command in a line too complex to check (" + an.Problem + ")"
		}
		return ""
	}
	// Each command is taken as what runs: the command under sudo or doas, and the one xargs runs.
	type cmd struct {
		prog   string
		args   []string
		nested bool
		piped  bool
	}
	cmds := make([]cmd, 0, len(an.Commands))
	for _, c := range an.Commands {
		prog, args := c.EffectiveCommand()
		prog = filepath.Base(prog)
		if prog == "xargs" {
			if i := slices.IndexFunc(args, func(a string) bool { return signalPrograms[filepath.Base(a)] }); i >= 0 {
				prog, args = filepath.Base(args[i]), args[i+1:]
			}
		}
		cmds = append(cmds, cmd{prog: prog, args: args, nested: c.Nested, piped: c.Piped})
	}
	// feedsSignal reports whether the output of cmds[i] picks what is signalled: it is in a substitution of a command that
	// signals (substitutions are listed right after their command), or its pipeline goes on to a command that signals.
	feedsSignal := func(i int) bool {
		if cmds[i].nested {
			for k := i - 1; k >= 0; k-- {
				if !cmds[k].nested {
					return signalPrograms[cmds[k].prog]
				}
			}
			return false
		}
		for k := i + 1; k < len(cmds) && cmds[k].piped; k++ {
			if signalPrograms[cmds[k].prog] {
				return true
			}
		}
		return false
	}
	psLine := strconv.Itoa(pid) + " " + line // ps -eo pid,args puts the pid first
	for i, c := range cmds {
		switch c.prog {
		case "kill":
			for _, a := range c.args {
				if a == strconv.Itoa(pid) {
					return "the harness's pid " + a
				}
			}
		case "pkill", "pgrep":
			if c.prog == "pgrep" && !feedsSignal(i) {
				continue
			}
			o := procOptions(c.args)
			if o.pattern == "" {
				continue
			}
			target := name
			if o.full {
				target = line
			} else if len(target) > 15 {
				target = target[:15] // the kernel's process name, which pkill matches without -f
			}
			if o.exact {
				o.pattern = "^(?:" + o.pattern + ")$"
			}
			if patternMatches(o.pattern, target, o.fold, false) {
				return fmt.Sprintf("the pattern %q of %s", o.pattern, c.prog)
			}
		case "killall":
			for _, a := range operands(c.args) {
				if a == name {
					return fmt.Sprintf("the name %q of killall", a)
				}
			}
		case "grep", "egrep", "fgrep":
			if !feedsSignal(i) {
				continue
			}
			o := grepOptions(c.prog, c.args)
			if o.invert {
				continue
			}
			for _, p := range o.patterns {
				expr, fixed := p, o.fixed
				if !fixed && !o.extended {
					expr = basicToExtended(p)
				}
				if o.word || o.line {
					if fixed {
						expr, fixed = regexp.QuoteMeta(p), false
					}
					if o.line {
						expr = "^(?:" + expr + ")$"
					} else {
						expr = `(?:^|\W)(?:` + expr + `)(?:\W|$)`
					}
				}
				if patternMatches(expr, line, o.fold, fixed) || patternMatches(expr, psLine, o.fold, fixed) {
					return fmt.Sprintf("the pattern %q of grep", p)
				}
			}
			if o.fromFile {
				return "a grep whose patterns come from a file, which cannot be checked"
			}
		}
	}
	return ""
}

// procOpts is what endsHarness reads of a pkill or pgrep command: the pattern and how it is matched.
type procOpts struct {
	pattern           string
	full, fold, exact bool
}

// procValueOpts are the short options of pkill and pgrep that take a value (the next word, or the rest of the word).
const procValueOpts = "dgGPstuUFqrc"

// procOptions reads pkill's and pgrep's options (procps): -f/--full, -i/--ignore-case, -x/--exact, options that take a value,
// and a signal (-9, -KILL, --signal X); the pattern is the operand left.
func procOptions(args []string) procOpts {
	var o procOpts
	var ops []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			ops = append(ops, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, _, hasValue := strings.Cut(a[2:], "=")
			switch name {
			case "full":
				o.full = true
			case "ignore-case":
				o.fold = true
			case "exact":
				o.exact = true
			case "signal", "delimiter", "pgroup", "group", "parent", "session", "terminal", "euid", "uid", "pidfile", "ns", "nslist", "env", "runstates", "cgroup":
				if !hasValue {
					i++
				}
			}
		case len(a) > 1 && a[0] == '-':
			if _, err := strconv.Atoi(a[1:]); err == nil || strings.ToUpper(a[1:]) == a[1:] && len(a) > 2 {
				continue // a signal: -9, -KILL, -HUP
			}
			for j, r := range a[1:] {
				switch {
				case r == 'f':
					o.full = true
				case r == 'i':
					o.fold = true
				case r == 'x':
					o.exact = true
				case strings.ContainsRune(procValueOpts, r):
					if j == len(a)-2 {
						i++ // the value is the next word
					}
				default:
					continue
				}
				if strings.ContainsRune(procValueOpts, r) {
					break // the rest of the word is the value
				}
			}
		default:
			ops = append(ops, a)
		}
	}
	if len(ops) > 0 {
		o.pattern = ops[len(ops)-1]
	}
	return o
}

// grepOpts is what endsHarness reads of a grep command.
type grepOpts struct {
	patterns                                            []string
	fixed, extended, fold, invert, word, line, fromFile bool
}

// grepValueOpts are grep's short options that take a value besides -e and -f.
const grepValueOpts = "ABCmdD"

// grepOptions reads grep's options: the patterns (-e, attached or not, --regexp, else the first operand), -F, -E, -i, -v, -w,
// -x and their long forms, and whether patterns come from a file (-f, --file).
func grepOptions(prog string, args []string) grepOpts {
	o := grepOpts{fixed: prog == "fgrep", extended: prog == "egrep"}
	var ops []string
	explicit := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			ops = append(ops, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, value, hasValue := strings.Cut(a[2:], "=")
			switch name {
			case "regexp":
				if !hasValue && i+1 < len(args) {
					i++
					value = args[i]
				}
				o.patterns, explicit = append(o.patterns, value), true
			case "file":
				if !hasValue {
					i++
				}
				o.fromFile, explicit = true, true
			case "fixed-strings":
				o.fixed = true
			case "extended-regexp":
				o.extended = true
			case "ignore-case":
				o.fold = true
			case "invert-match":
				o.invert = true
			case "word-regexp":
				o.word = true
			case "line-regexp":
				o.line = true
			case "after-context", "before-context", "context", "max-count", "label", "include", "exclude", "exclude-dir", "devices", "directories", "binary-files", "color", "colour":
				if !hasValue && name != "color" && name != "colour" {
					i++
				}
			}
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				r := a[j]
				switch {
				case r == 'e' || r == 'f':
					value := a[j+1:]
					if value == "" && i+1 < len(args) {
						i++
						value = args[i]
					}
					if r == 'e' {
						o.patterns = append(o.patterns, value)
					} else {
						o.fromFile = true
					}
					explicit, j = true, len(a)
				case strings.IndexByte(grepValueOpts, r) >= 0:
					if j == len(a)-1 {
						i++
					}
					j = len(a)
				case r == 'F':
					o.fixed = true
				case r == 'E':
					o.extended = true
				case r == 'G':
					o.extended = false
				case r == 'i' || r == 'y':
					o.fold = true
				case r == 'v':
					o.invert = true
				case r == 'w':
					o.word = true
				case r == 'x':
					o.line = true
				}
			}
		default:
			ops = append(ops, a)
		}
	}
	if !explicit && len(ops) > 0 {
		o.patterns = ops[:1]
	}
	return o
}

// basicToExtended turns a basic regular expression (grep without -E) into the syntax Go reads: the characters + ? | ( ) { }
// are literal in it, and special only after a backslash.
func basicToExtended(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p) && strings.IndexByte("+?|(){}", p[i+1]) >= 0:
			b.WriteByte(p[i+1])
			i++
		case c == '\\' && i+1 < len(p):
			b.WriteByte(c)
			b.WriteByte(p[i+1])
			i++
		case strings.IndexByte("+?|(){}", c) >= 0:
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// operands are the words of args that are not options.
func operands(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--" {
			return append(out, args[i+1:]...)
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			out = append(out, a)
		}
	}
	return out
}

// patternMatches reports whether the pattern pat (a regular expression unless fixed; one Go cannot read is taken as text) is
// found in s.
func patternMatches(pat, s string, fold, fixed bool) bool {
	if pat == "" {
		return false
	}
	if !fixed {
		expr := pat
		if fold {
			expr = "(?i)" + expr
		}
		if re, err := regexp.Compile(expr); err == nil {
			return re.MatchString(s)
		}
	}
	if fold {
		return strings.Contains(strings.ToLower(s), strings.ToLower(pat))
	}
	return strings.Contains(s, pat)
}
