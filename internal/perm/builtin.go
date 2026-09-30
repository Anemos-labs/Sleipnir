package perm

import (
	"path"
	"strings"
)

// access is one file-system touch a request would make.
type access struct {
	raw     string // as written, for messages
	lex     string // absolute and cleaned, symlinks not followed ("" if unresolved)
	real    string // symlinks followed
	read    bool   // reads the path (or might: arguments of unknown commands)
	write   bool   // mutates the path (or might: arguments of unknown commands)
	tree    bool   // reaches everything below the path: recursive walk, delete, archive
	content bool   // reads file contents during that walk (grep -r), not just names
	dynamic bool   // could not be resolved statically
	prefix  bool   // dynamic, but lex/real hold the directory its known prefix points into
	// nameOnly: only names and metadata are looked at (ls, stat, find, a for
	// loop's word list), so files whose secrecy is in their contents are not at risk.
	nameOnly bool
	redir    bool // comes from a redirection rather than an argument
}

// tier says how firmly a built-in protection applies.
type tier int

const (
	tierNone tier = iota
	// tierGuarded paths (.env files, token files) are denied unless a user allow
	// rule names them explicitly.
	tierGuarded
	// tierHard paths (private keys, cloud credentials, system directories,
	// .git internals) are denied no matter what: no allow rule and no mode,
	// bypass included, overrides them.
	tierHard
)

type protection struct {
	tier tier
	why  string
}

// systemDirs are never written by an agent (reads are ordinary reads).
var systemDirs = []string{
	"/etc", "/usr", "/bin", "/sbin", "/boot", "/dev",
	"/lib", "/lib32", "/lib64", "/libx32", "/sys", "/proc",
}

// harmlessDevices may be written (2>/dev/null and friends).
var harmlessDevices = []string{
	"/dev/null", "/dev/zero", "/dev/full", "/dev/tty", "/dev/stdin", "/dev/stdout",
	"/dev/stderr", "/dev/random", "/dev/urandom", "/dev/fd", "/dev/shm",
}

// credential directories under $HOME, relative to it.
var credentialDirs = []string{"/.ssh", "/.aws", "/.gnupg", "/.config/gcloud"}

// files under $HOME that hold tokens; guarded rather than hard.
var guardedHome = []string{
	"/.netrc", "/.git-credentials", "/.npmrc", "/.pypirc", "/.docker/config.json",
	"/.kube/config", "/.config/gh/hosts.yml",
}

var hardSystemFiles = []string{"/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/master.passwd", "/etc/security/opasswd"}

// protect reports the strongest built-in protection covering the access. It
// looks at both the lexical and the symlink-resolved form, lowercased, so a
// link into ~/.ssh and a case-folded ".SSH" on a case-insensitive file system
// are both caught.
func (rs *resolver) protect(a access) protection {
	var best protection
	for _, f := range uniq(strings.ToLower(a.lex), strings.ToLower(a.real)) {
		if p := rs.protectForm(f, a); p.tier > best.tier {
			best = p
		}
	}
	return best
}

func (rs *resolver) display(p string) string {
	for _, h := range uniq(rs.home, rs.homeReal) {
		if h != "" && (p == h || strings.HasPrefix(p, h+"/")) {
			return "~" + p[len(h):]
		}
	}
	return p
}

func (rs *resolver) protectForm(f string, a access) protection {
	shown := rs.display(a.real)
	if shown == "" || a.prefix {
		shown = a.raw
	}
	segs := splitSegs(f)
	base := ""
	if len(segs) > 0 {
		base = segs[len(segs)-1]
	}

	for _, h := range uniq(strings.ToLower(rs.home), strings.ToLower(rs.homeReal)) {
		if h == "" || h == "/" {
			continue
		}
		for _, d := range credentialDirs {
			dir := h + d
			if inside(dir, f) {
				if d == "/.ssh" && !a.write && !a.tree && publicSSH(f, dir) {
					return protection{tierGuarded, shown + " is SSH configuration (add an allow rule to read it)"}
				}
				return protection{tierHard, "~" + d + " holds credentials: " + shown + " is protected"}
			}
			if a.tree && (a.write || a.content) && f != dir && inside(f, dir) {
				return protection{tierHard, shown + " contains ~" + d + " (credentials); name a narrower path"}
			}
		}
		for _, g := range guardedHome {
			if f == h+g {
				return protection{tierGuarded, shown + " holds access tokens (add an allow rule to use it)"}
			}
		}
	}

	// Other users' homes (/root/.ssh, /home/bob/.aws) and copies of credential
	// directories elsewhere hold the same secrets. Inside the workspace a
	// directory that happens to be called .ssh or .aws is project content.
	if !rs.inStrictWorkspace(f) {
		for i, sg := range segs {
			dir := "/" + strings.Join(segs[:i+1], "/")
			switch {
			case sg == ".ssh":
				if !a.write && !a.tree && publicSSH(f, dir) {
					return protection{tierGuarded, shown + " is SSH configuration (add an allow rule to read it)"}
				}
				return protection{tierHard, "a .ssh directory holds credentials: " + shown + " is protected"}
			case sg == ".aws" || sg == ".gnupg":
				return protection{tierHard, "a " + sg + " directory holds credentials: " + shown + " is protected"}
			case sg == ".config" && i+1 < len(segs) && segs[i+1] == "gcloud":
				return protection{tierHard, "a gcloud configuration directory holds credentials: " + shown + " is protected"}
			}
		}
	}

	if !a.nameOnly || a.write {
		if len(segs) == 3 && segs[0] == "proc" && (segs[2] == "environ" || segs[2] == "mem") {
			return protection{tierHard, shown + " exposes another process's secrets"}
		}
		for _, s := range hardSystemFiles {
			if f == s || (a.tree && (a.write || a.content) && f != s && inside(f, s)) {
				return protection{tierHard, shown + " holds system credentials"}
			}
		}
		if inside("/etc/sudoers.d", f) {
			return protection{tierHard, shown + " holds system credentials"}
		}
		if len(segs) == 3 && segs[0] == "etc" && segs[1] == "ssh" && segMatch("ssh_host_*_key", segs[2]) {
			return protection{tierHard, shown + " is a host private key"}
		}
	}

	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return protection{tierGuarded, shown + " is an .env file and may hold secrets (add an allow rule to use it)"}
	}

	if a.write {
		for _, s := range segs {
			if s == ".git" {
				return protection{tierHard, shown + " is inside .git: change repository state with git commands, not by writing its files"}
			}
		}
		for _, d := range systemDirs {
			if inside(d, f) {
				if d == "/dev" && harmlessDevice(f) {
					continue
				}
				if rs.inStrictWorkspace(f) {
					continue
				}
				return protection{tierHard, shown + " is under " + d + ", a system directory"}
			}
			if a.tree && f != d && inside(f, d) {
				return protection{tierHard, shown + " contains " + d + ", a system directory"}
			}
		}
	}
	return protection{}
}

// publicSSH reports whether f is a non-secret file directly inside ~/.ssh.
func publicSSH(f, dir string) bool {
	if f == dir {
		return true
	}
	if path.Dir(f) != dir {
		return false
	}
	b := path.Base(f)
	return strings.HasSuffix(b, ".pub") || b == "known_hosts" || b == "known_hosts.old" ||
		b == "config" || b == "authorized_keys"
}

func harmlessDevice(f string) bool {
	for _, d := range harmlessDevices {
		if inside(d, f) {
			return true
		}
	}
	return false
}

// inStrictWorkspace reports whether f lies in a workspace root that is itself
// not "/" or a system directory: a project checked out under /usr/src is still
// a project, but a root of "/" must not switch the system-directory rule off.
func (rs *resolver) inStrictWorkspace(f string) bool {
	for _, r := range rs.roots {
		l, re := strings.ToLower(r.lex), strings.ToLower(r.real)
		if l == "/" || re == "/" {
			continue
		}
		system := false
		for _, d := range systemDirs {
			system = system || l == d || re == d
		}
		if !system && (inside(l, f) || inside(re, f)) {
			return true
		}
	}
	return false
}

// dynamicSuspect looks at a word the engine cannot resolve and reports what the
// text it does have already gives away: "$DIR/.ssh/id_rsa" and "~bob/.aws/x" name
// credential directories whatever the variable holds, and a write to
// "$DIR/.git/config" is a write into .git.
func dynamicSuspect(raw string, write bool) string {
	segs := splitSegs(strings.ToLower(raw))
	for i, sg := range segs {
		switch {
		case sg == ".ssh" || sg == ".aws" || sg == ".gnupg":
			return raw + " names a " + sg + " directory (credentials)"
		case sg == ".config" && i+1 < len(segs) && segs[i+1] == "gcloud":
			return raw + " names a gcloud configuration directory (credentials)"
		case sg == ".git" && write:
			return raw + " writes inside .git"
		}
	}
	if n := len(segs); n > 0 && (segs[n-1] == ".env" || strings.HasPrefix(segs[n-1], ".env.")) {
		return raw + " names an .env file"
	}
	return ""
}
