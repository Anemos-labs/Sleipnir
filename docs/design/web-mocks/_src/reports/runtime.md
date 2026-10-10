# Report: how the terminal chat is wired, for the `sleipnir web` controller (2026-10-09)

Read-only investigation. Paths relative to the repo root. (Saved by the coordinator from the investigator's report.)

## 1. Building and driving a session

- **Options.** chat.go:101-109 turns flags into `session.Options`. `chatOptions` (chat.go:244) sets `Interactive:true`. chat_tty.go:55-64 adds `Sink`, `Prompter` and `NewSink` (the swarm shares one sink), then sets `session.Version`.
- **Creation.** `session.New(ctx, o)` runs in a goroutine (chat_tty.go:77) so the program is already up. It can ask questions itself: the project-trust question (session/trust.go:77) and the MCP-server approvals (session/mcp.go:191).
- **Session fields.** session.go:163-239: `ID, Dir, Log, Blobs, Model, Provider, Registry, Perm, Ckpt, Shared, Agent|Swarm, Skills, Roles`.
- **A turn.** `Session.Run` (session.go:1014) opens a checkpoint per turn (:1059). A swarm goes through `Swarm.Start` and `RunManager` (:1061-63).
- **Goal loop.** `sessionHost.Turn` (chat_tty.go:345) calls Run, then `judgeGoal` (:362) calls `Session.GoalTurn` (session/goal.go:119):
  - A cancelled turn pauses the goal ("you interrupted it", goal.go:128); a failed turn pauses it too.
  - It returns a note plus the next prompt, and `SaveGoal` writes a `goal.state` event (goal.go:162).
  - On resume `LoadGoal` returns the goal paused.
  - `chatModel.runEnded` sends `Next` only if the typed-ahead queue is empty (app/chat.go:908).
  - `/goal` subcommands are in `goalCommand` (chat_tty.go:375). `/goal pause|clear` during a turn cancels it, then runs at the front of the queue (app/chat.go:705).
- **Typed-ahead queue.** It lives in the program, not the session: `submit` (app/chat.go:696-731) and `drainQueue` (:799).
  - Ctrl-D is queued (:648).
  - Everything is queued until the session attaches (`busy()`, :605).
  - "Look" commands bypass the queue (`isLookCommand` :737: /cost /stats /context /agents /help /skills /recon /status /permissions /trust /allow /steer /effort, and /mode and /mcp with no argument). They run beside the turn (`aside` :756).
  - The plain chat reads stdin through `stdinLines` instead (chat_input.go:58).
- **Slash commands.** `slashTo` (chat.go:336) handles /exit /help /mcp /status /permissions /trust /cost /stats /context /compact /agents /steer /plan /model /effort /fav /budget /allow /cwd /sessions /mode /rewind /diff /recon, plus custom commands, skills and MCP prompts through `expandSlash` (:519).
  - `sessionHost.programCommand` (chat_tty.go:442) handles /goal /verbose /anim /fav /roles /login, /model on a team, /new /clear /resume, and /restart /swarm.
  - /stats, /agents and /exit are handled by the program itself (app/chat.go:701-712).
  - The plain chat has none of the `programCommand` set.
  - Command text goes to an `io.Writer`; a web controller can collect it in a buffer, as the TUI does (`lockedBuffer`).
- **Interrupts.**
  - Esc cancels the run only when no question is open (app/chat.go:657-662).
  - Ctrl-C always cancels the run and refuses the main agent's open questions. Workers' questions survive (:610-645).
  - At the idle prompt, a second Ctrl-C within 2 s quits (`QuitWindow`, chat_types.go:151).
- **Steer.** `/steer` calls `Agent.Steer` (chat.go:377; agent.go:497). It is read at the next step boundary and survives compaction.
- **Resume.** The flags are in `resumeFlags` (flagutil.go:55) and become `Options.Resume` ("latest", an id or a directory). `ResolveResume` is in session/resume.go:23 and `restore` in :169. Permissions come back at session.go:450. `CheckResume` (:361) lets the chat refuse a bad resume without ending.
- **Restart family.** /new, /resume, /restart, /swarm, a team's /model and /login all call `restartArgs` (restart.go:52). That keeps `StartFlags` (session/switch.go:172), `--allow` for the granted rules, `--mode`, `--model`, and `--resume <id>` unless the restart is fresh.
  - The program ends, `chatOnTerminal` closes the session, then `runAgain` execs a child and calls `os.Exit` (chat_tty.go:122-129; restart.go:144-155).
- **In-place changes.**
  - `SwitchModel` for a single agent (switch.go:22).
  - `SetEffort` (effort.go:13).
  - `SetBudget` (switch.go:79, single agent only).
  - `Perm.SetMode` (engine.go:287), also on shift+tab through `Host.SetMode`.
  - `AllowForSession` (switch.go:91).
  - `Compact` (session.go:1130).
  - `/rewind` is `Ckpt.Restore` plus a note to the agent (chat.go:714) and `/diff` is `Ckpt.Diff` (:761).
- **Closing.** `finishRun` and `s.Close` (run.go:411; session.go:1199). An isolated team applies its merge on close.

## 2. Approvals

- **Type.** `perm.Prompter func(ctx, perm.Request) perm.Decision` (perm/perm.go:266). It is set as `Options.Prompter` and wrapped by `trackAsks` and `hookPrompter` (session.go:555; hooks.go:246, 261).
- **Serialisation.**
  - `Engine.Check` (engine.go:376) logs `perm.ask` before queuing, then `resolveAsk`/`lead` (ask.go:110, 141) run one prompt at a time per engine.
  - Identical requests coalesce into a single question (`promptKey`, ask.go:45).
  - After a slot is acquired the rules are re-judged (`again`), so an earlier "don't ask again" auto-settles the later ones.
  - All workers share the one engine.
- **"N waiting".** The TUI keeps a FIFO of `m.dialogs` (app/chat.go:224; `ask` in chat_pages.go:18). It needs one because MCP and trust questions go straight to the prompter, bypassing the engine. The hint is `len(dialogs)-1` (chat.go:1132; chat_live.go:590). The log-side equivalent is `state.Perms.Pending` (state/types_swarm.go:306). The full `Request` exists only in the Prompter call; the log clips the command to 400 characters and paths to 5.
- **Quiet period.** `defaultAnswerAfter` is 350 ms (chat_dialog.go:49; `ChatConfig.AnswerAfter`, chat_types.go:151).
  - The timer starts when a question appears (chat_pages.go:20) and restarts on any key that is not an answer (chat.go:429-439).
  - The next queued question is re-armed after an answer (:487-491).
  - Only the digits 1 to 3 (or 4) answer. Enter and the arrows only work while the prompt is empty, Esc means no, and letters never answer.
- **Answers** (`dialogOptions`, chat_dialog.go:68; `decision`, :123).
  - Yes is `{Allow, "allowed by user"}`.
  - Yes with "don't ask again" adds `Remember: ScopeSession`.
  - The 4th option, on build and test commands, sets `Preset:"tests"`.
  - No is `{Allow:false, "denied by user"}`.
  - For MCP and trust the second option is `ScopeProject`.
  - There is no "no + instruction" field. In the TUI the person types the next message. A web UI could put the text in `Decision.Reason`, which tools show the model verbatim (shell/bash.go:123). That skips the automatic advice appended at ask.go:156, so the web side must add it.
- **Storage.**
  - `remember` (ask.go:206) widens the rule (runner.go:38) and calls `AddRule` (engine.go:300). The rule goes into the allow list and the `granted` list (`Granted()`, :414).
  - `perm.state` {mode, allow} is written when a turn ends (permstate.go:13) and re-read on resume (:30). Restart passes the rules as `--allow` (restart.go:93).
  - A refusal that is remembered becomes a Deny rule.
  - `perm.Config.Persist` is not wired for engine rules.
  - MCP "remember" writes `mcp-approvals.json` (mcp.go:199) and trust writes `trust.json` (trust.go:76-92).

## 3. How the terminal consumes live data

- **Pushed.**
  - The `agent.Sink` (Text/Thinking/ToolStart/ToolEnd/Response/Notice, plus Reset; agent.go:80) goes through `ChatSink` into a 4096-slot channel (chat_link.go:90, 104). The channel blocks the agent when full.
  - `Log.Subscribe(4096)` (chat_tty.go:93; log.go:475) drops events for a slow subscriber. Each event is folded with `State.Apply` (chat_log.go:14).
- **Polled.** `m.snapshot()` takes `State.SnapshotAt(now)` per frame and tick (chat_log.go:20). The state is pure, bounded and thread-safe. `state.Snapshot` is immutable and already carries JSON tags (state/snapshot.go:11).
- **Not in the log.** Streaming answer text, tool bodies, questions and keys never reach the log (app/chat_transcript.go:21-30). Only the program's own state holds them (`stream`, `tools`, `dialogs`, `queue`, `running`).
- **`source.go` is not used by chat.** It polls a log file for watch, replay and inspect (`LiveSource`, `ReplaySource`). On resume `ChatAttach.State` is preloaded from `state.FoldInto` (chat_tty.go:151).
- **Wire schema.** `app.ChatRecord` (chat_transcript.go:56) is already a JSON schema for text, tool_start, tool_end, ask, event and turn_end. `chatRecorder` (chat_record.go:162-182) is a working headless Sink, Prompter and `sessionHost` driver. It is the best template.

## 4. What a web controller needs

| Web need | Existing function (file:line) |
|---|---|
| Create session | `session.New` (session.go:258) with `Sink`, `Prompter`, `NewSink`, `Interactive` |
| Run a turn, cancel it | `Session.Run` (session.go:1014), ctx cancel; `sessionHost.Turn` (chat_tty.go:345) |
| Slash commands | `sessionHost.Command` (chat_tty.go:426), `slashTo` (chat.go:336) |
| Goal | `goalCommand` (chat_tty.go:375), `GoalTurn` (goal.go:119), `SaveGoal`/`LoadGoal` |
| Stream to browser | `agent.Sink` + `agent.Resetter`; template `ChatSink` (chat_link.go:116) |
| Ask the browser | `perm.Prompter`; template `ChatLink.Prompter` (chat_link.go:166) |
| Answer table | `dialogOptions`/`decision` (chat_dialog.go:68, 123) |
| Live stats | `Log.Subscribe`, `state.New().Apply`, `SnapshotAt` |
| Resume list | `EarlierSessions` (resume.go:333), `ResolveResume`, `CheckResume`, `ResumedAsTeam` |
| Rebuild on restart | `restartArgs` (restart.go:52), `StartFlags` (switch.go:172), `Perm.Granted`, `Perm.Mode` |
| Model list and check | `CheckModel` (switch.go:60), `modelChoices` (models.go:330), `session.ResolveModel` |
| Rewind and diff | `Ckpt.List/Restore/Diff`; `rewind` (chat.go:714), `showDiff` (chat.go:761) |
| Close session | `finishRun` (run.go:411), `SetEndReason`, `Close` |

### Hard-wired terminal bits to bypass

- `bufio.NewReader(os.Stdin)` + `ensureModel` (chat.go:97-98; pick.go:101) for first-run model and key setup, and `readSecret`.
- `runAgain`/`os.Exit` and the child process on a terminal (restart.go:144-176).
- `/login` leaves the chat to run `sleipnir login` on the terminal.
- `/trust` and the update notice read `os.Getwd()` (chat.go:362) and the update check (chat.go:95).
- `signal.Notify` for SIGINT in chat_input.go:327 and chat_tty.go:66. `ownsInterrupt` is not needed for web.
- `printIntegration(os.Stderr)`, `readInput` writes `"… "` to stderr, and `TextSink` writes to `os.Stdout`.
- Nothing else in `internal/` outside tui/rl/demo/inspect writes to stdout or stderr (grep).

## 5. Coexistence and ownership

- No package-global session state, and no `os.Chdir`, `os.Setenv` or signal handlers in `internal/` outside the terminal code. Concurrent `session.New` is already exercised by the RL runner (`Concurrency`, rl/env/runner.go:118, 421).
- Each session owns:
  - `Dir` (`$SLEIPNIR_HOME`/`~/.sleipnir/sessions/<id>`), holding `events.jsonl`, `blobs/`, `checkpoints/`, `tmp/` and a `.lock` file.
  - A flock on that directory (dirlock_unix.go:19).
  - The `Log`, one permission `Engine` (mode, rules, prompts) and the checkpoint store.
  - A frozen tool registry, a shell manager (background jobs) and MCP child processes.
  - The budget and effort, the `Model`/`Provider`, the plans store and the archive.
  - Optionally a `Swarm` (board, roles, mailman, governor, leases and worktree isolation `iso`).
  - Its `Cwd` and `Root`, its hooks, and a goal in the host (`sessionHost.goal`).

## RISKS / GAPS

- `sessionHost`, `slashTo`, `restartArgs` and `chatSlashCommands` are in `package main` (unexported). A new `internal/web` cannot import them. Either put `web.go` in `cmd/sleipnir`, or move them into an importable package.
- The host is not goroutine-safe: `sessionHost.goal` is unguarded (app/chat.go:706 says so), and `SwitchModel`, `SetBudget` and `Compact` touch `s.Agent`, `s.Provider` and `s.Model` without `s.mu`. Run one turn or command at a time per session, as the chat model does. Only the look commands may run alongside a turn.
- There is no in-process restart. Options are unexported and the chat flags are parsed inside `cmdChat`, so a new session cannot be rebuilt from the old one. Needed: `Session.Options()` or a flag-parsing function, and closing the old session before the new `New` (the flock, and isolation's `Finish` of up to 5 minutes).
- The Prompter and Sink must exist before `New`, because trust and MCP questions arrive during it. They cannot depend on a session id.
- A page reload cannot be rebuilt from the log. Mid-turn text, tool output and open questions exist only in the sink and prompter, so the controller needs a per-session replay buffer. A subscriber that falls behind loses events (counted); `Event.Seq` is there for catch-up.
- The sink blocks agents when full (chat_link.go:104). A slow browser must be coalesced or dropped, never allowed to block, but a question must not be dropped.
- A swarm manager can run on a wake without a turn (`Interactive` wakes it). "Busy" must also come from events, not only from the turn goroutine, and questions can arrive at any time.
- An unanswered question has limits: `AskTimeout` is 0, meaning wait forever. The worker watchdog cancels at 20 minutes (docs/SWARM-PROTOCOL.md:423) and the MCP approval context is 5 minutes. A closed tab must refuse or park the question.
- No quiet period exists in HTTP. Web should disable the answer buttons for 350 ms after a question appears and after each answer, and tie each answer to a question id.
- Mutating endpoints are new surface. The inspect server (inspect/server.go:177-300) is GET-only. It has a Host allowlist, a CSP, a token cookie and embedded assets, all reusable. Approving shell commands from a browser also needs a token on loopback, an Origin/CSRF check and POST-only.
- API-key entry and sign-in are terminal-only (login.go). Web must call `config.SaveStoredKey` and `harden.Provide` and keep keys out of logs.
- Shared files are not serialized across sessions: `~/.sleipnir/MEMORY.md`, `trust.json` (the lock is per `Ledger` instance, trust/ledger.go:135), `mcp-approvals.json` and `history.jsonl`. Lost updates are possible.
- Process-wide state that stays shared: the `harden` key store (mutex-guarded, fine), `session.Version` (set once), `SLEIPNIR_*` environment (`stateRoot`, `config.Load`), and `workspace.livePaths`/`repoLocks` and `fs.fileLocks` (keyed sync.Maps, fine). Always pass absolute `Cwd` and `Root`: `os.Getwd()` fallbacks (perm/glob.go:87, fs/paths.go:39, leases.go:144) and `/trust` use the process directory.
- Opening the same session dir twice in one process fails with the misleading "in use by another sleipnir process". The controller must keep a registry and attach instead.
- A resumed session shows only a two-line recap (`recapLines`, chat_tty.go:267). Full history for the browser must come from `Agent.Stack().Thread.Turns` or `turn.append` events.
- Plain-chat help lists commands that only the TTY host implements (/goal, /new and others). Web should follow `programCommand`, not `slashTo` alone.
