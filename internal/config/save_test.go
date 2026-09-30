package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSaveCreatesCanonicalJSONWithPrivateMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new", "dir", "config.json")
	if err := Save(path, map[string]any{"models": map[string]any{"default": "a/b"}, "cache": map[string]any{"prewarm": false, "shared_ttl": "1h"}}); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"cache\": {\n    \"prewarm\": false,\n    \"shared_ttl\": \"1h\"\n  },\n  \"models\": {\n    \"default\": \"a/b\"\n  }\n}\n"
	if got := readFile(t, path); got != want {
		t.Fatalf("file =\n%q\nwant\n%q", got, want)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("created directory mode = %o, want 700", di.Mode().Perm())
	}
}

func TestSaveMergesIntoAnExistingFileAndKeepsUnknownKeysAtAnyDepth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`// my settings
{
  "models": {"default": "old/model", "future_field": {"deep": [1, 2]}},
  "providers": {"a": {"dialect": "anthropic", "x_experimental": true}},
  "zz_unknown_top": {"keep": "me"},
  "swarm": {"max_agents": 3,},
}`), 0o640); err != nil {
		t.Fatal(err)
	}
	err := Save(path, map[string]any{
		"models":    map[string]any{"default": "new/model", "compactor": "c/d"},
		"providers": map[string]any{"a": map[string]any{"base_url": "https://a.example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "my settings") || strings.Contains(got, "// ") {
		t.Errorf("comments are not preserved (documented): %s", got)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("the result must be plain JSON: %v\n%s", err, got)
	}
	models := v["models"].(map[string]any)
	if models["default"] != "new/model" || models["compactor"] != "c/d" {
		t.Errorf("models = %v", models)
	}
	if !reflect.DeepEqual(models["future_field"], map[string]any{"deep": []any{float64(1), float64(2)}}) {
		t.Errorf("unknown nested key lost: %v", models)
	}
	a := v["providers"].(map[string]any)["a"].(map[string]any)
	if a["dialect"] != "anthropic" || a["base_url"] != "https://a.example.com" || a["x_experimental"] != true {
		t.Errorf("provider a = %v", a)
	}
	if !reflect.DeepEqual(v["zz_unknown_top"], map[string]any{"keep": "me"}) || v["swarm"].(map[string]any)["max_agents"] != float64(3) {
		t.Errorf("untouched keys changed: %v", v)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("an existing file keeps its mode, got %o", fi.Mode().Perm())
	}
	// Canonical: sorted keys, stable output.
	if err := Save(path, nil); err != nil {
		t.Fatal(err)
	}
	if again := readFile(t, path); again != got {
		t.Errorf("saving an empty patch must not change canonical output:\n%s\n%s", got, again)
	}
}

func TestSaveListsReplaceAndNullDeletes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, map[string]any{
		"tools":     map[string]any{"web_allow_hosts": []string{"a.example.com", "b.example.com"}},
		"models":    map[string]any{"default": "a/b", "compactor": "c/d"},
		"providers": map[string]any{"p": map[string]any{"dialect": "anthropic"}, "q": map[string]any{}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, map[string]any{
		"tools":     map[string]any{"web_allow_hosts": []string{"only.example.com"}},
		"models":    map[string]any{"default": nil},
		"providers": map[string]any{"q": nil},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(LoadOpts{Home: filepath.Dir(filepath.Dir(path)), Root: t.TempDir(), Environ: noEnv})
	_ = cfg
	_ = err
	var v map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &v); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v["tools"], map[string]any{"web_allow_hosts": []any{"only.example.com"}}) {
		t.Errorf("lists replace: %v", v["tools"])
	}
	models := v["models"].(map[string]any)
	if _, ok := models["default"]; ok || models["compactor"] != "c/d" {
		t.Errorf("null deletes only its key: %v", models)
	}
	provs := v["providers"].(map[string]any)
	if _, ok := provs["q"]; ok || provs["p"] == nil {
		t.Errorf("providers = %v", provs)
	}
}

func TestSaveAcceptsStructsAndTypedSlicesInPatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	patch := map[string]any{
		"models":      Models{Default: "a/b", Roles: map[string]string{"planner": "c/d"}},
		"permissions": map[string]any{"allow": []string{"Read"}, "mode": "plan"},
		"swarm":       map[string]int{"max_agents": 6},
	}
	if err := Save(path, patch); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(LoadOpts{Home: t.TempDir(), Root: t.TempDir(), Environ: noEnv, Overrides: nil})
	if err != nil {
		t.Fatal(err)
	}
	_ = cfg
	var v map[string]any
	json.Unmarshal([]byte(readFile(t, path)), &v)
	if v["swarm"].(map[string]any)["max_agents"] != float64(6) || v["models"].(map[string]any)["roles"].(map[string]any)["planner"] != "c/d" {
		t.Fatalf("file = %v", v)
	}
}

func TestSaveRefusesToWriteAnInvalidConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := "{\n  \"models\": {\n    \"default\": \"a/b\"\n  }\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		patch map[string]any
		want  string
	}{
		{"bad enum", map[string]any{"cache": map[string]any{"shared_ttl": "9h"}}, `cache.shared_ttl: must be "5m" or "1h"`},
		{"wrong type", map[string]any{"swarm": map[string]any{"max_agents": "many"}}, "swarm.max_agents: expected an integer, got a string"},
		{"wrong shape", map[string]any{"models": []string{"x"}}, "models: expected an object, got a list"},
		{"bad model ref", map[string]any{"models": map[string]any{"default": "nomodel"}}, "models.default: must look like"},
		{"unencodable", map[string]any{"x": make(chan int)}, "cannot be encoded"},
	}
	for _, tc := range tests {
		err := Save(path, tc.patch)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", tc.name, err, tc.want)
		}
		if got := readFile(t, path); got != original {
			t.Errorf("%s: the file was modified by a refused save:\n%s", tc.name, got)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("leftover files after refused saves: %v", entries)
	}
}

func TestSaveRefusesToOverwriteAFileItCannotParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	broken := "{\n  \"models\": {\"default\": \"a/b\"\n  \"cache\": {}\n}"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Save(path, map[string]any{"ui": map[string]any{"theme": "dark"}})
	if err == nil || !strings.Contains(err.Error(), "does not parse") || !strings.Contains(err.Error(), path+":3:3") {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, path) != broken {
		t.Fatal("the user's hand-edited file must be left exactly as it was")
	}
	// A top-level value that is not an object is refused too.
	os.WriteFile(path, []byte(`["not", "an", "object"]`), 0o600)
	if err := Save(path, map[string]any{"ui": map[string]any{"theme": "x"}}); err == nil {
		t.Fatal("a top-level list must be refused")
	}
}

func TestSaveWritesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "sleipnir-config.json")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte(`{"ui": {"theme": "old"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "home", ".sleipnir", "config.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := Save(link, map[string]any{"ui": map[string]any{"theme": "new"}}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink must stay a symlink: %v %v", fi, err)
	}
	if !strings.Contains(readFile(t, real), `"theme": "new"`) {
		t.Fatalf("the target was not updated: %s", readFile(t, real))
	}
}

func TestSaveLeavesNoTemporaryFilesEvenOnFailure(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file should go makes the final rename fail.
	blocked := filepath.Join(dir, "config.json")
	if err := os.MkdirAll(filepath.Join(blocked, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Save(blocked, map[string]any{"ui": map[string]any{"theme": "x"}}); err == nil {
		t.Fatal("expected an error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("directory contents = %v", entries)
	}
	if err := Save("", nil); err == nil {
		t.Fatal("an empty path is an error")
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	path := UserConfigPath(home)
	patch := map[string]any{
		"models":    map[string]any{"default": "openrouter/vendor/model:free", "roles": map[string]any{"planner": "anthropic/opus"}},
		"providers": map[string]any{"openrouter": map[string]any{"dialect": "openai-chat", "base_url": "https://openrouter.ai/api/v1", "api_key_env": "OPENROUTER_API_KEY", "headers": map[string]any{"HTTP-Referer": "https://example.com"}}},
		"cache":     map[string]any{"shared_ttl": "1h", "prewarm": false, "affinity_shards": 4},
		"swarm":     map[string]any{"budget_usd": 12.5, "isolation": "worktree"},
		"tools":     map[string]any{"web_allow_hosts": []string{"docs.example.com"}},
	}
	if err := Save(path, patch); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(LoadOpts{Home: home, Root: root, Environ: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Default != "openrouter/vendor/model:free" || cfg.Cache.SharedTTL != "1h" || cfg.Cache.Prewarm || cfg.Cache.AffinityShards != 4 ||
		cfg.Swarm.BudgetUSD != 12.5 || cfg.Providers["openrouter"].Headers["HTTP-Referer"] != "https://example.com" ||
		!reflect.DeepEqual(cfg.Tools.WebAllowHosts, []string{"docs.example.com"}) {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestConcurrentSavesNeverProduceATornFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, map[string]any{"ui": map[string]any{"theme": "start"}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var readErr error
	var rmu sync.Mutex
	go func() { // a reader that must always see complete, parseable JSON
		for {
			select {
			case <-stop:
				return
			default:
				b, err := os.ReadFile(path)
				var v map[string]any
				if err == nil {
					err = json.Unmarshal(b, &v)
				}
				if err != nil {
					rmu.Lock()
					readErr = err
					rmu.Unlock()
					return
				}
			}
		}
	}()
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				// Concurrent read-modify-write may lose an update (documented), but
				// must never fail or leave a half-written file.
				_ = Save(path, map[string]any{"ui": map[string]any{"theme": fmt.Sprintf("g%d-%d", g, i)}})
			}
		}(g)
	}
	wg.Wait()
	close(stop)
	rmu.Lock()
	defer rmu.Unlock()
	if readErr != nil {
		t.Fatalf("a reader saw a torn file: %v", readErr)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("leftover temp files: %v", entries)
	}
}
