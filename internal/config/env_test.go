package config

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestEnvVarsDocumentTheMapping(t *testing.T) {
	got := map[string]EnvVar{}
	for _, v := range EnvVars() {
		got[v.Name] = v
	}
	want := map[string]EnvVar{
		"SLEIPNIR_MODEL":                 {Path: "models.default", Type: "string"},
		"SLEIPNIR_MODEL_<ROLE>":          {Path: "models.roles.<role>", Type: "string"},
		"SLEIPNIR_PERMISSION_MODE":       {Path: "permissions.mode", Type: "string"},
		"SLEIPNIR_CACHE_SHARED_TTL":      {Path: "cache.shared_ttl", Type: "string"},
		"SLEIPNIR_SWARM_MAILMAN":         {Path: "swarm.mailman", Type: "bool"},
		"SLEIPNIR_SWARM_MAX_AGENTS":      {Path: "swarm.max_agents", Type: "integer"},
		"SLEIPNIR_SWARM_BUDGET_USD":      {Path: "swarm.budget_usd", Type: "number"},
		"SLEIPNIR_TOOLS_WEB_ALLOW_HOSTS": {Path: "tools.web_allow_hosts", Type: "list"},
		"SLEIPNIR_PERMISSIONS_ALLOW":     {Path: "permissions.allow", Type: "list"},
		"SLEIPNIR_MODELS_DEFAULT":        {Path: "models.default", Type: "string"},
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("EnvVars lacks %s", name)
			continue
		}
		if g.Path != w.Path || g.Type != w.Type {
			t.Errorf("%s = %+v, want %+v", name, g, w)
		}
	}
	vars := EnvVars()
	if !sort.SliceIsSorted(vars, func(i, j int) bool { return vars[i].Name < vars[j].Name }) {
		t.Error("EnvVars must be sorted")
	}
	// Providers, hooks and MCP are files-only.
	for name := range got {
		if strings.Contains(name, "PROVIDERS") || strings.Contains(name, "HOOKS") || strings.Contains(name, "_MCP") {
			t.Errorf("%s: structured sections are not configurable from the environment", name)
		}
	}
}

func TestEnvironmentSetsEveryKindOfValue(t *testing.T) {
	p := newProj(t)
	cfg, rep := p.mustLoad(withEnv(
		"SLEIPNIR_MODEL=openrouter/vendor/model",
		"SLEIPNIR_PERMISSION_MODE=plan",
		"SLEIPNIR_CACHE_SHARED_TTL=1h",
		"SLEIPNIR_CACHE_HOT_MAX_TOKENS=1234",
		"SLEIPNIR_SWARM_MAX_AGENTS=12",
		"SLEIPNIR_SWARM_BUDGET_USD=7.25",
		"SLEIPNIR_SWARM_ISOLATION=worktree",
		"SLEIPNIR_SWARM_MAILMAN=YES",
		"SLEIPNIR_TOOLS_WEB_ALLOW_HOSTS=a.example.com, b.example.com ,,c.example.com",
		"SLEIPNIR_TOOLS_WEB_ALLOW_PRIVATE=1",
		"SLEIPNIR_PERMISSIONS_DENY=Bash(rm:*)",
		"SLEIPNIR_MODEL_PLANNER=anthropic/opus",
		"SLEIPNIR_MODEL_CODE_REVIEWER=openrouter/x/y",
	))
	if cfg.Models.Default != "openrouter/vendor/model" || cfg.Permissions.Mode != "plan" {
		t.Errorf("models/permissions: %+v %+v", cfg.Models, cfg.Permissions)
	}
	if cfg.Cache.SharedTTL != "1h" || cfg.Cache.HotMaxTokens != 1234 {
		t.Errorf("cache: %+v", cfg.Cache)
	}
	if cfg.Swarm.MaxAgents != 12 || cfg.Swarm.BudgetUSD != 7.25 || cfg.Swarm.Isolation != "worktree" || !cfg.Swarm.Mailman {
		t.Errorf("swarm: %+v", cfg.Swarm)
	}
	if !reflect.DeepEqual(cfg.Tools.WebAllowHosts, []string{"a.example.com", "b.example.com", "c.example.com"}) || !cfg.Tools.WebAllowPrivate {
		t.Errorf("tools: %+v", cfg.Tools)
	}
	if !reflect.DeepEqual(cfg.Permissions.Deny, []string{"Bash(rm:*)"}) {
		t.Errorf("permissions: %+v", cfg.Permissions)
	}
	if !reflect.DeepEqual(cfg.Models.Roles, map[string]string{"planner": "anthropic/opus", "code_reviewer": "openrouter/x/y"}) {
		t.Errorf("roles: %v", cfg.Models.Roles)
	}
	if rep.Origins["swarm.max_agents"] != "env:SLEIPNIR_SWARM_MAX_AGENTS" || rep.Origins["models.roles.code_reviewer"] != "env:SLEIPNIR_MODEL_CODE_REVIEWER" {
		t.Errorf("origins: %v", rep.Origins)
	}
	envInfo := LayerInfo{}
	for _, l := range rep.Layers {
		if l.Kind == "env" {
			envInfo = l
		}
	}
	if !envInfo.Found || len(envInfo.Keys) != 13 {
		t.Errorf("env layer = %+v", envInfo)
	}
}

func TestEnvironmentBoolSpellings(t *testing.T) {
	p := newProj(t)
	for _, v := range []string{"1", "true", "TRUE", "Yes", "on"} {
		cfg, _ := p.mustLoad(withEnv("SLEIPNIR_SWARM_MAILMAN=" + v))
		if !cfg.Swarm.Mailman {
			t.Errorf("%q should be true", v)
		}
	}
	// A false spelling in the environment overrides a true in a file.
	p.user(`{"swarm": {"mailman": true}}`)
	for _, v := range []string{"0", "false", "No", "OFF"} {
		cfg, _ := p.mustLoad(withEnv("SLEIPNIR_SWARM_MAILMAN=" + v))
		if cfg.Swarm.Mailman {
			t.Errorf("%q should be false", v)
		}
	}
}

func TestEnvironmentEmptyValuesAreUnsetAndOtherVariablesAreIgnored(t *testing.T) {
	p := newProj(t)
	p.user(`{"models": {"default": "file/model"}, "swarm": {"max_agents": 3}}`)
	cfg, rep := p.mustLoad(withEnv(
		"SLEIPNIR_MODEL=", // empty: does not blank the file's value
		"SLEIPNIR_SWARM_MAX_AGENTS=  ",
		"SLEIPNIR_HOME=/somewhere", // not a setting; the harness may use it for other things
		"SLEIPNIR_DEBUG=1",
		"SLEIPNIR_NOPE_NOTHING=x",
		"MODEL=other/model", // not prefixed
		"NOT_SLEIPNIR_MODEL=x/y",
		"SLEIPNIR_MODEL_=x/y", // an empty role name
		"SLEIPNIR_MODEL_EMPTYROLE=",
		"garbage without equals",
	))
	if cfg.Models.Default != "file/model" || cfg.Swarm.MaxAgents != 3 {
		t.Fatalf("cfg = %+v %+v", cfg.Models, cfg.Swarm)
	}
	if len(cfg.Models.Roles) != 0 {
		t.Fatalf("roles = %v", cfg.Models.Roles)
	}
	if len(rep.Warnings) != 0 {
		t.Fatalf("warnings = %+v", rep.Warnings)
	}
	for _, l := range rep.Layers {
		if l.Kind == "env" {
			t.Fatalf("no variable applied, so no env layer should be listed: %+v", l)
		}
	}
}

func TestEnvironmentValuesAreTrimmed(t *testing.T) {
	p := newProj(t)
	cfg, _ := p.mustLoad(withEnv("SLEIPNIR_MODEL=  a/b\n", "SLEIPNIR_SWARM_MAX_AGENTS= 4 "))
	if cfg.Models.Default != "a/b" || cfg.Swarm.MaxAgents != 4 {
		t.Fatalf("cfg = %+v %+v", cfg.Models, cfg.Swarm)
	}
}

func TestEnvironmentOrderDoesNotMatter(t *testing.T) {
	p := newProj(t)
	a, _ := p.mustLoad(withEnv("SLEIPNIR_MODEL_A=x/1", "SLEIPNIR_MODEL_B=x/2", "SLEIPNIR_MODEL=x/0", "SLEIPNIR_MODELS_DEFAULT=y/0"))
	b, _ := p.mustLoad(withEnv("SLEIPNIR_MODELS_DEFAULT=y/0", "SLEIPNIR_MODEL=x/0", "SLEIPNIR_MODEL_B=x/2", "SLEIPNIR_MODEL_A=x/1"))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("results depend on environment order:\n%+v\n%+v", a.Models, b.Models)
	}
}

func TestEnvironmentBeatsFilesAndLosesToOverrides(t *testing.T) {
	p := newProj(t)
	p.local(`{"swarm": {"max_agents": 1}}`)
	cfg, _ := p.mustLoad(withEnv("SLEIPNIR_SWARM_MAX_AGENTS=2"))
	if cfg.Swarm.MaxAgents != 2 {
		t.Fatalf("env should beat the local file: %d", cfg.Swarm.MaxAgents)
	}
	cfg, _ = p.mustLoad(withEnv("SLEIPNIR_SWARM_MAX_AGENTS=2"), withOverrides(map[string]any{"swarm": map[string]any{"max_agents": 0}}))
	if cfg.Swarm.MaxAgents != 0 {
		t.Fatalf("an explicit override of 0 must beat env: %d", cfg.Swarm.MaxAgents)
	}
}

func TestEnvironmentValueErrorsDoNotEchoLongValues(t *testing.T) {
	p := newProj(t)
	long := strings.Repeat("x", 500)
	_, _, err := p.load(withEnv("SLEIPNIR_SWARM_MAX_AGENTS=" + long))
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(err.Error()) > 300 {
		t.Fatalf("error is %d bytes; long values must be truncated", len(err.Error()))
	}
}

func TestEveryEnvBindingIsWiredToARealField(t *testing.T) {
	// Each binding's path must resolve to a field of matching type in Config, so
	// renaming a field cannot silently orphan its variable.
	for _, b := range envBindings() {
		cur := configType
		for i, s := range b.segs {
			var next *field
			for _, f := range structFields(cur) {
				if f.name == s {
					f := f
					next = &f
				}
			}
			if next == nil {
				t.Errorf("%s: path %v does not exist (no %q)", b.name, b.segs, s)
				break
			}
			if i == len(b.segs)-1 {
				if next.typ != b.typ {
					t.Errorf("%s: binding type %v, field type %v", b.name, b.typ, next.typ)
				}
			} else {
				cur = next.typ
			}
		}
	}
}
