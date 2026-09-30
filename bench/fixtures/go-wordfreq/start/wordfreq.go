// Package wordfreq counts how often words occur in a piece of text.
//
// README.md specifies the behaviour of TopN exactly.
package wordfreq

// Count is a word together with the number of times it occurs.
type Count struct {
	Word  string // the word, in lower case
	Count int    // how many times it occurs
}

// TopN returns the n most frequent words of text, most frequent first.
// Words with equal counts are ordered alphabetically; words in stop are ignored.
func TopN(text string, n int, stop []string) []Count {
	// TODO: implement, following README.md.
	return nil
}
