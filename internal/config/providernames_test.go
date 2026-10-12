package config

import "testing"

func TestARenamedProviderIsFoundByItsFormerName(t *testing.T) {
	for in, want := range map[string]string{"heimdall": "heimdallm", "heimdallm": "heimdallm", "openrouter": "openrouter", "": ""} {
		if got := CanonicalProvider(in); got != want {
			t.Errorf("CanonicalProvider(%q) = %q, want %q", in, got, want)
		}
	}
	former := &Config{Providers: map[string]Provider{"heimdall": {AllowHosts: []string{"old.example"}}}}
	for _, name := range []string{"heimdall", "heimdallm"} {
		if p, ok := former.ProviderEntry(name); !ok || len(p.AllowHosts) != 1 || p.AllowHosts[0] != "old.example" {
			t.Errorf("an entry under the former name, asked for as %q: %+v %v", name, p, ok)
		}
	}
	both := &Config{Providers: map[string]Provider{"heimdall": {AllowHosts: []string{"old.example"}}, "heimdallm": {AllowHosts: []string{"new.example"}}}}
	if p, _ := both.ProviderEntry("heimdall"); len(p.AllowHosts) != 1 || p.AllowHosts[0] != "new.example" {
		t.Errorf("the entry under the current name wins: %+v", p)
	}
	if _, ok := (*Config)(nil).ProviderEntry("heimdallm"); ok {
		t.Error("a nil configuration has no entries")
	}
}

func TestRemovingAKeyRemovesItUnderItsFormerNameToo(t *testing.T) {
	home := t.TempDir()
	if err := SaveStoredKey(home, "HEIMDALL_API_KEY", "stored-under-the-former-name"); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoredKey(home, "OTHER_API_KEY", "kept"); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoredKey(home, "HEIMDALLM_API_KEY", ""); err != nil {
		t.Fatal(err)
	}
	if m, _ := StoredKeys(home); len(m) != 1 || m["OTHER_API_KEY"] != "kept" {
		t.Errorf("after removing HEIMDALLM_API_KEY: %v", m)
	}
}
