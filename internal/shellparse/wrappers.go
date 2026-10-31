package shellparse

import (
	"path"
	"slices"
	"strings"
)

// stdDirs are the directories whose binaries we accept under an absolute path
// as "the real" env, sudo, sh, ... A relative or workspace path never counts: a
// script named ./env in a repository must not be mistaken for the wrapper, or
// an allow rule for the wrapped command would run attacker code.
var stdDirs = map[string]bool{
	"/usr/bin/": true, "/bin/": true, "/usr/local/bin/": true,
	"/usr/sbin/": true, "/sbin/": true, "/usr/local/sbin/": true,
}

// stdBase returns the program's name when it is a bare name or lives in a
// standard directory, and "" for any other path.
func stdBase(prog string) string {
	if !strings.Contains(prog, "/") {
		return prog
	}
	dir, base := path.Split(prog)
	if stdDirs[dir] {
		return base
	}
	return ""
}

// ProgramName returns the name of a command word when it is a bare name or a
// binary in a standard system directory (/usr/bin/git -> git), and "" for any
// other path (./git, /tmp/x/git, ~/bin/git): those are not the program of that
// name, and rules and allowlists keyed by name must not treat them as such.
func ProgramName(prog string) string { return stdBase(prog) }

// isAssignment reports whether a word begins with a valid shell variable name followed by an
// equals sign.
func isAssignment(a string) bool {
	eq := strings.IndexByte(a, '=')
	if eq <= 0 || !isNameStart(a[0]) {
		return false
	}
	for i := 1; i < eq; i++ {
		if !isNameChar(a[i]) {
			return false
		}
	}
	return true
}

var wrapperSet = map[string]bool{
	"env": true, "command": true, "builtin": true, "nohup": true, "time": true,
	"exec": true, "nice": true, "ionice": true, "setsid": true, "stdbuf": true,
	"timeout": true, "npx": true,
}

// peel strips wrapper programs from s in place, folding env assignments and
// time -o targets into Env and Redirects. A non-empty return describes a
// wrapper option it could not interpret; peeling stops there.
func peel(s *Simple) string {
	for i := 0; i < 8; i++ {
		name := stdBase(s.Program)
		if !wrapperSet[name] {
			return ""
		}
		rest, env, redirs, prob := unwrap(name, s.Args)
		if prob != "" {
			return prob
		}
		if len(rest) == 0 { // wrapper without a command (env alone, nice alone)
			return ""
		}
		s.Wrappers = append(s.Wrappers, name)
		s.Env = append(s.Env, env...)
		s.Redirects = append(s.Redirects, redirs...)
		s.Program, s.Args = rest[0], append([]string(nil), rest[1:]...)
	}
	return "too many nested wrapper commands"
}

// unwrap interprets wrapper options and returns the wrapped command words.
func unwrap(name string, args []string) (rest, env []string, redirs []Redirect, prob string) {
	i := 0
	bad := func(what string) ([]string, []string, []Redirect, string) {
		return nil, nil, nil, what
	}
	switch name {
	case "env":
	opts:
		for i < len(args) {
			a := args[i]
			switch {
			case a == "--":
				i++
				break opts
			case a == "-" || a == "-i" || a == "--ignore-environment" || a == "-0" ||
				a == "--null" || a == "-v" || a == "--debug":
				i++
			case a == "-u" || a == "--unset":
				i += 2
			case strings.HasPrefix(a, "--unset="):
				i++
			case a == "-C" || a == "--chdir" || strings.HasPrefix(a, "--chdir=") ||
				(strings.HasPrefix(a, "-C") && !strings.HasPrefix(a, "--")):
				return bad("env -C changes directory for the wrapped command")
			case a == "-S" || a == "--split-string" || strings.HasPrefix(a, "--split-string=") ||
				(strings.HasPrefix(a, "-S") && !strings.HasPrefix(a, "--")):
				return bad("env -S builds a command from a string")
			case len(a) > 1 && a[0] == '-':
				return bad("unrecognised env option " + a)
			case isAssignment(a):
				env = append(env, a)
				i++
			default:
				break opts
			}
		}
	case "command":
		for i < len(args) && args[i] == "-p" {
			i++
		}
		if i < len(args) && args[i] == "--" {
			i++
		}
		if i < len(args) && strings.HasPrefix(args[i], "-") {
			return nil, nil, nil, "" // command -v / -V only looks a name up
		}
	case "builtin":
	case "nohup":
		if len(args) > 0 && args[0] == "--" {
			i++
		} else if len(args) > 0 && strings.HasPrefix(args[0], "-") {
			return nil, nil, nil, ""
		}
	case "time":
	opts2:
		for i < len(args) {
			a := args[i]
			switch {
			case a == "--":
				i++
				break opts2
			case a == "-p" || a == "-v" || a == "-a" || a == "-q" || a == "-l" ||
				a == "--portability" || a == "--verbose" || a == "--append" || a == "--quiet":
				i++
			case a == "-f" || a == "--format":
				i += 2
			case a == "-o" || a == "--output":
				if i+1 < len(args) {
					redirs = append(redirs, Redirect{Op: ">", Target: args[i+1]})
				}
				i += 2
			case strings.HasPrefix(a, "--format="):
				i++
			case strings.HasPrefix(a, "--output="):
				redirs = append(redirs, Redirect{Op: ">", Target: strings.TrimPrefix(a, "--output=")})
				i++
			case len(a) > 1 && a[0] == '-':
				return bad("unrecognised time option " + a)
			default:
				break opts2
			}
		}
	case "exec":
	opts3:
		for i < len(args) {
			a := args[i]
			switch {
			case a == "--":
				i++
				break opts3
			case a == "-c" || a == "-l":
				i++
			case a == "-a":
				i += 2
			case len(a) > 1 && a[0] == '-':
				return bad("unrecognised exec option " + a)
			default:
				break opts3
			}
		}
	case "nice":
	opts4:
		for i < len(args) {
			a := args[i]
			switch {
			case a == "--":
				i++
				break opts4
			case a == "-n" || a == "--adjustment":
				i += 2
			case strings.HasPrefix(a, "--adjustment="):
				i++
			case len(a) > 1 && a[0] == '-' && (isDigit(a[1]) || a[1] == '-' && len(a) > 2 && isDigit(a[2])):
				i++
			case len(a) > 1 && a[0] == '-':
				return bad("unrecognised nice option " + a)
			default:
				break opts4
			}
		}
	case "ionice":
	opts5:
		for i < len(args) {
			a := args[i]
			switch {
			case a == "--":
				i++
				break opts5
			case a == "-c" || a == "--class" || a == "-n" || a == "--classdata" || a == "-p" ||
				a == "--pid" || a == "-P" || a == "--pgid" || a == "-u" || a == "--uid":
				i += 2
			case a == "-t" || a == "--ignore":
				i++
			case strings.HasPrefix(a, "--") && strings.Contains(a, "="):
				i++
			case len(a) > 2 && a[0] == '-' && strings.IndexByte("cnpPu", a[1]) >= 0:
				i++ // -c2, -n4: value attached
			case len(a) > 1 && a[0] == '-':
				return bad("unrecognised ionice option " + a)
			default:
				break opts5
			}
		}
	case "setsid":
		for i < len(args) {
			a := args[i]
			if a == "--" {
				i++
				break
			}
			if a == "-c" || a == "--ctty" || a == "-f" || a == "--fork" || a == "-w" || a == "--wait" {
				i++
				continue
			}
			if len(a) > 1 && a[0] == '-' {
				return bad("unrecognised setsid option " + a)
			}
			break
		}
	case "stdbuf":
	opts6:
		for i < len(args) {
			a := args[i]
			switch {
			case a == "--":
				i++
				break opts6
			case a == "-i" || a == "-o" || a == "-e" || a == "--input" || a == "--output" || a == "--error":
				i += 2
			case strings.HasPrefix(a, "--input=") || strings.HasPrefix(a, "--output=") || strings.HasPrefix(a, "--error="):
				i++
			case len(a) > 2 && a[0] == '-' && strings.IndexByte("ioe", a[1]) >= 0:
				i++ // -oL: value attached
			case len(a) > 1 && a[0] == '-':
				return bad("unrecognised stdbuf option " + a)
			default:
				break opts6
			}
		}
	case "npx":
		// npx runs a package's program, and first installs the package when it is not there (`npx tsc` without TypeScript installs an
		// unrelated package named tsc). --no, and its older spelling --no-install, forbid the install: the command is the program it names,
		// the project's own (node_modules/.bin) or nothing. Without one of them, or with an option that names, installs or builds what runs
		// (-p, --package, -c, --call, -y, --yes), npx is not peeled: it stays the program, as before.
		noInstall := false
	opts8:
		for i < len(args) {
			a := args[i]
			switch {
			case a == "--":
				i++
				break opts8
			case a == "--no" || a == "--no-install" || a == "--yes=false":
				noInstall = true
				i++
			case a == "-q" || a == "--quiet" || a == "--silent":
				i++
			case len(a) > 1 && a[0] == '-':
				return nil, nil, nil, ""
			default:
				break opts8
			}
		}
		if !noInstall {
			return nil, nil, nil, ""
		}
	case "timeout":
	opts7:
		for i < len(args) {
			a := args[i]
			switch {
			case a == "--":
				i++
				break opts7
			case a == "-k" || a == "-s" || a == "--kill-after" || a == "--signal":
				i += 2
			case strings.HasPrefix(a, "--kill-after=") || strings.HasPrefix(a, "--signal="):
				i++
			case a == "--preserve-status" || a == "--foreground" || a == "-v" || a == "--verbose":
				i++
			case len(a) > 2 && (strings.HasPrefix(a, "-k") || strings.HasPrefix(a, "-s")) && a[1] != '-':
				i++
			case len(a) > 1 && a[0] == '-':
				return bad("unrecognised timeout option " + a)
			default:
				break opts7
			}
		}
		i++ // the DURATION operand
	}
	if i >= len(args) {
		return nil, env, redirs, ""
	}
	return args[i:], env, redirs, ""
}

// privSpec describes how to skip the options of a privilege-escalation
// wrapper to find the command it runs.
type privSpec struct {
	shortValue string          // option letters that take a value
	longValue  map[string]bool // long options that take a value
	assigns    bool            // accepts NAME=value before the command
}

var privSpecs = map[string]privSpec{
	"sudo": {
		shortValue: "CDgpRrTtUu",
		longValue: map[string]bool{
			"chdir": true, "chroot": true, "close-from": true, "command-timeout": true,
			"group": true, "host": true, "other-user": true, "prompt": true,
			"role": true, "type": true, "user": true,
		},
		assigns: true,
	},
	"doas":   {shortValue: "uC"},
	"pkexec": {longValue: map[string]bool{"user": true}},
}

// isPrivileged reports whether the program is sudo, doas or pkexec.
func isPrivileged(prog string) bool {
	_, ok := privSpecs[stdBase(prog)]
	return ok
}

// skipPriv returns the words after the wrapper's own options: the command it
// will run (empty for sudo -i, sudo -l, ...).
func skipPriv(spec privSpec, args []string) []string {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--":
			return args[i+1:]
		case strings.HasPrefix(a, "--"):
			name, _, hasEq := strings.Cut(a[2:], "=")
			if spec.longValue[name] && !hasEq {
				i++
			}
			i++
		case len(a) > 1 && a[0] == '-':
			needNext := false
			for j := 1; j < len(a); j++ {
				if strings.IndexByte(spec.shortValue, a[j]) >= 0 {
					needNext = j == len(a)-1 // otherwise the rest of the cluster is the value
					break
				}
			}
			i++
			if needNext {
				i++
			}
		case spec.assigns && isAssignment(a):
			i++
		default:
			return args[i:]
		}
	}
	return nil
}

// Elevated reports whether the command runs through sudo, doas, pkexec or su,
// i.e. with someone else's privileges.
func (s Simple) Elevated() bool {
	return isPrivileged(s.Program) || stdBase(s.Program) == "su"
}

// Effective returns the program that actually runs: Program itself, or for
// sudo/doas/pkexec the command after their options (with wrappers peeled). It
// is "" when nothing runs (sudo -l) or it cannot be told (sudo -i).
func (s Simple) Effective() string {
	p, _ := s.EffectiveCommand()
	return p
}

// EffectiveCommand is Effective with the arguments of the command that runs.
func (s Simple) EffectiveCommand() (string, []string) {
	prog, args, _ := s.effective()
	return prog, args
}

// effective is EffectiveCommand with the problem met peeling a wrapper under a privilege command ("" when there is
// none): the parse reports it, as it reports one met on the outer command.
func (s Simple) effective() (string, []string, string) {
	prog, args := s.Program, s.Args
	for n := 0; n < 8; n++ {
		spec, ok := privSpecs[stdBase(prog)]
		if !ok {
			return prog, args, ""
		}
		rest := skipPriv(spec, args)
		if len(rest) == 0 {
			return "", nil, ""
		}
		sub := Simple{Program: rest[0], Args: append([]string(nil), rest[1:]...)}
		if prob := peel(&sub); prob != "" {
			return sub.Program, sub.Args, prob
		}
		prog, args = sub.Program, sub.Args
	}
	return prog, args, "too many nested privilege commands"
}

// Inline scripts: commands whose argument is itself a shell command line.

const (
	inlineNone = iota
	inlineFound
	inlineMissing
)

var posixShells = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true,
	"ash": true, "mksh": true, "rbash": true,
}

// shellOpts scans the options of a POSIX-style shell. cflag reports -c, sflag
// -s (read commands from stdin), and next is the index of the first operand.
func shellOpts(args []string) (cflag, sflag bool, next int) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--":
			return cflag, sflag, i + 1
		case a == "-o" || a == "+o" || a == "-O" || a == "+O" || a == "--rcfile" || a == "--init-file":
			i += 2
		case strings.HasPrefix(a, "--"):
			i++
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			if a[0] == '-' && strings.Contains(a[1:], "c") {
				cflag = true
			}
			if a[0] == '-' && strings.Contains(a[1:], "s") {
				sflag = true
			}
			i++
		default:
			return cflag, sflag, i
		}
	}
	return cflag, sflag, i
}

// inlineScript returns the command string that prog (already the effective
// program) will execute, for eval, sh -c, su -c and trap.
// InlineScript is the command string a program runs as shell code (sh -c, eval, trap, su -c, mapfile -C), and whether it has one
// or is written as if it had (sh -c with no string).
func InlineScript(prog string, args []string) (string, bool) {
	s, kind := inlineScript(prog, args)
	return s, kind != inlineNone
}

func inlineScript(prog string, args []string) (string, int) {
	base := stdBase(prog)
	switch {
	case posixShells[base]:
		cflag, _, next := shellOpts(args)
		if !cflag {
			return "", inlineNone
		}
		if next >= len(args) {
			return "", inlineMissing
		}
		return args[next], inlineFound
	case base == "eval":
		// eval takes no options but accepts "--" and consumes it: the script is
		// what follows.
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if len(args) == 0 {
			return "", inlineNone
		}
		return strings.Join(args, " "), inlineFound
	case base == "mapfile" || base == "readarray":
		// mapfile -C callback [-c quantum]: the callback is evaluated in this shell every quantum lines, with the index and the
		// line it read appended as words: text from the input, so they are given as "$@", which is not known.
		for i := 0; i < len(args); i++ {
			a := args[i]
			if a == "--" || len(a) < 2 || a[0] != '-' {
				break
			}
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'C':
					cb := a[j+1:]
					if cb == "" {
						if i+1 >= len(args) {
							return "", inlineMissing
						}
						cb = args[i+1]
					}
					return cb + ` 0 "$@"`, inlineFound
				case 'd', 'n', 'O', 's', 'u', 'c': // an option with a value: the rest of the word, or the next one
					if j+1 == len(a) {
						i++
					}
					j = len(a)
				}
			}
		}
		return "", inlineNone
	case base == "trap":
		// trap [-lp] [--] action sigspec...: the action is a command string that
		// runs when the signal (or shell exit) arrives.
		i := 0
		for i < len(args) && len(args[i]) > 1 && args[i][0] == '-' {
			if args[i] == "--" {
				i++
				break
			}
			i++
		}
		if i >= len(args)-1 || args[i] == "-" || args[i] == "" {
			return "", inlineNone // listing, resetting, ignoring: nothing runs
		}
		return args[i], inlineFound
	case base == "su":
		for i, a := range args {
			switch {
			case a == "-c" || a == "--command":
				if i+1 < len(args) {
					return args[i+1], inlineFound
				}
				return "", inlineMissing
			case strings.HasPrefix(a, "--command="):
				return strings.TrimPrefix(a, "--command="), inlineFound
			}
		}
	}
	return "", inlineNone
}

// pipesToShell reports whether any command receives a pipe and would run what
// it reads as code: `curl x | sh`, `... | sudo bash -s`, `... | python -`.
func pipesToShell(cmds []Simple) bool {
	for _, c := range cmds {
		if !c.Piped {
			continue
		}
		prog, args := c.EffectiveCommand()
		if readsCodeFromStdin(prog, args) {
			return true
		}
	}
	return false
}

var interpreterInline = map[string]string{
	// interpreter -> option letters / words that supply code inline or make it
	// print information instead of reading stdin as a program.
	"python": "cm", "perl": "eE", "ruby": "e", "node": "ep", "php": "rf",
	"lua": "e", "Rscript": "e", "deno": "", "bun": "", "tclsh": "",
}

// interpreterValueOpts are, per interpreter, the short options that take a value:
// attached (-Wignore) or, when the letter ends the argument, the next argument
// (-W ignore). Letters inside a value are the value's, not options: python
// -Wc reads its program from stdin, it does not run inline code.
var interpreterValueOpts = map[string]string{
	"python": "WXQ", "perl": "IMmxiCdD0l", "ruby": "IrCEFxKTWi0", "node": "rC", "php": "cdzt",
	"lua": "l", "Rscript": "", "deno": "", "bun": "r", "tclsh": "",
}

// interpreterLongValueOpts are, per interpreter, the long options that take the next argument as their value when it is not
// attached with "=" (node --require ./hook.js): that value is no script operand.
var interpreterLongValueOpts = map[string][]string{
	"python": {"--check-hash-based-pycs"},
	"node": {"--require", "--import", "--loader", "--experimental-loader", "--input-type", "--conditions", "--env-file",
		"--env-file-if-exists", "--title", "--inspect-port", "--debug-port", "--redirect-warnings", "--icu-data-dir",
		"--openssl-config", "--tls-cipher-list", "--max-http-header-size", "--stack-trace-limit", "--watch-path",
		"--experimental-config-file", "--disable-warning", "--secure-heap", "--secure-heap-min", "--heapsnapshot-signal",
		"--diagnostic-dir", "--report-dir", "--report-directory", "--report-filename", "--report-signal", "--test-name-pattern",
		"--test-reporter", "--test-reporter-destination", "--test-skip-pattern", "--test-concurrency", "--test-shard",
		"--test-timeout", "--localstorage-file", "--trace-event-categories", "--trace-event-file-pattern", "--unhandled-rejections",
		"--dns-result-order", "--policy-integrity", "--experimental-policy", "--snapshot-blob", "--cpu-prof-dir", "--cpu-prof-name",
		"--heap-prof-dir", "--heap-prof-name", "--run"},
	"ruby": {"--encoding", "--external-encoding", "--internal-encoding", "--enable", "--disable", "--dump", "--backtrace-limit",
		"--parser", "--crash-report"},
	"bun":  {"--preload", "--require", "--import", "--config", "--cwd", "--env-file", "--tsconfig-override", "--conditions", "--port", "--define", "--loader"},
	"deno": {"--config", "--import-map", "--cert", "--location", "--seed", "--v8-flags", "--ext"},
	"php":  {"--php-ini", "--define", "--zend-extension"},
}

var scriptExts = []string{".py", ".pl", ".rb", ".js", ".mjs", ".cjs", ".ts", ".php", ".lua", ".R", ".tcl"}

// ReadsCodeFromStdin reports whether prog with args runs what arrives on its standard input as a program: a shell with no script
// operand or with -s, an interpreter (python, node, perl, ...) given "-" or no script, xargs that runs a shell. prog is a bare name
// or a binary of a standard directory; any other path is not recognised.
func ReadsCodeFromStdin(prog string, args []string) bool { return readsCodeFromStdin(prog, args) }

// InterpreterName maps the base name of a program to the interpreter it is (python3.11 is "python", nodejs is "node"), or "" when it
// is not one of the interpreters the parser knows.
func InterpreterName(base string) string { return interpreterName(base) }

// readsAsData reports whether the program of s takes its standard input as data of its own, so a heredoc fed to it cannot change
// this shell: a program named in plain words (not "$X") that is no shell reading code (sh, bash -s, source /dev/stdin; an
// interpreter such as python - runs its own language, in its own process) and no builtin that brings the input into the shell
// (read, mapfile, readarray, eval, source, .).
func readsAsData(s Simple) bool {
	prog, args := s.EffectiveCommand()
	base := stdBase(prog)
	if base == "" || strings.ContainsAny(prog, "$`") {
		return false
	}
	switch base {
	case "read", "mapfile", "readarray", "eval", "source", ".", "exec", "builtin", "command", "busybox", "xargs":
		return false
	}
	if posixShells[base] || base == "csh" || base == "tcsh" || base == "fish" {
		return false
	}
	return !readsCodeFromStdin(prog, args) || interpreterName(base) != ""
}

// readsCodeFromStdin is ReadsCodeFromStdin.
func readsCodeFromStdin(prog string, args []string) bool {
	base := stdBase(prog)
	switch {
	case base == "":
		return false
	case posixShells[base] || base == "csh" || base == "tcsh" || base == "fish":
		cflag, sflag, next := shellOpts(args)
		if sflag {
			return true
		}
		if cflag {
			return false
		}
		if next < len(args) && args[next] != "-" && !stdinDevice(args[next]) {
			return false // a script file: stdin is data
		}
		return true
	case base == "busybox":
		return len(args) > 0 && readsCodeFromStdin(args[0], args[1:])
	case base == "xargs":
		return xargsRunsShell(args)
	case base == "source" || base == ".":
		return len(args) > 0 && (stdinDevice(args[0]) || args[0] == "-")
	}
	name := interpreterName(base)
	letters, ok := interpreterInline[name]
	if !ok {
		return false
	}
	values := interpreterValueOpts[name]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-":
			return true
		case a == "--eval" || a == "--print" || a == "--version" || a == "-V" || a == "--help" || a == "-h":
			return false
		case name == "deno" && (a == "eval" || a == "--version"):
			return false
		case strings.HasPrefix(a, "--") && !strings.Contains(a, "="):
			if slices.Contains(interpreterLongValueOpts[name], a) {
				i++ // the value is the next argument, not the script
			}
		case len(a) > 1 && a[0] == '-' && a[1] != '-':
			for j := 1; j < len(a); j++ {
				c := a[j]
				if name == "python" && c == 'i' {
					return true // inspect: after the inline code, module or script, python reads code from stdin
				}
				if letters != "" && strings.IndexByte(letters, c) >= 0 {
					return false
				}
				if strings.IndexByte(values, c) >= 0 {
					if j == len(a)-1 {
						i++ // the value is the next argument
					}
					break
				}
			}
		case len(a) > 0 && a[0] != '-':
			for _, ext := range scriptExts {
				if strings.HasSuffix(a, ext) {
					return false
				}
			}
			return true
		}
	}
	return true
}

// interpreterName maps python3.11, nodejs, perl5.34, ... to the key used in
// interpreterInline, or "" when base is not a known interpreter. Only a
// version suffix (digits and dots) is tolerated, so bunzip2 is not bun.
func interpreterName(base string) string {
	for _, pre := range []string{"python", "nodejs", "node", "perl", "ruby", "php", "lua", "Rscript", "deno", "bun", "tclsh"} {
		rest, ok := strings.CutPrefix(base, pre)
		if !ok {
			continue
		}
		if strings.Trim(rest, "0123456789.") != "" {
			continue
		}
		if pre == "nodejs" {
			return "node"
		}
		return pre
	}
	return ""
}

// xargsLongValueOpts are GNU xargs's long options that take a value, which may be the next word.
var xargsLongValueOpts = []string{"--arg-file", "--delimiter", "--max-args", "--max-procs", "--max-lines", "--max-chars", "--process-slot-var", "--eof"}

// xargsRunsShell reports whether xargs is invoked to run a shell or eval, which
// turns its stdin into code.
func xargsRunsShell(args []string) bool {
	withValue := "nILPsdEa"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			return i < len(args) && xargsUtilityRunsShell(args[i:], 0)
		case strings.HasPrefix(a, "--") && !strings.Contains(a, "=") && slices.Contains(xargsLongValueOpts, a):
			i++ // its value is the next word, not the command
		case strings.HasPrefix(a, "--"):
		case len(a) == 2 && a[0] == '-' && strings.IndexByte(withValue, a[1]) >= 0:
			i++
		case len(a) > 1 && a[0] == '-':
		default:
			return xargsUtilityRunsShell(args[i:], 0)
		}
	}
	return false
}

// xargsUtilityRunsShell reports whether the utility xargs runs (words: the utility and its arguments) is a shell or eval, seen
// through the wrappers unwrap understands and privilege wrappers. A wrapper it cannot read is judged as running one: xargs then
// turns its input into code.
func xargsUtilityRunsShell(words []string, depth int) bool {
	if len(words) == 0 {
		return false
	}
	b := stdBase(words[0])
	switch {
	case posixShells[b] || b == "eval":
		return true
	case depth >= 8: // as peel
		return true
	case b != "" && !plainName(b): // a name xargs fills in from its input (-I{} {} ...): any utility, a shell among them
		return true
	case b == "busybox": // busybox sh, busybox ash: the applet is the next word; without one, xargs's input names it
		return len(words) == 1 || xargsUtilityRunsShell(words[1:], depth+1)
	case isPrivileged(b):
		return xargsUtilityRunsShell(skipPriv(privSpecs[b], words[1:]), depth+1)
	case wrapperSet[b]:
		rest, _, _, prob := unwrap(b, words[1:])
		if prob != "" {
			return true
		}
		return xargsUtilityRunsShell(rest, depth+1)
	}
	return false
}

// plainName reports whether a utility's name is spelled out: letters, digits and . _ + -, the characters of a command's name.
func plainName(b string) bool {
	if b == "" {
		return false
	}
	for _, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._+-", c)) {
			return false
		}
	}
	return true
}

// stdinDevice reports whether a script operand names the standard input.
func stdinDevice(p string) bool {
	return p == "/dev/stdin" || p == "/dev/fd/0" || p == "/proc/self/fd/0"
}
