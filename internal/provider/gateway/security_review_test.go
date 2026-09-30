package gateway

// Security review repro for docs/reviews/security-robustness.md (gated: SLEIPNIR_REVIEW=1,
// asserts the SECURE behaviour and fails while the finding is open).

import (
	"math"
	"os"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
)

// S28b: catalogue prices are parsed with strconv.ParseFloat, which accepts "NaN", "Inf" and
// negatives. A NaN or negative price makes every dollar computation NaN/negative, and
// `spend >= budget` is false for NaN: the per-agent and swarm budget breakers fail open.
func TestSecReview_S28b_CatalogueAcceptsNaNAndNegativePrices(t *testing.T) {
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("security-review repro: set SLEIPNIR_REVIEW=1")
	}
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
}
