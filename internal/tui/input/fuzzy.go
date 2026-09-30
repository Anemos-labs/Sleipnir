package input

import (
	"sort"
	"unicode"
)

// Fuzzy matching is what makes typing filter the completion menu. It is deliberately simple and fully specified, so that a
// list is ordered the same way every time and a user can predict it.
//
// Fuzzy(query, text) reports whether every rune of query appears in text in order (a subsequence, ignoring case) and scores
// the match. Matches fall into three tiers, better tiers always beat worse ones:
//
//  1. prefix: text begins with query ("rev" matches "review"). An exact-case prefix ranks above one that differs in case.
//  2. word boundary: query begins a later word of text ("com" matches "git-commit"), or each rune of query begins a
//     successive word ("gc" matches "git-commit"). A word begins at the start of the text, after a space or one of - _ . / :
//     and at a lower-to-upper case change (the "C" of "gitCommit").
//  3. subsequence: the runes appear in order anywhere ("rvw" matches "review").
//
// Inside a tier, a tighter match (the shorter stretch of text from its first matched rune to its last) wins, then the earlier
// one, then the shorter text; callers sort stably, so candidates that still tie keep the order they were given in. An empty
// query matches everything with score 0.
func Fuzzy(query, text string) (score int, ok bool) {
	q := []rune(query)
	if len(q) == 0 {
		return 0, true
	}
	t := []rune(text)
	if len(q) > len(t) || len(t) > 4096 {
		return 0, false
	}
	ql, tl := foldRunes(q), foldRunes(t)
	bonus := func(span, start int) int { // always within 0..999999, so a tier can never overtake the one above it
		return max(0, 100000-span*100-start*10-len(t))
	}

	if hasPrefix(tl, ql) {
		b := bonus(len(q), 0)
		if hasPrefix(t, q) {
			b += 500 // the case matches too
		}
		return 3*1000000 + b, true
	}

	starts := wordStarts(t)
	for _, p := range starts { // the query begins a later word
		if p > 0 && hasPrefix(tl[p:], ql) {
			return 2*1000000 + bonus(len(q), p), true
		}
	}
	// each query rune begins a successive word
	if pos, ok := matchAt(ql, tl, starts); ok {
		return 2*1000000 + bonus(pos[len(pos)-1]-pos[0]+1, pos[0]), true
	}

	// plain subsequence: the earliest end, then the tightest start for it
	end, ok := subsequenceEnd(ql, tl)
	if !ok {
		return 0, false
	}
	start := subsequenceStart(ql, tl, end)
	return 1*1000000 + bonus(end-start+1, start), true
}

func foldRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}

func hasPrefix(s, p []rune) bool {
	if len(p) > len(s) {
		return false
	}
	for i := range p {
		if s[i] != p[i] {
			return false
		}
	}
	return true
}

func isSep(r rune) bool {
	switch r {
	case ' ', '-', '_', '.', '/', ':', '\\':
		return true
	}
	return false
}

// wordStarts lists the indexes where a word of t begins.
func wordStarts(t []rune) []int {
	var out []int
	for i, r := range t {
		switch {
		case isSep(r):
		case i == 0, isSep(t[i-1]), unicode.IsLower(t[i-1]) && unicode.IsUpper(r):
			out = append(out, i)
		}
	}
	return out
}

// matchAt matches every rune of q against a distinct word start, in order.
func matchAt(q, t []rune, starts []int) ([]int, bool) {
	var pos []int
	k := 0
	for _, p := range starts {
		if k < len(q) && t[p] == q[k] {
			pos = append(pos, p)
			k++
		}
	}
	return pos, k == len(q)
}

func subsequenceEnd(q, t []rune) (int, bool) {
	k := 0
	for i, r := range t {
		if r == q[k] {
			k++
			if k == len(q) {
				return i, true
			}
		}
	}
	return 0, false
}

// subsequenceStart walks back from end to find the latest start of a match that ends there.
func subsequenceStart(q, t []rune, end int) int {
	k := len(q) - 1
	for i := end; i >= 0; i-- {
		if t[i] == q[k] {
			if k == 0 {
				return i
			}
			k--
		}
	}
	return 0
}

// rank filters items by Fuzzy against the text key gives for each, best first. Items that tie keep their order.
func rank[T any](query string, items []T, key func(T) string) []T {
	type scored struct {
		item  T
		score int
	}
	var keep []scored
	for _, it := range items {
		if s, ok := Fuzzy(query, key(it)); ok {
			keep = append(keep, scored{it, s})
		}
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].score > keep[j].score })
	out := make([]T, len(keep))
	for i, k := range keep {
		out[i] = k.item
	}
	return out
}
