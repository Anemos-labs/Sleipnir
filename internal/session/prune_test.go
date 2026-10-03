package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

func TestStoredSessionPruneExcludesLiveRunsAndSalvagesWorkerEdits(t *testing.T) {
	ctx := context.Background()
	repo := isoRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "unused"} })
	o := isoOptions(t, repo, client, model, "prune-recovery")
	s, err := session.New(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.RemoveStoredSession(ctx, s.Dir); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("live session: %v", err)
	}
	gitRepo, err := gitx.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	m := &workspace.Manager{Repo: gitRepo, Dir: treesOf(o, s.ID), Prefix: "sleipnir/" + s.ID}
	w, err := m.Create(ctx, "worker", workspace.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "unsaved.txt"), []byte("preserve unique work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := session.RemoveStoredSession(ctx, s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if report == nil || len(report.BranchesKept) == 0 {
		t.Fatalf("salvaged branch not reported: %+v", report)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Fatalf("session was not removed: %v", err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatalf("cache tree was not removed: %v", err)
	}
	if got := git(t, repo, "show", w.Branch+":unsaved.txt"); got != "preserve unique work" {
		t.Fatalf("salvaged work: %q", got)
	}
	if _, err := gitRepo.BranchSHA(ctx, "sleipnir/"+s.ID+"/_resume"); err == nil {
		t.Fatal("obsolete recovery pin remains")
	}
}
