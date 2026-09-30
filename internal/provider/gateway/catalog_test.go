package gateway

import (
	"math"
	"os"
	"testing"

	"github.com/reee344/sleipnir/internal/cost"
)

func TestParseRealCatalogueSample(t *testing.T) {
	b, err := os.ReadFile("testdata/models.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Entry{}
	for _, e := range entries {
		byID[e.Model.ID] = e
	}
	ds := byID["deepseek/deepseek-v4.1-flash"]
	if ds.Model.ID == "" {
		t.Fatal("deepseek missing")
	}
	// 5e-9 $/token = $0.005 per million.
	if math.Abs(ds.Model.Price.InputPerM-0.005) > 1e-9 || math.Abs(ds.Model.Price.CacheReadPerM-0.00125) > 1e-9 {
		t.Fatalf("price conversion: %+v", ds.Model.Price)
	}
	w := ds.Model.Price.Weights()
	if math.Abs(w.Read-0.25) > 1e-9 || w.Write5m != 1 || w.Output != 2 {
		t.Fatalf("weights = %+v (want read 0.25, no write premium, output 2x)", w)
	}
	if !ds.SupportsTools() || !ds.SupportsReasoning() || !ds.IsChat() || ds.Model.ContextTokens != 1048576 {
		t.Fatalf("capabilities: %+v", ds)
	}
	if laya := byID["convaiinnovations/laya"]; laya.IsChat() || laya.SupportsTools() {
		t.Fatal("decision models are not chat models")
	}
	// The catalogue table finds models by full or short id.
	tbl := Table(cost.NewTable(), entries)
	if _, ok := tbl.Lookup("deepseek/deepseek-v4.1-flash"); !ok {
		t.Fatal("full id lookup")
	}
	if _, ok := tbl.Lookup("deepseek-v4-1-flash"); !ok {
		t.Fatal("short id lookup")
	}
}

func TestMissingCachePriceFallsBackToInput(t *testing.T) {
	es, err := Parse([]byte(`{"data":[{"id":"a/b","context_length":1000,"architecture":{"modality":"text->text"},"pricing":{"prompt":"0.000001","completion":"0.000002"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	p := es[0].Model.Price
	if p.CacheReadPerM != p.InputPerM || p.OutputPerM != 2 {
		t.Fatalf("%+v", p)
	}
}

// Catalogues of the OpenAI kind list ids and nothing else. Their models have no modality, and `sleipnir models` showed
// none of them: a header and an empty table.
func TestAModelWithoutAModalityIsListedAsChat(t *testing.T) {
	es, err := Parse([]byte(`{"object":"list","data":[{"id":"gpt-x","object":"model","owned_by":"openai"},{"id":"qwen3:8b","object":"model"},` +
		`{"id":"dall-e","architecture":{"modality":"text->image"},"pricing":{"prompt":"0.000001","completion":"0.000002"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	chat := map[string]bool{}
	for _, e := range es {
		chat[e.Model.ID] = e.IsChat()
	}
	if !chat["gpt-x"] || !chat["qwen3:8b"] || chat["dall-e"] || len(chat) != 3 {
		t.Fatalf("IsChat by model: %v (a catalogue that does not say is taken as chat; one that says text->image is not)", chat)
	}
}
