package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
)

// Invisible characters are built at run time: they must not sit in the source
// where a reviewer cannot see them.
var (
	bidi = string(rune(0x202e))
	zwsp = string(rune(0x200b))
)

// catalogue joins model entries into a /models body.
func catalogue(models ...string) []byte {
	return []byte(`{"data":[` + strings.Join(models, ",") + `]}`)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// model renders one chat model whose id and per-token prices are exactly as given
// (JSON-encoded, so the id may hold control characters).
func model(id, prompt, completion string) string {
	return fmt.Sprintf(`{"id":%s,"context_length":128000,"architecture":{"modality":"text->text"},"pricing":{"prompt":%s,"completion":%s}}`,
		jsonString(id), jsonString(prompt), jsonString(completion))
}

func TestHostileCatalogueEntriesAreLeftOutAndSaneOnesKept(t *testing.T) {
	long := strings.Repeat("m", cost.MaxModelIDBytes+1)
	nanCache := `{"id":"bad/nan-cache","context_length":1000,"architecture":{"modality":"text->text"},"pricing":{"prompt":"0.000001","completion":"0.000004","input_cache_read":"NaN"}}`
	negCache := `{"id":"bad/negative-cache","context_length":1000,"architecture":{"modality":"text->text"},"pricing":{"prompt":"0.000001","completion":"0.000004","input_cache_read":"-0.5"}}`
	body := catalogue(
		model("ok/normal", "0.000001", "0.000004"),
		model("ok/free", "0", "0"),
		model("ok/unpublished-output", "0.000001", ""),
		model("ok/whitespace-price", " 0.000002 ", "0.000008"),
		model("bad/nan", "NaN", "0.000001"),
		model("bad/nan-lower", "nan", "0.000001"),
		model("bad/inf", "Inf", "0.000001"),
		model("bad/neg-inf", "-Inf", "0.000001"),
		model("bad/infinity", "+infinity", "0.000001"),
		model("bad/overflow", "1e400", "0.000001"),
		model("bad/negative-prompt", "-0.000004", "0.000001"),
		model("bad/negative-output", "0.000001", "-1"), // OpenRouter's "variable price" marker
		model("bad/not-a-number", "cheap", "0.000001"),
		model("bad/absurd-price", "1", "1"), // $1 per token = $1,000,000 per million
		nanCache,
		negCache,
		model("", "0.000001", "0.000004"),
		model("bad id with spaces", "0.000001", "0.000004"),
		model("bad/esc\x1b[2J", "0.000001", "0.000004"),
		model("bad/bidi"+bidi, "0.000001", "0.000004"),
		model("bad/zwsp"+zwsp, "0.000001", "0.000004"),
		model(long, "0.000001", "0.000004"),
	)
	entries, rejected, err := ParseDetailed(body)
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, e := range entries {
		kept[e.Model.ID] = true
	}
	for _, id := range []string{"ok/normal", "ok/free", "ok/unpublished-output", "ok/whitespace-price"} {
		if !kept[id] {
			t.Errorf("legitimate entry %q was left out; rejected: %+v", id, rejected)
		}
	}
	for id := range kept {
		if strings.HasPrefix(id, "bad") || id == "" || id == long {
			t.Errorf("hostile entry %q was kept", id)
		}
	}
	if len(entries) != 4 || len(entries)+len(rejected) != 22 {
		t.Errorf("every entry is either kept or reported: %d kept + %d rejected, want 4 + 18", len(entries), len(rejected))
	}
	for _, r := range rejected {
		if strings.ContainsAny(r.ID, "\x1b"+bidi+zwsp) || len(r.ID) > 100 {
			t.Errorf("a rejection echoes a hostile id: %q", r.ID)
		}
		if r.Reason == "" {
			t.Errorf("rejection of %q has no reason", r.ID)
		}
	}
	// Every price that survived is a real, finite, non-negative figure.
	for _, e := range entries {
		if err := e.Model.Validate(); err != nil {
			t.Errorf("%s survived Parse but fails Validate: %v", e.Model.ID, err)
		}
	}
	// A free model stays free (that is a price, not a hole), and a price that was not
	// published is 0 (the cache price then follows the input price).
	for _, e := range entries {
		switch e.Model.ID {
		case "ok/free":
			if e.Model.Price.InputPerM != 0 || e.Model.Price.OutputPerM != 0 {
				t.Errorf("free model priced %+v", e.Model.Price)
			}
		case "ok/unpublished-output":
			if e.Model.Price.OutputPerM != 0 || e.Model.Price.CacheReadPerM != e.Model.Price.InputPerM {
				t.Errorf("unpublished output price: %+v", e.Model.Price)
			}
		case "ok/whitespace-price":
			if e.Model.Price.InputPerM != 2 {
				t.Errorf("a padded price must parse: %+v", e.Model.Price)
			}
		}
	}
}

func TestHostileSizesAreRejected(t *testing.T) {
	sized := func(id, ctxLen, maxOut string) string {
		return fmt.Sprintf(`{"id":%s,"context_length":%s,"architecture":{"modality":"text->text"},"pricing":{"prompt":"0.000001","completion":"0.000002"},"top_provider":{"max_completion_tokens":%s}}`, jsonString(id), ctxLen, maxOut)
	}
	entries, rejected, err := ParseDetailed(catalogue(
		sized("ok/window", "1048576", "65536"),
		sized("ok/unknown-window", "0", "null"),
		sized("bad/negative-window", "-1", "null"),
		sized("bad/huge-window", "9007199254740993", "null"), // beyond any planner arithmetic
		sized("bad/negative-output", "128000", "-8"),
		sized("bad/huge-output", "128000", "4611686018427387904"),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || len(rejected) != 4 {
		t.Fatalf("kept %d, rejected %d: %+v %+v", len(entries), len(rejected), entries, rejected)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Model.ID, "ok/") {
			t.Errorf("kept %q", e.Model.ID)
		}
	}
}

func TestTooManyModelsRefusesTheCatalogue(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := 0; i <= MaxCatalogueEntries; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":"v/m` + fmt.Sprint(i) + `"}`)
	}
	b.WriteString(`]}`)
	if _, err := Parse([]byte(b.String())); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("a flood of models must be refused: %v", err)
	}
}

func TestCatalogueStringsArePrintable(t *testing.T) {
	body := `{"data":[{"id":"v/m","context_length":1000,"architecture":{"modality":` +
		jsonString("text->text\x1b]52;c;ZXZpbA==\a"+bidi) + `},"pricing":{"prompt":"0.000001","completion":"0.000002"},"supported_parameters":[` +
		jsonString("tools") + `,` + jsonString("rea"+zwsp+"soning\x1b[31m") + `,""],"endpoint_count":-7}]}`
	entries, err := Parse([]byte(body))
	if err != nil || len(entries) != 1 {
		t.Fatalf("%v %+v", err, entries)
	}
	e := entries[0]
	if e.Modality != "text->text" || !e.IsChat() {
		t.Errorf("modality = %q", e.Modality)
	}
	if strings.Join(e.Supported, ",") != "tools,reasoning" || !e.SupportsTools() || !e.SupportsReasoning() {
		t.Errorf("supported = %q", e.Supported)
	}
	if e.Endpoints != 0 {
		t.Errorf("a negative endpoint count is %d", e.Endpoints)
	}
}

func TestVetDropsTamperedEntriesFromACachedCopy(t *testing.T) {
	good := Entry{Model: cost.Model{ID: "v/good", ContextTokens: 1000, Price: cost.Price{InputPerM: 1, OutputPerM: 2}, Cache: cost.OpenAICacheModel()}, Modality: "text->text"}
	neg := good
	neg.Model.ID = "v/neg"
	neg.Model.Price.OutputPerM = -2
	huge := good
	huge.Model.ID = "v/huge"
	huge.Model.ContextTokens = 1 << 40
	dirty := good
	dirty.Model.ID = "v/dirty"
	dirty.Modality = "text->text\x1b[2J"
	dirty.Endpoints = -3
	got := Vet([]Entry{good, neg, huge, dirty})
	if len(got) != 2 || got[0].Model.ID != "v/good" || got[1].Model.ID != "v/dirty" {
		t.Fatalf("Vet kept %+v", got)
	}
	if got[1].Modality != "text->text" || got[1].Endpoints != 0 {
		t.Errorf("Vet must clean what it keeps: %+v", got[1])
	}
}

// What a hostile catalogue leaves behind must not disarm a budget: the table has no
// entry for the model, and the conservative fallback trips a $5 limit.
func TestHostileCatalogueCannotMakeABudgetUnreachable(t *testing.T) {
	entries, err := Parse(catalogue(
		model("v/nan", "NaN", "NaN"),
		model("v/negative", "-0.000004", "-0.00002"),
	))
	if err != nil {
		t.Fatal(err)
	}
	tbl := Table(cost.NewTable(), entries)
	for _, id := range []string{"v/nan", "v/negative"} {
		if _, ok := tbl.Lookup(id); ok {
			t.Fatalf("%s reached the price table", id)
		}
		spent := 0.0
		for i := 0; i < 100 && !(spent >= 5); i++ {
			spent += cost.Fallback(id).Price.USD(core.Usage{InputTokens: 200_000, OutputTokens: 4_000})
		}
		if !(spent >= 5) {
			t.Fatalf("%s: the fallback never reached a $5 budget (spent %v)", id, spent)
		}
	}
}

// ---- Fetch ----------------------------------------------------------------------------------

const goodCatalogue = `{"data":[{"id":"v/m","context_length":1000,"architecture":{"modality":"text->text"},"pricing":{"prompt":"0.000001","completion":"0.000002"}}]}`

func TestFetchFollowsRedirectsOnlyWithinTheOrigin(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		fmt.Fprint(w, goodCatalogue)
	}))
	defer other.Close()
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1) // same machine, different origin

	mux := http.NewServeMux()
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/v2/models", http.StatusFound) // same origin: followed
	})
	mux.HandleFunc("/api/v2/models", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, goodCatalogue) })
	mux.HandleFunc("/away/models", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherURL+"/models", http.StatusTemporaryRedirect) // another origin: refused
	})
	mux.HandleFunc("/loop/models", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop/models", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	es, err := Fetch(context.Background(), nil, srv.URL+"/api")
	if err != nil || len(es) != 1 {
		t.Fatalf("a same-origin redirect must be followed: %v %+v", err, es)
	}
	_, err = Fetch(context.Background(), nil, srv.URL+"/away")
	if err == nil || !strings.Contains(err.Error(), "another origin") || !strings.Contains(err.Error(), "localhost") {
		t.Fatalf("a redirect to another origin must be refused, naming the target: %v", err)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("the other origin was contacted %d time(s)", n)
	}
	if _, err = Fetch(context.Background(), nil, srv.URL+"/loop"); err == nil {
		t.Fatal("a redirect loop must end")
	}
	// A caller-supplied client cannot loosen the policy.
	permissive := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	if _, err = Fetch(context.Background(), permissive, srv.URL+"/away"); err == nil || elsewhere.Load() != 0 {
		t.Fatalf("a permissive client policy must not allow a cross-origin redirect: %v (contacted %d)", err, elsewhere.Load())
	}
}

func TestFetchBoundsTheBodyAndReportsTheStatus(t *testing.T) {
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat(" ", 1<<20))
		for i := 0; i < MaxCatalogueBytes>>20+2; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer huge.Close()
	if _, err := Fetch(context.Background(), nil, huge.URL); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("an endless catalogue must be cut off with a clear error: %v", err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "\x1b[2Jdown", 503) }))
	defer down.Close()
	if _, err := Fetch(context.Background(), nil, down.URL); err == nil || !strings.Contains(err.Error(), "http 503") || strings.ContainsRune(err.Error(), 0x1b) {
		t.Fatalf("status error: %v", err)
	}
}
