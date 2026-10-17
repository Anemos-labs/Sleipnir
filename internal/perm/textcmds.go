package perm

import (
	"runtime"
	"strings"
)

// awk and jq are how models pick fields out of text and JSON (`awk '{print $1}'`, `jq .scripts package.json`), and both were off the
// read-only allowlist, so a reviewer or a pool member was refused them and a session with no one to ask could not run them. awk can
// write files (print > "f"), run programs (system, print | "cmd", "cmd" | getline), read any file (getline < f, ARGV rewritten in
// BEGIN) and dump the environment (ENVIRON); jq can read files named in its options and dump the environment (env, $ENV). The
// allowlist takes back the shapes that can do none of that, judged from the program text alone and refusing whatever it cannot
// tell apart.

// awkRefused are words whose presence anywhere in an awk program takes it off the list: system and getline run programs or read
// files, ENVIRON holds the environment, ARGV and ARGC choose the files awk reads, and "@" is gawk's @include, @load and indirect call
// (@f() with f = "system").
var awkRefused = []string{"system", "getline", "ENVIRON", "ARGV", "ARGC", "PROCINFO", "@"}

// analyseAwk allows an awk program that only reads its input files and prints to standard output. The options are -F (the field
// separator), -v (an assignment) and "--"; any other (-f, a program in a file this cannot see; gawk's -i, -l, -o, -p, -d, -E; mawk's
// -W) takes the command off the list. The program must hold none of awkRefused, and, when it prints, no ">" other than ">=" and no
// "|" other than "||": print and printf are the only statements that redirect their output, so a program without them uses ">" and
// "|" only as a comparison and in a regular expression ('$3 > 100', '/a|b/'). The operands are files it reads (an operand of the form
// name=value is an assignment, read as a file all the same, which only judges more).
func analyseAwk(args []string) safeAnalysis {
	var prog string
	haveProg := false
	var files []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case haveProg:
			files = append(files, a)
		case a == "--":
			if i+1 < len(args) {
				prog, haveProg = args[i+1], true
				files = append(files, args[i+2:]...)
			}
			i = len(args)
		case a == "-F" || a == "-v" || a == "--field-separator" || a == "--assign":
			if i+1 >= len(args) {
				return safeAnalysis{why: "awk " + a + " without a value"}
			}
			if a == "-v" || a == "--assign" {
				if why := awkAssignProblem(args[i+1]); why != "" {
					return safeAnalysis{why: why}
				}
			}
			i++
		case strings.HasPrefix(a, "-F") || strings.HasPrefix(a, "--field-separator="):
		case strings.HasPrefix(a, "-v") || strings.HasPrefix(a, "--assign="):
			v := strings.TrimPrefix(strings.TrimPrefix(a, "--assign="), "-v")
			if why := awkAssignProblem(v); why != "" {
				return safeAnalysis{why: why}
			}
		case len(a) > 1 && a[0] == '-':
			return safeAnalysis{why: "awk option " + a + " is not on the read-only list (only -F and -v are)"}
		default:
			prog, haveProg = a, true
		}
	}
	if !haveProg {
		return safeAnalysis{why: "awk without a program"}
	}
	if why := awkProgramProblem(prog); why != "" {
		return safeAnalysis{why: "awk program " + quote(prog) + ": " + why}
	}
	var uses []pathUse
	for _, f := range files {
		if f != "-" {
			uses = append(uses, pathUse{raw: f})
		}
	}
	return safeAnalysis{ok: true, uses: uses}
}

// awkAssignProblem returns why the value of awk -v is refused: an assignment to one of the variables that choose what awk reads.
func awkAssignProblem(v string) string {
	name, _, _ := strings.Cut(v, "=")
	if name == "ARGV" || name == "ARGC" || strings.HasPrefix(name, "ARGV[") {
		return "awk -v " + name + " chooses the files awk reads"
	}
	return ""
}

// awkProgramProblem returns why an awk program does more than read its input and print to standard output, or "".
func awkProgramProblem(p string) string {
	p = strings.NewReplacer("\\\r\n", "", "\\\n", "").Replace(p) // a line continued with a backslash is judged joined, as the most an awk could read it as
	for _, w := range awkRefused {
		if strings.Contains(p, w) {
			return "it uses " + w + ", which can run programs, read other files or print the environment"
		}
	}
	if !strings.Contains(p, "print") {
		return "" // with no print or printf, ">" and "|" redirect nothing
	}
	if strings.Contains(strings.ReplaceAll(p, ">=", ""), ">") {
		return `print with ">" writes a file (a comparison written ">=", or inside a pattern of a program that does not print, is fine)`
	}
	if strings.Contains(strings.ReplaceAll(p, "||", ""), "|") {
		return `print with "|" runs a program`
	}
	return ""
}

// jqValueFlags are the jq options that take values: how many words follow, and which of those words (1-based) is a file jq reads.
var jqValueFlags = map[string]struct{ n, file int }{
	"--arg": {2, 0}, "--argjson": {2, 0}, "--slurpfile": {2, 2}, "--rawfile": {2, 2}, "--indent": {1, 0},
}

// jqSwitches are the jq options that only change how input is read and output written.
var jqSwitches = map[string]bool{
	"--null-input": true, "--raw-input": true, "--slurp": true, "--compact-output": true, "--raw-output": true,
	"--raw-output0": true, "--join-output": true, "--ascii-output": true, "--sort-keys": true, "--color-output": true,
	"--monochrome-output": true, "--tab": true, "--stream": true, "--stream-errors": true, "--seq": true,
	"--exit-status": true, "--binary": true, "--unbuffered": true,
}

// jqShortSwitches are the letters of jq's one-letter switches, which may be clustered (-rc).
const jqShortSwitches = "nRscrjaSCMeb0"

// analyseJq allows jq, which reads JSON and prints it: the operands after the filter are files it reads, and so are the files of
// --slurpfile and --rawfile. Refused: a filter from a file (-f, --from-file), which this cannot see; a module search path (-L) and a
// filter that imports or includes a module or reads its metadata, which read files by name; and a filter that reads the environment (env, $ENV), which
// printenv is refused for. After --args or --jsonargs the operands are values, not files.
func analyseJq(args []string) safeAnalysis {
	var filter string
	haveFilter, valuesOnly := false, false
	var uses []pathUse
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			if a == "--" {
				continue
			}
			switch {
			case !haveFilter:
				filter, haveFilter = a, true
			case !valuesOnly && a != "-":
				uses = append(uses, pathUse{raw: a})
			}
			continue
		}
		if a == "--args" || a == "--jsonargs" {
			valuesOnly = true
			continue
		}
		if jqSwitches[a] {
			continue
		}
		if vf, ok := jqValueFlags[a]; ok {
			if i+vf.n >= len(args) {
				return safeAnalysis{why: "jq " + a + " without its values"}
			}
			if vf.file > 0 {
				uses = append(uses, pathUse{raw: args[i+vf.file]})
			}
			i += vf.n
			continue
		}
		if strings.HasPrefix(a, "--") {
			return safeAnalysis{why: "jq option " + a + " is not on the read-only list"}
		}
		if strings.Trim(a[1:], jqShortSwitches) != "" {
			return safeAnalysis{why: "jq option " + a + " is not on the read-only list (-f reads the filter from a file, -L searches modules)"}
		}
	}
	if !haveFilter {
		return safeAnalysis{why: "jq without a filter"}
	}
	for _, w := range []string{"env", "$ENV", "import", "include", "modulemeta"} {
		if strings.Contains(filter, w) {
			return safeAnalysis{why: "jq filter " + quote(filter) + " uses " + w + ", which reads the environment or other files"}
		}
	}
	return safeAnalysis{ok: true, uses: uses}
}

// analysePs allows ps, which lists processes, except the forms that print each process's environment: BSD-style "e" (ps aux e,
// ps eww) everywhere, and on systems other than Linux also -e and -E (on macOS and the BSDs, ps -e adds the environment; on Linux it
// selects every process, ps -ef). The values of options that take one (-o pid,etime) are not read as letters.
func analysePs(args []string) safeAnalysis {
	sp := &argSpec{short: "CGgOopqstUu", long: []string{"format", "pid", "ppid", "sort", "cols", "columns", "rows", "width",
		"user", "group", "sid", "tty", "Group", "User", "quick-pid"}, noPaths: true}
	s := sp.scan(args)
	words := s.pos
	if runtime.GOOS != "linux" {
		// Elsewhere the option letters differ (macOS ps -g takes no value), so a word read as an option's value may be options; the
		// values of -o, -O and -p (a format and process ids, on every system) are not.
		if s.has("-e") {
			return safeAnalysis{why: "ps -e prints the environment of each process here"}
		}
		for f, vs := range s.flags {
			if f != "-o" && f != "-O" && f != "-p" {
				words = append(words, vs...)
			}
		}
	}
	if s.has("-E") {
		return safeAnalysis{why: "ps -E prints the environment of each process"}
	}
	for _, w := range words {
		if strings.Trim(w, "0123456789,") != "" && strings.ContainsAny(w, "eE") {
			return safeAnalysis{why: "ps " + w + " may print the environment of each process"}
		}
	}
	return safeAnalysis{ok: true}
}

// analyseNpm allows npm ls (list, ll, la: the packages installed in the project) and a bare npm run, which lists the project's
// scripts. Options that point npm at another project or the global packages (--prefix and its -C, -g, --global, --location, a
// configuration file of its own) take it off the list; package names are not files.
func analyseNpm(args []string) safeAnalysis {
	if len(args) == 0 {
		return safeAnalysis{why: unlisted}
	}
	switch sub, rest := args[0], args[1:]; sub {
	case "ls", "list", "ll", "la":
		for _, a := range rest {
			name, _, _ := strings.Cut(a, "=")
			switch {
			case !strings.HasPrefix(a, "-"):
			case inList([]string{"--prefix", "-g", "--global", "--location", "--userconfig", "--globalconfig", "-C"}, name):
				return safeAnalysis{why: "npm " + sub + " " + name + " reads another project's packages"}
			}
		}
		return safeAnalysis{ok: true}
	case "run", "run-script":
		if len(rest) == 0 {
			return safeAnalysis{ok: true}
		}
	}
	return safeAnalysis{why: unlisted}
}

// analyseGitConfig allows git config to read a value: --get, --get-all and --get-regexp, a lone key (git config user.name), and git
// config get KEY. Listing every value (--list, -l, git config list) is allowed with --local or --worktree only: the user's and the
// system's configuration lie outside the workspace and may hold credentials (an http.extraHeader, a url with a token). Any other
// option, a scope other than the repository's (--global, --system, --file, --blob), or a key with a value (which sets it) takes the
// command off the list.
func analyseGitConfig(rest []string) safeAnalysis {
	var pos []string
	get, list, local := false, false, false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		name, _, hasVal := strings.Cut(a, "=")
		switch {
		case a == "--":
			pos = append(pos, rest[i+1:]...)
			i = len(rest)
		case !strings.HasPrefix(a, "-"):
			pos = append(pos, a)
		case a == "--get" || a == "--get-all" || a == "--get-regexp":
			get = true
		case a == "--list" || a == "-l":
			list = true
		case a == "--local" || a == "--worktree":
			local = true
		case inList([]string{"--show-origin", "--show-scope", "--name-only", "--null", "-z", "--bool", "--int", "--bool-or-int",
			"--path", "--expiry-date", "--no-includes", "--all"}, a):
		case name == "--type" || name == "--default":
			if !hasVal {
				i++
			}
		default:
			return safeAnalysis{why: "git config " + a + " is not on the read-only list"}
		}
	}
	if len(pos) > 0 && !get && !list && (pos[0] == "get" || pos[0] == "list") {
		get, list, pos = pos[0] == "get", pos[0] == "list", pos[1:]
	}
	switch {
	case list && !get:
		if len(pos) > 0 {
			return safeAnalysis{why: "git config --list with a name"}
		}
		if !local {
			return safeAnalysis{why: "git config --list reads the user's configuration too, which may hold credentials: add --local"}
		}
	case get && !list:
		if len(pos) == 0 || len(pos) > 2 {
			return safeAnalysis{why: "git config --get without a name"}
		}
	case !get && !list && len(pos) == 1:
	default:
		return safeAnalysis{why: "git config with a value sets it"}
	}
	return safeAnalysis{ok: true}
}
