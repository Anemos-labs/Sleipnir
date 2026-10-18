package config

import (
	"fmt"
	"path"
	"strings"
)

// PushTarget is where a worktree-isolated team's verified work is pushed after every
// merge (swarm.push_to, --push-to). The zero value is the default: the checkout's
// push remote, the harness's own branch, the project at the root.
type PushTarget struct {
	// Remote is a remote's name or a repository URL; "" is the checkout's push remote.
	Remote string
	// Branch is the target branch, without refs/heads/; "" is the harness's own
	// branch, sleipnir/<session>/_integration.
	Branch string
	// Dir is the folder of the target repository the project lands in, slash
	// separated and clean; "" is the root (the integration commits are pushed as
	// they are).
	Dir string
}

// IsZero reports whether t is the default target.
func (t PushTarget) IsZero() bool { return t == PushTarget{} }

// String is t in the form ParsePushTarget reads.
func (t PushTarget) String() string {
	s := t.Remote
	if t.Branch != "" || t.Dir != "" {
		s += "#" + t.Branch
	}
	if t.Dir != "" {
		s += ":" + t.Dir
	}
	return s
}

// ParsePushTarget reads "<remote or URL>[#<branch>][:<dir>]", the form docker build
// takes for a git context: "https://github.com/o/monorepo.git#build/app:web-apps/app"
// pushes to the branch build/app of that repository with the project in
// web-apps/app/. A URL's own colons come before the "#", and a branch name cannot hold
// one, so the first colon after the "#" starts the dir. "" is the default target. The
// remote may be left out ("#build/app" is the checkout's push remote), a branch name
// must be one git accepts, and the dir must be a relative path inside the repository.
func ParsePushTarget(s string) (PushTarget, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return PushTarget{}, nil
	}
	var t PushTarget
	t.Remote, s, _ = strings.Cut(s, "#")
	t.Branch, t.Dir, _ = strings.Cut(s, ":")
	if strings.HasPrefix(t.Remote, "-") || strings.ContainsAny(t.Remote, " \t\n") {
		return PushTarget{}, fmt.Errorf("push target %q: the remote must be a remote's name or a repository URL", t.Remote)
	}
	if t.Branch != "" && !validBranch(t.Branch) {
		return PushTarget{}, fmt.Errorf("push target: %q is not a branch name git accepts", t.Branch)
	}
	if t.Dir != "" {
		d := strings.Trim(t.Dir, "/")
		clean := path.Clean(d)
		if strings.HasPrefix(t.Dir, "/") || clean != d || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsAny(clean, "\\\n\t") {
			return PushTarget{}, fmt.Errorf("push target: the folder %q must be a relative path inside the repository, like web-apps/app", t.Dir)
		}
		for _, part := range strings.Split(clean, "/") {
			if part == ".git" {
				return PushTarget{}, fmt.Errorf("push target: the folder %q is inside .git", t.Dir)
			}
		}
		if clean == "." {
			clean = ""
		}
		t.Dir = clean
	}
	return t, nil
}

// validBranch is a conservative reading of git check-ref-format --branch.
func validBranch(b string) bool {
	if strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") ||
		strings.HasSuffix(b, ".lock") || strings.Contains(b, "..") || strings.Contains(b, "//") || strings.Contains(b, "@{") || b == "@" {
		return false
	}
	for _, r := range b {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	for _, part := range strings.Split(b, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}
