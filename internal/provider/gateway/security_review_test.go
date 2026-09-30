package gateway

// Security review repro for docs/reviews/security-robustness.md, now a regression test.

import (
	"math"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
)

// S28b (fixed): catalogue prices are parsed with strconv.ParseFloat, which accepts "NaN", "Inf"
// and negatives. A NaN or negative price makes every dollar computation NaN/negative, and
// `spend >= budget` is false for NaN: the per-agent and swarm budget breakers fail open. Parse
// now leaves such entries out (the model is priced with cost.Fallback's conservative estimate
// instead); see catalog_hostile_test.go for the whole family.
func TestSec_S28b_CatalogueRejectsNaNAndNegativePrices(t *testing.T) {
	body := []byte(`{"data":[
	 {"id":"vendor/nan-model","context_length":128000,"architecture":{"modality":"text->text"},"pricing":{"prompt":"NaN","completion":"NaN","input_cache_read":"NaN"}},
	 {"id":"vendor/negative-model","context_length":128000,"architecture":{"modality":"text->text"},"pricing":{"prompt":"-0.000004","completion":"-0.00002"}}]}`)
	entries, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		p := e.Model.Price
		usd := p.USD(core.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
		t.Logf("%s: price=%+v -> $%v for 1M in + 1M out; budget check 'usd >= 5' = %v", e.Model.ID, p, usd, usd >= 5)
		for name, v := range map[string]float64{"input": p.InputPerM, "output": p.OutputPerM, "cache_read": p.CacheReadPerM} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				t.Errorf("S28b: %s %s price %v accepted from the catalogue; spend accounting becomes NaN/negative and budgets never trip", e.Model.ID, name, v)
			}
		}
	}
	// The loop above is vacuous if nothing was returned: say what happened to each entry.
	if len(entries) != 0 {
		t.Errorf("S28b: %d hostile entries were kept: %+v", len(entries), entries)
	}
	_, rejected, err := ParseDetailed(body)
	if err != nil || len(rejected) != 2 {
		t.Fatalf("both hostile entries must be reported as rejected: %+v %v", rejected, err)
	}
}
