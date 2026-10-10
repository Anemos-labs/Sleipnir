# The shipped page and the reference page

The page in `internal/web/ui` is the one `sleipnir web` serves: plain files, embedded in the binary, wired to the HTTP API of the
server. The reference page (`internal/web/uidev/mock`, opened from its `index-mock.html`) is a tracked, self-contained
prototype of the same screens that runs on sample data and a scripted simulation. Both load the stylesheets and fonts of
`internal/web/ui`, and most renderers are the same code, so a screen drawn from the server's data can be compared with the same screen
drawn from the sample data (`scripts/web-parity.mjs`, and the tests in `internal/web/uidev/test`). Nothing in the embedded page
touches the simulation or the sample data; this file lists what takes the place of each file that exists only in the reference
page. `js-inventory.md` describes every module of the shipped page.

## Files of the reference page that the shipped page does not have

| File of the reference page | What it is | What takes its place in the shipped page |
| --- | --- | --- |
| `js/data.js` | The data pack (`window.SLDATA`): sample projects, models, prices, sessions, file histories, formatters. | Snapshots and catalogues served by the API, read by `js/live.js` and `js/11-data-live.js`. |
| `js/outputs.js` | Command outputs of the data pack (`SLDATA.outputs`): the text a `sleipnir` command prints in the runner. | Output streamed from the commands the server runs (`js/92-runner.js`). |
| `js/cli-spec.js` | The flag spec of every `sleipnir` command (`window.SLCLISPEC`), the cli-spec JSON of the data pack as an assignment. | The spec the server serves at `/api/cli`, generated from the real flag sets (`sh scripts/gen-clispec.sh`). |
| `js/10-fixtures.js` | The built-in sample data (`SL.FX`): roles, roster, prices, models, providers, rules, skills, MCP servers, sample code and trees, commands. | The catalogues the API returns. |
| `js/11-data-adapter.js` | `SL.D`, the one object the renderers read data from; adopts pieces of `SLDATA` over `SL.FX` when their shape matches. | `js/11-data-live.js`: `SL.D`, `SL.G` and `SL.data`, caches filled from the API under the same field names. |
| `js/40-scripts.js` | `SL.scripts`: the deterministic timelines of the sample sessions, the generic new-run script, scripted chat and steer replies, the director that schedules verification and the end of the goal. | The event stream of each session, applied by `js/live.js`. |
| `js/95-kit.js` | The `?kit` component catalogue view: a design reference, not part of the product. | Not shipped. |
| `js/zz-test-hooks.js` | Test hooks of the reference page: a time warp, counters of timers and listeners, a signature of a model. | `internal/web/uidev/hooks-real.js`, injected by a test driver and never loaded by `index.html`. |

The shipped page also has files the reference page lacks: `js/api.js` (`SL.api`, the only door to the server: requests,
confirmations, the stream) and `js/live.js` (`SL.live`, the boot sequence, the snapshots and the frames that become sessions).

## Files of both pages whose bodies differ

| File | In the reference page | In the shipped page |
| --- | --- | --- |
| `js/50-sessions.js` | `SL.sessions`: the registry, the `Session` class with its two clocks and event log, creation, restart, pruning, the recorded-session list, all produced locally. | The same clocks, log, `rebuild` and `collapse`; the registry follows the tabs the server hosts, and events come from the stream. |
| `js/60-actions.js` | `SL.act`: every action a person can take, applied to the simulation by inserting synthetic events; `SL.G`, the state shared by every session. | The same action names, arguments and `{ok, why}` results; each action is a request to the API, and its effect arrives as events and meta from the server. `SL.G` is built by `js/11-data-live.js`. |
| `js/92-runner.js` | The runner's simulated outputs for `sim`, `sessions prune`, `trust`, `mcp`, `doctor` and the generic fallback. | The same form and output pane; a run is a child process of the server and its output is streamed. |
| `js/96b-ws-data.js` | `SL.ws`: file contents, status, ownership, blame and hunks at any checkpoint, computed from the data pack's change sets. | The same methods, filled from the workspace routes of the API (tree, file as of a checkpoint, diff, per-line authorship). |

## Left out of the shipped page

| Where | What |
| --- | --- |
| `index.html`, footer | The `MOCK · sample data` chip (`.mockchip`) of the reference page; its rule remains in `css/70-overlays.css`. |
| `js/95-kit.js` | The `?kit` view (see above). |

## Load order

The reference page loads `00-namespace.js` (creates `window.SL`), the data pack (`data.js`, `outputs.js`, `cli-spec.js`), then the
modules in the numeric order of their names (`96-ui-nav.js` before `96b-ws-data.js`).

The shipped page loads `00-namespace.js`, `00-util.js`, `api.js`, `11-data-live.js`, `20-clock.js`, `30-model.js`,
`50-sessions.js`, `60-actions.js` and `live.js`, then the views in the numeric order of their names (`96-ui-nav.js` before
`96b-ws-data.js`, `98-ui-settings.js` before `98b-ui-tools.js`), and `99-app.js` last. A module reads another module's namespace
when it is evaluated (`const D = SL.D`), so a file loads after the files whose namespaces it reads.
