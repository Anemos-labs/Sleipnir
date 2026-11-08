package swarm

import (
	"os"
	"path/filepath"
	"strings"
)

// In a shared tree (no worktree isolation) nothing commits on its own: a worker's accepted change stays in the working tree,
// uncommitted, and its task done commit= message was thrown away. A team run accepted seven tasks and left the tree dirty at the
// starting commit, so the reviewer reviewed uncommitted files and nobody knew which change was whose. The submission's message is
// kept, and the manager's accept answer says to commit the change with it, when the work lies in a git repository.

// maxSharedCommits bounds the messages kept: one per task in review, dropped when the task is accepted.
const maxSharedCommits = 256

// noteSharedCommit keeps msg, the commit message of t's submission in a shared tree, for the manager's accept. At the bound, the
// messages of abandoned submissions are dropped first: a submission that was never accepted leaves its message behind, and those
// must not crowd out the current ones. A message is dropped only when its task is gone, todo, blocked, failed or done: a doing
// task may be submitting now (its message is kept before it enters review), and a done task's message was read before the accept
// (sharedCommitMessage), so neither loses a current message. A doing task's stale message is replaced by its next submission.
func (s *Swarm) noteSharedCommit(t Task, msg string) {
	if strings.TrimSpace(msg) == "" {
		return
	}
	s.sharedCommitAt("cleanup")
	s.mu.Lock()
	defer s.mu.Unlock()
	// The snapshot is read under the lock that every message is written under, so each kept message's task is seen at least as
	// new as when its message was written: a task claimed and submitted after a snapshot read before the lock would look todo.
	sn := s.Board.Snapshot()
	if s.sharedCommits == nil {
		s.sharedCommits = map[string]string{}
	}
	if _, ok := s.sharedCommits[t.ID]; !ok && len(s.sharedCommits) >= maxSharedCommits {
		for id := range s.sharedCommits {
			if cur, ok := sn.Task(id); !ok || cur.Status != StatusReview && cur.Status != StatusDoing {
				delete(s.sharedCommits, id)
			}
		}
		if len(s.sharedCommits) >= maxSharedCommits {
			return
		}
	}
	s.sharedCommits[t.ID] = msg
}

// sharedCommitAt runs the test hook for step (Swarm.sharedCommitStep).
func (s *Swarm) sharedCommitAt(step string) {
	if f := s.sharedCommitStep; f != nil {
		f(step)
	}
}

// sharedCommitMessage is the kept commit message of t's submission, read while t is still in review (before the accept), so that
// no cleanup at the bound can take it in between.
func (s *Swarm) sharedCommitMessage(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sharedCommits[id]
}

// sharedCommitHint is what the manager's accept of t adds in a shared tree whose work lies in a git repository: commit the change
// now, with msg, the message its worker wrote (sharedCommitMessage; one made from the task when ""), naming the repository and
// the task's scope. The kept message is dropped. "" when the project is in no git repository, or t changed nothing (a plan, a
// report, a task whose evidence shows no file edited and was accepted as needing no change).
func (s *Swarm) sharedCommitHint(t Task, msg string) string {
	s.mu.Lock()
	delete(s.sharedCommits, t.ID)
	s.mu.Unlock()
	if t.Kind == TaskKindPlan || s.roles[s.roleOf(t.Owner)].Reports() || evidenceShowsNoEdits(t.Evidence) {
		return ""
	}
	repo := s.gitRepoFor(t.Files)
	if repo == "" {
		return ""
	}
	if msg == "" {
		msg = taskCommitMessage(t, "", "", s.everyMember())
	}
	subject, _, _ := strings.Cut(msg, "\n")
	where := "its files"
	if len(t.Files) > 0 {
		where = "the files of its scope (" + strings.Join(shownScopes(t.Files), ", ") + ")"
	}
	return ". Nothing in a shared tree is committed for you: commit this change now in " + s.repoName(repo) + ", " + where +
		" only, so each accepted task is one commit, with the subject " + quoteLine(subject)
}

// repoName names repo for the manager, whose commands run at the project root: "the project root", its path relative to the root,
// or (above the root) its absolute path, whole: a cut path would name no directory.
func (s *Swarm) repoName(repo string) string {
	root := s.deps.Root
	if root == "" {
		root = s.deps.Workdir
	}
	if rel, err := filepath.Rel(root, repo); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		if rel == "." {
			return "the project root"
		}
		return filepath.ToSlash(rel) + " (below the project root)"
	}
	return repo
}

// quoteLine quotes one line of text for an answer.
func quoteLine(s string) string { return "\"" + cleanText(s, 120) + "\"" }

// gitRepoFor is the git repository that holds the work of scopes: the nearest directory with a .git entry at or above the literal
// directory of the first scope that names one (or the project root), "" when there is none.
func (s *Swarm) gitRepoFor(scopes []string) string {
	root := s.deps.Root
	if root == "" {
		root = s.deps.Workdir
	}
	if root == "" {
		return ""
	}
	start := root
	for _, sc := range scopes {
		if d := docsDirOf(sc); d != "" && d != "." {
			start = filepath.Join(root, filepath.FromSlash(d))
			break
		}
	}
	for d := filepath.Clean(start); ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}
