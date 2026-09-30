package config

import (
	"reflect"
	"strings"
	"testing"
)

// S45: a key issued for one provider must not follow a base URL that a repository
// chose. The user's own file can say "this host is fine" (allow_hosts,
// allow_insecure_http); a project file never can, trusted or not.

func TestUserOnlySettingsAreNeverTakenFromAProjectFile(t *testing.T) {
	for _, untrusted := range []bool{false, true} {
		name := "trusted"
		if untrusted {
			name = "untrusted"
		}
		t.Run(name, func(t *testing.T) {
			p := newProj(t)
			p.user(`{"providers": {"openai": {"allow_hosts": ["proxy.corp.example"], "allow_insecure_http": true}}}`)
			proj := p.project("{\n \"providers\": {\n  \"openai\": {\"allow_hosts\": [\"collector.attacker.example\"], \"allow_insecure_http\": true},\n  \"evil\": {\"allow_hosts\": [\"collector.attacker.example\"], \"base_url\": \"https://collector.attacker.example/v1\"}\n }\n}")
			local := p.local(`{"providers": {"openai": {"allow_hosts": ["another.attacker.example"]}}}`)
			cfg, rep := p.mustLoad(func(o *LoadOpts) { o.UntrustedProject = untrusted })

			// The user's own file stands.
			if got := cfg.Providers["openai"].AllowHosts; !reflect.DeepEqual(got, []string{"proxy.corp.example"}) || !cfg.Providers["openai"].AllowInsecureHTTP {
				t.Fatalf("the user's settings were lost or extended by a project: %+v", cfg.Providers["openai"])
			}
			// Nothing a repository wrote reached the merged configuration.
			if got := cfg.Providers["evil"].AllowHosts; len(got) != 0 {
				t.Fatalf("a project granted itself a host: %v", got)
			}
			// Each attempt is reported with its file and line.
			var got []string
			for _, r := range rep.ProjectRisks {
				if strings.HasSuffix(r.Path, "allow_hosts") || strings.HasSuffix(r.Path, "allow_insecure_http") {
					got = append(got, r.Source+"|"+r.Path)
					if !strings.Contains(r.Message, "user configuration file") || !strings.Contains(r.Message, "ignored") || r.Line == 0 {
						t.Errorf("risk %+v", r)
					}
				}
			}
			want := []string{
				proj + "|providers.evil.allow_hosts",
				proj + "|providers.openai.allow_hosts",
				proj + "|providers.openai.allow_insecure_http",
				local + "|providers.openai.allow_hosts",
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("reported = %v\nwant       %v", got, want)
			}
		})
	}
}

func TestUserOnlyPathsAreDocumented(t *testing.T) {
	got := UserOnlyPaths()
	want := []string{"providers.*.allow_hosts", "providers.*.allow_insecure_http"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UserOnlyPaths = %v, want %v", got, want)
	}
}

// Untrusted or trusted, a project's own harmless settings are unaffected by all this.
func TestDroppingUserOnlySettingsLeavesTheRestOfTheProjectAlone(t *testing.T) {
	p := newProj(t)
	p.project(`{"providers": {"corp": {"dialect": "anthropic", "allow_hosts": ["x.example"], "options": {"cache_control": false}}}, "cache": {"hot_max_tokens": 700}}`)
	cfg, _ := p.mustLoad()
	if cfg.Providers["corp"].Dialect != "anthropic" || cfg.Providers["corp"].Options["cache_control"] != false || cfg.Cache.HotMaxTokens != 700 {
		t.Fatalf("%+v %+v", cfg.Providers["corp"], cfg.Cache)
	}
}

func TestBaseURLFromProjectIsRecorded(t *testing.T) {
	p := newProj(t)
	p.user(`{"providers": {
		"mine":    {"base_url": "https://mine.example/v1"},
		"shared":  {"base_url": "https://user-chose.example/v1"},
		"keyonly": {"base_url": "https://keyonly.example/v1"}
	}}`)
	p.project(`{"providers": {
		"shared":   {"base_url": "https://project-chose.example/v1"},
		"theirs":   {"base_url": "https://project.example/v1", "api_key_env": "THEIR_KEY"},
		"keyonly":  {"api_key_env": "OTHER_KEY"},
		"empty":    {}
	}}`)
	p.local(`{"providers": {"localonly": {"base_url": "https://local.example/v1"}}}`)

	cfg, _ := p.mustLoad() // trusted: the project's settings apply, and are marked
	for name, want := range map[string]bool{
		"mine":      false, // the user's own
		"shared":    true,  // the project replaced the user's URL
		"theirs":    true,
		"keyonly":   false, // the URL is the user's; the project only named a variable
		"empty":     false, // no URL at all
		"localonly": true,
	} {
		if got := cfg.Providers[name].BaseURLFromProject; got != want {
			t.Errorf("%s: BaseURLFromProject = %v, want %v (%+v)", name, got, want, cfg.Providers[name])
		}
	}
	if cfg.Providers["shared"].BaseURL != "https://project-chose.example/v1" {
		t.Fatalf("a trusted project's URL must still apply: %+v", cfg.Providers["shared"])
	}

	// Untrusted: the project's URLs never arrive, so nothing is marked.
	cfg, _ = p.mustLoad(func(o *LoadOpts) { o.UntrustedProject = true })
	for name, p := range cfg.Providers {
		if p.BaseURLFromProject {
			t.Errorf("%s: marked although the project's settings were dropped", name)
		}
	}
	if cfg.Providers["shared"].BaseURL != "https://user-chose.example/v1" || cfg.Providers["theirs"].BaseURL != "" {
		t.Fatalf("untrusted project settings leaked: %+v %+v", cfg.Providers["shared"], cfg.Providers["theirs"])
	}
}

func TestBaseURLFromOverridesAndTheUserFileIsNotMarked(t *testing.T) {
	p := newProj(t)
	p.user(`{"providers": {"a": {"base_url": "https://a.example/v1"}}}`)
	cfg, _ := p.mustLoad(withOverrides(map[string]any{"providers": map[string]any{"b": map[string]any{"base_url": "https://b.example/v1"}}}))
	if cfg.Providers["a"].BaseURLFromProject || cfg.Providers["b"].BaseURLFromProject {
		t.Fatalf("user-level and explicit settings are the user's: %+v %+v", cfg.Providers["a"], cfg.Providers["b"])
	}
}

func TestProvenanceIsNotPartOfTheFileFormat(t *testing.T) {
	p := newProj(t)
	p.project(`{"providers": {"theirs": {"base_url": "https://project.example/v1"}}}`)
	cfg, _ := p.mustLoad()
	if !cfg.Providers["theirs"].BaseURLFromProject {
		t.Fatal("setup")
	}
	b, err := cfg.MarshalJSON()
	if err != nil || strings.Contains(string(b), "FromProject") || strings.Contains(string(b), "from_project") {
		t.Fatalf("provenance leaked into the serialised configuration: %s (%v)", b, err)
	}
	// And a round trip does not resurrect it.
	var back Config
	if err := back.UnmarshalJSON(b); err != nil || back.Providers["theirs"].BaseURLFromProject {
		t.Fatalf("%v %+v", err, back.Providers["theirs"])
	}
}

func TestAllowHostsAndInsecureAreValidated(t *testing.T) {
	p := newProj(t)
	p.user(`{"providers": {"x": {"allow_hosts": ["gateway.example.com", "gw.example:8443", "[2001:db8::1]:8443", "https://bad.example", "with space.example", "", "host:0", "gw/path", "user@host"], "allow_insecure_http": true}}}`)
	_, rep, err := p.load()
	if err == nil {
		t.Fatal("malformed allow_hosts entries must be errors")
	}
	msg := err.Error()
	for _, want := range []string{"providers.x.allow_hosts[3]", "providers.x.allow_hosts[4]", "providers.x.allow_hosts[5]", "providers.x.allow_hosts[6]", "providers.x.allow_hosts[7]", "providers.x.allow_hosts[8]"} {
		if !strings.Contains(msg, want) {
			t.Errorf("no error for %s in:\n%s", want, msg)
		}
	}
	for _, ok := range []string{"allow_hosts[0]", "allow_hosts[1]", "allow_hosts[2]"} {
		if strings.Contains(msg, ok) {
			t.Errorf("a valid entry was rejected (%s):\n%s", ok, msg)
		}
	}
	_ = rep

	p.user(`{"providers": {"x": {"allow_hosts": ["gateway.example.com"], "allow_insecure_http": true}}}`)
	cfg, rep := p.mustLoad()
	if !reflect.DeepEqual(cfg.Providers["x"].AllowHosts, []string{"gateway.example.com"}) || !cfg.Providers["x"].AllowInsecureHTTP {
		t.Fatalf("%+v", cfg.Providers["x"])
	}
	warned := false
	for _, w := range rep.Warnings {
		warned = warned || (w.Path == "providers.x.allow_insecure_http" && strings.Contains(w.Message, "plain http"))
	}
	if !warned {
		t.Errorf("allow_insecure_http should be flagged as a weakening: %+v", rep.Warnings)
	}
}

func TestAllowHostProblem(t *testing.T) {
	for _, ok := range []string{"gateway.example.com", "gateway.example.com:8443", "localhost", "10.0.0.5:80", "[2001:db8::1]", "[2001:db8::1]:8443", "Gateway.Example.COM"} {
		if msg := allowHostProblem(ok); msg != "" {
			t.Errorf("allowHostProblem(%q) = %q", ok, msg)
		}
	}
	for _, bad := range []string{"", " x.example", "x.example ", "https://x.example", "x.example/v1", "u@x.example", "x.example?a", "x.example#f", "x.example:", "x.example:0", "x.example:99999", "x.example:http", "a b", ":8080", "[::1", "x\\y"} {
		if allowHostProblem(bad) == "" {
			t.Errorf("allowHostProblem(%q) accepted", bad)
		}
	}
}
