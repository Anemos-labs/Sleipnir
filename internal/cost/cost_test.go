package cost

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
)

func goodModel(id string) Model {
	return Model{
		ID: id, Provider: "gateway", ContextTokens: 128_000, MaxOutput: 16_000,
		Price: Price{InputPerM: 1, OutputPerM: 4, CacheReadPerM: 0.1, CacheWrite5mPerM: 1, CacheWrite1hPerM: 1},
		Cache: OpenAICacheModel(),
	}
}

func TestPriceValidate(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	for _, tc := range []struct {
		name string
		mut  func(*Price)
		bad  string // substring of the complaint; "" means valid
	}{
		{"typical", func(p *Price) {}, ""},
		{"free is a real price", func(p *Price) { *p = Price{} }, ""},
		{"the ceiling itself", func(p *Price) { p.OutputPerM = MaxPricePerM }, ""},
		{"NaN input", func(p *Price) { p.InputPerM = nan }, "input price is not a number"},
		{"NaN output", func(p *Price) { p.OutputPerM = nan }, "output price is not a number"},
		{"NaN cache read", func(p *Price) { p.CacheReadPerM = nan }, "cache read price is not a number"},
		{"NaN cache write 5m", func(p *Price) { p.CacheWrite5mPerM = nan }, "cache write (5m)"},
		{"NaN cache write 1h", func(p *Price) { p.CacheWrite1hPerM = nan }, "cache write (1h)"},
		{"+Inf", func(p *Price) { p.InputPerM = inf }, "infinite"},
		{"-Inf", func(p *Price) { p.OutputPerM = -inf }, "infinite"},
		{"negative input", func(p *Price) { p.InputPerM = -0.000004 }, "negative"},
		{"negative output", func(p *Price) { p.OutputPerM = -1 }, "negative"},
		{"negative cache read", func(p *Price) { p.CacheReadPerM = -1e-9 }, "negative"},
		{"past the ceiling", func(p *Price) { p.OutputPerM = MaxPricePerM * 1.0001 }, "beyond any real model"},
		{"astronomical", func(p *Price) { p.InputPerM = 1e300 }, "beyond any real model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := goodModel("a/b").Price
			tc.mut(&p)
			err := p.Validate()
			switch {
			case tc.bad == "" && err != nil:
				t.Fatalf("valid price refused: %v", err)
			case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
				t.Fatalf("Validate = %v, want a complaint containing %q", err, tc.bad)
			}
		})
	}
}

func TestModelValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Model)
		bad  string
	}{
		{"typical", func(m *Model) {}, ""},
		{"unknown window and output are zero, not invalid", func(m *Model) { m.ContextTokens, m.MaxOutput = 0, 0 }, ""},
		{"window at the ceiling", func(m *Model) { m.ContextTokens = MaxWindowTokens }, ""},
		{"vendor prefixed id", func(m *Model) { m.ID = "deepseek/deepseek-v4.1-flash:free" }, ""},
		{"empty id", func(m *Model) { m.ID = "" }, "empty model id"},
		{"blank id", func(m *Model) { m.ID = "  \t" }, "empty model id"},
		{"id with a space", func(m *Model) { m.ID = "a b" }, "unprintable or whitespace"},
		{"id with ESC", func(m *Model) { m.ID = "a\x1b[2Jb" }, "unprintable or whitespace"},
		{"id with a newline", func(m *Model) { m.ID = "a\nb" }, "unprintable or whitespace"},
		{"id with a bidi override", func(m *Model) { m.ID = "a" + string(rune(0x202e)) + "b" }, "unprintable or whitespace"},
		{"id with a tag character", func(m *Model) { m.ID = "a\U000E0041b" }, "unprintable or whitespace"},
		{"id with a zero width space", func(m *Model) { m.ID = "a" + string(rune(0x200b)) + "b" }, "unprintable or whitespace"},
		{"id that is not UTF-8", func(m *Model) { m.ID = "a\xffb" }, "not valid UTF-8"},
		{"id of 257 bytes", func(m *Model) { m.ID = strings.Repeat("a", MaxModelIDBytes+1) }, "limit"},
		{"id of 256 bytes", func(m *Model) { m.ID = strings.Repeat("a", MaxModelIDBytes) }, ""},
		{"NaN price", func(m *Model) { m.Price.InputPerM = math.NaN() }, "not a number"},
		{"negative price", func(m *Model) { m.Price.OutputPerM = -20 }, "negative"},
		{"negative window", func(m *Model) { m.ContextTokens = -1 }, "context window"},
		{"absurd window", func(m *Model) { m.ContextTokens = MaxWindowTokens + 1 }, "context window"},
		{"a window that overflows the planner's arithmetic", func(m *Model) { m.ContextTokens = math.MaxInt64 }, "context window"},
		{"negative output limit", func(m *Model) { m.MaxOutput = -5 }, "output limit"},
		{"absurd output limit", func(m *Model) { m.MaxOutput = math.MaxInt32 * 4 }, "output limit"},
		{"negative granularity", func(m *Model) { m.Cache.Granularity = -16 }, "granularity"},
		{"negative minimum prefix", func(m *Model) { m.Cache.MinPrefixTokens = -1 }, "minimum cacheable prefix"},
		{"zero cache lifetime", func(m *Model) { m.Cache.TTLs = []time.Duration{0} }, "lifetime"},
		{"negative cache lifetime", func(m *Model) { m.Cache.TTLs = []time.Duration{-time.Hour} }, "lifetime"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := goodModel("vendor/model")
			tc.mut(&m)
			err := m.Validate()
			switch {
			case tc.bad == "" && err != nil:
				t.Fatalf("valid model refused: %v", err)
			case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
				t.Fatalf("Validate = %v, want a complaint containing %q", err, tc.bad)
			}
		})
	}
}

// The built-in table and the fallback are what most runs are priced with: they
// must pass the check every outside model has to pass.
func TestBuiltInModelsAreValid(t *testing.T) {
	all := Defaults().All()
	if len(all) < 10 {
		t.Fatalf("Defaults() lost models: %d", len(all))
	}
	for _, m := range all {
		if err := m.Validate(); err != nil {
			t.Errorf("built-in model %s: %v", m.ID, err)
		}
	}
	if err := Fallback("whatever/model").Validate(); err != nil {
		t.Errorf("Fallback: %v", err)
	}
}

func TestTablePutRefusesHostileModels(t *testing.T) {
	tbl := NewTable(goodModel("vendor/model"))
	before, _ := tbl.Lookup("vendor/model")

	nanPrice := goodModel("vendor/model")
	nanPrice.Price.InputPerM = math.NaN()
	negPrice := goodModel("vendor/model")
	negPrice.Price.OutputPerM = -20
	hugeWindow := goodModel("vendor/model")
	hugeWindow.ContextTokens = math.MaxInt64
	for name, m := range map[string]Model{"NaN price": nanPrice, "negative price": negPrice, "absurd window": hugeWindow} {
		err := tbl.Put(m)
		if err == nil {
			t.Fatalf("%s: Put accepted a hostile model", name)
		}
		if !strings.Contains(err.Error(), "vendor/model") {
			t.Errorf("%s: the refusal should name the model: %v", name, err)
		}
		if got, ok := tbl.Lookup("vendor/model"); !ok || !reflect.DeepEqual(got, before) {
			t.Fatalf("%s: a refused model replaced the good one: %+v", name, got)
		}
	}

	// A model that was never good stays unknown, so callers fall through to Fallback.
	bad := goodModel("other/unpriced")
	bad.Price.OutputPerM = math.Inf(1)
	if err := tbl.Put(bad); err == nil {
		t.Fatal("Put accepted an infinite price")
	}
	if _, ok := tbl.Lookup("other/unpriced"); ok {
		t.Fatal("a refused model is visible in the table")
	}

	// NewTable leaves hostile models out rather than storing them.
	tbl2 := NewTable(bad, goodModel("ok/model"))
	if len(tbl2.All()) != 1 {
		t.Fatalf("NewTable stored %d models, want only the valid one", len(tbl2.All()))
	}
}

func TestPutErrorDoesNotEchoControlCharacters(t *testing.T) {
	m := goodModel("evil\x1b]52;c;ZXZpbA==\a" + string(rune(0x202e)) + strings.Repeat("x", 500))
	err := NewTable().Put(m)
	if err == nil {
		t.Fatal("Put accepted a model whose id has control characters")
	}
	if strings.ContainsAny(err.Error(), "\x1b\a"+string(rune(0x202e))) || len(err.Error()) > 300 {
		t.Fatalf("the refusal echoes hostile text: %q", err.Error())
	}
}

// A price that slipped past every check must still not disarm a budget: NaN and
// negative dollar figures compare false against a limit, +Inf trips it.
func TestUSDNeverReturnsNaNOrNegative(t *testing.T) {
	u := core.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	if got := (Price{InputPerM: 4, OutputPerM: 20}).USD(u); math.Abs(got-24) > 1e-9 {
		t.Fatalf("ordinary pricing changed: %v", got)
	}
	if got := (Price{}).USD(u); got != 0 {
		t.Fatalf("a free model costs %v", got)
	}
	for name, p := range map[string]Price{
		"NaN":      {InputPerM: math.NaN(), OutputPerM: math.NaN()},
		"negative": {InputPerM: -4, OutputPerM: -20},
		"NaN cache": {
			InputPerM: 4, OutputPerM: 20, CacheReadPerM: math.NaN(),
		},
	} {
		usd := p.USD(core.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 10})
		if !(usd >= 5) {
			t.Errorf("%s: %v does not trip a $5 budget", name, usd)
		}
		if math.IsNaN(usd) || usd < 0 {
			t.Errorf("%s: USD returned %v", name, usd)
		}
	}
	// Negative token counts (a bug or a hostile server upstream of the clamp) fail closed too.
	if usd := (Price{InputPerM: 4, OutputPerM: 20}).USD(core.Usage{InputTokens: -1_000_000_000}); !(usd >= 5) {
		t.Errorf("negative usage priced at %v", usd)
	}
}

// End to end at this package's boundary: a hostile catalogue entry cannot make a
// budget loop run forever.
func TestHostileCatalogueCannotMakeABudgetUnreachable(t *testing.T) {
	hostile := []Model{
		func() Model {
			m := goodModel("v/nan")
			m.Price = Price{InputPerM: math.NaN(), OutputPerM: math.NaN()}
			return m
		}(),
		func() Model { m := goodModel("v/neg"); m.Price = Price{InputPerM: -4, OutputPerM: -20}; return m }(),
		func() Model { m := goodModel("v/inf"); m.Price = Price{InputPerM: math.Inf(-1)}; return m }(),
	}
	tbl := NewTable(hostile...)
	for _, m := range hostile {
		got, ok := tbl.Lookup(m.ID)
		if ok {
			t.Fatalf("%s reached the table: %+v", m.ID, got)
		}
		// What callers do next: price with the conservative fallback.
		price := Fallback(m.ID).Price
		spent := 0.0
		for i := 0; i < 1000 && !(spent >= 5); i++ {
			spent += price.USD(core.Usage{InputTokens: 100_000, OutputTokens: 5_000})
		}
		if !(spent >= 5) {
			t.Fatalf("%s: a $5 budget was never reached (spent %v)", m.ID, spent)
		}
	}
}

func TestWeightsIgnoreGarbage(t *testing.T) {
	want := Weights{Read: 0.1, Write5m: 1.25, Write1h: 2, Output: 5}
	for name, p := range map[string]Price{
		"zero":     {},
		"NaN":      {InputPerM: math.NaN(), OutputPerM: 1},
		"Inf":      {InputPerM: math.Inf(1), OutputPerM: 1},
		"negative": {InputPerM: -1, OutputPerM: 1},
		"NaN out":  {InputPerM: 1, OutputPerM: math.NaN()},
	} {
		if got := p.Weights(); got != want {
			t.Errorf("%s: Weights = %+v, want the default structure %+v", name, got, want)
		}
	}
	w := Price{InputPerM: 2, OutputPerM: 10, CacheReadPerM: 0.5}.Weights()
	if w.Read != 0.25 || w.Output != 5 || w.Write5m != 1.25 || w.Write1h != 2 {
		t.Errorf("ordinary weights changed: %+v", w)
	}
}
