package fs

import (
	"bytes"
	"fmt"
	"strings"
)

// The diff shown after an edit exists so the model can see what actually
// changed without re-reading the file. It is a plain line diff: common prefix
// and suffix are trimmed first (an edit touches a tiny part of a big file), and
// Myers' O(ND) algorithm handles what is left. If the middle is too different
// to diff cheaply the whole middle is reported as replaced, which is still a
// correct diff, only not a minimal one.

const (
	diffContext   = 2
	maxMyersD     = 1000
	maxDiffLineCh = 200
	// maxMyersSteps bounds the work of one diff. Myers is O((N+M)·D); a huge file
	// with thousands of scattered changes must not turn a successful edit into a
	// multi-second computation of a diff nobody will read past its first lines.
	maxMyersSteps = 40_000_000
)

// diffResult is a rendered diff. Only the first capLines lines are materialised
// (a coarse diff of a huge file could otherwise be millions of strings); total
// says how many there would be.
type diffResult struct {
	lines          []string
	total          int
	added, removed int
}

// text renders at most max lines (0 = all that were materialised) and says how
// many were cut.
func (d diffResult) text(max int) string {
	if d.total == 0 {
		return ""
	}
	lines := d.lines
	if max > 0 && len(lines) > max {
		lines = lines[:max]
	}
	s := strings.Join(lines, "\n")
	if extra := d.total - len(lines); extra > 0 {
		s += fmt.Sprintf("\n… [diff truncated: %d more lines]", extra)
	}
	return s
}

func splitForDiff(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		if strings.HasSuffix(l, "\r") {
			lines[i] = l[:len(l)-1]
		}
	}
	return lines
}

type region struct{ aStart, aEnd, bStart, bEnd int }

// diffFiles diffs two whole-file contents, materialising at most capLines lines
// (0 = all).
func diffFiles(a, b string, ctx, capLines int) diffResult {
	al, bl := splitForDiff(a), splitForDiff(b)
	pre := 0
	for pre < len(al) && pre < len(bl) && al[pre] == bl[pre] {
		pre++
	}
	suf := 0
	for suf < len(al)-pre && suf < len(bl)-pre && al[len(al)-1-suf] == bl[len(bl)-1-suf] {
		suf++
	}
	am, bm := al[pre:len(al)-suf], bl[pre:len(bl)-suf]
	var regs []region
	if len(am) > 0 || len(bm) > 0 {
		ops, ok := myersOps(am, bm, maxMyersD)
		if !ok {
			regs = []region{{0, len(am), 0, len(bm)}}
		} else {
			regs = regionsFromOps(ops)
		}
		for i := range regs {
			regs[i].aStart += pre
			regs[i].aEnd += pre
			regs[i].bStart += pre
			regs[i].bEnd += pre
		}
	}
	res := diffResult{}
	res.lines, res.total = renderHunks(al, bl, regs, ctx, capLines)
	for _, r := range regs {
		res.removed += r.aEnd - r.aStart
		res.added += r.bEnd - r.bStart
	}
	return res
}

func regionsFromOps(ops []byte) []region {
	var regs []region
	ai, bi := 0, 0
	for i := 0; i < len(ops); {
		if ops[i] == '=' {
			ai++
			bi++
			i++
			continue
		}
		r := region{aStart: ai, bStart: bi}
		for i < len(ops) && ops[i] != '=' {
			if ops[i] == '-' {
				ai++
			} else {
				bi++
			}
			i++
		}
		r.aEnd, r.bEnd = ai, bi
		regs = append(regs, r)
	}
	return regs
}

func renderHunks(a, b []string, regs []region, ctx, capLines int) ([]string, int) {
	var out []string
	total := 0
	emit := func(line func() string) {
		if capLines <= 0 || len(out) < capLines {
			out = append(out, line())
		}
	}
	for i := 0; i < len(regs); {
		j := i
		for j+1 < len(regs) && regs[j+1].aStart-regs[j].aEnd <= 2*ctx {
			j++
		}
		first, last := regs[i], regs[j]
		aFrom := first.aStart - ctx
		if aFrom < 0 {
			aFrom = 0
		}
		aTo := last.aEnd + ctx
		if aTo > len(a) {
			aTo = len(a)
		}
		bFrom := first.bStart - (first.aStart - aFrom)
		bTo := last.bEnd + (aTo - last.aEnd)
		// Every line of a appears once (as context or removed); added lines are extra.
		total += 1 + (aTo - aFrom)
		for r := i; r <= j; r++ {
			total += regs[r].bEnd - regs[r].bStart
		}
		emit(func() string {
			return fmt.Sprintf("@@ -%s +%s @@", hunkRange(aFrom, aTo-aFrom), hunkRange(bFrom, bTo-bFrom))
		})
		pos := aFrom
		for r := i; r <= j; r++ {
			for ; pos < regs[r].aStart; pos++ {
				p := pos
				emit(func() string { return " " + diffText(a[p]) })
			}
			for p := regs[r].aStart; p < regs[r].aEnd; p++ {
				p := p
				emit(func() string { return "-" + diffText(a[p]) })
			}
			for p := regs[r].bStart; p < regs[r].bEnd; p++ {
				p := p
				emit(func() string { return "+" + diffText(b[p]) })
			}
			pos = regs[r].aEnd
		}
		for ; pos < aTo; pos++ {
			p := pos
			emit(func() string { return " " + diffText(a[p]) })
		}
		i = j + 1
	}
	return out, total
}

// hunkRange formats "start,count" the way unified diffs do: start is 1-based,
// a count of 1 is omitted, and an empty range names the line before it.
func hunkRange(from, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", from)
	case 1:
		return fmt.Sprintf("%d", from+1)
	}
	return fmt.Sprintf("%d,%d", from+1, count)
}

// diffText repairs invalid UTF-8 and bounds displayed diff text, adding an ellipsis when the
// byte-length threshold is exceeded.
func diffText(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) > maxDiffLineCh {
		s = cutRunes(s, maxDiffLineCh) + "…"
	}
	return s
}

// myersOps returns the edit script turning a into b as '=', '-' and '+'
// operations, or ok=false when more than maxD edits would be needed.
func myersOps(a, b []string, maxD int) (ops []byte, ok bool) {
	n, m := len(a), len(b)
	if n == 0 {
		return bytes.Repeat([]byte{'+'}, m), true
	}
	if m == 0 {
		return bytes.Repeat([]byte{'-'}, n), true
	}
	max := n + m
	if maxD > 0 && maxD < max {
		max = maxD
	}
	off := max + 1
	v := make([]int, 2*max+3)
	var trace [][]int
	found := -1
	steps := 0
	for d := 0; d <= max && found < 0; d++ {
		// Keep the window of v the backtrace will read: diagonals -d-1 .. d+1.
		snap := make([]int, 2*d+3)
		copy(snap, v[off-d-1:off+d+2])
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
				steps++
			}
			steps++
			v[off+k] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}
		if steps > maxMyersSteps {
			return nil, false
		}
	}
	if found < 0 {
		return nil, false
	}
	ops = make([]byte, 0, n+m)
	x, y := n, m
	for d := found; d >= 0; d-- {
		snap := trace[d]
		get := func(k int) int { return snap[k+d+1] }
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
			ops = append(ops, '=')
			x--
			y--
		}
		if d > 0 {
			if x == prevX {
				ops = append(ops, '+')
			} else {
				ops = append(ops, '-')
			}
		}
		x, y = prevX, prevY
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops, true
}
