package env

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// EnvSpec describes the environment of one command run for a task.
//
// The environment is built from an allowlist, never by filtering the harness's
// own: the machine running rollouts holds provider API keys, cloud
// credentials and CI tokens, and a policy that can run `env` must not find
// any of them there (a repository file or tool result can just as well tell
// the agent to `env | curl ...`). Only what a toolchain needs to find itself
// is copied across.
type EnvSpec struct {
	// Home is the HOME of the command: a directory private to the run, so tool
	// caches and dotfiles neither leak from nor pollute the operator's home.
	Home string
	// Tmp is TMPDIR; "" means Home.
	Tmp string
	// Network says the task may use the network. Only then are proxy variables
	// and CA-bundle locations passed on, and GOPROXY is otherwise forced to
	// "off" so a build that lacks a dependency fails at once instead of hanging
	// on DNS.
	Network bool
	// Marker is exported as SLEIPNIR_ENV_RUN so stragglers can be found.
	Marker string
	// Base is the environment to draw pass-through variables from; nil means
	// os.Environ().
	Base []string
	// Deny lists directories (workspace roots) that must never be on PATH: a
	// tool the agent dropped into its own tree must not shadow a real one.
	Deny []string
	// PassEnv names extra variables (or path.Match globs such as "GOFLAGS" or
	// "ARTIFACTORY_*") to copy from Base regardless of the allowlist. This is the
	// operator's escape hatch, e.g. for a private registry token needed by setup.
	PassEnv []string
	// Set forces values last; it wins over everything. On Windows its names are matched without regard to case,
	// like Base's.
	Set map[string]string
}

// toolchainVars are copied when present: they say where an installed toolchain
// lives or how it behaves, and hold no credentials. Caches are deliberately
// absent: GOCACHE, GOMODCACHE, GOPATH, CARGO_HOME, npm and pip caches all
// default to locations under Home, which keeps a poisoned cache from crossing
// from an agent's run into the verifier's.
var toolchainVars = []string{
	"GOROOT", "GOTOOLCHAIN", "GOFLAGS", "GOSUMDB", "GONOSUMDB", "GONOSUMCHECK", "GOPRIVATE", "GONOPROXY",
	"GOEXPERIMENT", "CGO_ENABLED", "CC", "CXX", "RUSTUP_HOME", "RUSTUP_TOOLCHAIN", "JAVA_HOME",
	"VIRTUAL_ENV", "CONDA_PREFIX", "PYENV_ROOT", "NVM_DIR", "SDKROOT", "DEVELOPER_DIR",
}

// networkVars are copied only for tasks that may use the network.
var networkVars = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS",
	"GIT_SSL_CAINFO", "PIP_CERT", "CARGO_HTTP_CAINFO", "GOPROXY", "GOINSECURE",
}

// localeFor chooses a UTF-8 locale for Linux and macOS and falls back to C elsewhere.
func localeFor(goos string) string {
	switch goos {
	case "linux":
		return "C.UTF-8"
	case "darwin":
		return "en_US.UTF-8"
	}
	return "C"
}

// windowsVars are copied on Windows when present: without them processes cannot find the system (SYSTEMROOT is
// needed by sockets, crypto and Python's start-up), run a program by its bare name (PATHEXT) or start cmd.exe
// (COMSPEC). They name system locations and hold no credentials.
var windowsVars = []string{
	"SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "PATHEXT", "OS",
	"PROCESSOR_ARCHITECTURE", "PROCESSOR_IDENTIFIER", "PROCESSOR_LEVEL", "PROCESSOR_REVISION", "NUMBER_OF_PROCESSORS",
	"PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMW6432", "COMMONPROGRAMFILES", "COMMONPROGRAMFILES(X86)",
	"COMMONPROGRAMW6432", "PROGRAMDATA", "ALLUSERSPROFILE",
}

// BuildEnv returns the environment (sorted "K=V" entries) for a command.
//
// On Windows variable names are case-insensitive ("Path" is PATH), so they are matched without regard to case and
// written in upper case; the Windows system variables are passed on; TEMP and TMP point at Tmp; and USERPROFILE,
// APPDATA and LOCALAPPDATA at Home, where the toolchains' default caches then live, as they do under HOME elsewhere.
func BuildEnv(s EnvSpec) []string { return buildEnv(s, runtime.GOOS) }

// buildEnv is BuildEnv for the operating system goos.
func buildEnv(s EnvSpec, goos string) []string {
	windows := goos == "windows"
	base := map[string]string{}
	src := s.Base
	if src == nil {
		src = os.Environ()
	}
	for _, kv := range src {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			if windows {
				k = strings.ToUpper(k)
			}
			base[k] = v
		}
	}
	home := s.Home
	tmp := s.Tmp
	if tmp == "" {
		tmp = home
	}
	if home == "" {
		home = tmp
	}
	if home == "" {
		home = os.TempDir()
		tmp = home
	}

	out := map[string]string{
		"HOME":   home,
		"TMPDIR": tmp,
		"USER":   "sleipnir", "LOGNAME": "sleipnir",
		"SHELL": "/bin/sh",
		"LANG":  localeFor(goos), "LC_ALL": localeFor(goos),
		"TZ":   "UTC",
		"TERM": "dumb", "NO_COLOR": "1", "CI": "true",
		"PAGER": "cat", "GIT_PAGER": "cat",
		"GIT_TERMINAL_PROMPT": "0",
		// Git: no system or user configuration but our own, and a fixed identity so
		// `git commit` works and never reveals the operator's name.
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   filepath.Join(home, ".gitconfig"),
		"GIT_AUTHOR_NAME":     "sleipnir", "GIT_AUTHOR_EMAIL": "sleipnir@localhost",
		"GIT_COMMITTER_NAME": "sleipnir", "GIT_COMMITTER_EMAIL": "sleipnir@localhost",
		// Reproducible tool behaviour, and no stray bytecode in a diff.
		"PYTHONDONTWRITEBYTECODE": "1", "PYTHONHASHSEED": "0", "PYTHONUNBUFFERED": "1",
		"PIP_DISABLE_PIP_VERSION_CHECK": "1", "PIP_NO_INPUT": "1",
		"npm_config_update_notifier": "false", "npm_config_fund": "false", "npm_config_audit": "false",
		"DEBIAN_FRONTEND": "noninteractive",
		"GOTOOLCHAIN":     "local", // never download a toolchain mid-episode
	}
	for _, k := range toolchainVars {
		if v, ok := base[k]; ok {
			out[k] = v
		}
	}
	if windows {
		for _, k := range windowsVars {
			if v, ok := base[k]; ok {
				out[k] = v
			}
		}
		out["TEMP"], out["TMP"] = tmp, tmp
		out["USERPROFILE"] = home
		out["APPDATA"] = filepath.Join(home, "AppData", "Roaming")
		out["LOCALAPPDATA"] = filepath.Join(home, "AppData", "Local")
		out["USERNAME"] = "sleipnir"
		// Python reads and writes files in the ANSI code page unless told otherwise; elsewhere the UTF-8 locale
		// makes it use UTF-8, and this does here.
		out["PYTHONUTF8"] = "1"
	}
	if s.Network {
		for _, k := range networkVars {
			if v, ok := base[k]; ok {
				out[k] = v
			}
		}
	} else {
		out["GOPROXY"] = "off"
		out["CARGO_NET_OFFLINE"] = "true"
	}
	for _, pat := range s.PassEnv {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if windows {
			pat = strings.ToUpper(pat)
		}
		for k, v := range base {
			if k == pat {
				out[k] = v
			} else if ok, err := path.Match(pat, k); err == nil && ok {
				out[k] = v
			}
		}
	}
	out["PATH"] = strings.Join(sanitizePath(base["PATH"], s.Deny, goos, base["SYSTEMROOT"]), string(os.PathListSeparator))
	if s.Marker != "" {
		out[MarkerEnv] = s.Marker
	}
	// Set names are folded like Base's on Windows, so Set["Path"] replaces PATH instead of sitting beside it
	// (the child would get whichever spelling comes last). Sorted, so two spellings of one name resolve the
	// same way on every run.
	setKeys := make([]string, 0, len(s.Set))
	for k := range s.Set {
		setKeys = append(setKeys, k)
	}
	sort.Strings(setKeys)
	for _, k := range setKeys {
		name := k
		if windows {
			name = strings.ToUpper(k)
		}
		out[name] = s.Set[k]
	}

	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	list := make([]string, len(keys))
	for i, k := range keys {
		list[i] = k + "=" + strings.ReplaceAll(out[k], "\x00", "")
	}
	return list
}

// sanitizePath keeps the absolute, existing directories of p that nobody but
// their owner can write to and that lie outside the denied roots. A relative
// entry (including ".") would let a repository file shadow a tool; a
// world-writable entry would let any local user, including a sibling rollout,
// plant one.
//
// On Windows (goos) the mode bits say nothing: Go reports every directory as
// 0777 and access is decided by ACLs, which are not inspected, so there only
// relative, missing and denied entries are dropped, and systemRoot is the
// fallback when nothing is left.
func sanitizePath(p string, deny []string, goos, systemRoot string) []string {
	windows := goos == "windows"
	var out []string
	seen := map[string]bool{}
	for _, d := range filepath.SplitList(p) {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		d = filepath.Clean(d)
		key := d
		if windows {
			key = strings.ToLower(d)
		}
		if seen[key] || underAny(d, deny) {
			continue
		}
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() {
			continue
		}
		if !windows && fi.Mode().Perm()&0o002 != 0 {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	if len(out) == 0 {
		// A working default rather than an empty PATH, which would make the
		// shell fall back to its own built-in search path.
		defaults := []string{"/usr/local/bin", "/usr/bin", "/bin"}
		if windows && systemRoot != "" {
			defaults = []string{filepath.Join(systemRoot, "System32"), systemRoot}
		}
		for _, d := range defaults {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				out = append(out, d)
			}
		}
	}
	return out
}

// underAny compares p against cleaned root paths using native separator boundaries without
// resolving p or symlinks. On Windows the comparison ignores case, as the file system does.
func underAny(p string, roots []string) bool {
	fold := runtime.GOOS == "windows"
	if fold {
		p = strings.ToLower(p)
	}
	for _, r := range roots {
		if r == "" {
			continue
		}
		r = filepath.Clean(r)
		if fold {
			r = strings.ToLower(r)
		}
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// EnvMap converts "K=V" entries to a map, for tests and diagnostics.
func EnvMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}
