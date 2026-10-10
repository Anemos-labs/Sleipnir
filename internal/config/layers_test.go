package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

// layerFixture writes a user file, a project file and a local file and returns the options that load them, with env as the whole
// environment.
func layerFixture(t *testing.T, user, project, local string, env ...string) LoadOpts {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	write := func(path, body string) {
		if body == "" {
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(UserConfigPath(home), user)
	write(ProjectConfigPath(root), project)
	write(LocalConfigPath(root), local)
	return LoadOpts{Home: home, Root: root, Environ: func() []string { return env }}
}

// originRows renders rule origins as "effect role rule <- layer" lines, sorted, so tables compare.
func originRows(rs []RuleOrigin) []string {
	var out []string
	for _, r := range rs {
		role := r.Role
		if role == "" {
			role = "*"
		}
		out = append(out, fmt.Sprintf("%s %s %s <- %s", r.Effect, role, r.Rule, r.Layer))
	}
	sort.Strings(out)
	return out
}

func TestRuleOriginsNameTheLayerOfEveryRule(t *testing.T) {
	user := `{"permissions":{"allow":["Bash(git status:*)"],"deny":["Read(~/.ssh/**)"],"ask":["Bash(git push:*)"],
		"roles":{"tester":{"deny":["Bash(rm:*)"]}}}}`
	project := `{"permissions":{"allow":["Bash(go test:*)"],"deny":["Read(./.env)","Read(~/.ssh/**)"],"ask":["Edit(docs/**)"],
		"roles":{"tester":{"deny":["Bash(curl:*)"]}}}}`
	local := `{"permissions":{"deny":["Read(./secrets/**)"]}}`
	for _, tc := range []struct {
		name      string
		untrusted bool
		env       []string
		over      map[string]any
		want      []string
	}{
		{
			name: "trusted: a project's allow replaces the user's, its deny and ask add to them", untrusted: false,
			want: []string{
				"allow * Bash(go test:*) <- project",
				"ask * Bash(git push:*) <- user", "ask * Edit(docs/**) <- project",
				"deny * Read(./.env) <- project", "deny * Read(./secrets/**) <- local", "deny * Read(~/.ssh/**) <- user",
				"deny tester Bash(curl:*) <- project", "deny tester Bash(rm:*) <- user",
			},
		},
		{
			name: "untrusted: the project's allow is left out, its restrictions still apply", untrusted: true,
			want: []string{
				"allow * Bash(git status:*) <- user",
				"ask * Bash(git push:*) <- user", "ask * Edit(docs/**) <- project",
				"deny * Read(./.env) <- project", "deny * Read(./secrets/**) <- local", "deny * Read(~/.ssh/**) <- user",
				"deny tester Bash(curl:*) <- project", "deny tester Bash(rm:*) <- user",
			},
		},
		{
			name: "the environment and the flags replace a list", untrusted: true,
			env:  []string{"SLEIPNIR_PERMISSIONS_ASK=Bash(make:*)"},
			over: map[string]any{"permissions": map[string]any{"allow": []any{"Bash(go vet:*)"}}},
			want: []string{
				"allow * Bash(go vet:*) <- overrides",
				"ask * Bash(make:*) <- env",
				"deny * Read(./.env) <- project", "deny * Read(./secrets/**) <- local", "deny * Read(~/.ssh/**) <- user",
				"deny tester Bash(curl:*) <- project", "deny tester Bash(rm:*) <- user",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := layerFixture(t, user, project, local, tc.env...)
			o.UntrustedProject, o.Overrides = tc.untrusted, tc.over
			got, err := RuleOrigins(o)
			if err != nil {
				t.Fatal(err)
			}
			if rows := originRows(got); !reflect.DeepEqual(rows, tc.want) {
				t.Errorf("origins:\n got %q\nwant %q", rows, tc.want)
			}
			for _, r := range got {
				switch r.Layer {
				case "user", "project", "local":
					if !filepath.IsAbs(r.File) || !strings.HasSuffix(r.File, ".json") {
						t.Errorf("%s: the file of a file layer is its path, got %q", r.Rule, r.File)
					}
				case "env":
					if r.File != "env:SLEIPNIR_PERMISSIONS_ASK" {
						t.Errorf("an environment rule names its variable, got %q", r.File)
					}
				}
			}
			// the replay agrees with the merged configuration, element for element
			cfg, _, err := Load(o)
			if err != nil {
				t.Fatal(err)
			}
			var flat []string
			for _, r := range got {
				if r.Role == "" && r.Effect == "deny" {
					flat = append(flat, r.Rule)
				}
			}
			if !reflect.DeepEqual(flat, cfg.Permissions.Deny) {
				t.Errorf("deny order %q, merged %q", flat, cfg.Permissions.Deny)
			}
		})
	}
}

// A null removes what lower layers set, except where a repository may only add (its null on a deny or ask list changes nothing); the
// per-layer view follows the merge exactly, so the page never lists a rule that is not in force, nor leaves one out.
func TestRuleOriginsFollowNulls(t *testing.T) {
	o := layerFixture(t, `{"permissions":{"ask":["Bash(git push:*)"],"deny":["Read(~/.ssh/**)"],"allow":["Bash(ls:*)"]}}`, "", `{"permissions":{"ask":null}}`)
	o.Overrides = map[string]any{"permissions": map[string]any{"allow": nil}}
	got, err := RuleOrigins(o)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ask * Bash(git push:*) <- user", "deny * Read(~/.ssh/**) <- user"}
	if rows := originRows(got); !reflect.DeepEqual(rows, want) {
		t.Errorf("origins %q, want %q", rows, want)
	}
}

func TestListOriginsOfHooks(t *testing.T) {
	user := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"guard.sh"}]}]}}`
	project := `{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"fmt.sh"}]}]}}`
	o := layerFixture(t, user, project, "")
	got, err := ListOrigins(o, "hooks", "PreToolUse")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != "user" || got[1].Kind != "project" {
		t.Fatalf("a trusted project's hooks run after the user's: %+v", got)
	}
	o.UntrustedProject = true
	got, _ = ListOrigins(o, "hooks", "PreToolUse")
	if len(got) != 1 || got[0].Kind != "user" {
		t.Errorf("an untrusted project's hooks are left out: %+v", got)
	}
}

func TestLoadLayersAreCopiesOfWhatLoadApplies(t *testing.T) {
	o := layerFixture(t, `{"models":{"default":"a/b"}}`, `{"permissions":{"mode":"yolo"},"models":{"favorites":["x/y"]}}`, "")
	o.UntrustedProject = true
	ls, rep, err := LoadLayers(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 || ls[0].Kind != "user" || ls[1].Kind != "project" || rep == nil {
		t.Fatalf("layers %+v", ls)
	}
	if perms, _ := ls[1].Values["permissions"].(map[string]any); perms["mode"] != nil {
		t.Errorf("an untrusted project's mode is not among what was applied: %v", ls[1].Values)
	}
	ls[0].Values["models"] = "changed"
	again, _, _ := LoadLayers(o)
	if _, ok := again[0].Values["models"].(map[string]any); !ok {
		t.Error("changing a returned layer changed the next load")
	}
	bad := layerFixture(t, `{"models":`, "", "")
	if _, _, err := LoadLayers(bad); err == nil {
		t.Error("a file that does not parse fails as Load fails")
	}
}

func TestValuesSayWhereEachValueCameFromAndWhatItReplaced(t *testing.T) {
	o := layerFixture(t, `{"cache":{"shared_ttl":"5m"},"swarm":{"max_workers":16}}`, `{"swarm":{"max_workers":12}}`, `{"cache":{"shared_ttl":"1h"}}`,
		"SLEIPNIR_MODEL=env/model")
	vals, rep, err := Values(o)
	if err != nil || rep == nil {
		t.Fatal(err)
	}
	by := map[string]Value{}
	for _, v := range vals {
		by[v.Key] = v
	}
	if v := by["cache.shared_ttl"]; v.Value != "1h" || v.Layer != "local" || !strings.HasSuffix(v.File, "config.local.json") || v.Below["user"] != "5m" {
		t.Errorf("cache.shared_ttl: %+v", v)
	}
	if v := by["swarm.max_workers"]; v.Value != float64(12) || v.Layer != "project" || v.Below["user"] != float64(16) {
		t.Errorf("swarm.max_workers: %+v", v)
	}
	if v := by["models.default"]; v.Value != "env/model" || v.Layer != "env" || v.File != "env:SLEIPNIR_MODEL" {
		t.Errorf("models.default: %+v", v)
	}
	if v, ok := by["tools.max_output_chars"]; !ok || v.Layer != "defaults" || v.File != "" || v.Below != nil {
		t.Errorf("a default: %+v (present %v)", v, ok)
	}
	if !sort.SliceIsSorted(vals, func(i, j int) bool { return vals[i].Key < vals[j].Key }) {
		t.Error("rows are sorted by key")
	}
}

func TestSaveKeepsEveryConcurrentChangeOfOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".sleipnir", "config.json")
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			role := fmt.Sprintf("r%02d", i)
			if err := Save(path, map[string]any{"models": map[string]any{"roles": map[string]any{role: "p/m" + role}}}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	l, iss := loadFile("user", path)
	if l == nil {
		t.Fatalf("the file does not load: %v", iss)
	}
	roles, _ := l.tree["models"].(map[string]any)["roles"].(map[string]any)
	if len(roles) != 24 {
		t.Errorf("%d of 24 concurrent changes kept", len(roles))
	}
}

func TestWriteLockIsPerFile(t *testing.T) {
	dir := t.TempDir()
	a := WriteLock(filepath.Join(dir, "a"))
	done := make(chan struct{})
	go func() {
		unlock := WriteLock(filepath.Join(dir, "b")) // another file: not held up
		unlock()
		close(done)
	}()
	<-done
	held := make(chan struct{})
	go func() {
		unlock := WriteLock(filepath.Join(dir, ".", "a")) // the same file, spelled differently
		close(held)
		unlock()
	}()
	select {
	case <-held:
		t.Fatal("a second holder of the same file did not wait")
	default:
	}
	a()
	<-held
}

func TestEnumsAndTheTestsPreset(t *testing.T) {
	if got := PermModes(); len(got) != 5 || got[0] != "default" {
		t.Errorf("modes %v", got)
	}
	d := Dialects()
	d[0] = "changed"
	if Dialects()[0] == "changed" {
		t.Error("the lists are copies")
	}
	if len(Isolations()) == 0 || len(CacheTTLs()) != 2 {
		t.Error("enum lists are empty")
	}
	got := ExpandAllow([]string{"Edit(docs/**)", " tests ", "testsX"})
	if got[0] != "Edit(docs/**)" || got[len(got)-1] != "testsX" || len(got) < 30 {
		t.Errorf("expand: %q", got)
	}
}
