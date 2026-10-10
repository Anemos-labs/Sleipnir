package session_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/trust"
)

func TestSettingsInfoIsASnapshotOfTheSession(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, nil)
	o := opts(t, repo, client, model)
	o.Allow = []string{"Bash(go test:*)"}
	o.NoMCP = true
	o.Mode = perm.ModeAcceptEdits
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info := s.SettingsInfo()
	if info.Root != repo || info.Cwd != repo || info.Home != o.Home || !info.TrustProject || info.TrustHow != "flag" || !info.NoMCP {
		t.Errorf("info: %+v", info)
	}
	if info.Mode != perm.ModeAcceptEdits || !slices.Equal(info.FlagAllow, o.Allow) || info.Config == nil || info.Report == nil {
		t.Errorf("mode %q allow %v config %v", info.Mode, info.FlagAllow, info.Config != nil)
	}
	info.FlagAllow[0] = "changed"
	if s.SettingsInfo().FlagAllow[0] != "Bash(go test:*)" {
		t.Error("the snapshot shares the session's rules")
	}
	rules := session.ProtectedConfigRules()
	if !slices.Contains(rules, "Edit(./.sleipnir/**)") {
		t.Errorf("protected rules %v", rules)
	}
	rules[0] = "changed"
	if session.ProtectedConfigRules()[0] == "changed" {
		t.Error("ProtectedConfigRules returns the session's own slice")
	}
}

func TestTrustNowSaysWhatTrustListSays(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", "")
	repo := newRepo(t)
	writeFile(t, filepath.Join(repo, "AGENTS.md"), "be careful\n")
	ledger := trust.OpenLedger(session.TrustLedgerPath(home))
	fp, err := session.ProjectFootprint(home, repo)
	if err != nil || fp.Empty() {
		t.Fatalf("footprint: %v %v", fp, err)
	}
	if err := ledger.Remember(repo, fp, time.Now()); err != nil {
		t.Fatal(err)
	}
	e := ledger.All()[repo]
	if now, st := session.TrustNow(ledger, home, repo, e); now != "unchanged" || st != session.TrustUnchanged {
		t.Errorf("unchanged: %q %q", now, st)
	}
	writeFile(t, filepath.Join(repo, "AGENTS.md"), "be careless\n")
	if now, st := session.TrustNow(ledger, home, repo, e); !strings.HasPrefix(now, "changed: AGENTS.md changed") || st != session.TrustChanged {
		t.Errorf("changed: %q %q", now, st)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	if now, st := session.TrustNow(ledger, home, gone, e); now != "directory is gone" || st != session.TrustGone {
		t.Errorf("gone: %q %q", now, st)
	}
	sub := filepath.Join(repo, "server")
	fp2, _ := session.ProjectFootprint(home, sub)
	if fp2 == nil || fp2.Root != repo {
		t.Errorf("a subdirectory's footprint is its project's: %+v", fp2)
	}
}

func TestMCPTestStartsServersWithoutASession(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, "state")
	t.Setenv("SLEIPNIR_HOME", state)
	repo := newRepo(t)
	writeConfig(t, config.UserConfigPath(home), map[string]any{"mcp": map[string]any{"ref": refServer("")}})
	writeConfig(t, config.ProjectConfigPath(repo), map[string]any{"mcp": map[string]any{"proj": refServer("p")}})

	res, err := session.MCPTest(context.Background(), home, repo, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	ready := map[string]bool{}
	for _, st := range res.Servers {
		ready[st.Name] = st.State == "ready"
	}
	if !ready["ref"] || ready["proj"] || !slices.Contains(res.Refused, "proj") {
		t.Errorf("the user's server starts, an unapproved project entry does not: %+v refused %v", res.Servers, res.Refused)
	}
	if !slices.Contains(res.Tools, "mcp__ref__echo") || !slices.IsSorted(res.Tools) {
		t.Errorf("tools %v", res.Tools)
	}
	if _, err := os.Stat(filepath.Join(state, "sessions")); !os.IsNotExist(err) {
		t.Errorf("a test of the servers created a session directory (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(state, "mcp-approvals.json")); !os.IsNotExist(err) {
		t.Error("a test of the servers wrote an approval")
	}

	entry, err := session.ProjectMCPEntry(home, repo, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.OpenMCPApprovals(home).Approve(repo, entry.Fingerprint, "proj"); err != nil {
		t.Fatal(err)
	}
	res, err = session.MCPTest(context.Background(), home, repo, []string{"proj"}, true)
	if err != nil || len(res.Servers) != 1 || res.Servers[0].State != "ready" || len(res.Refused) != 0 {
		t.Fatalf("an approved project entry starts: %+v %v", res, err)
	}
	if _, err := session.MCPTest(context.Background(), home, repo, []string{"nope"}, true); err == nil {
		t.Error("an unknown server name is an error")
	}
	if _, err := session.ProjectMCPEntry(home, repo, "ref"); err == nil || !strings.Contains(err.Error(), "your own entry") {
		t.Errorf("the user's own entry needs no approval: %v", err)
	}
	if _, err := session.ProjectMCPEntry(home, repo, "nope"); err == nil || !strings.Contains(err.Error(), "no project MCP server") {
		t.Errorf("an unknown entry: %v", err)
	}
}
