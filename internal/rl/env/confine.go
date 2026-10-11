package env

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Confinement modes (WorkspaceOptions.Confine, `--confine`).
const (
	// ConfineOff runs commands with the filesystem view of the harness (the
	// default: unchanged behaviour outside training-data generation).
	ConfineOff = "off"
	// ConfineBwrap runs every setup, agent and verifier command in a bubblewrap
	// mount namespace (see Confinement) and refuses to start where that is
	// unavailable.
	ConfineBwrap = "bwrap"
)

// ErrConfinementUnavailable is returned when confinement was required but
// bubblewrap cannot create a sandbox on this host.
var ErrConfinementUnavailable = errors.New("command confinement unavailable")

// Confinement keeps a command to its own files. The command sees the whole
// filesystem read-only, with the directories that hold other people's and
// other runs' data replaced by empty tmpfs mounts (the defaults below plus the
// work root, the task sources, the blob store and the run output, see Hide),
// a few secret files masked, its own PID, IPC and UTS namespaces (it cannot see
// or signal the harness, whose environment holds provider keys), and, unless
// the task needs the network, its own network namespace with loopback up.
// Writable are only the directories a Mounts names (the command's tree, HOME
// and TMPDIR) and the caches its environment points at; toolchain directories
// on its PATH and named by its environment are visible read-only even under a
// hidden directory.
//
// Without confinement an agent's shell (python3 -c ..., which no static
// permission check can judge) and the code a verifier runs could read the
// hidden test files and reference solutions in the blob store, the full history
// of mined repositories, other rollouts' workspaces and the harness's own
// environment.
type Confinement struct {
	// Available reports that bubblewrap works here; Reason says why not.
	Available bool
	Reason    string
	// Bwrap is the bubblewrap binary.
	Bwrap string
	// Hide are directories replaced by an empty tmpfs, Mask files and
	// directories made empty; RO and RW are directories re-exposed read-only or
	// writable for every command (operator-configured toolchains and caches).
	Hide, Mask, RO, RW []string
	// protected are hidden directories that nothing may re-expose, not even a
	// PATH entry or a cache variable: the work root, the task sources, the blob
	// store and the run output. A Mounts entry inside them is allowed (the
	// command's own tree lives in the work root).
	protected []string
}

// Mounts are the directories one command may use beyond the read-only view:
// its own tree, HOME and TMPDIR (writable) and, for a workspace exported from
// a snapshot, the snapshot's object store that the workspace's git reads
// through alternates (read-only).
type Mounts struct {
	RW, RO []string
}

// DefaultHidden are the directories every confined command sees empty: user
// homes (where the work root, campaign data and secrets usually live),
// temporary and runtime directories (sockets, other processes' files) and
// service state.
var DefaultHidden = []string{"/home", "/root", "/tmp", "/var/tmp", "/run", "/var/lib", "/var/log", "/var/spool", "/var/mail",
	"/var/cache", "/srv", "/mnt", "/media"}

// DefaultMasked are secrets a command run as root could otherwise read through
// the read-only root.
var DefaultMasked = []string{"/etc/shadow", "/etc/shadow-", "/etc/gshadow", "/etc/gshadow-", "/etc/sudoers", "/etc/sudoers.d",
	"/etc/ssh", "/etc/ssl/private"}

// toolchainROEnv and cacheRWEnv name environment variables whose directories a
// confined command needs: toolchain roots read-only, caches writable.
var (
	toolchainROEnv = []string{"GOROOT", "RUSTUP_HOME", "JAVA_HOME"}
	cacheRWEnv     = []string{"GOCACHE", "GOMODCACHE", "GOPATH", "CARGO_HOME", "PIP_CACHE_DIR", "npm_config_cache",
		"YARN_CACHE_FOLDER", "UV_CACHE_DIR", "XDG_CACHE_HOME"}
)

// ConfineOptions configures DetectConfinement.
type ConfineOptions struct {
	// Protect are directories that hold data commands must never see (task
	// sources, blob store, run output); they are hidden and never re-exposed.
	Protect []string
	// RO and RW re-expose directories for every command.
	RO, RW []string
	// LookPath and Probe let tests substitute the host.
	LookPath func(string) (string, error)
	Probe    func(ctx context.Context, name string, args ...string) error
}

// DetectConfinement probes bubblewrap once. Everything that goes wrong becomes
// a Reason; the caller decides whether that is fatal (ConfineBwrap) or not.
func DetectConfinement(o ConfineOptions) Confinement {
	c := Confinement{Hide: append([]string(nil), DefaultHidden...), Mask: append([]string(nil), DefaultMasked...)}
	for _, p := range o.Protect {
		if p = absOrEmpty(p); p != "" {
			c.protected = append(c.protected, p)
			c.Hide = append(c.Hide, p)
		}
	}
	for _, p := range o.RO {
		if p = absOrEmpty(p); p != "" {
			c.RO = append(c.RO, p)
		}
	}
	for _, p := range o.RW {
		if p = absOrEmpty(p); p != "" {
			c.RW = append(c.RW, p)
		}
	}
	if runtime.GOOS != "linux" {
		c.Reason = "only implemented on Linux, not " + runtime.GOOS
		return c
	}
	look := o.LookPath
	if look == nil {
		look = exec.LookPath
	}
	probe := o.Probe
	if probe == nil {
		probe = runProbe
	}
	bw, err := lookAny(look, "bwrap")
	if err != nil {
		c.Reason = "bubblewrap (bwrap) is not installed"
		return c
	}
	c.Bwrap = bw
	argv := c.argv(Mounts{}, false, nil, []string{"/bin/sh", "-c", "exit 0"})
	if err := probe(context.Background(), argv[0], argv[1:]...); err != nil {
		c.Reason = fmt.Sprintf("bubblewrap cannot create a sandbox here: %v", err)
		return c
	}
	c.Available = true
	return c
}

// absOrEmpty returns the cleaned absolute form of p, or "" for an empty path.
func absOrEmpty(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	return filepath.Clean(a)
}

// bind is one re-exposed directory.
type bind struct {
	path string
	rw   bool
}

// argv returns the bubblewrap command line that runs inner confined (inner nil
// gives a prefix ending in "--", to which a caller appends its own command).
// env is the command's environment: its PATH entries and toolchain and cache
// variables decide what is re-exposed.
func (c Confinement) argv(m Mounts, network bool, env []string, inner []string) []string {
	a := []string{c.Bwrap, "--die-with-parent", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc"}
	if !network {
		a = append(a, "--unshare-net")
	}
	for _, h := range sortByDepth(existing(c.Hide)) {
		a = append(a, "--tmpfs", h)
	}
	for _, p := range c.Mask {
		fi, err := os.Stat(p)
		switch {
		case err != nil:
		case fi.IsDir():
			a = append(a, "--tmpfs", p)
		default:
			a = append(a, "--ro-bind", "/dev/null", p)
		}
	}
	if network {
		// /run is hidden; name resolution on systemd hosts lives there.
		for _, p := range []string{"/run/systemd/resolve"} {
			if dirExists(p) {
				a = append(a, "--ro-bind", p, p)
			}
		}
	}
	for _, b := range c.binds(m, env) {
		flag := "--ro-bind"
		if b.rw {
			flag = "--bind"
		}
		a = append(a, flag, b.path, b.path)
	}
	a = append(a, "--")
	return append(a, inner...)
}

// binds collects the re-exposed directories, deepest last so a writable
// directory inside a read-only one (or the reverse) gets its own access.
func (c Confinement) binds(m Mounts, env []string) []bind {
	seen := map[string]int{}
	var out []bind
	add := func(p string, rw, explicit bool) {
		p = absOrEmpty(p)
		if p == "" || p == "/" || !dirExists(p) {
			return
		}
		if !explicit && c.exposesProtected(p) {
			return
		}
		if i, ok := seen[p]; ok {
			out[i].rw = out[i].rw || rw
			return
		}
		seen[p] = len(out)
		out = append(out, bind{path: p, rw: rw})
	}
	for _, p := range m.RO {
		add(p, false, true)
	}
	for _, p := range m.RW {
		add(p, true, true)
	}
	for _, p := range c.RO {
		add(p, false, false)
	}
	for _, p := range c.RW {
		add(p, true, false)
	}
	vars := envMap(env)
	for _, p := range filepath.SplitList(vars["PATH"]) {
		if c.hidden(p) {
			add(p, false, false)
		}
	}
	for _, k := range toolchainROEnv {
		if v := vars[k]; v != "" && c.hidden(v) {
			add(v, false, false)
		}
	}
	for _, k := range cacheRWEnv {
		if v := vars[k]; v != "" && c.hidden(v) {
			add(v, true, false)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return depth(out[i].path) < depth(out[j].path) })
	return out
}

// hidden reports whether p lies in (or is) a hidden directory.
func (c Confinement) hidden(p string) bool {
	p = absOrEmpty(p)
	for _, h := range c.Hide {
		if within(h, p) {
			return true
		}
	}
	return false
}

// exposesProtected reports whether re-exposing p would show a protected
// directory or anything in it: p is one, lies in one or contains one.
func (c Confinement) exposesProtected(p string) bool {
	for _, q := range c.protected {
		if within(q, p) || within(p, q) {
			return true
		}
	}
	return false
}

// within reports whether p is dir or lies under it.
func within(dir, p string) bool {
	if dir == p {
		return true
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// existing keeps the paths that exist.
func existing(ps []string) []string {
	var out []string
	for _, p := range ps {
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// sortByDepth orders paths parents first (deterministic within a depth).
func sortByDepth(ps []string) []string {
	out := append([]string(nil), ps...)
	sort.SliceStable(out, func(i, j int) bool {
		if di, dj := depth(out[i]), depth(out[j]); di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	return out
}

// depth counts the path's components.
func depth(p string) int { return strings.Count(filepath.Clean(p), string(filepath.Separator)) }

// dirExists reports whether p is a directory.
func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// envMap turns KEY=VALUE pairs into a map; later entries win.
func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}
