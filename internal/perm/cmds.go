package perm

import (
	"os"
	"path/filepath"
	"strings"
)

// versionProbes are programs whose "--version" style invocation is harmless.
var versionProbes = map[string]bool{
	"node": true, "nodejs": true, "npm": true, "npx": true, "yarn": true, "pnpm": true, "bun": true,
	"deno": true, "python": true, "python2": true, "python3": true, "pip": true, "pip3": true,
	"ruby": true, "gem": true, "perl": true, "php": true, "java": true, "javac": true, "rustc": true,
	"cargo": true, "gcc": true, "g++": true, "cc": true, "clang": true, "clang++": true, "make": true,
	"cmake": true, "tsc": true, "docker": true, "dotnet": true, "lua": true, "rg": true, "git": true,
}

// safeAnalysis is the outcome of checking a command against the read-only
// allowlist.
type safeAnalysis struct {
	ok    bool
	why   string    // when !ok: what disqualifies it (or that it is unlisted)
	uses  []pathUse // files it reads
	chdir string    // git -C: operands are relative to this directory
}

// analyseSafe checks whether prog (a bare program name) with args is a
// read-only invocation on the default-mode allowlist.
func analyseSafe(prog string, args []string) safeAnalysis {
	switch prog {
	case "git":
		return analyseGit(args)
	case "go":
		return analyseGo(args)
	case "find":
		return analyseFind(args)
	case "sed":
		return analyseSed(args)
	case "for":
		// shellparse reports the word list of a for/select loop as this
		// pseudo-command. The loop only walks the names, so links are not
		// followed; what the body does with each name is judged where it occurs.
		var uses []pathUse
		for _, a := range args {
			uses = append(uses, pathUse{raw: a, noFollow: true, nameOnly: true})
		}
		return safeAnalysis{ok: true, uses: uses}
	case "set":
		// Strict mode only (set -e, set -eu, set -o pipefail): it changes how the script
		// stops, not what it touches. A bare set prints variables, and set -- rewrites
		// the arguments, so neither is allowed.
		for i := 0; i < len(args); i++ {
			a := args[i]
			if len(a) < 2 || (a[0] != '-' && a[0] != '+') || strings.Trim(a[1:], "euxo") != "" || strings.Contains(a[1:len(a)-1], "o") {
				return safeAnalysis{why: "set with " + a + ": only -e, -u and -o pipefail are on the read-only list"}
			}
			if a[len(a)-1] == 'o' { // -o and -euo take an option name
				if i+1 >= len(args) || !inList([]string{"pipefail", "errexit", "nounset"}, args[i+1]) {
					return safeAnalysis{why: "set -o with an option other than pipefail, errexit or nounset"}
				}
				i++
			}
		}
		if len(args) == 0 {
			return safeAnalysis{why: "a bare set prints every variable"}
		}
		return safeAnalysis{ok: true}
	case "cd":
		var operands []string
		for _, a := range args {
			if a != "--" && a != "-P" && a != "-L" && a != "-e" && a != "-@" {
				operands = append(operands, a)
			}
		}
		switch {
		case len(operands) > 1:
			return safeAnalysis{why: "cd with several arguments"}
		case len(operands) == 1 && operands[0] != "-":
			return safeAnalysis{ok: true, uses: []pathUse{{raw: operands[0]}}}
		}
		return safeAnalysis{ok: true}
	}
	if sp, ok := safeSpecs[prog]; ok {
		uses, why := sp.analyse(args)
		if why != "" {
			return safeAnalysis{why: why}
		}
		return safeAnalysis{ok: true, uses: uses}
	}
	if versionProbes[prog] && len(args) == 1 && inList([]string{"--version", "-V", "-v", "-version"}, args[0]) {
		return safeAnalysis{ok: true}
	}
	if prog == "command" && len(args) >= 1 && (args[0] == "-v" || args[0] == "-V") {
		return safeAnalysis{ok: true}
	}
	return safeAnalysis{why: "not on the read-only allowlist"}
}

// gitSafeSubcommands are read-only whatever their (non-output) options are.
var gitSafeSubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "blame": true, "rev-parse": true,
	"ls-files": true, "describe": true, "rev-list": true, "ls-tree": true, "merge-base": true,
	"shortlog": true, "name-rev": true, "cat-file": true, "diff-tree": true, "diff-files": true,
	"diff-index": true, "show-ref": true, "for-each-ref": true, "whatchanged": true,
	"count-objects": true, "check-ignore": true, "version": true, "grep": true,
}

var gitDenyFlags = []string{"--output", "--output=*", "--ext-diff", "--exec-path*", "--open-files-in-pager", "-O*"}

func analyseGit(args []string) safeAnalysis {
	var out safeAnalysis
	i := 0
loop:
	for i < len(args) {
		switch a := args[i]; {
		case a == "--no-pager" || a == "-P" || a == "--no-optional-locks":
			i++
		case a == "-C":
			if i+1 >= len(args) || out.chdir != "" {
				return safeAnalysis{why: "git -C without a single directory"}
			}
			out.chdir = args[i+1]
			out.uses = append(out.uses, pathUse{raw: out.chdir})
			i += 2
		case a == "--version" && i == len(args)-1:
			return safeAnalysis{ok: true}
		case strings.HasPrefix(a, "-"):
			return safeAnalysis{why: "git option " + a + " is not on the read-only list"}
		default:
			break loop
		}
	}
	if i >= len(args) {
		return safeAnalysis{why: "git without a subcommand"}
	}
	sub, rest := args[i], args[i+1:]
	deny := func(sp *argSpec) (string, bool) {
		s := sp.scan(rest)
		if d := sp.denied(&s); d != "" {
			return "git " + sub + " " + d + " can write files or run programs", false
		}
		return "", true
	}
	operands := func(rest []string) {
		for _, r := range rest {
			if r != "--" && !strings.HasPrefix(r, "-") {
				out.uses = append(out.uses, pathUse{raw: r})
			}
		}
	}
	switch {
	case gitSafeSubcommands[sub]:
		if why, ok := deny(&argSpec{deny: gitDenyFlags}); !ok {
			return safeAnalysis{why: why}
		}
		operands(rest)
	case sub == "branch":
		sp := &argSpec{long: []string{"contains", "no-contains", "merged", "no-merged", "points-at", "sort", "format", "abbrev"}}
		s := sp.scan(rest)
		allowed := []string{"-a", "--all", "-r", "--remotes", "-v", "--verbose", "--list", "-l", "--show-current",
			"--no-color", "--color", "--no-column", "--column", "--contains", "--no-contains", "--merged",
			"--no-merged", "--points-at", "--sort", "--format", "-i", "--ignore-case", "--abbrev", "--no-abbrev",
			"-q", "--quiet"}
		for f := range s.flags {
			if !inList(allowed, f) && !strings.HasPrefix(f, "--sort=") && !strings.HasPrefix(f, "--format=") &&
				!strings.HasPrefix(f, "--color=") && !strings.HasPrefix(f, "--column=") && !strings.HasPrefix(f, "--abbrev=") {
				return safeAnalysis{why: "git branch " + f + " changes branches"}
			}
		}
		if len(s.pos) > 0 && !s.has("-l", "--list") {
			return safeAnalysis{why: "git branch with a name creates a branch"}
		}
	case sub == "remote":
		switch {
		case len(rest) == 0 || (len(rest) == 1 && (rest[0] == "-v" || rest[0] == "--verbose")):
		case len(rest) == 2 && rest[0] == "get-url":
		default:
			return safeAnalysis{why: "git remote form is not read-only"}
		}
	case sub == "tag":
		sp := &argSpec{long: []string{"contains", "no-contains", "points-at", "merged", "no-merged", "sort", "format"}}
		s := sp.scan(rest)
		allowed := []string{"-l", "--list", "-n", "--contains", "--no-contains", "--points-at", "--merged",
			"--no-merged", "--sort", "--format", "--column", "--no-column", "-i", "--ignore-case"}
		for f := range s.flags {
			if !inList(allowed, f) && !strings.HasPrefix(f, "--sort=") && !strings.HasPrefix(f, "--format=") &&
				!strings.HasPrefix(f, "-n") {
				return safeAnalysis{why: "git tag " + f + " changes tags"}
			}
		}
		if len(s.pos) > 0 && !s.has("-l", "--list") {
			return safeAnalysis{why: "git tag with a name creates a tag"}
		}
	case sub == "stash":
		if len(rest) == 0 || (rest[0] != "list" && rest[0] != "show") {
			return safeAnalysis{why: "git stash changes the stash"}
		}
	case sub == "worktree":
		if len(rest) == 0 || rest[0] != "list" {
			return safeAnalysis{why: "git worktree changes worktrees"}
		}
	case sub == "reflog":
		if len(rest) > 0 && rest[0] != "show" {
			return safeAnalysis{why: "git reflog " + rest[0] + " rewrites the reflog"}
		}
	default:
		return safeAnalysis{why: "git " + sub + " is not on the read-only list"}
	}
	out.ok = true
	return out
}

// goValueFlags are go build/list/vet flags that take a value, needed only to
// keep flag values from being mistaken for package operands.
var goValueFlags = map[string]bool{
	"f": true, "tags": true, "mod": true, "modfile": true, "pkgdir": true, "overlay": true, "p": true,
	"gcflags": true, "ldflags": true, "asmflags": true, "buildmode": true, "compiler": true,
	"installsuffix": true, "o": true, "vettool": true, "toolexec": true, "exec": true, "pgo": true,
	"covermode": true, "coverpkg": true, "buildvcs": true, "C": true,
}

func pathLike(s string) bool {
	return strings.Contains(s, "/") || s == "." || s == ".." || strings.HasPrefix(s, "~")
}

func analyseGo(args []string) safeAnalysis {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return safeAnalysis{why: "go without a subcommand"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "version":
		return safeAnalysis{ok: true}
	case "env", "list", "vet", "build", "doc":
	default:
		return safeAnalysis{why: "go " + sub + " is not on the read-only list"}
	}
	out := safeAnalysis{ok: true}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			if sub != "env" && sub != "doc" && pathLike(a) {
				out.uses = append(out.uses, pathUse{raw: a})
			}
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch name {
		case "toolexec", "exec", "vettool":
			return safeAnalysis{why: "go -" + name + " runs another program"}
		case "ldflags", "gcflags", "asmflags", "compiler", "buildmode", "pkgdir", "installsuffix":
			// -ldflags=-extld=prog runs prog; the others change what is built where.
			return safeAnalysis{why: "go -" + name + " can run programs or change what is written"}
		case "w", "u", "W":
			if sub == "env" {
				return safeAnalysis{why: "go env -" + name + " changes the environment"}
			}
		case "o":
			if !hasVal && i+1 < len(rest) {
				val = rest[i+1]
				i++
			}
			if sub == "build" && val != "/dev/null" {
				return safeAnalysis{why: "go build -o writes a file"}
			}
			continue
		}
		if goValueFlags[name] && !hasVal && i+1 < len(rest) {
			if name == "overlay" || name == "modfile" {
				out.uses = append(out.uses, pathUse{raw: rest[i+1]})
			}
			i++
		} else if hasVal && (name == "overlay" || name == "modfile") {
			out.uses = append(out.uses, pathUse{raw: val})
		}
	}
	return out
}

// findBanned are find primaries that delete, run programs or write files.
var findBanned = []string{"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fdelete", "-fprint",
	"-fprintf", "-fprint0", "-fls", "-follow"}

func analyseFind(args []string) safeAnalysis {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-H" || a == "-P":
			i++
		case a == "-L":
			return safeAnalysis{why: "find -L follows symlinks"}
		case a == "-D":
			i += 2
		case strings.HasPrefix(a, "-O") && len(a) == 3:
			i++
		default:
			goto starts
		}
	}
starts:
	out := safeAnalysis{ok: true}
	exprStart := func(a string) bool {
		return strings.HasPrefix(a, "-") || a == "(" || a == ")" || a == "!" || a == ","
	}
	n := 0
	for i < len(args) && !exprStart(args[i]) {
		out.uses = append(out.uses, pathUse{raw: args[i], tree: true, nameOnly: true})
		i++
		n++
	}
	if n == 0 {
		out.uses = append(out.uses, pathUse{raw: ".", tree: true, nameOnly: true})
	}
	for ; i < len(args); i++ {
		if inList(findBanned, args[i]) {
			return safeAnalysis{why: "find " + args[i] + " can delete or run programs"}
		}
	}
	return out
}

// fsWriteSpecs are the file-manipulating programs accept-edits mode may run
// when every path they touch is inside the workspace.
var fsWriteSpecs = map[string]*argSpec{
	"mkdir": {short: "m", long: []string{"mode"}},
	"touch": {short: "dtr", long: []string{"date", "reference", "time"}, pathVals: []string{"-r", "--reference"}},
	"rmdir": {},
	"rm":    {},
	"cp":    {short: "St", long: []string{"suffix", "target-directory"}},
	"mv":    {short: "St", long: []string{"suffix", "target-directory"}},
	"tee":   {},
}

// analyseFSWrite returns the paths a file-manipulating command writes.
func analyseFSWrite(prog string, args []string) ([]pathUse, bool) {
	sp, ok := fsWriteSpecs[prog]
	if !ok {
		return nil, false
	}
	s := sp.scan(args)
	var uses []pathUse
	for _, p := range s.pos {
		if p == "-" {
			continue
		}
		uses = append(uses, pathUse{raw: p, write: true, tree: prog != "tee", noFollow: prog == "rm" || prog == "rmdir"})
	}
	for _, v := range append(s.values("-t"), s.values("--target-directory")...) {
		uses = append(uses, pathUse{raw: v, write: true, tree: true})
	}
	for _, f := range sp.pathVals {
		for _, v := range s.values(f) {
			uses = append(uses, pathUse{raw: v})
		}
	}
	return uses, true
}

// unknownUses treats every operand of an unlisted command as a path it may read
// or write, recursively. Options are searched for embedded paths too
// (--out=/etc/x, -o/etc/x, of=/dev/sda, -v ~/.ssh:/root/.ssh).
func unknownUses(args []string) []pathUse {
	var out []pathUse
	for _, a := range args {
		for _, c := range pathCandidates(a) {
			out = append(out, pathUse{raw: c, both: true, tree: true})
		}
	}
	return out
}

// pathCandidates returns the substrings of a command-line word that might be
// file names: the word itself, the value of KEY=value and --opt=value, and the
// pieces of a colon- or comma-separated list.
func pathCandidates(a string) []string {
	if a == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if strings.HasPrefix(a, "-") {
		if i := strings.IndexByte(a, '='); i >= 0 {
			add(a[i+1:])
		}
		if i := strings.IndexAny(a, "/~"); i > 0 {
			add(a[i:]) // -o/etc/x, -I/usr/include
		}
	} else {
		add(a)
		if i := strings.IndexByte(a, '='); i > 0 {
			add(a[i+1:])
		}
	}
	for _, c := range append([]string(nil), out...) {
		add(strings.TrimPrefix(c, "@")) // curl -d @file, -F f=@file
	}
	for _, c := range append([]string(nil), out...) {
		for _, piece := range strings.FieldsFunc(c, func(r rune) bool { return r == ':' || r == ',' }) {
			add(piece)
		}
	}
	return out
}

// benign environment assignments that cannot change what a command executes.
var benignEnv = map[string]bool{
	"LANG": true, "LANGUAGE": true, "TZ": true, "NO_COLOR": true, "FORCE_COLOR": true, "CLICOLOR": true,
	"CLICOLOR_FORCE": true, "TERM": true, "COLUMNS": true, "LINES": true, "CI": true,
	"DEBIAN_FRONTEND": true, "GOOS": true, "GOARCH": true, "CGO_ENABLED": true, "GO111MODULE": true,
	"GIT_TERMINAL_PROMPT": true, "DEBUG": true, "NODE_ENV": true, "RUST_BACKTRACE": true, "RUST_LOG": true,
	"PYTHONDONTWRITEBYTECODE": true, "PYTHONUNBUFFERED": true, "PYTHONIOENCODING": true,
}

var dangerousEnv = map[string]bool{
	"PATH": true, "HOME": true, "IFS": true, "ENV": true, "CDPATH": true, "PROMPT_COMMAND": true,
	"PS1": true, "PS2": true, "PS3": true, "PS4": true, "EDITOR": true, "VISUAL": true, "PAGER": true,
	"BROWSER": true, "TMPDIR": true, "TERMINFO": true, "OPENSSL_CONF": true, "SSL_CERT_FILE": true,
	"CURL_CA_BUNDLE": true, "REQUESTS_CA_BUNDLE": true, "http_proxy": true, "https_proxy": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
}

var dangerousEnvPrefixes = []string{
	"LD_", "DYLD_", "BASH_", "SHELL", "PYTHON", "NODE_", "NPM_", "PERL", "RUBY", "JAVA", "_JAVA",
	"GIT_", "GO", "CARGO_", "RUST", "XDG_", "SSH_", "SUDO_", "GCONV", "MALLOC_", "LESS", "MAN",
}

func envName(assign string) string {
	n, _, _ := strings.Cut(assign, "=")
	return strings.TrimSuffix(n, "+")
}

func envBenign(assign string) bool {
	n := envName(assign)
	return benignEnv[n] || strings.HasPrefix(n, "LC_")
}

// envDangerous reports assignments that change how later commands resolve or
// behave; benign names win over the prefix list (GOOS is fine, GOFLAGS is not).
func envDangerous(assign string) bool {
	n := envName(assign)
	if benignEnv[n] || strings.HasPrefix(n, "LC_") {
		return false
	}
	if dangerousEnv[n] {
		return true
	}
	for _, p := range dangerousEnvPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

var networkPrograms = map[string]bool{
	"curl": true, "wget": true, "ssh": true, "scp": true, "sftp": true, "rsync": true, "nc": true,
	"ncat": true, "netcat": true, "telnet": true, "ftp": true, "ping": true, "dig": true, "nslookup": true,
}

func isNetwork(prog string, args []string) bool {
	if networkPrograms[prog] {
		return true
	}
	if prog == "git" {
		for _, a := range args {
			switch a {
			case "push", "pull", "fetch", "clone", "ls-remote", "submodule":
				return true
			}
		}
	}
	return false
}

// hasShort reports whether args holds one of letters as a short option, alone
// or inside a cluster ("-rf" has "r" and "f"). Operands after "--" are ignored.
func hasShort(args []string, letters string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], letters) {
			return true
		}
	}
	return false
}

func hasLong(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

// gitBranch reads the current branch of the repository at dir from .git/HEAD.
// ok is false when it cannot be determined (no repository, detached HEAD).
func gitBranch(dir string) (name string, ok bool) {
	head := filepath.Join(dir, ".git")
	fi, err := os.Stat(head)
	if err != nil {
		return "", false
	}
	if !fi.IsDir() { // worktree or submodule: .git is a file "gitdir: <path>"
		b, err := os.ReadFile(head)
		if err != nil || !strings.HasPrefix(string(b), "gitdir:") {
			return "", false
		}
		g := strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:"))
		if !filepath.IsAbs(g) {
			g = filepath.Join(dir, g)
		}
		head = g
	}
	b, err := os.ReadFile(filepath.Join(head, "HEAD"))
	if err != nil {
		return "", false
	}
	ref, found := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: refs/heads/")
	return ref, found
}

var sharedBranches = map[string]bool{
	"main": true, "master": true, "trunk": true, "develop": true, "development": true,
	"production": true, "prod": true, "stable": true,
}

func sharedBranch(name string) bool {
	return sharedBranches[name] || strings.HasPrefix(name, "release") || strings.HasPrefix(name, "hotfix")
}
