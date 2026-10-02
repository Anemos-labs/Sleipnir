# Testing Sleipnir: what guards what

`docs/BUILDING.md` says how a test is written (table-driven, adversarial, race-clean, safe under load, no stopwatch). This page is
the map: the kinds of check the repository has, what each one is there to catch, and the one command that runs it. About 4,200
test functions, 100 fuzz targets, 31 benchmarks and 435 golden and seed files, in 78 packages (`internal/repocheck` counts them and holds
these figures to a tenth); the test code is about as long as the code it tests.

## The layers

| Layer | Guards against | Where | Run it |
|---|---|---|---|
| **Unit and table tests** | a function that does the wrong thing on an empty, huge, hostile, Unicode, CRLF, symlinked or concurrent input | `*_test.go` next to the code | `go test -race -count=1 ./...` |
| **Golden bytes** | a silent change of what the model, the provider or the screen is sent: the canonical JSON of every block and tool, the whole rendered prompt for every route and hot mode, the constitution, the tool list, the wire bodies of the adapters, every widget at four widths, the exports | `testdata/golden/` (about 400 files) | the same, and `go test ./internal/kv -run Golden -update` to change one on purpose (`BUILDING.md`, "Changing prompt bytes") |
| **Declared change** | a change to the bytes of a prompt layer that nobody priced | `scripts/check-declared.sh BASE`: a golden file of `kv`, `agent` or `core` changed, so `CHANGELOG.md` must have an added line | `sh scripts/check-declared.sh origin/main` |
| **Fuzz targets** | a parser that panics, hangs or lies on input nobody wrote: the SSE and chat decoders, the event log, the shell analyser, the permission rules, the redactor, tool arguments, JSON repair | `Fuzz*` functions and `testdata/fuzz/` seeds | `sh scripts/fuzz.sh 30s` (every target of every package; the nightly run gives each three minutes) |
| **Every-split property** | a stream decoder whose answer depends on where the network cut the bytes | `internal/provider/providertest` | in the adapters' tests |
| **Real-binary e2e** | what a function test cannot see: Ctrl-C, the exit status, the streams, the terminal. The binary runs in a child process in a private home against a scripted mock model that a test can hold mid-turn | `cmd/sleipnir/e2e_*_test.go` | `go test -race ./cmd/sleipnir` |
| **Terminal tests** | the program and the terminal fitting together: `internal/ptytest` runs a command on a pseudo-terminal, a test types, resizes, pastes and presses Ctrl-C, and a terminal emulator (`internal/tui/vt`) says what the screen is | `cmd/sleipnir/e2e_*`, `internal/tui/app` | the same |
| **Screens as goldens** | a widget that overflows its width, writes a control character or draws something else than it did | `internal/tui/widget`, `internal/tui/app` | `go test ./internal/tui/...` |
| **Recordings** | a README recording that no longer matches what the interface draws | `docs/media/`, `scripts/record-demo.sh --check` | `sh scripts/record-demo.sh --check` |
| **Allocation gates** | a hot path that allocates more than it is held to (the count does not depend on the machine, so it can fail a build) | `allocs_gate_test.go`, built only without `-race` | `go test -run Allocations ./...` |
| **Benchmarks** | a slowdown, compared as two ranges that must not overlap, never gated on a wall-clock number | `Benchmark*`, `scripts/perf.sh` | `sh scripts/perf.sh run OUT.txt` |
| **Chaos** | an agent that cannot survive the endpoint it will really meet: a 503 whose body says the database is away, a 502 with an HTML page, a 429, a reset, a response cut off or ended without its last frame, a server that says nothing | `internal/provider/chaos` (the faults), `internal/agent/chaos_test.go` (every fault at every request of a run, eighty random mixtures; a thousand nightly), `internal/provider/openaichat` (what each fault is called) | `go test -race ./internal/agent -run Fault`; `SLEIPNIR_CHAOS_SEEDS=1000` widens the mixtures |
| **Soak** | a run that keeps something for every step and so cannot last for hours: a thousand steps, a tool result each, the thread folded as it grows, and the live heap after a collection at a quarter, a half and three quarters of the way, which may climb by what the archive's index takes and no more (twenty thousand steps nightly) | `internal/agent/soak_test.go` | `SLEIPNIR_SOAK_STEPS=20000 go test -run LongRun ./internal/agent` |
| **Goroutine leaks** | a test that leaves a goroutine behind in the packages that start any | `testutil.CheckLeaks` in their `TestMain` | the same as the unit tests |
| **Load** | the test that only passes on a quiet machine | `BUILDING.md` ("a load test finds what a quiet machine hides") | three `go test -race ./...` at once |
| **Simulation** | a change of the cache policy that makes the model of the economics lose | `internal/kv/sim`, `sleipnir sim --mode scenarios` | `go run ./cmd/sleipnir sim` |
| **Repository invariants** | a link that is dead, an action that is not pinned, a job that a ruleset names and a workflow renamed, a CODEOWNERS line that matches nothing, a platform that the release builds and CI does not | `internal/repocheck`, `scripts/*_test.sh` | `go test ./internal/repocheck` |
| **Real endpoints** | everything above, which is a model of the world | `docs/VALIDATION.md` (`scripts/validate.sh`), `docs/BENCHMARKS.md` (`scripts/bench.sh`), `docs/DOGFOOD.md` | the three scripts, with a key |

## The three that need a model

The mock provider is protocol-strict and has an automatic prefix cache, so most of the suite runs offline. Whether the harness is any
good needs a real model, in three ways that complement each other:

- **Validation** (`scripts/validate.sh`): one task per model, to see that the endpoint and the harness speak to each other, what
  the cache really does, what a compaction costs.
- **The benchmark** (`bench/`, `scripts/bench.sh`, `sleipnir rl report|compare`): a fixed suite of verifiable tasks, scored by the
  project's own tests in a clean checkout, two builds compared side by side. It finds what is common (a refusal in three quarters of
  the runs).
- **Dogfooding** (`docs/DOGFOOD.md`): using it, and writing down what hurt. It finds what a count cannot rank: the advice cut off in
  the middle of a line, the run that could not run its tests. Every row of the register is a defect with its evidence, its cause and
  a test that failed before the fix.

## What fails a merge, and what only reports

Required (`ci-gate`, which is the one check the ruleset names): gofmt, `go vet`, tidy modules, the dependency allow-list, pinned
actions, the declared-change check, the generated documents (`docs/CLI.md`, the README's simulator block, the recorded demo), the
tests of the scripts, the suite under `-race` on Linux (amd64 and arm64) and macOS with the Go of `go.mod` and on Linux amd64 with
the newest stable Go, the allocation gates (the one leg that runs them: they are not built under `-race`), every release platform
built and vetted, the cache-policy guards, `govulncheck`, dependency review on pull requests, `actionlint` and `zizmor`, and
`internal/repocheck` (it runs with the suite). Informational: Windows (every package but those `scripts/windows-excluded.txt` lists, with the reason for each); the nightly run (every fuzz target for three minutes, the
suite three times under `-race` in shuffled order, a thousand random mixtures of endpoint faults, a run of twenty thousand steps, coverage, the drift checks again, `govulncheck` again); `bench/build.sh --check`
and the benchmark itself (by hand, they need a key). `docs/REPO-SETUP.md` has the whole list and what each guards.

A required check that flakes blocks merges, so a test that cannot pass on a loaded runner is a defect of the test: its bounds are
hang guards (a minute, not seconds), its waits are barriers, and `docs/BUILDING.md` says how to write one.

## Writing the next test

Start from the failure: a regression test is run on the parent commit first and must fail there, for the reason it will name. Put the
case next to the others of its table. If it is about bytes, it is a golden. If it is about time, it is a barrier or an allocation
count, never a clock. If a real model found it, add the row to `docs/DOGFOOD.md` with the evidence.
