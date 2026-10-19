package workspace

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// With an owner, the commits of the trees and the queue are the person's and the
// agent rides along as a trailer; the message keeps its own trailers in one block.
func TestOwnerAuthorsTheCommits(t *testing.T) {
	for _, noff := range []bool{false, true} {
		e := newQueueEnv(t, QueueOptions{NoFF: noff})
		e.m.Owner = gitx.Author{Name: "Ada Lovelace", Email: "ada@example.com"}
		a := e.agent("fe-1")
		edit(t, a, "a.txt", "a\n")
		r := mustSubmit(t, e.q, Submission{Tree: a, Task: "T1: add a", Message: "Add a\n\nSleipnir-Task: T1"})
		if !r.Merged() {
			t.Fatalf("%+v", r)
		}
		head, _ := a.Head(tctx(t))
		c, _ := e.repo.CommitInfo(tctx(t), head)
		if c.Author.Name != "Ada Lovelace" || c.Author.Email != "ada@example.com" || c.Committer.Email != "ada@example.com" {
			t.Fatalf("NoFF=%v: the agent's commit is not the owner's: %+v", noff, c)
		}
		if c.Subject != "Add a" || c.Body != "Sleipnir-Task: T1\nSleipnir-Agent: fe-1" {
			t.Fatalf("NoFF=%v: message %q / %q", noff, c.Subject, c.Body)
		}
		tip, _ := e.repo.CommitInfo(tctx(t), e.q.Tip())
		if noff && (tip.Author.Email != "ada@example.com" || tip.Subject != `Merge "Add a"` || !strings.Contains(tip.Body, "Sleipnir-Agent: fe-1")) {
			t.Fatalf("merge commit: %+v", tip)
		}
	}
}

func TestUserIdentity(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	repo := openRepo(t, dir)
	rawGit(t, dir, "config", "user.name", "Ada Lovelace")
	rawGit(t, dir, "config", "user.email", "ada@example.com")
	a, ok, err := repo.UserIdentity(tctx(t))
	if err != nil || !ok || a.Name != "Ada Lovelace" || a.Email != "ada@example.com" {
		t.Fatalf("identity %+v %v %v", a, ok, err)
	}
}
