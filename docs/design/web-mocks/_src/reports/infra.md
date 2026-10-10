# Report: HTTP infrastructure and repository rules for `sleipnir web` (2026-10-09)

Read-only investigation. Paths relative to the repo root. (Saved by the coordinator from the investigator's report.)

## 1. How `inspect` is served

**Corrections to the brief**
- `internal/inspect/open_unix.go` and `open_other.go` are not a browser opener. They define `openRegular` (O_NOFOLLOW/O_NONBLOCK, FIFO and symlink refusal) for reading logs. `--open` lives in `cmd/sleipnir/inspect.go:106-110` and `openBrowser` at `:158-173` (open, rundll32 or xdg-open, reaped in a goroutine).
- `state.go` only holds fold structs. `registry.go` is session discovery.
- `isolation_test.go` is about swarm worktree isolation, not server isolation.
- Updates are polling, not SSE. `tickLoop` (`server.go:605`) tails the logs every `--interval` (1s), and the JS polls `/api/*` with `fetch` (`web/js/state.js:37`, `app.js:263`). There is no SSE or WebSocket anywhere.

**Serving**
- `//go:embed web` is at `server.go:24`. `loadAssets` (`:119-147`) reads every file into memory with a sha256 ETag. Its content-type switch knows only html, js, css and svg. Fonts or images would be served as octet-stream.
- Routing is Go 1.22 `ServeMux` patterns, e.g. `/api/agent/{id}` (`:149-174`). The `/api/` catch-all returns a JSON 404.
- The security envelope in `ServeHTTP` (`:177-200`) runs in this order: security headers; a method gate, GET/HEAD only (405 with `Allow`); the Host allowlist; the token check.
- Ids are map keys, never paths. `textParam` and `uintParam` cap length and reject NUL (`:463-482`).
- Default address is `127.0.0.1:8787` (`cmd/sleipnir/inspect.go:30`). There is no free-port fallback. `--addr 127.0.0.1:0` is the way to get an ephemeral port, and e2e uses it.
- Output convention: the first stdout line is the URL, and everything else goes to stderr (`inspect.go:97-111`). Ctrl-C returns nil and exits 0.
- `Serve` (`server.go:572-602`) refuses a non-loopback socket without a token, sets timeouts (header 10s, read 30s, write 2m, idle 2m), caps headers at 16 KiB, discards the ErrorLog, and shuts down with a 3s grace.

**Security measures present**
- Loopback is enforced three times: `checkListenAddr` (`:223`), `Listen` on the bound address (`:236-249`) and `Serve` (`:573`). A non-loopback address requires a token.
- The Host allowlist (`:255-284`) blocks DNS rebinding on loopback.
- Token handling: Bearer header, cookie, or `?token=`. Constant-time comparison on sha256 digests (`:289-297`). The cookie is HttpOnly and SameSite=Strict, and holds a digest rather than the token (`:314`). The redirect after `?token=` is built only from known asset paths (`:316-329`).
- CSP (`:31`) is `default-src 'none'`, self-only for script, style, img and connect, with `frame-ancestors 'none'` and `form-action 'none'`. The envelope also sends nosniff, X-Frame-Options, no-referrer, COOP/CORP and Permissions-Policy.
- JSON goes through `json.Encoder`, which HTML-escapes strings (`:340`). The UI uses `textContent` only.
- Error bodies don't leak paths (`server_test.go:686`). `Logf` never logs request contents or the token. `/healthz` is the only unauthenticated route, and it is still Host-gated.

**Tests to mirror**
- `server_test.go` (753 lines): helpers `do`, `getJSON` and `newTestServer` (httptest recorder with `Host: 127.0.0.1:8787`); methods refused (`:170`); DNS rebinding (`:198`); token matrix (`:217`); redirect confinement (`:300`); non-loopback (`:334`); path traversal (`:389`); inert log strings (`:461`); headers on every response (`:503`); a real socket plus shutdown (`:699`).
- `web_test.go` reads the embedded assets and enforces: no inline script, style or handler; no innerHTML, eval, import(), WebSocket, XHR, postMessage or window.open (`:85-127`); no remote URLs or `@font-face`, and `fetch` only to `/api/` (`:129-160`); every module import resolves inside the binary; dark/light token parity and a phone breakpoint.
- `swarm_contract_test.go` and `synth_test.go` build synthetic logs.

**Security gaps for a server that starts agents and runs commands**
- Methods are hard-coded to GET/HEAD (`server.go:187`). A POST surface needs per-route methods, e.g. `"POST /api/x"` patterns.
- There is no Origin or `Sec-Fetch-Site` check and no CSRF defence. Go 1.25 (the `go.mod` toolchain) has `http.CrossOriginProtection` (not confirmed here because `go` was not on PATH). It covers non-safe methods only and passes requests carrying neither header. A `text/plain` POST from any web page to `127.0.0.1:6969` passes the Host check. So also require `Content-Type: application/json`, which forces a preflight.
- The Host check ignores the port (`:274-284`), so another localhost origin passes. Mutating routes need a full-origin compare.
- A token is optional on loopback (`:45-47`, `:228`). For `web` it must be mandatory. Other local users and processes can reach a loopback port.
- Token lifecycle: the token is static; the cookie is a deterministic digest of it (`:107`) and lives for 7 days (`:315`); there is no rotation, logout, revocation or per-run random token; `?token=` is accepted on any `/api/` path (`:311`); the token is printed in the URL on stdout (`inspect.go:97`); the cookie is `Secure` only when TLS is in use; the server speaks plain HTTP.
- There is no rate limit, auth-failure throttle, concurrency cap or per-IP cap.
- There are no request-body limits, because no body is ever read. Use `http.MaxBytesReader`, decode strictly (`DisallowUnknownFields`) and cap fields.
- `WriteTimeout: 2m` (`:580`) would kill SSE streams. Use `http.NewResponseController(w).SetWriteDeadline` per stream, plus heartbeats and a per-client buffer bound.
- WebSocket would add cross-site hijacking, because there is no Origin check and the browser does not apply same-origin policy to it. Prefer SSE plus `fetch` POST: no dependency, and it works with `connect-src 'self'`. EventSource cannot send an Authorization header, so SSE needs the cookie.
- The CSP has no `font-src`. Fonts would be blocked, and the content-type map lacks woff2 and png.
- There is no panic recoverer. `internal/rl/env/server.go:289-299` has one to copy.
- There is no secret redaction. Logs are verbatim by design (`docs/SECURITY.md`), and the inspector streams them as is. Reuse `harden.LooksSecret` (`internal/harden/secret.go:43`), `harden.Held()` and `internal/rl/redact` (`redact.New`, `String`). Never serialize the web token or held keys. Add a test for that.
- There is no approval design for the browser. The chat's rule is that an answer must be typed after the question was shown (`docs/SECURITY.md`, `cmd/sleipnir/chat_input.go`). A web equivalent needs random per-question ids, single use, and deny on timeout or disconnect (`session.Options.AskTimeout`). Sanitize `Request.Summary`. Don't let the page choose cwd, trust, permission mode or a `Remember` scope. Fix these by CLI flags at startup, with `--trust-project` as a flag only.
- `internal/rl/env/server.go` is the precedent for a server that runs commands over POST: bearer token, `MaxBodyBytes` 8 MiB, `MaxRuns` 429 and a recoverer. But it allows no token on loopback, has no Host or Origin check, and is not browser-hardened.

## 2. Registering a command

- `cmd/sleipnir/inspect.go:23` does `func init() { extraCommands["inspect"] = cmdInspect }`. The map is declared at `main.go:223`, and dispatch is at `main.go:79-82`. A new file such as `cmd/sleipnir/web.go` follows the same pattern. A command that handles Ctrl-C itself would be added to `ownsInterrupt` (`:227`). Don't do that for `web`. The default context cancel plus returning nil gives exit 0 on Ctrl-C.
- The usage text is `main.go:229-`. Add the command under "Look at a session, or measure:" (`:261`), as `  web   description` with two or more spaces after the name. Lines must be at most 78 or 79 columns (`usageWidth`, `help_test.go:118`).
- Flags: `newFlagSet(name, flag.ContinueOnError)` (`flagutil.go:16`), `fs.Usage` using `printHelp` and `printFlags`, and `usageError(fs, msg)` (`:33`). Go's flag package accepts `-x` and `--x`. Flags after positionals: inspect loops `Parse` (`inspect.go:53-69`), and `parseInterspersed` (`flagutil.go:74`) is the shared helper. Read env-backed tokens via `harden.Secret(...)`. `harden/readers_test.go:40` fails on a plain `os.Getenv` of a credential-looking name.
- Exit codes (`exitcode.go`): 0 ok, 1 error, 2 usage, 3 unfinished (`exitUnfinished`), 75 temp-fail (`exitTempFail`), and 128+signal for an interrupt that surfaces `context.Canceled` (`main.go:112`, `reportError` at `:182`). A bind failure is a plain error and exits 1.
- `docs/CLI.md` generation: `scripts/gen-cli-docs.sh` runs `<cmd> -h` for each `<!-- flags: web -->` marker and rewrites the block. It also fails if a command listed in `--help` between "Every day:" and "A model is written" has no marker (and for markers with no command). Add a hand-written `### sleipnir web` section and a row in the command table (`CLI.md:~34`). If `web` takes positionals, update the Conventions paragraph (`CLI.md:12`). If the token env var is added, add it to `env -u` in the script's `run()` (`gen-cli-docs.sh:52`). Otherwise the help default depends on the caller's environment.
- Tests enforcing help consistency: `e2e_width_test.go:52` runs `<name> -h` for every listed command at 80 columns. `e2e_commands_test.go:59` (`TestCommandExitStatus`) is a table to extend with `web` error cases. `help_test.go` covers the usage text.

## 3. Checks every change must pass

`scripts/check.sh` runs these in order. CI's `lint` job has the same list, and `repocheck.TestDriftChecksAreWired` fails if the two diverge.

1. `gofmt -l cmd internal` must print nothing.
2. `go mod tidy -diff`, then `go mod download` and `go mod verify`.
3. `scripts/check-deps.sh` against `scripts/deps-allowlist.txt`. Only x/net, x/sys and x/term are `direct`. It also fails on any compiled package outside the allowlist, so any new Go module or vendored Go code fails. `x/net/websocket` would pass the module check but isn't needed.
4. `scripts/check-pins.sh` for action SHAs.
5. `scripts/check-declared.sh`, which requires a CHANGELOG entry only when golden prompt files change. A new command doesn't trigger it, and the CHANGELOG never mentions `inspect`.
6. `gen-cli-docs.sh --check`.
7. The CACHE-ECONOMICS sim block.
8. `record-demo.sh --check`.
9. The `scripts/*_test.sh` tests.
10. `go vet ./...`.
11. `go build ./...`.
12. `go test -race -count=1 -timeout 25m ./...`, which includes `internal/repocheck`.
13. The allocation gates, run without race.
14. Cross-compile and `go vet` for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 and windows/amd64 with CGO off. Files that use unix-only syscalls need build tags, as `open_unix.go` and `open_other.go` do.

CI also runs a security job (govulncheck, dependency review), actionlint and zizmor (workflows only), and CodeQL for Go.

**`internal/repocheck` rules that apply**
- `TestProductionDocCoverage` (`doccoverage_test.go:73`): at least 90% of named funcs and methods in `cmd/` and `internal/` need doc comments, counted over the whole tree.
- `TestWindowsExclusionsNameRealPackagesWithReasons`: `scripts/windows-excluded.txt` must be sorted, each entry needs a `# reason`, and the package must exist. The informational Windows job tests every package not on that list. Exclude `internal/web` only if its tests assume POSIX.
- `TestCodeownersPatternsMatchFiles`: every CODEOWNERS pattern must match a file. Add `/internal/web/ @Thanos420NoScope`, because this is a code-execution surface like `/internal/perm/`.
- `TestTestsBuiltOnlyWithoutRaceAreRunByCI`: avoid `//go:build !race` test files that contain Test functions.
- `TestMarkdownLinksResolve`: every markdown link must resolve, with exact case.
- Go-version statements in docs must match `go.mod`.
- `TestScriptsAreExecutableOrInvokedWithSh`. `TestGitattributesPinLineEnds`.

**Not present:** no file-size limits, no forbidden-string scan, no binary-size gate and no JS/CSS lint.

**Things a large embedded asset set could trip**
- `.gitattributes` is `* text=auto eol=lf`. Mark any woff2 or other binary asset as binary explicitly. There is no linguist config, so large JS will skew language stats.
- `.goreleaser.yaml` archives `docs/**/*`. The untracked `docs/design/web-mocks/` holds many HTML files of 0.2 to 1.3 MB each. Committing them would bloat every release archive, so keep them out of git or ignore them. `docs/design/ux/.gitignore` already ignores `*.html`, but only in that folder.
- `embed` skips names starting with `.` or `_` unless `all:` is used.
- The license is BSL 1.1. No tooling checks the licenses of vendored JS or fonts, so any third-party asset needs a manual license decision.
- `make audit` (`cmd/codeage`) only reports ages. Files over 2 MiB get file dates only. It never fails.
- CSP: fonts need `font-src 'self'`.
- AGENTS.md requires that UI changes are actually looked at. For a browser UI that means screenshots in Playwright Chromium (`scripts/svg2png.mjs` shows the pattern), at narrow and wide sizes.

## 4. Test conventions and what a web e2e can reuse

- `cmd/sleipnir/main_test.go` `TestMain` re-executes the test binary as `sleipnir` when `SLEIPNIR_E2E_CHILD=1`.
- `e2e_test.go` provides: `newWorld(t, providerURL)` (private HOME, state, tmp and project, with a minimal environment, `:68-109`), `w.cmd(args...)`, `w.run(stdin, args...)`, `noCrash`, `startModel(t)` (a scripted OpenAI-style mock with `on(name, say/writes/runs)`, `.held()` gates and `seen()`/`toolResults()`), and `e2eGuard = ptytest.Guard` (60s), a hang guard and never a timing assertion.
- `TestDemoAndInspect` (`e2e_commands_test.go:369-486`) is the exact template for a web e2e. It starts the command with `--addr 127.0.0.1:0`, reads the URL from the first stdout line and matches `^http://127\.0\.0\.1:[1-9]\d*/$`, exercises GET, 404 and POST with an http client, sends os.Interrupt and expects exit 0, and checks the stderr banner.
- `internal/provider/mock` (`mock.New(Config, Responder)`, `Reply{Text|ToolCalls|Fault}`) is the in-process fake provider. `sleipnir mock` (`main.go:516`) serves it on :8089.
- `sleipnir demo --scenario handbook|shop --dir D` writes a real recorded team session at `D/session/events.jsonl`. `internal/demo` and `statetest.DemoLogFile(t)` give ready logs. These are good fixtures for streaming and replaying events.
- For a live feed use `internal/events` `Log.Subscribe` (`log.go:475`, which drops events for slow subscribers rather than blocking) or `internal/tui/state` `Tail`/`Follow` (`tail.go:73,104`). The state package is a pure, tolerant, bounded reducer with immutable Snapshots, and it sanitizes text. Prefer it to the inspector's heavier model for live state.
- `ptytest` is for terminals only. A web server doesn't need it.
- Leak checks: `testutil.CheckLeaks` in `TestMain` (used in swarm, agent, session, workspace, mcp). `internal/inspect` doesn't use it; the new package should, because of the SSE goroutines.
- Session directories are single-writer. `session.lockDir` takes a flock on `.lock` (`internal/session/dirlock_unix.go`), and a second process gets an "in use" error.

## 5. Documentation to update

Docs are timeless and impersonal: describe behaviour, contracts and limits, with no journals and no performance anecdotes (AGENTS.md; `docs/BUILDING.md`, "Documentation").

| File | Change |
|---|---|
| `docs/CLI.md` | `<!-- flags: web -->` block, command table row, section, conventions line |
| `README.md` | headless commands block (`~:64`) and doc links |
| `docs/UX.md` | a web-interface section (it currently covers the terminal only) |
| `docs/ARCHITECTURE.md` | package-table row and an "Observability" mention |
| `docs/STATUS.md` | limitations |
| `docs/SECURITY.md` | a section on the web surface: threat model, loopback, token, Origin, approvals. It does not mention the inspector at all today |
| `docs/CONFIGURATION.md` | env var table (`:199`) if `SLEIPNIR_WEB_TOKEN` is added |
| `docs/TESTING.md` | a test-layer row |
| `CHANGELOG.md` | optional, since it holds compatibility and cache declarations only |
| `.github/CODEOWNERS` | the `/internal/web/` line (see section 3) |
| `docs/GETTING-STARTED.md` | optional |

## Recommended layout

Mirror `internal/inspect`: a flat package, and the command in `cmd/sleipnir/web.go`, alongside `inspect.go`.

- `internal/web/doc.go`: security design rules, in the same style as `inspect/doc.go`.
- `server.go`: Config, NewServer, the envelope (Host, Origin, token, headers, CSP), Listen and Serve with a recoverer.
- `auth.go`: random per-run token, cookie and rotation, throttling. Keep it separate so it can be reviewed and CODEOWNED.
- `api_*.go`: sessions, actions (start, send, cancel, approvals) and a bounded-body JSON helper.
- `stream.go`: SSE hub over `events.Log.Subscribe` or `state.Follow`, with heartbeats and per-client buffers.
- `approvals.go`: the `perm.Prompter` bridge. Single-use ids, timeout, deny on disconnect.
- `assets.go` with `//go:embed ui`: a `ui/` dir with `index.html`, `app.css`, `js/` and, if any, `fonts/`. Keep content types and the CSP in sync (`font-src`).
- Tests: `server_test.go`, `auth_test.go`, `csrf_test.go`, `stream_test.go`, `approvals_test.go`, `assets_test.go` (ported from `web_test.go`, with `fetch` allowed to `/api/` and EventSource to `/events`), `main_test.go` with `CheckLeaks`.
- Reuse directly: `inspect.isLoopbackHost`, `checkListenAddr`, `Listen` and the token-compare helpers (export or duplicate them; they are unexported today), plus `openBrowser` and `inspectURL` from `cmd/sleipnir/inspect.go`.
- Default `--addr 127.0.0.1:6969`. On "address in use", fail with a message that names `--addr 127.0.0.1:0`.
