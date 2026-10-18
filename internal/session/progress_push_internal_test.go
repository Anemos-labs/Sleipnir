package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A failing push is said once and retried after the next merge; a recovery is said; pushes
// are coalesced to the newest tip and never run two at a time.
func TestProgressPusherReportsFailureOnceAndRetries(t *testing.T) {
	var mu sync.Mutex
	var pushed []string
	fail := true
	release := make(chan struct{}, 10)
	entered := make(chan struct{}, 10)
	git := func(ctx context.Context, args ...string) ([]byte, error) {
		entered <- struct{}{}
		<-release
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return nil, errors.New("git push: exit status 128: could not read Username")
		}
		pushed = append(pushed, args[len(args)-1])
		return nil, nil
	}
	var told []string
	var tmu sync.Mutex
	p := &progressPusher{git: git, remote: "origin", ref: "sleipnir/s/_integration", force: true, tell: func(level, msg string) {
		tmu.Lock()
		told = append(told, level+": "+msg)
		tmu.Unlock()
	}}
	wait := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Wait(ctx)
	}

	release <- struct{}{}
	p.Kick("aaa")
	<-entered
	wait()
	release <- struct{}{}
	p.Kick("bbb") // still failing: not said again
	<-entered
	wait()
	if len(told) != 1 || !strings.HasPrefix(told[0], "warn: Could not push") {
		t.Fatalf("told %q", told)
	}

	mu.Lock()
	fail = false
	mu.Unlock()
	p.Kick("ccc") // blocks in git until released
	<-entered
	p.Kick("ddd")
	p.Kick("eee") // coalesced with ddd
	release <- struct{}{}
	release <- struct{}{}
	wait()
	if got := strings.Join(pushed, ","); got != "+ccc:refs/heads/sleipnir/s/_integration,+eee:refs/heads/sleipnir/s/_integration" {
		t.Fatalf("pushed %s", got)
	}
	if len(told) != 2 || !strings.Contains(told[1], "pushed to origin branch sleipnir/s/_integration") {
		t.Fatalf("told %q", told)
	}
	p.Kick("eee") // already there: no push
	wait()
	if len(pushed) != 2 {
		t.Fatalf("pushed again: %v", pushed)
	}
}

// A branch whose push remote is "." (the repository itself) falls back to origin.
func TestPushRemoteIgnoresTheRepositoryItself(t *testing.T) {
	git := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "for-each-ref" {
			return []byte(".\n"), nil
		}
		return nil, nil
	}
	if got := pushRemote(context.Background(), git, "main"); got != "origin" {
		t.Fatalf("remote %q, want origin", got)
	}
}

// With a folder, the integration commits land in that folder of the target branch: one
// commit each, on top of the repository's default branch at first and of the branch
// itself after, keeping what others pushed there, never adding a commit that changes
// nothing, and resuming from the Sleipnir-Source trailer.
func TestProgressPusherRewritesIntoAFolder(t *testing.T) {
	run := func(dir string, args ...string) string {
		t.Helper()
		out, err := commandGitEnv(dir, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")(context.Background(), args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	tmp := t.TempDir()
	remote, other, repo := tmp+"/remote.git", tmp+"/other", tmp+"/repo"
	run(tmp, "init", "-q", "--bare", "-b", "main", remote)
	run(tmp, "init", "-q", "-b", "main", other)
	putFile(t, other+"/README.md", "monorepo\n")
	putFile(t, other+"/web/app/stale.txt", "old\n")
	run(other, "add", ".")
	run(other, "commit", "-q", "-m", "monorepo")
	run(other, "push", "-q", remote, "main")

	run(tmp, "init", "-q", "-b", "main", repo)
	putFile(t, repo+"/go.mod", "module app\n")
	run(repo, "add", ".")
	run(repo, "commit", "-q", "-m", "base")
	base := run(repo, "rev-parse", "HEAD")
	putFile(t, repo+"/main.go", "package main\n")
	run(repo, "add", ".")
	run(repo, "commit", "-q", "-m", "feat: main")
	c1 := run(repo, "rev-parse", "HEAD")
	run(repo, "commit", "-q", "--allow-empty", "-m", "chore: nothing")
	putFile(t, repo+"/b.go", "package main\n")
	run(repo, "add", ".")
	run(repo, "commit", "-q", "-m", "feat: b")
	c3 := run(repo, "rev-parse", "HEAD")

	p := &progressPusher{git: commandGit(repo), remote: remote, ref: "build/app", dir: "web/app", root: repo}
	p.SetBase(base)
	if err := p.push(c1); err != nil {
		t.Fatal(err)
	}
	files := func() string {
		return strings.Join(strings.Fields(run(remote, "ls-tree", "-r", "--name-only", "build/app")), ",")
	}
	if got := files(); got != "README.md,web/app/go.mod,web/app/main.go" {
		t.Fatalf("after c1: %s", got)
	}
	if got := run(remote, "rev-parse", "build/app^"); got != run(remote, "rev-parse", "main") {
		t.Fatalf("the first commit is not on main: parent %s", got)
	}
	if msg := run(remote, "log", "-1", "--format=%B", "build/app"); !strings.HasPrefix(msg, "feat: main") || !strings.Contains(msg, "Sleipnir-Source: "+c1) {
		t.Fatalf("message %q", msg)
	}

	// Someone else adds to the branch; the next push keeps it.
	run(other, "fetch", "-q", remote, "build/app")
	run(other, "checkout", "-q", "FETCH_HEAD")
	putFile(t, other+"/NOTES.md", "x\n")
	run(other, "add", ".")
	run(other, "commit", "-q", "-m", "notes")
	run(other, "push", "-q", remote, "HEAD:refs/heads/build/app")

	if err := p.push(c3); err != nil {
		t.Fatal(err)
	}
	if got := files(); got != "NOTES.md,README.md,web/app/b.go,web/app/go.mod,web/app/main.go" {
		t.Fatalf("after c3: %s", got)
	}
	if n := run(remote, "rev-list", "--count", "main..build/app"); n != "3" { // feat: main, notes, feat: b (the empty commit adds nothing)
		t.Fatalf("%s commits on the branch", n)
	}
	head := run(remote, "rev-parse", "build/app")

	// A new pusher (a resumed session) with the same tip pushes nothing.
	again := &progressPusher{git: commandGit(repo), remote: remote, ref: "build/app", dir: "web/app", root: repo}
	again.SetBase(base)
	if err := again.push(c3); err != nil {
		t.Fatal(err)
	}
	if got := run(remote, "rev-parse", "build/app"); got != head {
		t.Fatalf("a repeated push moved the branch to %s", got)
	}
}

// putFile writes body at path, making its folders.
func putFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
