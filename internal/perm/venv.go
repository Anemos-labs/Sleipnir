package perm

import (
	"bufio"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// Project virtual environments.
//
// A team that writes Python tests needs the packages they import (pytest, the project's own dependencies), and the system Python is
// not theirs to change. What the tests preset allows instead (the rule Bash(python:venv)) is a virtual environment of the project:
//
//   - creating one in a directory of the workspace: python -m venv DIR, virtualenv DIR, uv venv [DIR];
//   - installing packages into it with its own pip, from the package index pip or uv is configured with: DIR/bin/pip install ...,
//     DIR/bin/python -m pip install ..., uv pip install --python DIR/bin/python ...;
//   - running the tests with it: DIR/bin/python -m pytest|unittest ..., DIR/bin/pytest ..., and DIR/bin/python -m
//     py_compile|compileall on files of the workspace.
//
// The environment must be a directory of the workspace that is not a workspace root (the project, an agent's tree, $TMPDIR); every
// entry of it, of its lib directories and of their site-packages must resolve inside it; and to install into it or run it, it must be
// one (a pyvenv.cfg) whose python is a link to an installed interpreter and whose scripts run that python (ownInterpreter, ownScript).
// So an install lands in the workspace, never in the system's or the user's site-packages; a test run is allowed while every file it
// names is inside the workspace. The options of an install are a short list (requirement and constraint files and editable paths inside the workspace,
// -U, -q, --pre, --no-input, --no-cache-dir, --disable-pip-version-check); an option that changes where packages come from (an index,
// find-links, a trusted host) or where they go (--target, --prefix, --root, --user, --src, --break-system-packages), a URL or a
// repository, or a file outside the workspace keeps the command asking, and it is then not routine. What the project's own files say (a
// requirement file, pyproject.toml, setup.py, a pip.conf of the environment) is the project's: an install runs their build code, as a
// test run runs the project's tests. The checks are made when the command is judged; a link swapped in before it runs is not seen.

// venvUse is a command that creates a Python virtual environment, installs packages into one, or runs the tests with one.
type venvUse struct {
	create, install bool   // neither: it runs a test or check program of the environment
	dir             string // the environment's directory, as written
	why             string // why the tests preset does not allow it; "" when it does
	uses            []pathUse
}

// ok reports whether the tests preset's Bash(python:venv) allows the command.
func (v *venvUse) ok() bool { return v != nil && v.why == "" }

// testRun reports whether the command only runs the tests (or a check) with an environment: what Bash(python:venv-tests) covers.
func (v *venvUse) testRun() bool { return v != nil && !v.create && !v.install }

// reachesIndex reports whether the command is an install the preset allows: it reaches the package index.
func (v *venvUse) reachesIndex() bool { return v.ok() && v.install }

// venvRunModules are the modules a python of an environment may run under the preset; those of venvFileModules on files of the
// workspace only.
var (
	venvRunModules  = map[string]bool{"pytest": true, "unittest": true, "py_compile": true, "compileall": true}
	venvFileModules = map[string]bool{"py_compile": true, "compileall": true}
)

// venvCommand recognises a command that creates a virtual environment, installs into one or runs the tests with one (see the head of
// this file), and judges whether the tests preset allows it; it returns nil for any other command.
func (ev *evaluator) venvCommand(s shellparse.Simple, cw *cwdSet) *venvUse {
	prog, args := s.Program, s.Args
	if prog == "" || dynamicProgram(prog) || s.Elevated() {
		return nil
	}
	base := path.Base(prog)
	named := shellparse.ProgramName(prog) != "" // a bare name, or a binary of a system directory
	python := shellparse.InterpreterName(base) == "python"
	inBin := strings.Contains(prog, "/")
	switch {
	case named && python && hasPrefixArgs(args, "-m", "venv"):
		return ev.venvCreate(args[2:], venvTool{name: "python -m venv", flags: venvFlags, values: []string{"--prompt"}}, cw)
	case named && base == "virtualenv":
		return ev.venvCreate(args, venvTool{name: "virtualenv", flags: virtualenvFlags, values: []string{"--prompt"}}, cw)
	case named && base == "uv" && hasPrefixArgs(args, "venv"):
		return ev.venvCreate(args[1:], venvTool{name: "uv venv", flags: uvVenvFlags, values: []string{"--prompt"}, optional: true}, cw)
	case named && base == "uv" && hasPrefixArgs(args, "pip", "install"):
		return ev.uvInstall(args[2:], cw)
	case inBin && pipName(base) && hasPrefixArgs(args, "install"):
		return ev.pipInstall(prog, true, args[1:], cw)
	case inBin && python && hasPrefixArgs(args, "-m", "pip", "install"):
		return ev.pipInstall(prog, false, args[3:], cw)
	case inBin && python && len(args) >= 2 && args[0] == "-m" && venvRunModules[args[1]]:
		return ev.venvRun(prog, false, args, venvFileModules[args[1]], cw)
	case inBin && (base == "pytest" || base == "py.test"):
		return ev.venvRun(prog, true, args, false, cw)
	}
	return nil
}

// hasPrefixArgs reports whether args begins with want.
func hasPrefixArgs(args []string, want ...string) bool {
	if len(args) < len(want) {
		return false
	}
	for i, w := range want {
		if args[i] != w {
			return false
		}
	}
	return true
}

// pipName reports whether base is pip, pip3 or pip3.N.
func pipName(base string) bool {
	return base == "pip" || base == "pip3" || strings.HasPrefix(base, "pip3.") && strings.Trim(base[len("pip3."):], "0123456789") == ""
}

// venvOf is the environment a program of its bin directory belongs to (".venv/bin/pip" is in ".venv"), or "" when the program is not
// in a directory called bin.
func venvOf(prog string) string {
	bin := path.Dir(prog)
	if path.Base(bin) != "bin" {
		return ""
	}
	return path.Dir(bin)
}

// venvTool describes the options of a command that creates an environment.
type venvTool struct {
	name     string
	flags    map[string]bool // options without a value
	values   []string        // options with a value (that is not a file)
	optional bool            // the directory may be left out (uv venv: .venv)
}

var (
	venvFlags       = map[string]bool{"--system-site-packages": true, "--without-pip": true, "--symlinks": true, "--copies": true}
	virtualenvFlags = map[string]bool{"--system-site-packages": true, "-q": true, "--quiet": true}
	uvVenvFlags     = map[string]bool{"-q": true, "--quiet": true, "--seed": true}
)

// venvCreate judges a command that creates an environment in the one directory its operands name. It writes that directory.
func (ev *evaluator) venvCreate(args []string, t venvTool, cw *cwdSet) *venvUse {
	v := &venvUse{create: true}
	var dirs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, inline := strings.Cut(a, "=")
		switch {
		case a == "--":
			dirs = append(dirs, args[i+1:]...)
			i = len(args)
		case !strings.HasPrefix(a, "-") || a == "-":
			dirs = append(dirs, a)
		case t.flags[a]:
		case inList(t.values, name):
			if !inline {
				i++
			}
		default:
			// An option it is not allowed (--clear) may do more than create: every operand is judged as an unlisted command's is.
			v.why = t.name + " " + a + " is not an option the tests preset allows (--clear and --upgrade change an existing directory)"
			v.uses = unknownUses(args)
			return v
		}
	}
	switch {
	case len(dirs) == 0 && t.optional:
		dirs = []string{".venv"}
	case len(dirs) != 1:
		v.why = t.name + " creates one environment at a time in the tests preset"
		return v
	}
	v.dir = dirs[0]
	real, why := ev.venvDir(v.dir, cw)
	if why == "" {
		why = existingDir(real, v.dir)
	}
	v.why = why
	// It writes its own names in the directory (bin, lib, include, pyvenv.cfg), not into what is already below it (access.format).
	v.uses = []pathUse{{raw: v.dir, write: true, tree: true, format: true}}
	return v
}

// existingDir is why an environment may not be created in a directory that is already there: it is not a directory, or it is one
// that holds something other than an environment. "" when it does not exist, is empty, or is an environment already.
func existingDir(real, shown string) string {
	fi, err := os.Lstat(real)
	if err != nil {
		return ""
	}
	if !fi.IsDir() {
		return shown + " exists and is not a directory"
	}
	names, _ := listDir(real, 1)
	if len(names) == 0 {
		return ""
	}
	if why := venvLayout(real, shown); why != "" {
		return shown + " exists and is not a virtual environment"
	}
	return ""
}

// The options of an install, in pip and in uv pip.
var (
	// pipSwitches take no value: they change how much is printed, whether newer versions or pre-releases are taken, and caching.
	pipSwitches = map[string]bool{"-U": true, "--upgrade": true, "-q": true, "--quiet": true, "--no-input": true,
		"--disable-pip-version-check": true, "--no-cache-dir": true, "--pre": true}
	uvSwitches = map[string]bool{"-U": true, "--upgrade": true, "-q": true, "--quiet": true, "--no-cache": true}
	// pipFiles take a file of the workspace: a requirement or constraint file, or the project to install in editable mode.
	pipFiles = map[string]bool{"-r": true, "--requirement": true, "-c": true, "--constraint": true, "-e": true, "--editable": true}
	uvFiles  = map[string]bool{"-r": true, "--requirement": true, "--requirements": true, "-c": true, "--constraint": true,
		"--constraints": true, "-e": true, "--editable": true}
	// installSources change where packages come from; installTargets where they go.
	installSources = map[string]bool{"-i": true, "--index-url": true, "--extra-index-url": true, "-f": true, "--find-links": true,
		"--trusted-host": true, "--no-index": true, "--index": true, "--default-index": true, "--config-file": true,
		"--index-strategy": true, "--keyring-provider": true, "--allow-insecure-host": true}
	installTargets = map[string]bool{"-t": true, "--target": true, "--prefix": true, "--root": true, "--user": true, "--src": true,
		"--break-system-packages": true, "--system": true}
)

// requirementSpec is a requirement by name: a project name, extras, and version specifiers ("pytest", "requests[socks]>=2,<3").
var requirementSpec = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?(\[[A-Za-z0-9._, -]*\])?` +
	`( *(~=|===|==|!=|<=|>=|<|>) *[A-Za-z0-9.*+!_-]+ *(, *(~=|===|==|!=|<=|>=|<|>) *[A-Za-z0-9.*+!_-]+ *)*)?$`)

// ownEnv is why the environment of a program of its bin directory is not one the preset installs into or runs: it is not in such a
// directory, the directory is not one of the workspace, it is not an environment that keeps what it holds inside it, or the program
// does not run the environment's own interpreter (ownInterpreter): prog is that interpreter (DIR/bin/python), or, with script, a
// script whose first line names it (DIR/bin/pip, DIR/bin/pytest). It returns the environment's resolved directory.
func (ev *evaluator) ownEnv(prog string, script bool, cw *cwdSet) (dir, real, why string) {
	dir = venvOf(prog)
	if dir == "" {
		return "", "", prog + " is not in the bin directory of a virtual environment"
	}
	real, why = ev.venvDir(dir, cw)
	if why == "" {
		why = venvLayout(real, dir)
	}
	if why != "" {
		return dir, real, why
	}
	interp := path.Base(prog)
	if script {
		var shebang string
		if shebang, why = ownScript(real, interp, prog); why != "" {
			return dir, real, why
		}
		interp = filepath.Base(shebang)
	}
	return dir, real, ev.ownInterpreter(real, interp, prog)
}

// ownInterpreter is why DIR/bin/<name> of the environment real is not its own Python interpreter, the one thing that makes python -m
// pip install into the environment: CPython takes the environment from a pyvenv.cfg beside (or above) the path it was started by, so
// a link to an installed interpreter (what python -m venv, virtualenv and uv venv make) runs in the environment, while a program that
// starts another python (a wrapper script, "exec /usr/bin/python3") runs that one, which installs into the system's or the user's
// site-packages. So the entry, followed through its links, must end at a native executable (ELF or Mach-O: not a script, which a
// shell would also run without a "#!") named python, outside the workspace (a program of the workspace is one an agent can write).
// A copy of an interpreter made by python -m venv --copies is therefore not taken: it asks.
func (ev *evaluator) ownInterpreter(real, name, shown string) string {
	notOwn := shown + " does not run the environment's own interpreter (a link to an installed python)"
	p := filepath.Join(real, "bin", name)
	if _, err := os.Lstat(p); err != nil || shellparse.InterpreterName(name) != "python" {
		return notOwn
	}
	target := realPath(p)
	fi, err := os.Stat(target)
	switch {
	case target == "" || err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0:
		return notOwn
	case shellparse.InterpreterName(filepath.Base(target)) != "python":
		return notOwn
	case ev.rs.inWorkspace(target):
		return shown + " runs a program of the workspace, not an installed python"
	}
	f, err := os.Open(target)
	if err != nil {
		return notOwn
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || !nativeExecutable(magic) {
		return notOwn
	}
	return ""
}

// nativeExecutable reports whether the first bytes of a file are those of an executable the kernel runs itself: ELF, or Mach-O (thin
// or universal).
func nativeExecutable(m [4]byte) bool {
	switch string(m[:]) {
	case "\x7fELF", "\xfe\xed\xfa\xce", "\xfe\xed\xfa\xcf", "\xce\xfa\xed\xfe", "\xcf\xfa\xed\xfe", "\xca\xfe\xba\xbe":
		return true
	}
	return false
}

// pipInstall judges an install by the pip of an environment: DIR/bin/pip install (script true) or DIR/bin/python -m pip install. It
// runs the program (a read of it, not of what a python link points to), writes the environment, and reads the files it installs from.
func (ev *evaluator) pipInstall(prog string, script bool, args []string, cw *cwdSet) *venvUse {
	dir, _, why := ev.ownEnv(prog, script, cw)
	v := &venvUse{install: true, dir: dir, why: why}
	var reads []pathUse
	if why == "" {
		v.why, reads = ev.installArgs("pip install", args, pipSwitches, pipFiles, cw)
	}
	v.uses = installUses(prog, dir, v.why, reads, args)
	return v
}

// installUses are the files an install reads and writes: it runs its program (a read of the program itself, not of the system python a
// link of the environment leads to), writes the environment and reads what it installs from. One the preset does not allow (why) has
// every operand judged as an unlisted command's are.
func installUses(prog, dir, why string, reads []pathUse, args []string) []pathUse {
	uses := []pathUse{{raw: prog, noFollow: true}}
	if why != "" {
		return append(uses, unknownUses(args)...)
	}
	return append(append(uses, pathUse{raw: dir, write: true, tree: true}), reads...)
}

// uvInstall judges uv pip install, which installs into the environment its --python names (DIR/bin/python): without one, uv picks an
// environment by itself ($VIRTUAL_ENV, a .venv above the working directory), which the command does not show.
func (ev *evaluator) uvInstall(args []string, cw *cwdSet) *venvUse {
	v := &venvUse{install: true}
	var py []string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		name, val, inline := strings.Cut(args[i], "=")
		if name != "-p" && name != "--python" {
			rest = append(rest, args[i])
			continue
		}
		if !inline {
			if i+1 >= len(args) {
				break
			}
			i++
			val = args[i]
		}
		py = append(py, val)
	}
	if len(py) != 1 || !strings.Contains(py[0], "/") || shellparse.InterpreterName(path.Base(py[0])) != "python" {
		v.why = "uv pip install without --python DIR/bin/python installs into an environment the command does not name"
		return v
	}
	v.dir, _, v.why = ev.ownEnv(py[0], false, cw)
	var reads []pathUse
	if v.why == "" {
		v.why, reads = ev.installArgs("uv pip install", rest, uvSwitches, uvFiles, cw)
	}
	v.uses = installUses(py[0], v.dir, v.why, reads, rest)
	return v
}

// venvRun judges a test or check program of an environment: DIR/bin/python -m pytest|unittest|py_compile|compileall, or the
// environment's pytest script. Its operands are judged as an unlisted command's are; files (py_compile, compileall) must all be inside
// the workspace.
func (ev *evaluator) venvRun(prog string, script bool, args []string, files bool, cw *cwdSet) *venvUse {
	dir, _, why := ev.ownEnv(prog, script, cw)
	v := &venvUse{dir: dir, why: why}
	v.uses = append([]pathUse{{raw: prog, noFollow: true}}, unknownUses(args)...)
	if why == "" && files {
		v.why = compiledFiles(args[2:])
	}
	if v.why == "" && files {
		for _, a := range args[2:] {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if w := ev.installFile(a, cw); w != "" {
				v.why = w
				break
			}
		}
	}
	return v
}

// installArgs is why the options and operands of an install are not ones the tests preset allows: an option that changes where
// packages come from or go, a URL or repository, or a file outside the workspace ("" when they are); and the files it reads.
func (ev *evaluator) installArgs(what string, args []string, switches, files map[string]bool, cw *cwdSet) (string, []pathUse) {
	var reads []pathUse
	operands := 0
	operand := func(a string, file bool) string {
		operands++
		if !file && !localSpec(a) && !remoteSpec(a) {
			if requirementSpec.MatchString(a) {
				return ""
			}
			return quote(a) + " is not a package name with versions"
		}
		if why := ev.installFile(a, cw); why != "" {
			return why
		}
		reads = append(reads, pathUse{raw: withoutExtras(a), tree: true, content: true})
		return ""
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, inline := strings.Cut(a, "=")
		short := len(a) > 2 && a[1] != '-' && files[a[:2]] // -rrequirements.txt
		switch {
		case a == "--":
			for _, o := range args[i+1:] {
				if why := operand(o, false); why != "" {
					return why, nil
				}
			}
			i = len(args)
		case !strings.HasPrefix(a, "-") || a == "-":
			if why := operand(a, false); why != "" {
				return why, nil
			}
		case switches[a] || shortCluster(a, "qU"):
		case files[name] || short:
			switch {
			case short && !inline:
				val = a[2:]
			case !inline:
				if i+1 >= len(args) {
					return what + " " + a + " has no value", nil
				}
				i++
				val = args[i]
			}
			if why := operand(val, true); why != "" {
				return why, nil
			}
		case installSources[name]:
			return what + " " + name + " changes where packages come from; the tests preset installs from the package index only", nil
		case installTargets[name]:
			return what + " " + name + " changes where packages are installed; the tests preset installs into the virtual environment only", nil
		default:
			return what + " " + name + " is not an option the tests preset allows", nil
		}
	}
	if operands == 0 {
		return what + " names nothing to install", nil
	}
	return "", reads
}

// withoutExtras is a local project without the extras its install names (".[dev]" is ".").
func withoutExtras(p string) string {
	if i := strings.LastIndexByte(p, '['); i > 0 && strings.HasSuffix(p, "]") {
		return p[:i]
	}
	return p
}

// installFile is why a file an install reads (a requirement file, a local project or archive) or a check is given is not one inside
// the workspace: a URL or a repository, a name only known when the command runs, or a path that resolves elsewhere.
func (ev *evaluator) installFile(p string, cw *cwdSet) string {
	if remoteSpec(p) {
		return quote(p) + " installs from a URL or a repository; the tests preset installs from the package index only"
	}
	for _, a := range ev.resolve(pathUse{raw: withoutExtras(p)}, cw) {
		switch {
		case a.dynamic:
			return quote(p) + " is only known when the command runs"
		case a.invalid != "" || a.real == "" || !ev.rs.inWorkspace(a.real):
			return quote(p) + " is outside the workspace"
		}
	}
	return ""
}

// remoteSpec reports whether an install operand names a URL or a repository: a scheme, a VCS prefix, or a direct reference
// ("name @ url").
func remoteSpec(a string) bool {
	return strings.Contains(a, "://") || strings.Contains(a, "@") || strings.HasPrefix(a, "file:") ||
		strings.HasPrefix(a, "git+") || strings.HasPrefix(a, "hg+") || strings.HasPrefix(a, "svn+") || strings.HasPrefix(a, "bzr+")
}

// localSpec reports whether an install operand is a path rather than a name: it has a "/", starts with "." or "~", or is an archive.
func localSpec(a string) bool {
	if strings.ContainsAny(a, "/\\") || strings.HasPrefix(a, ".") || strings.HasPrefix(a, "~") {
		return true
	}
	for _, ext := range []string{".whl", ".tar.gz", ".zip", ".tar.bz2", ".tgz"} {
		if strings.HasSuffix(strings.ToLower(a), ext) {
			return true
		}
	}
	return false
}

// venvDir resolves the directory of an environment in every directory the command may run in, and says why it is not one of the
// workspace: the command runs elsewhere, the name is only known when it runs, it resolves outside the workspace, or it is a workspace
// root itself (the project, an agent's tree, $TMPDIR).
func (ev *evaluator) venvDir(dir string, cw *cwdSet) (string, string) {
	if runtime.GOOS == "windows" {
		return "", "virtual environments are recognised on Unix-like systems only"
	}
	if cw.unknown || len(cw.dirs) == 0 {
		return "", "the directory the command runs in is not known"
	}
	real := ""
	for _, d := range cw.dirs {
		if !ev.rs.inWorkspace(realPath(d)) {
			return "", "it runs in " + ev.rs.display(d) + ", outside the workspace"
		}
		w, _, dyn := ev.rs.expandWordVars(dir, d, ev.expansion())
		if dyn {
			return "", quote(dir) + " is only known when the command runs"
		}
		if !filepath.IsAbs(w) {
			w = filepath.Join(d, w)
		}
		r := realPath(w)
		switch {
		case r == "" || !ev.rs.inWorkspace(r):
			return "", dir + " is outside the workspace"
		case real != "" && r != real:
			return "", dir + " names different directories in the directories the command may run in"
		}
		real = r
	}
	if ev.rs.isRoot(real) {
		return "", dir + " is a workspace root (the project, an agent's tree or $TMPDIR), not a directory of it"
	}
	return real, ""
}

// venvLayout is why the directory real (shown as written) is not a virtual environment that keeps what is installed inside it: it
// has no pyvenv.cfg, or an entry of it, of its lib directories or their site-packages leads outside it. "" when it is one.
func venvLayout(real, shown string) string {
	if fi, err := os.Lstat(filepath.Join(real, "pyvenv.cfg")); err != nil || !fi.Mode().IsRegular() {
		return shown + " is not a virtual environment (it has no pyvenv.cfg)"
	}
	escapes := func(p string) bool {
		r := realPath(p)
		return r == "" || !inside(real, r)
	}
	names, ok := listDir(real, 64)
	if !ok {
		return shown + " has too many entries to check"
	}
	for _, n := range names {
		if escapes(filepath.Join(real, n)) {
			return shown + "/" + n + " leads outside the environment"
		}
	}
	for _, lib := range []string{"lib", "lib64"} {
		vers, ok := listDir(filepath.Join(real, lib), 16)
		if !ok {
			return shown + "/" + lib + " has too many entries to check"
		}
		for _, v := range vers {
			for _, p := range []string{filepath.Join(real, lib, v), filepath.Join(real, lib, v, "site-packages")} {
				if escapes(p) {
					return strings.TrimPrefix(p, real+"/") + " of " + shown + " leads outside the environment"
				}
			}
		}
	}
	return ""
}

// listDir returns the names in dir, at most limit of them (ok false when there are more); a directory that cannot be read has none.
func listDir(dir string, limit int) ([]string, bool) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, true
	}
	defer f.Close()
	names, err := f.Readdirnames(limit + 1)
	if err != nil && err != io.EOF {
		return nil, true
	}
	return names, len(names) <= limit
}

// ownScript is why a script of an environment's bin directory (real, its entry name) does not run a python of that bin directory: it
// is a link (to another pip), not a file, or its first line runs another interpreter (a copy of an environment made elsewhere runs the
// python of the original). pip writes its python's absolute path on the first line, or, for a long one, on the second, after the
// shell's "exec". It returns that interpreter's path, which ownEnv holds to ownInterpreter.
func ownScript(real, name, shown string) (interp, why string) {
	p := filepath.Join(real, "bin", name)
	notOwn := shown + " does not run the environment's own python"
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "", notOwn
	}
	f, err := os.Open(p)
	if err != nil {
		return "", notOwn
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 4096))
	var lines []string
	for len(lines) < 2 && sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "#!") {
		return "", notOwn
	}
	interp = strings.TrimSpace(lines[0][2:])
	if interp == "/bin/sh" && len(lines) > 1 {
		if rest, ok := strings.CutPrefix(lines[1], `'''exec' "`); ok {
			interp, _, _ = strings.Cut(rest, `"`)
		}
	} else if f := strings.Fields(interp); len(f) > 0 {
		interp = f[0]
	}
	bin := realPath(filepath.Join(real, "bin"))
	if !filepath.IsAbs(interp) || shellparse.InterpreterName(filepath.Base(interp)) != "python" || realPath(filepath.Dir(interp)) != bin {
		return "", notOwn
	}
	return interp, ""
}
