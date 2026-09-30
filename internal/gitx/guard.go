package gitx

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"time"
)

// Attribute-selected drivers are the hard part of "never run repo-controlled
// programs". A .gitattributes line such as `* filter=x` or `*.txt merge=x` picks
// a driver by *name*, and the configuration binds the name to a command:
//
//	filter.x.clean / .smudge / .process   run on add, commit, checkout, status, diff
//	merge.x.driver                        runs on merge, rebase, cherry-pick
//	diff.x.command / .textconv            run on diff, log -p, show
//
// The names are chosen by whoever prepared the repository, so no fixed list of
// `-c` overrides can cover them. We therefore read the effective configuration
// (system, global, repository, worktree, includes) right before each command that
// can touch attributes, and blank every driver it defines. The blanks travel in
// GIT_CONFIG_COUNT/KEY/VALUE environment variables: command-scope like `-c`, but
// with no "key=value" splitting, so a subsection name containing '=' or spaces
// cannot confuse the override. Child git processes inherit them.
//
// What blanking means for each driver (all verified against git 2.43):
//
//   - filter: an empty clean/smudge/process runs nothing and passes content
//     through; required=false stops git treating that as an error. Content is
//     then stored as-is (an LFS pointer stays a pointer).
//   - merge: an empty driver command is not executed ("cannot run") and the file
//     becomes an ordinary content conflict. Failing closed is deliberate: the
//     alternatives are silently keeping one side (driver "true") or running a
//     command.
//   - diff: command and textconv are blanked; callers also pass --no-ext-diff and
//     --no-textconv.
//
// A residual race remains and is documented rather than hidden: an agent that
// defines a driver *between* our scan and our command can have it run. Closing it
// needs an isolated config view that git does not offer; the window is one
// process start.

// maxDriverNames bounds the override list: environment space is finite and a
// hostile configuration should not be able to make every command fail with
// E2BIG in a way that looks like a git bug.
const maxDriverNames = 512

// driverOverrides scans the configuration visible from dir and returns the
// overrides that neutralize its drivers.
func (s *settings) driverOverrides(ctx context.Context, dir, workTree string) ([]kv, error) {
	out, err := s.exec(ctx, call{
		dir: dir, workTree: workTree,
		args:    []string{"config", "--list", "--includes", "-z"},
		maxOut:  8 << 20,
		timeout: 30 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	filters, merges, diffs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, entry := range bytes.Split(out.stdout, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		key, _, _ := strings.Cut(string(entry), "\n")
		section, rest, ok := strings.Cut(key, ".")
		if !ok {
			continue
		}
		i := strings.LastIndexByte(rest, '.')
		if i <= 0 {
			continue // "section.variable": no subsection, so no driver name
		}
		name, variable := rest[:i], rest[i+1:]
		switch section {
		case "filter":
			if variable == "clean" || variable == "smudge" || variable == "process" || variable == "required" {
				filters[name] = true
			}
		case "merge":
			if variable == "driver" {
				merges[name] = true
			}
		case "diff":
			if variable == "command" || variable == "textconv" {
				diffs[name] = true
			}
		}
	}
	for n := range filters {
		if s.trusted[n] {
			delete(filters, n)
		}
	}
	if len(filters)+len(merges)+len(diffs) > maxDriverNames {
		return nil, &Error{Kind: KindUnsafe, Op: "config", ExitCode: -1,
			Detail: "configuration defines an implausible number of filter/merge/diff drivers; refusing to run git"}
	}
	var ov []kv
	for _, n := range sortedKeys(filters) {
		ov = append(ov,
			kv{"filter." + n + ".clean", ""},
			kv{"filter." + n + ".smudge", ""},
			kv{"filter." + n + ".process", ""},
			kv{"filter." + n + ".required", "false"})
	}
	for _, n := range sortedKeys(merges) {
		ov = append(ov, kv{"merge." + n + ".driver", ""})
	}
	for _, n := range sortedKeys(diffs) {
		ov = append(ov, kv{"diff." + n + ".command", ""}, kv{"diff." + n + ".textconv", ""})
	}
	for _, o := range ov {
		if len(o.k) > 4096 {
			return nil, &Error{Kind: KindUnsafe, Op: "config", ExitCode: -1, Detail: "driver name too long"}
		}
	}
	return ov, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
