package skills

import (
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
)

const (
	// DefaultListingTokens is the listing budget when the caller passes none. At
	// roughly 55 tokens per skill that is about 27 skills with full descriptions;
	// the shared layer is read on every request of every agent, so it stays small.
	DefaultListingTokens = 1500
	// maxListedDescription is the longest description a listing line carries
	// while the budget allows it.
	maxListedDescription = 240
)

// descLadder are the per-description caps, in runes, tried from generous to
// tight until the listing fits. 0 means names only.
var descLadder = []int{maxListedDescription, 160, 110, 80, 56, 40, 24, 0}

// Listing renders the always-in-context part of the catalog: one line per
// skill the model may invoke, "name: description", in name order.
//
// The text is a pure function of the catalog, budgetTokens and est, so it is
// byte-stable and fit for a cached prompt layer. Descriptions are forced onto
// one line, have markup that could pass for the harness's own framing
// neutralised, and are cut with a trailing "…" when they exceed the cap. When
// the whole listing does not fit budgetTokens (0 means DefaultListingTokens)
// the descriptions are shortened uniformly, step by step, down to bare names; if
// even the names do not fit, the skills that matter least are left out (extras
// first, then user, then project skills, and later names before earlier ones)
// and a last line says how many. Skills with DisableModelInvocation are not
// listed: the model must not be told about what it may not use.
//
// est decides what "fits". A calibrating estimator that changes between runs
// would change the listing; pass a fixed one (core.NewBytesEstimator() that is
// never given observations) when byte stability across runs matters. nil means
// exactly that.
func (c *Catalog) Listing(budgetTokens int, est core.Estimator) string {
	if c == nil {
		return ""
	}
	if est == nil {
		est = core.NewBytesEstimator()
	}
	if budgetTokens <= 0 {
		budgetTokens = DefaultListingTokens
	}
	var listed []*Skill
	for i := range c.skills {
		if !c.skills[i].DisableModelInvocation {
			listed = append(listed, &c.skills[i])
		}
	}
	if len(listed) == 0 {
		return ""
	}
	for _, capRunes := range descLadder {
		text := renderListing(listed, capRunes, 0)
		if est.Tokens(text) <= budgetTokens {
			return text
		}
	}

	// Even bare names do not fit: keep the k most relevant skills. Relevance is
	// precedence first (project skills before user skills before plugins), then
	// name; the survivors are still rendered in name order.
	byRelevance := append([]*Skill(nil), listed...)
	sort.SliceStable(byRelevance, func(i, j int) bool {
		if byRelevance[i].rank != byRelevance[j].rank {
			return byRelevance[i].rank < byRelevance[j].rank
		}
		return byRelevance[i].Name < byRelevance[j].Name
	})
	fits := func(k int) (string, bool) {
		keep := append([]*Skill(nil), byRelevance[:k]...)
		sort.Slice(keep, func(i, j int) bool { return keep[i].Name < keep[j].Name })
		text := renderListing(keep, 0, len(listed)-k)
		return text, est.Tokens(text) <= budgetTokens
	}
	lo, hi := 0, len(byRelevance) // the largest k that fits lies in [lo, hi)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if _, ok := fits(mid); ok {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	text, ok := fits(lo)
	if !ok { // not even the note fits: an empty listing beats a broken one
		return ""
	}
	return text
}

// renderListing renders skills with descriptions capped at capRunes (0: names
// only). omitted > 0 appends the note about skills that did not fit.
func renderListing(skills []*Skill, capRunes, omitted int) string {
	var b strings.Builder
	for i, s := range skills {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s.Name)
		if capRunes > 0 {
			if d := mdfile.OneLine(mdfile.EscapeTags(s.Summary()), capRunes); d != "" {
				b.WriteString(": ")
				b.WriteString(d)
			}
		}
	}
	if omitted > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "(%d more skills are not listed; they can still be loaded by name with the Skill tool)", omitted)
	}
	return b.String()
}
