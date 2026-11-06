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

// selfLine is the harness's own command line as ps -o args shows it, and selfName the name pkill and killall match without -f.
var (
	selfLine = sync.OnceValue(func() string { return strings.Join(os.Args, " ") })
	selfName = sync.OnceValue(func() string { return filepath.Base(os.Args[0]) })
)

// signalPrograms are the programs that send a signal to processes they are given.
var signalPrograms = map[string]bool{"kill": true, "pkill": true, "killall": true}

// endsHarness names what in command would signal the harness's own process (pid, its command line and its name), "" when
// nothing does: a command line that sends a signal (kill, pkill, killall, or one run by xargs) and picks processes by a pattern
// that the harness also matches (that of pkill, or of a pgrep or grep whose output picks them: ps | grep x | xargs kill,
// kill $(pgrep -f x)), or by the harness's pid. An agent that ends stray processes of its project by name otherwise ends the
// harness too when the project's path or name is in the harness's command line, and with it every agent of the session.
func endsHarness(command string, pid int, line, name string) string {
	cmds := shellparse.Parse(command).Commands
	signalling := func(c shellparse.Simple) bool {
		prog := filepath.Base(c.Program)
		return signalPrograms[prog] || prog == "xargs" && slices.ContainsFunc(c.Args, func(a string) bool { return signalPrograms[filepath.Base(a)] })
	}
	if !slices.ContainsFunc(cmds, signalling) {
		return ""
	}
	// feedsSignal reports whether the output of cmds[i] picks what is signalled: it is in a substitution, or its pipeline goes on
	// to a command that signals.
	feedsSignal := func(i int) bool {
		if cmds[i].Nested {
			return true
		}
		for k := i + 1; k < len(cmds) && cmds[k].Piped; k++ {
			if signalling(cmds[k]) {
				return true
			}
		}
		return false
	}
	for i, c := range cmds {
		prog := filepath.Base(c.Program)
		switch prog {
		case "kill":
			for _, a := range c.Args {
				if a == strconv.Itoa(pid) {
					return "the harness's pid " + a
				}
			}
		case "pkill", "pgrep":
			if prog == "pgrep" && !feedsSignal(i) {
				continue
			}
			full := slices.ContainsFunc(c.Args, func(a string) bool {
				return a == "--full" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(a, 'f'))
			})
			ops := operands(c.Args)
			if len(ops) == 0 {
				continue
			}
			pat, target := ops[len(ops)-1], name
			if full {
				target = line
			} else if len(target) > 15 {
				target = target[:15] // the kernel's process name, which pkill matches without -f
			}
			if patternMatches(pat, target, slices.Contains(c.Args, "-i"), false) {
				return fmt.Sprintf("the pattern %q of %s", pat, prog)
			}
		case "killall":
			for _, a := range operands(c.Args) {
				if a == name {
					return fmt.Sprintf("the name %q of killall", a)
				}
			}
		case "grep", "egrep", "fgrep":
			if !feedsSignal(i) || slices.ContainsFunc(c.Args, func(a string) bool { return grepFlag(a, 'v') || a == "--invert-match" }) {
				continue
			}
			var pats []string
			for i := 0; i < len(c.Args); i++ {
				if c.Args[i] == "-e" && i+1 < len(c.Args) {
					pats = append(pats, c.Args[i+1])
					i++
				}
			}
			if len(pats) == 0 {
				if ops := operands(c.Args); len(ops) > 0 {
					pats = ops[:1]
				}
			}
			fixed := prog == "fgrep" || slices.ContainsFunc(c.Args, func(a string) bool { return grepFlag(a, 'F') || a == "--fixed-strings" })
			fold := slices.ContainsFunc(c.Args, func(a string) bool { return grepFlag(a, 'i') || a == "--ignore-case" })
			for _, p := range pats {
				if patternMatches(p, line, fold, fixed) {
					return fmt.Sprintf("the pattern %q of grep", p)
				}
			}
		}
	}
	return ""
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

// grepFlag reports whether a is a group of short options that holds flag.
func grepFlag(a string, flag rune) bool {
	return len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsRune(a[1:], flag)
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
