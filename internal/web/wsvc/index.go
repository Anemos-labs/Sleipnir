package wsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// handleIndex answers GET /api/sessions/{id}/ws/index: the checkpoints with their change
// sets, the tree at the live edge, the latest restore, the reverts and the reviewed marks.
// The ETag is the index's version; a request with the same If-None-Match gets 304.
func (s *service) handleIndex(w http.ResponseWriter, r *http.Request) {
	acc, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	release, err := s.acquire(r.Context())
	if err != nil {
		return
	}
	defer release()
	idx := s.buildIndex(r.Context(), acc, sess)
	etag := `"` + idx.Version + `"`
	w.Header().Set("ETag", etag)
	if m := r.Header.Get("If-None-Match"); m != "" && strings.Contains(m, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_ = web.WriteJSON(w, http.StatusOK, idx)
}

// clock formats a checkpoint's time of day as the page shows it.
func clock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("15:04:05")
}

// buildIndex assembles the index of a tab's session.
func (s *service) buildIndex(ctx context.Context, acc seam.SessionAccess, sess *session.Session) wire.WsIndex {
	store := sess.Ckpt
	logs := s.logOf(sess.Dir)
	logs.refresh()
	root := sess.Root()
	list := store.List()

	idx := wire.WsIndex{Root: root, Isolation: "none", Base: wire.WsBase{ID: "base", Label: "before the session"}, Cps: []wire.WsCheckpoint{}, Tree: []wire.WsFile{}}
	if sess.WorktreeIsolation() {
		idx.Isolation = "worktree"
	}
	if len(list) > 0 {
		idx.Base.Time = clock(list[0].Time)
	}

	// who wrote what, and on which task
	writerTask := func(agent string, at time.Time) string {
		if agent == "" || agent == checkpoint.PersonAgent {
			return ""
		}
		return logs.taskAt(agent, at)
	}
	touched := map[string]bool{}
	for _, info := range list {
		cp := wire.WsCheckpoint{ID: uiID(info.ID), Time: clock(info.Time), At: info.Time.UnixMilli(), Label: info.Label,
			Skipped: len(info.Files) == 0, Files: info.Files, Agents: info.Agents, Tasks: []string{}, Added: info.Added,
			Removed: info.Removed, NFiles: len(info.Files), Unsaved: info.Unsaved, Changes: []wire.WsChange{}}
		for _, f := range info.Files {
			touched[f] = true
		}
		if len(info.Files) > 0 {
			changes, _ := store.Changes(info.ID)
			for _, c := range changes {
				wc := wire.WsChange{Path: c.Path, Status: c.Status, Added: c.Added, Removed: c.Removed, Agents: nonNil(c.Agents), Binary: c.Binary}
				if a, at := lastWriteIn(store, c.Path, info.ID); a != "" {
					wc.Task = writerTask(a, at)
				}
				if wc.Task != "" && !contains(cp.Tasks, wc.Task) {
					cp.Tasks = append(cp.Tasks, wc.Task)
				}
				cp.Changes = append(cp.Changes, wc)
			}
		}
		idx.Cps = append(idx.Cps, cp)
	}
	if rec := s.latestRestore(sess); rec != nil {
		idx.Restore = &wire.WsRestore{To: rec.to, Files: rec.files, At: rec.at, Safety: rec.safety}
		idx.Cps = append(idx.Cps, wire.WsCheckpoint{ID: rec.safety, Time: clock(rec.when), At: rec.when.UnixMilli(),
			Label: "before restoring " + rec.to, Safety: true, Files: rec.files, Agents: []string{}, Tasks: []string{},
			NFiles: len(rec.files), Changes: []wire.WsChange{}})
		sort.SliceStable(idx.Cps, func(i, j int) bool { return idx.Cps[i].At < idx.Cps[j].At })
	}
	idx.Reverted = s.revertsOf(sess)
	if m, err := session.LoadMeta(sess.Dir); err == nil && len(m.Reviewed) > 0 {
		idx.Reviewed = m.Reviewed
	}

	// the tree: what exists now and what existed at any checkpoint
	files, _ := listFiles(ctx, root)
	seen := map[string]bool{}
	var all []string
	for _, f := range files {
		if !seen[f] {
			seen[f] = true
			all = append(all, f)
		}
	}
	for f := range touched {
		if !seen[f] && !filepath.IsAbs(filepath.FromSlash(f)) && len(all) < maxTree {
			seen[f] = true
			all = append(all, f)
		}
	}
	sort.Strings(all)
	vsBase := map[string]checkpoint.FileDiff{}
	if len(list) > 0 {
		if diffs, err := store.Diff(list[0].ID); err == nil {
			for _, d := range diffs {
				vsBase[d.Path] = d
			}
		}
	}
	landed := s.landedOwners(sess)
	for _, f := range all {
		if hiddenPath(sess, f) {
			continue
		}
		kind, size, exists := s.fileKind(root, f)
		row := wire.WsFile{Path: f, Dir: dirOf(f), Name: path.Base(f), Kind: kind, Status: "-", Size: size, Exists: exists}
		if d, ok := vsBase[f]; ok {
			switch d.Status {
			case checkpoint.DiffAdded:
				row.Status = "A"
			case checkpoint.DiffDeleted:
				row.Status = "D"
			default:
				row.Status = "M"
			}
			row.Add, row.Del = d.Added, d.Removed
			if d.Binary {
				row.Kind = "other"
			}
		}
		if !exists && row.Status == "-" {
			continue // touched once, back to nothing: not part of the project at any point shown
		}
		if l, ok := landed[f]; ok {
			// an isolated team's file: the agent whose merge last brought it
			row.Owner, row.Task = l.agent, l.task
			if a, cp, ok := store.LastWriter(f); ok && a == integrationAgent {
				row.Cp = uiID(cp)
			}
		}
		if a, cp, ok := store.LastWriter(f); ok && (row.Owner == "" || a != integrationAgent) {
			row.Owner, row.Cp = a, uiID(cp)
			_, at := lastWriteIn(store, f, cp)
			row.Task = writerTask(a, at)
		}
		row.Lease = s.leaseOf(sess, f)
		c := s.classOf(sess, f)
		row.Protected, row.Ask = c.protected, c.ask
		idx.Tree = append(idx.Tree, row)
	}
	idx.Version = version(idx)
	return idx
}

// version is a short digest of the index's content.
func version(idx wire.WsIndex) string {
	b, _ := json.Marshal(idx)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// lastWriteIn is the agent and time of the last journaled write of path in a checkpoint.
func lastWriteIn(store *checkpoint.Store, p, cp string) (string, time.Time) {
	var agent string
	var at time.Time
	for _, w := range store.Writes(p) {
		if w.Checkpoint == cp {
			agent, at = w.Agent, w.At
		}
	}
	return agent, at
}

// integrationAgent is the name the checkout's records give the application of an
// isolated team's merged work.
const integrationAgent = "integration"

// owner is who landed a file in an isolated team's integration branch.
type owner struct{ agent, task string }

// landedOwners maps the files of an isolated team's merges to the last agent and task
// that landed them (the merge queue's ledger).
func (s *service) landedOwners(sess *session.Session) map[string]owner {
	out := map[string]owner{}
	q := sess.MergeQueue()
	if q == nil {
		return out
	}
	for _, l := range q.Status().Landed {
		for _, f := range l.Files {
			out[relToRoot(sess, f)] = owner{agent: l.Agent, task: taskID(l.Task)}
		}
	}
	return out
}

// relToRoot turns a repository-relative path of an isolated team (git's view) into a
// project path when the session works in a subdirectory of the repository.
func relToRoot(sess *session.Session, repoRel string) string {
	m := sess.Worktrees()
	if m == nil || m.Repo == nil {
		return repoRel
	}
	sub, err := filepath.Rel(m.Repo.Root(), resolved(sess.Root()))
	if err != nil || sub == "." {
		return repoRel
	}
	return strings.TrimPrefix(repoRel, filepath.ToSlash(sub)+"/")
}

// leaseOf is the write lease or task scope that covers a project path, if any.
func (s *service) leaseOf(sess *session.Session, rel string) *wire.WsLease {
	if sess.Swarm == nil || sess.Swarm.Leases == nil {
		return nil
	}
	l := sess.Swarm.Leases
	if c, ok := l.Covering(rel); ok {
		return &wire.WsLease{Agent: c.Agent, Task: c.Task, Glob: c.Glob}
	}
	if holder, ok := l.Holder(filepath.Join(sess.Root(), filepath.FromSlash(rel))); ok {
		return &wire.WsLease{Agent: holder, Glob: rel}
	}
	return nil
}

// dirOf is the directory part of a slash path ("" at the top).
func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// nonNil gives an empty slice for nil, so the JSON is [] and not null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
