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
