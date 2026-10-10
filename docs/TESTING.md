# Testing

## Required checks

```sh
go test -race -count=1 ./...
go vet ./...
go build ./...
gofmt -l cmd internal
```

`scripts/check.sh` also checks generated files, module consistency, dependency
policy, action pinning, and release builds. CI configuration is in
`.github/workflows/`. The broad Windows suite excludes packages listed with reasons
in `scripts/windows-excluded.txt`. A required Windows runtime job checks session
lock contention, release, and recovery after process death; command quoting across
verification, hooks, and shell tools; concurrent verifier output capture;
inherited credential filtering; detected shell guidance in solo, worker, and resumed
provider requests; native workspace reads and writes, path rules,
protected files, symlink and junction escapes, and worker confinement; and verifier descendant cleanup after
cancellation, timeout, and normal exit:

```sh
go test -count=1 -timeout 3m ./internal/session -run 'Test(SessionDirectoryLock|ASessionDirectoryHasOneWriter)'
go test -count=1 -timeout 3m ./internal/perm ./internal/session -run 'TestWindows(Workspace|Junction|Glob)'
go test -count=1 -timeout 3m ./internal/executil
go test -count=1 -timeout 3m ./internal/session -run 'Test(WindowsVerification|WindowsQuotedCommands|VerificationCombinesConcurrentOutput|VerificationRetainsFinalFailure|VerificationScrubsInheritedSecrets)'
go test -count=1 -timeout 3m ./internal/tools/shell ./internal/session -run '^TestRuntimeShell'
```

## Coverage by layer

| Check | Purpose |
|---|---|
| Unit and regression tests | Behavior, errors, input limits, and concurrency |
| Provider wire and streaming tests | API payloads, tool and reasoning replay, usage, partial streams |
| Cache boundary tests | Reuse of complete previously sent messages, not just internal blocks |
| Golden prompt tests | Deliberate changes to stable bytes |
| Real-binary and pseudo-terminal tests | Keyboard input, signals, exit status, resize and rendering |
| Fuzz tests | Malformed inputs and parser limits |
| Leak, chaos and soak tests | Cleanup, endpoint failures and long sessions |
| Allocation gates and benchmarks | Memory behavior and hot-path performance |
| Repository checks | Links, workflows, platform and release configuration |
| Code-age collector | Committed line attribution across formatting and renames, unusual paths, incomplete history, and safe report rendering; see [Maintenance](MAINTENANCE.md) |
| Production doc coverage | At least 90% of named, non-generated Go functions and methods have doc comments; tests and fixtures are excluded |
| Browser interface: Go | The server envelope (token, Host and Origin, CSRF header, body limits, confirmation ids), every route with its error cases, the registered routes against the tables of [Web API](WEB-API.md), the event translator against recorded logs, and the host over real sessions on the mock provider, including the real binary with `sleipnir web --fixture` |
| Browser interface: JavaScript | `node --test internal/web/uidev/test/*.test.mjs`: the data layer, the reducer on recorded streams, the forms and state machines, and hostile strings in every field the server sends (names, diffs, command output, tool-server text) rendering as text |
| Browser interface: appearance | `node scripts/web-parity.mjs` renders two pages in headless Chromium on a virtual clock and reports the differing pixels per scene (a change to a screen is compared with a reference page and then looked at); see [Building](BUILDING.md) |
| Isolated-team recovery | Real process death with unfinished worker edits, original dirty base, integration cursors, ownership, mode validation and safe pruning |

Use a focused test while fixing a bug, then run the relevant package suites.
A regression test should demonstrate the original failure. Do not add tests
whose only purpose is to enforce prose, test counts, or a preferred document layout.

Widget growth checks compare CPU work for an input and eight times that input.
They use process CPU time on Unix and process CPU cycles on Windows, excluding
scheduler wait time from the ratio. Control workloads verify that the check
tolerates scheduling delays and detects quadratic growth. Other platforms use
elapsed wall time and skip the scheduler-wait control.

## Terminal verification

Inspect changes in a real terminal at narrow and wide sizes. Open generated
screenshots and check contrast, wrapping, focus, and cursor placement.
See [Building](BUILDING.md).

## Endpoint and task quality

Mocks exercise protocol contracts. They do not establish real provider caching,
latency, pricing, or task quality.

[Validation](VALIDATION.md) covers endpoint behavior.
[Benchmarks](BENCHMARKS.md) covers paired task comparisons.
[Manual acceptance checks](DOGFOOD.md) covers the interactive workflow.
