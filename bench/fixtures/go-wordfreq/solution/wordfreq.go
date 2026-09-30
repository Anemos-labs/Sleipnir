// Package wordfreq counts how often words occur in a piece of text.
//
// README.md specifies the behaviour of TopN exactly.
package wordfreq

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
)

// Count is a word together with the number of times it occurs.
type Count struct {
	Word  string // the word, in lower case
	Count int    // how many times it occurs
}

// TopN returns the n most frequent words of text, most frequent first.
// Words with equal counts are ordered alphabetically; words in stop are ignored.
func TopN(text string, n int, stop []string) []Count {
	if n <= 0 {
		return []Count{}
	}
	ignored := make(map[string]bool, len(stop))
	for _, s := range stop {
		ignored[strings.ToLower(s)] = true
	}
	freq := make(map[string]int)
	for _, w := range words(text) {
		w = strings.ToLower(w)
		if !ignored[w] {
			freq[w]++
		}
	}
	counts := make([]Count, 0, len(freq))
	for w, c := range freq {
		counts = append(counts, Count{Word: w, Count: c})
	}
	slices.SortFunc(counts, func(a, b Count) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c // higher count first
		}
		return strings.Compare(a.Word, b.Word)
	})
	if len(counts) > n {
		counts = counts[:n]
	}
	return counts
}

// words splits text into words: maximal runs of letters, where an ASCII
// apostrophe with a letter on each side also belongs to the word.
func words(text string) []string {
	runes := []rune(text)
	inWord := func(i int) bool {
		r := runes[i]
		if unicode.IsLetter(r) {
			return true
		}
		return r == '\'' && i > 0 && i+1 < len(runes) &&
			unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1])
	}
	var out []string
	start := -1
	for i := range runes {
		switch {
		case inWord(i) && start < 0:
			start = i
		case !inWord(i) && start >= 0:
			out = append(out, string(runes[start:i]))
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, string(runes[start:]))
	}
	return out
}
