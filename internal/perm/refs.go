package perm

import (
	"os"
	"path/filepath"
	"strings"
)

// movesQueuedBranch says why a git command run by a confined agent would move a local branch other than the one its tree has checked
// out, or "" when it would not. A confined agent's tree shares the repository's refs with the person's checkout and every other tree,
// so a ref write is not a file access the tree check sees: git update-ref refs/heads/<the checkout's branch> moved the person's branch
// to the integration tip while their files stayed behind, and their next look at git status showed every merged change undone. The
// merge queue alone moves the checkout's branch and the run's branches.
func (ev *evaluator) movesQueuedBranch(u *unit) string {
	if u.name != "git" || u.simple == nil {
		return ""
	}
	args, dir := u.simple.Args, ev.startDir()
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-C" && i+1 < len(args):
			if filepath.IsAbs(args[i+1]) {
				dir = filepath.Clean(args[i+1])
			} else {
				dir = filepath.Join(dir, args[i+1])
			}
			i += 2
		case a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace":
			i += 2
		case strings.HasPrefix(a, "-"):
			i++
		default:
			goto sub
		}
	}
	return ""
sub:
	own, _ := gitBranch(dir)
	var checkout string
	if len(ev.rs.roots) > 0 {
		checkout, _ = gitBranch(ev.rs.roots[0].lex)
	}
	// queued names a branch only the merge queue moves: the person's checkout's, and the run's branches (the integration branch and
	// the agents' trees, all under sleipnir/). An agent's own branch is its own, unless it would lose it (move, delete).
	queued := func(ref string, losing bool) bool {
		b := strings.TrimPrefix(ref, "refs/heads/")
		if b == "" || (b == own && !losing) {
			return false
		}
		return b == checkout || strings.HasPrefix(b, "sleipnir/")
	}
	refuse := func(what, ref string) string {
		return ev.r.Agent + " may not move branch " + strings.TrimPrefix(ref, "refs/heads/") + " (" + clipText(what, 80) + "): only the " +
			"merge queue moves the person's checkout's branch and the run's branches, and a branch moved without its files makes " +
			"merged work look undone. Accepted work reaches the checkout by the merge; do not retry this another way"
	}
	sub, rest := args[i], args[i+1:]
	ops := operands(rest)
	if sub == "update-ref" || sub == "symbolic-ref" {
		ops = operands(withoutValue(rest, "-m"))
	}
	switch sub {
	case "update-ref":
		if hasLong(rest, "--stdin") {
			return refuse("git update-ref --stdin", "refs read from stdin")
		}
		if len(ops) > 0 && strings.HasPrefix(ops[0], "refs/heads/") && queued(ops[0], hasShort(rest, "d")) {
			return refuse("git update-ref "+ops[0], ops[0])
		}
	case "symbolic-ref":
		if len(ops) > 1 && strings.HasPrefix(ops[0], "refs/heads/") && queued(ops[0], false) {
			return refuse("git symbolic-ref "+ops[0], ops[0])
		}
		// Pointing the tree's HEAD at a branch makes the next commit in the tree
		// move that branch.
		if len(ops) > 1 && ops[0] == "HEAD" && queued(ops[1], false) {
			return refuse("git symbolic-ref HEAD "+ops[1], ops[1])
		}
	case "branch":
		switch {
		case hasShort(rest, "dD") || hasLong(rest, "--delete"):
			for _, b := range ops {
				if queued(b, true) {
					return refuse("git branch -d "+b, b)
				}
			}
		case hasShort(rest, "mM") || hasLong(rest, "--move"):
			if len(ops) == 1 && queued(own, true) { // renames the tree's own branch from under it
				return refuse("git branch -m "+ops[0], own)
			}
			for k, b := range ops {
				if queued(b, k == 0) {
					return refuse("git branch -m "+strings.Join(ops, " "), b)
				}
			}
		case hasShort(rest, "cC") || hasLong(rest, "--copy"):
			// git branch -c [<old>] <new> writes <new>: the last operand, or the only one (a copy of the current branch).
			if len(ops) > 0 {
				t := ops[min(len(ops), 2)-1]
				if queued(t, false) {
					return refuse("git branch --copy "+strings.Join(ops, " "), t)
				}
			}
		case hasShort(rest, "f") || hasLong(rest, "--force"):
			if len(ops) > 0 && queued(ops[0], false) {
				return refuse("git branch -f "+ops[0], ops[0])
			}
		}
	case "push", "fetch":
		// git fetch writes this repository's refs whatever it fetches from; git push writes them when its destination is this
		// repository, which a path, a file:// URL or a remote configured with one can name as well as ".". A destination that is
		// not known to be another machine's is judged as this repository.
		if len(ops) < 2 || (sub == "push" && !pushMayBeLocal(dir, ops[0])) {
			return ""
		}
		if sub == "push" && (hasLong(rest, "--delete") || hasShort(rest, "d")) {
			for _, b := range ops[1:] {
				if queued(b, true) {
					return refuse("git push . --delete "+b, b)
				}
			}
		}
		for _, spec := range ops[1:] {
			j := strings.LastIndexByte(spec, ':')
			if j < 0 || j == len(spec)-1 {
				continue
			}
			dst := strings.TrimPrefix(spec[j+1:], "+")
			if sub == "push" && j == 0 && queued(dst, true) { // push . :branch deletes it
				return refuse("git push . "+spec, dst)
			}
			if strings.HasPrefix(dst, "refs/") && !strings.HasPrefix(dst, "refs/heads/") {
				continue
			}
			if queued(dst, false) {
				return refuse("git "+sub+" "+spec, dst)
			}
		}
	}
	return ""
}

// operands are a git subcommand's arguments that are not options, everything after a bare -- included.
func operands(args []string) []string {
	var out []string
	for k, a := range args {
		if a == "--" {
			return append(out, args[k+1:]...)
		}
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

// withoutValue drops each opt and the separate value that follows it (git update-ref -m <reason>), up to a bare --.
func withoutValue(args []string, opt string) []string {
	out := make([]string, 0, len(args))
	for k := 0; k < len(args); k++ {
		if args[k] == "--" {
			return append(out, args[k:]...)
		}
		if args[k] == opt {
			k++
			continue
		}
		out = append(out, args[k])
	}
	return out
}

// pushMayBeLocal reports whether the destination of git push, run in dir, may be a repository on this machine (and so this one,
// whose refs every tree shares): ".", a path, a file:// URL, or a remote whose configured URL is one of those or cannot be read.
// Only a URL of another scheme or an scp-like host:path is known to be elsewhere.
func pushMayBeLocal(dir, dest string) bool {
	if remoteURL(dest) {
		return false
	}
	if dest == "." || dest == ".." || strings.ContainsAny(dest, "/\\") || strings.HasPrefix(dest, "file:") {
		return true
	}
	urls, ok := remoteURLs(dir, dest)
	if !ok || len(urls) == 0 {
		return true
	}
	for _, u := range urls {
		if !remoteURL(u) {
			return true
		}
	}
	return false
}

// remoteURL reports whether s names a repository on another machine: a URL of a scheme other than file, or an scp-like
// [user@]host:path.
func remoteURL(s string) bool {
	if len(s) >= 5 && strings.EqualFold(s[:5], "file:") {
		return false
	}
	if scheme, _, ok := strings.Cut(s, "://"); ok {
		return scheme != ""
	}
	host, _, ok := strings.Cut(s, ":")
	return ok && host != "" && !strings.ContainsAny(host, "/\\") && len(host) > 1 // C:\repo is a Windows path
}

// remoteURLs reads the url and pushurl of remote name from the configuration of the repository at dir. ok is false when it cannot
// be established: no readable configuration, an include (which may set the URL elsewhere), or an insteadOf rewrite.
func remoteURLs(dir, name string) (urls []string, ok bool) {
	common, found := gitCommonDir(dir)
	if !found {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(common, "config"))
	if err != nil {
		return nil, false
	}
	section := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.Contains(lower, "insteadof") || strings.HasPrefix(lower, "[include") {
			return nil, false
		}
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if section != `[remote "`+name+`"]` {
			continue
		}
		k, v, has := strings.Cut(line, "=")
		if k = strings.ToLower(strings.TrimSpace(k)); has && (k == "url" || k == "pushurl") {
			urls = append(urls, strings.Trim(strings.TrimSpace(v), `"`))
		}
	}
	return urls, true
}

// gitCommonDir finds the git directory that holds the shared configuration and refs of the repository at dir, or of the linked
// worktree at dir (its .git file names its own directory, whose commondir names the shared one).
func gitCommonDir(dir string) (string, bool) {
	g := filepath.Join(dir, ".git")
	fi, err := os.Stat(g)
	if err != nil {
		return "", false
	}
	if fi.IsDir() {
		return g, true
	}
	b, err := os.ReadFile(g)
	if err != nil || !strings.HasPrefix(string(b), "gitdir:") {
		return "", false
	}
	own := strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:"))
	if !filepath.IsAbs(own) {
		own = filepath.Join(dir, own)
	}
	c, err := os.ReadFile(filepath.Join(own, "commondir"))
	if err != nil {
		return own, true
	}
	common := strings.TrimSpace(string(c))
	if !filepath.IsAbs(common) {
		common = filepath.Join(own, common)
	}
	return filepath.Clean(common), true
}
