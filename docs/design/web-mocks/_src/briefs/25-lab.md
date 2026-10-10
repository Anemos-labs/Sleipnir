# V5 · LAB: replay, inspect, evaluate

Deliverable: `docs/design/web-mocks/v2/05-lab.html`, title `Sleipnir Lab`.

## Idea
Sleipnir's strength is that everything is an append-only event log: a session can be **replayed, inspected, compared and
scored**. The owner builds RL data pipelines and cares about cache behaviour and evaluation. Lab makes **time and
evidence** the centre: a permanent **timeline dock** under the Cockpit turns the whole UI into a scrubbable instrument
(live <-> replay), and a set of analysis rooms (Inspect, Friction, Sim, Recordings, RL Lab) are first-class, not buried in
a CLI. The live Cockpit is fully functional; the centre of gravity is analysis.

## Information architecture
* **Mode switch** (top-left): `Live` · `Replay` · `Compare` · `Lab`. Session tabs (three live sessions) + recorded sessions
  openable as read-only tabs (including the **real recorded `demo shop` session** from the data pack's `replay.shop`).
* **Timeline dock (always visible, bottom)**: per-worker tracks (8 workers + manager), markers for question, cache break, compaction,
  merge, mail, anomaly, governor 429; playhead (live edge vs scrub), speed, pause (space), range selection; **the entire
  Cockpit follows the playhead** (it is the same event log); drag a range -> `Inspect range` / `Friction in range` / `Export
  range`; bookmarks with notes. Under hover-hold the dock's playhead stays; catch-up respects the core rules.
* **Rooms** (left rail): Cockpit (live hero + Radio) · **Inspect** (the cache dashboard of `sleipnir inspect`: prompt layers per
  request, hit ratios, compactions with the fold, anomalies, swarm coordination, cost against no-cache and naive baselines,
  per-agent drill-down; "est." everywhere) · **Friction** (ranked findings with categories permission/tool/agent/request/cache/
  compaction/file/call; every finding links to its events: click -> the dock jumps and the Cockpit shows the moment) ·
  **Sim** (policy comparison tables and charts from real `sleipnir sim`: compare/scenarios/pins/agents; sliders for agents,
  provider model, seed; "a model, not a benchmark" banner) · **Recordings** (open/replay/export; `replay --record` as an SVG
  preview) · **Doctor** (probe an endpoint, 3 saved endpoints, deep mode) · **RL Lab**.
* **RL Lab** (the deep room): Tasks (taskgen git/mutate/composite/recall/fixture forms via the runner; the task table with kind/
  language/difficulty/tags; validate/filter/split/check), **Rollouts** (a run directory: tasks x G samples as a pass/fail heat grid;
  click an episode -> its trajectory in the dock), **Eval** (pass@k, cost, protocol quality), **Reward** (breakdown with weights,
  re-score with different weights live, detectors), **Report** (pass rate with interval, cost, cache, friction), **Compare** (two
  runs, paired intervals, gates pass/fail), **Export** (trainer data formats; verify wire hashes). Numbers from the data pack.
* **Compare mode**: two sessions or two runs side by side with a shared scrubber and a diff of cost/hit/steps.
* The same floor as every variant (chat, per-agent channels, approvals, goals, team, Files/Changes/Checkpoints, Settings, Tools).
  Files/Changes/Checkpoints live in a `Workspace` room.

## Signature moments
1. Drag the scrubber back 20 s on the live session: legs, gantt, board and chat rewind; "go live" snaps back with the bounded catch-up.
2. Select the be-2 cache break range -> `Inspect range`: layer stack shows G0..G5 paid in full, the explanation, est. cost.
3. Friction: click "same file read 4x" -> the dock jumps to the first read and the Cockpit highlights the agent.
4. Sim: move the agents slider 8 -> 20, watch the table and chart change (real numbers, interpolated only where the binary printed them; label which).
5. RL Lab: the rollout heat grid -> an episode -> its trajectory replays in the dock; Compare two runs, gates turn green/red.
6. Doctor: run the probe against the failing endpoint and read the result.

## Avoid
Charts for their own sake; invented benchmark claims (everything labelled sample/est.; sim is a model); a BI-dashboard look.
