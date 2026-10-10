package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/catalog"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/gateway"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// fakeCatalogue is a catalogue read: a priced chat model with tools, one with no prices, an embedding model and a plan's model, and a
// source that failed with a credential in its error.
func fakeCatalogue(at time.Time) catalog.Result {
	mk := func(ref string, ctx int, in, out float64, modality string, sup ...string) catalog.Entry {
		return catalog.Entry{Ref: ref, Entry: gateway.Entry{Model: cost.Model{ID: ref, ContextTokens: ctx, Price: cost.Price{InputPerM: in, OutputPerM: out, CacheReadPerM: in}}, Modality: modality, Supported: sup}}
	}
	plan := mk("chatgpt/gpt-x", 400_000, 0, 0, "text->text", "tools", "reasoning_effort")
	plan.Model.Provider = catalog.PlanProvider
	es := []catalog.Entry{
		mk("acme/big", 400_000, 3, 15, "text->text", "tools", "reasoning"),
		mk("acme/small", 128_000, 0.2, 0.8, "text->text", "tools"),
		mk("local/ids-only", 0, 0, 0, ""),
		mk("acme/embed", 8000, 0.01, 0, "text->embedding"),
		plan,
	}
	res := catalog.Result{Entries: es, AsOf: at, Errors: []error{errors.New("broken: http 401 for key sk-canaryInError0123456789abcdef")}}
	for _, e := range es {
		res.Rows = append(res.Rows, catalog.RowOf(e))
	}
	return res
}

func TestModelsPage(t *testing.T) {
	e := newEnv(t, nil)
	at := time.Unix(1_700_000_000, 0)
	var asked []catalog.Options
	e.svc.fetch = func(_ context.Context, o catalog.Options) catalog.Result {
		asked = append(asked, o)
		return fakeCatalogue(at)
	}
	refs := func(q string) []string {
		var v wire.ModelsView
		e.ok("GET", "/api/models"+q, nil, nil, &v)
		var out []string
		for _, m := range v.Models {
			out = append(out, m.Ref)
		}
		return out
	}
	for q, want := range map[string][]string{
		"":                      {"acme/big", "acme/small", "chatgpt/gpt-x", "local/ids-only"},
		"?all=1":                {"acme/big", "acme/embed", "acme/small", "chatgpt/gpt-x", "local/ids-only"},
		"?tools=1&reasoning=1":  {"acme/big", "chatgpt/gpt-x"},
		"?max_price=1":          {"acme/small", "local/ids-only"},
		"?min_context=200k":     {"acme/big", "chatgpt/gpt-x"},
		"?q=ACME+sm":            {"acme/small"},
		"?fav=1":                nil,
		"?tab=t1&all=0&tools=0": {"acme/big", "acme/small", "chatgpt/gpt-x", "local/ids-only"},
	} {
		if got := refs(q); !slices.Equal(got, want) {
			t.Errorf("GET /api/models%s: %q, want %q", q, got, want)
		}
	}
	if rec := e.do("GET", "/api/models?max_price=cheap", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad price filter: %d", rec.Code)
	}
	if rec := e.do("GET", "/api/models?tab=nope", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("a tab that is not there: %d", rec.Code)
	}
	var v wire.ModelsView
	body := e.ok("GET", "/api/models?refresh=1", nil, nil, &v)
	if !asked[len(asked)-1].Refresh || asked[0].Refresh {
		t.Error("refresh=1 asks the catalogues again, and only then")
	}
	if strings.Contains(string(body), "sk-canaryInError") || len(v.Errors) != 1 {
		t.Errorf("the source's error is shown without its credential: %q", v.Errors)
	}
	if v.FetchedAt != at.UnixMilli() || len(v.Roles) < 8 || !slices.Contains(v.RoleOrder, "scout") || slices.Contains(v.RoleOrder, "manager") {
		t.Errorf("fetchedAt %d roles %d order %v", v.FetchedAt, len(v.Roles), v.RoleOrder)
	}
	if v.Efforts[0] != "default" || len(v.Efforts) != 8 {
		t.Errorf("efforts %v", v.Efforts)
	}
	for _, m := range v.Models {
		switch m.Ref {
		case "local/ids-only", "chatgpt/gpt-x":
			if m.PriceKnown || m.In != nil || m.Out != nil {
				t.Errorf("%s has no price: %+v", m.Ref, m)
			}
		case "acme/big":
			if !m.PriceKnown || *m.In != 3 || *m.Out != 15 || !m.Tools || !m.Reasoning || m.Provider != "acme" {
				t.Errorf("acme/big: %+v", m)
			}
		}
	}

	// favourites: star, idempotent, listed first
	var fr favReply
	e.ok("POST", "/api/models/fav", wire.FavRequest{Ref: "local/ids-only", On: true}, nil, &fr)
	e.ok("POST", "/api/models/fav", wire.FavRequest{Ref: "local/ids-only", On: true}, nil, &fr)
	if !fr.On || !slices.Equal(fr.Favs, []string{"local/ids-only"}) {
		t.Errorf("fav: %+v", fr)
	}
	if got := refs(""); got[0] != "local/ids-only" {
		t.Errorf("a favourite comes first: %v", got)
	}
	if got := refs("?fav=1"); !slices.Equal(got, []string{"local/ids-only"}) {
		t.Errorf("only favourites: %v", got)
	}
	e.ok("POST", "/api/models/fav", wire.FavRequest{Ref: "local/ids-only", On: false}, nil, &fr)
	if len(fr.Favs) != 0 {
		t.Errorf("unstar: %+v", fr)
	}
	if rec := e.do("POST", "/api/models/fav", wire.FavRequest{Ref: "nonsense", On: true}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a reference without a provider: %d", rec.Code)
	}
	// a file with comments: the page is told before it writes, and once it has
	write(t, config.UserConfigPath(e.home), "{\n  // my settings\n  \"models\": {}\n}\n")
	var mr modelsReply
	e.ok("GET", "/api/models", nil, nil, &mr)
	if !strings.Contains(mr.ConfigNote, "comments") {
		t.Errorf("no warning about the comments: %q", mr.ConfigNote)
	}
	e.ok("POST", "/api/models/fav", wire.FavRequest{Ref: "a/b", On: true}, nil, &fr)
	if !strings.Contains(fr.Note, "comments") {
		t.Errorf("the write does not say the comments went: %+v", fr)
	}
	mr = modelsReply{}
	e.ok("GET", "/api/models", nil, nil, &mr)
	if mr.ConfigNote != "" {
		t.Errorf("a file without comments: %q", mr.ConfigNote)
	}
	write(t, config.UserConfigPath(e.home), `{"models": `)
	if rec := e.do("POST", "/api/models/fav", wire.FavRequest{Ref: "a/b", On: true}, nil); rec.Code != http.StatusUnprocessableEntity || strings.Contains(rec.Body.String(), e.home) {
		t.Errorf("a configuration that does not load: %d %s", rec.Code, rec.Body)
	}
}

func TestPermissionsPageNamesTheOriginOfEveryRule(t *testing.T) {
	e := newEnv(t, func(home, repo string) {
		write(t, config.UserConfigPath(home), `{"permissions":{"allow":["Bash(git status:*)"],"deny":["Read(~/.ssh/**)"]}}`)
		write(t, config.ProjectConfigPath(repo), `{"permissions":{"allow":["Bash(go test:*)"],"deny":["Read(./.env)"]}}`)
	})
	e.tab.rules = []wire.Rule{
		{Effect: "allow", Rule: "Bash(npm test)", Origin: "don't ask again"},
		{Effect: "deny", Rule: "Read(~/.ssh/**)", Origin: "user config", Fixed: true},
	}
	var v wire.PermissionsView
	e.ok("GET", "/api/sessions/t1/permissions", nil, nil, &v)
	find := func(effect, rule string) (wire.Rule, bool) {
		for _, r := range v.Rules[effect] {
			if r.Rule == rule {
				return r, true
			}
		}
		return wire.Rule{}, false
	}
	if r, ok := find("allow", "Bash(git status:*)"); !ok || r.Origin != "user config" || r.File != "~/.sleipnir/config.json" || !r.Fixed {
		t.Errorf("the user's allow rule (the project is not trusted): %+v %v", r, ok)
	}
	if _, ok := find("allow", "Bash(go test:*)"); ok {
		t.Error("an untrusted project's allow rule is listed as in force")
	}
	if r, ok := find("deny", "Read(./.env)"); !ok || r.Origin != "project config" || r.File != ".sleipnir/config.json" || r.Note == "" {
		t.Errorf("the project's deny rule: %+v %v", r, ok)
	}
	if r, ok := find("ask", "Edit(./.sleipnir/**)"); !ok || r.Origin != "built-in protection" || !r.Fixed {
		t.Errorf("a built-in protection: %+v %v", r, ok)
	}
	if len(v.Session) != 1 || v.Session[0].Rule != "Bash(npm test)" {
		t.Errorf("the session's own rules: %+v", v.Session)
	}
	if v.Mode != "default" || len(v.Modes) != 5 || len(v.Order) != 5 || v.ManagerWrites.Text != managerWritesText {
		t.Errorf("mode %q modes %d order %d", v.Mode, len(v.Modes), len(v.Order))
	}
	if !slices.Equal(v.TestsPreset.Rules, perm.TestsAllow) || v.TestsPreset.Name != "tests" {
		t.Errorf("tests preset: %+v", v.TestsPreset)
	}

	// a running session that trusts the project: the project's allow rule replaces the user's, and the --allow flag is listed
	e.startSession(session.Options{TrustProject: true, NoMCP: true, Allow: []string{"Bash(make lint)"}, Mode: perm.ModeAcceptEdits})
	v = wire.PermissionsView{}
	e.ok("GET", "/api/sessions/t1/permissions", nil, nil, &v)
	if r, ok := find("allow", "Bash(go test:*)"); !ok || r.Origin != "project config" {
		t.Errorf("a trusted project's allow rule: %+v %v", r, ok)
	}
	if r, ok := find("allow", "Bash(make lint)"); !ok || r.Origin != "flag" || r.File != "--allow" {
		t.Errorf("the --allow flag: %+v %v", r, ok)
	}
	if v.Mode != "accept-edits" {
		t.Errorf("the session's mode: %q", v.Mode)
	}
}

func TestTrustPageAndItsConfirmation(t *testing.T) {
	e := newEnv(t, func(home, repo string) { write(t, filepath.Join(repo, "AGENTS.md"), "be careful\n") })
	var v wire.TrustView
	e.ok("GET", "/api/sessions/t1/trust", nil, nil, &v)
	if v.Project.State != "not trusted" || len(v.Files) != 1 || v.Files[0].Path != "AGENTS.md" || len(v.Files[0].Hash) != 16 || len(v.Question.Options) != 3 {
		t.Fatalf("before: %+v", v)
	}
	if len(v.Ledger) != 1 || v.Ledger[0].State != "not trusted" {
		t.Errorf("the project offered in the ledger: %+v", v.Ledger)
	}
	// on:true without the challenge's confirmation
	if rec := e.do("POST", "/api/trust", trustRequest{Dir: e.repo, On: true}, nil); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("trust without a confirmation: %d", rec.Code)
	}
	var ch wire.TrustChallenge
	e.ok("GET", "/api/trust/challenge?dir="+e.repo, nil, nil, &ch)
	if ch.Confirm == "" || !strings.HasPrefix(ch.Scope, "trust:") || ch.Scope != trustScope(e.repo, ch.Digest) || len(ch.Files) != 1 {
		t.Fatalf("challenge: %+v", ch)
	}
	var tr trustReply
	e.ok("POST", "/api/trust", trustRequest{Dir: e.repo, On: true}, map[string]string{"X-Confirm": ch.Confirm}, &tr)
	if !tr.OK || tr.Files != 1 {
		t.Errorf("trusted: %+v", tr)
	}
	if rec := e.do("POST", "/api/trust", trustRequest{Dir: e.repo, On: true}, map[string]string{"X-Confirm": ch.Confirm}); rec.Code != http.StatusForbidden {
		t.Errorf("a replayed confirmation: %d", rec.Code)
	}
	v = wire.TrustView{}
	e.ok("GET", "/api/sessions/t1/trust", nil, nil, &v)
	if v.Project.State != "trusted" || v.Project.SavedDay == "" || len(v.Ledger) != 1 || v.Ledger[0].State != session.TrustUnchanged {
		t.Errorf("after: %+v", v)
	}

	// the files change between the challenge and the yes: 409 changed, with a new challenge
	e.ok("GET", "/api/trust/challenge?dir="+e.repo, nil, nil, &ch)
	write(t, filepath.Join(e.repo, "AGENTS.md"), "be careless\n")
	rec := e.do("POST", "/api/trust", trustRequest{Dir: e.repo, On: true}, map[string]string{"X-Confirm": ch.Confirm})
	var refusal struct {
		Code   string
		Detail wire.TrustChallenge
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &refusal)
	if rec.Code != http.StatusConflict || refusal.Code != "changed" || refusal.Detail.Confirm == "" || refusal.Detail.Digest == ch.Digest || refusal.Detail.Changed == "" {
		t.Fatalf("changed files: %d %s", rec.Code, rec.Body)
	}
	e.ok("POST", "/api/trust", trustRequest{Dir: e.repo, On: true}, map[string]string{"X-Confirm": refusal.Detail.Confirm}, &tr)

	// the page cannot trust a directory that is not one of the server's projects
	other := t.TempDir()
	write(t, filepath.Join(other, "AGENTS.md"), "x\n")
	if rec := e.do("GET", "/api/trust/challenge?dir="+other, nil, nil); rec.Code != http.StatusForbidden || errCode(rec) != "not_a_project" {
		t.Errorf("another directory: %d %s", rec.Code, errCode(rec))
	}
	if rec := e.do("POST", "/api/trust", trustRequest{Dir: other, On: true}, map[string]string{"X-Confirm": e.confirm(trustScope(other, "x"))}); rec.Code != http.StatusForbidden {
		t.Errorf("trusting another directory: %d", rec.Code)
	}
	if rec := e.do("GET", "/api/trust/challenge?dir=relative/path", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a relative path: %d", rec.Code)
	}

	// forget, then forget everything
	e.ok("POST", "/api/trust", trustRequest{Dir: e.repo}, nil, &tr)
	if !tr.Had {
		t.Errorf("forget: %+v", tr)
	}
	tr = trustReply{}
	e.ok("POST", "/api/trust", trustRequest{Dir: e.repo}, nil, &tr)
	if tr.Had {
		t.Error("forgetting twice: the second had nothing")
	}
	if rec := e.do("POST", "/api/trust", trustRequest{All: true, On: true}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("all with on: %d", rec.Code)
	}
	e.ok("GET", "/api/trust/challenge?dir="+e.repo, nil, nil, &ch)
	e.ok("POST", "/api/trust", trustRequest{Dir: e.repo, On: true}, map[string]string{"X-Confirm": ch.Confirm}, nil)
	e.ok("POST", "/api/trust", trustRequest{All: true}, nil, &tr)
	if tr.Forgot != 1 || len(e.svc.ledger().All()) != 0 {
		t.Errorf("forget all: %+v", tr)
	}
}

// TestLedgerWritesSerialized trusts two projects at once from two goroutines: both yeses are kept.
func TestLedgerWritesSerialized(t *testing.T) {
	e := newEnv(t, nil)
	a, b := t.TempDir(), t.TempDir()
	for _, d := range []string{a, b} {
		write(t, filepath.Join(d, "AGENTS.md"), "x "+d+"\n")
		e.host.projects = append(e.host.projects, wire.Project{Dir: d})
	}
	for round := 0; round < 5; round++ {
		done := make(chan struct{}, 2)
		for _, d := range []string{a, b} {
			go func() {
				defer func() { done <- struct{}{} }()
				var ch wire.TrustChallenge
				rec := e.do("GET", "/api/trust/challenge?dir="+d, nil, nil)
				_ = json.Unmarshal(rec.Body.Bytes(), &ch)
				if rec := e.do("POST", "/api/trust", trustRequest{Dir: d, On: true}, map[string]string{"X-Confirm": ch.Confirm}); rec.Code != http.StatusOK {
					t.Errorf("trust %s: %d %s", d, rec.Code, rec.Body)
				}
			}()
		}
		<-done
		<-done
		if all := e.svc.ledger().All(); len(all) != 2 {
			t.Fatalf("round %d: %d of 2 projects kept", round, len(all))
		}
		e.ok("POST", "/api/trust", trustRequest{All: true}, nil, nil)
	}
}

func TestMCPPage(t *testing.T) {
	e := newEnv(t, func(home, repo string) {
		writeJSON(t, config.UserConfigPath(home), map[string]any{"mcp": map[string]any{"ref": refServer("")}})
		writeJSON(t, filepath.Join(repo, ".mcp.json"), map[string]any{"mcpServers": map[string]any{"proj": refServer("p")}})
	})
	// no session yet
	if rec := e.do("POST", "/api/sessions/t1/mcp/ref/reconnect", nil, nil); rec.Code != http.StatusConflict || errCode(rec) != "idle" {
		t.Errorf("reconnect without a session: %d %s", rec.Code, errCode(rec))
	}
	e.startSession(session.Options{TrustProject: true})
	var v mcpView
	e.ok("GET", "/api/sessions/t1/mcp", nil, nil, &v)
	by := map[string]mcpServer{}
	for _, s := range v.Servers {
		by[s.Name] = s
	}
	ref, proj := by["ref"], by["proj"]
	if ref.State != "running" || !slices.Contains(ref.Tools, "echo") || ref.From != "your config" || len(ref.Prompts) == 0 || ref.Fingerprint != "" {
		t.Errorf("the user's server: %+v", ref)
	}
	if proj.State != "needs approval" || proj.Fingerprint == "" || proj.ConfirmScope != mcpScope(e.repo, "proj", proj.Fingerprint) || proj.Origin != "project .mcp.json" {
		t.Errorf("the project's server: %+v", proj)
	}
	if !strings.Contains(proj.Describe, "runs a local program") || len(v.FrozenTools) == 0 || !strings.Contains(v.SessionNote, "ref") {
		t.Errorf("describe %q frozen %v note %q", proj.Describe, v.FrozenTools, v.SessionNote)
	}
	if rec := e.do("POST", "/api/sessions/t1/mcp/proj/test", nil, nil); rec.Code != http.StatusConflict || errCode(rec) != "needs_approval" {
		t.Errorf("testing an unapproved project entry: %d %s", rec.Code, errCode(rec))
	}
	if rec := e.do("POST", "/api/sessions/t1/mcp/proj/approve", nil, nil); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("approve without a confirmation: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/sessions/t1/mcp/proj/approve", nil, map[string]string{"X-Confirm": e.confirm("mcp.approve:0000000000000000")}); rec.Code != http.StatusForbidden {
		t.Errorf("approve with a confirmation of another scope: %d", rec.Code)
	}
	var res wire.MCPResult
	e.ok("POST", "/api/sessions/t1/mcp/proj/approve", nil, map[string]string{"X-Confirm": e.confirm(proj.ConfirmScope)}, &res)
	if res.T != mcpApproved || res.Cls != "ok" || !session.OpenMCPApprovals(e.home).Has(e.repo, proj.Fingerprint) {
		t.Errorf("approve: %+v", res)
	}
	e.ok("GET", "/api/sessions/t1/mcp", nil, nil, &v)
	for _, s := range v.Servers {
		if s.Name == "proj" && (!s.Approved || s.State != "not started") {
			t.Errorf("approved during the session, it starts with the next one (D-08): %+v", s)
		}
	}
	e.ok("POST", "/api/sessions/t1/mcp/proj/test", nil, nil, &res)
	if res.Cls != "ok" || !strings.Contains(res.T, "tools answered ✓") {
		t.Errorf("test of the approved entry: %+v", res)
	}
	e.ok("POST", "/api/sessions/t1/mcp/ref/reconnect", nil, nil, &res)
	if res.Cls != "ok" {
		t.Errorf("reconnect: %+v", res)
	}
	if rec := e.do("POST", "/api/sessions/t1/mcp/ref/approve", nil, nil); rec.Code != http.StatusConflict {
		t.Errorf("approving the user's own entry: %d", rec.Code)
	}
	e.ok("POST", "/api/sessions/t1/mcp/proj/revoke", nil, nil, &res)
	if res.T != mcpRevoked || session.OpenMCPApprovals(e.home).Has(e.repo, proj.Fingerprint) {
		t.Errorf("revoke: %+v", res)
	}
	if rec := e.do("POST", "/api/sessions/t1/mcp/nope/test", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown server: %d", rec.Code)
	}
}

func TestProvidersPage(t *testing.T) {
	e := newEnv(t, nil)
	t.Setenv("HEIMDALL_API_KEY", "env-key-0123456789")
	signedOut := false
	e.svc.chatgptSignOut = func(*http.Request, string) error { signedOut = true; return nil }
	t.Cleanup(func() {
		for _, n := range []string{"OPENROUTER_API_KEY", "OPENAI_API_KEY"} {
			catalog.ForgetKey(e.home, n) //nolint:errcheck // cleanup of held keys
		}
	})
	rows := func() map[string]wire.ProviderRow {
		var v wire.ProvidersView
		e.ok("GET", "/api/providers", nil, nil, &v)
		m := map[string]wire.ProviderRow{}
		for _, p := range v.Providers {
			m[p.ID] = p
		}
		if len(v.Providers) < 24 || v.Note == "" {
			t.Fatalf("%d providers", len(v.Providers))
		}
		return m
	}
	r := rows()
	if h := r["heimdall"]; h.Key != keyEnv || h.Env != "HEIMDALL_API_KEY" || !h.Recommended || !strings.Contains(h.KeyWhere, "HEIMDALL_API_KEY") {
		t.Errorf("heimdall: %+v", h)
	}
	if o := r["ollama"]; o.Key != keyNotNeeded {
		t.Errorf("a local server: %+v", o)
	}
	if c := r["chatgpt"]; c.Key != keyNone || c.SignedIn {
		t.Errorf("chatgpt without a sign-in: %+v", c)
	}
	if rec := e.do("POST", "/api/providers/heimdall/signout", nil, nil); rec.Code != http.StatusConflict || errCode(rec) != "env" {
		t.Errorf("signing out of a key from the environment: %d %s", rec.Code, errCode(rec))
	}

	// `sleipnir login openrouter` in a terminal: the page's "I ran it: check again" takes the key up
	write(t, config.AuthPath(e.home), `{"OPENROUTER_API_KEY":"stored-key-0123456789"}`)
	var pv wire.ProvidersView
	e.ok("POST", "/api/providers/recheck", nil, nil, &pv)
	if r = rows(); r["openrouter"].Key != keyStored {
		t.Fatalf("after recheck: %+v", r["openrouter"])
	}
	var row wire.ProviderRow
	e.ok("POST", "/api/providers/openrouter/signout", nil, nil, &row)
	if row.Key != keyNone {
		t.Errorf("signed out: %+v", row)
	}
	if m, _ := config.StoredKeys(e.home); m["OPENROUTER_API_KEY"] != "" {
		t.Error("the stored key is still in auth.json")
	}
	e.ok("POST", "/api/providers/openrouter/signout", nil, nil, &row) // nothing left: the same answer

	// `sleipnir logout openai` in a terminal: recheck forgets the held key
	write(t, config.AuthPath(e.home), `{"OPENAI_API_KEY":"stored-key-abcdefghij"}`)
	e.ok("POST", "/api/providers/recheck", nil, nil, nil)
	write(t, config.AuthPath(e.home), `{}`)
	e.ok("POST", "/api/providers/recheck", nil, nil, nil)
	if r = rows(); r["openai"].Key != keyNone {
		t.Errorf("after a terminal logout and recheck: %+v", r["openai"])
	}

	// a ChatGPT sign-in: who is signed in, and signing out
	write(t, filepath.Join(e.home, ".sleipnir", "chatgpt.json"), `{"version":1,"host_id":"urn:uuid:x","client_id":"c","access_token":"at","refresh_token":"rt","email":"ada@example.com","expires_at_ms":`+
		strings.TrimSpace(jsonInt(time.Now().Add(time.Hour).UnixMilli()))+`}`)
	if c := rows()["chatgpt"]; !c.SignedIn || c.Who != "ada@example.com" || c.Key != keySignedIn {
		t.Errorf("chatgpt signed in: %+v", c)
	}
	e.ok("POST", "/api/providers/chatgpt/signout", nil, nil, nil)
	if !signedOut {
		t.Error("signing out of ChatGPT ends the sign-in")
	}
	if rec := e.do("POST", "/api/providers/nowhere/signout", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown provider: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/providers/ollama/signout", nil, nil); rec.Code != http.StatusConflict {
		t.Errorf("a provider without a key: %d", rec.Code)
	}
}

// jsonInt is n as JSON.
func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

// The key route's handler (served only when KeyRoutes is on): a confirmation, a small body, the key kept and never sent back.
func TestKeyRouteHandler(t *testing.T) {
	e := newEnv(t, nil)
	e.svc.registerKeyRoute()
	t.Cleanup(func() { catalog.ForgetKey(e.home, "OPENAI_API_KEY") }) //nolint:errcheck // cleanup of a held key
	const key = "sk-browserTypedKey0123456789abcdef"
	if rec := e.do("POST", "/api/providers/openai/key", wire.KeySaveRequest{Key: key}, nil); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("without a confirmation: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/providers/openai/key", wire.KeySaveRequest{Key: "two words"}, map[string]string{"X-Confirm": e.confirm("key:openai")}); rec.Code != http.StatusBadRequest {
		t.Errorf("a key with spaces: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/providers/openai/key", `{"key":"`+strings.Repeat("k", 9<<10)+`"}`, map[string]string{"X-Confirm": e.confirm("key:openai")}); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over 8 KiB: %d", rec.Code)
	}
	rec := e.do("POST", "/api/providers/openai/key", wire.KeySaveRequest{Key: key}, map[string]string{"X-Confirm": e.confirm("key:openai")})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), key) {
		t.Fatalf("store: %d %s", rec.Code, rec.Body)
	}
	if m, _ := config.StoredKeys(e.home); m["OPENAI_API_KEY"] != key {
		t.Error("the key is not in auth.json")
	}
	if strings.Contains(e.logText(), key) {
		t.Error("the key is in the log")
	}
}

func TestSkillsPage(t *testing.T) {
	e := newEnv(t, func(home, repo string) {
		write(t, filepath.Join(home, ".sleipnir", "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Run the deploy checklist.\nargument-hint: \"[env]\"\n---\nsteps\n")
		write(t, filepath.Join(home, ".sleipnir", "skills", "notes", "SKILL.md"), "---\nname: notes\ndescription: Private notes.\ndisable-model-invocation: true\n---\nx\n")
		write(t, filepath.Join(home, ".sleipnir", "commands", "standup.md"), "---\ndescription: Summarise the day\n---\nsay what happened\n")
		write(t, config.UserConfigPath(home), `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"guard.sh --token sekret-value-123","timeout":5},
			{"type":"http","url":"https://h.test/x?k=v","headers":{"Authorization":"hook-header-value"}}]}]}}`)
	})
	var v wire.SkillsView
	body := e.ok("GET", "/api/sessions/t1/skills", nil, nil, &v)
	if len(v.Skills) != 2 || v.Skills[0].Name != "deploy" || v.Skills[0].Scope != "user" || v.Skills[0].Source != "~/.sleipnir/skills/deploy/SKILL.md" || !v.Skills[1].YouOnly {
		t.Errorf("skills: %+v", v.Skills)
	}
	if v.SkillsBudget == nil || v.SkillsBudget.Used <= 0 || v.SkillsBudget.Tokens <= v.SkillsBudget.Used {
		t.Errorf("budget: %+v", v.SkillsBudget)
	}
	if len(v.Commands) != 1 || v.Commands[0].Name != "standup" || v.Commands[0].Description != "Summarise the day" {
		t.Errorf("commands: %+v", v.Commands)
	}
	if v.Hooks == nil || len(v.Hooks.Configured) != 2 || len(v.Hooks.Events) < 10 {
		t.Fatalf("hooks: %+v", v.Hooks)
	}
	h := v.Hooks.Configured[0]
	if h.Event != "PreToolUse" || h.Matcher != "Bash" || h.Timeout != 5 || !strings.HasPrefix(h.Origin, "user config") || !strings.Contains(h.Command, "guard.sh") {
		t.Errorf("hook: %+v", h)
	}
	for _, secret := range []string{"sekret-value-123", "hook-header-value", "k=v"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("%q is in the hooks page", secret)
		}
	}
}

func TestConfigPage(t *testing.T) {
	e := newEnv(t, func(home, repo string) {
		write(t, config.UserConfigPath(home), `{"cache":{"shared_ttl":"5m"},"unknownkey":1}`)
		write(t, config.LocalConfigPath(repo), `{"cache":{"shared_ttl":"1h"}}`)
		write(t, config.ProjectConfigPath(repo), `{"permissions":{"mode":"yolo"}}`)
	})
	var v configView
	e.ok("GET", "/api/sessions/t1/config", nil, nil, &v)
	if !v.OK || !strings.HasPrefix(v.Valid, "configuration is valid, with 1 warning") || len(v.Issues) != 1 || v.Issues[0].Line != 1 || v.Issues[0].File != "~/.sleipnir/config.json" {
		t.Errorf("warnings: %q %+v", v.Valid, v.Issues)
	}
	if len(v.Risks) == 0 || !strings.Contains(v.Risks[0].Message, "not trusted") {
		t.Errorf("the project's mode left out: %+v", v.Risks)
	}
	kinds := []string{}
	for _, l := range v.Layers {
		kinds = append(kinds, l.Kind+":"+l.State)
	}
	if !slices.Equal(kinds, []string{"defaults:active", "user:found", "project:found", "local:found"}) {
		t.Errorf("layers %v", kinds)
	}
	by := map[string]wire.ConfigValue{}
	for _, cv := range v.Effective {
		by[cv.Key] = cv
	}
	if cv := by["cache.shared_ttl"]; cv.Value != "1h" || cv.Layer != "local" || cv.File != ".sleipnir/config.local.json" || cv.Below["user"] != "5m" {
		t.Errorf("cache.shared_ttl: %+v", cv)
	}
	if cv := by["tools.max_output_chars"]; cv.Layer != "defaults" || cv.File != "built-in" {
		t.Errorf("a default: %+v", cv)
	}
	if cv, ok := by["unknownkey"]; !ok || cv.Value != config.Hidden {
		t.Errorf("an unknown key is withheld: %+v", cv)
	}
	write(t, config.ProjectConfigPath(e.repo), `{"permissions":`)
	v = configView{}
	e.ok("GET", "/api/sessions/t1/config", nil, nil, &v)
	if v.OK || !strings.HasPrefix(v.Valid, "configuration is not valid") || strings.Contains(v.Valid, e.repo) {
		t.Errorf("a broken file: %q", v.Valid)
	}
}
