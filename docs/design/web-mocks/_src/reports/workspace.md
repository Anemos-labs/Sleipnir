# Report: files, checkpoints, diff, rewind, worktrees (2026-10-09)

Read-only investigation. Paths relative to the repo root. (Saved by the coordinator from the investigator's report.)

## 1. internal/checkpoint

- **Ids**: `cp_0001`, `cp_0002`, ... Never reused (`store.go:297`, counter in metaDoc). `normalizeID` (`store.go:300`) accepts `cp_3`, `3` and `0003`, but not "c3". The UI must map cN to this itself.
- **Creation**: `Session.Run` calls `Ckpt.Begin("turn N: <goal>")` once per user turn or goal (`session/session.go:1059`). It is per turn, not per agent and not per file. In an interactive swarm, workers that outlive the turn write into whatever checkpoint is current. Begin and checkpoint creation are not logged as events.
- **What is stored**: only the pre-write state, once per path per checkpoint (`Before`, `store.go:549`, `snapshot` at `:665`): kind, content blob (shared session `blobs/`), sha256, mode, mtime, symlink target and owner (`state.go:38`), plus `Agents` (set), `At`, `NewDirs`, and a `Post` fingerprint. `Post` is a hash only, no blob: `After` discards the data (`store.go:786`). Files over 64 MiB are recorded as unsaved (`store.go:62`). The manifest is `checkpoints/cp_NNNN.json`.
- **APIs**:
  - `List()` returns `Info{ID, Label, Time, Files, Agents, Unsaved}` with JSON tags (`store.go:100,798`). It has no +/- counts. `Files` are the paths first touched in that checkpoint.
  - `Diff(id)` (`diff.go:52`) compares checkpoint to working tree, cumulatively from id forward. It returns `FileDiff{Path, Status added|modified|deleted, Agents, Binary, Old/NewSize, Added, Removed, Unified, Note}`. The unified text has `--- a/p` / `+++ b/p` headers, 3 lines of context and limits at `diff.go:40` (2 MiB per file, 256 KiB output, "[diff truncated]"). Binary detection is NUL in the first 8000 bytes (`:198`).
  - `Restore(id, RestoreOpts{DryRun, OnlyPaths, OnlyAgent, Force})` (`restore.go:51,182`) returns a `RestoreReport` with per-file `Action` and `Outcome` (`done|planned|unchanged|conflict|unrestorable|failed`) and `Summary()`. Conflict rules are at `restore.go:373`: a file edited after the last recorded write, or touched by another agent when `OnlyAgent` is set. A restore refuses paths that resolve outside the root.
- **Restore is destructive with no undo**: restored paths are forgotten from the records, and a full restore deletes all later checkpoints (`restore.go:570-600`). `DryRun` gives the preview. Undo does not exist. Restore must not run while agents write (`restore.go:180`).
- **CLI**:
  - `/rewind` (`cmd/sleipnir/chat.go:714`) lists checkpoints that touched files. With an id it calls `Restore(id, RestoreOpts{})` (no Force, no filters, no preview), prints `Summary()`, and then `s.Send(rewindNote)` so the agent re-reads (`:744`).
  - `/diff` (`:761`) defaults to the newest checkpoint with files and prints `Unified` for each file.
  - There is no separate CLI.
- **Swarm, shared tree (default)**: one store (`s.Ckpt`) is shared by all agents. Tools pass `Env.Agent` to `Before`, so `Agents` and `OnlyAgent` work (`session.go:815-817`). Gates in `fs.commit` (`tools/fs/fileio.go:281`): Guard.BeforeWrite, then Snap.Before, then atomic write, then FileState.RecordWrite, then Guard.AfterWrite.
- **Swarm, worktree isolation**: each worker gets its own store at `<session>/checkpoints-trees/<agent>`, rooted at its tree, with one checkpoint named "<agent> worktree" (`session/isolate.go:369`, `swarm/lifecycle.go:202`). `/rewind` only sees the checkout store.
  - Default (uncommitted) integration records the files into the checkout store as agent `"integration"` before applying the patch (`swarm/isolate.go:608-621`), so `/rewind` undoes it.
  - `--commit` integration records nothing (`applyCommits`, `:636`); undo is git.
- **bash never snapshots**: there is no Snap use in `tools/shell`. `sed -i`, formatters and redirects are not checkpointed or attributed. They only show up as "edited behind our back" conflicts.

## 2. workspace / gitx / isolation

- **Preconditions** (`session/isolate.go:84-205`): git repo whose root is the project root, at least one commit, cache dir outside the repo. With `--commit`: a branch and a clean checkout (`:156-169`).
- **Names**: trees at `<cache>/sleipnir/worktrees/<session>/<agent>` (`:217`); branches `sleipnir/<session>/<agent>`, plus `_integration`, `_base` and `_resume` (`:292`).
- **Per writer**: `Swarm.createTree` (`swarm/isolate.go:136`) calls `Manager.Create` (`workspace/manager.go:345`) at the integration tip. Trees start from a snapshot commit that includes the user's uncommitted edits (`Manager.Snapshot`).
- **Worker commits** are authored `<agent>@sleipnir.invalid` (`workspace/tree.go:54`).
- **On `done`**: `swarm/isolate.go:263 integrate()` commits the tree and calls `Queue.Submit` (`workspace/queue.go:438`). Outcomes (`queue.go:89`) are merged, empty, conflict, verify_failed and rejected. The default strategy is a three-way merge. The verifier is `--verify` with `{dirs}` expansion (`swarm/verify.go:227`), run on the merged result, and the tip rolls back on failure.
- **Live data**:
  - `Queue.Status()` (`queue.go:335`) gives Branch/Base/Tip, Healthy, Active, Waiting, Landed ledger (agent, task, commit, files) and counters. `Manager.Trees()` (`manager.go:626`) gives Path, Branch, Base and Agent. Neither is reachable from `Session`. `s.iso` is private and only `WorktreeIsolation() bool` is exported (`session/isolate.go:312`).
  - Events: `workspace.create` (path, branch, base) and `merge.*`, plus `task.merge` (task, outcome, commit, files) and `swarm.integration`.
- **Applying to the checkout** (`Swarm.Integrate`, `swarm/isolate.go:541`): it diffs `applied..Tip`, runs `git apply --check`, takes checkpoint `Before("integration")`, applies, then calls `OnWrite`. It runs automatically when the manager stops (`swarm/swarm.go:611`, `applyMerged` at `isolate.go:721`) and at `Finish` (`:768`), and is idempotent. It never overwrites user edits; on failure the report carries a Hint.
- **`--commit`**: `applyCommits` uses `Queue.FastForward` (`queue.go:1032`). It needs a clean tree and a branch that has not moved. It is chosen at session start only; there is no runtime switch and no manual trigger.
- **Diff and patch helpers**:
  - `gitx.Repo.Diff/DiffTrees` return `Files[]{Path, OldPath, Status, Added, Deleted, Binary}` plus a `--binary` patch (`gitx/diff.go:26-86`).
  - `Tree.Changed/Diff/DiffWith` (`tree.go:70-95`) give per-worker diffs.
  - `gitx.ApplyOptions{Check, ThreeWay, Index, Reverse}` (`gitx/apply.go:12`) means a single hunk can be reverse-applied. Slice file header plus one hunk from `Unified` and use `Check` then `Reverse`.
  - `Repo.Show(rev, path)` (`refs.go:152`) reads a file at a commit. `Repo.Worktrees` is at `worktree.go:27`.
  - `CommitAll` (`commit.go:172`) does `add -A`, with no path-limited commit.
  - `Repo.Git` has no `blame` in its allowlist (`repo.go:243`).

## 3. Leases, scopes, protection, staleness

- **Scopes**: a scope is `Task.Files` plus `Owner`, from `Swarm.Board.Snapshot()` (`swarm/board.go:59,127`). `Board` and `Leases` are exported fields on `Swarm`. Enforcement is `Leases.checkScope` (`swarm/leases.go:176`), applied to writers with a doing task. Unscoped agents are unrestricted. Globs are matched by `swarm.inScope` (unexported, `swarm/scope.go:204`) and by exported `workspace.Match/Covered/OutOfScope/Overlap` (`workspace/scope.go:486,511,532`). The latter is reusable for the UI.
- **Leases**: shared tree: per-file TTL lease (10 min), `BeforeWrite` at `leases.go:300`, with `Holder(path)` and `HeldBy(agent)` at `:408,419`. Isolated: advisory "overlap" marks only (`:232`). `lease` events: acquire, conflict, scope, release, overlap (`:121`).
- **Protected paths**: deny and ask rules come from config `permissions.deny/ask` (`config/config.go:170`), loaded into `perm.NewEngine` (`session/session.go:506-562`). Built-in ask rules: `protectedConfigDirs` (`session/ext.go:140`). `Engine.Rules(perm.Deny)` lists the rule text (`engine.go:422`); `perm.ParseRule` parses it. Built-ins (`perm/builtin.go`): `.env*` guarded (`:248`), credentials hard-denied, and `.git` writes denied. Roles: manager and read-only roles are plan mode. `Engine.Confine` confines each worker to its tree (`:130`). There is no non-prompting "classify path" API. `Check` can prompt, and `evaluate` and the glob matcher are unexported.
- **Stale-read check**: `tools.FileState` (`tools/support.go:139-231`) is in-memory, holds a per-path `lastWriter`, and has no accessor. `CheckFresh` is at `:212`.

## 4. Blame

- **What the log carries**:
  - `tool.call` carries the agent in the envelope, the tool name, and the full raw `input` inline (`agent/exec.go:181-185`): path plus `content` for write, `old_string/new_string` for edit, patch text for apply_patch.
  - `tool.result` shares the `id` and has `meta` (`exec.go:295`): edit: path, added, removed, and a unified-hunk `diff` (max 400 lines, lines cut at 200 characters) at `edit.go:177`; write: path, created, bytes (`write.go:110`); apply_patch: files, added, removed (`patch_apply.go:768`).
  - `agent.spawn` and `agent.assign` carry agent to task (`spawn.go:162,247`). `board.op` carries task statuses. `lease` events and `task.merge` carry files.
- **Verdict**: line-level blame is derivable approximately, not exactly. It is reconstructed by replaying write, edit and patch inputs over the first-touch `Pre` blob. It breaks on bash edits, human edits, truncated diffs and patch hunks that have no line numbers. No post-write content is persisted anywhere.
- **Isolation exception**: in isolation, git history is exact. Each worker commit is authored by the agent and merged. `git blame <integration-branch> -- file` gives per-line agents and task ids from commit subjects. This needs `blame` added to the gitx allowlist, and the integration branch is retained only for resumable sessions.

## 5. Safe file access (for the web)

- No exported safe-path or read helper exists. These all sit in unexported or private code, so each needs exporting or copying:
  - `tools/fs`: `realPath`, `openRegular`, `readFile` (32 MiB cap, `fs.go:36`), `walker` (never follows symlinks, honours .gitignore, skips `.git`, `walk.go:40`); `tools.DefaultLimits` has 4 MiB `MaxReadBytes`.
  - `checkpoint`: `resolveKey/resolveDir/underRoot` (`store.go:637-663`) and `readBounded` (`state.go:293`).
  - `inspect`: `openRegular` (O_NOFOLLOW|O_NONBLOCK, `inspect/open_unix.go`), hash-validated blob paths (`blobs.go:47`).
- `internal/inspect/server.go` is a reusable model for the web server: embedded assets and a strict CSP, loopback Host allowlist, token cookie, mandatory token for non-loopback (`checkListenAddr :223`). It is GET/HEAD only (`:187`), so mutating endpoints would need their own CSRF and Origin checks.
- `tools.SanitizeForTerminal` (`tools/terminal.go:39`) exists. Binary detection: the NUL check in `diff.go:198` and `read.go:132`.
- `inspect/isolation.go` already ingests the worktree events (counts, merges, integration), so the line in `docs/SWARM-PROTOCOL.md:653` is stale.

## 6. Feature table

| UI feature | Status |
|---|---|
| File tree, file read | NEW: path-safe lister and reader; export patterns from 5 |
| Ownership stripes | Derivable: tool.call events by agent; shared tree also from `Info.Agents`; isolation also from per-tree `Diff`. Last writer needs a new accessor on FileState |
| Leases (scope globs) | EXISTS in memory: `Board` tasks and `Leases`, `workspace.Match`. Needs a snapshot endpoint |
| Protected paths | Derivable: `Rules(Deny)` plus built-ins. NEW: a non-prompting classifier |
| Changes grouped by agent or task, +/- | Partly: `Diff().Agents/Added/Removed` are cumulative per checkpoint. Task mapping from `agent.assign` and `board.op` ranges. NEW: a per-checkpoint delta diff and per-agent split in shared-tree mode. Isolation: exact via `Tree.DiffWith` |
| Verification state | Derivable: `Task.Status/Closure/VerificationFailures`, `task.merge`, `merge.verify_failed` (cmd, exit, tail). `Evidence` is free text |
| Diff viewer | EXISTS: `FileDiff.Unified`, `Tree.DiffWith`, `gitx.Diff` |
| Per-line blame | Derivable (approximate) from events plus `Pre` blob. NEW: a write journal for exactness; isolation: git blame |
| Hunk revert | Derivable: `gitx.ApplyWith{Reverse}` on a sliced hunk. NEW: path scoping when the root is a subdir, Before/After around it, and a non-git fallback |
| Time-travel scrubber | Derivable: the content of path as of cN is the earliest `Pre` blob among checkpoints at or after N (from `planLocked`), else the current file. NEW: exported accessor. Blind to bash and human edits. Isolation: `Repo.Show(commit)` |
| Checkpoints list, Diff, Restore | EXISTS (cp ids, `List/Diff/Restore`) |
| Restore preview | EXISTS: `DryRun` |
| Restore confirm, undo | Confirm is UI-only. Undo: NEW (restore drops history); can be built from a second Store |
| Reviewed marks | NEW (UI-owned persisted state) |
| Verify/Merge queue strip | EXISTS: `Queue.Status`. NEW: a `Session` accessor |
| Worktrees list | EXISTS: `Manager.Trees`, `Repo.Worktrees`. NEW: a `Session` accessor |
| Accept verified -> commit | Partial: `--commit` and `FastForward`. NEW: manual trigger and deferral of the automatic apply; non-isolated commit with a path-limited commit |

## 7. Gaps (needs new harness code)

1. `Session` accessors for the merge queue, trees, `Leases`, `FileState` last-writer and the effective deny and ask rules.
2. Checkpoint API: exported `ContentAt(id, path)` and a per-checkpoint or between-checkpoints diff. Optionally add +/- counts to `Info`, plus `Begin`/`Restore` log events so the UI can follow along.
3. Persist post-write content or diff per write. This is the cleanest basis for exact blame and per-agent counts. Also snapshot bash-driven changes.
4. A non-prompting `perm` classifier: "this path is deny, guarded, hard or ask".
5. Undo of a restore, built as a pre-restore capture into a separate Store.
6. Manual integration and commit control. It must also refuse a dirty checkout and use a path-limited commit.
7. Safe file tree and read helpers, plus a mutating-endpoint security model (CSRF/Origin, token).
8. Concurrency: gate Restore and revert while workers are writing (`restore.go:180`) and send the rewind note to affected agents, as `/rewind` does.
