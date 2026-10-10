# TEST-PLAN: proving that `sleipnir web` is the mock, made real

Binding for every builder (their own package's tests) and for D (integration, parity, security review). Tests never call a real model
or the network: they use `internal/provider/mock`, `sleipnir demo` scenarios, `internal/tui/state/statetest` fixtures and the e2e
world helper (`cmd/sleipnir/e2e_test.go` `newWorld`, `startModel`). `docs/TESTING.md` says what each layer guards; D adds the web rows.

Contents: 1 Layers · 2 Unit tests per package · 3 The deterministic backend (`--fixture`) · 4 End-to-end · 5 Security tests ·
6 Leak tests · 7 Parity with the mock · 8 Behaviour suite ported from the mock · 9 Manual and visual QA · 10 Performance and bounds ·
11 Repository checks · 12 Documentation to update

---

## 1. Layers

| Layer | Guards | Runs in |
|---|---|---|
| Go unit (per package) | contracts of each service, the translator's mapping, the security gate, the harness extensions | `go test -race -count=1 ./...` |
| Go e2e (`cmd/sleipnir`) | the real binary: launch link, cookie, stream, a session driven to a question and an answer, restart, close, exit codes | `go test ./cmd/sleipnir -run Web` |
| Asset tests (`internal/web/ui_test.go`) | CSP-clean files, no remote URLs, module list resolves, fonts and licences, no sample data shipped | `go test ./internal/web` |
| Browser behaviour suite (`scripts/web-parity.mjs` and the ported `t-*.mjs`) | the page against the fixture backend: the mock's own checks | Node + headless Chromium, manual and pre-merge (not in CI: it needs a browser) |
| Parity screenshots | the look, screen by screen, against the mock | as above; images are read by a person (AGENTS.md "look at what you make") |

## 2. Unit tests per package

### 2.1 `internal/web` (A2)

Committed in 08c4482 and kept green: the envelope (`TestSecurityHeadersOnEveryResponse`, `TestHostHeaderBlocksDNSRebinding`,
`TestHostOnOtherPortsAndAddresses`, `TestCrossSiteRequestForgeryMatrix`, `TestFetchMetadataOnReads`, `TestBodyLimitAndStrictDecoding`,
`TestRequestsInFlightAreCapped`, `TestLogsNeverHoldCredentialsOrBodies`, `TestHeldProviderKeysAreMaskedInLogs`, the panic tests), the
authority (`TestTokenIsRandomAndLongEnough`, `TestTheTokenCannotBeChosenFromOutside`, `TestTokenMatrix`, `TestBrowserSignInPage`,
`TestTheURLTokenBecomesACookieAndLeavesTheAddressBar`, `TestTokenInTheURLIsAcceptedOnTheRootOnly`, `TestTokenRedirectNeverLeavesThisOrigin`,
`TestSessionsExpireAndAreBounded`, `TestFailedTokensAreThrottled`, `TestLogoutEndsTheSession`, `TestRotateEndsEveryOtherSessionAndTheOldToken`,
`TestConfirmationFlow`), the hub (`TestResumeReportsAGapWhenTheIDHasAgedOut`, `TestCoalescableEventsAreSupersededByKey`,
`TestCriticalEventsAreNeverDropped`, `TestAClientThatCannotKeepUpWithCriticalEventsIsToldAndReconnects`, `TestEveryWriteGetsAFreshDeadline`,
...), the server and the assets (`TestTheEmbeddedUIWorksUnderTheContentSecurityPolicy`, `TestTheDevelopmentServerSendsTheSamePolicy`),
with `testutil.CheckLeaks` in `TestMain`. Added for the deltas and this contract:

| Test | Expectation |
|---|---|
| `TestOpenUsesASingleUseLaunchCode` (Δ1) | the URL given to the opener carries a code that is accepted once within 30 s by `?token=` on `/`, then refused (and counted for the throttle); the run token never appears in it |
| `TestErrorDetail` (Δ2) | the body is `{"error", "code", "detail"}`, uncacheable, the message cut at 512 bytes |
| `TestPageTopicSizes` (Δ3) | with the `sleipnir web` hub configuration, 20,000 small events resume from the first id without a `gap` |
| `TestEveryContractRouteIsRegistered` (D, after integration) | every route of CONTRACT.md exists with its method, its body rule and its confirmation scope (a table in the test mirrors the contract) |

`internal/web/wire`: golden JSON of one instance of every UI event kind and frame (`testdata/wire.golden.json`), `KindOf` covers every
type, `Stamp` sets `k`/`t`/`seq`, an empty `MetaPatch.Rules` pointer encodes `"rules":[]`. The golden file pins the field names; the
page's reducer test (a Node script run by C1 against `30-model.js`) feeds the same file through `reduce` and checks that every field
VOCAB.md lists as read is read.

### 2.2 `internal/web/translate` (B2)

| Test | Expectation |
|---|---|
| `TestGoldenDemoShop` | `statetest.DemoLogFile(t)` (and `sleipnir demo --scenario shop` logs) translated to UI events equal `testdata/demo-shop.ui.jsonl` (reviewed once, then a golden); every event has a known kind and the fields VOCAB.md lists |
| `TestSinkMapping` | scripted sink calls (text deltas, tool start/end of each tool name in VOCAB 8.2, response, notice, reset) produce `say`/`more`/`state`/`tool`/`stream`/`diff`/`sys` exactly; `more` coalesces to ≤ 1 per 100 ms per message; `end` on response, tool start, reset (`reset: true`), turn end |
| `TestSinkNeverBlocks` | a stalled publisher: 100,000 sink calls return promptly (hang guard); overflow produces the `⚠ the page fell behind` row and a fresh `use`/`state`/`layers` set |
| `TestStateMapping` | each `state.Status` maps per VOCAB 8.3; the manager of a team between turns shows `wait … (T…)` |
| `TestTaskMapping` | board ops → `task` with columns per 8.4; closures carried; handover → `sys` + owner change |
| `TestQueueCounters` | `merge.conflict`, `merge.verify_failed`, `merge.rejected` increment `conflicts`/`bounced` |
| `TestJournalBoundsAndKeyframe` | with small limits, after eviction: a Go reduction of `keyframe ⧺ events` equals the reduction of the full stream for every non-transcript field (tokens, states, tasks, plan, goal, ckpts, open questions, counters); open questions are never evicted |
| `TestHistoryOfResumedSession` | a log with two runs: history events at `t: 0` with `at`, a `↺ resumed` row, keyframe counters equal a full fold |
| `TestSeqGapRescan` | dropping events from the subscription is detected and repaired by re-scanning the file |
| `TestTimeAndOrdering` | `t` non-decreasing in `seq`; events before `startedAt` clamp to 0 |
| `TestNoUntrustedMarkup` | tool output, mail, file names with `<script>`, ANSI, bidi controls and secret-shaped tokens come out sanitized and masked (`redact`) |

`internal/tui/state` (B2): handler tests for `swarm.stall`, `swarm.handover`, `goal.state`, `goal.judge`, `checkpoint`, closures
(golden snapshot updates), `OnStatus` called once per change, `LayerSplit` equals the terminal's stack bar values on the existing
`cacheview` test data.

### 2.3 `internal/web/approvals` and the host (B1)

| Test | Expectation |
|---|---|
| `TestQuestionIDs` | 10,000 ids unique, 26 base32 chars after `q_`, from `crypto/rand` (no seed path) |
| `TestAnswerOnceAndFloor` | answer at +100 ms → `too_soon` with `retryAfterMs`; at +360 ms ok; second answer → `answered`; the floor re-arms after each answer of the tab |
| `TestDecisionTable` | choices 1/2/3 produce the Decisions of VOCAB 9 (choice 2 → `ScopeSession`, trust/mcp kind → `ScopeProject`; 3 with a note → `perm.DeclinedWith(note)`) |
| `TestInterruptRefusesMainQuestionsOnly` | interrupt refuses the manager's open questions (`by: "canceled"`), keeps workers' |
| `TestGraceRefusal` | no stream for `--ask-grace` (fake clock) → open questions refused `by: "nobody"` |
| `TestStartRefusesTrustAndMCPQuestions` | during `session.New` the prompter answers `ToolProjectTrust`/`ToolMCPServer` with no without emitting `ask` |
| `TestHostCreateCloseResume` | with `internal/provider/mock`: create two tabs in different temp projects, run a turn in each, close one (flock released: a second `session.New` on its dir succeeds), resume it in a new tab (same sid, history present) |
| `TestRestartKinds` | `new`/`clear`/`swarm` give a new generation and sid with an empty journal; `restart`/`model`/`roles` keep the sid and carry history; a single agent's model change does not restart |
| `TestQueueAndGoalLoop` | messages during a turn are queued in order (`meta.queued`), delivered after the turn and after the goal's last continuation; the mock judge's `continue`/`done`/`blocked` drive `goal` and `verdict` events and the `goal.judge` log event |
| `TestTrustStep` | an untrusted directory with `trustProject` → 409 with a challenge; repeating with the token records the ledger entry and starts trusted; a changed file between challenge and repeat → 409 again |
| `TestCwdAllowlist` | a directory not in `/api/projects` → 403 `not_a_project` |
| `TestShutdown` | Ctrl-C closes every tab within the limit, isolation finish included, exit 0 |
| `TestChatUnchanged` | after the extraction of the chat-flag parser and goal loop, the existing `cmd/sleipnir` chat tests pass unmodified |

### 2.4 Workspace (B3)

| Test | Expectation |
|---|---|
| `TestContentAt` | for a scripted sequence of writes across checkpoints, `ContentAt(id, path)` returns the state when each began; created files do not exist before; deleted ones do after |
| `TestChangesPerCheckpoint` | per-checkpoint change sets, statuses, `+/-` and agents |
| `TestBlameExactAndInexact` | only tool writes → `exact: true` and the right agent per line; a shell edit in between → `exact: false`, unexplained lines `-`; isolated team → `git blame` result |
| `TestRevertHunkAndUndo` | reverting a hunk restores those lines only; the file changed since the diff → 409 `changed`; undo reapplies; both go through a checkpoint (`/rewind` sees them) |
| `TestRestorePreviewApplyUndo` | dry run lists outcomes without writing; apply takes the undo capture first; undo puts files back; conflicts are reported, never forced; refused while a turn runs |
| `TestSafePaths` | `..`, absolute paths, a symlink out of the root, `.git/config`, the session directory, a FIFO, `/dev/zero` symlink → refused; a denied path (`.env`) → 403 with no content; a binary file → `binary: true`; > 2 MiB → truncated |
| `TestClassifyEqualsCheck` | for a corpus of requests (the perm package's existing test tables), `Engine.Classify` returns the verdict that `Engine.Check` reaches with a recording prompter, and never calls the prompter |
| `TestRememberRulesAndWhy` | a question's `Request.Why` and `RememberRules` are set as the TUI dialog would phrase them |
| `TestAcceptVerified` | clean checkout + unmoved branch → path-limited commit of the merged files; dirty → 409; moved → 409 |

### 2.5 Settings, runner, clispec (B4)

| Test | Expectation |
|---|---|
| `TestNoSecretInSettingsDTOs` | a configuration with canary secrets in provider headers, hook headers, MCP env, MCP args, URL userinfo and query, and auth.json: no canary appears in any Settings response (fuzz the value shapes with `harden.LooksSecret`'s patterns) |
| `TestProvidersKeySource` | env-moved key → `env`; stored → `stored`; both → `env` wins (the precedence settings.md hazard 2 asks to verify; fix if wrong) |
| `TestSignOutDropsHeldKey` | after sign-out, `harden.Secret(env)` is empty |
| `TestCatalogueOffline` | `httptest` gateways; unknown prices stay `null`; favourites round-trip through `config.Save` |
| `TestLedgerWritesSerialized` | concurrent trust writes from two goroutines lose nothing |
| `TestMCPApprovalReread` | an approval written by the CLI while a session holds the store is not overwritten |
| `TestSchedule` | add/edit/pause/remove; the lock refuses a second daemon; run-now streams output and records the exit; bypass/yolo refused |
| `TestPrunePlanEqualsCLI` | `PlanPrune` and `sleipnir sessions prune` list the same ids for a synthetic state directory |
| `TestRunnerModes` | every spec command has a mode; `tty_only` refused with its message; `priv` without a token → 403; `server` commands stop on cancel |
| `TestRunnerKillsProcessGroup` | cancel kills the child and its children (a command that spawns `sleep`); no orphan after the test |
| `TestRunnerNoKeyInArgv` | for `net` commands the key travels only in the child's environment (the `jobEnv` pattern) and never in argv |
| `TestCLISpecDrift` | regenerating in memory equals the checked-in `clispec.json`; every command of `sleipnir --help` has an entry |

## 3. The deterministic backend: `sleipnir web --fixture NAME` (B1)

A hidden flag (not in `-h`, documented in docs/TESTING.md) that hosts tabs whose provider is `internal/provider/mock` with scripted
responders, in temp projects copied from `internal/demo` assets, so the real harness runs end to end with a predictable model:

| Fixture | Reproduces the mock's | Script (mock provider) |
|---|---|---|
| `shop` | `shop` session: manager + 8 workers, goal with a six-step plan, scouts T1..T3, builders T4..T8 with leases, a question from `fe-1` (`npm install --save-dev vitest`), a cache break on `be-2` (the mock provider reports `cache_read: 0` once), a compaction of `be-1`, mail between workers, merges through the queue (`--isolation worktree --verify "go test {dirs}"`) | `internal/demo` shop scenario extended with the question and the break |
| `orders` | `orders-api`: single agent, accept-edits, one turn that edits two files and runs `go test`, ends | mock responder |
| `docs` | `docs-sweep`: headless-like tab with `--ask-timeout`, two docs workers, a refused command (nobody can answer) | mock responder with a command that needs approval and an ask timeout |

`--fixture all` opens the three as tabs (the mock's three live sessions). The flag refuses to run without `--addr 127.0.0.1:0` or a
loopback address and uses a temp state directory. Timing is real; tests and screenshots wait for state predicates (an open question,
`m.merged.length === 5`), never for durations.

## 4. End-to-end (`cmd/sleipnir/e2e_web_test.go`, B1 + D)

Template: `TestDemoAndInspect` (`cmd/sleipnir/e2e_commands_test.go:369-486`).

1. `sleipnir web --addr 127.0.0.1:0 --fixture shop` in a `newWorld`; the first stdout line matches the committed
   `TestURLCarriesTheTokenOnceAndNamesTheBoundPort` form (`http://127.0.0.1:PORT/?token=...`).
2. GET the link → redirect + `sleipnir_web` cookie; `/api/hello` with the cookie → one tab and `streamAfter`; without → 401.
3. Open `/api/stream?after=<streamAfter>`; GET the snapshot; wait for an `ev` frame with an `ask` for `fe-1`.
4. Answer it at once → 409 `too_soon`; after 400 ms → ok; observe `answer` and the worker's next `tool` frame.
5. POST a message; observe `say you`, `turn start`, `say mgr` + `more`, `final`.
6. Restart `swarm` with 2 workers → `reset`, new snapshot with three roster entries.
7. Close the tab → 409 `last`; create a second (orders fixture project) and close the first → ok.
8. SIGINT → exit 0 within the hang guard; stderr has the banner; the state directory holds no token, cookie or code (grep).
9. `TestCommandExitStatus` rows: `web --addr 0.0.0.0:0` → 2 (usage: not loopback), `web --addr 127.0.0.1:<busy>` → 1 with the
   `--addr 127.0.0.1:0` hint, `web --bogus` → 2. `e2e_width_test.go` covers `web -h` at 80 columns.

## 5. Security tests (D reviews; each has an owner test)

| Id | Attack | Expected | Owner test |
|---|---|---|---|
| S-01 | DNS rebinding (`Host: attacker.test`), another port | 403 `bad_host` | A2 `TestHostHeaderBlocksDNSRebinding`, `TestHostOnOtherPortsAndAddresses` (committed) |
| S-02 | Cross-site form POST (`text/plain`, no custom header) | 403 | A2 `TestCrossSiteRequestForgeryMatrix` (committed) |
| S-03 | Cross-origin `fetch` with `Content-Type: application/json` | preflight not answered; the request never reaches a handler (403 if it does) | A2 |
| S-04 | Another localhost port's page (`Origin: http://127.0.0.1:7000`) | 403 `forbidden_origin` | A2 (committed matrix) |
| S-05 | Guessing the token / brute force | throttled after 10 a minute; 256-bit space | A2 `TestFailedTokensAreThrottled` (committed) |
| S-06 | Replaying `--open`'s launch code | refused after its first use (Δ1) | A2 `TestOpenUsesASingleUseLaunchCode` |
| S-07 | Stealing the cookie via script | HttpOnly (asserted on `Set-Cookie`) | A2 (committed) |
| S-08 | XSS through tool output, file names, mail, MCP text, model text, session names | rendered as text (port `t-safe.mjs`: no `img`/`script` created, `window.__pwn` stays 0) | C1/C2/C3 |
| S-09 | Path traversal in `ws/file`, `ws/diff`, `complete` | refused | B3 `TestSafePaths` |
| S-10 | Privilege escalation without confirmation (bypass mode, trust add, MCP approve, schedule add, key save, update, priv runner commands) | 403 `confirm` | A2 + B1/B3/B4 tables |
| S-11 | Answering a question too fast / twice / of another page | `too_soon` / `answered`; ids unguessable | B1 |
| S-12 | The agent curls the server | inside a fixture session, a mock-scripted `bash` tool call `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:PORT/api/hello` answers 401; the child's environment, argv and the files of the project and session directory contain no launch code, cookie, run secret or token (scan) | B1 e2e |
| S-13 | Secrets in responses or logs | canaries never appear (A2 `TestNoSecretsInLogs`, B4 `TestNoSecretInSettingsDTOs`, B2 masking test) | A2/B2/B4 |
| S-14 | Body bombs, deep JSON, huge headers | 413 / 400 / 431 | A2 |
| S-15 | Slow-loris on the stream | write deadlines drop it; other clients fine | A2 |
| S-16 | CSP regressions | asset test fails on any inline script/style element/handler, `eval`, remote URL | A3 |
| S-17 | `--open` race | documented residual risk; code valid 30 s | A2 (doc) |
| S-18 | A repository's `.sleipnir/config.json` trying to pre-trust itself or pre-approve MCP | ledger and approvals live in the state directory; project config cannot set them (existing tests + a web e2e: a project with a `.mcp.json` shows "needs approval") | B4 |

## 6. Leak tests

* Go: `internal/web` and its subpackages call `testutil.CheckLeaks` in `TestMain` (as `internal/swarm` does); tests open and close
  streams, cancel runs, close tabs, and the check finds no goroutine left.
* Runner: after each test, no child process of the test binary remains (`/proc` scan on Linux).
* Page: `hooks-real.js` exposes the mock's instrumentation (`counts()`); the ported `t-lifecycle.mjs` (30 view switches, 50 session
  switches while questions arrive, each view mounted 10 times) asserts timers/listeners/observers/animations/bus/scopes equal before
  and after, and detached nodes are collected, against the fixture backend.
* Stream: open and close 200 streams; the hub's client count returns to 0; memory stable (pprof heap in a test, bound 5 MiB growth).

## 7. Parity with the mock

**Goal**: every screen of the mock and the same screen of the real page look the same: same structure, same styles, same states; only
the values differ (real numbers, real names, real times).

**Setup.** The committed tool `scripts/web-parity.mjs` (A3, db38f7d) with `scripts/web-parity-scenes.json`: page A is the approved
mock (`docs/design/web-mocks/v3/sleipnir-web.html`, fonts answered locally), page B the page under test. Until the wiring lands, B is
the packaged UI (`internal/web/ui` through `scripts/web-ui-dev.mjs`) and every scene must be pixel-identical (threshold 0). Once wired,
B is `sleipnir web --addr 127.0.0.1:0 --fixture all` (its launch URL) and the tool runs with `--profile live`: the mask profile
`maskProfiles.live` hides live clocks and times, scene steps reach states with `until` predicates and `sleep` (the virtual clock
drives the mock only), and the default threshold is 0.5% of differing pixels outside the masks. Scenes to cover, beyond the committed
ones:

| Scene | Mock state | Real state (predicate) | Viewports |
|---|---|---|---|
| P-01 cockpit | shop at 00:38 | shop tab: an open question from `fe-1` and ≥ 5 workers spawned | 1440×900, 1024×700, 1920×1080, 390×844 |
| P-02 cache break flash | until `m.anomalies.length` | `m.anomalies.length > 0` | 1440×900 |
| P-03 question answered | answer 2, run 9 s | answer 2, wait for the next `tool` of `fe-1` | 1440×900 |
| P-04 hold pinned while events pile up | pin, run 50 s | pin, wait until `S.unseen() ≥ 5` | 1440×900 |
| P-05 digest row | pin 40 s, release | pin until `S.wt − S.vt > 30`, release | 1440×900 |
| P-06 every view | cache, mail, board, replay, sessions, settings, tools, doctor, schedule, runner, workspace (files, changes, checkpoints), kit | same views after the shop tab has merged ≥ 1 task | 1440×900; phone for cockpit, radio, cache |
| P-07 every Settings page | the 11 pages | same | 1440×900 |
| P-08 dialogs and sheets | New session, Resume, Rename, Compact, Close confirm, Restore, Revert hunk, Sign in, Goal, Mode (typed confirm open), Context, Help, History, Views (phone), palette (empty and filtered "diff") | same | 1440×900 |
| P-09 popovers | mode menu, session menu, spend popover, inbox with two questions | same (`--fixture all`: shop and docs) | 1440×900 |
| P-10 single agent | orders-api cockpit | orders tab | 1440×900 |
| P-11 headless refusal | docs-sweep until `refused > 0` | docs tab until `m.refused > 0` | 1440×900 |
| P-12 ninth worker | `/swarm 9` | restart `swarm 9` | 1440×900 |
| P-13 reduced motion | `reduced: true` | same | 1440×900 |
| P-14 Quiet vs Full cache details | both | both | 1440×900 |
| P-15 drawer tabs | Details, Tool log, Mail, Cache of `be-2` | same | 1440×900 |
| P-16 runner output | `sim` run | `sim` run | 1440×900 |
| P-17 doctor | the mock's probe | the fixture endpoint (the mock provider served by the fixture: `mock-1`) | 1440×900 |

**Comparisons** (per scene; thresholds are failures, not warnings). The committed tool does 4; A3 adds 1 to 3 as a `--dom` mode
(the DOM and computed styles read through the DevTools protocol in both pages) and 5 as a `--no-sample` check:

1. *Structure.* A DOM signature per region (`#hud`, `#sstrip`, `#nav`, `#views`, `#rail`, `#foot`, open overlay): the ordered list of
   element tag + class list + `data-state`/`aria-*` presence, with text removed and repeated rows collapsed to one (row counts may
   differ). Must be identical.
2. *Styles.* For every element of the signature, computed `font-family`, `font-size`, `font-weight`, `color`, `background-color`,
   `border-*-color`, `display`, `grid-template-*`, `gap`, `padding` equal (colours compared as resolved RGB). Must be identical.
3. *Geometry.* Bounding boxes of the regions and of the first instance of each component class within 2 px (text length changes
   allowed: boxes of text-bearing inline elements are excluded).
4. *Pixels.* The tool's diff: ≤ 0.5% of the viewport outside `maskProfiles.live` plus the scene's own masks (the horse's legs and arcs
   when animated, text that holds real values); diff images under the tool's `--out` directory (not committed) are **looked at** by the
   builder and by D.
5. *No sample data on the real side.* No text of `_src/data/data.js` appears in the real page (a scan of strings longer than 12
   characters from the pack), except the UI's own fixed copy.

## 8. Behaviour suite ported from the mock

`_src/v3/test/t-*.mjs` are ported to run against the real page with `hooks-real.js` and the fixture backend (A3 provides the harness,
the owning C builder ports the script). Each keeps its assertions; time warps become waits for predicates.

| Mock script | Real port checks |
|---|---|
| `t-numbers.mjs` | HUD ring, stalls, Cache rows and `all` row, drawer chips, Sessions card and Replay frame agree at five moments (they all call `SL.calc` on the same model) |
| `t-governor.mjs` | a long hold collapses into one digest row; catch-up bounded; held and never-held runs end in equal models (`sig`) |
| `t-lifecycle.mjs` | section 6 |
| `t-input.mjs` | real key events: hover hold, slow, pin, Esc precedence, the quiet period (typed-ahead text never answers) |
| `t-func.mjs` | store round trip through the API, steer, interrupt, goals, new/resume/restart, every runner command answers (exit or `tty_only`), the palette has no entry without a handler |
| `t-layout.mjs`, `t-fit.mjs`, `t-layout-dock.mjs` | no horizontal overflow at 390..1920; vertical fit 1024×700..2560×1300 |
| `t-motion.mjs` | reduced motion: no animation, legs still, data still flows |
| `t-prune.mjs` | prune preview equals the server's plan; `--yes` removes exactly those |
| `t-a11y.mjs` | contrast, names, labels, landmarks, focus ring in every view and page |
| `t-safe.mjs` | S-08 |
| `t-quiet-scan.mjs` | Quiet hides hit-%/savings outside Cache and the drawer's Cache tab |
| `t-ten.mjs`, `t-perf.mjs` | section 10 |
| `t-dock.mjs`, `t-dock2.mjs`, `t-v3.mjs` | navigation, chords, the Workspace (blame, time travel, restore, revert, reviewed), Settings acting on the server, Tools, palette, the six v3 changes |
| `t-kbd.mjs` | ctrl+k and typing in the palette |
| `t-shipped.mjs` | the shipped page: no `window.__SL`, real frame loop, every view from its tab, palette and help from the keyboard, pointer hold, no console error, no CSP violation (phone and desktop) |
| `t-pack.mjs` | replaced by the asset test "no sample data shipped" |

## 9. Manual and visual QA (D, with screenshots read by a person)

1. Start `sleipnir web` in a real project with a real (owner-provided, never in tests) model; open the link; the cockpit fills as the
   team works; nothing says "sample" or "mock".
2. A question arrives while the pointer is over the chat: the inbox badge and toast appear, the strip shows `?`, the view stays held.
3. Answer 3 with an instruction: the worker reads it (its next message reflects it).
4. Restore a checkpoint, undo it; revert a hunk, undo it; `git diff` in a terminal agrees.
5. Close the browser tab for 15 minutes with a question open (`--ask-grace 10m`): the question is refused `nobody`; reopen: the
   history shows it.
6. Two browser windows on the same server: an answer in one closes the question in both.
7. Kill the server: the page shows the disconnected state (D-13); restart it: the old cookie is refused; the new link works.
8. Phone width: every view reachable from the bottom bar and More.
9. Reduced motion (OS setting and the page's own): the horse stands.
10. Keyboard only: every control reachable, focus visible, Esc precedence as section 2 of FEATURES.md.
11. `sleipnir web --open` on Linux opens the browser; the printed link is spent after the first open.
12. Resume a recorded session of the terminal chat: its history appears at the top with real times.

## 10. Performance and bounds

| Budget | Value | Measured by |
|---|---|---|
| Page main-thread time at rest (cockpit, live, 9 agents working) | ≤ 15% of a core averaged over 10 s; ≤ 3% while held | ported `t-perf.mjs` (CDP `Performance.getMetrics`) |
| DOM nodes after 10 minutes of the shop fixture | within 10% of the count after 1 minute | ported `t-ten.mjs` |
| Page heap after 1 hour of `--fixture shop` looping | ≤ 250 MB | CDP heap |
| Server memory per tab | journal ≤ 32 MiB (enforced), translator + state ≤ 64 MiB total | Go test with a synthetic 1-hour log |
| Event latency (sink call → frame written) | p99 ≤ 50 ms with 16 tabs and 4 clients on loopback | Go benchmark (`BenchmarkHubFanout`), not a CI gate |
| Snapshot of a tab with a full journal | ≤ 16 MiB gzip, built in ≤ 500 ms | Go test |
| Stream frame size | ≤ 1 MiB | Go test |
| Sink | never blocks an agent (`TestSinkNeverBlocks`) | Go test |

## 11. Repository checks (every builder before each commit; D at the end)

* `gofmt -l .` empty; `go vet ./...`; `go build ./...`; `go test -race -count=1 ./...` (with the 25-minute timeout of
  `scripts/check.sh`).
* `go test ./internal/repocheck` (doc coverage ≥ 90% repo-wide, CODEOWNERS patterns match files, markdown links resolve,
  `scripts/windows-excluded.txt` sorted with reasons, scripts executable, line-end rules).
* `go mod tidy -diff` empty; `scripts/check-deps.sh` (no new module); `scripts/gen-cli-docs.sh --check` (B1's `web` block);
  `scripts/record-demo.sh --check`.
* Cross-compile and vet for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64 with `CGO_ENABLED=0`: the web code
  builds everywhere; Unix-only parts (process groups in the runner, flock in the schedule lock) have `_unix.go`/`_other.go` files as
  `internal/inspect/open_unix.go` does.
* `scripts/check.sh` in full at integration (D).
* `.gitattributes`: `*.woff2 binary` (A3). The large mock HTML files under `docs/design/web-mocks/` are not committed (PLAN.md); the
  contract and `_src` sources are, unless the owner says otherwise.

## 12. Documentation to update (timeless, impersonal; AGENTS.md)

| File | Change | Owner |
|---|---|---|
| `docs/CLI.md` | the `web` section exists (A2); B1 regenerates the flags block after adding `--ask-timeout`, `--ask-grace`, `--project` | B1 |
| `docs/SECURITY.md` | section 5 "The web interface" exists (A2); add Δ1 (`--open`'s launch code) and the approvals rules of CONTRACT.md 2.5 | A2 |
| `docs/UX.md` | the web section exists (A2); C1 completes it: the views, the hold and catch-up, approvals and the quiet period, keys | C1 |
| `docs/ARCHITECTURE.md` | the `internal/web` row exists (A2); D adds the translator and the route packages to it | D |
| `docs/TESTING.md` | rows for the web unit tests, the e2e, the browser suite and parity, the `--fixture` backend | D |
| `docs/STATUS.md` | limitations: Replay of history (D-15), runner TTY commands (D-11), one server per user recommended | D |
| `README.md` | one line in the commands block and a link | D |
| `docs/GETTING-STARTED.md` | optional: "open it in a browser" | D |
| `docs/CONFIGURATION.md` | none (no new environment variable, no new config key) | |
| `CHANGELOG.md` | none unless a golden prompt file changes (it must not) | |
