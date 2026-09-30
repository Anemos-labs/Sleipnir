package perm

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/reee344/sleipnir/internal/shellparse"
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
	dyn      string // reason the command word itself cannot be judged
	envBad   string // env assignment that changes what the command runs
	network  bool

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
}

func (e *Engine) evaluate(v *view, r Request) verdict {
	ev := &evaluator{e: e, v: v, rs: e.rs, r: r, globBudget: 20000}
	res := ev.run()
	if r.Risk == RiskHigh && res.kind == vAllow && !res.explicit && v.mode != ModeBypass {
		if v.mode == ModePlan {
			return deny(planReason("the tool marked this action high risk"))
		}
		return ask("the tool marked this action high risk", nil)
	}
	return res
}

func (ev *evaluator) run() verdict {
	if cmd, ok := ev.command(); ok {
		return ev.bash(cmd)
	}
	return ev.tool()
}

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

func (ev *evaluator) startDir() string {
	if c := ev.r.Cwd; c != "" {
		if filepath.IsAbs(c) {
			return filepath.Clean(c)
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
		return deny("built-in protection: fork bomb")
	}
	an := shellparse.Parse(cmd)
	// Judging costs file-system lookups per command; a line with thousands of
	// commands is checked up to a bound and then never auto-allowed.
	tooMany := len(an.Commands) > maxUnits
	if tooMany {
		an.Commands = an.Commands[:maxUnits]
	}
	cw := &cwdSet{dirs: []string{ev.startDir()}}
	res := verdict{kind: vAllow, explicit: true, reason: "empty command"}
	for i, s := range an.Commands {
		v := ev.judge(ev.buildUnit(s, cw))
		if v.kind == vDeny { // nothing can outrank a denial
			return v
		}
		if i == 0 {
			res = v
		} else {
			res = combine(res, v)
		}
	}
	why := opaqueReason(cmd, an)
	if why == "" && tooMany {
		why = fmt.Sprintf("it has more than %d commands, too many to check", maxUnits)
	}
	if why == "" || res.kind == vDeny || ev.v.mode == ModeBypass {
		return res
	}
	if ev.v.mode == ModePlan {
		return deny(planReason("cannot verify that the command is read-only: " + why))
	}
	if res.kind == vAllow {
		return ask("cannot be checked statically: "+why, nil)
	}
	res.rem = nil // remembering one command would not stop the construct asking again
	res.reason = "cannot be checked statically: " + why + "; " + res.reason
	return res
}

// opaqueReason names the constructs whose effect cannot be judged from the text.
func opaqueReason(cmd string, an shellparse.Analysis) string {
	switch {
	case !an.Parsed:
		return "the command could not be parsed reliably (" + an.Problem + ")"
	case an.HasCommandSubstitution:
		return "it contains a command substitution whose output decides what runs"
	case an.HasProcessSubstitution:
		return "it contains a process substitution"
	case an.PipesToShell:
		return "it pipes data into a shell or interpreter"
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

	for _, env := range s.Env {
		bad := false
		if s.Program == "" {
			bad = envDangerous(env)
		} else {
			bad = !envBenign(env)
		}
		if bad && u.envBad == "" {
			u.envBad = envName(env)
		}
		if _, val, ok := strings.Cut(env, "="); ok {
			for _, c := range pathCandidates(val) {
				u.accesses = append(u.accesses, ev.resolve(pathUse{raw: c}, cw)...)
			}
		}
	}
	u.redirs = ev.redirAccesses(s.Redirects, cw)
	if s.Program == "" {
		u.class = cmdNoop
		return u
	}
	if dynamicProgram(s.Program) || strings.HasPrefix(s.Program, "~") {
		u.dyn = "the command name is built from a variable, glob or substitution"
	}
	u.network = isNetwork(u.name, s.Args)

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
			u.why = "not on the read-only allowlist"
		}
		uses = unknownUses(s.Args)
		if u.name == "" {
			// ./tool or /opt/x/tool: running a file reads it; it is not a write.
			uses = append([]pathUse{{raw: s.Program}}, uses...)
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
	w, _, dyn := ev.rs.expandWord(dir, ev.startDir())
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
