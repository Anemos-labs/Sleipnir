// Package gitx is Sleipnir's only gateway to the git binary: a small set of
// typed helpers (status, diff, log, commit, apply, worktrees, merge ...) that all
// go through one hardened process runner.
//
// The stance, and why it exists. A repository is untrusted input. A directory
// somebody else prepared (an unpacked archive, a cloned project the user has not
// audited) can carry a .git/config and .git/hooks that make an ordinary
// `git status` run programs: core.fsmonitor, filter drivers selected by
// .gitattributes, merge drivers, external diff and textconv programs, hooks,
// credential helpers, signing programs, pagers, editors. Agents make it worse:
// an agent with a shell can plant the same things in its own worktree (the
// config is shared with the main checkout) and then wait for the harness to
// commit or diff its work with the harness's privileges. So every invocation:
//
//   - runs git by absolute path, with a scrubbed environment (all GIT_* reset,
//     credential-looking variables and askpass helpers dropped, LC_ALL=C so error
//     text is stable, GIT_TERMINAL_PROMPT=0, no pager, no editor, stdin closed,
//     no controlling terminal);
//   - overrides every configuration key that names a program with a harmless
//     value on the command line (highest precedence): hooks path, fsmonitor,
//     external diff, credential/askpass/ssh helpers, signing, gc/maintenance;
//   - discovers the attribute-selected drivers (filter.*, merge.*.driver,
//     diff.*.command/textconv) that the effective configuration defines and
//     blanks each of them, because those names are chosen by the repository and
//     cannot be listed in advance (see guard.go);
//   - pins the work tree to the directory that holds .git, and the git directory
//     to the one found when the handle was opened (GIT_DIR), so a hostile
//     core.worktree cannot redirect `add -A`, `reset --hard` or `clean` at some
//     other directory, and an agent that rewrites the .git file of its own
//     worktree cannot make the harness commit into a different repository;
//   - is bounded: a deadline (context and per-call timeout), a cap on captured
//     output, and a kill of the whole process group when either trips.
//
// Consequences worth knowing: hooks never run (also the user's own; the harness
// commits are machine work, not the user's), LFS-style filters are not applied
// (pointers stay pointers) unless the operator names the driver in
// WithTrustedFilters, and custom merge drivers turn into ordinary conflicts.
//
// Handles are immutable after Open and safe for concurrent use. Operations that
// change a repository retry briefly when another git process holds one of its
// lock files, which is what many agents sharing one object store produce.
package gitx
