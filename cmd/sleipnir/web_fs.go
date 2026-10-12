package main

// The folder of a new session. The New session dialog takes any path a person types and completes the folder names as they type
// (GET /api/fs/dirs, internal/web/dirs); a folder that is not one of the server's projects is allowed, and what starting there asks
// of the server (making the folder, a git repository in it) is decided before anything is done (planDir), shown with the other
// privileges the request raises (web_authorize.go), and done only once the request is confirmed (dirPlan.apply).

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unicode"

	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/dirs"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// fsSlots bounds the folder listings in flight: a page that asks on every key press cannot pile up reads of a slow disk.
var fsSlots = make(chan struct{}, 4)

// fsTimeout is how long a listing may take (a network mount that does not answer must not hold the request).
const fsTimeout = 4 * time.Second

// repoProbeTimeout is how long git may take to say what a repository is (git status of a large checkout can be slow): past it the
// listing goes without, and the server's check when the session starts says what is wrong.
const repoProbeTimeout = 2 * time.Second

// firstCommit is the message of the empty commit that starts a repository the server makes.
const firstCommit = "Start of the project"

// fsDirs handles GET /api/fs/dirs?path=: the subfolders that complete the path typed, and what the path is.
func (h *webHostImpl) fsDirs(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	opts := dirs.Options{Home: home, Base: h.d.Cwd}
	select {
	case fsSlots <- struct{}{}:
	default:
		writeErr(w, werr(http.StatusTooManyRequests, "busy", "folders are being listed: try again"))
		return
	}
	type result struct {
		l   wire.DirListing
		err error
	}
	done := make(chan result, 1)
	text := r.URL.Query().Get("path")
	go func() {
		defer func() { <-fsSlots }()
		l, err := dirs.Complete(text, opts)
		if err == nil && (l.State.Git == "repo" || l.State.Git == "bare") {
			// what the New session dialog needs to know of a repository before it offers worktree isolation and --commit
			ctx, cancel := context.WithTimeout(r.Context(), repoProbeTimeout)
			if p := session.ProbeRepo(ctx, l.State.Path); p.Probed {
				l.State.Repo = &wire.RepoState{Bare: p.Bare, HasCommits: p.HasCommits, Branch: p.Branch, Detached: p.Detached, Clean: p.Clean}
			}
			cancel()
		}
		done <- result{l, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			code := "bad_path"
			if errors.Is(res.err, dirs.ErrEmpty) {
				code = "bad_request"
			}
			writeErr(w, werr(http.StatusBadRequest, code, res.err.Error()))
			return
		}
		res.l.State.Known = h.knownDir(r.Context(), res.l.State.Path)
		ok(w, res.l)
	case <-time.After(fsTimeout):
		writeErr(w, werr(http.StatusGatewayTimeout, "slow", "that folder did not answer in time"))
	case <-r.Context().Done():
	}
}

// dirPlan is what starting a session in a folder asks of the server, decided before anything is done.
type dirPlan struct {
	unlisted bool // the folder is not one of the server's projects
	create   bool // the folder does not exist: make it
	git      bool // make the folder a git repository, with an empty first commit
}

// planDir decides what a session in dir needs: that the folder exists (or that it may be made, when create says so and its parent is
// a folder), and, when git asks for a repository, that the folder is empty (or made now) and outside any repository. A folder that is
// a repository already needs nothing. Nothing is changed.
func planDir(dir string, unlisted, create, git bool) (dirPlan, error) {
	pl := dirPlan{unlisted: unlisted}
	st := dirs.Inspect(dir)
	switch {
	case st.Inaccessible:
		return pl, werr(http.StatusForbidden, "unreadable", "the server cannot look at that path")
	case st.Exists && !st.Dir:
		return pl, werr(http.StatusBadRequest, "not_a_dir", "that is a file, not a folder")
	case !st.Exists && !st.ParentOK:
		return pl, werr(http.StatusNotFound, "no_dir", "that folder does not exist, and neither does the folder it would be made in")
	case !st.Exists && !create:
		return pl, werr(http.StatusNotFound, "no_dir", "that folder does not exist: the dialog can make it")
	case !st.Exists:
		if err := plainName(filepath.Base(dir)); err != nil {
			return pl, err
		}
		pl.create = true
	case !st.Readable:
		return pl, werr(http.StatusForbidden, "unreadable", "that folder cannot be read by the server")
	}
	if git {
		switch {
		case st.Dir && st.Git == "repo":
		case st.Git == "inside":
			return pl, werr(http.StatusConflict, "git_init", "that folder is inside a git repository already")
		case st.Dir && !st.Empty:
			return pl, werr(http.StatusConflict, "git_init", "a git repository is made in an empty folder: that one has files")
		default:
			pl.git = true
		}
	}
	return pl, nil
}

// plainName refuses the name of a folder to make that holds a control character: it would be shown as an escape and made as typed.
func plainName(name string) error {
	for _, r := range name {
		if unicode.IsControl(r) {
			return werr(http.StatusBadRequest, "bad_path", "the name of a new folder holds a control character")
		}
	}
	return nil
}

// apply does what the plan decided, once the request that asked for it is confirmed: makes the folder (the last part of the path
// only), then the repository and its first commit.
func (pl dirPlan) apply(ctx context.Context, dir string) error {
	if pl.create {
		// a folder that is there by now (another request, another process) is not the one that was confirmed: nothing of it is used
		if err := os.Mkdir(dir, 0o755); err != nil {
			return werr(http.StatusConflict, "mkdir", "the folder could not be made: "+clip(err.Error(), 300))
		}
	}
	if pl.git {
		repo, err := gitx.Init(ctx, dir, false)
		if err == nil {
			_, err = repo.CommitEmpty(ctx, firstCommit, gitx.DefaultAuthor)
		}
		if err != nil {
			return werr(http.StatusConflict, "git_init", "a git repository could not be made there: "+clip(err.Error(), 300))
		}
	}
	return nil
}

// existingDir accepts dir, an absolute folder, for a session to start or restart in: one of the projects (named as the server names
// it) or any other folder that exists, which the caller raises as a privilege (unlisted).
func (h *webHostImpl) existingDir(ctx context.Context, dir string) (path string, unlisted bool, err error) {
	if proj, ok := h.isProject(ctx, dir); ok {
		return proj.Dir, false, nil
	}
	pl, err := planDir(dir, true, false, false)
	return dir, pl.unlisted, err
}
