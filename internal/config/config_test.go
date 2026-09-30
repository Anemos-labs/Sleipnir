package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/perm"
)

func TestDefaults(t *testing.T) {
	d := Defaults()
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"permissions.mode", d.Permissions.Mode, "default"},
		{"cache.shared_ttl", d.Cache.SharedTTL, "5m"},
		{"cache.min_layer_for_breakpoint", d.Cache.MinLayerForBreakpoint, 1500},
		{"cache.prewarm", d.Cache.Prewarm, true},
		{"cache.keepalive", d.Cache.Keepalive, false},
		{"swarm.isolation", d.Swarm.Isolation, "none"},
		{"tools.max_output_chars", d.Tools.MaxOutputChars, 24000},
		{"tools.default_timeout_sec", d.Tools.DefaultTimeoutSec, 120},
		{"tools.max_timeout_sec", d.Tools.MaxTimeoutSec, 600},
		{"training.enabled", d.Training.Enabled, false},
		{"training.redact_secrets", d.Training.RedactSecrets, true},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if issues := d.Validate(); len(issues) != 0 {
		t.Fatalf("the defaults must validate cleanly: %v", issues)
	}
	if d2 := Defaults(); d == d2 {
		t.Fatal("Defaults must return a fresh value each call")
	}
}

func TestDefaultsMirrorTheEnginesTheyFeed(t *testing.T) {
	// tools.DefaultLimits is 24k chars, 2 minutes, 10 minutes; if it ever
	// changes this reminds whoever changes it to update the config default too.
	d := Defaults()
	if d.Tools.DefaultTimeout() != 2*time.Minute || d.Tools.MaxTimeout() != 10*time.Minute {
		t.Fatalf("timeouts = %v / %v", d.Tools.DefaultTimeout(), d.Tools.MaxTimeout())
	}
}

func TestZeroConfigIsUsable(t *testing.T) {
	var c Config
	if issues := c.Validate(); len(issues) != 0 {
		t.Fatalf("the zero Config must be valid: %v", issues)
	}
	if c.ModelFor("planner") != "" || c.Permissions.ModeFor("planner") != perm.ModeDefault {
		t.Fatal("zero-value accessors misbehave")
	}
	if c.Cache.SharedTTLDuration() != 5*time.Minute {
		t.Fatalf("ttl = %v", c.Cache.SharedTTLDuration())
	}
	if (Provider{}).EffectiveDialect() != DialectOpenAIChat {
		t.Fatal("empty dialect defaults to openai-chat")
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
}

func TestEveryJSONTagIsSnakeCase(t *testing.T) {
	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if !snake.MatchString(name) {
				t.Errorf("%s.%s has json tag %q; tags must be snake_case", path, f.Name, tag)
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	walk(reflect.TypeOf(Config{}), "Config")
}

func TestSectionsMatchTheSpecifiedFields(t *testing.T) {
	want := map[string][]string{
		"Cache":       {"shared_ttl", "min_layer_for_breakpoint", "compact_threshold_tokens", "thread_soft_limit_tokens", "hot_max_tokens", "affinity_shards", "prewarm", "keepalive"},
		"Swarm":       {"max_agents", "requests_per_minute", "max_concurrent_requests", "isolation", "mailman", "budget_usd"},
		"Tools":       {"max_output_chars", "default_timeout_sec", "max_timeout_sec", "web_allow_private", "web_allow_hosts"},
		"UI":          {"theme", "editor"},
		"Training":    {"enabled", "redact_secrets", "dir"},
		"Models":      {"default", "roles", "compactor"},
		"Permissions": {"mode", "allow", "ask", "deny", "roles"},
		"Provider":    {"dialect", "base_url", "api_key_env", "headers", "options", "models", "allow_hosts", "allow_insecure_http"},
		"Config":      {"providers", "models", "permissions", "cache", "swarm", "tools", "ui", "training", "hooks", "mcp"},
	}
	types := map[string]reflect.Type{
		"Cache": reflect.TypeOf(Cache{}), "Swarm": reflect.TypeOf(Swarm{}), "Tools": reflect.TypeOf(Tools{}),
		"UI": reflect.TypeOf(UI{}), "Training": reflect.TypeOf(Training{}), "Models": reflect.TypeOf(Models{}),
		"Permissions": reflect.TypeOf(Permissions{}), "Provider": reflect.TypeOf(Provider{}), "Config": reflect.TypeOf(Config{}),
	}
	for name, fields := range want {
		var got []string
		for _, f := range structFields(types[name]) {
			got = append(got, f.name)
		}
		if !reflect.DeepEqual(got, fields) {
			t.Errorf("%s fields = %v, want %v", name, got, fields)
		}
	}
	// The types the spec calls out.
	if reflect.TypeOf(Config{}.Hooks) != reflect.TypeOf(map[string]json.RawMessage{}) || reflect.TypeOf(Config{}.MCP) != reflect.TypeOf(map[string]json.RawMessage{}) {
		t.Error("Hooks and MCP must be map[string]json.RawMessage placeholders")
	}
	if reflect.TypeOf(Config{}.Extra) != reflect.TypeOf(map[string]json.RawMessage{}) {
		t.Error("Extra must be map[string]json.RawMessage")
	}
}

func TestUnmarshalOverlaysOnAndPreservesUnknownKeys(t *testing.T) {
	cfg := Defaults()
	err := json.Unmarshal([]byte(`{"cache": {"prewarm": false}, "swarm": {"max_agents": 7}, "future_feature": {"a": [1, 2]}, "$schema": "s"}`), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cache.Prewarm || cfg.Swarm.MaxAgents != 7 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.Cache.SharedTTL != "5m" || cfg.Cache.MinLayerForBreakpoint != 1500 || cfg.Swarm.Isolation != "none" {
		t.Fatalf("keys absent from the JSON must keep their values: %+v", cfg)
	}
	if string(cfg.Extra["future_feature"]) != `{"a": [1, 2]}` || string(cfg.Extra["$schema"]) != `"s"` {
		t.Fatalf("Extra = %v", cfg.Extra)
	}
}

func TestMarshalIncludesExtraAndRoundTrips(t *testing.T) {
	in := `{"models":{"default":"a/b"},"providers":{"p":{"dialect":"anthropic","headers":{"X":"1"}}},"zz_future":{"k":[1,2,3]},"aa_future":true}`
	var cfg Config
	if err := json.Unmarshal([]byte(in), &cfg); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["zz_future"]) != `{"k":[1,2,3]}` || string(m["aa_future"]) != "true" || m["models"] == nil {
		t.Fatalf("marshal dropped keys: %s", out)
	}
	var back Config
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, back) {
		t.Fatalf("round trip differs:\n%+v\n%+v", cfg, back)
	}
	// A pointer marshals the same way.
	out2, _ := json.Marshal(&cfg)
	if string(out) != string(out2) {
		t.Fatal("pointer and value must marshal identically")
	}
	// Extra never overrides a real field.
	cfg.Extra["models"] = json.RawMessage(`"shadow"`)
	out3, _ := json.Marshal(cfg)
	if strings.Contains(string(out3), "shadow") {
		t.Fatal("Extra shadowed a known key")
	}
}

func TestTopLevelKeysAreCaseSensitive(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"Models": {"default": "a/b"}, "models": {"compactor": "c/d"}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Default != "" || cfg.Models.Compactor != "c/d" {
		t.Fatalf("models = %+v: only the exact key applies", cfg.Models)
	}
	if _, ok := cfg.Extra["Models"]; !ok {
		t.Fatal("the mis-cased key belongs in Extra")
	}
}

func TestUnmarshalRejectsWrongTypes(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"swarm": {"max_agents": "x"}}`), &cfg); err == nil {
		t.Fatal("expected a type error")
	}
	if err := json.Unmarshal([]byte(`[1]`), &cfg); err == nil {
		t.Fatal("expected an error for a non-object")
	}
}

func TestSplitModelRef(t *testing.T) {
	tests := []struct {
		in      string
		prov, m string
		ok      bool
	}{
		{"anthropic/claude-sonnet-5-5", "anthropic", "claude-sonnet-5-5", true},
		{"openrouter/vendor/model:free", "openrouter", "vendor/model:free", true},
		{"a/b", "a", "b", true},
		{"", "", "", false},
		{"nomodel", "", "", false},
		{"/model", "", "", false},
		{"prov/", "", "", false},
		{" prov/model", "", "", false},
		{"prov/model ", "", "", false},
	}
	for _, tc := range tests {
		p, m, ok := SplitModelRef(tc.in)
		if p != tc.prov || m != tc.m || ok != tc.ok {
			t.Errorf("SplitModelRef(%q) = %q, %q, %v; want %q, %q, %v", tc.in, p, m, ok, tc.prov, tc.m, tc.ok)
		}
	}
}

func TestModelForAndModeFor(t *testing.T) {
	c := &Config{
		Models:      Models{Default: "d/m", Roles: map[string]string{"planner": "p/m"}},
		Permissions: Permissions{Mode: "accept-edits", Roles: map[string]RolePermissions{"reviewer": {Mode: "plan"}, "empty": {}}},
	}
	if c.ModelFor("planner") != "p/m" || c.ModelFor("coder") != "d/m" || c.ModelFor("") != "d/m" {
		t.Fatal("ModelFor: role entry, else the default")
	}
	if c.Permissions.ModeFor("reviewer") != perm.ModePlan || c.Permissions.ModeFor("coder") != perm.ModeAcceptEdits || c.Permissions.ModeFor("empty") != perm.ModeAcceptEdits {
		t.Fatal("ModeFor: role override, else the global mode")
	}
	if (Permissions{}).ModeFor("x") != perm.ModeDefault {
		t.Fatal("ModeFor with nothing set is default")
	}
}

func TestCacheAndToolsDurations(t *testing.T) {
	if (Cache{SharedTTL: "1h"}).SharedTTLDuration() != time.Hour || (Cache{SharedTTL: "5m"}).SharedTTLDuration() != 5*time.Minute || (Cache{}).SharedTTLDuration() != 5*time.Minute {
		t.Fatal("SharedTTLDuration")
	}
	tl := Tools{DefaultTimeoutSec: 30, MaxTimeoutSec: 90}
	if tl.DefaultTimeout() != 30*time.Second || tl.MaxTimeout() != 90*time.Second {
		t.Fatal("timeouts")
	}
}

func TestIssueFormattingAndErrorsHelper(t *testing.T) {
	is := Issue{Severity: SeverityError, Source: "/x/config.json", Line: 3, Col: 9, Path: "cache.shared_ttl", Message: "bad"}
	if is.Error() != "/x/config.json:3:9: cache.shared_ttl: bad" {
		t.Fatalf("Error() = %q", is.Error())
	}
	if is.String() != "error: /x/config.json:3:9: cache.shared_ttl: bad" {
		t.Fatalf("String() = %q", is.String())
	}
	if (Issue{Message: "m"}).Error() != "m" || (Issue{Source: "env:X", Path: "a.b", Message: "m"}).Error() != "env:X: a.b: m" {
		t.Fatal("partial locations")
	}
	if b, _ := json.Marshal(SeverityWarning); string(b) != `"warning"` {
		t.Fatalf("severity JSON = %s", b)
	}

	warn := Issue{Severity: SeverityWarning, Message: "w"}
	if Errors([]Issue{warn}) != nil || Errors(nil) != nil {
		t.Fatal("warnings alone are not an error")
	}
	err := Errors([]Issue{warn, is, {Severity: SeverityError, Message: "second"}})
	if err == nil || !strings.Contains(err.Error(), "bad") || !strings.Contains(err.Error(), "second") || strings.Contains(err.Error(), "w\n") {
		t.Fatalf("Errors = %v", err)
	}
	var got Issue
	if !errors.As(err, &got) || got.Line != 3 {
		t.Fatalf("errors.As: %+v", got)
	}
}

func TestFmtPath(t *testing.T) {
	tests := []struct {
		segs []string
		want string
	}{
		{nil, ""},
		{[]string{"cache", "shared_ttl"}, "cache.shared_ttl"},
		{[]string{"providers", "my.azure", "dialect"}, `providers["my.azure"].dialect`},
		{[]string{"tools", "web_allow_hosts", "[2]"}, "tools.web_allow_hosts[2]"},
		{[]string{"providers", "open router"}, `providers["open router"]`},
		{[]string{"a-b", "c_d"}, "a-b.c_d"},
	}
	for _, tc := range tests {
		if got := fmtPath(tc.segs); got != tc.want {
			t.Errorf("fmtPath(%v) = %q, want %q", tc.segs, got, tc.want)
		}
	}
}

func TestClosestSuggestions(t *testing.T) {
	names := []string{"providers", "models", "permissions", "cache", "swarm", "tools", "ui", "training", "hooks", "mcp"}
	tests := map[string]string{
		"provider": "providers", "permission": "permissions", "Cache": "cache", "cach": "cache", "swam": "swarm",
		"modles": "models", "tool": "tools", "xyz": "", "completely_unrelated": "", "u": "ui",
	}
	for in, want := range tests {
		if got := closest(in, names); got != want {
			t.Errorf("closest(%q) = %q, want %q", in, got, want)
		}
	}
}
