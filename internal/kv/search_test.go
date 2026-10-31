package kv

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A word every turn has decides nothing; the rare identifier of the query decides which turn comes first, and a stopword in
// the query matches nothing on its own.
func TestSearchRanksByTheRarityOfTheWords(t *testing.T) {
	a := NewArchive(events.NewMemBlobs())
	for i := 1; i <= 9; i++ {
		_ = a.Put("be-1", turnOf(i, fmt.Sprintf("the build ran the build again and the build passed, build %d", i)))
	}
	_ = a.Put("be-1", turnOf(10, "the migration failed: ErrNoSuchTable in the orders table"))
	hits := a.Search("be-1", "the build errnosuchtable", 3)
	if len(hits) == 0 || hits[0].Turn != 10 {
		t.Fatalf("the turn with the rare word should come first: %+v", hits)
	}
	if hits[0].Score != 1 || len(hits) < 2 || hits[0].Rank <= hits[1].Rank {
		t.Fatalf("score and rank: %+v", hits)
	}
	if h := a.Search("be-1", "the of", 20); len(h) != 10 {
		t.Fatalf("a query of stopwords alone keeps them: %d hits", len(h))
	}
}

// Rank orders docs of several scopes on one scale, and each hit says which scope it is from.
func TestRankAcrossScopes(t *testing.T) {
	docs := []Doc{
		NewDoc("s1", turnOf(3, "configured the reverse proxy for websocket upgrades")),
		NewDoc("s2", turnOf(3, "wrote the README")),
		NewDoc("s2", turnOf(7, "websocket upgrades fail behind the proxy without Connection: upgrade")),
	}
	hits := Rank(docs, "websocket upgrade proxy", 0)
	if len(hits) != 2 || hits[0].Scope != "s2" || hits[0].Turn != 7 || hits[1].Scope != "s1" {
		t.Fatalf("hits = %+v", hits)
	}
}

// A query of more distinct words than are ranked keeps the first maxQueryTerms of them.
func TestQueryTermsAreBounded(t *testing.T) {
	var words []string
	for i := 0; i < 10*maxQueryTerms; i++ {
		words = append(words, fmt.Sprintf("w%d", i))
	}
	if got := queryTerms(strings.Join(words, " ")); len(got) != maxQueryTerms || got[0] != "w0" {
		t.Errorf("%d terms, first %q", len(got), got[0])
	}
}

// Stopwords do not use up the bound: a query of a stopword and 64 content words ranks all 64, the last one too.
func TestQueryTermsBoundCountsContentWords(t *testing.T) {
	words := []string{"the"}
	for i := range maxQueryTerms - 1 {
		words = append(words, fmt.Sprintf("w%d", i))
	}
	words = append(words, "needle")
	got := queryTerms(strings.Join(words, " "))
	if len(got) != maxQueryTerms || got[len(got)-1] != "needle" {
		t.Errorf("queryTerms kept %d words, the last %q", len(got), got[len(got)-1])
	}
	if got := queryTerms("the of and"); len(got) != 3 {
		t.Errorf("a query of stopwords only keeps them: %v", got)
	}
}
