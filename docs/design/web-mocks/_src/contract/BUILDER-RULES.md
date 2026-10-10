# Rules for every builder (read after AGENTS.md and PLAN.md; these override OWNERSHIP.md section 4's "Commits" line)

## Git
- Do NOT run any git command that changes state (no add, commit, checkout, stash, reset, merge, rebase, branch, push). The coordinator commits your files by path between waves. Read-only git (status, diff, log, show, blame) is fine.
- Several builders work in the SAME worktree at the same time. Touch only the files you own (OWNERSHIP.md section 3). If a file you do not own blocks you, or does not compile because another builder is mid-edit, wait a few minutes and retry; never "fix" it. Ask the coordinator through your final report (or a SendMessage if you are blocked) for a contract change; do not diverge silently.

## Machine safety (a previous session crashed three times from runaway test processes; the machine has 24 cores and 62 GB shared by up to eight builders)
- Source the toolchain first in every shell: `. ~/.local/sleipnir-toolchains.sh`, and use `(umask 022; ...)` for go test.
- Run EVERY `go test`, `go vet ./...`, `go build ./...` and any script that runs generated code under `~/.local/bin/capmem -m 8G`, with `-p 4` for go test (e.g. `~/.local/bin/capmem -m 8G go test -race -count=1 -p 4 ./internal/web/...`). Exit status 137 means the cap killed a runaway: fix the cause.
- Test only your own packages while iterating; run the broader `./cmd/sleipnir/...` / `./...` set at most once at the end. Never run the full repository suite more than once.
- Browser tests (headless Chromium) run SERIALLY and the browser is killed on exit; never leave processes behind. Never start a long-running server and forget it.
- Never touch the owner's real `~/.sleipnir`: set `SLEIPNIR_HOME` and `HOME` to a temp dir under `/tmp/claude-1000/.../scratchpad` (or t.TempDir) for anything that reads state. Never run anything that needs a network or a real key; never call a real model.

## Quality gates (each must pass for your files before you report)
`gofmt -l cmd internal` empty; `go vet` of your packages; `go test -race -count=1 -p 4` of your packages (under capmem); every production function and method has a doc comment (repo gate: >= 90% overall: `go test ./internal/repocheck -run TestProductionDocCoverage`); no new module or dependency; docs timeless and impersonal (describe behaviour, contracts, limits; no journals, no anecdotes); JS: no inline script or handler, no eval, every dynamic string through the page's `esc()`; secrets, tokens and held credentials never reach a browser response, a log line or argv.

## Tests
Table-driven and meaningful: they must be able to fail. Use `internal/provider/mock`, `sleipnir demo` scenarios, `statetest` fixtures, the e2e world helper; goroutine-leak checks (`testutil.CheckLeaks`) in packages that start goroutines; security tests for every route that mutates or reads files/secrets (Origin, token, traversal, symlink escape, oversized body, wrong content type, replayed confirm).

## Final report (SHORT: at most 40 lines)
Files created/changed (paths), the public API/signatures you added, test counts and the exact commands you ran with results, decisions or deviations (each with the reason), requests to the coordinator (contract changes, other builders' files), known gaps. Never paste long code or logs.
