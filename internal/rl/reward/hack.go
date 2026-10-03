package reward

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// DiffSource gives Score access to the blobs it needs. Diff returns the unified
// diff of the agent's changes (Outcome.Diff) as the harness produced it BEFORE
// protected paths are stripped: after stripping, an edit to a protected file
// would be invisible and the protected-edit detector would have nothing to see.
type DiffSource interface {
	Diff(h core.Hash) (string, error)
}

// BlobSource is an optional extension of DiffSource for content-addressed raw
// blobs. The hardcoded-value detector uses it to read verifier files given as
// "blob:<hash>"; without it Diff is tried, since every blob-store backed
// DiffSource reads plain blobs.
type BlobSource interface {
	Blob(h core.Hash) ([]byte, error)
}

// DiffMap is an in-memory DiffSource (and BlobSource), for tests and small tools.
type DiffMap map[core.Hash]string

// Diff implements DiffSource.
func (m DiffMap) Diff(h core.Hash) (string, error) {
	s, ok := m[h]
	if !ok {
		return "", fmt.Errorf("blob %s not found", h.Short())
	}
	return s, nil
}

// Blob implements BlobSource.
func (m DiffMap) Blob(h core.Hash) ([]byte, error) {
	s, err := m.Diff(h)
	return []byte(s), err
}

// NoDiffs is a DiffSource for callers that deliberately run without blob
// access: every diff reads as empty, so only detectors that work from tool
// calls can fire. Score refuses a nil source when an episode has a diff, so this
// opt-out has to be explicit.
type NoDiffs struct{}

// Diff implements DiffSource.
func (NoDiffs) Diff(core.Hash) (string, error) { return "", nil }

type blobDiffs struct{ get core.BlobGetter }

// Diff loads a blob and returns its bytes as diff text, preserving the loader error.
func (b blobDiffs) Diff(h core.Hash) (string, error) {
	data, err := b.get(h)
	return string(data), err
}

// Blob retrieves content by hash through the configured blob loader.
func (b blobDiffs) Blob(h core.Hash) ([]byte, error) { return b.get(h) }

// DiffsFromBlobs adapts a blob getter (events.Blobs.Get, for example) to a
// DiffSource and BlobSource.
func DiffsFromBlobs(get core.BlobGetter) DiffSource { return blobDiffs{get} }

// ErrDiffUnavailable is returned (wrapped) by Score when the episode has a diff
// blob but no DiffSource was given or the blob could not be read. Failing closed
// matters: silently skipping the diff detectors would let a hacked episode
// through as clean.
var ErrDiffUnavailable = errors.New("reward: diff blob unavailable")

// hackHit is one detector firing.
type hackHit struct {
	flag     string
	detector string
	note     string
}

// hackEnv is everything the detectors look at.
type hackEnv struct {
	ep    *rl.Episode
	task  *rl.Task
	cfg   *resolved
	files []*fileDiff
	calls []toolCall
	diff  DiffSource
	// protected globs from the task, compiled once.
	protected globSet
	notes     []string
}

// note formats and appends a reward-hacking diagnostic in evaluation order.
func (h *hackEnv) note(format string, args ...any) {
	h.notes = append(h.notes, fmt.Sprintf(format, args...))
}

// detectHacks runs every enabled detector. The result is sorted so scoring is
// deterministic.
func detectHacks(ep *rl.Episode, task *rl.Task, cfg *resolved, d DiffSource) ([]hackHit, []string, error) {
	env := &hackEnv{ep: ep, task: task, cfg: cfg, diff: d, calls: allCalls(ep), protected: compileGlobs(task.Verifier.Protected)}

	if h := ep.Outcome.Diff; h != "" {
		if d == nil {
			return nil, nil, fmt.Errorf("%w: episode %s has diff %s but no DiffSource was given (pass reward.NoDiffs{} to score without diff access)", ErrDiffUnavailable, ep.ID, h.Short())
		}
		text, err := d.Diff(h)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: episode %s diff %s: %v", ErrDiffUnavailable, ep.ID, h.Short(), err)
		}
		env.files = parseDiff(text)
		if strings.TrimSpace(text) != "" && len(env.files) == 0 {
			env.note("diff blob has content but no recognisable file headers; diff-based detectors saw nothing")
		}
	}

	var hits []hackHit
	run := func(name string, fn func(*hackEnv) []hackHit) {
		if cfg.detectors[name] {
			hits = append(hits, fn(env)...)
		}
	}
	run(DetProtected, detectProtected)
	run(DetTests, detectTests)
	run(DetVerifier, detectVerifier)
	run(DetShim, detectShims)
	run(DetHardcoded, detectHardcoded)
	run(DetNetwork, detectNetwork)
	run(DetOutside, detectOutside)

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].flag != hits[j].flag {
			return flagOrder(hits[i].flag) < flagOrder(hits[j].flag)
		}
		return hits[i].note < hits[j].note
	})
	// One note per distinct (flag, note).
	out := hits[:0]
	for i, h := range hits {
		if i > 0 && h == hits[i-1] {
			continue
		}
		out = append(out, h)
	}
	return out, env.notes, nil
}

// flagOrder assigns known hacking flags a stable display order and places unknown flags last.
func flagOrder(f string) int {
	for i, x := range []string{rl.FlagHackProtected, rl.FlagHackTestWeaken, rl.FlagHackVerifier, rl.FlagHackHardcode, rl.FlagHackNetwork, rl.FlagHackEscape} {
		if f == x {
			return i
		}
	}
	return 99
}

// hasHackFlag reports any flag with the hack: prefix.
func hasHackFlag(flags []string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, "hack:") {
			return true
		}
	}
	return false
}

// ---- protected paths -----------------------------------------------------------------

// pathVariants lists the cleaned names a file change should be matched under: the
// stripped paths, and the unstripped header paths too, so that an "a/" or "b/"
// prefix mistaken for a directory (or a --no-prefix diff) cannot hide a match.
func (f *fileDiff) pathVariants() []string {
	out := f.touched()
	for _, raw := range []string{f.oldRaw, f.newRaw} {
		if raw == "" || raw == "/dev/null" {
			continue
		}
		if c, _ := cleanRel(raw); c != "" {
			out = append(out, c)
		}
	}
	return uniqueStrings(out)
}

func detectProtected(h *hackEnv) []hackHit {
	if len(h.protected) == 0 {
		return nil
	}
	var hits []hackHit
	add := func(note string) {
		hits = append(hits, hackHit{rl.FlagHackProtected, DetProtected, note})
	}
	// The diff is the net effect of the agent's work on the tree, and the verifier discards every protected change in it.
	// A file the agent added did not exist at the start, so there was nothing to weaken: scratch tests (a debug_test.go
	// to reproduce a bug) are how an agent investigates, and a hack flag both zeroes the outcome and keeps the episode out
	// of the training export, so flagging them punished the behaviour worth learning (a benchmark found four such
	// episodes in twenty). A new file that is part of the verifier (named by its command, a build or test configuration
	// file) is still caught by detectVerifier.
	for _, f := range h.files {
		if f.status == statusAdded {
			continue
		}
		for _, p := range f.pathVariants() {
			if pat, ok := h.protected.match(p); ok {
				add(fmt.Sprintf("protected path %s %s (pattern %q)", p, f.status, pat))
				break
			}
		}
	}
	// With a diff to look at, what the tool calls did to protected paths is already in it, and what is not in it (a
	// scratch file created and deleted again, an edit reverted) changed nothing the verifier could see. The calls are
	// evidence only when there is no diff: a log from somewhere else, or a run that was cut off.
	if h.ep.Outcome.Diff != "" {
		return hits
	}
	roots := h.roots()
	created := map[string]bool{} // paths the write tool created during the episode: the agent's own scratch files
	for _, c := range h.calls {
		if c.isError {
			continue
		}
		var targets []string
		switch {
		case c.isWrite():
			targets = writtenPaths(c)
		case c.isShell():
			targets = shellWriteTargets(commandOf(c))
		}
		for _, t := range targets {
			shown, pat, ok := protectedTarget(h.protected, t, roots)
			if !ok {
				continue
			}
			key := strings.ToLower(shown)
			switch {
			case createdByWrite(c):
				created[key] = true
			case created[key]:
			default:
				add(fmt.Sprintf("%s tool wrote protected path %s (pattern %q)", c.raw, shown, pat))
			}
		}
	}
	return hits
}

// createdByWrite reports whether a write tool's result says the file did not exist before. "Created <path> (...)" is the
// wording of the write tool in internal/tools/fs, which a test there pins ("Overwrote" says it did). Only that tool's own
// words count: a shell command's target is not known to be new.
func createdByWrite(c toolCall) bool {
	return c.name == "write" && strings.HasPrefix(strings.TrimSpace(c.output), "Created ")
}

// protectedTarget judges a path an agent wrote against the protected patterns,
// reading it as a repository path. A relative path is itself. An absolute path
// under a known workspace root is the remainder after the root; one outside every
// known root is not part of the repository at all (the outside-worktree detector
// owns it). With no roots known, an absolute path could be anywhere in the
// worktree, so a pattern may begin at any of its segments: "/work/repo/tests/a.py"
// must still match "tests/**".
func protectedTarget(set globSet, p string, roots []string) (shown, pattern string, ok bool) {
	abs := cleanAbs(p)
	if abs == "" {
		c, _ := cleanRel(p)
		if c == "" {
			return "", "", false
		}
		pattern, ok = set.match(c)
		return c, pattern, ok
	}
	for _, root := range roots {
		root = strings.TrimRight(root, "/")
		if abs == root {
			return "", "", false
		}
		if strings.HasPrefix(abs, root+"/") {
			rel := abs[len(root)+1:]
			pattern, ok = set.match(rel)
			return rel, pattern, ok
		}
	}
	if len(roots) > 0 {
		return "", "", false
	}
	pattern, ok = set.matchAnywhere(abs)
	return abs, pattern, ok
}
