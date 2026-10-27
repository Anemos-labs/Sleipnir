package reward

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// The repository is searched once per literal, and for at most so many literals; past that the answer is unknown.
func TestRepositorySearchesAreCachedAndBounded(t *testing.T) {
	calls := 0
	s := cachedSearch(func(lit string) (bool, error) { calls++; return lit == "a", nil }, 2)
	for i := 0; i < 100; i++ {
		if had, err := s("a"); !had || err != nil {
			t.Fatal(had, err)
		}
	}
	if had, err := s("b"); had || err != nil {
		t.Fatal(had, err)
	}
	if _, err := s("c"); !errors.Is(err, errSearchBudget) {
		t.Fatalf("a third literal: %v", err)
	}
	if calls != 2 {
		t.Fatalf("%d searches, want 2", calls)
	}
}

// Literals nothing compares against spend none of the search budget, so a comparison after many of them is still judged.
func TestUncomparedLiteralsDoNotSpendTheSearchBudget(t *testing.T) {
	var hidden, added strings.Builder
	hidden.WriteString("package x\nfunc TestX(t *testing.T) {\n")
	for i := 0; i < maxHardcodeSearches+10; i++ {
		lit := fmt.Sprintf("produced value number %d here", i)
		fmt.Fprintf(&hidden, "\t_ = %q\n", lit)
		fmt.Fprintf(&added, "+\tlog.Println(%q)\n", lit)
	}
	hidden.WriteString("\tif got != \"the compared answer text\" { t.Fatal() }\n}\n")
	added.WriteString("+\tif p == \"the compared answer text\" { return nil }\n")
	task := &rl.Task{Verifier: rl.Verifier{Cmd: "go test ./...", Hidden: map[string]string{"x_hidden_test.go": "text:" + hidden.String()}}}
	diff := gitDiff("x.go", hunkOf(strings.Split(strings.TrimSuffix(added.String(), "\n"), "\n")...))
	ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(100, ""))))
	dh := core.HashString(diff)
	ep.Outcome.Diff = dh
	searches := 0
	mustScore(t, ep, task, DefaultConfig(), WithRepo(DiffMap{dh: diff}, func(string) (bool, error) { searches++; return false, nil }))
	if !hasFlag(ep, fHard) {
		t.Errorf("the comparison was not judged:\n%s", strings.Join(ep.Reward.Notes, "\n"))
	}
	if searches > 2 {
		t.Errorf("%d searches, want only the compared literal's", searches)
	}
}
