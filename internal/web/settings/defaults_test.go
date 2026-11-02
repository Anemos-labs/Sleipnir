package settings

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// A model kept from the Models page is written to the user's file, read back as the default of the next start with the file it came
// from, and a layer that still outranks the file (a project's file, a --model of the server) is named in the answer.
func TestDefaultModelIsKeptAndItsSourceShown(t *testing.T) {
	e := newEnv(t, nil)
	var v wire.ModelsView
	e.ok("GET", "/api/models", nil, nil, &v)
	if d := v.Saved["default"]; d.Ref != "" || d.Key != "models.default" || v.SaveFile != "~/.sleipnir/config.json" {
		t.Fatalf("nothing saved yet: %+v %q", v.Saved, v.SaveFile)
	}

	var r wire.DefaultReply
	e.ok("POST", "/api/models/default", wire.DefaultRequest{Ref: "acme/big"}, nil, &r)
	if !r.OK || r.Over != "" || r.Saved.Ref != "acme/big" || r.Saved.Layer != "user" || r.Saved.From != "~/.sleipnir/config.json" || r.File != "~/.sleipnir/config.json" {
		t.Errorf("saved: %+v", r)
	}
	cfg, _, err := config.Load(config.LoadOpts{Home: e.home, Cwd: e.home, UntrustedProject: true})
	if err != nil || cfg.Models.Default != "acme/big" {
		t.Fatalf("the file holds %q (%v)", cfg.Models.Default, err)
	}
	e.ok("POST", "/api/models/default", wire.DefaultRequest{Ref: "acme/small", Role: "judge"}, nil, &r)
	cfg, _, _ = config.Load(config.LoadOpts{Home: e.home, Cwd: e.home, UntrustedProject: true})
	if cfg.Models.Roles["judge"] != "acme/small" || cfg.Models.Default != "acme/big" || r.Saved.Key != "models.roles.judge" {
		t.Errorf("a role's default: %+v %+v", cfg.Models, r)
	}
	v = wire.ModelsView{}
	e.ok("GET", "/api/models", nil, nil, &v)
	if v.Saved["default"].Ref != "acme/big" || v.Saved["manager"].Ref != "acme/big" {
		t.Errorf("the page reads the saved default: %+v", v.Saved)
	}

	// the manager: an entry of models.roles.manager in the user's file would outrank the default written, so it goes
	if err := config.Save(config.UserConfigPath(e.home), map[string]any{"models": map[string]any{"roles": map[string]any{"manager": "acme/old"}}}); err != nil {
		t.Fatal(err)
	}
	e.ok("POST", "/api/models/default", wire.DefaultRequest{Ref: "acme/new", Role: "manager"}, nil, &r)
	cfg, _, _ = config.Load(config.LoadOpts{Home: e.home, Cwd: e.home, UntrustedProject: true})
	if cfg.ModelFor("manager") != "acme/new" || cfg.Models.Roles["judge"] != "acme/small" || r.Over != "" {
		t.Errorf("the manager's default: %+v %+v", cfg.Models, r)
	}

	// a project's file outranks the user's: the answer and the page say which file wins
	if err := os.MkdirAll(filepath.Join(e.repo, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, config.ProjectConfigPath(e.repo), `{"models": {"default": "acme/project"}}`)
	e.ok("POST", "/api/models/default", wire.DefaultRequest{Ref: "acme/big"}, nil, &r)
	if r.Saved.Ref != "acme/project" || r.Saved.Layer != "project" || !strings.Contains(r.Over, "wins over your file") {
		t.Errorf("an outranking project file: %+v", r)
	}
	v = wire.ModelsView{}
	e.ok("GET", "/api/models", nil, nil, &v)
	if v.Saved["default"].Layer != "project" || !strings.HasSuffix(v.Saved["default"].From, filepath.Join(".sleipnir", "config.json")) {
		t.Errorf("the page names the project's file: %+v", v.Saved["default"])
	}

	// the server's --model wins for its new sessions
	if err := os.Remove(config.ProjectConfigPath(e.repo)); err != nil {
		t.Fatal(err)
	}
	e.svc.o.Model = "acme/flag"
	e.ok("POST", "/api/models/default", wire.DefaultRequest{Ref: "acme/big"}, nil, &r)
	if !strings.Contains(r.Over, "--model acme/flag") {
		t.Errorf("the server's --model: %+v", r)
	}
	v = wire.ModelsView{}
	e.ok("GET", "/api/models", nil, nil, &v)
	if d := v.Saved["default"]; d.Ref != "acme/flag" || d.Layer != "flag" {
		t.Errorf("the page names the flag: %+v", d)
	}

	for _, bad := range []wire.DefaultRequest{{Ref: "nonsense"}, {Ref: "a/b c"}, {Ref: ""}} {
		if rec := e.do("POST", "/api/models/default", bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%+v: %d", bad, rec.Code)
		}
	}
	if rec := e.do("POST", "/api/models/default", wire.DefaultRequest{Ref: "a/b", Role: "Bad Role"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a role that is not a name: %d", rec.Code)
	}
	write(t, config.UserConfigPath(e.home), `{"models": `)
	if rec := e.do("POST", "/api/models/default", wire.DefaultRequest{Ref: "a/b"}, nil); rec.Code != http.StatusUnprocessableEntity || strings.Contains(rec.Body.String(), e.home) {
		t.Errorf("a configuration that does not load: %d %s", rec.Code, rec.Body)
	}
}
