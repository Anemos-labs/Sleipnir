/* ---------------------------------------------------------------------------------------------------------------
 * sessions: three live fixtures (shop, orders-api, docs-sweep), 12 recent recorded sessions and 14 older ones (the
 * state directory of the sample user holds 29 sessions, so `sessions prune --older-than 30d --keep 20` has work to do).
 * The listing format is REAL (cmd/sleipnir printSessions); ids, prompts, costs, sizes are SAMPLE.
 * ------------------------------------------------------------------------------------------------------------- */
(function () {
  var ordersTeam = {   // single agent: the rider alone, legs stand
    id: 'main', role: 'single agent', state: 'idle', doing: 'waits at the prompt; turn 2 finished', prompt: 52177, read: 36016, uncached: 16161, out: 692, model: 'anthropic/claude-sonnet-5-5',
  };
  ordersTeam.hit = ordersTeam.read / ordersTeam.prompt; ordersTeam.hitPct = Math.round(ordersTeam.hit * 100);
  ordersTeam.cost = (ordersTeam.uncached * S.prices.manager.inPerM + ordersTeam.read * S.prices.manager.readPerM + ordersTeam.out * S.prices.manager.outPerM) / 1e6;
  S.orders = {
    note: 'REAL: the first turn is the recorded session of docs/media/chat (orders/list.go off-by-one, a compaction, a cache break at request 6); the second turn is SAMPLE (it finds the cache cold). Prices are the sample manager card; the mean of the per-request ratios differs from the cumulative hit by ~1.6 points because the requests differ in size.',
    agent: ordersTeam, requests: 11,
    hitSeries: [0, 0.844, 0.976, 0.997, 0.795, 0.834, 0, 0.978, 0, 0.994, 0.992],   // main.1 .. main.7 of turn 1 (REAL ratios, the compactor request c1 in 5th place), then turn 2 (SAMPLE): its first request finds the cache cold (8 minutes since the last one, TTL 5m)
    marks: [{ req: 5, mark: 'compaction', note: 'thread 1.4k over the hard limit: committed regardless of cost' }, { req: 7, mark: 'break', kind: 'low_hit', note: 'cache break: read 0 of 4.5k expected' }, { req: 9, mark: 'cold', note: 'cold start after 8 minutes: the shared prefix is written again (not an anomaly)' }],
    anomalies: [{ agent: 'main', req: 'main.6', kind: 'low_hit', expected: 4515, actual: 0, missed: 4515, missUSD: 4515 * (S.prices.manager.inPerM - S.prices.manager.readPerM) / 1e6, line: '⚠ cache break [main] (low_hit) · read 0 of 4.5k expected · cost ' + F.usd(4515 * 2.7 / 1e6), explain: 'the prompt prefix did not change: the endpoint did not serve it' }],
    compactions: [{ before: 1363, after: 625, line: '◆ compacted [main] 1.4k → 625  -54%' }],
    layers: { G0: 1900, G1: 2200, G2: 400, G3: 55, G4: 90, G5: 900 },
    checkpoints: [
      { id: 'c01', time: '02:54:49', label: 'turn 1: the pagination test in ./orders is failing, fix it', files: ['orders/list.go'], agents: ['main'] },
      { id: 'c02', time: '03:03:21', label: 'turn 2: yes, add a test for page 0 and a negative size', files: ['orders/list_test.go'], agents: ['main'] },
    ],
    conversation: [
      { t: '02:54:49', kind: 'user', text: 'the pagination test in ./orders is failing, fix it' },
      { t: '02:54:52', kind: 'say', text: 'I\'ll start with the code the test exercises.' },
      { t: '02:54:52', kind: 'tool', tool: 'read', arg: 'orders/list.go', result: '89 lines' },
      { t: '02:54:54', kind: 'say', text: 'Now the tests, to see how they fail.' },
      { t: '02:54:54', kind: 'tool', tool: 'bash', arg: 'go test ./orders/...', result: '--- FAIL: TestListFirstPage (0.00s)\n    list_test.go:32: List(1, 2) holds orders [3 4], want 1 and 2\n--- FAIL: TestListLastPage (0.00s)\n    list_test.go:39: List(3, 2) holds orders [], want 5\nFAIL\nFAIL\texample.com/orders-api/orders\t0.005s\nFAIL\n[exit code 1]', exit: 1 },
      { t: '02:54:56', kind: 'say', text: 'Two pagination tests fail. Let me read what they expect.' },
      { t: '02:54:56', kind: 'tool', tool: 'read', arg: 'orders/list_test.go', result: '53 lines' },
      { t: '02:54:58', kind: 'compaction', text: '◆ compacted 1.4k → 625  -54%  · spine +1 resume · a declared, priced rebase' },
      { t: '02:54:59', kind: 'say', text: 'The offset is the bug: pages count from 1, so page 1 starts at row 0, not at row size. It should be (page - 1) * size.' },
      { t: '02:54:59', kind: 'ask', tool: 'edit', arg: 'orders/list.go', answered: 'allowed by user' },
      { t: '02:55:03', kind: 'tool', tool: 'edit', arg: 'orders/list.go', result: 'Edited orders/list.go: 1 replacement (+1 -1 lines)\n@@ -56,5 +56,5 @@\n \t\treturn nil\n \t}\n-\toffset := page * size\n+\toffset := (page - 1) * size\n \tif offset >= len(s.rows) {\n \t\treturn nil', cp: 'c01' },
      { t: '02:55:04', kind: 'say', text: 'Running the tests again.' },
      { t: '02:55:04', kind: 'tool', tool: 'bash', arg: 'go test ./orders/...', result: 'ok  \texample.com/orders-api/orders\t0.003s\n[exit code 0]', exit: 0 },
      { t: '02:55:06', kind: 'warn', text: '⚠ cache break [main] (low_hit) · read 0 of 4.5k expected · cost ' + F.usd(4515 * 2.7 / 1e6), detail: 'the prompt prefix did not change: the endpoint did not serve it' },
      { t: '02:55:07', kind: 'say', text: 'They pass. One more look, for callers that worked around the old offset.' },
      { t: '02:55:07', kind: 'tool', tool: 'grep', arg: '\\.List\\( *.go', result: 'internal/httpapi/handler.go:25:\t\t"orders": h.Store.List(page, size),\norders/list_test.go:30:\tgot := NewStore(sample).List(1, 2)\norders/list_test.go:37:\tgot := NewStore(sample).List(3, 2)' },
      { t: '02:55:09', kind: 'say', text: '## Fixed\n\n`List` took its offset from the page number, but pages count from 1, so page 1 skipped the first `size` rows.\n\n- the offset is `(page - 1) * size` now\n- `go test ./orders/...` passes, and the HTTP handler already sends one-based pages\n\n```go\noffset := (page - 1) * size\n```\n\nWant a test for page 0 and a negative size?' },
      { t: '02:55:12', kind: 'summary', text: '── 23s · 7 steps · $0.0464' },
      { t: '03:03:21', kind: 'user', text: 'yes, add a test for page 0 and a negative size' },
      { t: '03:03:22', kind: 'note', text: 'the prompt cache went cold while you were away (8 minutes, lifetime 5m): the next request writes the prefix again' },
      { t: '03:03:24', kind: 'say', text: 'I\'ll add table-driven cases for page 0, a negative page and a negative size, next to the tests that are there and in the same style.' },
      { t: '03:03:30', kind: 'tool', tool: 'edit', arg: 'orders/list_test.go', result: 'Edited orders/list_test.go: 1 replacement (+20 -0 lines)', cp: 'c02' },
      { t: '03:03:38', kind: 'tool', tool: 'bash', arg: 'go test ./orders/...', result: 'ok  \texample.com/orders-api/orders\t0.004s\n[exit code 0]', exit: 0 },
      { t: '03:03:42', kind: 'say', text: 'Added TestListEdges: page 0, a negative page, size 0 and a negative size all return nothing, as List\'s doc comment says. `go test ./orders/...` passes.' },
      { t: '03:03:48', kind: 'summary', text: '── 27s · 3 steps · $0.0233' },
    ],
    footer: { left: 'default · ctrl+t stats · / commands', right: 'anthropic/claude-sonnet-5-5 · 20260102-025118-9d4c21' },
    statusLine: '❯ Message Sleipnir, / for commands, @ for files',
    goal: null,
  };
  S.orders.cost = ordersTeam.cost;
})();

/* docs-sweep: a headless team (manager + 2 workers) on ~/projects/handbook. Nobody can answer: an action that needs approval is refused. */
S.docsSweep = (function () {
  var rows = [
    { id: 'mgr',  role: 'manager', state: 'wait',  doing: 'waits for the team (T2 T3)',                       task: null, scope: '- (edits no file)', prompt: 6100, read: 4880, uncached: 1220, out: 640,  model: 'anthropic/claude-sonnet-5-5' },
    { id: 'sc-1', role: 'scout',   state: 'done',  doing: 'nine docs mention commands that no longer exist', task: 'T1', scope: '-',                 prompt: 4200, read: 3570, uncached: 630,  out: 380,  model: 'heimdall/demo-model' },
    { id: 'dc-1', role: 'docs',    state: 'stuck', doing: 'refused: npx prettier --write docs/ (no one to ask)', task: 'T2', scope: 'docs/**',         prompt: 5200, read: 4160, uncached: 1040, out: 910,  model: 'heimdall/demo-model' },
  ];
  rows.forEach(function (a) {
    var p = a.role === 'manager' ? S.prices.manager : S.prices.worker;
    a.hit = a.read / a.prompt; a.hitPct = Math.round(a.hit * 100); a.color = S.roleColors[a.role]; a.short = { manager: 'mgr', scout: 'sc', docs: 'dc' }[a.role];
    a.cost = (a.uncached * p.inPerM + a.read * p.readPerM + a.out * p.outPerM) / 1e6; a.rider = a.role === 'manager'; a.leg = a.rider ? null : a.id === 'sc-1' ? 1 : 2;
  });
  var tot = rows.reduce(function (t, a) { t.prompt += a.prompt; t.read += a.read; t.uncached += a.uncached; t.out += a.out; t.cost += a.cost; return t; }, { prompt: 0, read: 0, uncached: 0, out: 0, cost: 0 });
  tot.hit = tot.read / tot.prompt;
  return {
    id: '20260102-030000-b7e0aa', name: 'docs-sweep', cwd: '~/projects/handbook', branch: 'main',
    command: 'sleipnir run --swarm 2 --mode accept-edits --budget-usd 1 --ask-timeout 10m "sweep the docs for stale commands and fix them"',
    startedBy: 'cron (a crontab line that runs `sleipnir run ...`); the daemon\'s own jobs run `sleipnir run --quiet [--model] [--cwd] [--mode] [--budget-usd] -- GOAL` (REAL: cmd/sleipnir/schedule.go)',
    headless: true, mode: 'accept-edits', swarm: 2, isolation: 'none', verify: null, budgetUsd: 1.00, askTimeout: '10m', model: 'anthropic/claude-sonnet-5-5', workerModel: 'heimdall/demo-model',
    started: '03:00:00', now: '03:04:43', elapsed: '04:43', state: 'running', cost: tot.cost, costText: F.usd(tot.cost), totals: tot,
    team: rows, activeText: '1 active of 2 workers',
    tasks: [
      { id: 'T1', title: 'survey the docs for commands that no longer exist', owner: 'sc-1', status: 'done', phase: 'merged', result: 'nine docs mention commands that no longer exist' },
      { id: 'T2', title: 'fix the stale commands in docs/', owner: 'dc-1', status: 'doing', phase: 'running', note: 'stuck behind a refused command' },
      { id: 'T3', title: 'review: docs changes', owner: null, status: 'todo', phase: 'todo' },
    ],
    refused: [
      { t: '03:04:12', agent: 'dc-1', tool: 'bash', command: 'npx prettier --write docs/', mode: 'accept-edits',
        reason: 'approval required: npx can run any package, so it asks in accept-edits mode (this run has no one to ask, so nothing can be approved: use an action that is allowed, or finish and say which permission you needed)',
        kind: 'no-one-to-ask', realText: 'REAL wording: perm.NoOneToAsk; with a prompter and --ask-timeout 10m the refusal ends "(nobody answered within 10m0s, so nothing was approved: use an action that is allowed, or finish and say which permission you needed)"' },
    ],
    askTimeoutNote: '--ask-timeout 10m refuses a question that nobody has answered in that time and tells the worker so; without it a question waits for the person, however long (REAL: docs/CLI.md "run")',
    outputPending: 'run --quiet prints only the final answer, on stdout, when it ends; progress is in the event log (watch it with `sleipnir watch 20260102-030000-b7e0aa`)',
    hitSeries: { mgr: [0.52, 0.86, 0.9, 0.92], 'sc-1': [0.7, 0.9, 0.95], 'dc-1': [0.62, 0.84, 0.9, 0.84] },
    changed: [{ path: 'docs/install.md', status: 'M', added: 3, removed: 3, agent: 'dc-1' }, { path: 'docs/cli.md', status: 'M', added: 5, removed: 5, agent: 'dc-1' }],
    footer: { left: 'accept-edits · ctrl+t stats · / commands', right: 'anthropic/claude-sonnet-5-5 · 20260102-030000-b7e0aa' },
    log: '~/.sleipnir/schedule-logs/j1-20260102-030000.log',
  };
})();

S.sessions = {};
S.sessions.live = [
  { key: 'shop', id: S.session.id, name: 'shop', cwd: '~/projects/shop', model: S.session.model, workerModel: S.session.workerModel, mode: 'default', swarm: 8, isolation: 'worktree', verify: 'go test {dirs}', commit: false, mailman: false,
    budgetUsd: 5.00, cost: S.totals().cost, state: 'running', question: true, waiting: 1, kind: 'team', project: 'shop', hit: S.totals().hit, started: '03:04:05', lastWritten: '2026-01-02T03:04:43', resumable: true, size: 3.4 * 1048576,
    summary: 'manager + 8 workers · 4 active of 8 workers · a question is waiting (fe-1)' },
  { key: 'orders-api', id: '20260102-025118-9d4c21', name: 'orders-api', cwd: '~/projects/orders-api', model: 'anthropic/claude-sonnet-5-5', workerModel: null, mode: 'default', swarm: 0, isolation: 'none', verify: null, commit: false, mailman: false,
    budgetUsd: null, cost: S.orders.cost, state: 'idle', question: false, waiting: 0, kind: 'single', project: 'orders-api', hit: S.orders.agent.hit, started: '02:51:18', lastWritten: '2026-01-02T03:03:48', resumable: true, size: 1.6 * 1048576,
    summary: 'single agent · turn 2 finished 1m ago · waiting at the prompt' },
  { key: 'docs-sweep', id: S.docsSweep.id, name: 'docs-sweep', cwd: '~/projects/handbook', model: 'anthropic/claude-sonnet-5-5', workerModel: 'heimdall/demo-model', mode: 'accept-edits', swarm: 2, isolation: 'none', verify: null, commit: false, mailman: false,
    budgetUsd: 1.00, cost: S.docsSweep.cost, state: 'running', question: false, waiting: 0, kind: 'team', headless: true, project: 'handbook', hit: S.docsSweep.totals.hit, started: '03:00:00', lastWritten: '2026-01-02T03:04:41', resumable: true, size: 1.1 * 1048576,
    summary: 'headless · manager + 2 workers · 1 refused action · nobody can answer (--ask-timeout 10m)' },
];

/* 12 recent recorded sessions, newest first. lastWritten = the newest file's mtime (what `sessions` sorts by and `prune` ages by). */
var R = function (id, wrote, model, cost, prompt, o) { o = o || {}; return { id: id, lastWritten: wrote, model: model, cost: cost, prompt: prompt, resumable: o.resumable !== false, size: Math.round((o.mb || 1) * 1048576), kind: o.kind || 'single', swarm: o.swarm || 0, project: o.project || 'shop', requests: o.req || 12, files: o.files || 0, ended: o.ended || 'exit' }; };
S.sessions.recorded = [
  R('20260102-021744-e2a07b', '2026-01-02T02:41:09', 'anthropic/claude-sonnet-5-5', 0.1884, 'add request ids to every log line of the api package', { mb: 2.6, req: 31, files: 5, project: 'shop' }),
  R('20260102-004912-6fd3c8', '2026-01-02T01:12:51', 'anthropic/claude-haiku-5-5', 0.0127, 'what does the merge queue do when two tasks touch one file?', { mb: 0.4, req: 6, project: 'shop' }),
  R('20260101-231655-0b9e41', '2026-01-01T23:58:20', 'anthropic/claude-sonnet-5-5', 0.2210, 'add the cart endpoints: POST /cart, DELETE /cart/{id}, GET /cart/total', { mb: 5.7, kind: 'team', swarm: 4, req: 73, files: 6, project: 'shop' }),
  R('20260101-204231-91ac5d', '2026-01-01T21:09:02', 'anthropic/claude-sonnet-5-5', 0.3112, 'migrate the config loader from yaml to json and keep the old keys', { mb: 4.1, req: 64, files: 9, project: 'shop' }),
  R('20260101-171204-7c1e3a', '2026-01-01T17:12:25', 'demo-model', 0.0803, 'Build the shop: a catalogue with paging, a cart that totals in cents, …', { mb: 0.9, kind: 'team', swarm: 8, req: 55, files: 5, project: 'shop', ended: 'other' }),
  R('20260101-142808-3d6e92', '2026-01-01T14:55:37', 'openrouter/sample/frugal-coder-32b', 0.0212, 'explain why TestRetry is flaky and fix it', { mb: 1.2, req: 22, files: 2, project: 'shop' }),
  R('20251231-221903-5a18fe', '2025-12-31T22:48:11', 'anthropic/claude-sonnet-5-5', 0.2046, 'review the diff on branch feature/cart and list the risky parts', { mb: 2.2, req: 18, project: 'shop', resumable: false }),
  R('20251230-101552-c74b20', '2025-12-30T10:40:30', 'anthropic/claude-haiku-5-5', 0.0094, 'write a CHANGELOG entry for v0.4.0 from the merged PRs', { mb: 0.3, req: 5, project: 'orders-api', files: 1 }),
  R('20251228-193047-8e02da', '2025-12-28T20:21:44', 'anthropic/claude-sonnet-5-5', 0.5420, 'add rate limiting to the /login handler and test it', { mb: 7.9, kind: 'team', swarm: 4, req: 88, files: 7, project: 'orders-api' }),
  R('20251224-084410-2fb6c1', '2025-12-24T09:02:17', 'local/qwen-sample-32b', 0.0000, 'summarise the open TODOs in cmd/ and group them by package', { mb: 0.8, req: 14, project: 'orders-api' }),
  R('20251219-163302-a9d457', '2025-12-19T16:58:26', 'anthropic/claude-sonnet-5-5', 0.1675, 'why does go vet complain about copylocks in store.go?', { mb: 1.9, req: 20, files: 1, project: 'orders-api' }),
  R('20251210-120930-47c1be', '2025-12-10T12:31:55', 'heimdall/demo-model', 0.0379, 'rename the Order.Total field to TotalCents and fix the callers', { mb: 3.3, req: 41, files: 6, project: 'orders-api', ended: 'interrupted' }),
];
/* 14 older sessions (all older than 30 days at the sample clock). Only what a listing needs. */
S.sessions.archive = [
  R('20251130-091512-b3f09a', '2025-11-30T09:44:02', 'anthropic/claude-sonnet-5-5', 0.2231, 'port the csv export to streaming writes', { mb: 2.9, req: 27, files: 3, project: 'orders-api' }),
  R('20251127-213855-6e7d12', '2025-11-27T22:10:40', 'anthropic/claude-haiku-5-5', 0.0141, 'rename the package orders to order, update imports', { mb: 0.6, req: 9, files: 12, project: 'orders-api' }),
  R('20251122-140211-d81a5c', '2025-11-22T14:49:09', 'heimdall/demo-model', 0.0560, 'add pagination to GET /orders with a cursor', { mb: 3.8, req: 47, files: 4, project: 'orders-api' }),
  R('20251119-110034-1c4e8b', '2025-11-19T11:20:18', 'anthropic/claude-sonnet-5-5', 0.0918, 'find out why the nightly build takes twice as long', { mb: 1.4, req: 16, project: 'shop', resumable: false }),
  R('20251113-173320-f50b37', '2025-11-13T18:02:55', 'openrouter/sample/frugal-coder-32b', 0.0305, 'make the seed loader tolerate a missing price', { mb: 1.1, req: 19, files: 2, project: 'shop' }),
  R('20251108-082701-92ad6c', '2025-11-08T08:50:33', 'anthropic/claude-sonnet-5-5', 0.4107, 'extract the cache layer behind an interface', { mb: 6.2, kind: 'team', swarm: 4, req: 71, files: 8, project: 'orders-api' }),
  R('20251101-151946-3a7c50', '2025-11-01T16:30:07', 'anthropic/claude-haiku-5-5', 0.0076, 'summarise this stack trace', { mb: 0.2, req: 4, project: 'shop' }),
  R('20251026-195512-7be194', '2025-10-26T20:11:42', 'anthropic/claude-sonnet-5-5', 0.1302, 'add a --dry-run flag to the importer', { mb: 2.0, req: 25, files: 3, project: 'orders-api' }),
  R('20251018-101108-c02f6d', '2025-10-18T10:41:29', 'heimdall/demo-model', 0.0449, 'document the retry policy in AGENTS.md', { mb: 0.9, req: 11, files: 1, project: 'shop' }),
  R('20251009-132244-58e3a1', '2025-10-09T13:55:10', 'anthropic/claude-sonnet-5-5', 0.2764, 'split the 900-line handler file by resource', { mb: 5.1, req: 52, files: 6, project: 'orders-api' }),
  R('20250930-090015-e6d720', '2025-09-30T09:27:01', 'anthropic/claude-haiku-5-5', 0.0112, 'what changed in the last release?', { mb: 0.3, req: 6, project: 'shop' }),
  R('20250921-201733-0f8b4c', '2025-09-21T20:52:48', 'anthropic/claude-sonnet-5-5', 0.1840, 'fix the race in the order book mutex', { mb: 2.4, req: 29, files: 2, project: 'orders-api' }),
  R('20250912-114509-a47d93', '2025-09-12T12:08:12', 'heimdall/demo-model', 0.0230, 'try the new linter config and list what it flags', { mb: 0.7, req: 13, project: 'shop' }),
  R('20250830-160227-2d9e6f', '2025-08-30T16:41:55', 'anthropic/claude-sonnet-5-5', 0.0690, 'bootstrap the repo: go.mod, Makefile, AGENTS.md', { mb: 1.0, req: 15, files: 6, project: 'shop' }),
];
/** everything in the sample state directory, newest first: the 3 live sessions, the 12 recent, the 14 older. */
S.sessions.all = function () {
  var live = S.sessions.live.map(function (l) { return { id: l.id, lastWritten: l.lastWritten, model: l.model, cost: l.cost, prompt: l.key === 'shop' ? S.goal.text : l.key === 'orders-api' ? 'the pagination test in ./orders is failing, fix it' : 'sweep the docs for stale commands and fix them', resumable: true, size: Math.round(l.size), kind: l.kind, swarm: l.swarm, project: l.project, live: true }; });
  return live.concat(S.sessions.recorded, S.sessions.archive).sort(function (a, b) { return a.lastWritten < b.lastWritten ? 1 : a.lastWritten > b.lastWritten ? -1 : 0; });
};
S.sessions.note = 'the first prompt is shown by `sessions` cut to 70 runes with "…"; prune ages by lastWritten against S.meta.sampleNow and keeps the newest N whatever their age (REAL rules: cmd/sleipnir/sessions_prune.go)';

/* docs-sweep channels: the headless team's transcripts (a refused action is a normal event of a run that nobody can answer) */
S.docsSweep.channels = {
  'mgr': { agent: 'mgr', title: 'manager', state: 'wait', entries: [
    { t: '03:00:00', kind: 'user', text: 'sweep the docs for stale commands and fix them' },
    { t: '03:00:04', kind: 'say', text: 'Two jobs: find the commands the docs mention that the program no longer has, then fix them. A scout first, a docs writer after.' },
    { t: '03:00:05', kind: 'tool', tool: 'task', label: 'TaskBoard', arg: 'create T1 T2 T3', status: 'ok', ms: 3 },
    { t: '03:00:05', kind: 'tool', tool: 'spawn', arg: 'sc-1', status: 'ok', ms: 9 },
    { t: '03:01:40', kind: 'say', text: 'Nine docs mention commands that no longer exist. dc-1 takes the fixes, inside docs/**.' },
    { t: '03:01:41', kind: 'tool', tool: 'spawn', arg: 'dc-1', status: 'ok', ms: 9 },
    { t: '03:01:41', kind: 'tool', tool: 'wait', arg: 'until T2 T3', status: 'running' },
  ] },
  'sc-1': { agent: 'sc-1', title: 'scout · T1', state: 'done', entries: [
    { t: '03:00:06', kind: 'note', text: 'task T1 assigned: survey the docs for commands that no longer exist (read-only)' },
    { t: '03:00:08', kind: 'tool', tool: 'grep', arg: 'sleipnir [a-z]+ docs/', status: 'ok', ms: 31, result: '212 matches in 31 files' },
    { t: '03:01:38', kind: 'tool', tool: 'task', arg: 'done T1 "nine docs mention commands that no longer exist"', status: 'ok', ms: 5 },
  ] },
  'dc-1': { agent: 'dc-1', title: 'docs · T2', state: 'stuck', entries: [
    { t: '03:01:42', kind: 'note', text: 'task T2 assigned: fix the stale commands in docs/ · lease docs/**' },
    { t: '03:01:50', kind: 'tool', tool: 'edit', arg: 'docs/install.md', status: 'ok', ms: 3, added: 3, removed: 3 },
    { t: '03:02:20', kind: 'tool', tool: 'edit', arg: 'docs/cli.md', status: 'ok', ms: 3, added: 5, removed: 5 },
    { t: '03:04:12', kind: 'tool', tool: 'bash', arg: 'npx prettier --write docs/', status: 'refused', ms: 2, result: S.docsSweep.refused[0].reason },
    { t: '03:04:14', kind: 'say', text: 'The formatter is not allowed in this run. I will leave the formatting as it is and finish the fixes by hand.' },
  ] },
};
