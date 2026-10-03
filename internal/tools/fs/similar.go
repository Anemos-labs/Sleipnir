package fs

import (
	"sort"
	"strings"
)

// Similarity is used only to make error messages useful ("closest line is ..."),
// so it favours speed and determinism over linguistic quality: Sørensen–Dice
// over byte bigrams, ASCII case-folded.

// fold lowercases ASCII uppercase bytes and leaves other bytes unchanged.
func fold(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// bigram packs two case-folded bytes into a lookup key; i and i+1 must be valid indices.
func bigram(s string, i int) uint16 { return uint16(fold(s[i]))<<8 | uint16(fold(s[i+1])) }

// diceStrings compares two short strings (file names).
func diceStrings(a, b string) float64 {
	if a == b {
		return 1
	}
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	counts := map[uint16]int{}
	for i := 0; i+1 < len(a); i++ {
		counts[bigram(a, i)]++
	}
	inter := 0
	for i := 0; i+1 < len(b); i++ {
		g := bigram(b, i)
		if counts[g] > 0 {
			counts[g]--
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(a)-1+len(b)-1)
}

// osaDistance is the optimal-string-alignment edit distance (insert, delete,
// substitute, transpose adjacent) between two short strings: the natural measure
// for file-name typos such as "mian.go".
func osaDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d := prev[j] + 1
			if v := cur[j-1] + 1; v < d {
				d = v
			}
			if v := prev[j-1] + cost; v < d {
				d = v
			}
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				if v := prev2[j-2] + 1; v < d {
					d = v
				}
			}
			cur[j] = d
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}

// simScratch scores many lines against one target without allocating per line.
// It is sized for the full bigram space, so create one per hint, not per line.
type simScratch struct {
	target []bool
	seen   []uint32
	gen    uint32
	marked []uint16
	tcount int
}

// newSimScratch allocates reusable tables covering all possible byte bigrams.
func newSimScratch() *simScratch {
	return &simScratch{target: make([]bool, 1<<16), seen: make([]uint32, 1<<16)}
}

func (sc *simScratch) setTarget(t string) {
	for _, g := range sc.marked {
		sc.target[g] = false
	}
	sc.marked = sc.marked[:0]
	for i := 0; i+1 < len(t); i++ {
		g := bigram(t, i)
		if !sc.target[g] {
			sc.target[g] = true
			sc.marked = append(sc.marked, g)
		}
	}
	sc.tcount = len(sc.marked)
}

func (sc *simScratch) score(line string) float64 {
	if sc.tcount == 0 || len(line) < 2 {
		return 0
	}
	sc.gen++
	inter, n := 0, 0
	for i := 0; i+1 < len(line); i++ {
		g := bigram(line, i)
		if sc.seen[g] == sc.gen {
			continue
		}
		sc.seen[g] = sc.gen
		n++
		if sc.target[g] {
			inter++
		}
	}
	return 2 * float64(inter) / float64(sc.tcount+n)
}

type similarLine struct {
	line  int // 1-based
	score float64
	// idx is which of the target lines produced the match.
	idx int
}

// closestLines finds the file lines most similar to any of the targets. Only the
// first maxBytes of text are examined so a huge file cannot make an error
// message slow. Ties keep the earlier line.
func closestLines(lines []string, targets []string, minScore float64, maxBytes int) (best similarLine, ok bool) {
	sc := newSimScratch()
	trimmed := make([]string, 0, len(lines))
	total := 0
	for _, l := range lines {
		t := strings.TrimSpace(l)
		total += len(t) + 1
		if total > maxBytes {
			break
		}
		trimmed = append(trimmed, t)
	}
	for ti, t := range targets {
		sc.setTarget(strings.ToLower(t))
		for i, l := range trimmed {
			if len(l) == 0 {
				continue
			}
			s := sc.score(l)
			if s > best.score {
				best = similarLine{line: i + 1, score: s, idx: ti}
			}
		}
	}
	return best, best.score >= minScore
}

// topTargets picks up to n of the longest distinct non-blank trimmed lines of
// old: long lines carry the most identifying text.
func topTargets(old string, n int) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range strings.Split(old, "\n") {
		t := strings.TrimSpace(l)
		if len(t) < 4 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}
