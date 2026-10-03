package widget

import (
	"unicode"
	"unicode/utf8"
)

// The diff algorithm: Myers' O(ND) greedy algorithm over lines interned to integers, with the common head and tail cut off
// first, a cap on D and on the work done, and a coarse answer (the whole middle replaced) when a cap is hit. The same routine,
// over tokens, finds what changed inside a pair of changed lines.

const (
	diffMaxD      = 1024       // the largest edit distance searched; memory is D squared (int32s): 4 MB
	diffMaxWork   = 30_000_000 // the most steps of the search; a pathological pair falls back to a coarse hunk
	wordMaxTokens = 400        // lines with more tokens than this get no word-level highlight
	wordMaxD      = 200        // and lines that need more token edits than this are too different to highlight
)

// dfRun is a stretch of the edit script: n lines starting at before[a] and/or after[b], each kept (' '), removed ('-') or
// added ('+').
type dfRun struct {
	kind byte
	a, b int
	n    int
}

// diffInternLines gives every distinct line an integer, so the search compares ints. The numbering follows first appearance, so it
// does not depend on map order.
func diffInternLines(a, b []string) (ia, ib []int32) {
	ids := make(map[string]int32, len(a)+len(b))
	conv := func(ls []string) []int32 {
		out := make([]int32, len(ls))
		for i, l := range ls {
			id, ok := ids[l]
			if !ok {
				id = int32(len(ids))
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	return conv(a), conv(b)
}

// diffScript returns the runs that turn before into after, in order. Between two kept runs there is at most one removed run
// followed by one added run.
func diffScript(before, after []string) []dfRun {
	ia, ib := diffInternLines(before, after)
	p := 0
	for p < len(ia) && p < len(ib) && ia[p] == ib[p] {
		p++
	}
	s := 0
	for s < len(ia)-p && s < len(ib)-p && ia[len(ia)-1-s] == ib[len(ib)-1-s] {
		s++
	}
	var runs []dfRun
	if p > 0 {
		runs = append(runs, dfRun{' ', 0, 0, p})
	}
	runs = append(runs, middleRuns(ia[p:len(ia)-s], ib[p:len(ib)-s], p, p)...)
	if s > 0 {
		runs = append(runs, dfRun{' ', len(ia) - s, len(ib) - s, s})
	}
	return runs
}

func middleRuns(a, b []int32, offA, offB int) []dfRun {
	switch {
	case len(a) == 0 && len(b) == 0:
		return nil
	case len(a) == 0:
		return []dfRun{{'+', offA, offB, len(b)}}
	case len(b) == 0:
		return []dfRun{{'-', offA, offB, len(a)}}
	}
	moves, ok := myersMoves(a, b, diffMaxD, diffMaxWork)
	if !ok { // too different, or too big: one coarse hunk that replaces the middle
		return []dfRun{{'-', offA, offB, len(a)}, {'+', offA + len(a), offB, len(b)}}
	}
	var runs []dfRun
	ai, bi := offA, offB
	for i := 0; i < len(moves); {
		if moves[i] == 'e' {
			j := i
			for j < len(moves) && moves[j] == 'e' {
				j++
			}
			runs = append(runs, dfRun{' ', ai, bi, j - i})
			ai, bi = ai+j-i, bi+j-i
			i = j
			continue
		}
		// a changed region: collect its deletions and insertions, then emit the deletions first
		j := i
		dels, adds := 0, 0
		for j < len(moves) && moves[j] != 'e' {
			if moves[j] == 'd' {
				dels++
			} else {
				adds++
			}
			j++
		}
		if dels > 0 {
			runs = append(runs, dfRun{'-', ai, bi, dels})
		}
		if adds > 0 {
			runs = append(runs, dfRun{'+', ai + dels, bi, adds})
		}
		ai, bi = ai+dels, bi+adds
		i = j
	}
	return runs
}

// myersMoves returns the shortest edit script from a to b as one move per step: 'e' the lines are equal, 'd' a line of a is
// deleted, 'i' a line of b is inserted. ok is false when more than maxD edits are needed or the search took more than budget
// steps.
func myersMoves(a, b []int32, maxD, budget int) (moves []byte, ok bool) {
	n, m := len(a), len(b)
	maxD = min(maxD, n+m)
	off := maxD + 1
	v := make([]int32, 2*maxD+3)
	trace := make([][]int32, 0, 16)
	work := 0
	for d := 0; d <= maxD; d++ {
		snap := make([]int32, 2*d+1) // v for the diagonals -d..d as this round starts
		copy(snap, v[off-d:off+d+1])
		trace = append(trace, snap)
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
				work++
			}
			work++
			v[off+k] = int32(x)
			if x >= n && y >= m {
				return diffBacktrack(trace, n, m), true
			}
		}
		if work > budget {
			return nil, false
		}
	}
	return nil, false
}

func diffBacktrack(trace [][]int32, n, m int) []byte {
	moves := make([]byte, 0, n+m)
	x, y := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		if d == 0 { // round 0 is a snake from the origin
			for x > 0 && y > 0 {
				moves = append(moves, 'e')
				x--
				y--
			}
			break
		}
		snap := trace[d]
		at := func(k int) int { return int(snap[k+d]) }
		k := x - y
		var prevK int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		ex, ey := prevX+1, prevY // a deletion moves right
		edit := byte('d')
		if prevK == k+1 { // an insertion moves down
			ex, ey = prevX, prevY+1
			edit = 'i'
		}
		for x > ex && y > ey {
			moves = append(moves, 'e')
			x--
			y--
		}
		moves = append(moves, edit)
		x, y = prevX, prevY
	}
	for i, j := 0, len(moves)-1; i < j; i, j = i+1, j-1 {
		moves[i], moves[j] = moves[j], moves[i]
	}
	return moves
}

// ---- inside a line ----

// diffMark is a range of bytes of a line's text that changed.
type diffMark struct{ lo, hi int }

// diffTokenize cuts a line into words (letters, digits, underscore), runs of spaces, and single other characters, with the byte
// offset of each.
func diffTokenize(s string) (toks []string, offs []int) {
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		j := i + n
		switch {
		case diffWordRune(r):
			for j < len(s) {
				r2, n2 := utf8.DecodeRuneInString(s[j:])
				if !diffWordRune(r2) {
					break
				}
				j += n2
			}
		case r == ' ':
			for j < len(s) && s[j] == ' ' {
				j++
			}
		}
		toks = append(toks, s[i:j])
		offs = append(offs, i)
		i = j
	}
	return toks, offs
}

// diffWordRune groups underscores and Unicode letters or digits into diff words.
func diffWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// wordMarks finds what changed between two lines that replace one another: the byte ranges of the tokens each line does not
// share with the other. It answers nothing (ok false) for lines too long to tokenize cheaply, for lines so different that
// marking most of both would only be noise, and once budget (a count of tokens shared by all the pairs of one diff) is used up.
func wordMarks(a, b string, budget *int) (ma, mb []diffMark, ok bool) {
	ta, oa := diffTokenize(a)
	tb, ob := diffTokenize(b)
	if len(ta) > wordMaxTokens || len(tb) > wordMaxTokens || len(ta) == 0 || len(tb) == 0 {
		return nil, nil, false
	}
	if *budget -= len(ta) + len(tb); *budget < 0 { // the diff has used what it may spend on highlighting words
		return nil, nil, false
	}
	ids := map[string]int32{}
	conv := func(ts []string) []int32 {
		out := make([]int32, len(ts))
		for i, t := range ts {
			id, ok := ids[t]
			if !ok {
				id = int32(len(ids))
				ids[t] = id
			}
			out[i] = id
		}
		return out
	}
	moves, found := myersMoves(conv(ta), conv(tb), wordMaxD, 2_000_000)
	if !found {
		return nil, nil, false
	}
	chA, chB := make([]bool, len(ta)), make([]bool, len(tb))
	i, j := 0, 0
	shared, wordsA, wordsB := 0, 0, 0
	for _, t := range ta {
		if t[0] != ' ' {
			wordsA++
		}
	}
	for _, t := range tb {
		if t[0] != ' ' {
			wordsB++
		}
	}
	for _, mv := range moves {
		switch mv {
		case 'e':
			if ta[i][0] != ' ' {
				shared++
			}
			i++
			j++
		case 'd':
			chA[i] = true
			i++
		case 'i':
			chB[j] = true
			j++
		}
	}
	if shared*5 < max(wordsA, wordsB)*2 { // under 40% of the words in common
		return nil, nil, false
	}
	return diffRangesOf(ta, oa, chA), diffRangesOf(tb, ob, chB), true
}

// diffRangesOf turns flags on tokens into byte ranges, joining changed tokens that are separated by nothing but one run of spaces.
func diffRangesOf(toks []string, offs []int, changed []bool) []diffMark {
	var out []diffMark
	for i := 0; i < len(toks); i++ {
		if !changed[i] {
			continue
		}
		lo, hi := offs[i], offs[i]+len(toks[i])
		for {
			if i+1 < len(toks) && changed[i+1] {
				i++
			} else if i+2 < len(toks) && toks[i+1][0] == ' ' && changed[i+2] {
				i += 2 // the spaces between two changed words belong to the change
			} else {
				break
			}
			hi = offs[i] + len(toks[i])
		}
		out = append(out, diffMark{lo, hi})
	}
	return out
}
