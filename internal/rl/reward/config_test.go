package reward

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/cost"
)

func TestDefaultConfigMatchesTheDoc(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	// docs/TRAINING-DATA.md section 5.
	want := map[string]float64{
		CompOutcome: 1.0, CompHonestDone: 0.1, CompFalseDone: 0.2, CompCost: 0.15,
		CompRequests: 0.05, CompTime: 0.05, CompProtocol: 0.1,
	}
	for k, v := range want {
		if cfg.Weights[k] != v {
			t.Errorf("weight %s = %v, want %v", k, cfg.Weights[k], v)
		}
	}
	// Two calls must not share maps (no global mutable state).
	a, b := DefaultConfig(), DefaultConfig()
	a.Weights[CompOutcome] = 42
	if b.Weights[CompOutcome] == 42 {
		t.Fatal("DefaultConfig shares its weight map between calls")
	}
	if !cfg.Probes {
		t.Error("probes should default to on")
	}
}

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr []string // substrings, all must appear
		check   func(t *testing.T, c Config)
	}{
		{name: "empty object keeps defaults", in: `{}`, check: func(t *testing.T, c Config) {
			if c.Weights[CompOutcome] != 1 || c.Target.ID == "" || c.TargetName != DefaultTargetName {
				t.Errorf("defaults lost: %+v", c)
			}
		}},
		{name: "weights merge over defaults", in: `{"weights":{"cost":0.5}}`, check: func(t *testing.T, c Config) {
			if c.Weights[CompCost] != 0.5 || c.Weights[CompOutcome] != 1 {
				t.Errorf("weights: %v", c.Weights)
			}
		}},
		{name: "utf-8 BOM tolerated", in: "\xef\xbb\xbf{\"clip\":[-2,2]}", check: func(t *testing.T, c Config) {
			if c.Clip != [2]float64{-2, 2} {
				t.Errorf("clip: %v", c.Clip)
			}
		}},
		{name: "target by name", in: `{"target":"openai"}`, check: func(t *testing.T, c Config) {
			if c.Target.ID != "openai-like" || !c.Target.Cache.Auto {
				t.Errorf("target: %+v", c.Target)
			}
		}},
		{name: "target from the cost table", in: `{"target":"claude-opus-4-8"}`, check: func(t *testing.T, c Config) {
			if !strings.HasPrefix(c.Target.ID, "claude-opus-4-8") {
				t.Errorf("target: %+v", c.Target.ID)
			}
		}},
		{name: "unknown top-level field", in: `{"wieghts":{}}`, wantErr: []string{"wieghts", "unknown field"}},
		{name: "unknown weight names the field", in: `{"weights":{"outcom":1}}`, wantErr: []string{"weights.outcom", "unknown component"}},
		{name: "negative weight", in: `{"weights":{"cost":-0.1}}`, wantErr: []string{"weights.cost", ">= 0"}},
		{name: "unknown cap", in: `{"caps":{"protocl":3}}`, wantErr: []string{"caps.protocl"}},
		{name: "negative cap", in: `{"caps":{"protocol":-1}}`, wantErr: []string{"caps.protocol"}},
		{name: "unknown detector", in: `{"detectors":{"vibes":true}}`, wantErr: []string{"detectors.vibes"}},
		{name: "clip inverted", in: `{"clip":[2,-2]}`, wantErr: []string{"clip"}},
		{name: "unknown target", in: `{"target":"nonesuch"}`, wantErr: []string{"target", "nonesuch"}},
		{name: "wrong type names the field", in: `{"weights":{"cost":"high"}}`, wantErr: []string{"weights.cost"}},
		{name: "wrong type for bool", in: `{"probes":"yes"}`, wantErr: []string{"probes"}},
		{name: "syntax error", in: `{"weights":`, wantErr: []string{"reward config"}},
		{name: "trailing data", in: `{} {}`, wantErr: []string{"unexpected data"}},
		{name: "empty document", in: ``, wantErr: []string{"empty"}},
		{name: "whitespace document", in: "  \n ", wantErr: []string{"empty"}},
		{name: "relative workspace root", in: `{"workspace_roots":["work/tree"]}`, wantErr: []string{"workspace_roots[0]", "absolute"}},
		{name: "windows workspace roots", in: `{"workspace_roots":["D:\\work\\tree","c:/work/other","\\\\server\\share\\tree"]}`, check: func(t *testing.T, c Config) {
			if len(c.WorkspaceRoots) != 3 {
				t.Errorf("roots: %q", c.WorkspaceRoots)
			}
		}},
		{name: "drive-relative workspace root", in: `{"workspace_roots":["D:work"]}`, wantErr: []string{"workspace_roots[0]", "absolute"}},
		{name: "negative engines", in: `{"reprice":{"engines":-1}}`, wantErr: []string{"reprice.engines"}},
		{name: "several problems reported together", in: `{"weights":{"cost":-1,"nope":1},"caps":{"idle":-2}}`,
			wantErr: []string{"weights.cost", "weights.nope", "caps.idle"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseConfig([]byte(tc.in))
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatalf("expected an error containing %v, got config %+v", tc.wantErr, c)
				}
				for _, s := range tc.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, c)
		})
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "rewards.json")
	if err := os.WriteFile(good, []byte(`{"weights":{"outcome":2},"target":"marketplace","probes":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(good)
	if err != nil {
		t.Fatal(err)
	}
	if c.Weights[CompOutcome] != 2 || c.Target.ID != "marketplace-like" || c.Probes {
		t.Errorf("loaded config wrong: %+v", c)
	}

	if _, err := LoadConfig(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file must be an error")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"weights":{"cost":-3}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bad); err == nil || !strings.Contains(err.Error(), "bad.json") || !strings.Contains(err.Error(), "weights.cost") {
		t.Errorf("error should name the file and the field, got %v", err)
	}
	huge := filepath.Join(dir, "huge.json")
	if err := os.WriteFile(huge, []byte(`{"tags":"`+strings.Repeat("x", 2<<20)+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(huge); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("oversized file must be refused, got %v", err)
	}
}

func TestConfigRoundTripsThroughJSON(t *testing.T) {
	c := DefaultConfig()
	c.Weights[CompCost] = 0.3
	c.Clip = [2]float64{-1, 3}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseConfig(b)
	if err != nil {
		t.Fatalf("own output does not parse: %v\n%s", err, b)
	}
	if back.Weights[CompCost] != 0.3 || back.Clip != c.Clip || back.TargetName != c.TargetName {
		t.Errorf("round trip changed the config: %+v", back)
	}
}

func TestValidateRejectsNonFinite(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		c := DefaultConfig()
		c.Weights[CompOutcome] = bad
		if err := c.Validate(); err == nil {
			t.Errorf("weight %v accepted", bad)
		}
		c = DefaultConfig()
		c.Caps[CapProtocol] = bad
		if err := c.Validate(); err == nil {
			t.Errorf("cap %v accepted", bad)
		}
		c = DefaultConfig()
		c.Clip = [2]float64{bad, 1}
		if err := c.Validate(); err == nil {
			t.Errorf("clip %v accepted", bad)
		}
	}
}

func TestTotalAppliesTwoSidedHonestDone(t *testing.T) {
	c := DefaultConfig()
	near(t, "honest", c.Total(map[string]float64{CompHonestDone: 1}), 0.1)
	near(t, "false", c.Total(map[string]float64{CompHonestDone: -1}), -0.2)
	near(t, "sum", c.Total(map[string]float64{CompOutcome: 1, CompCost: -0.5, CompHonestDone: 1}), 1+(-0.5*0.15)+0.1)
	// Keys without a weight are informational and ignored; non-finite values too.
	near(t, "info", c.Total(map[string]float64{"role/worker": 99, "compactor/x/valid": 5, CompOutcome: 1}), 1)
	near(t, "nan", c.Total(map[string]float64{CompOutcome: math.NaN(), CompCost: -1}), -0.15)
	near(t, "empty", c.Total(nil), 0)
	// A zero-value Config falls back to the documented defaults.
	near(t, "zero cfg", Config{}.Total(map[string]float64{CompOutcome: 1}), 1)
}

func TestResolveFillsDefaultsAndCopies(t *testing.T) {
	c := Config{Weights: map[string]float64{CompCost: 0}}
	r, err := c.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if r.weights[CompCost] != 0 || r.weights[CompOutcome] != 1 {
		t.Errorf("explicit zero must win over the default, missing keys must default: %v", r.weights)
	}
	if _, ok := c.Weights[CompOutcome]; ok {
		t.Error("resolve mutated the caller's map")
	}
	if r.target.ID == "" {
		t.Error("no target resolved")
	}
	if r.frac(5, CapProtocol) != 0.5 || r.frac(50, CapProtocol) != 1 || r.frac(-3, CapProtocol) != 0 || r.frac(math.NaN(), CapProtocol) != 0 {
		t.Error("frac must saturate and ignore junk")
	}
	r.caps[CapProtocol] = 0
	if r.frac(1, CapProtocol) != 1 || r.frac(0, CapProtocol) != 0 {
		t.Error("a zero cap saturates on the first event")
	}
}

func TestTargets(t *testing.T) {
	for _, name := range Targets() {
		m, ok := LookupTarget(name)
		if !ok {
			t.Errorf("preset %s does not resolve", name)
			continue
		}
		w := m.Price.Weights()
		for _, v := range []float64{w.Read, w.Output} {
			if !finite(v) || v < 0 {
				t.Errorf("%s: bad weight %v", name, v)
			}
		}
	}
	if _, ok := LookupTarget("  Anthropic-Sonnet "); !ok {
		t.Error("names are case and space insensitive")
	}
	if _, ok := LookupTarget("nonesuch"); ok {
		t.Error("unknown target resolved")
	}
	an, _ := LookupTarget("anthropic-sonnet")
	w5, w1 := writeWeights(an)
	near(t, "anthropic write5m", w5, 1.25)
	near(t, "anthropic write1h", w1, 2)
	near(t, "anthropic read", an.Price.Weights().Read, 0.1)
	for _, name := range []string{"openai", "marketplace"} {
		m, _ := LookupTarget(name)
		w5, _ := writeWeights(m)
		near(t, name+" has no write premium", w5, 1)
	}
	// An auto-cache model with no stated write price has no premium either
	// (cost.Price.Weights alone would say 1.25).
	auto := cost.Model{Price: cost.Price{InputPerM: 1, OutputPerM: 2, CacheReadPerM: 0.5}, Cache: cost.OpenAICacheModel()}
	w5, _ = writeWeights(auto)
	near(t, "auto default write", w5, 1)
}
