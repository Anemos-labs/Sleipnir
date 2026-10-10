# Report: events and swarm state for the web UI (2026-10-09)

Read-only investigation. Paths relative to the repo root. (Saved by the coordinator from the investigator's report.)

## Headlines

1. **A reducer over the log already exists.** `internal/tui/state` (State.Apply state.go:109; Snapshot snapshot.go:11/50) is what `sleipnir watch`, `replay` and the chat read. It is pure, bounded, tolerant, and every Snapshot type has json tags, so it can be marshalled as is. The mock's vocabulary is a lossy subset of it. Wrap this state, don't write a second one.
2. **A web dashboard already ships.** `internal/inspect` is GET-only, poll-based (no SSE/WebSocket), and embeds a SPA in `internal/inspect/web`. Routes are at server.go:149-173 and the `http.Handler` at server.go:203. `cmd/sleipnir/inspect.go` serves it.
3. **`swarm/hot.go` and `swarm/replay.go` are not UI views.** `RenderHot` renders the text of each agent's uncached prompt tail. `ReplayBoard` (replay.go:16) rebuilds a swarm.Snapshot from board.op for resume. Neither feeds watch, replay or inspect.
4. **Both reducers ignore the newest events.** `swarm.stall`, `swarm.handover` and `goal.state` are ignored or counted unknown. board.op fields `closure`, `blocked_on`, `kind`, `agreement(s)` and `verification_failures` are not read.

## Log mechanics

- **Envelope** (events/log.go:27): seq, ts (UTC), session, agent (empty means kernel-level), type, cause, data, v (first record only).
- **Seq numbers:** contiguous from 1 (`log.open`), assigned under a mutex (log.go:390).
- **Storage:** append-only `events.jsonl`, with large payloads in content-addressed blobs (blob.go:20, DirBlobs `ab/cd/<sha256>` at blob.go:62). A line may not exceed 32 MiB (log.go:93).
- **Disk lag:** group commit gives at most about 5 ms between Emit and the file (log.go:81, 430). `Flush()` fsyncs.
- **Readers:** `events.Scan` (log.go:567) skips corrupt lines. `state.Tail` and `Follow` poll every 250 ms and survive rotation and torn lines (tail.go:73, 104).
- **In-process subscription exists:** `Log.Subscribe(buf)` (log.go:475). The chat uses it at cmd/sleipnir/chat_tty.go:93 and chat_record.go:179. A slow subscriber silently loses events. The drop counter is unexported, so detect a seq gap and re-Fold the file after `Flush()`, as `pastState` does (chat_tty.go:151). `State` drops duplicates (seq not greater than the last) but does not detect gaps.
- **Not in the log:** streamed text deltas (`agent.Sink.Text`, in-process only; the log has `turn.append` after each response) and tool output text (only in `turn.append` blocks or the `ref` blob).

## Event types and payloads

**Lifecycle**
- `log.open`: schema. `log.corrupt`: corrupt_lines, first_corrupt_line, torn_bytes.
- `session.start` (session.go:1057): version, model, provider, dialect, swarm, root, cwd, renderer, recon_tokens, shared_hash, models{id:{source, context, input/output/cache_read/cache_write_5m/cache_write_1h per_m, ttl_s}}, isolation?, mailman?, resumed?, meta?. No permission mode, no budget.
- `session.end`: cost_usd (includes goal-judge spend), reason.
- `session.effort`, `session.isolation`, `swarm.integration_state`, `model.switch`, `perm.state`: bookkeeping.
- `user.input`: text, origin? (task, mail or system for harness input). `user.steer`: defined but never emitted.
- `notice`: level, msg.
- `goal.state`: {goal:{Objective, Turns, Max, Paused, Reason, Repeats, Done}}. These keys are capitalised because the struct has no json tags.

**Agents**
- `agent.spawn`: id, role, task, by/parent, model, service?, handover_from?.
- `agent.assign`: id, role, task, by. `agent.prepare`: id, role, task.
- `agent.state`: id, state (running, waiting, idle, done or failed), line, task.
- `agent.end`: id, state (idle, done or failed), evidence, service.
- `agent.stuck`: phase (nudge, stop, plan, verify or leak), note or error.
- `agent.cancel`: phase (model, tools or between), cause (canceled or deadline), steps.
- `agent.snapshot` and `agent.restore`: resume blobs.
- Faults: `agent.panic`, `agent.abandon`, `supervisor.panic`, `sink.panic`, `tool.panic`, `tool.timeout`.

**Model, tools, permissions**
- `turn.append`: a full core.Turn (role, blocks of text, thinking, tool_use or tool_result, usage).
- `model.request` (request.go:393): req, agent, role, kind (main or compactor), model, provider, sections[{name shared|role|notes|spine, hash, tokens, bp}], thread_from/to, hot (hash), cache_key, prefix_key, breakpoints[{label, ttl in ns}], shared_blocks/tokens, tools, manifest, wire_hash.
- `model.response` (request.go:307): req, usage{input_tokens, cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens, output_tokens, reasoning_tokens}, cost_usd, hit_ratio, expected_read, missed, anomaly, expected_cold, stop, ttfb_ms, total_ms, completion (blob).
- `model.error`: req and error, or a retry notice (kind, status, attempt, delay_ms).
- `tool.call`: id, name, as?, input.
- `tool.result`: id, error, chars, ms, ref (blob), meta (edit adds added, removed, diff, path), refused?.
- `tool.spill`, `tool.budget`, `hook.run`. `tool.job`: id, command, status, exit, duration_ms.
- `perm.ask`: tool, reason, command, paths, role.
- `perm.decide`: the same fields plus allow, by (user, no one, policy or canceled), remember. There is no id; pair a decision with its question by agent, tool, command and paths.

**Cache engine**
- `layer.commit`: scope (shared-epoch, shared-sync, thinking-strip or params), reason.
- `cache.anomaly`: kind (drift, low_hit, thinking_binding, thinking_dropped or notes_over_budget), diverged layer, req, expected_read, actual_read, missed.
- `compact.plan`: decision (start or "commit?"), mode, reason, warm, thread_tokens, net_ite.
- `compact.patch`: stage, reason.
- `compact.commit`: reason (prefixed `mask:` or `emergency:`), removed_turns/tokens, retained_tokens, snap_tokens, spine_added, masked*, squeezed*, fallback, held_ms.
- `compact.reject`: stage, reason, fallback.
- `cache.plan` and `recall`: constants only, no emitter.

**Swarm** (events/types.go:67-131)
- `board.op` (board.go:320-392): op, version, plus the task's whole state.
  - Create ops add title, desc, role, kind, deps.
  - A handover `assign` adds `from` and `assignment_closure`.
  - The `agent` op carries agent, role, state, task, line, ctx_tokens (always 0) and cost_usd.
  - Other ops: agent-remove, note, notes-take, alert, alert-clear, alert-expire, dropped.
- Typed closure (swarm/closure.go:14-36): verified, agreed, blocked_on(T), superseded(T), canceled, denied, verifier, exhausted, handed_off(agent).
- Mail:
  - `mail.send`: id, from, to, kind, text, via, origins.
  - `mail.route`: id, from, to, kind.
  - `mail.deliver`: id, from, via (agent is the recipient).
  - `mail.ack`: id. `mail.drop`: id, from, reason.
  - `mail.digest`: id, to, mailman, parcels[], senders[], kind, frame.
  - `mail.direct`: reason, n, ids. `mail.batch`: batch, mailman, recipients, parcels. `mail.mailman`: state (up or down), reason.
- `lease`: action (acquire, release, conflict, scope or overlap), agent, path, holder.
- `governor`: action "rate-limited", rate_per_min, pause_ms, retry_after_ms, inflight, queued. Emitted only on a 429 episode (swarm.go:386, governor.go:196).
- `swarm.budget`: budget_usd, spent_usd. Emitted only when the budget is spent (budget.go:57).
- Supervision: `swarm.hold` (reason), `swarm.unfinished` (reason), `swarm.wake` (n, note), `swarm.wake.paused`, `swarm.wake_limit` (limit, task), `swarm.shutdown`.
- `swarm.stall` (stall.go:424): action (raise or clear), kind (claimed_no_progress, manager_waiting_on_idle, orphaned_task, blocked_cycle or review_starved), task, agent, notify, detail.
- `swarm.handover` (handover.go:147, 223, 283): phase (begin, done or abort), task, rev, from, to, by, closure?, error?.

**Isolation**
- `workspace.create`, `remove`, `prune`, `commit`, `reset`.
- `merge.queued`: position, task (the subject "T3: title"). `merge.merged`: before, after, commits, files, file_count, strategy, verified. `merge.conflict`: files, hunks. `merge.verify_failed`: cmd, exit_code, timed_out, output. `merge.rolled_back`: tip. `merge.rejected`: reason, empty?. `merge.fast_forward`: branch, from, to.
- `task.merge`: task, outcome (merged, empty, conflict, verify_failed, rejected or error), commit, files, reason.
- `swarm.integration`: branch, tip, applied, committed, files or reason.

**Other:** `outcome`: RL label, not used by the UI.

## UI concept to harness source

| UI concept | Harness source | Available? |
|---|---|---|
| Agent state | `state.Status` (tui/state/agent.go:119, tools.go:71); mapping at tui/app/cockpit.go:108: starting and thinking → think, asking → ask, error → stuck | yes |
| `doing` line | `Agent.Line` is coarse and tool-kind only ("running a command", swarm/evidence.go:249); `Agent.Tool + ToolSummary` gives the command or path | partial, no prose |
| Per-request hit series | `Agent.Hits.Ratios` (ring of 256) plus marks (types.go:303, model.go:162); `inspect.Requests` (views.go:441) has hit, read, write, in, out, expected, layers[7] per request | yes |
| Tokens (uncached, read, write, out) and cost | `Agent.Tokens` and `CostUSD` (reported cost, compactor calls included); `Totals.Savings` at list price; `inspect.CostReport` compares against no-cache and naive baselines (views.go:117) | yes (5m and 1h writes are merged in state) |
| Layers G0..G6 | state gives G1–G4 sized and the rest as one `Unsectioned` lump (types.go:205/220; app/cacheview.go:187 splits it by estimate); `inspect.Layers` (layers.go:42, views.go:376) gives all seven via blobs | partial in state, yes in inspect |
| Cache anomaly plus why | `state.Anomaly` (types.go:342): kind, layer, expected/actual, `MissUSD`; `inspect.Anomaly` adds Title, Explain[], Causes[] (explain.go:23) | yes via inspect |
| Compaction | `state.Compaction` (types.go:313): before, after, mode, cold or warm moment; pct is derived | yes |
| Gantt, last 60 s | `Snapshot.Activity` (activity.go:79) is 120 one-second busy levels (0–8) per agent plus mail, compact, stuck and anomaly bits | partial: busy only |
| Task columns, deps | `Task.State` todo, running, verifying, merged or failed (board.go:339, types_swarm.go:26); deps, files, owner, rev, attempts | yes |
| Merge queue steps | `MergeQueue` (types_swarm.go:148): waiting, recent, counts, bounced, integration | outcomes yes, phases no |
| Mail | `Mail` (types_swarm.go:212): stages sent, routed, delivered, digested, dropped; counts; mailman up or down | routed and mailman yes, dup no |
| Governor | `Governor` (types_swarm.go:235): rpm (counted from model.request in the last 60 s), episodes, rate, pause, inflight, queued, retries, rate_limited | yes |
| Warm-cache clock | `TTLEntry` (types_swarm.go:397; prefix.go:72, 114; model.go:256); cockpit uses the newest "prefix" entry's `Remaining(sn.Now)` (cockpit.go:158-166), stack bar uses the agent's own entry (cacheview.go:266) | yes (needs a wall clock) |
| Goal plan and judge verdict | plan = `tool.call` name "plan", input.items[{step, status}]; `goal.state` | partial |
| Budget and cost | `Totals.CostUSD`; `swarm.budget` only on exhaustion; `session.end.cost_usd` | partial |
| Approvals | `Perms` pending and recent (types_swarm.go:306) | yes |

**Warm-cache clock details.** Last = response ts minus total_ms, not earlier than the request. TTL = the breakpoint ttl for the labels shared and role (prefix) or thread and notes (agent), else `session.start.models[].ttl_s`, else a flagged 5-minute default. Cold = remaining ≤ 0. Live, call `State.SnapshotAt(wallclock)`. For a replay, pass the virtual now.

**Goal and judge.** The standing goal and judge exist only in the interactive chat host (cmd/sleipnir/chat_tty.go:371-420). A web host must reimplement that loop. The judge call (session/goal.go:52) bypasses the log. Its verdict kind and `Left[]` are never recorded, and `state.go:258` ignores `goal.state`.

## Replay and seek

- `state.FoldUntil(path, seq)` (load.go:21) folds the log up to a seq.
- `state.Replay` / `Player` (replay.go:55/159/186) replays on a virtual clock: Advance, Drain, Until.
- `ReplaySource.Seek` (tui/app/source.go:199) plays forward, but a backward seek reopens the log and replays from the top, so every backward seek is O(n).
- There is no State Clone or keyframe.
- `inspect` keeps a sparse seq→offset index every 256 events (session.go:61, eventsview.go:29), but it only pages raw events (`/api/events?since=`).
- Options for a scrubber: serve raw events to the browser, or keep server-side keyframe snapshots every N events. A time→seq mapping must come from Event.TS.

## Gaps the web adapter must compute itself

1. Per-state gantt segments (think, tool, edit, wait, ask, with doing text). The reducer keeps only busy levels, and no status history or per-event hook exists. Diff statuses after each Apply, or add a hook.
2. Merge-queue phases (rebase, verify). Only queued and the outcome are logged. The live phase exists only in `workspace.Queue.Status()` (queue.go:335), in-process. Duration = merged ts minus queued ts.
3. Mail dedupe and rate-limit refusals. They go back to the sender as a tool error, with no event (mailbox.go:277).
4. Governor RPM cap and an RPM time series. The configured cap is not logged, and `governor` fires only on 429.
5. Budget cap and remaining budget. The server must know its own `--budget-usd`; per-request cost excludes the judge.
6. Judge verdict, `Left[]`, and plan-step state. Parse `plan` tool calls; infer the verdict from goal.state (Done, `Paused` starting "blocked:").
7. `swarm.stall`, `swarm.handover`, board closure, blocked_on and plan-task kind. Neither reducer handles them yet, so add handlers.
8. Streaming text and tool output. Use `turn.append` and blobs, or an `agent.Sink` fan-out.
9. Edit diffs. Use `tool.result.meta.diff`, added and removed. Checkpoints are not events; read `checkpoint.Store.List()` (checkpoint/store.go:798) from the session's `checkpoints/` dir.
10. Permission mode: absent from the log.
11. Session-relative seconds. The mock uses seconds, but events carry wall-clock ts; subtract `Snapshot.Session.Started`.
12. In-process swarm views (Board, Leases, Gov.Stats, Swarm.Stalls, MailmanStats). They exist for a server that hosts the session, but are not replayable, so don't rely on them for replay.
