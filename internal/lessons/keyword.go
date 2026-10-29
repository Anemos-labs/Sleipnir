package lessons

import (
	"math"
	"strings"
	"unicode"
)

// Keyword relevance is the lesson memory's second way of finding a lesson, beside embeddings and without them: a lesson whose
// own content words are in the task is about the task, whatever an embeddings model makes of the two. With no embedder it is
// the only way; with one, a lesson's relevance is the higher of the two, so a lesson that names the task's exact library or
// error message is found even when the vectors of a long objective and a one-line lesson drift apart.
const (
	// KeywordMinTerms is how many distinct content words of a lesson the task must contain before the lesson can match by keyword.
	KeywordMinTerms = 2
	// KeywordMergeSimilarity is the Jaccard similarity of two lessons' content words at or above which, without embeddings, a
	// new lesson says what an existing one of its kind says.
	KeywordMergeSimilarity = 0.8
)

// stopwords are words that say nothing about what a lesson is about: function words, and the words every task and lesson of a
// coding agent uses.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a about above after again against all also always an and any are as at be because been before
		being below between both but by can could did do does doing done down during each either else even every few for from further
		had has have having here how if in into is it its itself just less like may might more most much must never no nor not now of
		off on once one only or other our out over own same should since so some such than that the their them then there these they
		this those through to too under until up upon use used using very via was we were what when where whether which while who whom
		why will with within without would yet you your
		add added adding agent agents build building built change changes check code done ensure file files first fix get goal goals
		keep let lesson lessons make makes making new run runs running see set task tasks test tests tested testing thing things time
		try work works working write writes writing`) {
		m[w] = true
	}
	return m
}()

// terms returns the distinct content words of s: lowercased runs of letters and digits of two runes or more, with a trailing
// plural or third-person "s" taken off, without stopwords and numbers alone.
func terms(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			w = w[:len(w)-1]
		}
		if len([]rune(w)) < 2 || stopwords[w] || strings.IndexFunc(w, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
			continue
		}
		out[w] = true
	}
	return out
}

// keywordIndex scores lessons against one task by the content words they share, each word weighted by its rarity among the lessons
// (BM25's inverse document frequency), so a word every lesson has counts for little and a rare one for much.
type keywordIndex struct {
	text, task []map[string]bool // the content words of each lesson's text and of the task it was learned in
	idf        map[string]float64
}

// newKeywordIndex indexes ls, in order.
func newKeywordIndex(ls []Lesson) *keywordIndex {
	ix := &keywordIndex{text: make([]map[string]bool, len(ls)), task: make([]map[string]bool, len(ls)), idf: map[string]float64{}}
	df := map[string]int{}
	for i, l := range ls {
		ix.text[i], ix.task[i] = terms(l.Text), terms(l.Task)
		seen := map[string]bool{}
		for _, set := range []map[string]bool{ix.text[i], ix.task[i]} {
			for t := range set {
				if !seen[t] {
					seen[t] = true
					df[t]++
				}
			}
		}
	}
	n := float64(len(ls))
	for t, d := range df {
		ix.idf[t] = math.Log(1 + (n-float64(d)+0.5)/(float64(d)+0.5)) // always positive
	}
	return ix
}

// relevance is how well the task's content words q cover lesson i, in [0, 1]: the higher of the covers of its text and of the
// task it was learned in, a cover being the idf weight of the words q has over the weight of all of them. A cover of fewer than
// KeywordMinTerms words is 0.
func (ix *keywordIndex) relevance(i int, q map[string]bool) float64 {
	return max(ix.cover(ix.text[i], q), ix.cover(ix.task[i], q))
}

func (ix *keywordIndex) cover(doc, q map[string]bool) float64 {
	var hit, all float64
	n := 0
	for t := range doc {
		w := ix.idf[t]
		all += w
		if q[t] {
			hit += w
			n++
		}
	}
	if n < KeywordMinTerms || all == 0 {
		return 0
	}
	return hit / all
}

// jaccard is the share of the words of a and b that both have.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	both := 0
	for t := range a {
		if b[t] {
			both++
		}
	}
	return float64(both) / float64(len(a)+len(b)-both)
}
