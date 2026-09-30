# Swarm runtime features: decisions and residual risks

Notes from finishing the swarm runtime in three pieces (manager stop guard and wake, worktree isolation with the verifying merge
queue, optional mailman mode). Each section records what was decided and why, what the tests hold the code to, and what is
still open. The protocol as implemented is `docs/SWARM-PROTOCOL.md`; this page is the reasoning behind the choices in it.

## Piece 1: manager stop guard and manager wake

### What was built

* `session.Options.Interactive` (set by `sleipnir chat`, not by `run`/`swarm`/RL rollouts) selects the behaviour:
  `Interactive == false` gives the swarm `Config.HoldManager`, `true` gives it `Config.WakeManager`.
* **Hold** (`swarm/hold.go`): the swarm wraps the `agent.Hooks` it gives the manager. `BeforeStop` runs the user's hooks first, then
  vetoes while the board holds unfinished work. The reason is a pure function of the board and roster (ids only, sorted, capped
  at 400 characters; the lists shrink before the instruction is cut). The agent loop's `maxStopVetoes` (3) bounds it; when it is
  reached `RunManager` appends `[harness] Unfinished when the manager stopped: ...` to the result text and raises a notice
  (`swarm.unfinished`).
* **Wake** (`swarm/wake.go`): events (a worker run ended, harness or peer mail for the manager) start a coalescing timer; when it
  fires the swarm computes what changed on the board since the manager's newest request rendered it, and if anything did (or mail
  is waiting) starts one manager run with a harness-written note.

### Decisions worth knowing

* **The note is mail, not a user turn.** `agent.Run(ctx, note)` would push an `OriginUser` turn, and compaction folds user turns
  into the manager's `instructions` notes as things the person asked for: eight wakes would leave eight fake instructions. The
  note is queued with `Agent.Send("[mail hN info from harness] ...")` (data origin, kept out of `instructions`) and the run starts
  with an empty input. It carries ids and status words only, so harness-authored text can never launder worker-authored text.
* **"What the manager has seen" is its last hot-view render.** The manager's `Hot` callback stores the snapshot it rendered
  (`Swarm.mgrSeen`); the wake digest is the diff against it. No extra bookkeeping, and an event whose effect the manager already
  saw does not wake it (`TestNothingNewDoesNotWake`, `TestNoWakeWhileTheManagerRuns`).
* **The swarm's context had to change for interactive sessions.** `Session.Run` used to start the swarm on the turn's context, and
  `runTurn` in chat cancels that context when the turn returns (`signal.NotifyContext`'s stop function cancels). So in
  `chat --swarm N` every worker still running at the end of a turn was cancelled and its task returned to todo, contrary to the
  documented intent that workers outlive a turn. Interactive sessions now run the swarm on a session-lifetime context
  (`Session.swarmContext`); Ctrl-C keeps working because it cancels the turn's context, which `RunManager` turns into a stop of
  the workers. A mutation test proves it: with the old behaviour `TestInteractiveSwarmSessionWakesTheManagerAfterTheTurnEnded`
  fails. Batch runs are unchanged (the swarm lives on the one `Run`'s context).
* **Batch sessions embedded elsewhere are now held.** The RL harness sets `Swarm: true` without `Interactive`, so its manager is
  held (at most three vetoes) instead of stopping with workers running. That is the intent for rollouts (the environment scores
  the repository at the end), but it is a behaviour change for anyone scripting a manager that answers early.
* **Never hold a run that is ending.** The agent loop does not consult Stop hooks for a cancelled run or an exhausted per-agent
  budget (verified in `agent.Run`: the context and budget checks come first, and a failed request returns before `BeforeStop`).
  The guard additionally refuses to veto when its context is done, the swarm is shut down, or the swarm or the manager's own
  budget is spent, because a response that overspends is followed by `BeforeStop` before the next loop iteration would notice.

### Residual risks

* A manager that ignores three vetoes still ends the run with work unfinished; the run says so instead of continuing.
* Wake latency is the quiet period (1.5 s) plus a manager request; the bound (8) is per human input, so a session left alone for
  a long time can still spend eight manager requests on a burst of finishing workers. `--budget-usd` bounds the money.
* In chat the wake turn streams to the terminal while the person may be typing at the prompt: the text is interleaved, and the
  prompt is not redrawn.

## Piece 2: worktree isolation (`swarm.isolation = "worktree"`)

### What was built

The protocol as implemented is `docs/SWARM-PROTOCOL.md` section 14. In short: `internal/session/isolate.go` decides early
(preconditions, refused before a model is contacted) and builds late (`workspace.Manager` + `workspace.Queue`, a start-up
`Prune`); `internal/swarm/isolate.go` runs it (a tree per writer, the merge step in `done`, bounces, the end-of-run
application); `internal/perm` learned two things (`Confine`, `TreeParents`); the CLI has `--isolation` and `--commit` on
`run`, `swarm` and `chat` (and `chat --verify`), `sleipnir config` prints the effective value and its origin.

### Decisions worth knowing

* **Merge before review.** `done` commits the tree and submits it to the queue; the task reaches review only when it merged, and
  `accept` requires the merge record (`swarm.merged`) instead of re-running the verifier in a checkout that does not have the
  work. The alternative (merge at `accept`) would make the manager's acceptance also the merge, so a rejected task's conflict
  would surface after review and a worker that had already stopped could not resolve it. The price is that work of a task the
  manager later rejects or fails stays on the integration branch (no revert; a rejected task's worker resubmits on top).
* **The verifier could not run is not a failed test.** A merge whose verifier timed out or could not start, and a git error, are
  reported as infrastructure (`integration.infra`): a `done` call is answered with the error; a worker that stopped without `done`
  has its task sent to review marked NOT MERGED, which `accept` refuses. Only a verifier that ran and exited non-zero bounces the
  worker as `verify_failed`. While a worker waits its turn in the serial queue a keepalive tells the watchdog it is not stuck.
* **Confinement is a hard deny in the permission engine, not a convention** (`perm.Engine.Confine`). It sits where every access
  of a request is judged, so it covers shell commands as well as the file tools, works on resolved paths (a link inside the tree
  that leads into another tree is another tree), and is not lifted by an allow rule or by bypass mode. The lease guard alone
  could not have done this: it only sees the file tools.
* **A finding of the end-to-end test: the project's relative rules did not apply inside a tree.** Rules such as `Deny(Edit(./migrations/**))`
  or the built-in ask rule on `./.sleipnir/**` are anchored at the workspace root, so a worker in its own copy of the project was
  held to none of them, and the merge would carry the forbidden change into the checkout. `perm.Config.TreeParents` anchors every
  relative rule (and floating allow rules) at each tree as well; `TestRelativeRulesApplyInsideEveryTree` and
  `TestProjectPermissionRulesHoldInsideTheTrees` fail without it (mutation-checked).
* **Where the trees live** (a second finding of the same test). The first design kept them under the state directory. Every write
  there asked for approval, in every mode, because `~/.sleipnir/**` is a protected configuration directory (hooks, skills and
  commands run as code), so no worker could write a file. They now live in the per-user cache directory
  (`$XDG_CACHE_HOME` or `~/.cache`, `<cache>/sleipnir/worktrees/<session>/`), which is also where large re-creatable data belongs. No
  configuration key chooses the location, and a location inside the repository or a protected directory is refused.
  Sessions run for another home (tests, the RL harness) keep everything under that home.
* **The result is applied when the manager stops, not only at the end.** `applyMerged` runs after every manager run (a batch
  run's only one, each turn and each wake run of a chat session), incrementally (the delta since the last application), and the
  person is told at level `integrate` when something arrived or when it could not (once per distinct failure). `Finish` reports
  the whole run. The default is uncommitted edits, which is what a shared-tree run leaves, and it is recorded in the checkpoint
  store first, so `/rewind` undoes it and the end of the session does not re-apply it. `--commit` fast-forwards the person's branch
  instead and requires a clean checkout, checked at start.
* **The trees start from the working tree, uncommitted edits included** (`Manager.Snapshot`), so agents see what the person sees
  and the result applies on top of it; if the person edits the same files during the run the patch does not apply, nothing is
  changed, and the report names the branch and the command that gets the result (`git diff --binary <applied> <branch> | git apply
  --3way`, starting from what was already applied).
* **The manager and read-only roles keep the checkout.** The manager is read-only in an isolated run (swarm-level denial with
  `strictShell`, plus a plan-mode profile in the engine) because anything it wrote into the checkout would bypass the queue.
* **No new tool.** A worker that needs another's merged work blocks its task and says so, as before; its tree is brought to the
  integration tip when the manager resumes it. New, reused, resumed and bounced trees are brought to the tip by the harness.
* **Leases are advisory; scopes are not.** The writer cap and the scope-overlap refusal are lifted (the queue settles overlaps and
  enforces the scope on the commit); overlapping writers raise the existing alert, naming agents only.
* **Cache.** The isolation card is appended to a worker's private assignment note only. No shared layer changes; the prompt names no
  path (the tools print relative paths), so two workers' first requests have the same system prompt byte for byte
  (`TestIsolatedSwarm...` asserts it and that no request names a tree).
* **Clean-up.** `Session.Finish` (called by `Close`, and by the CLI to print the report) removes trees that hold nothing unmerged and
  deletes the integration branches once their result is in the checkout; a killed session's trees are salvaged onto their own
  branches and removed by the next isolated session (`Manager.Prune`, live owners are never touched), and the person is told
  what was kept.
* **Subdirectories.** A session started below the repository root keeps its writers in the same subdirectory of their trees. A
  project root that is not the repository's root is refused (scopes and rules are anchored differently in a tree).

### Tests that hold it

`internal/session`: `TestIsolatedSwarmMergesAConflictAndAVerifierFailureAndAppliesTheResult` (seven scripted workers over a mock
endpoint on a real repository: three disjoint, a same-line pair, a pair whose changes fail the project's verifier together; the
checkout ends merged and verified, nothing crossed between trees, prompts are byte-equal and name no tree, the trees and branches
are gone), `TestTheSameTeamWithoutIsolationStillWorks`, `TestCommitOptionCommitsTheVerifiedResultOntoTheBranch`,
`TestIsolationRefusesWhatItCannotDo`, `TestTreesOfACrashedSessionAreCleanedUpAtTheNextStart`, `TestInteractiveIsolatedSessionAppliesAtTheEndOfEveryTurn`,
`TestRewindUndoesWhatAnIsolatedRunApplied`, `TestResultThatCannotBeAppliedStaysOnItsBranch`,
`TestProjectPermissionRulesHoldInsideTheTrees`, `TestIsolatedSessionInASubdirectoryKeepsItsWritersThere`; `internal/swarm`
(`isolate_test.go`, `isolate_flow_test.go`: conflicts, verifier failures, scope rejection, bounded bounces, empty merges, accept
needs the merge, advisory leases, guards); `internal/perm` (`confine_test.go`); `internal/config` (`isolation_test.go`);
`cmd/sleipnir` (`isolate_test.go`).

### Residual risks

* Reviewers and scouts read the person's checkout, which lacks merged work until the manager stops. They can read it with `git show`
  on the commit in the task's evidence, but an isolated review is weaker than a shared-tree one. A read-only tree at the integration
  tip per reviewer would fix it and was left out because the specification keeps read-only roles on the checkout.
* No revert of merged work when the manager rejects or fails the task afterwards (above).
* A verifier that needs untracked or ignored files (`.env`, `node_modules`, build output) fails in a tree; submodules are not
  initialised in trees. A project that cannot be built from a clean checkout is a poor fit.
* A project root that is not the repository root cannot be isolated; a monorepo sub-project needs the repository root.
* Disk: one checkout per writer, created at spawn; no sparse checkout by scope yet.
* The inspector does not show trees or the queue (the events are in the log); `internal/inspect` is not in this change's scope.
* The queue verifies with `--verify` only; without it merges are serialised and conflict-checked but not tested.
