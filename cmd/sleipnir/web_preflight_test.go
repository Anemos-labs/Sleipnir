package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// A new session whose isolation settings cannot start is refused before a tab opens (409 isolation, with the session's own reason),
// and before the confirmation of an unknown folder is asked for; a combination that can start goes on to that confirmation.
func TestWebNewSessionRefusesIsolationThatCannotStart(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	plain := t.TempDir()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "start"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	team, zero := 8, 0
	tabs := func() int { return len(r.h.Tabs()) }
	before := tabs()
	for _, tc := range []struct {
		name string
		req  wire.NewSessionRequest
		want string
	}{
		{"worktree outside a repository", wire.NewSessionRequest{Cwd: plain, Swarm: &team, Isolation: "worktree"}, "needs a git repository"},
		{"worktree for a single agent", wire.NewSessionRequest{Cwd: repo, Swarm: &zero, Isolation: "worktree"}, "isolation applies to swarm sessions"},
		{"commit without worktree", wire.NewSessionRequest{Cwd: repo, Swarm: &team, Isolation: "none", Commit: true}, "commit needs worktree isolation"},
		{"commit on a checkout with changes", wire.NewSessionRequest{Cwd: repo, Swarm: &team, Isolation: "worktree", Commit: true}, "clean checkout"},
	} {
		code, body := r.do("POST", "/api/sessions", tc.req)
		var e struct{ Code, Error string }
		_ = json.Unmarshal(body, &e)
		if code != 409 || e.Code != "isolation" || !strings.Contains(e.Error, tc.want) {
			t.Errorf("%s: %d %s", tc.name, code, body)
		}
	}
	// the folder probe says what the dialog needs of a repository; a folder that is none has nothing to say
	if st := r.listing(repo).State; st.Git != "repo" || st.Repo == nil || !st.Repo.HasCommits || st.Repo.Clean || st.Repo.Detached || st.Repo.Branch == "" {
		t.Errorf("the probe of a repository with a change: %+v %+v", st, st.Repo)
	}
	if st := r.listing(plain).State; st.Repo != nil {
		t.Errorf("the probe of a plain folder: %+v", st.Repo)
	}
	if n := tabs(); n != before {
		t.Errorf("a refused session opened a tab: %d tabs, %d before", n, before)
	}
	// what can start goes on to the confirmation of a folder this server does not know
	if code, body := r.do("POST", "/api/sessions", wire.NewSessionRequest{Cwd: repo, Swarm: &team, Isolation: "worktree"}); code != 428 {
		t.Errorf("worktree in a repository's root: %d %s", code, body)
	}
	empty := t.TempDir()
	if code, body := r.do("POST", "/api/sessions", wire.NewSessionRequest{Cwd: empty, Swarm: &team, Isolation: "worktree", Commit: true, GitInit: true}); code != 428 {
		t.Errorf("a folder that git init makes a repository: %d %s", code, body)
	}
}
