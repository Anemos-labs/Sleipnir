package kv

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// Doc is one searchable turn: where it is (Scope, such as an earlier session's id, "" for the archive searched), its id, and
// the search text the archive keeps for it.
type Doc struct {
	Scope   string
	Turn    core.TurnID
	preview string
}

// NewDoc is the searchable form of turn t of scope: the same capped preview an Archive keeps, so a search over the turns of
// several sessions holds a few hundred bytes per turn, never the turns.
func NewDoc(scope string, t core.Turn) Doc {
	return Doc{Scope: scope, Turn: t.ID, preview: buildPreview(searchText(t))}
}

// Hit is a search result.
type Hit struct {
	Scope   string // the Doc's scope
	Turn    core.TurnID
	Score   int     // how many of the query's terms the turn has
	Rank    float64 // its BM25 score: what the hits are ordered by
	Snippet string
}

// BM25's parameters: how fast repeats of a term stop adding (k1), and how much a long text is held against its matches (b).
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// stopwords are the function words a query is written with, which every turn has. A query of nothing else keeps them.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an and are as at be but by did do does for from had has have how i if in into is it its
		me my no not of on or our so that the their then there these this those to was we were what when where which who why will
		with you your`) {
		m[w] = true
	}
	return m
}()

// maxQueryTerms bounds the distinct words of a query that are ranked.
const maxQueryTerms = 64

// queryTerms are the distinct lower-cased words of query, without stopwords unless nothing else is left.
func queryTerms(query string) []string {
	var all, content []string
	seen := map[string]bool{}
	for _, t := range strings.Fields(strings.ToLower(query)) {
		if seen[t] {
			continue
		}
		if len(all) == maxQueryTerms {
			break // a query is a few words; past this many, more terms only multiply the work over every turn
		}
		seen[t] = true
		all = append(all, t)
		if !stopwords[t] {
			content = append(content, t)
		}
	}
	if len(content) > 0 {
		return content
	}
	return all
}

// Rank scores docs against query with Okapi BM25 and returns at most limit hits (all for limit <= 0), best first; equal ranks
// put the newer turn first. A term matches where it appears in a turn's text, also inside a longer word, so a query in a
// language written without spaces still finds its turns. Each term counts by its rarity across docs, so a word every turn
// has decides nothing and a rare identifier or error message decides much.
func Rank(docs []Doc, query string, limit int) []Hit {
	terms := queryTerms(query)
	if len(terms) == 0 || len(docs) == 0 {
		return nil
	}
	tf := make([][]int, len(docs))
	df := make([]int, len(terms))
	var total float64
	for i, d := range docs {
		total += float64(len(d.preview))
		for j, t := range terms {
			if n := strings.Count(d.preview, t); n > 0 {
				if tf[i] == nil {
					tf[i] = make([]int, len(terms))
				}
				tf[i][j] = n
				df[j]++
			}
		}
	}
	n := float64(len(docs))
	avg := max(total/n, 1)
	idf := make([]float64, len(terms))
	for j := range terms {
		idf[j] = math.Log(1 + (n-float64(df[j])+0.5)/(float64(df[j])+0.5)) // always positive
	}
	var hits []Hit
	for i, d := range docs {
		if tf[i] == nil {
			continue
		}
		h := Hit{Scope: d.Scope, Turn: d.Turn}
		first := -1
		norm := bm25K1 * (1 - bm25B + bm25B*float64(len(d.preview))/avg)
		for j, t := range terms {
			f := float64(tf[i][j])
			if f == 0 {
				continue
			}
			h.Score++
			h.Rank += idf[j] * f * (bm25K1 + 1) / (f + norm)
			if k := strings.Index(d.preview, t); first < 0 || k < first {
				first = k
			}
		}
		h.Snippet = snippet(d.preview, first)
		hits = append(hits, h)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Rank != hits[j].Rank {
			return hits[i].Rank > hits[j].Rank
		}
		return hits[i].Turn > hits[j].Turn // newer first on ties
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// snippet is the text around byte offset at of pv, cut on character boundaries, on one line.
func snippet(pv string, at int) string {
	lo, hi := max(at-60, 0), min(at+140, len(pv))
	for lo > 0 && !utf8.RuneStart(pv[lo]) {
		lo--
	}
	for hi < len(pv) && !utf8.RuneStart(pv[hi]) {
		hi++
	}
	return strings.Join(strings.Fields(pv[lo:hi]), " ")
}
