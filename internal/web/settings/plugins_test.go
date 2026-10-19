package settings

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// rowByID finds a plugin row in a catalogue.
func rowByID(t *testing.T, v pluginsView, id string) pluginRow {
	t.Helper()
	for _, r := range v.Plugins {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no %s in the catalogue", id)
	return pluginRow{}
}

func TestPluginsCatalogueListsEveryBuiltinWithItsState(t *testing.T) {
	e := newEnv(t, func(home, repo string) {
		write(t, config.UserConfigPath(home), `{"plugins":{"enabled":{"web":false},"settings":{"goal":{"api_key":"sk-1","rounds":3}}}}`)
	})
	var v pluginsView
	e.ok("GET", "/api/plugins", nil, nil, &v)
	if v.Profile != "standard" || len(v.Profiles) < 4 {
		t.Fatalf("profile %q, %d profiles", v.Profile, len(v.Profiles))
	}
	if r := rowByID(t, v, "web"); r.Enabled || r.State != "off" || r.Layer != "user" {
		t.Errorf("web = %+v", r)
	}
	if r := rowByID(t, v, "mailman"); r.Enabled || r.Default {
		t.Errorf("mailman is off by default: %+v", r)
	}
	if r := rowByID(t, v, "core:kernel"); !r.Core || !r.Enabled || r.CoreReason == "" {
		t.Errorf("core:kernel = %+v", r)
	}
	g := rowByID(t, v, "goal")
	if g.Config["api_key"] != "(withheld)" || g.Config["rounds"] != float64(3) {
		t.Errorf("goal settings = %v", g.Config)
	}
	var sv pluginsView
	e.ok("GET", "/api/sessions/t1/plugins", nil, nil, &sv)
	if len(sv.Plugins) != len(v.Plugins) {
		t.Errorf("the session route lists %d, the other %d", len(sv.Plugins), len(v.Plugins))
	}
}

func TestPluginWritesAreMinimalDiffsInTheChosenFile(t *testing.T) {
	e := newEnv(t, nil)
	var r pluginReply
	e.ok("POST", "/api/plugins/web", pluginWrite{Enabled: ptr(false)}, nil, &r)
	if r.Plugin.Enabled || r.Plugin.State != "off" {
		t.Fatalf("web after the write = %+v", r.Plugin)
	}
	user := config.UserConfigPath(e.home)
	if b, _ := os.ReadFile(user); !strings.Contains(string(b), `"web": false`) {
		t.Errorf("user file = %s", b)
	}
	e.ok("POST", "/api/plugins/web", pluginWrite{Enabled: ptr(true)}, nil, &r)
	if b, _ := os.ReadFile(user); strings.Contains(string(b), `"web"`) {
		t.Errorf("a choice equal to the default is removed: %s", b)
	}
	e.ok("POST", "/api/plugins/mailman", pluginWrite{Enabled: ptr(true), Scope: "project"}, nil, &r)
	if b, _ := os.ReadFile(filepath.Join(e.repo, ".sleipnir", "config.json")); !strings.Contains(string(b), `"mailman": true`) {
		t.Errorf("project file = %s", b)
	}
	if b, _ := os.ReadFile(user); strings.Contains(string(b), "mailman") {
		t.Errorf("a project write stays out of the user file: %s", b)
	}
	if rec := e.do("DELETE", "/api/plugins/mailman?scope=project", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(e.repo, ".sleipnir", "config.json")); strings.Contains(string(b), "mailman") {
		t.Errorf("reset leaves the project file without the row: %s", b)
	}
}

func TestPluginWritesAreRefusedForCoreUnknownAndBadScope(t *testing.T) {
	e := newEnv(t, nil)
	if rec := e.do("POST", "/api/plugins/core:kernel", pluginWrite{Enabled: ptr(false)}, nil); rec.Code != http.StatusConflict || errCode(rec) != "core" {
		t.Errorf("core: %d %s", rec.Code, errCode(rec))
	}
	if rec := e.do("POST", "/api/plugins/nope", pluginWrite{Enabled: ptr(false)}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/plugins/BAD%20ID", pluginWrite{Enabled: ptr(false)}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/plugins/web", pluginWrite{Enabled: ptr(false), Scope: "everyone"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad scope: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/plugins/web", pluginWrite{}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty write: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/plugins/profile", profileWrite{Profile: "nope"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown profile: %d", rec.Code)
	}
}

func TestPluginProfileIsSavedAndChangesTheDefaults(t *testing.T) {
	e := newEnv(t, nil)
	var v pluginsView
	e.ok("POST", "/api/plugins/profile", profileWrite{Profile: "minimal"}, nil, &v)
	if v.Profile != "minimal" {
		t.Fatalf("profile = %q", v.Profile)
	}
	if r := rowByID(t, v, "web"); r.Enabled || r.Layer != "profile" {
		t.Errorf("web under minimal = %+v", r)
	}
	if r := rowByID(t, v, "fs"); !r.Enabled {
		t.Errorf("fs stays on under minimal")
	}
	e.ok("POST", "/api/plugins/profile", profileWrite{Profile: "standard"}, nil, &v)
	if b, _ := os.ReadFile(config.UserConfigPath(e.home)); strings.Contains(string(b), "profile") {
		t.Errorf("the default profile is not written: %s", b)
	}
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }

func TestDeliveryRowSaysWhenGhIsMissing(t *testing.T) {
	e := newEnv(t, nil)
	was := lookPath
	t.Cleanup(func() { lookPath = was })
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	var v pluginsView
	e.ok("GET", "/api/plugins", nil, nil, &v)
	if r := rowByID(t, v, "delivery"); !strings.Contains(r.Unavailable, "gh") {
		t.Errorf("delivery without gh = %+v", r)
	}
	lookPath = func(string) (string, error) { return "/usr/bin/gh", nil }
	var with pluginsView
	e.ok("GET", "/api/plugins", nil, nil, &with)
	if r := rowByID(t, with, "delivery"); r.Unavailable != "" {
		t.Errorf("delivery with gh = %q", r.Unavailable)
	}
}
