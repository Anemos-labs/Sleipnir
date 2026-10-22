package perm

import (
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
		case hasShort(rest, "fC") || hasLong(rest, "--force"):
			if len(ops) > 0 {
				t := ops[0]
				if hasShort(rest, "C") && len(ops) > 1 {
					t = ops[1]
				}
				if queued(t, false) {
					return refuse("git branch -f "+t, t)
				}
			}
		}
	case "push", "fetch":
		if len(ops) < 2 || (sub == "push" && ops[0] != ".") {
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
