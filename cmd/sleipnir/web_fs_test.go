package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// mkdirs makes the folders under parent and returns the first.
func mkdirs(t *testing.T, parent string, names ...string) string {
	t.Helper()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(parent, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(parent, names[0])
}

// listing asks the folder route for a path.
func (r *webRig) listing(path string) wire.DirListing {
	r.t.Helper()
	var l wire.DirListing
	r.json("GET", "/api/fs/dirs?path="+url.QueryEscape(path), nil, 200, &l)
	return l
}

// reasonsOf are the lines a refusal for want of a confirmation says it raises.
func reasonsOf(t *testing.T, body []byte) []string {
	t.Helper()
	var e struct{ Detail struct{ Reasons []string } }
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return e.Detail.Reasons
}

// The folder route completes the path being typed with the subfolders that start with it, reads ~ as the home folder and a relative
// path from the folder the server was started in, says what the path is (a project of the server, a repository, a folder to make),
// and refuses what is not a path.
func TestFolderRouteCompletesPathsAndSaysWhatTheyAre(t *testing.T) {
	project := webWorld(t)
	root := filepath.Dir(project)
	mkdirs(t, root, "projects-old/.git", "other/deep", "project/sub")
	if err := os.WriteFile(filepath.Join(root, "project-notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	sep := string(filepath.Separator)

	l := r.listing(filepath.Join(root, "proj"))
	var names []string
	for _, e := range l.Entries {
		names = append(names, e.Name)
		if e.Name == "projects-old" && !e.Repo {
			t.Error("a repository is not marked")
		}
	}
	if strings.Join(names, ",") != "project,projects-old" || l.Dir != root || l.Prefix != "proj" || l.State.Exists {
		t.Errorf("proj: %v in %q (%q), state %+v", names, l.Dir, l.Prefix, l.State)
	}

	if st := r.listing(project).State; !st.Known || !st.Dir || st.Git != "none" || st.Path != project {
		t.Errorf("the project: %+v", st)
	}
	if st := r.listing(filepath.Join(root, "other")).State; st.Known || !st.Dir || st.Empty {
		t.Errorf("a folder of no project: %+v", st)
	}
	if st := r.listing(filepath.Join(root, "projects-old")).State; st.Git != "repo" {
		t.Errorf("a repository: %+v", st)
	}
	if st := r.listing(filepath.Join(root, "other", "new")).State; st.Exists || !st.ParentOK {
		t.Errorf("a folder to make: %+v", st)
	}

	home, _ := os.UserHomeDir()
	if l := r.listing("~" + sep); l.Dir != home || l.Home != home || l.State.Path != home {
		t.Errorf("~: %+v", l)
	}
	if l := r.listing("su"); l.Dir != project || len(l.Entries) != 1 || l.Entries[0].Name != "sub" {
		t.Errorf("a relative path is read from the server's folder: %+v", l)
	}

	for path, want := range map[string]string{"": "bad_request", "   ": "bad_request", "~nobody/x": "bad_path", "a\x00b": "bad_path"} {
		code, body := r.do("GET", "/api/fs/dirs?path="+url.QueryEscape(path), nil)
		if code != 400 || !strings.Contains(string(body), want) {
			t.Errorf("path %q = %d %s, want 400 %s", path, code, body, want)
		}
	}
}

// A session starts in any folder that exists once the person confirms it (the refusal names the folder and starts nothing); the
// folder is one of the server's projects from then on, as long as a session is there.
func TestASessionStartsInAnyFolderAfterAConfirmation(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	zero := 0
	elsewhere := mkdirs(t, filepath.Dir(project), "elsewhere")
	req := wire.NewSessionRequest{Cwd: elsewhere, Swarm: &zero}

	code, body := r.do("POST", "/api/sessions", req)
	if rs := reasonsOf(t, body); code != 428 || len(rs) != 1 || !strings.Contains(rs[0], elsewhere) || !strings.Contains(rs[0], "not one of this server's projects") {
		t.Fatalf("without a confirmation = %d %s", code, body)
	}
	scope := r.needsConfirm("POST", "/api/sessions", req)
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", req, 201, &created, web.ConfirmHeader, r.confirmFor(scope))
	if got := r.ready(created.Tab.ID); got.Cwd != elsewhere {
		t.Errorf("the session runs in %q", got.Cwd)
	}
	r.json("POST", "/api/sessions", req, 201, nil) // a project now: nothing to confirm

	// the confirmation is for this folder: another one needs its own
	other := mkdirs(t, filepath.Dir(project), "another")
	if code, _ := r.do("POST", "/api/sessions", wire.NewSessionRequest{Cwd: other, Swarm: &zero}, web.ConfirmHeader, r.confirmFor(scope)); code != 403 {
		t.Errorf("a confirmation for another folder = %d", code)
	}
}

// A folder that does not exist is made only when the request says so and its parent exists, once confirmed; with git asked for, it
// becomes a repository with a first commit. Until the confirmation nothing is made.
func TestASessionMakesTheFolderAndTheRepositoryWhenAskedAndConfirmed(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	zero := 0
	parent := filepath.Dir(project)
	fresh := filepath.Join(parent, "fresh")

	if code, body := r.do("POST", "/api/sessions", wire.NewSessionRequest{Cwd: fresh, Swarm: &zero}); code != 404 || !strings.Contains(string(body), "no_dir") {
		t.Errorf("a folder that does not exist, not asked to be made = %d %s", code, body)
	}
	if code, body := r.do("POST", "/api/sessions", wire.NewSessionRequest{Cwd: filepath.Join(fresh, "deeper"), CreateDir: true, Swarm: &zero}); code != 404 || !strings.Contains(string(body), "no_dir") {
		t.Errorf("a folder whose parent does not exist = %d %s", code, body)
	}

	// trusting the project's files of a folder to make asks for nothing more: it has none
	req := wire.NewSessionRequest{Cwd: fresh, CreateDir: true, GitInit: true, TrustProject: true, Swarm: &zero}
	code, body := r.do("POST", "/api/sessions", req)
	rs := reasonsOf(t, body)
	if code != 428 || len(rs) != 2 || !strings.Contains(rs[0], "make the folder "+fresh) || !strings.Contains(rs[1], "git repository") {
		t.Fatalf("without a confirmation = %d %s", code, body)
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Fatal("the folder was made before the request was confirmed")
	}
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", req, 201, &created, web.ConfirmHeader, r.confirmFor(r.needsConfirm("POST", "/api/sessions", req)))
	if got := r.ready(created.Tab.ID); got.Cwd != fresh {
		t.Errorf("the session runs in %q", got.Cwd)
	}
	repo, err := gitx.Open(fresh)
	if err != nil {
		t.Fatalf("the folder is not a repository: %v", err)
	}
	if head, err := repo.Head(context.Background()); err != nil || head == "" {
		t.Errorf("the repository has no first commit: %q, %v", head, err)
	}
	if entries, _ := os.ReadDir(fresh); len(entries) != 1 || entries[0].Name() != ".git" {
		t.Errorf("the new folder holds %v, want only .git", entries)
	}

	// an empty folder that exists becomes a repository too; a repository already needs nothing more
	empty := mkdirs(t, parent, "empty")
	e := wire.NewSessionRequest{Cwd: empty, GitInit: true, Swarm: &zero}
	r.json("POST", "/api/sessions", e, 201, nil, web.ConfirmHeader, r.confirmFor(r.needsConfirm("POST", "/api/sessions", e)))
	if _, err := gitx.Open(empty); err != nil {
		t.Errorf("the empty folder is not a repository: %v", err)
	}
	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: fresh, GitInit: true, Swarm: &zero}, 201, nil)
}

// What cannot be started is refused before anything is made: a path that is a file, is not absolute or names a folder with a control
// character, and a repository asked for in a folder that has files or is inside a repository.
func TestAFolderThatCannotHoldTheSessionIsRefusedBeforeAnythingIsMade(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	zero := 0
	parent := filepath.Dir(project)
	full := mkdirs(t, parent, "full")
	if err := os.WriteFile(filepath.Join(full, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inside := mkdirs(t, parent, "repo/.git", "repo/sub")
	_ = inside
	file := filepath.Join(full, "a.txt")

	for name, c := range map[string]struct {
		req  wire.NewSessionRequest
		code int
		word string
	}{
		"a file":                   {wire.NewSessionRequest{Cwd: file}, 400, "not_a_dir"},
		"a relative path":          {wire.NewSessionRequest{Cwd: "full"}, 400, "bad_path"},
		"no path":                  {wire.NewSessionRequest{}, 400, "bad_path"},
		"a repository in files":    {wire.NewSessionRequest{Cwd: full, GitInit: true}, 409, "git_init"},
		"a repository in a repo":   {wire.NewSessionRequest{Cwd: filepath.Join(parent, "repo", "sub"), GitInit: true}, 409, "git_init"},
		"a new folder in a repo":   {wire.NewSessionRequest{Cwd: filepath.Join(parent, "repo", "new"), CreateDir: true, GitInit: true}, 409, "git_init"},
		"a control character made": {wire.NewSessionRequest{Cwd: filepath.Join(parent, "bad\nname"), CreateDir: true}, 400, "bad_path"},
	} {
		c.req.Swarm = &zero
		if code, body := r.do("POST", "/api/sessions", c.req); code != c.code || !strings.Contains(string(body), c.word) {
			t.Errorf("%s = %d %s, want %d %s", name, code, body, c.code, c.word)
		}
	}
	if _, err := os.Stat(filepath.Join(full, ".git")); err == nil {
		t.Error("a repository was made in a folder with files")
	}
	if _, err := os.Stat(filepath.Join(parent, "repo", "new")); err == nil {
		t.Error("a folder was made in a repository that was refused")
	}
}

// A folder that is there by the time the confirmed request makes it (another request, another process) is not the one that was
// confirmed: nothing of it is used, and no repository is made in it. A path the server cannot look at is refused, never made.
func TestAFolderThatAppearedAfterTheConfirmationIsNotUsedAndWhatCannotBeLookedAtIsNotMade(t *testing.T) {
	dir := mkdirs(t, t.TempDir(), "taken")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := dirPlan{create: true, git: true}.apply(context.Background(), dir)
	if we, ok := err.(*wire.Error); !ok || we.Status != 409 || we.Code != "mkdir" {
		t.Fatalf("apply on a folder that exists = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Error("a repository was made in a folder that was not the one confirmed")
	}
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Skipf("no links here: %v", err)
	}
	if _, err := planDir(loop, true, true, true); err == nil {
		t.Error("a path that cannot be looked at was planned")
	} else if we, ok := err.(*wire.Error); !ok || we.Status != 403 || we.Code != "unreadable" {
		t.Errorf("planDir of a link that loops = %v", err)
	}
}
