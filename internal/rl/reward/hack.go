package reward

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
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

func (b blobDiffs) Diff(h core.Hash) (string, error) {
	data, err := b.get(h)
	return string(data), err
}

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

func flagOrder(f string) int {
	for i, x := range []string{rl.FlagHackProtected, rl.FlagHackTestWeaken, rl.FlagHackVerifier, rl.FlagHackHardcode, rl.FlagHackNetwork, rl.FlagHackEscape} {
		if f == x {
			return i
		}
	}
	return 99
}

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
	for _, f := range h.files {
		for _, p := range f.pathVariants() {
			if pat, ok := h.protected.match(p); ok {
				add(fmt.Sprintf("protected path %s %s (pattern %q)", p, f.status, pat))
				break
			}
		}
	}
	// Tool calls: the diff shown to the scorer may already have had protected
	// paths stripped, and the write tools name their targets explicitly.
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
			rel := h.relativeToRoots(t)
			cl, _ := cleanRel(rel)
			if cl == "" {
				continue
			}
			if pat, ok := h.protected.match(cl); ok {
				add(fmt.Sprintf("%s tool wrote protected path %s (pattern %q)", c.raw, cl, pat))
			}
		}
	}
	return hits
}

// relativeToRoots turns an absolute path under a known workspace root into a
// root-relative one so protected globs (which are repository relative) apply.
func (h *hackEnv) relativeToRoots(p string) string {
	abs := cleanAbs(p)
	if abs == "" {
		return p
	}
	for _, root := range h.cfg.roots {
		if abs == root {
			return "."
		}
		if strings.HasPrefix(abs, strings.TrimRight(root, "/")+"/") {
			return abs[len(strings.TrimRight(root, "/"))+1:]
		}
	}
	return p
}
