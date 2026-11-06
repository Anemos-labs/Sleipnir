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
// messages of tasks no longer in review (sent back, failed, closed) are dropped first: a submission that was never accepted
// leaves its message behind, and those must not crowd out the current ones.
func (s *Swarm) noteSharedCommit(t Task, msg string) {
	if strings.TrimSpace(msg) == "" {
		return
	}
	sn := s.Board.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sharedCommits == nil {
		s.sharedCommits = map[string]string{}
	}
	if _, ok := s.sharedCommits[t.ID]; !ok && len(s.sharedCommits) >= maxSharedCommits {
		for id := range s.sharedCommits {
			if cur, ok := sn.Task(id); !ok || cur.Status != StatusReview {
				delete(s.sharedCommits, id)
			}
		}
		if len(s.sharedCommits) >= maxSharedCommits {
			return
		}
	}
	s.sharedCommits[t.ID] = msg
}

// sharedCommitHint is what the manager's accept of t adds in a shared tree whose work lies in a git repository: commit the change
// now, with the message its worker wrote (or one made from the task), naming the repository and the task's scope. "" when the
// project is in no git repository, or t changed nothing (a plan, a report).
func (s *Swarm) sharedCommitHint(t Task) string {
	s.mu.Lock()
	msg := s.sharedCommits[t.ID]
	delete(s.sharedCommits, t.ID)
	s.mu.Unlock()
	if t.Kind == TaskKindPlan || s.roles[s.roleOf(t.Owner)].Reports() {
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
