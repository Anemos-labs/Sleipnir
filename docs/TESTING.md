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
verification, hooks, and shell tools; concurrent verifier output capture; and
inherited credential filtering:

```sh
go test -count=1 -timeout 3m ./internal/session -run 'Test(SessionDirectoryLock|ASessionDirectoryHasOneWriter)'
go test -count=1 -timeout 3m ./internal/executil
go test -count=1 -timeout 3m ./internal/session -run 'Test(WindowsQuotedCommands|VerificationCombinesConcurrentOutput|VerificationRetainsFinalFailure|VerificationScrubsInheritedSecrets)'
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
| Production doc coverage | At least 90% of named, non-generated Go functions and methods have doc comments; tests and fixtures are excluded |
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
