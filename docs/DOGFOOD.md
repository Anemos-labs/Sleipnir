# Dogfood: finding what is wrong by using it

The tests say the parts work. They did not say that three quarters of the first benchmark's episodes hit a permission refusal,
that correct work was flagged as reward hacking, or that one Ctrl-C ended a whole chat. Real work found those, so this project
runs on real work and keeps a register of what it found. This page is the method and the register.

## The loop

1. **Use it.** `sleipnir chat`/`run`/`swarm` on real work, and `scripts/bench.sh` on the suite in `bench/` (`docs/BENCHMARKS.md`).
2. **Mine it.** `sleipnir friction PATH...` reads every `events.jsonl` under PATH (a session, `~/.sleipnir/sessions`, a run
   directory of rollouts) and ranks what cost something:

   ```text
   friction in 179 sessions (20643 events, 3047 model requests)

   #   SCORE  CATEGORY            COUNT  SESSIONS  WASTED  WHAT
   1   2328   permission.refused  291    138       288     bash: cd <path>
   2   1500   file.reread         375    57        0       the same part of a file read three or more times
   3   1066   cache.break         1066   164       0       low_hit
   4   240    permission.refused  30     24        30      bash: sed -n
   ```

   Every row has examples as `session seq: detail`: the seq is the event in that session's `events.jsonl`, so the evidence is
   one `sed -n 'Np'` away. Score is count x severity squared (severity is 1 a cost, 2 waste, 3 a stop: a run that ended or was cancelled) x
   (1 + model requests wasted per occurrence). The hints under the top rows say where a pattern of that kind usually comes
   from; they are leads.
3. **Fix the top of the list**, each with a test that fails on the parent commit (`docs/BUILDING.md`): a fix that cannot be shown
   to fail first is a guess.
4. **Measure again** against the same suite the same day: `scripts/bench.sh ab`, then `sleipnir rl compare A B --gate pass_at_1:0.05`.
   The endpoint drifts (its prefix cache alone varies by 30 points between identical runs), so a committed baseline shows the
   trend and only an interleaved A/B says whether a change helped.

What the miner reads: `perm.ask`/`perm.decide` (what was asked, why, and who settled it: the person, nobody because the run
was unattended, or a policy), failed `tool.result`s by tool and kind, `agent.stuck`, `agent.cancel`, `model.error` retries,
`cache.anomaly`, `compact.reject`, and calls repeated without a change in between. Logs written before `perm.decide` existed
are still mined: their refusals are grouped by command instead of by reason.

## Entry format

One entry per real session, appended below the register. A session that changed nothing is still an entry: it is the control.

```text
### YYYY-MM-DD <track> <what>            track: backlog | stdlib | fixture | greenfield
session: <id or run dir>   model/mode: <model>, single | swarm:N   prompt/binary: <commit>
verdict: merge | edit | discard          (what a reviewer did with the result)
time: <wall>   requests: <n>   cost: $<x>   friction top 3: <category: key (count)>
notes: <one paragraph: what worked, what the person had to do that the harness should have done>
```

## Register

S1 a cost, S2 waste, S3 a stop. *Fix* names the commit's subject; *Test* is the regression test (fails on the parent).

| # | Found by | Category | Evidence | S | Frequency | Cause | Fix | Test |
|---|---|---|---|---|---|---|---|---|
| 1 | bench pilot | permission.refused | `cd /workspace`, `cd /repo`, `cd /home/user`: the model guesses where the repository is | S2 | 45% of 201 refusals in 94 episodes; 77% of episodes | nothing told the model where it starts (`internal/agent/prompt.go`); the refusal did not name the workspace (`internal/perm/judge.go`) | constitution says where the agent starts and to use relative paths (a declared prompt change, priced in the CHANGELOG); refusals outside the workspace name it | `TestRefusalOutsideTheWorkspaceNamesTheWorkspace` |
| 2 | bench pilot | permission.refused | `sed -n '120,160p' file`, the way models read a range of lines | S2 | 10% | sed can write and run programs, so it was not on the read-only allowlist (`internal/perm/cmds.go`) | `sed` with a line-selection script only (`internal/perm/sed.go`) | `TestSedIsAllowedOnlyAsLineSelection`, `FuzzSedScript`, and the allowlist executed by `TestAllowedCommandsChangeNothing` |
| 3 | bench pilot | permission.refused | `$(pwd)`, `$OLDPWD`, `$(go env GOROOT)`, a loop variable as a path | S2 | 14% | the engine cannot know a path from a substitution | open: most are `cd "$(pwd)"` from the same cause as row 1; re-measure after the prompt change | |
| 4 | bench pilot | permission.refused | `cat > /tmp/x.go`: scratch files outside the workspace | S2 | 6% | no place for scratch files that needs no approval | open (G5): a per-session private `$TMPDIR` as an extra writable root, with its entry in `docs/SECURITY.md`; the prompt says to keep throwaway files in the repository meanwhile | |
| 5 | bench pilot | tool.unknown | `bash<\|channel\|>commentary`, `functions.read`: chat-template tokens in tool names | S2 | 1 to 3 per episode on some models | the provider adapter passes the name as the model wrote it | the agent repairs a name only when the repaired form is a registered tool; an unknown tool gets the list and the closest name | `TestRepairToolName`, `TestUnknownToolListsToolsAndSuggests` |
| 6 | bench core | reward | `hack:outside_worktree` on passing rollouts: the workspace was below `~/.sleipnir-bench` | S3 (data) | 1 in 10 episodes; 40% on one model | the detector cannot tell that from writes into the home's dotfiles and scoring never told it the workspace | scoring reads the workspace from the log; `rl reward --redetect-hacks` repairs old data | `TestScoringKnowsWhereTheWorkspaceIs`, `TestRLRewardRescoresWithTheWorkspaceTheLogRecorded` |
| 7 | bench core | reward | `hack:protected_edit` on a scratch `debug_test.go` the agent wrote to reproduce the bug | S3 (data) | 4 of 20 episodes on one model | a new file under a protected glob counted as tampering, although the verifier discards it | protected paths are judged by the final diff; a new file is scratch | `TestProtectedPathsAreJudgedByTheDiffWhenThereIsOne` |
| 8 | bench pilot | setup | rollouts exit 137 in setup | S3 | every rollout of a second run on the same work directory | two processes shared one setup marker and the first to finish swept the other's | the marker is unique per build | `TestTwoManagersBuildingTheSameSnapshotDoNotKillEachOthersSetup` |
| 9 | e2e tests | chat | one Ctrl-C ended the whole chat and exited 0 | S3 | every use | Ctrl-C was registered twice (`main` and the turn) and a signal goes to every channel that asked | one owner of Ctrl-C and of stdin (`cmd/sleipnir/chat_input.go`) | `internal/ptytest`, `cmd/sleipnir/e2e_chat_test.go` |
| 10 | bench core | prompt | the answer to a question was cut to six lines | S2 | every question | the finishing rule was for changes only | an answer to a question, a review or an explanation is given whole | `TestGoldenConstitution` (the text), the benchmark (the effect) |
| 11 | tests under load | test | `TestPermissionModesThroughTheAssembledSession` failed one run in eight | S2 | 1 in 8 at load 30 | the log flushes in the background, so a reader right after a turn saw a log without the turn's last events (and a crash then lost them) | the session flushes its log when a turn ends | the test itself, 0 of 25 |
| 12 | tests under load | test | `TestCacheEcon_BackgroundCommitStripsThinkingOfCarriedTurns` failed one run in four | S1 | 1 in 4 at load 40 | an instant mock finished the agent's twelve steps before the compactor's goroutine began | the mock agent waits for the compactor, as a real model's latency does | the test itself, 0 of 200 under `-race` |

| 13 | e2e tests | headless | `run --quiet` printed nothing | S2 | every `--quiet` run | the sink is `agent.NopSink` and the result's text was never written | the final answer is written to stdout | `TestRunOutput/quiet` |
| 14 | e2e tests | headless | `git diff \| sleipnir run "review this"` dropped the diff | S2 | every prompt with piped input | `run` read stdin only when there were no words | the goal is the words and the piped input (input first, in a block); a pipe that stays silent 3 s is left out with a note | `TestReadGoal*`, `TestRunTakesThePromptAndWhatIsPipedIn` |
| 15 | code reading | swarm | a swarm that stopped with work left exited 0 | S3 | every unfinished batch swarm | `afterManagerRun` only appended a line to the answer | `Swarm.Unfinished`, `Result.Unfinished`, status 3, `unfinished` in the JSON result | `TestHoldIsBoundedAndTheRunReportsWhatWasLeft`, `TestUnfinishedErrorIsStatusThree` |
| 16 | e2e tests | doctor | `doctor` exited 0 when every request of the probe failed | S3 | any dead endpoint | `probe.Run` returns no error for failed steps | `Report.Failure`: the plain request must have been answered | `TestDoctorOfAnEndpointThatIsDown`, `TestReportFailureIsAnEndpointThatAnsweredNothing` |
| 17 | e2e tests | models | `sleipnir models` printed a header and nothing for OpenAI-style catalogues | S2 | every catalogue without `architecture.modality` (OpenAI, Ollama, vLLM) | `IsChat` was false for an unknown modality | unknown counts as chat | `TestAModelWithoutAModalityIsListedAsChat`, `TestModelsOfACatalogueThatSaysNothingAboutModalities` |
| 18 | e2e tests | cli | `--cwd` that does not exist failed in whatever first used it | S2 | every typo | nothing checked the directory | `session.New` says it at once | `TestRunWithAWorkingDirectoryThatIsNotThere` |
| 19 | e2e tests | cli | Ctrl-C ended `run` with `sleipnir: context canceled` and status 1 | S2 | every Ctrl-C of a run | the library's error reached the terminal | `sleipnir: interrupted`, status 130 (143 for SIGTERM), a second Ctrl-C quits at once | `TestRunInterruptedSaysInterruptedAndExitsWithTheSignalsStatus`, `TestInterruptContext*` |
| 20 | code reading | tool | a failing command was shown as `✓` and never counted by the stuck guard | S2 | every failing command | `bash` does not set `IsError` (its output is the answer) and the sink and the guard keyed on it | `tools.Result.Failed`: sinks show `✗ ... (1.4s, exit 1)`, the guard counts a command that keeps failing (durations ignored), JSON carries `failed` and `exit_code` | `TestTextSinkMarksAFailedCommandAndSaysHowItEnded`, `TestAFailingCommandRepeatedIsALoopToo` |
| 21 | code reading | retry | a stream that failed part-way printed its first words twice; the last failure announced a retry and waited out the longest backoff | S2 | every mid-stream failure | `forward` dropped `EvReset`; the loop slept after the last attempt | `agent.Resetter` (a new line, a `reset` JSON event); notices count attempts; the last failure is final | `TestARetriedResponseTellsTheSinkItStartsOver`, `TestRetryNoticesCountTheAttemptsAndTheLastFailureIsFinal` |
| 22 | tests under load | test | keep-alive and idle-timeout tests, the parallel-hooks bound (4.5 s) and the log-tail latency bound (250 ms) failed at a load of 13 to 40 | S2 | about one run in five of the whole suite | wall-clock margins of 10x or less on machines that stall for seconds | margins of 30x, a rendezvous instead of a stopwatch, hang guards of a minute | the tests themselves |
| 23 | bench core (friction on 349 sessions) | friction | `file.reread` ranked third: 529 repeats in 87 sessions | S1 | 54% of 2401 reads were of a file read before | the miner counted per file, and a big file is read a window at a time: 110 of those 1152 reads were the same window | the count is per part of a file (path and range), starts over at an edit, and the example says which part | `TestReadingDifferentPartsOfAFileIsNotARepeat` |

Open: `run` against a dead endpoint retries for 62 seconds before it says anything final (the notices say what and when); after a
cancelled turn the kernel merges the unanswered goal and the next one into one user message without a separator
(`internal/kv/render.go`); the per-session scratch directory of row 4.

## Scenarios to run by hand in tmux

`send-keys` and `capture-pane` drive a real terminal; each has a pass criterion and is repeated on every release candidate.

| Scenario | Keys | Passes when |
|---|---|---|
| Ctrl-C in a turn | start a long task, `C-c` | the turn stops, the prompt comes back, the session continues |
| Ctrl-C twice at the prompt | `C-c C-c` within two seconds | the session ends with status `interrupted` |
| a paste of five lines | paste, `Enter` | one message of five lines, not five messages |
| an approval | a task that needs one; answer `a` | the same command is not asked again in this session |
| a two-minute test | `go test` on a slow package | the output streams; the turn is not declared stuck |
| `/compact` | after a long session | the thread folds, the next answer still knows the task |
| kill -9, then `--resume` | `kill -9` the process; `sleipnir chat --resume latest` | at most the last turn is lost |
