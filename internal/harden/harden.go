// Package harden makes the harness process opaque to the commands it runs.
//
// The shell tool hands a model a shell: a same-user process that starts as a child
// of the harness. Scrubbing the child's environment keeps the harness's
// credentials out of `env`, but says nothing about the harness's own memory, and
// on Linux any process of the same user can read /proc/<pid>/environ (the
// environment the harness was started with, provider API key included) and
// /proc/<pid>/mem, or ptrace it: `tr '\0' '\n' </proc/$PPID/environ` is one
// command. Process closes those doors as far as the OS lets an unprivileged
// process close them:
//
//   - Linux: prctl(PR_SET_DUMPABLE, 0) makes the /proc/<pid>/* files root-owned and
//     the ptrace check fail for every other same-user process (unless it has
//     CAP_SYS_PTRACE, which root usually has); children are unaffected, execve
//     resets the flag. Before that, the values of credential-looking variables are
//     erased from the initial environment block in the process's own memory (the
//     bytes /proc/<pid>/environ serves, which os.Unsetenv never touches), so even a
//     reader with CAP_SYS_PTRACE finds NAME= and nothing behind it.
//   - macOS: ptrace(PT_DENY_ATTACH) refuses debugger attach. It does not stop
//     `ps eww` or sysctl(KERN_PROCARGS2) from reading a same-user process's
//     environment; keys must not be in the environment there (see SECURITY.md).
//   - Windows and others: nothing (see SECURITY.md).
//
// Neither is a boundary against a hostile same-user program that the harness
// itself runs; that needs a sandbox (SECURITY.md, shell.Options.Wrap). They close
// the cheap, reliable exfiltration that needs no exploit at all.
//
// # Keys out of the environment
//
// Every process the harness starts with a nil Cmd.Env (git, hooks, verifiers, MCP
// servers) inherits its environment, provider keys included. With the MoveKeys
// option Process takes the provider API keys out of the environment and holds them
// in memory; readers then use Secret instead of os.Getenv. It is an option because
// it only works once every reader of those variables has moved to Secret: a variable
// that is gone from the environment is invisible to os.Getenv.
//
// SLEIPNIR_DUMPABLE=1 leaves the process dumpable (debuggers, core dumps, reading
// /proc/<pid>/environ for debugging). It does not stop the environment erasure.
package harden

import (
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Status says what Process did. It carries no secret values.
type Status struct {
	// Platform is runtime.GOOS.
	Platform string
	// Protected is true when the OS now refuses same-user processes access to this
	// process's memory (Linux: non-dumpable; macOS: debugger attach denied).
	Protected bool
	// OptedOut is true when SLEIPNIR_DUMPABLE=1 left the process dumpable on purpose.
	OptedOut bool
	// EnvErased counts the initial-environment entries whose value was erased in
	// memory (Linux only): what /proc/<pid>/environ no longer shows.
	EnvErased int
	// Moved lists, sorted, the variables MoveKeys took out of the environment.
	Moved []string
	// Notes lists what did not work (no /proc, no permission, ...). Hardening is best
	// effort, never fatal.
	Notes []string
}

// Option changes what Process does beyond its defaults.
type Option func(*settings)

type settings struct {
	moveKeys bool
	extra    []string
}

// MoveKeys makes Process take the provider API keys (every variable whose name ends
// in API_KEY, e.g. HEIMDALL_API_KEY, OPENAI_API_KEY, BRAVE_API_KEY) and any extra
// variables named here out of the process environment: they are held in memory and
// read with Secret. Once moved, os.Getenv no longer sees them and neither does any
// process started with the inherited environment. Use it only when every reader
// goes through Secret.
func MoveKeys(extra ...string) Option {
	return func(s *settings) {
		s.moveKeys = true
		s.extra = append(s.extra, extra...)
	}
}

var (
	mu     sync.Mutex
	done   bool
	status Status
	moving bool              // MoveKeys was requested: Secret moves the credentials it reads
	named  []string          // variables MoveKeys was told are credentials whatever they look like
	held   map[string]string // credentials moved out of the environment
)

// Process hardens the running process; call it once, first thing in main, before
// anything reads keys or starts a command. It is idempotent (later calls repeat
// only what their options ask for), cheap, and never fails: what could not be done
// is in Status.Notes.
func Process(opts ...Option) Status {
	var s settings
	for _, o := range opts {
		o(&s)
	}
	mu.Lock()
	defer mu.Unlock()
	if !done {
		done = true
		status = platformHarden(dumpableRequested())
		status.Platform = runtime.GOOS
	}
	applyLocked(s)
	out := status
	out.Notes = append([]string(nil), status.Notes...)
	out.Moved = sortedHeld()
	return out
}

// applyLocked carries out the options: it switches on moving mode and takes the
// variables that are already in the environment out of it.
func applyLocked(s settings) {
	if !s.moveKeys {
		return
	}
	moving = true
	named = append(named, s.extra...)
	if held == nil {
		held = map[string]string{}
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name != "" && (providerKeyName.MatchString(name) || listed(name, s.extra)) {
			moveLocked(name)
		}
	}
}

// movableLocked reports whether Secret may take name out of the environment in
// moving mode: the variable must look like a credential (or have been named to
// MoveKeys). A caller that passes an ordinary name (PATH, HOME) by mistake gets its
// value, and the environment keeps it: moving it would break every child.
func movableLocked(name, value string) bool {
	return providerKeyName.MatchString(name) || listed(name, named) || shouldErase(name, value)
}

// dumpableRequested reports whether the operator asked to keep the process
// dumpable (SLEIPNIR_DUMPABLE=1), for debugging.
func dumpableRequested() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SLEIPNIR_DUMPABLE"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// providerKeyName matches the variables provider clients and search tools read
// their key from.
var providerKeyName = regexp.MustCompile(`(?i)api[_-]?key$`)

// listed checks for an exact name in a list without normalization.
func listed(name string, names []string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// moveLocked saves an existing environment variable in held storage and unsets it; the caller must
// hold the hardening lock.
func moveLocked(name string) {
	if v, ok := os.LookupEnv(name); ok {
		held[name] = v
		_ = os.Unsetenv(name)
	}
}

// sortedHeld returns saved environment-variable names in lexical order; the caller must
// synchronize access to held.
func sortedHeld() []string {
	names := make([]string, 0, len(held))
	for n := range held {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Secret returns the value of the credential variable name: from memory if
// MoveKeys took it out of the environment, otherwise from the environment. After
// MoveKeys, reading a variable that looks like a credential through Secret also
// moves it, so a key that only the configuration names (a custom provider's key
// variable, MYCO_TOKEN) is out of the environment from its first read; a variable
// that does not look like one is only read. Without MoveKeys it is os.Getenv.
func Secret(name string) string {
	v, _ := LookupSecret(name)
	return v
}

// LookupSecret is Secret with the "was it set" result of os.LookupEnv.
func LookupSecret(name string) (string, bool) {
	mu.Lock()
	defer mu.Unlock()
	if v, ok := os.LookupEnv(name); ok && (v != "" || held[name] == "") { // the environment wins: it may have been set again; an empty variable is not a word
		if moving && movableLocked(name, v) {
			held[name] = v
			_ = os.Unsetenv(name)
		}
		return v, true
	}
	v, ok := held[name]
	return v, ok
}

// Held lists, sorted, the names of the credentials MoveKeys took out of the
// environment. A caller that lets an operator pass such a variable on to commands
// on purpose (shell.Options.PassEnv) can read it back with Secret.
func Held() []string {
	mu.Lock()
	defer mu.Unlock()
	return sortedHeld()
}

// Provide (with an empty value: forget) makes a credential known to Secret without putting it in the environment: a key the person stored (config.LoadStoredKeys). The
// environment wins: a variable that is set is the person's word for this process, whatever was stored. Like a moved key, a provided one
// is held in memory only, so no command the harness starts can read it from its environment.
func Provide(name, value string) {
	mu.Lock()
	defer mu.Unlock()
	if value == "" { // forget it
		delete(held, name)
		return
	}
	if v, ok := os.LookupEnv(name); (ok && v != "") || name == "" {
		return
	}
	if held == nil {
		held = map[string]string{}
	}
	held[name] = value
}
