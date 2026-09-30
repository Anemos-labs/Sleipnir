# Tranche 2, engineer C: tools, events, process hardening

Scope: `internal/tools/**` (except `recall`), `internal/events`, `internal/harden` (new), `docs/SECURITY.md` (new) and one line of
`cmd/sleipnir/main.go`. Findings from `docs/reviews/security-robustness.md` (F7, F11, F18) and `docs/reviews/swarm-concurrency.md`
(C-13, C-19). Every repro named there is now an ordinary, ungated regression test; the only gated repro left in these packages is S46,
which is not part of this tranche.

```
go build ./...  &&  go vet ./...  &&  gofmt -l .      # clean
GOOS=darwin go vet ./internal/harden/... ./internal/tools/...   # compiles (also arm64)
GOOS=windows go vet ./internal/harden/... ./internal/tools/...  # compiles
go test -race -count=1 ./internal/harden/... ./internal/tools/... ./internal/events ./internal/checkpoint ./internal/memory
SLEIPNIR_REVIEW=1 HEIMDALL_API_KEY=sk-review-canary go test -count=1 -run 'TestSecReview|TestConc_' \
    ./internal/tools/... ./internal/events ./internal/checkpoint ./internal/memory     # only S46 fails
```

| Finding | Status | Where |
|---|---|---|
| S32 recall handles alias | fixed | `tools/support.go` |
| S40 key readable through `/proc/$PPID/environ` | fixed | `internal/harden`, `cmd/sleipnir/main.go`, `shell` |
| C-13 group commit has no timer | fixed | `events/log.go` |
| C-19 `Subscribe` after `Close` | fixed | `events/log.go` |
| S38 job tools bypass permissions | already fixed (`shell/jobs.go`); ungated; residual below | `shell/security_review_test.go` |
| S39 env scrub is a name heuristic | already fixed (names and values); ungated | `harden/secret.go` (moved from `shell/env.go`) |
| S50 quadratic glob matcher | already fixed (`fs/match.go` DP); renamed | `fs/security_review_test.go` |
| F18 terminal escapes | helper `tools.SanitizeForTerminal`; the sink must call it | `tools/terminal.go` |
| F18 no per-tool deadline | partly: FIFO opens can no longer block; `runOne` is not ours | `fs/fileio.go` |
| F18 every tool asks the engine | verified, nothing to change | see below |

## S32 and `TestConc_RecallHandles...`

**Changed.** `tools.Handles` (`support.go`): a handle is `out_` plus 16 hex characters (64 bits) of the blob hash. `Add` never lets a
different blob take an issued handle: when the id is held by another blob the newcomer's id grows by four hex characters until it is free
(`out_` + 20, 24, ... up to the whole hash; a numbered fallback that real hashes cannot reach keeps the guarantee for odd input). Adding the
same blob again returns the same handle; ids depend only on the hashes and the order of the calls, never on time or map order, so a replayed
session mints the same ones. `Resolve` is an exact lookup (whitespace around the handle is ignored; no prefix matching, so a truncated handle
is unknown rather than someone else's) and keeps working for every handle minted earlier in the session. `Add` no longer panics on hashes
shorter than 8 characters (it used to slice `Short()[:8]`).

**Tests.** `handles_test.go`: format, idempotence, forced collisions at 16, 20, 24 and 28 shared digits (each newcomer gets the next free
width, the owner keeps its handle), determinism, 500 blobs ground onto one prefix, 20k random blobs, odd and prefix-of-each-other hashes, a
`-race` run of 16 workers adding overlapping colliding blobs, and end to end through `Env.Finish`. `TestSec_S32_DistinctBlobsNeverShareAHandle`
(was `TestSecReview_S32_...`) now asserts both handles resolve to their own blobs, not only "if equal".
Mutation check: removing the collision check fails 5 of these tests.

**Edit to a repro, with the reason** (`support_review_test.go`, renamed `TestConc_RecallHandlesDoNotAliasOn32BitPrefixCollisions`): the
original ended with `if ida != idb { t.Fatalf("setup: handles differ") }`, a precondition that only holds for the vulnerable 32-bit format. With
64-bit handles the two real blobs found by the birthday search no longer share a handle, so the precondition failed *because of the fix*. It
is replaced by the assertion that matters (both handles resolve to their own blob), applied to both; nothing was weakened, and the comment on
the test says so. The 64-bit collision itself cannot be found by search and is covered deterministically in `handles_test.go`.

**Residual.** The table grows by roughly 150 bytes per truncated output for the life of the session (a handle must keep resolving; there is no
GC). The recall tool's schema (`tools/recall`, not ours) still gives `out_ab12cd34` as its example; the real handles are 8 characters longer.
Changing that text is a G0 event and belongs with whoever owns recall.

## S40 / F7: `internal/harden`

**Changed.**

* New package `internal/harden`, build-tagged per OS. `harden.Process()` is the first statement of `main` (a test reads the AST of
  `cmd/sleipnir/main.go` and fails if it is not). It never fails: `Status` says what was and was not done.
  * Linux: erases the *values* of credential-looking variables from the kernel's copy of the initial environment (the bytes
    `/proc/<pid>/environ` serves, which `os.Unsetenv` never touches) by writing through `/proc/self/mem`, after checking that what it is about
    to change is exactly what the kernel serves as `environ` and reading it back afterwards; then `prctl(PR_SET_DUMPABLE, 0)`. The erasure is what
    protects against root and `CAP_SYS_PTRACE`, which the flag does not stop; the flag is what stops an unprivileged same-user process from reading
    `/proc/<pid>/mem` and from ptracing. Go's own environment and every child are unaffected (measured: a hardened unprivileged process can still use
    `/proc/self/{stat,status,maps,cgroup,limits,fd,exe,ns/pid}` and `os.Executable`; only `environ` and `mem` are closed to it; children are
    dumpable again after `execve`).
  * macOS: `ptrace(PT_DENY_ATTACH)`. Compiled (amd64 and arm64), never run here; it exits a process that is already being traced.
  * Other systems: nothing, and `Status.Notes` says so.
  * `SLEIPNIR_DUMPABLE=1` (also `true`, `yes`, `on`) skips the flag for debugging; the erasure still happens.
* `harden.LooksSecret` is the scrub heuristic that used to live in `shell/env.go` (moved, unchanged: `shell.looksSecret` delegates, all its tests
  pass as they were); the erasure uses a wider net (`key|token|secret|passw|credential|auth|cookie|bearer|dsn|signature|_pat|_pwd` in the name, or
  a credential-shaped value), because a false positive there costs only a blank in `/proc`.
* `harden.MoveKeys` (an option to `Process`), `harden.Secret`, `LookupSecret`, `Held`: the "read once, unset, hold" half. Provider API keys
  (`*API_KEY`, and any names passed to `MoveKeys`) leave the process environment, so nothing started with an inherited environment (git and its
  hooks or fsmonitor, verifiers, project hooks, MCP servers) receives them; readers call `harden.Secret(name)`, which is `os.Getenv` until moving is
  on. After that, `Secret` also moves a credential-looking variable on its first read (a custom provider's key variable), and never moves an
  ordinary name. `shell.Manager` puts back held credentials that `PassEnv` lists, so moving keys does not take away what `PassEnv` promised.
  `web.BackendFromEnv` reads the Brave and Tavily keys through `Secret`.
* `shell.HardenProcess` (an earlier, Linux-only `prctl` call) is removed with its tests; `internal/harden` supersedes it and its tests moved.
* `docs/SECURITY.md`: threat model on one page, what is scrubbed and hardened per OS, sandboxing with `Options.Wrap`, the macOS and Windows limits.

**Why `MoveKeys` is off by default (needs the integrator).** With it on, a variable that was moved is invisible to `os.Getenv`. The provider
key is still read with `os.Getenv` in files this tranche may not edit: `cmd/sleipnir/provider.go` (`resolve`: the three autodetect lookups and
`os.Getenv(spec.keyEnv)`), `cmd/sleipnir/setup.go` (three lookups) and `internal/config/config.go` (`Provider.APIKey`). Switching those to
`harden.Secret` and changing the call in `main.go` to `harden.Process(harden.MoveKeys())` finishes the job; until then keys stay in the
inherited environment of the harness's own children (the shell tool scrubs them for commands, git/hooks/MCP do not). Nothing breaks in the
meantime: `Secret` is `os.Getenv` when moving is off.

**Tests.** `internal/harden`: erasure, layout and `MoveKeys` in a real child process (a copy of the test binary, run as uid 65534 when the tests
run as root, because root reads everything whatever the flag says); the control run proves the leak is visible without `Process`; the
unprivileged child gets `Permission denied` after it; opt-out; idempotence; a process with an empty environment; the AST wiring test; unit
tests of the stat parser (command names with spaces and `)`, missing or absurd ranges), of the erasure spans (unterminated entries, empty values,
NULs, non-UTF-8, duplicates) and of the vault (rotation, concurrency, ordinary names). `shell`: `TestSec_S40_...` runs the original repro (same
command, same assertion, same canary `sk-review-canary`, which is never a real key) in a child harness that calls `harden.Process()` first, with a
control child that does not; `TestMovedKeysReachCommandsOnlyThroughPassEnv`; `TestWrapInAPIDNamespaceHidesTheHarnessFromCommands` runs the
document's `unshare` example for real. Also checked by hand with the built binary (`sleipnir mock`, key in the environment, unprivileged uid):
root reads `HEIMDALL_API_KEY=` (blank), the same uid gets `Permission denied`, and `SLEIPNIR_DUMPABLE=1` shows the file with the key still blank.
Mutation checks: no `prctl` fails the unprivileged-child test, no erasure fails 3 harden tests and `TestSec_S40_...` (as root).

**Edit to a repro, with the reason.** `TestSecReview_S40_...` required `HEIMDALL_API_KEY=sk-review-canary` in the test binary's own environment
and read the test process's `/proc`. Ungated, that would have skipped in every normal run, and the test binary is not `main` (nothing calls
`harden.Process` in it). The assertion is unchanged (`strings.Contains(leak.Text, key)` must be false for `tr '\0' '\n' < /proc/$PPID/environ | grep
HEIMDALL_API_KEY` run by the shell tool); the harness is now a child copy of the test binary started with the canary in its environment, exactly
as a user's shell would start `sleipnir`, so it runs everywhere `/proc` exists. The old command line (`-run TestSecReview_S40`) no longer matches.

**Residual risk.** Root and `CAP_SYS_PTRACE` still read the harness's heap through `/proc/<pid>/mem` (the key is in memory while in use); the
environment of the shell that launched the harness and other same-user files stay readable; `/proc/<pid>/cmdline` is world-readable, so keys
must not be flags; macOS and Windows keep the initial environment readable (`ps eww`, PEB) and nothing in-process changes that; on macOS
`PT_DENY_ATTACH` is untested at run time. The real answer for an untrusted repository is a PID namespace or a container (SECURITY.md section 3).

## C-13 and C-19 (`events/log.go`)

**Changed.** Group commit now has a timer: the first event after a quiet period is flushed at once, later events inside the 5 ms window mark
the buffer dirty and arm one `time.AfterFunc`, whose callback flushes the tail. The cadence is measured on the monotonic clock instead of event
timestamps (which come from an injectable clock and from the wall clock, which can step back). `Flush` and `Close` still flush (and fsync)
everything; `Close` stops the timer, and a callback that was already waiting sees the log closed. A failed timer flush is not lost: bufio's error is
sticky, so the next `Emit`, `Flush` and `Close` report it. `Subscribe` on a closed log returns a channel that is already closed and a `cancel` that
does nothing (it used to return one nobody would ever close); a subscriber that was open when `Close` ran still sees its channel closed.

**Tests.** The two repros (ungated, renamed `TestConc_GroupCommitFlushesTheBurstTailInQuietPeriods`, `TestConc_SubscribeAfterCloseNeverDelivers`)
plus: the tail is out within 250 ms in five rounds (measured about 7 ms for a 5 ms window), an isolated event is on disk before `Emit` returns (no
added latency), a burst of 20,000 events (about 31 ms) costs about 36 `write` calls (a counting writer; flush-per-event fails it), `Flush` and `Close` right after
a burst write everything, `Close` stops the timer (20 rounds, `-race`), a timer flush error surfaces, late subscribers with every buffer size,
`Subscribe` racing `Close`. Mutation checks: no timer fails 3 tests, flush-per-event fails the grouping test. The existing stress test
(`TestConcSound_LogCloseRacesEmitAndSubscribeStress`) runs with the timer.

**Residual.** A crash can still lose events of the last ~5 ms (one flush window, in the OS page cache after it; `Flush` is the only fsync, so a
power loss can lose more: the log does not fsync on `agent.end`/`session.end`, which C-13 suggested). Dropped-event counts of slow subscribers are
still not exposed.

## S38, S39, S50 (verified, ungated)

They passed at the start of this tranche (fixed earlier: `Manager.authorize` in `jobs.go`, name-plus-value scrubbing, a DP matcher) and were only
gated. Now `TestSec_S38_JobToolsAreGatedByPermissionsAcrossAgents` (also checks the owner is not asked for its own job),
`TestSec_S39_EnvScrubCoversCredentialNamesAndValues` (also checks that `PATH`, `HOME`, `GOPATH`, `KEYBOARD`, `PWD` still reach commands and that
`PassEnv` works) and `TestSec_S50_GlobMatcherCostIsLinearInPathDepth`. The fixes already have thorough tests (`jobs_test.go`,
`unit_test.go`, `match_test.go`).

**Residual for S38, measured with the real engine.** The repro's requester denies everything, and the tool consults it. `perm.Engine` itself
answers `bash_output` with "touches no files and no network: allow" in every mode and for every role, and `bash_kill` with ask (default) or
deny (plan and read-only roles). So in production a read-only reviewer can still read another agent's job output unless its role profile says
otherwise. `RoleProfile{Deny: []string{"bash_output"}}` works (checked); the read-only role profiles in `swarm` should carry it. The swarm design
does not rely on cross-agent job reads, so the safest default would be "own jobs only"; that changes tested behaviour (`TestJobsAreSessionWide`)
and is left to the integrator.

## F18, in `fs`, `web`, `shell`

Done:

* `tools.SanitizeForTerminal` (`tools/terminal.go`): what a UI must call before printing file, web or listing text to a TTY. It removes escape
  sequences (CSI, OSC including clipboard and hyperlinks, DCS, APC, PM, SOS, charset selection; an unterminated one loses only its introducer),
  C1 and C0 controls, DEL, bidi and other invisible characters, turns CR and Unicode line separators into `\n`, replaces invalid UTF-8; clean text
  comes back unchanged without copying. Tested with a table, a fuzz target (no ESC ever survives; idempotent), 4 MB hostile inputs (linear) and an
  allocation check. `tools.Invisible` is the one list of invisible characters (`shell` and `web` now call it instead of their own copies). The session
  sink, `sleipnir models` (catalogue ids) and `doctor` (provider error text) should call the helper; none of them is in this tranche's files.
* FIFO-safe opens in `fs`: `read`, `grep` and `.gitignore` loading opened paths with `os.Open` after a `Stat`. The model has a shell, so a path can
  become a FIFO in between (or be one to begin with: a `.gitignore`), and `os.Open` then waits for ever with nothing able to cancel it. Found by
  test: `glob` over a tree with a FIFO called `.gitignore` hung. All opens now use `O_NONBLOCK` and check the descriptor (`openRegular`); the in-place
  write fallback no longer truncates before it knows what it opened. Tests fail (hang, caught by a deadline) with the flag removed.
* Every tool asks the engine (checked, "make sure every tool asks it"): `read`, `write`, `edit`, `apply_patch`, `glob`, `grep`, `ls`, `web_fetch`,
  `web_search`, `bash`, and `bash_output`/`bash_kill` for jobs of other agents. `recall` reads the calling agent's own archive and `skill` a catalogue
  loaded at session start; neither takes a path from the model.

Skipped, and why:

* A default deadline in `agent.runOne` (package `agent`, not ours). What our tools do have: glob/ls/grep are bounded by `Limits.DefaultTimeout`
  (2 min), ripgrep by 60 s (then the Go engine takes over), web fetch and search by their own timeouts, bash by 2 to 10 minutes. `wait` (swarm) is a
  read-only tool that legitimately blocks up to 10 minutes and MCP tools are unknown, so a blanket deadline in `tools.Registry` would be wrong.
* ripgrep itself still waits on a FIFO named `.gitignore` until its 60 s timeout; the Go engine that then runs does not.
* `read` does not strip tag characters or bidi controls from file text: an edit must match the file's real bytes, and web text is already cleaned.
* `unknownJob` lists the ids of jobs of other agents ("known jobs: ..."); ids are sequential and guessable anyway.

## Cosmetic

`TestSecReview_S##_*` without a gate any more were renamed `TestSec_S##_*`: S33 to S36 (`internal/events`), S37 (`internal/checkpoint`), S41 to S43
(`internal/memory`), plus S32, S38, S39, S40 and S50 above. The unused `secRevGate` helpers in `events` and `memory` are gone. `docs/reviews/security-robustness.md`
still uses the old names (it is a record; not edited).

## Declared cache events

None. No tool schema, description, constitution or layer text changed, and nothing that reaches a prompt carries a timestamp, an id or map order.
One model-visible string changes, in tool *results* only (volatile thread text, not a stable layer): recall handles are `out_` plus 16 hex
characters instead of 8, in the "[full output saved as ...]" line of newly truncated outputs.

## Needs from others

* Integrator: switch the provider-key reads (`cmd/sleipnir/provider.go`, `setup.go`, `internal/config/config.go`) to `harden.Secret`, then call
  `harden.Process(harden.MoveKeys())`; add `Deny: bash_output` (and `bash_kill`) to the read-only role profiles; point `docs/ARCHITECTURE.md`
  ("Security posture") and the README documentation list at `docs/SECURITY.md`.
* Session/UI owners: print file, web and listing text through `tools.SanitizeForTerminal`.
* Agent owner: a default per-call deadline in `runOne` that skips `wait`.
* Recall owner: the schema example `out_ab12cd34` (G0) when convenient.

## Integration follow-ups

* **Provider keys** are held out of the environment: `cmd/sleipnir` calls `harden.Process(harden.MoveKeys())` and every reader (provider resolution, `config.Provider.APIKey`, the
  RL token, the inspector token, MCP `${VAR}` expansion) uses `harden.Secret`. `TestReadersOfCredentialsUseSecret` (source level) fails on a new `os.Getenv` of a credential;
  `TestKeysLeaveTheEnvironmentButStillReachTheirReaders` runs the real paths in a child that did what `main` does.
* **S46.** `perm.DenyAll` is what `agent.New`, swarm members and `tools.Env.Defaults` use when they are given no `Requester`; `TestSec_S46_*` is an ordinary test now.
* **Per-call deadline.** `agent.Config.ToolTimeout` (default 30 minutes, above the longest wait a built-in tool allows itself, negative for none) stops a tool that would hold the
  agent for ever; the model is told the call ran too long (`error_kind: timeout`, a `tool.timeout` event). A tool that finishes late keeps its result.
* **Still open from this list:** the read-only role profiles do not deny `bash_output`/`bash_kill` by name (the engine asks about other agents' jobs).
* The recall schema example (`out_ab12cd34`) is left alone: changing it is a change to the bytes of G0, to be made together with the next one that is needed anyway.
* `docs/ARCHITECTURE.md` ("Security posture") and the README documentation list point at `docs/SECURITY.md`.
