package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// Needs: the programs and tool servers outside Sleipnir that a plugin uses
// (Blender for the Blender skills, gh for GitHub delivery). A plugin declares
// each one with where it is usually installed and how to install it; the
// Plugins page and `sleipnir plugins needs` show the missing ones with two
// actions:
//
//   - Locate: the person names the program's file. It is checked (an
//     executable file, and its version command succeeds), remembered in
//     <state>/needs.json, and linked into <state>/bin, which every shell
//     command of a session has on its PATH.
//   - Install: one of the declared install methods, run only when the person
//     asks for it, as the argv shown to them (no shell). A tool server's
//     method then adds the server to the user's configuration.
//
// Nothing here runs a program at detection time: a need is found by looking
// at files (the remembered path, PATH, the declared default locations) and,
// for a tool server, at the user's "mcp" configuration.

// Need is one program or tool server a plugin uses.
type Need struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	// Required needs make the plugin unusable while they are missing; the others
	// only limit what it can do.
	Required bool `json:"required,omitempty"`
	// Commands are the program names looked up on PATH, the main one first; the
	// others are siblings linked from the same directory (uv and uvx).
	Commands []string `json:"commands,omitempty"`
	// Paths are default install locations by operating system ("darwin",
	// "linux", "windows", or "unix" for both of the first two). ~ and ${VAR}
	// are expanded and * matches; of several matches the last in order wins.
	Paths map[string][]string `json:"paths,omitempty"`
	// VersionArgs, run on a located file, must succeed for the file to be taken.
	VersionArgs []string `json:"version_args,omitempty"`
	// MCP makes the need a tool server: it is found when the user's
	// configuration has a server of that name.
	MCP     *NeedMCP        `json:"mcp,omitempty"`
	Install []InstallMethod `json:"install,omitempty"`
}

// NeedMCP is the tool server entry a need's install adds to the user's configuration.
type NeedMCP struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// InstallMethod is one way to install a need.
type InstallMethod struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// OS limits the method to these operating systems ("unix" for darwin and linux).
	OS []string `json:"os,omitempty"`
	// Env are environment variables the method's commands get, each read from
	// the output of a command (argv): the last line it prints of the form
	// NAME=value. A variable already set in the environment is kept as it is,
	// and its command is not run.
	Env map[string][]string `json:"env,omitempty"`
	// Setup are commands, as argv, run in order before Run, such as adding the
	// package source Run installs from. Each must be safe to run again (an
	// add with --if-not-exists), since a retried install repeats them.
	Setup [][]string `json:"setup,omitempty"`
	// Run is the command, as argv; its first word is looked up on PATH.
	Run []string `json:"run,omitempty"`
	// Connect adds the need's tool server to the user's configuration after Run.
	Connect bool   `json:"connect,omitempty"`
	Note    string `json:"note,omitempty"`
}

// NeedFile is the store of located needs under the state directory.
const NeedFile = "needs.json"

// NeedBin is the directory under the state directory that holds the links to
// needs found outside PATH; sessions put it in front of their commands' PATH.
func NeedBin(state string) string { return filepath.Join(state, "bin") }

// validNeed checks a declaration from a plugin's manifest.
func validNeed(n Need) error {
	if !ValidID(n.ID) || strings.Contains(n.ID, ":") {
		return fmt.Errorf("need id %q is not valid", n.ID)
	}
	if len(n.Commands) == 0 && n.MCP == nil {
		return fmt.Errorf("need %s: declare commands or an mcp server", n.ID)
	}
	for _, c := range n.Commands {
		if c == "" || strings.ContainsAny(c, `/\ `) {
			return fmt.Errorf("need %s: command %q is not a bare program name", n.ID, c)
		}
	}
	if n.MCP != nil && (n.MCP.Name == "" || n.MCP.Command == "") {
		return fmt.Errorf("need %s: the mcp server needs a name and a command", n.ID)
	}
	for _, m := range n.Install {
		if !ValidID(m.ID) {
			return fmt.Errorf("need %s: install method id %q is not valid", n.ID, m.ID)
		}
		if len(m.Run) == 0 && !m.Connect {
			return fmt.Errorf("need %s: install method %s runs nothing", n.ID, m.ID)
		}
		for _, step := range m.Setup {
			if len(step) == 0 || step[0] == "" {
				return fmt.Errorf("need %s: install method %s has an empty setup command", n.ID, m.ID)
			}
		}
		for name, argv := range m.Env {
			if !validEnvName(name) {
				return fmt.Errorf("need %s: install method %s: %q is not an environment variable name", n.ID, m.ID, name)
			}
			if len(argv) == 0 || argv[0] == "" {
				return fmt.Errorf("need %s: install method %s has an empty command for %s", n.ID, m.ID, name)
			}
		}
		if m.Connect && n.MCP == nil {
			return fmt.Errorf("need %s: install method %s connects, but the need has no mcp server", n.ID, m.ID)
		}
	}
	return nil
}

// OwnedNeed is a need with the plugin that declared it.
type OwnedNeed struct {
	Need
	Plugin string `json:"plugin"`
}

// Needs lists the needs of the plugins that are on in set, by id; a need two
// plugins declare is kept once, as the first in plugin order declared it.
func Needs(set *Set) []OwnedNeed {
	var out []OwnedNeed
	seen := map[string]bool{}
	for _, st := range set.List() {
		if !st.Enabled {
			continue
		}
		for _, n := range st.Manifest.Needs {
			if !seen[n.ID] {
				seen[n.ID] = true
				out = append(out, OwnedNeed{Need: n, Plugin: st.ID})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// NeedEnv is where needs are looked for: the state directory, the user's
// configuration and the PATH of the process.
type NeedEnv struct {
	State string // the state directory (session.StateRoot)
	Home  string // the home whose .sleipnir/config.json holds tool servers
	GOOS  string // default runtime.GOOS
	PATH  string // default $PATH
	// SkipDefaults leaves the declared default locations out of the search.
	SkipDefaults bool
}

func (e NeedEnv) goos() string {
	if e.GOOS != "" {
		return e.GOOS
	}
	return runtime.GOOS
}

// rawPath is the PATH searched for the needs themselves: without <state>/bin, whose launchers only repeat what was found.
func (e NeedEnv) rawPath() string {
	if e.PATH != "" {
		return e.PATH
	}
	return os.Getenv("PATH")
}

// path is the PATH commands run with: <state>/bin first.
func (e NeedEnv) path() string {
	if e.State == "" {
		return e.rawPath()
	}
	return NeedBin(e.State) + string(os.PathListSeparator) + e.rawPath()
}

// NeedStatus is a need as found on this machine.
type NeedStatus struct {
	OwnedNeed
	Found bool `json:"found"`
	// Path is the program's file, or for a tool server its configured command.
	Path string `json:"path,omitempty"`
	// Via says how it was found: "located", "path", "default" or "config".
	Via     string         `json:"via,omitempty"`
	Version string         `json:"version,omitempty"`
	Methods []MethodStatus `json:"methods"`
}

// MethodStatus is an install method as it can run here.
type MethodStatus struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Env are the variables the commands get, with the command each is read from.
	Env map[string][]string `json:"env,omitempty"`
	// Setup are the commands run before Argv, in order.
	Setup     [][]string `json:"setup,omitempty"`
	Argv      []string   `json:"argv,omitempty"`
	Connect   string     `json:"connect,omitempty"` // the server entry it adds, in words
	Note      string     `json:"note,omitempty"`
	Available bool       `json:"available"`
	Reason    string     `json:"reason,omitempty"`
}

// located is <state>/needs.json.
type located struct {
	Located map[string]locatedEntry `json:"located"`
}

type locatedEntry struct {
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
}

var needMu sync.Mutex // one write of needs.json and <state>/bin at a time in this process

func readLocated(state string) located {
	var l located
	if b, err := os.ReadFile(filepath.Join(state, NeedFile)); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	if l.Located == nil {
		l.Located = map[string]locatedEntry{}
	}
	return l
}

func writeLocated(state string, l located) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(state, ".needs-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(state, NeedFile))
}

// CheckNeeds finds each need, in order: the located file, PATH (which includes
// <state>/bin), then the default locations; a tool server in the user's configuration.
func CheckNeeds(env NeedEnv, needs []OwnedNeed) []NeedStatus {
	loc := readLocated(env.State)
	servers := configuredServers(env.Home)
	out := make([]NeedStatus, 0, len(needs))
	for _, n := range needs {
		st := NeedStatus{OwnedNeed: n, Methods: []MethodStatus{}}
		if n.MCP != nil {
			if cmd, ok := servers[n.MCP.Name]; ok {
				st.Found, st.Via, st.Path = true, "config", cmd
			}
		} else if p, via, ver := findNeed(env, n.Need, loc); p != "" {
			st.Found, st.Path, st.Via, st.Version = true, p, via, ver
		}
		for _, m := range n.Install {
			if ms, ok := methodStatus(env, n, m, needs); ok {
				st.Methods = append(st.Methods, ms)
			}
		}
		out = append(out, st)
	}
	return out
}

// findNeed is the file of a program need and how it was found.
func findNeed(env NeedEnv, n Need, loc located) (path, via, version string) {
	if e, ok := loc.Located[n.ID]; ok && isExecutable(env.goos(), e.Path) {
		return e.Path, "located", e.Version
	}
	if p := lookIn(env.goos(), n.Commands[0], env.rawPath()); p != "" {
		return p, "path", ""
	}
	if env.SkipDefaults {
		return "", "", ""
	}
	if p := defaultPath(env.goos(), n); p != "" {
		return p, "default", ""
	}
	return "", "", ""
}

// defaultPath is the last existing executable of the need's default locations for goos.
func defaultPath(goos string, n Need) string {
	pats := append([]string{}, n.Paths[goos]...)
	if goos != "windows" {
		pats = append(pats, n.Paths["unix"]...)
	}
	home, _ := os.UserHomeDir()
	for _, pat := range pats {
		p, ok := expandPath(pat, home)
		if !ok {
			continue
		}
		matches, _ := filepath.Glob(p)
		sort.Strings(matches)
		for i := len(matches) - 1; i >= 0; i-- {
			if isExecutable(goos, matches[i]) {
				return matches[i]
			}
		}
	}
	return ""
}

// expandPath expands ~ and ${VAR} in a declared location; a variable that is not set drops the location.
func expandPath(p, home string) (string, bool) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home == "" {
			return "", false
		}
		p = home + p[1:]
	}
	ok := true
	p = os.Expand(p, func(v string) string {
		val := os.Getenv(v)
		if val == "" {
			ok = false
		}
		return val
	})
	return filepath.FromSlash(p), ok && filepath.IsAbs(filepath.FromSlash(p))
}

// lookIn finds a program in a PATH list, as exec.LookPath does but with a given list.
func lookIn(goos, name, pathList string) string {
	exts := []string{""}
	if goos == "windows" {
		exts = []string{".exe", ".cmd", ".bat", ".com", ""}
	}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			p := filepath.Join(dir, name+ext)
			if isExecutable(goos, p) {
				return p
			}
		}
	}
	return ""
}

// isExecutable reports whether p is a regular file that can run.
func isExecutable(goos, p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if goos == "windows" {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".exe", ".cmd", ".bat", ".com":
			return true
		}
		return false
	}
	return fi.Mode()&0o111 != 0
}

// configuredServers are the user's tool servers by name, with their command.
func configuredServers(home string) map[string]string {
	out := map[string]string{}
	if home == "" {
		return out
	}
	b, err := os.ReadFile(config.UserConfigPath(home))
	if err != nil {
		return out
	}
	var doc struct {
		MCP map[string]struct {
			Command string `json:"command"`
			URL     string `json:"url"`
		} `json:"mcp"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return out
	}
	for name, s := range doc.MCP {
		out[name] = s.Command
		if s.Command == "" {
			out[name] = s.URL
		}
	}
	return out
}

// methodStatus is a method as it can run on env, false when it is not for this operating system.
func methodStatus(env NeedEnv, n OwnedNeed, m InstallMethod, all []OwnedNeed) (MethodStatus, bool) {
	if !forOS(env.goos(), m.OS) {
		return MethodStatus{}, false
	}
	ms := MethodStatus{ID: m.ID, Title: m.Title, Argv: append([]string{}, m.Run...), Note: m.Note, Available: true}
	for _, step := range m.Setup {
		ms.Setup = append(ms.Setup, append([]string{}, step...))
	}
	for name, argv := range m.Env {
		if ms.Env == nil {
			ms.Env = map[string][]string{}
		}
		ms.Env[name] = append([]string{}, argv...)
	}
	if m.Connect {
		ms.Connect = fmt.Sprintf("adds the %q tool server (%s) to your configuration", n.MCP.Name, strings.Join(append([]string{n.MCP.Command}, n.MCP.Args...), " "))
	}
	for _, prog := range runsFirst(n.Need, m) {
		if lookIn(env.goos(), prog, env.path()) == "" {
			ms.Available, ms.Reason = false, "needs "+prog+", which is not installed"
			for _, o := range all {
				if o.MCP == nil && contains(o.Commands, prog) {
					ms.Reason = "needs " + o.Title + " first"
				}
			}
			break
		}
	}
	return ms, true
}

// runsFirst are the programs a method needs on PATH: its setup and own commands and, when it connects, the server's.
func runsFirst(n Need, m InstallMethod) []string {
	var out []string
	for _, name := range EnvNames(m.Env) {
		if argv := m.Env[name]; !contains(out, argv[0]) {
			out = append(out, argv[0])
		}
	}
	for _, step := range m.Setup {
		if len(step) > 0 && !contains(out, step[0]) {
			out = append(out, step[0])
		}
	}
	if len(m.Run) > 0 && !contains(out, m.Run[0]) {
		out = append(out, m.Run[0])
	}
	if m.Connect && !filepath.IsAbs(n.MCP.Command) {
		out = append(out, n.MCP.Command)
	}
	return out
}

func forOS(goos string, list []string) bool {
	if len(list) == 0 {
		return true
	}
	for _, o := range list {
		if o == goos || (o == "unix" && goos != "windows") {
			return true
		}
	}
	return false
}

// LocateNeed checks that p is the need's program and remembers it: p must be an
// absolute path to an executable file (on macOS an .app bundle is followed to
// its program), and the need's version command must succeed on it.
func LocateNeed(ctx context.Context, env NeedEnv, n Need, p string) (NeedStatus, error) {
	if n.MCP != nil {
		return NeedStatus{}, errors.New(n.Title + " is a tool server: install or connect it instead")
	}
	if !filepath.IsAbs(p) {
		return NeedStatus{}, errors.New("give the full path of the program")
	}
	// The path is recorded first and the record is what is checked, run and linked: a file that fails the check
	// leaves the earlier record as it was.
	needMu.Lock()
	defer needMu.Unlock()
	l := readLocated(env.State)
	prev, had := l.Located[n.ID]
	l.Located[n.ID] = locatedEntry{Path: filepath.Clean(p)}
	if err := writeLocated(env.State, l); err != nil {
		return NeedStatus{}, err
	}
	st, err := verifyLocated(ctx, env, n)
	if err != nil {
		l = readLocated(env.State)
		if had {
			l.Located[n.ID] = prev
		} else {
			delete(l.Located, n.ID)
		}
		_ = writeLocated(env.State, l)
	}
	return st, err
}

// verifyLocated checks the recorded file of n (an executable, not one of Sleipnir's launchers; on macOS an .app bundle
// is followed to its program; its version command succeeds), records what it found and links it into <state>/bin.
// The caller holds needMu.
func verifyLocated(ctx context.Context, env NeedEnv, n Need) (NeedStatus, error) {
	l := readLocated(env.State)
	p := l.Located[n.ID].Path
	if fi, err := os.Stat(p); err == nil && fi.IsDir() && strings.HasSuffix(p, ".app") {
		if inner := appProgram(p); inner != "" {
			p = inner
		}
	}
	if !isExecutable(env.goos(), p) {
		return NeedStatus{}, fmt.Errorf("%s is not a program file that can run", p)
	}
	if env.State != "" && strings.HasPrefix(p, NeedBin(env.State)+string(os.PathSeparator)) {
		return NeedStatus{}, fmt.Errorf("%s is Sleipnir's own launcher: give the program it starts", p)
	}
	version := ""
	if len(n.VersionArgs) > 0 {
		v, err := runVersion(ctx, p, n.VersionArgs)
		if err != nil {
			return NeedStatus{}, fmt.Errorf("%s does not look like %s: %v", p, n.Title, err)
		}
		version = v
	}
	l.Located[n.ID] = locatedEntry{Path: p, Version: version}
	if err := writeLocated(env.State, l); err != nil {
		return NeedStatus{}, err
	}
	if err := link(env.State, env.goos(), n, p); err != nil {
		return NeedStatus{}, err
	}
	return NeedStatus{OwnedNeed: OwnedNeed{Need: n}, Found: true, Path: p, Via: "located", Version: version, Methods: []MethodStatus{}}, nil
}

// ForgetNeed drops a located path and its links.
func ForgetNeed(env NeedEnv, n Need) error {
	needMu.Lock()
	defer needMu.Unlock()
	l := readLocated(env.State)
	delete(l.Located, n.ID)
	if err := writeLocated(env.State, l); err != nil {
		return err
	}
	for _, c := range n.Commands {
		_ = os.Remove(shimPath(env.State, env.goos(), c))
	}
	return nil
}

// appProgram is the program inside a macOS application bundle.
func appProgram(app string) string {
	want := strings.TrimSuffix(filepath.Base(app), ".app")
	p := filepath.Join(app, "Contents", "MacOS", want)
	if isExecutable("darwin", p) {
		return p
	}
	return ""
}

// runVersion runs p with args and returns the first line it printed.
func runVersion(ctx context.Context, p string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s failed (%v)", filepath.Base(p), strings.Join(args, " "), err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n")
	if len(line) > 120 {
		line = line[:120]
	}
	return strings.TrimSpace(line), nil
}

// PrepareNeeds links each program need of set that was found outside PATH into
// <state>/bin and returns that directory; a session puts it in front of its
// commands' PATH, so agents call `blender` by name. The directory is returned
// even while it is empty or missing, so a program located or installed while
// the session runs is on its agents' PATH at once, without a restart.
func PrepareNeeds(state string, set *Set) string {
	if state == "" {
		return ""
	}
	env := NeedEnv{State: state}
	needMu.Lock()
	for _, st := range CheckNeeds(env, Needs(set)) {
		switch {
		case st.Found && (st.Via == "default" || st.Via == "located"):
			_ = link(state, env.goos(), st.Need, st.Path)
		case !st.Found || st.Via == "path":
			for _, c := range st.Commands { // a launcher whose program went away, or one PATH now has itself
				_ = os.Remove(shimPath(state, env.goos(), c))
			}
		}
	}
	needMu.Unlock()
	return NeedBin(state)
}

// shimPath is the link of one command in <state>/bin.
func shimPath(state, goos, command string) string {
	if goos == "windows" {
		return filepath.Join(NeedBin(state), command+".cmd")
	}
	return filepath.Join(NeedBin(state), command)
}

// link writes a small launcher for each of the need's commands found next to
// target (the main command is target itself). A launcher, not a symbolic link,
// so that programs that find their files from their own path (Blender inside
// its application bundle) see the real one.
func link(state, goos string, n Need, target string) error {
	if len(n.Commands) == 0 {
		return nil
	}
	if err := os.MkdirAll(NeedBin(state), 0o755); err != nil {
		return err
	}
	for i, c := range n.Commands {
		t := target
		if i > 0 {
			if t = lookIn(goos, c, filepath.Dir(target)); t == "" {
				continue
			}
		}
		var body string
		if goos == "windows" {
			body = "@echo off\r\n\"" + t + "\" %*\r\n"
		} else {
			body = "#!/bin/sh\nexec '" + strings.ReplaceAll(t, "'", `'\''`) + "' \"$@\"\n"
		}
		p := shimPath(state, goos, c)
		if b, err := os.ReadFile(p); err == nil && string(b) == body {
			continue
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, []byte(body), 0o755); err != nil {
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			return err
		}
	}
	return nil
}

// InstallNeed runs one install method of n, writing what it prints to out, and
// then, for a method that connects, adds the tool server to the user's
// configuration. It must only run because the person asked for that method.
func InstallNeed(ctx context.Context, env NeedEnv, n Need, methodID string, out io.Writer) (NeedStatus, error) {
	var m *InstallMethod
	for i := range n.Install {
		if n.Install[i].ID == methodID {
			m = &n.Install[i]
		}
	}
	if m == nil {
		return NeedStatus{}, fmt.Errorf("%s has no install method %q", n.Title, methodID)
	}
	ms, ok := methodStatus(env, OwnedNeed{Need: n}, *m, nil)
	if !ok {
		return NeedStatus{}, fmt.Errorf("%s is not for this operating system", m.Title)
	}
	if !ms.Available {
		return NeedStatus{}, errors.New(ms.Reason)
	}
	vars, err := methodEnv(ctx, env, m.Env, out)
	if err != nil {
		return NeedStatus{}, err
	}
	for _, step := range m.Setup {
		if err := runStep(ctx, env, step, vars, out); err != nil {
			return NeedStatus{}, err
		}
	}
	if len(m.Run) > 0 {
		if err := runStep(ctx, env, m.Run, vars, out); err != nil {
			return NeedStatus{}, err
		}
	}
	if m.Connect {
		command := n.MCP.Command
		if p := lookIn(env.goos(), command, env.path()); p != "" {
			command = p // the server starts with the harness's PATH, which may not have <state>/bin
		}
		entry := map[string]any{"command": command}
		if len(n.MCP.Args) > 0 {
			entry["args"] = n.MCP.Args
		}
		if err := config.Save(config.UserConfigPath(env.Home), map[string]any{"mcp": map[string]any{n.MCP.Name: entry}}); err != nil {
			return NeedStatus{}, fmt.Errorf("adding the %s tool server: %v", n.MCP.Name, err)
		}
		fmt.Fprintf(out, "added the %q tool server to %s; it starts with your next session\n", n.MCP.Name, config.UserConfigPath(env.Home))
	}
	st := CheckNeeds(env, []OwnedNeed{{Need: n}})[0]
	if !st.Found {
		return st, fmt.Errorf("the install finished, but %s is still not found: use Locate to point at it", n.Title)
	}
	if st.Via == "default" {
		needMu.Lock()
		_ = link(env.State, env.goos(), n, st.Path)
		needMu.Unlock()
	}
	return st, nil
}

// runStep runs one command of an install with vars (NAME=value) added to its
// environment, echoing it and writing what it prints to out.
func runStep(ctx context.Context, env NeedEnv, argv, vars []string, out io.Writer) error {
	prog := lookIn(env.goos(), argv[0], env.path())
	if prog == "" {
		return fmt.Errorf("%s is not installed", argv[0])
	}
	fmt.Fprintf(out, "$ %s\n", strings.Join(argv, " "))
	cmd := exec.CommandContext(ctx, prog, argv[1:]...)
	cmd.Env = append(append(os.Environ(), "PATH="+env.path()), vars...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %v", strings.Join(argv, " "), err)
	}
	return nil
}

// methodEnv works out a method's environment variables (InstallMethod.Env) as
// NAME=value, in name order; one already set in the environment is kept.
func methodEnv(ctx context.Context, env NeedEnv, decl map[string][]string, out io.Writer) ([]string, error) {
	var vars []string
	for _, name := range EnvNames(decl) {
		if v, ok := os.LookupEnv(name); ok && v != "" {
			fmt.Fprintf(out, "using %s=%s from the environment\n", name, v)
			continue
		}
		var got bytes.Buffer
		if err := runStep(ctx, env, decl[name], nil, io.MultiWriter(out, &got)); err != nil {
			return nil, fmt.Errorf("working out %s: %w", name, err)
		}
		value := ""
		for _, line := range strings.Split(got.String(), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), name+"="); ok {
				value = v
			}
		}
		if value == "" {
			return nil, fmt.Errorf("working out %s: %s printed no %s= line; set %s yourself and try again", name, strings.Join(decl[name], " "), name, name)
		}
		fmt.Fprintf(out, "using %s=%s\n", name, value)
		vars = append(vars, name+"="+value)
	}
	return vars, nil
}

// EnvNames are the names of a method's environment variables, sorted.
func EnvNames(decl map[string][]string) []string {
	names := make([]string, 0, len(decl))
	for name := range decl {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// validEnvName reports whether name is a portable environment variable name.
func validEnvName(name string) bool {
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for _, c := range name {
		if !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// FindNeed is the need of id among needs.
func FindNeed(needs []OwnedNeed, id string) (OwnedNeed, bool) {
	for _, n := range needs {
		if n.ID == id {
			return n, true
		}
	}
	return OwnedNeed{}, false
}
