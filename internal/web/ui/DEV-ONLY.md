# Mock-only parts of the UI

The UI in this directory is the approved single-file mock, split into plain files and served as it is. It still runs on a
simulation and on sample data. The files below exist only for that: each is replaced by a client of the live API
(`sleipnir web`), or removed, when the UI is wired to a real session. Nothing outside this list talks to the simulation
directly; `js-inventory.md` says what each of the other modules reads and writes.

## Replaced by the live client

| File | What it is | What replaces it |
| --- | --- | --- |
| `js/data.js` | The data pack (`window.SLDATA`): sample projects, models, prices, sessions, file histories, formatters. | Snapshots served by the API. |
| `js/outputs.js` | Command outputs of the data pack (`SLDATA.outputs`): the text a `sleipnir` command prints in the runner. | Streamed results of the runner endpoints. |
| `js/cli-spec.js` | The flag spec of every `sleipnir` command (`window.SLCLISPEC`), the cli-spec JSON of the data pack as an assignment. | The spec generated from the real flag sets, served by the API. |
| `js/10-fixtures.js` | The built-in sample data (`SL.FX`): roles, roster, prices, models, providers, rules, skills, MCP servers, sample code and trees, commands. | The catalogues the API returns. |
| `js/11-data-adapter.js` | `SL.D`, the one object the engine reads data from; adopts pieces of `SLDATA` over `SL.FX` when their shape matches. | `SL.D` filled from the API at start-up, same field names. |
| `js/40-scripts.js` | `SL.scripts`: the deterministic timelines of the sample sessions, the generic new-run script, scripted chat and steer replies, the director that schedules verification and the end of the goal. | The live event feed (server-sent events) of each session. |
| `js/50-sessions.js` | `SL.sessions`: the registry, the `Session` class with its two clocks and event log, creation, restart, pruning, the recorded-session list. The clocks, the log and `rebuild`/`collapse` stay; the sources of events and the registry operations change. | A registry over the API: sessions come from the server, events from the feed. |
| `js/60-actions.js` | `SL.act`: every action a person can take, applied to the simulation by inserting synthetic events; `SL.G`, state shared by every session (providers, MCP, trust, schedule, favourites). `SL.settings` (per-viewer) stays. | The same action names, each one a request to the API. |
| `js/92-runner.js` (`BUILTIN`, `exec`) | The runner's simulated outputs for `sim`, `sessions prune`, `trust`, `mcp`, `doctor` and the generic fallback. The form and output pane stay. | Streamed command execution. |
| `js/96b-ws-data.js` | `SL.ws`: file contents, status, ownership, blame and hunks at any checkpoint, computed from the data pack's change sets. | The workspace service of the API (tree, file as of a checkpoint, diff, per-line authorship). |

## Removed or hidden when live

| Where | What |
| --- | --- |
| `index.html`, footer | The `MOCK · sample data` chip (`.mockchip`). |
| `js/95-kit.js` | The `?kit` component catalogue view: a design reference, useful in development, not part of the product. |

## Load order

`index.html` loads, in order: `00-namespace.js` (creates `window.SL`), the data pack (`data.js`, `outputs.js`,
`cli-spec.js`), then the modules in the numeric order of their names (`96-ui-nav.js` before `96b-ws-data.js`).
A replacement keeps its predecessor's position so that every module that reads it at load time still finds it.
