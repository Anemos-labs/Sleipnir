// Package workspace isolates writers from each other and integrates their work
// deliberately.
//
// Public studies of agent-authored pull requests put textual conflicts at 20-42%
// when changes overlap, and merging is only half the damage: two agents that edit
// one working tree also read each other's half-finished files. The defence used
// here is the "hybrid" of docs/SWARM-PROTOCOL.md:
//
//   - Every writer gets a private tree of its own (Manager.Create): a git worktree
//     on its own branch, sharing the repository's object store, so creating one is
//     cheap; or, for a directory that is not a git repository, a copy-on-write
//     snapshot (reflink where the filesystem has it) backed by a private
//     repository, so every operation below behaves identically.
//   - Finished trees are integrated by one serial, verifying merge queue (Queue):
//     each submission is merged or rebased onto the current integration tip, the
//     verifier runs on the result, and a failure rolls the tip back. A textual
//     conflict is returned as data (which files, which hunks, who landed the
//     competing change) and leaves the integration tree exactly as it was.
//   - Scope declarations are checkable (Overlap, Assert): the harness, not the
//     model, decides whether a task stayed inside the files it was leased.
//
// Trust. Repositories and agents are untrusted input to the harness. All git runs
// through internal/gitx, which never lets repository configuration, attributes
// or hooks execute programs. On top of that this package guarantees that it only
// ever deletes what it created: agent ids and branch names are validated, tree
// directories must stay under Manager.Dir (symlinks are not followed), every
// tree carries a marker written into its private git directory, and Remove and
// Prune refuse anything without a matching marker and branch prefix.
//
// Interruptions. A canceled context or a crash never leaves the repository or the
// queue half-done. gitx stops git in a way that lets it remove its lock files;
// the steps whose outcome would otherwise be ambiguous (moving the integration
// branch, moving the user's branch, finishing the registration of a tree) run to
// completion on their own bounded context once started; what an interrupted or
// crashed creation leaves behind is cleared by the next Create or Prune; and a
// queue whose integration tree was damaged rebuilds it from the branch, which only
// ever names verified commits.
//
// Integration with the rest of the harness (done by the session layer, not here):
// a tree's Path is what tools.Env.Cwd and Root should be for that agent, and the
// checkpoint store for that agent should be rooted there. Tree.Reset rewinds a
// tree to its base at the git level; it invalidates checkpoint records of that
// tree, so begin a new checkpoint after calling it.
package workspace
