package perm

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// Bounds on hostile input: simple commands judged per line, and file operands
// resolved per command or tool request.
const (
	maxUnits    = 2000
	maxOperands = 4000
)

// cmdClass says where a simple command sits relative to the allowlists.
type cmdClass int

const (
	cmdUnknown cmdClass = iota // not on any list: needs approval
	cmdSafe                    // read-only allowlist
	cmdFSWrite                 // edits files (accept-edits may allow it inside the workspace)
	cmdNoop                    // only assignments and/or redirections
)

// riskInfo describes why a command is flagged high risk. An allow rule can
// override the flag only if it is not a blanket rule and either matches the
// command exactly or names one of the markers (a rule for "git push:*" does not
// cover a force push; "git push --force:*" does).
type riskInfo struct {
	why     string
	markers []string
}

// unit is one thing to judge: a simple command from a shell line, or a whole
// non-shell tool request.
type unit struct {
	label    string
	simple   *shellparse.Simple
	name     string // program name for allowlists ("" for ./script or /tmp/x/prog)
	class    cmdClass
	why      string // when unlisted: why the allowlist did not take it
	accesses []access
	redirs   []access
	high     *riskInfo
	dyn      string   // reason the command word itself cannot be judged
	envBad   string   // env assignment that changes what the command runs
	envAcc   []access // the files the values of its environment assignments name (read)
	network  bool
	venv     *venvUse // a command that creates or installs into a Python virtual environment (venv.go)

	tool bool // a non-shell tool request
	req  *Request

	rem     []Rule // cached remember rules
	remDone bool
}

// evaluator judges one request against one view (mode and rule set).
type evaluator struct {
	e          *Engine
	v          *view
	rs         *resolver
	r          Request
	globBudget int
	unroutine  bool // a part of the request is not routine (routine.go)
	// vars are the shell variables an earlier statement of the same command line set to a known value (noteAssignments), and
	// exported those of them an export (or declare -x) statement gave to the commands the line starts.
	vars     map[string]string
	exported map[string]bool
	// fallible are the known variables whose value came from a command's output (expansion.fallible)
	fallible map[string]bool
	// off are the variables the engine otherwise knows ($HOME, $TMPDIR, $PWD, the special parameters) that the line assigns or
	// otherwise names (expansion.off).
	off map[string]bool
	// gitEnv: the line names a GIT_ variable, so $(git rev-parse --show-toplevel) is not taken as the repository around the directory.
	gitEnv bool
	// index is set when a unit of the request installs packages into a virtual environment of the workspace (venv.go): the one
	// routine question that reaches beyond this machine, to the package index.
	index bool
	// cleanEnv is set when a command of the line runs under env, which can start a new shell without the exported variables.
	cleanEnv bool
}

// evaluate runs bounded permission analysis and escalates implicitly allowed high-risk actions to
// ask or plan-mode denial unless in yolo mode.
func (e *Engine) evaluate(v *view, r Request) verdict {
	ev := &evaluator{e: e, v: v, rs: e.rs, r: r, globBudget: 20000}
	res := ev.run()
	if r.Risk == RiskHigh && res.kind == vAllow && !res.explicit && v.mode != ModeYolo {
		if v.mode == ModePlan {
			return deny(planReason("the tool marked this action high risk"))
		}
		return ask("the tool marked this action high risk", nil).protected(TierGuarded)
	}
	res.routine = res.kind == vAsk && !ev.unroutine && !res.askRule && res.tier == "" && r.Risk != RiskHigh
	res.index = ev.index
	return res
}

// run evaluates an extracted shell command when available and otherwise applies ordinary tool
// permission rules.
func (ev *evaluator) run() verdict {
	if cmd, ok := ev.command(); ok {
		return ev.bash(cmd)
	}
	return ev.tool()
}

// isBashTool reports whether the normalized tool name belongs to the shell-command class.
func isBashTool(tool string) bool { return classOf(tool) == classBash }

// command returns the shell command line of the request, if it is one.
func (ev *evaluator) command() (string, bool) {
	r := ev.r
	if r.Command != "" {
		return r.Command, true
	}
	if !isBashTool(r.Tool) {
		return "", false
	}
	var in struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"`
	}
	if json.Unmarshal(r.Input, &in) == nil {
		if in.Command != "" {
			return in.Command, true
		}
		if in.Cmd != "" {
			return in.Cmd, true
		}
	}
	return "", true // a shell tool with nothing to run
}

// startDir resolves the request working directory against the permission root, falling back to the
// root or current directory.
func (ev *evaluator) startDir() string {
	if c := ev.r.Cwd; c != "" {
		if filepath.IsAbs(c) {
			return cleanPath(c)
		}
		return cleanAbs(filepath.Join(ev.rs.root().lex, c))
	}
	if r := ev.rs.root().lex; r != "" {
		return r
	}
	return cleanAbs(".")
}

// bash judges a shell command line: every simple command in it must be allowed
// for the whole to be, any denied one denies it, and anything the parser cannot
// see through is never auto-allowed.
func (ev *evaluator) bash(cmd string) verdict {
	if isForkBomb(cmd) {
		return deny("built-in protection: fork bomb").protected(TierHard)
	}
	an := shellparse.Parse(cmd)
	an.Commands = bindLoopVars(cmd, an.Commands)
	// What each loop assigns is taken from all of its commands, those past the bound below included. An arithmetic expansion
	// assigns, as the command's words are expanded, only the names it reaches (arithAssigns).
	ints := intVars(an.Commands)
	loopNames, loopAll := loopAssignments(an.Commands, ints)
	// Judging costs file-system lookups per command; a line with thousands of
	// commands is checked up to a bound and then never auto-allowed, in any mode.
	tooMany := len(an.Commands) > maxUnits
	if tooMany {
		an.Commands = an.Commands[:maxUnits]
	}
	cw := &cwdSet{dirs: []string{ev.startDir()}}
	res := verdict{kind: vAllow, explicit: true, reason: "empty command"}
	first := true
	// fold adds one command's verdict; it reports a denial, which nothing can outrank.
	fold := func(v verdict) bool {
		if v.kind == vDeny {
			res = v
			return true
		}
		if first {
			res, first = v, false
		} else {
			res = combine(res, v)
		}
		return false
	}
	// The scans of the line's text for what may change its variables read the shell's code: the body of a heredoc that is a
	// program's data (python3 - <<'PY' ... os.environ["TMPDIR"] ... PY) is blank there (shellparse.Analysis.Literal).
	code := an.Code(cmd)
	ev.off = harnessOff(code, an.Commands)
	ev.gitEnv = strings.Contains(code, "GIT_")
	ev.cleanEnv = false
	for _, c := range an.Commands {
		ev.cleanEnv = ev.cleanEnv || slices.Contains(c.Wrappers, "env")
	}
	conflicted := map[string]bool{}
	track := varsTrackable(code, an)
	// The commands of a trap action run when the signal arrives or the shell exits: they are judged after the rest, in every directory
	// the line may be in by then, and with only the variables that were known when the trap was set and still are at the end.
	type deferred struct {
		s    shellparse.Simple
		vars map[string]string
	}
	var later []deferred
	// A loop runs its commands again: what any of them assigns may already be there when the first one runs, so the names a loop
	// assigns are unknown from its start (all of them when one of its builtins may set any).
	entered := map[int]bool{}
	for i, s := range an.Commands {
		if s.Deferred {
			later = append(later, deferred{s: s, vars: maps.Clone(ev.vars)})
			continue
		}
		if track && s.Loop != 0 && !entered[s.Loop] {
			entered[s.Loop] = true
			if loopAll[s.Loop] {
				ev.vars, ev.exported, track = nil, nil, false
			}
			for _, n := range loopNames[s.Loop] {
				ev.forget(n, conflicted)
			}
		}
		if track && len(s.Arith) > 0 {
			if names, all := arithAssigns(s.Arith, i, ints); all {
				ev.vars, ev.exported, track = nil, nil, false // an expansion may set any variable from here on
			} else {
				for _, n := range names {
					ev.forget(n, conflicted)
				}
			}
		}
		v := ev.judgeWith(s, cw, ev.vars)
		if track {
			if reassigns(s) {
				if names, all := reassignedNames(s); all {
					ev.vars, ev.exported, track = nil, nil, false // a builtin may set any variable from here on
				} else {
					for _, n := range names {
						ev.forget(n, conflicted)
					}
				}
			} else if unconditional(s) {
				ev.noteAssignments(s, cw, conflicted)
			} else {
				for _, env := range assignedBy(s) { // an assignment that may not run leaves the name unknown
					ev.forget(envName(env), conflicted)
				}
			}
		}
		if fold(v) {
			return res
		}
	}
	for _, d := range later {
		var vars map[string]string
		if track {
			vars = stableVars(d.vars, ev.vars)
		}
		if fold(ev.judgeWith(d.s, cw, vars)) {
			return res
		}
	}
	why := opaqueReason(cmd, an)
	if why == "" && tooMany {
		why = fmt.Sprintf("it has more than %d commands, too many to check", maxUnits)
	}
	if why != "" {
		ev.unroutine = true
	}
	if why == "" || res.kind == vDeny {
		return res
	}
	if free(ev.v.mode) {
		if !tooMany {
			return res
		}
		// The commands past the bound were not judged at all, so a mode that allows what it is not asked about cannot allow them:
		// bypass asks, and yolo, which never asks, refuses.
		if ev.v.mode == ModeYolo {
			return deny("cannot be checked beforehand: " + why + "; split it into shorter commands")
		}
		return ask("cannot be checked beforehand: "+why, nil).protected(TierGuarded)
	}
	if ev.v.mode == ModePlan {
		return deny(planReason("cannot verify that the command is read-only: " + why))
	}
	if res.kind == vAllow {
		return ask("cannot be checked beforehand: "+why, nil)
	}
	res.rem = nil // remembering one command would not stop the construct asking again
	res.reason = "cannot be checked beforehand: " + why + "; " + res.reason
	return res
}

// judgeWith judges one command with vars as the variables the line has set when it runs. A command that a new shell runs (sh -c,
// a script on a shell's standard input) sees only those of them the line exported.
func (ev *evaluator) judgeWith(s shellparse.Simple, cw *cwdSet, vars map[string]string) verdict {
	saved := ev.vars
	ev.vars = vars
	if s.Child {
		ev.vars = nil
		for n, v := range vars {
			if ev.exported[n] && !ev.cleanEnv { // env -i, env -u: the new shell may not get them
				if ev.vars == nil {
					ev.vars = map[string]string{}
				}
				ev.vars[n] = v
			}
		}
	}
	defer func() { ev.vars = saved }()
	return ev.judge(ev.buildUnit(s, cw))
}

// stableVars are the variables of at (what was known where a trap was set) that end holds with the same value: whenever the trap
// runs after that, they hold that value.
func stableVars(at, end map[string]string) map[string]string {
	var out map[string]string
	for n, v := range at {
		if w, ok := end[n]; ok && w == v {
			if out == nil {
				out = map[string]string{}
			}
			out[n] = v
		}
	}
	return out
}

// forget makes a variable unknown for the rest of the line.
func (ev *evaluator) forget(name string, conflicted map[string]bool) {
	delete(ev.vars, name)
	delete(ev.exported, name)
	delete(ev.fallible, name)
	conflicted[name] = true
}

// noteAssignments records the variables a statement made only of assignments (SCR="$TMPDIR/x"), or a declaration of the form
// export, readonly or declare NAME=value, sets to a value the engine can expand, for the statements after it: "$SCR" then names
// that path. A name assigned two different values on the line, a value that cannot be expanded or is empty (${SCR:=/etc} would
// replace it), a name that steers programs (envDangerous) and a variable of the shell's own (untrackedVars) is not known; conflicted
// remembers the names that lost their value. An assignment before a program is that program's environment, not a shell variable.
func (ev *evaluator) noteAssignments(s shellparse.Simple, cw *cwdSet, conflicted map[string]bool) {
	assigns, exported := s.Env, false
	if s.Program != "" {
		d, ok := declaration(s)
		if !ok {
			return
		}
		assigns, exported = d.assigns, d.exported
		for _, n := range d.names {
			if exported && ev.vars[n] != "" {
				ev.markExported(n)
			}
		}
	}
	// The values of a declaration are expanded before it assigns any of them (export a=1 b=$a gives b the old a); the values of an
	// assignment statement one after the other.
	before := ev.vars
	if s.Program != "" {
		before = maps.Clone(ev.vars)
	}
	for _, env := range assigns {
		name, val, ok := strings.Cut(env, "=")
		if !ok || name == "" {
			continue
		}
		if strings.HasSuffix(name, "+") || envDangerous(env) || untrackedVars[name] || strings.HasPrefix(name, "BASH") {
			ev.forget(strings.TrimSuffix(name, "+"), conflicted)
			continue
		}
		// Do not expand shell-literal or mixed-quoted values as variables.
		literal := strings.ContainsAny(s.Raw, "'\\")
		val = unquoteAssigned(val)
		if inner, ok := soleSubstitution(val); ok && tokenSubstitution(inner) && !conflicted[name] {
			// ts="$(date -u '+%Y%m%dT%H%M%SZ')": one name token, whatever quotes the command has, and nothing when it fails
			if prev, had := ev.vars[name]; had && prev != tokenPlaceholder(inner) {
				ev.forget(name, conflicted)
				continue
			}
			if ev.vars == nil {
				ev.vars = map[string]string{}
			}
			ev.vars[name] = tokenPlaceholder(inner)
			if exported {
				ev.markExported(name)
			}
			if ev.fallible == nil {
				ev.fallible = map[string]bool{}
			}
			ev.fallible[name] = true
			if s.Program == "" {
				before = ev.vars
			}
			continue
		}
		// The directory matters only to a value that names it ($PWD, ~+, $(pwd)), which cannot be expanded without one: any other is
		// the same wherever the line is, so a cd to a place only known when the line runs (cd "$(git rev-parse ... || pwd)") does not
		// make "$TMPDIR/x" unknown. A relative value stays text, resolved where it is used.
		cwd := ""
		if len(cw.dirs) == 1 && !cw.unknown {
			cwd = cw.dirs[0]
		}
		var w string
		dyn := literal
		if !dyn {
			w, _, dyn = ev.rs.expandWordVars(val, cwd, expansion{vars: before, off: ev.off, gitEnv: ev.gitEnv})
		}
		prev, had := ev.vars[name]
		switch {
		case dyn || w == "" || conflicted[name] || had && prev != w:
			ev.forget(name, conflicted)
		default:
			if ev.vars == nil {
				ev.vars = map[string]string{}
			}
			ev.vars[name] = w
			if exported {
				ev.markExported(name)
			}
			// a value from a command's output is empty when the command fails
			if e, _, edyn := ev.rs.expandWordVars(val, cwd, expansion{vars: before, off: ev.off, gitEnv: ev.gitEnv, fallible: ev.fallible, emptied: true}); !edyn && e != w {
				if ev.fallible == nil {
					ev.fallible = map[string]bool{}
				}
				ev.fallible[name] = true
			} else {
				delete(ev.fallible, name)
			}
		}
		if s.Program == "" {
			before = ev.vars
		}
	}
}

// soleSubstitution returns the command of a value that is exactly one command substitution, $(cmd), and whether it is one.
func soleSubstitution(v string) (string, bool) {
	if !strings.HasPrefix(v, "$(") || strings.HasPrefix(v, "$((") || !strings.HasSuffix(v, ")") {
		return "", false
	}
	inner := v[2 : len(v)-1]
	if strings.ContainsAny(inner, "()`") {
		return "", false // a nested substitution or a parenthesis: not one plain command
	}
	return inner, true
}

// markExported records that a known variable is in the environment of the commands the line starts.
func (ev *evaluator) markExported(name string) {
	if ev.exported == nil {
		ev.exported = map[string]bool{}
	}
	ev.exported[name] = true
}

// untrackedVars are variables of the shell itself: what a read of them gives is not what was assigned (RANDOM, SECONDS, LINENO), an
// assignment to them fails (UID, PPID) or a builtin changes them (PWD and OLDPWD by cd, OPTIND by getopts, REPLY by read, _ by every
// command). harnessNames are not tracked either.
var untrackedVars = map[string]bool{
	"_": true, "PWD": true, "OLDPWD": true, "HOME": true, "TMPDIR": true, "PPID": true, "UID": true, "EUID": true, "GROUPS": true,
	"RANDOM": true, "SRANDOM": true, "SECONDS": true, "LINENO": true, "EPOCHSECONDS": true, "EPOCHREALTIME": true, "FUNCNAME": true,
	"HISTCMD": true, "DIRSTACK": true, "PIPESTATUS": true, "OPTIND": true, "OPTARG": true, "REPLY": true, "SHELLOPTS": true, "IFS": true,
}

// assignedBy lists the NAME=value assignments a statement makes to shell variables: those of an assignment statement, or of a
// declaration (export, readonly, declare NAME=value).
func assignedBy(s shellparse.Simple) []string {
	if s.Program == "" {
		return s.Env
	}
	if d, ok := declaration(s); ok {
		return d.assigns
	}
	return nil
}

// decl is a declaration builtin written as plain assignments: export a=1 b, readonly a=1, declare -x a=1.
type decl struct {
	assigns  []string // NAME=value operands, in order
	names    []string // NAME operands, which keep their value
	exported bool     // export, or declare/typeset -x: the values reach the commands the line starts
}

// declaration reads export, readonly, declare and typeset whose operands are all NAME=value or NAME, with no option (declare and
// typeset may have -x and -r). Anything else (export -f, declare -n, declare -i, an array, local outside a function) is not one, and
// keeps its own analysis. A declaration with no operand lists variables, and is not one either.
func declaration(s shellparse.Simple) (decl, bool) {
	prog := shellparse.ProgramName(s.Program)
	var d decl
	switch prog {
	case "export":
		d.exported = true
	case "readonly", "declare", "typeset":
	default:
		return d, false
	}
	for _, a := range s.Args {
		switch {
		case strings.HasPrefix(a, "-"):
			flags := a[1:]
			if prog == "export" || prog == "readonly" || flags == "" || strings.Trim(flags, "xr") != "" {
				return decl{}, false
			}
			d.exported = d.exported || strings.Contains(flags, "x")
		case strings.Contains(a, "="):
			name, _, _ := strings.Cut(a, "=")
			if !validName(name) {
				return decl{}, false
			}
			d.assigns = append(d.assigns, a)
		case validName(a):
			d.names = append(d.names, a)
		default:
			return decl{}, false
		}
	}
	return d, len(d.assigns)+len(d.names) > 0
}

// validName reports whether s is a shell variable name.
func validName(s string) bool {
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isNameByte(s[i]) {
			return false
		}
	}
	return true
}

// varsTrackable reports whether a command line is simple enough for noteAssignments, from what the parser found: the line parsed (a
// function definition, whose body may set variables when it is called, and an arithmetic command leave it unparsed); each of its
// arithmetic expansions ($((n=5))) is on the command it belongs to (shellparse.Analysis.ArithKnown; arithAssigns says what it may
// assign); it has no other place bash evaluates arithmetic (mayAssignArithmetically); and no trap action assigns, since it may run
// between any two commands. Quoted text and here-document bodies are the parser's business: printf 'func main() {...}' > main.go
// defines nothing. cmd is the line's code (shellparse.Analysis.Code).
func varsTrackable(cmd string, an shellparse.Analysis) bool {
	if !an.Parsed || an.HasArithmetic && !an.ArithKnown || mayAssignArithmetically(cmd) {
		return false
	}
	for _, s := range an.Commands {
		if s.Deferred && (reassigns(s) || len(assignedBy(s)) > 0 || len(s.Arith) > 0) {
			return false
		}
	}
	return true
}

// arithmeticContexts are the other places bash evaluates arithmetic, where an expression may assign (d=5): the operands of [[ ... -eq
// ... ]] and its kin, an array subscript in an assignment or an expansion (a[d=5]=1, ${a[d=5]}), a substring offset (${x:d=5}), and
// the legacy $[...]. Matched on the raw text, so a quoted look-alike also counts: that only makes the line's variables unknown.
var arithmeticContexts = []*regexp.Regexp{
	regexp.MustCompile(`\$\[`),
	regexp.MustCompile(`\[\[[^\]]*\s-(eq|ne|lt|le|gt|ge)\s`),
	regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*\[[^\]]*\]\+?=`),
	regexp.MustCompile(`\$\{[^}]*\[`),
	regexp.MustCompile(`\$\{[^}:]*:[^-=?+}]`),
}

// mayAssignArithmetically reports whether cmd holds one of the arithmeticContexts.
func mayAssignArithmetically(cmd string) bool {
	for _, re := range arithmeticContexts {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// reassignBuiltins are the commands that may set or unset a shell variable without an assignment statement (printf with -v:
// printfAssigns).
var reassignBuiltins = map[string]bool{
	"read": true, "declare": true, "typeset": true, "local": true, "export": true, "readonly": true, "unset": true,
	"mapfile": true, "readarray": true, "getopts": true, "let": true, "eval": true, "source": true, ".": true,
	"for": true, "select": true, "wait": true, "coproc": true,
}

// reassigns reports whether s may set a variable other than by a plain assignment statement or a declaration (declaration).
func reassigns(s shellparse.Simple) bool {
	if _, ok := declaration(s); ok {
		return false
	}
	name := shellparse.ProgramName(s.Program)
	if name == "printf" {
		return printfAssigns(s.Args)
	}
	return s.LoopVar != "" || reassignBuiltins[name]
}

// reassignedNames are the variables a statement of reassigns may set: a loop's variable (and REPLY, which select sets), the names
// a builtin is given (read, unset, getopts, mapfile, wait -p, printf -v; and the variables they set when given none), and nothing
// for a plain wait. all is true when they are not known from its words: eval, source, let (an arithmetic expression may set any
// variable it reaches), a declaration that is not plain assignments, coproc, and a name only known when the line runs.
func reassignedNames(s shellparse.Simple) (names []string, all bool) {
	if s.LoopVar != "" {
		return []string{s.LoopVar, "REPLY"}, false
	}
	name := shellparse.ProgramName(s.Program)
	switch name {
	case "read", "unset", "getopts", "mapfile", "readarray", "wait", "printf":
	default:
		return nil, true
	}
	ops, opsAll := assignedNames(s)
	if opsAll {
		return nil, true // mapfile -C: the callback runs text in this shell
	}
	if name == "printf" { // printf -v NAME or printf -vNAME
		ops = nil
		if a := s.Args; len(a) > 0 {
			if t := strings.TrimPrefix(a[0], "-v"); t != a[0] && t != "" {
				ops = []string{t}
			} else if a[0] == "-v" && len(a) > 1 {
				ops = []string{a[1]}
			} else {
				return nil, true // a first word only known when the line runs (printfAssigns)
			}
		}
	}
	for _, a := range ops {
		if strings.ContainsAny(a, "$`*?[") { // a word only known when the line runs, or a glob a file name may turn into another name
			return nil, true
		}
		if n, _, _ := strings.Cut(a, "["); validName(n) {
			names = append(names, n)
		}
	}
	switch name {
	case "read":
		names = append(names, "REPLY")
	case "mapfile", "readarray":
		names = append(names, "MAPFILE")
	case "getopts":
		names = append(names, "OPTARG", "OPTIND")
	}
	return names, false
}

// loopAssignments are the names the commands of each loop (shellparse.Simple.Loop) may set, and the loops one of whose builtins
// may set any variable.
func loopAssignments(cmds []shellparse.Simple, ints map[string]int) (map[int][]string, map[int]bool) {
	names, all := map[int][]string{}, map[int]bool{}
	for i, s := range cmds {
		if s.Loop == 0 || s.Deferred {
			continue
		}
		if len(s.Arith) > 0 {
			n, a := arithAssigns(s.Arith, i, ints)
			names[s.Loop] = append(names[s.Loop], n...)
			all[s.Loop] = all[s.Loop] || a
		}
		for _, env := range assignedBy(s) {
			names[s.Loop] = append(names[s.Loop], envName(env))
		}
		if d, ok := declaration(s); ok {
			names[s.Loop] = append(names[s.Loop], d.names...)
		}
		if reassigns(s) {
			n, a := reassignedNames(s)
			names[s.Loop] = append(names[s.Loop], n...)
			all[s.Loop] = all[s.Loop] || a
		}
	}
	return names, all
}

// printfAssigns reports whether a printf may set a variable: printf -v NAME does, and so may one whose first word is only known when
// the line runs (a variable, a substitution, a glob). printf 'date\n' > "$F" sets nothing.
func printfAssigns(args []string) bool {
	return len(args) > 0 && (strings.HasPrefix(args[0], "-v") || strings.ContainsAny(args[0], "$`*?["))
}

// unconditional reports whether the statement s certainly runs when the line runs, in the line's own shell: the parser did not mark
// it Cond (inside a compound command, a group or a subshell, after && or ||, in a pipeline, in the background) and it is not inside
// a substitution.
func unconditional(s shellparse.Simple) bool {
	return !s.Nested && !s.Piped && !s.Cond && s.Raw != ""
}

// unquoteAssigned removes the double quotes around an assigned value ("$TMPDIR/x"); a single-quoted value is literal and has no
// variable to expand, so its quotes are removed as well.
func unquoteAssigned(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

// opaqueReason names the constructs whose effect cannot be judged from the text.
func opaqueReason(cmd string, an shellparse.Analysis) string {
	switch {
	case !an.Parsed:
		return "the command could not be parsed reliably (" + an.Problem + "); split it into simpler commands"
	case an.HasCommandSubstitution:
		return "it contains a command substitution, whose output is only known when it runs; run the inner command on its own first"
	case an.HasProcessSubstitution:
		return "it contains a process substitution; write the data to a file first"
	case an.PipesToShell:
		return "it pipes data into a shell or interpreter, which runs whatever arrives"
	case evaluatesQuotedSubstitution(cmd, an):
		return "quoted text that looks like a command substitution could be run by bash's own expression evaluation"
	}
	return ""
}

// evalBuiltins are bash builtins that evaluate some of their arguments as
// expressions. Array subscripts in expressions are expanded, so
// declare 'a[$(cmd)]=1' or read 'a[$(cmd)]' runs cmd although the substitution
// is inside single quotes.
var evalBuiltins = map[string]bool{
	"declare": true, "typeset": true, "local": true, "export": true, "readonly": true, "unset": true,
	"read": true, "printf": true, "let": true, "test": true, "[": true, "[[": true, "mapfile": true,
	"readarray": true, "wait": true, "set": true,
}

// arithmeticMarkers show that a command line evaluates variable contents as
// arithmetic, which runs command substitutions hidden in array subscripts:
// x='a[$(cmd)]'; echo $((x)) runs cmd.
var arithmeticMarkers = []string{"((", "[[", "let ", "declare -i", "typeset -i", " -eq ", " -ne ", " -lt ", " -gt ", " -le ", " -ge "}

// looksLikeSubstitution reports text such as $(cmd) or `cmd`; an arithmetic
// expansion $((1+1)) is not one.
func looksLikeSubstitution(v string) bool {
	if strings.Contains(v, "`") {
		return true
	}
	for i := 0; i+1 < len(v); i++ {
		if v[i] == '$' && v[i+1] == '(' && (i+2 >= len(v) || v[i+2] != '(') {
			return true
		}
	}
	return false
}

// evaluatesQuotedSubstitution reports whether text that looks like a command
// substitution (but did not count as one because it was quoted) sits where bash
// could evaluate it: as an argument of an evaluating builtin, or anywhere in a
// line that also evaluates variables as arithmetic.
func evaluatesQuotedSubstitution(cmd string, an shellparse.Analysis) bool {
	arith := false
	for _, m := range arithmeticMarkers {
		arith = arith || strings.Contains(cmd, m)
	}
	for _, c := range an.Commands {
		texts := append(append([]string(nil), c.Args...), c.Env...)
		for _, v := range texts {
			if !looksLikeSubstitution(v) {
				continue
			}
			if arith || evalBuiltins[shellparse.ProgramName(c.Program)] {
				return true
			}
		}
	}
	return false
}

// label builds a bounded single-line command label, reconstructing parsed words when raw command
// text is unavailable.
func label(s shellparse.Simple) string {
	l := strings.TrimSpace(s.Raw)
	if l == "" {
		l = shellparse.Join(append([]string{s.Program}, s.Args...))
	}
	l = strings.Join(strings.Fields(l), " ")
	if len(l) > 80 {
		l = l[:77] + "..."
	}
	return l
}

func (ev *evaluator) buildUnit(s shellparse.Simple, cw *cwdSet) *unit {
	sp := s
	u := &unit{simple: &sp, label: label(s), name: shellparse.ProgramName(s.Program)}

	assign := func(env string) {
		// An assignment of a plain value (PORT=8080 ./bin/app) changes what a program reads, not which program runs or what it loads:
		// only the names that do (PATH, LD_PRELOAD, GIT_*, a proxy, see envDangerous) keep the command from any allow rule. A value
		// that names a file is judged as a read of that file, whether or not a rule allows the command (envAcc).
		if envDangerous(env) && u.envBad == "" {
			u.envBad = envName(env)
		}
		if _, val, ok := strings.Cut(env, "="); ok {
			for _, c := range pathCandidates(val) {
				for _, a := range ev.resolve(pathUse{raw: c}, cw) {
					a.guess = true
					u.envAcc = append(u.envAcc, a)
				}
			}
		}
	}
	for _, env := range s.Env {
		assign(env)
	}
	u.redirs = ev.redirAccesses(s.Redirects, cw)
	if s.Program == "" {
		u.class = cmdNoop
		return u
	}
	if d, ok := declaration(s); ok && !dynamicProgram(s.Program) {
		// export a=1, readonly a=1, declare -x a=1: assignments, judged as an assignment statement is (an exported PATH or LD_PRELOAD
		// steers the commands after it, and asks).
		for _, a := range d.assigns {
			assign(a)
		}
		u.class = cmdNoop
		return u
	}
	if dynamicProgram(s.Program) || strings.HasPrefix(s.Program, "~") {
		u.dyn = "the command name is built from a variable, glob or substitution; write the program's name out"
	}
	// A program given by its path (.venv/bin/pip, /usr/bin/curl, ./node_modules/.bin/x) is what its name says for the questions of
	// what it reaches: an install, the network.
	u.network = isNetwork(programBase(s.Program), s.Args)
	if u.dyn == "" {
		u.venv = ev.venvCommand(s, cw)
		ev.index = ev.index || u.venv.reachesIndex()
	}

	var uses []pathUse
	chdir := ""
	if u.dyn == "" && u.name != "" {
		if sa := analyseSafe(u.name, s.Args); sa.ok {
			u.class, uses, chdir = cmdSafe, sa.uses, sa.chdir
		} else {
			u.why = sa.why
			if fs, ok := analyseFSWrite(u.name, s.Args); ok {
				u.class, uses = cmdFSWrite, fs
			}
		}
	}
	if u.class == cmdUnknown {
		if u.why == "" {
			u.why = unlisted
		}
		uses = unknownUses(withoutInlineCode(u.name, s.Args))
		if u.name == "git" {
			if g, ok := gitRepoUses(s.Args); ok {
				uses = g // writes only into the repository: its operands are read
			}
		}
		if tr, ok := testRunnerUses(u.name, s.Args); ok {
			uses = tr // a test runner reads the tests below its operands (testrunners.go)
		}
		if u.name == "curl" {
			if cu, ok := curlUses(s.Args, func(w string) ([]string, bool) { return ev.knownWords(w, cw) }); ok {
				uses = cu // URLs and data are not files; the files curl touches are named (curluses.go)
			}
		}
		if formatter(u.name, s.Args) || checker(u.name, s.Args) {
			for i := range uses {
				uses[i].format = true
			}
		}
		if u.name == "" {
			// ./tool or /opt/x/tool: running a file reads it; it is not a write.
			uses = append([]pathUse{{raw: s.Program}}, uses...)
		}
		if u.venv != nil && u.venv.uses != nil {
			uses = u.venv.uses // what a virtual environment's command reads and writes is known (venv.go)
		}
	}
	if u.name == "go" && u.dyn == "" {
		if goUses, ok := goPathUses(s.Args); ok {
			uses = goUses
			if hasLong(s.Args[1:], "-overlay", "--overlay") {
				// The JSON can redirect source reads outside the visible operands.
				// This gate precedes command allow rules, including test presets.
				u.dyn = "go -overlay replacement paths cannot be checked from the command; approval is required"
			}
		}
	}
	if len(uses) > maxOperands {
		// Beyond this the operands are not looked at one by one; the command is
		// simply never auto-allowed. Protections still see the first ones.
		uses = uses[:maxOperands]
		if u.dyn == "" {
			u.dyn = fmt.Sprintf("it has more than %d operands, too many to check", maxOperands)
		}
	}
	for i, use := range uses {
		c := cw
		if chdir != "" && i > 0 {
			c = ev.cdTo(cw, chdir)
		}
		u.accesses = append(u.accesses, ev.resolve(use, c)...)
	}
	if u.name == "for" {
		// A loop's word list is data as often as files ("for i in $ITEMS"); the
		// body's own uses of the variable are judged where they occur. Keep
		// only what the text already gives away.
		kept := u.accesses[:0]
		for _, a := range u.accesses {
			if !a.dynamic || a.prefix || dynamicSuspect(a.raw, false) != "" {
				kept = append(kept, a)
			}
		}
		u.accesses = kept
	}
	u.high = ev.highRisk(u)
	ev.trackCwd(u, cw)
	return u
}

// trackCwd folds a cd into the set of possible working directories.
func (ev *evaluator) trackCwd(u *unit, cw *cwdSet) {
	switch u.name {
	case "cd":
		target := "~"
		var operand []string
		for _, a := range u.simple.Args {
			if a != "--" && a != "-P" && a != "-L" && a != "-e" && a != "-@" {
				operand = append(operand, a)
			}
		}
		if len(operand) == 1 {
			target = operand[0]
		} else if len(operand) > 1 {
			cw.unknown = true
			return
		}
		next := ev.cdTo(cw, target)
		for _, d := range next.dirs {
			cw.add(d)
		}
		cw.unknown = cw.unknown || next.unknown
	case "pushd", "popd", "chdir":
		cw.unknown = true
	}
}

var diskTools = map[string]bool{
	"mke2fs": true, "mkswap": true, "fdisk": true, "sfdisk": true, "parted": true, "wipefs": true,
	"shred": true, "blkdiscard": true, "cfdisk": true, "gdisk": true,
}

var haltTools = map[string]bool{
	"shutdown": true, "reboot": true, "halt": true, "poweroff": true, "init": true, "telinit": true,
}

var wideModes = map[string]bool{
	"777": true, "0777": true, "666": true, "0666": true, "a+rwx": true, "ugo+rwx": true,
	"a=rwx": true, "o+rwx": true, "o+w": true, "a+w": true, "ugo=rwx": true,
}

// highRisk flags the dangerous patterns: recursive deletes of broad paths,
// sudo, wide-open chmod, disk-level tools, forced pushes to shared branches,
// hard resets of shared branches, machine shutdown.
func (ev *evaluator) highRisk(u *unit) *riskInfo {
	s := u.simple
	if s.Elevated() {
		return &riskInfo{why: "it runs with elevated privileges (sudo/doas/su)"}
	}
	name, args := u.name, s.Args
	switch {
	case name == "rm":
		recursive := hasShort(args, "rR") || hasLong(args, "--recursive")
		if hasLong(args, "--no-preserve-root") {
			return &riskInfo{why: "rm --no-preserve-root", markers: []string{"--no-preserve-root"}}
		}
		if !recursive {
			return nil
		}
		mk := []string{"-r", "-R", "--recursive"}
		for _, a := range u.accesses {
			if a.write && !a.dynamic && ev.broad(a.real) {
				return &riskInfo{why: "recursive delete of " + a.raw, markers: mk}
			}
			if a.write && a.dynamic { // rm -rf $DIR: what it deletes is only known when it runs, and an empty variable is "/" or "."
				return &riskInfo{why: "recursive delete of " + a.raw + ", a path that is only known when the command runs", markers: mk}
			}
		}
		for _, p := range fsWriteSpecs["rm"].scan(args).pos {
			if ev.bareStar(p) {
				return &riskInfo{why: "recursive delete of " + p, markers: mk}
			}
		}
	case name == "chmod" || name == "chown" || name == "chgrp":
		if !(hasShort(args, "R") || hasLong(args, "--recursive")) {
			return nil
		}
		mk := []string{"-R", "--recursive"}
		if name == "chmod" {
			for _, a := range args {
				if wideModes[a] {
					return &riskInfo{why: "chmod -R with a world-writable mode", markers: append(mk, a)}
				}
			}
		}
		for _, a := range u.accesses {
			if a.write && !a.dynamic && ev.broad(a.real) {
				return &riskInfo{why: "recursive " + name + " of " + a.raw, markers: mk}
			}
		}
	case name == "dd":
		for _, a := range args {
			if strings.HasPrefix(a, "of=/dev/") && !harmlessDevice(strings.TrimPrefix(a, "of=")) {
				return &riskInfo{why: "dd writing to a device", markers: []string{"of=/dev"}}
			}
		}
	case strings.HasPrefix(name, "mkfs") || diskTools[name]:
		return &riskInfo{why: name + " can destroy a disk"}
	case haltTools[name]:
		return &riskInfo{why: name + " stops the machine"}
	case name == "git":
		return ev.gitRisk(args)
	}
	return nil
}

// broad reports whether deleting or re-owning real would take in the whole
// workspace, the home directory, or the file system root.
func (ev *evaluator) broad(real string) bool {
	if real == "/" || real == "" {
		return real == "/"
	}
	for _, r := range ev.rs.roots {
		if inside(real, r.real) {
			return true
		}
	}
	for _, h := range uniq(ev.rs.home, ev.rs.homeReal) {
		if h != "" && inside(real, h) {
			return true
		}
	}
	return false
}

// bareStar recognises rm targets like *, .*, ./*, /*, ~/*, ../* whose directory
// is broad: they delete the contents of the workspace, home or beyond.
func (ev *evaluator) bareStar(p string) bool {
	base := path.Base(p)
	if base != "*" && base != ".*" && base != "**" {
		return false
	}
	dir := path.Dir(p)
	if dir == "." {
		return true // the current directory, normally the workspace
	}
	w, _, dyn := ev.rs.expandWordVars(dir, ev.startDir(), ev.expansion())
	if dyn {
		return true
	}
	if !filepath.IsAbs(w) {
		w = filepath.Join(ev.startDir(), w)
	}
	return ev.broad(realPath(filepath.Clean(w)))
}

func (ev *evaluator) gitRisk(args []string) *riskInfo {
	i := 0
	for i < len(args) {
		switch {
		case args[i] == "-C" || args[i] == "-c":
			i += 2
		case strings.HasPrefix(args[i], "-"):
			i++
		default:
			goto sub
		}
	}
	return nil
sub:
	if i >= len(args) {
		return nil
	}
	sub, rest := args[i], args[i+1:]
	switch sub {
	case "push":
		force := hasShort(rest, "f") || hasLong(rest, "--force", "--force-with-lease", "--force-if-includes", "--mirror")
		var targets []string
		for _, a := range rest {
			if strings.HasPrefix(a, "+") {
				force = true
			}
			if !strings.HasPrefix(a, "-") {
				targets = append(targets, a)
			}
		}
		if len(targets) > 0 {
			targets = targets[1:] // the first operand is the remote
		}
		mk := []string{"-f", "--force", "--mirror", "+"}
		if hasLong(rest, "--mirror") {
			return &riskInfo{why: "git push --mirror overwrites the remote", markers: mk}
		}
		if force {
			if len(targets) == 0 {
				return &riskInfo{why: "forced git push with no explicit branch (may be main/master)", markers: mk}
			}
			for _, t := range targets {
				t = strings.TrimPrefix(t, "+")
				if j := strings.LastIndexByte(t, ':'); j >= 0 {
					t = t[j+1:]
				}
				t = strings.TrimPrefix(t, "refs/heads/")
				if sharedBranch(t) || t == "HEAD" {
					return &riskInfo{why: "forced git push to " + t, markers: mk}
				}
			}
		}
		if hasLong(rest, "--delete") || hasShort(rest, "d") {
			for _, t := range targets {
				if sharedBranch(strings.TrimPrefix(t, "refs/heads/")) {
					return &riskInfo{why: "deleting remote branch " + t, markers: []string{"--delete", "-d"}}
				}
			}
		}
		for _, t := range targets {
			if strings.HasPrefix(t, ":") && sharedBranch(strings.TrimPrefix(strings.TrimPrefix(t, ":"), "refs/heads/")) {
				return &riskInfo{why: "deleting remote branch " + t[1:]}
			}
		}
	case "reset":
		if !hasLong(rest, "--hard") {
			return nil
		}
		for _, d := range ev.startDirs() {
			b, ok := gitBranch(d)
			if !ok {
				return &riskInfo{why: "git reset --hard (current branch unknown)", markers: []string{"--hard"}}
			}
			if sharedBranch(b) {
				return &riskInfo{why: "git reset --hard on shared branch " + b, markers: []string{"--hard"}}
			}
		}
	}
	return nil
}

// startDirs wraps the evaluator's initial working directory as a single-candidate path set.
func (ev *evaluator) startDirs() []string { return []string{ev.startDir()} }

// isForkBomb recognises the classic  :(){ :|:& };:  and its renamed variants.
func isForkBomb(cmd string) bool {
	s := strings.Join(strings.Fields(cmd), "")
	for from := 0; ; {
		j := strings.Index(s[from:], "(){")
		if j < 0 {
			return false
		}
		j += from
		k := j
		for k > 0 && (s[k-1] == ':' || s[k-1] == '_' || s[k-1] == '.' || s[k-1] >= 'a' && s[k-1] <= 'z' ||
			s[k-1] >= 'A' && s[k-1] <= 'Z' || s[k-1] >= '0' && s[k-1] <= '9') {
			k--
		}
		if name := s[k:j]; name != "" {
			if end := strings.IndexByte(s[j+3:], '}'); end >= 0 {
				if strings.Contains(s[j+3:j+3+end], name+"|"+name+"&") {
					return true
				}
			}
		}
		from = j + 3
	}
}
