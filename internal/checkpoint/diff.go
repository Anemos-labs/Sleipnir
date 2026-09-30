package checkpoint

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
)

// DiffStatus says how a path differs from a checkpoint.
type DiffStatus string

const (
	DiffAdded    DiffStatus = "added"    // did not exist at the checkpoint, exists now
	DiffModified DiffStatus = "modified" // content, mode, link target or type differs
	DiffDeleted  DiffStatus = "deleted"  // existed at the checkpoint, gone now
)

// FileDiff is one path's difference between a checkpoint and the working tree.
type FileDiff struct {
	Path   string     `json:"path"`
	Status DiffStatus `json:"status"`
	// Agents that touched the path since the checkpoint.
	Agents  []string `json:"agents,omitempty"`
	Binary  bool     `json:"binary,omitempty"`
	OldSize int64    `json:"old_size"`
	NewSize int64    `json:"new_size"`
	Added   int      `json:"added_lines"`
	Removed int      `json:"removed_lines"`
	// Unified is a unified diff (checkpoint -> now) for text files.
	Unified string `json:"unified,omitempty"`
	// Note explains anything the diff cannot show: mode or type changes, files
	// too large to diff, content that was never saved.
	Note string `json:"note,omitempty"`
}

const (
	diffContext   = 3
	maxDiffBytes  = 2 << 20
	maxDiffLines  = 100_000
	maxEditDist   = 1500
	maxDiffOutput = 256 << 10
)

// Diff compares the working tree with checkpoint id ("" means the latest): for
// every file touched since then, whether it was added, modified or deleted, with
// a unified diff for text files. Files touched but back in their original state
// are omitted. Paths are sorted.
func (s *Store) Diff(id string) ([]FileDiff, error) {
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("%w: there are no checkpoints", ErrUnknownCheckpoint)
		}
		return nil, fmt.Errorf("%w: %q", ErrUnknownCheckpoint, id)
	}
	items := s.planLocked(idx)
	s.mu.Unlock()

	out := []FileDiff{}
	for _, it := range items {
		cur, curData := s.capture(it.abs)
		if sameState(cur, it.want) {
			continue
		}
		out = append(out, s.diffOne(it, cur, curData))
	}
	return out, nil
}

func (s *Store) diffOne(it *planned, cur state, curData []byte) FileDiff {
	want := it.want
	d := FileDiff{Path: it.key, Agents: slices.Clone(it.agents), OldSize: want.Size, NewSize: cur.Size}
	switch {
	case want.Kind == kAbsent:
		d.Status = DiffAdded
	case cur.Kind == kAbsent:
		d.Status = DiffDeleted
	default:
		d.Status = DiffModified
	}
	var notes []string
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	defer func() { d.Note = strings.Join(notes, "; ") }()

	// Content is comparable only when both sides are a saved file or nothing.
	var oldData []byte
	oldOK, newOK := false, false
	switch want.Kind {
	case kAbsent:
		oldOK = true
	case kFile:
		b, err := s.blobs.Get(want.Blob)
		if err != nil {
			note("the checkpoint's saved content is unavailable: %v", err)
		} else {
			oldData, oldOK = b, true
		}
	case kUnsaved, kOther:
		note("the original was not saved: %s", want.Note)
	}
	switch cur.Kind {
	case kAbsent, kFile:
		newOK = true
	case kUnsaved, kOther:
		note("the current file cannot be diffed: %s", cur.Note)
	}

	if want.Kind != cur.Kind && want.Kind != kAbsent && cur.Kind != kAbsent {
		note("type changed: %s -> %s", describe(want), describe(cur))
	} else {
		switch {
		case want.Kind == kLink && cur.Kind == kLink:
			note("symlink target changed: %s -> %s", want.Target, cur.Target)
		case want.Kind == kAbsent && cur.Kind == kLink:
			note("symlink created: %s", cur.Target)
		case want.Kind == kAbsent && cur.Kind == kDir:
			note("directory created")
		case want.Kind == kDir && cur.Kind == kAbsent:
			note("directory removed")
		case want.Kind == kLink && cur.Kind == kAbsent:
			note("symlink removed (was %s)", want.Target)
		case want.Kind == kDir && cur.Kind == kDir:
			note("mode %04o -> %04o", want.Mode, cur.Mode)
		}
	}
	if want.Kind == kFile && cur.Kind == kFile && want.Mode != cur.Mode {
		note("mode %04o -> %04o", want.Mode, cur.Mode)
	}

	hasContent := (want.Kind == kFile || cur.Kind == kFile)
	if !hasContent || !oldOK || !newOK {
		return d
	}
	if isBinary(oldData) || isBinary(curData) {
		d.Binary = true
		note("binary file (%s -> %s)", humanBytes(int64(len(oldData))), humanBytes(int64(len(curData))))
		return d
	}
	if len(oldData) > maxDiffBytes || len(curData) > maxDiffBytes {
		note("too large to diff (%s -> %s)", humanBytes(int64(len(oldData))), humanBytes(int64(len(curData))))
		return d
	}
	if want.Kind == kFile && cur.Kind == kFile && want.Sum == cur.Sum {
		return d // only the mode changed
	}
	oldName, newName := "a/"+strings.TrimPrefix(it.key, "/"), "b/"+strings.TrimPrefix(it.key, "/")
	if want.Kind == kAbsent {
		oldName = "/dev/null"
	}
	if cur.Kind == kAbsent {
		newName = "/dev/null"
	}
	d.Unified, d.Added, d.Removed = unifiedDiff(oldName, newName, string(oldData), string(curData))
	return d
}

func describe(st state) string {
	switch st.Kind {
	case kLink:
		return "symlink to " + st.Target
	case kOther:
		return st.Note
	}
	return string(st.Kind)
}

// isBinary uses git's heuristic: a NUL byte in the first 8000 bytes.
func isBinary(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0
}

// splitLines splits keeping each line's terminator, so "no newline at end of
// file" is a property of the last line and a final "x" differs from "x\n".
func splitLines(s string) []string {
	var lines []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			lines = append(lines, s)
			break
		}
		lines = append(lines, s[:i+1])
		s = s[i+1:]
	}
	return lines
}

// unifiedDiff renders the difference between two texts with three lines of
// context. It also reports how many lines were added and removed.
func unifiedDiff(oldName, newName, oldText, newText string) (out string, added, removed int) {
	a, b := splitLines(oldText), splitLines(newText)
	script := editScript(a, b)
	for _, op := range script {
		switch op {
		case 'i':
			added++
		case 'd':
			removed++
		}
	}
	blks := changeBlocks(script)
	if len(blks) == 0 {
		return "", 0, 0
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", oldName, newName)
	over, skipped := false, false
	emit := func(prefix byte, line string) {
		if over {
			skipped = true
			return
		}
		sb.WriteByte(prefix)
		sb.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			sb.WriteString("\n\\ No newline at end of file\n")
		}
		over = sb.Len() > maxDiffOutput
	}
	i := 0
	for i < len(blks) && !over {
		j := i
		for j+1 < len(blks) && blks[j+1].a0-blks[j].a1 <= 2*diffContext {
			j++
		}
		first, last := blks[i], blks[j]
		a0 := max(0, first.a0-diffContext)
		a1 := min(len(a), last.a1+diffContext)
		b0 := a0 + (first.b0 - first.a0)
		b1 := a1 + (last.b1 - last.a1)
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", hunkRange(a0, a1-a0), hunkRange(b0, b1-b0))
		pos := a0
		for k := i; k <= j; k++ {
			blk := blks[k]
			for ; pos < blk.a0; pos++ {
				emit(' ', a[pos])
			}
			for x := blk.a0; x < blk.a1; x++ {
				emit('-', a[x])
			}
			for y := blk.b0; y < blk.b1; y++ {
				emit('+', b[y])
			}
			pos = blk.a1
		}
		for ; pos < a1; pos++ {
			emit(' ', a[pos])
		}
		i = j + 1
	}
	if skipped || i < len(blks) {
		sb.WriteString("[diff truncated]\n")
	}
	return sb.String(), added, removed
}

// hunkRange formats a unified-diff range: an empty range is "N,0" where N is
// the line before it, a single line is just "N".
func hunkRange(start0, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", start0)
	case 1:
		return fmt.Sprintf("%d", start0+1)
	}
	return fmt.Sprintf("%d,%d", start0+1, count)
}

// block is a maximal run of changes: a[a0:a1] was replaced by b[b0:b1].
type block struct{ a0, a1, b0, b1 int }

func changeBlocks(script []byte) []block {
	var out []block
	ai, bi := 0, 0
	for i := 0; i < len(script); {
		if script[i] == 'e' {
			ai++
			bi++
			i++
			continue
		}
		blk := block{a0: ai, b0: bi}
		for i < len(script) && script[i] != 'e' {
			if script[i] == 'd' {
				ai++
			} else {
				bi++
			}
			i++
		}
		blk.a1, blk.b1 = ai, bi
		out = append(out, blk)
	}
	return out
}

// editScript returns one op per step turning a into b: 'e' keep, 'd' delete an
// a line, 'i' insert a b line. Common prefix and suffix are trimmed first, which
// is nearly all the work for a typical edit; the middle goes through Myers.
func editScript(a, b []string) []byte {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	ma, mb := a[p:len(a)-s], b[p:len(b)-s]

	script := bytes.Repeat([]byte{'e'}, p)
	script = append(script, diffMiddle(ma, mb)...)
	script = append(script, bytes.Repeat([]byte{'e'}, s)...)
	return script
}

func diffMiddle(a, b []string) []byte {
	if len(a) == 0 || len(b) == 0 || len(a) > maxDiffLines || len(b) > maxDiffLines {
		return replaceAll(len(a), len(b))
	}
	ids := map[string]int32{}
	intern := func(lines []string) []int32 {
		out := make([]int32, len(lines))
		for i, l := range lines {
			id, ok := ids[l]
			if !ok {
				id = int32(len(ids))
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	ia, ib := intern(a), intern(b)
	if script, ok := myers(ia, ib, maxEditDist); ok {
		return script
	}
	return patience(ia, ib)
}

func replaceAll(na, nb int) []byte {
	return append(bytes.Repeat([]byte{'d'}, na), bytes.Repeat([]byte{'i'}, nb)...)
}

// patience is the fallback when the texts differ by more than Myers is allowed
// to chew on (a formatter run over a big file, say). Lines that occur exactly
// once on each side and keep their relative order become fixed anchors; the
// stretches between anchors are small enough to diff exactly. The result is not
// always a shortest script, but it stays readable where a wholesale
// replacement would not.
func patience(a, b []int32) []byte {
	count := func(x []int32) map[int32]int {
		m := make(map[int32]int, len(x))
		for _, id := range x {
			m[id]++
		}
		return m
	}
	ca, cb := count(a), count(b)
	posB := make(map[int32]int, len(b))
	for j, id := range b {
		if ca[id] == 1 && cb[id] == 1 {
			posB[id] = j
		}
	}
	type pair struct{ i, j int }
	var pairs []pair
	for i, id := range a {
		if j, ok := posB[id]; ok {
			pairs = append(pairs, pair{i, j})
		}
	}
	// Longest increasing subsequence of j (pairs are already ordered by i).
	tails := []int{} // tails[k]: index in pairs ending the best chain of length k+1
	prev := make([]int, len(pairs))
	for idx, p := range pairs {
		lo, hi := 0, len(tails)
		for lo < hi {
			mid := (lo + hi) / 2
			if pairs[tails[mid]].j < p.j {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		prev[idx] = -1
		if lo > 0 {
			prev[idx] = tails[lo-1]
		}
		if lo == len(tails) {
			tails = append(tails, idx)
		} else {
			tails[lo] = idx
		}
	}
	var anchors []pair
	if len(tails) > 0 {
		for idx := tails[len(tails)-1]; idx >= 0; idx = prev[idx] {
			anchors = append(anchors, pairs[idx])
		}
		slices.Reverse(anchors)
	}

	var script []byte
	ai, bj := 0, 0
	for _, an := range anchors {
		script = append(script, gapScript(a[ai:an.i], b[bj:an.j])...)
		script = append(script, 'e')
		ai, bj = an.i+1, an.j+1
	}
	return append(script, gapScript(a[ai:], b[bj:])...)
}

// gapScript diffs the stretch between two anchors exactly when it is small
// enough, and replaces it wholesale otherwise.
func gapScript(a, b []int32) []byte {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	ma, mb := a[p:len(a)-s], b[p:len(b)-s]
	script := bytes.Repeat([]byte{'e'}, p)
	switch {
	case len(ma) == 0 || len(mb) == 0:
		script = append(script, replaceAll(len(ma), len(mb))...)
	default:
		if mid, ok := myers(ma, mb, maxEditDist); ok {
			script = append(script, mid...)
		} else {
			script = append(script, replaceAll(len(ma), len(mb))...)
		}
	}
	return append(script, bytes.Repeat([]byte{'e'}, s)...)
}

// myers computes a shortest edit script between a and b (Myers 1986). It gives
// up (ok=false) when more than maxD edits are needed, bounding both time and the
// O(D^2) trace memory.
func myers(a, b []int32, maxD int) (script []byte, ok bool) {
	n, m := len(a), len(b)
	maxD = min(maxD, n+m)
	off := maxD + 1
	v := make([]int32, 2*maxD+3) // v[off+k]: furthest x reached on diagonal k
	trace := make([][]int32, 0, 32)
	found := -1
outer:
	for d := 0; d <= maxD; d++ {
		// Keep the window of v this round reads (diagonals -d-1..d+1) as it was
		// before the round overwrote it: backtracking needs it.
		lo, hi := off-d-1, off+d+1
		trace = append(trace, slices.Clone(v[lo:hi+1]))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = int(v[off+k+1])
			} else {
				x = int(v[off+k-1]) + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = int32(x)
			if x >= n && y >= m {
				found = d
				break outer
			}
		}
	}
	if found < 0 {
		return nil, false
	}
	rev := make([]byte, 0, n+m)
	x, y := n, m
	for d := found; d >= 0; d-- {
		if d == 0 {
			for x > 0 && y > 0 {
				rev = append(rev, 'e')
				x--
				y--
			}
			break
		}
		snap := trace[d]
		lo := off - d - 1
		get := func(k int) int { return int(snap[off+k-lo]) }
		k := x - y
		var prevK int
		if k == -d || (k != d && get(k-1) < get(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := get(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, 'e')
			x--
			y--
		}
		if x == prevX {
			rev = append(rev, 'i')
		} else {
			rev = append(rev, 'd')
		}
		x, y = prevX, prevY
	}
	slices.Reverse(rev)
	return rev, true
}
