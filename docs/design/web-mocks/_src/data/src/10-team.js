/* ---------------------------------------------------------------------------------------------------------------
 * team: the roster of 10-v2-shared.md. ONE table (ROWS); every other figure is computed from it.
 * Sample prices (manager $3.00 / cached $0.30 / out $15.00 per M; workers $1.00 / $0.10 / $4.00 per M): SAMPLE.
 * ------------------------------------------------------------------------------------------------------------- */
S.prices = {
  sample: true,
  label: 'sample prices, per million tokens: manager $3.00 uncached / $0.30 cached-read / $15.00 out; workers $1.00 / $0.10 / $4.00',
  manager: { inPerM: 3.00, readPerM: 0.30, outPerM: 15.00 },
  worker:  { inPerM: 1.00, readPerM: 0.10, outPerM: 4.00 },
};
S.roleColors = {            // fixed across the product (the eight legs); the harness service role "mailman" has none
  manager: '#bb9af7', backend: '#7aa2f7', frontend: '#7dcfff', scout: '#73daca', tester: '#9ece6a', docs: '#e0af68',
  reviewer: '#ff9e64', fullstack: '#c0a6f7',
  error: '#f7768e', fg: '#c0caf5', comment: '#565f89'
};
/* roles: REAL names, short codes and read-only flags (internal/swarm/roles.go); pins are the first sentence of each role pin. */
S.roleDefs = [
  { name: 'manager',   short: 'mgr', color: S.roleColors.manager,   readOnly: false, maxSteps: 400, editsFiles: false, pin: 'You are the manager. You coordinate a team: you plan, delegate and review, and workers make every change; you do not edit files yourself.' },
  { name: 'backend',   short: 'be',  color: S.roleColors.backend,   readOnly: false, maxSteps: 150, pin: 'You are a backend engineer. You own server-side code: APIs, data access, services, migrations, background jobs.' },
  { name: 'frontend',  short: 'fe',  color: S.roleColors.frontend,  readOnly: false, maxSteps: 150, pin: 'You are a frontend engineer. You own UI code: components, state, styling, client-side data fetching.' },
  { name: 'fullstack', short: 'fs',  color: S.roleColors.fullstack, readOnly: false, maxSteps: 150, pin: 'You are a full-stack engineer. You handle tasks that cut across backend and frontend and keep the two consistent.' },
  { name: 'tester',    short: 'ts',  color: S.roleColors.tester,    readOnly: false, maxSteps: 120, pin: 'You are a test engineer. You write and run tests, find bugs, and report them precisely.' },
  { name: 'reviewer',  short: 'rv',  color: S.roleColors.reviewer,  readOnly: true,  maxSteps: 80,  pin: 'You are a code reviewer. You are read-only: you cannot modify files.' },
  { name: 'scout',     short: 'sc',  color: S.roleColors.scout,     readOnly: true,  maxSteps: 60,  pin: 'You are a scout. You are read-only. You explore part of the codebase and report a compact map.' },
  { name: 'docs',      short: 'dc',  color: S.roleColors.docs,      readOnly: false, maxSteps: 80,  pin: 'You are a documentation engineer. You write and update documentation, comments and examples so they match the code.' },
];
S.serviceRoles = [{ name: 'mailman', short: 'mm', readOnly: true, maxSteps: 12, onRoster: false, note: 'harness service role (swarm.mailman / --mailman): digests bursts of worker mail; on no roster, board or leg; its model is --role-model mailman=<model>' }];
S.managerRefusal = 'the manager does not edit files: spawn a worker for the change (or reuse an idle one with spawn agent=...), then review its work';   // REAL (internal/swarm/swarm.go managerWritesMsg)

/* The table: id, role, state, doing, task, scope, prompt tokens, cached-read, uncached, out. Everything else derives. */
var ROWS = [
  // id    role        state    doing                                                       task  scope                               prompt  read   uncached out
  ['mgr',  'manager',  'wait',  'waits for the team (T4 T5 T6 T7 T8)',                       '-',  '- (edits no file)',                9400,  7332, 2068, 1200],
  ['be-1', 'backend',  'wait',  'T4 submitted: harness runs `go test ./api/catalog/...`',    'T4', 'api/catalog/**, api/server.go',    8100,  7209,  891, 2300],
  ['be-2', 'backend',  'edit',  'editing api/cart/cart.go: Total() in cents',                'T5', 'api/cart/**',                      8600,  6622, 1978, 2100],
  ['fe-1', 'frontend', 'ask',   'wants to run `npm install --save-dev vitest`',              'T6', 'web/**',                           7200,  6480,  720, 1500],
  ['sc-1', 'scout',    'done',  'eleven endpoints; list shapes',                             'T1', '-',                                6100,  5490,  610,  500],
  ['sc-2', 'scout',    'done',  '48 items with id, name, price',                             'T2', '-',                                6400,  5760,  640,  520],
  ['sc-3', 'scout',    'done',  'one-based paging; cursors only on /orders',                 'T3', '-',                                5800,  5278,  522,  480],
  ['ts-1', 'tester',   'think', 'table tests for the catalogue contract',                    'T7', 'api/**/*_test.go',                 5900,  4897, 1003,  900],
  ['rv-1', 'reviewer', 'idle',  'waits for T4 to merge (read-only)',                         'T8', '- (read-only)',                    2400,  1776,  624,  150],
];
var SHORT = { manager: 'mgr', backend: 'be', frontend: 'fe', fullstack: 'fs', tester: 'ts', reviewer: 'rv', scout: 'sc', docs: 'dc' };
var LEG = { 'sc-1': 1, 'sc-2': 2, 'sc-3': 3, 'be-1': 4, 'be-2': 5, 'fe-1': 6, 'ts-1': 7, 'rv-1': 8 };   // legs 1-8 in start order; the manager rides and takes no leg
var GLYPH = { think: '◇', tool: '⚙', edit: '✎', wait: '◌', ask: '?', idle: '◌', done: '✓', stuck: '⚠' };
var STARTED = { mgr: '03:04:05', 'sc-1': '03:04:10', 'sc-2': '03:04:10', 'sc-3': '03:04:10', 'be-1': '03:04:21', 'be-2': '03:04:21', 'fe-1': '03:04:21', 'ts-1': '03:04:21', 'rv-1': '03:04:22' };
var ROLE_COLOR = S.roleColors;
var LAYERS_TOK = { G0: 1900, G1: 2200, G2: 400, G3: 300, G4: 350 };
var THREAD = { mgr: 1400, 'be-1': 1200, 'be-2': 1100, 'fe-1': 900, 'sc-1': 600, 'sc-2': 620, 'sc-3': 580, 'ts-1': 700, 'rv-1': 600 };

/** cost of one agent from its token counters and the sample price card of its class. */
S.costOf = function (a) {
  var p = a.role === 'manager' ? S.prices.manager : S.prices.worker;
  return (a.uncached * p.inPerM + a.read * p.readPerM + a.out * p.outPerM) / 1e6;
};
S.team = ROWS.map(function (r) {
  var a = {
    id: r[0], role: r[1], short: SHORT[r[1]], color: ROLE_COLOR[r[1]], state: r[2], glyph: GLYPH[r[2]], doing: r[3], task: r[4] === '-' ? null : r[4], scope: r[5],
    scopeGlobs: r[5].indexOf('(') >= 0 || r[5] === '-' ? [] : r[5].split(', '),
    readOnly: r[1] === 'scout' || r[1] === 'reviewer', editsFiles: r[1] !== 'manager' && r[1] !== 'scout' && r[1] !== 'reviewer',
    leg: r[0] === 'mgr' ? null : LEG[r[0]], rider: r[0] === 'mgr',
    model: r[0] === 'mgr' ? 'anthropic/claude-sonnet-5-5' : 'heimdall/demo-model',
    started: STARTED[r[0]],
    prompt: r[6], read: r[7], uncached: r[8], out: r[9],
    layerTokens: { G0: LAYERS_TOK.G0, G1: LAYERS_TOK.G1, G2: LAYERS_TOK.G2, G3: LAYERS_TOK.G3, G4: LAYERS_TOK.G4, G5: THREAD[r[0]] },
  };
  a.hit = a.read / a.prompt;                     // cumulative hit ratio, 0..1
  a.hitPct = Math.round(a.hit * 100);            // what every panel shows
  a.cost = S.costOf(a);
  a.costText = F.usd4(a.cost);
  return a;
});
S.team.forEach(function (a) { if (a.prompt !== a.read + a.uncached) throw new Error('team table: prompt != read + uncached for ' + a.id); });
S.teamById = {}; S.team.forEach(function (a) { S.teamById[a.id] = a; });

/** totals(team): the only way a panel gets an overall figure. savedEst is an ESTIMATE at list price: what the cached reads would have cost uncached, minus what they cost. */
S.totals = function (team) {
  var t = { prompt: 0, read: 0, uncached: 0, out: 0, cost: 0, savedEst: 0 };
  (team || S.team).forEach(function (a) {
    var p = a.role === 'manager' ? S.prices.manager : S.prices.worker;
    t.prompt += a.prompt; t.read += a.read; t.uncached += a.uncached; t.out += a.out;
    t.cost += (a.uncached * p.inPerM + a.read * p.readPerM + a.out * p.outPerM) / 1e6;
    t.savedEst += a.read * (p.inPerM - p.readPerM) / 1e6;
  });
  t.hit = t.prompt ? t.read / t.prompt : 0;
  t.hitPct = Math.round(t.hit * 100);
  t.costText = F.usd(t.cost);          // header: "$0.07"
  t.costText4 = F.usd4(t.cost);        // /cost: "$0.0715"
  t.savedText = F.usd(t.savedEst);     // "$0.06" (est.)
  t.savedText3 = '$' + t.savedEst.toFixed(3);   // "$0.059" (est.)
  return t;
};
/** agentsCount: "manager + 8 workers", "4 active of 8 workers". Active = a worker that is thinking, using a tool, editing, or asking (waiting for verification or idle or done is not active). */
S.teamSummary = function (team) {
  team = team || S.team;
  var workers = team.filter(function (a) { return !a.rider; });
  // "active" = a worker that is thinking, using a tool, editing or asking, or waiting for the harness to verify its submitted task (be-1, T4);
  // idle and done workers are not active. At the snapshot: be-1, be-2, fe-1, ts-1.
  var activeIds = workers.filter(function (a) { return ['think', 'tool', 'edit', 'ask'].indexOf(a.state) >= 0 || (a.state === 'wait' && a.task && S.tasks.some(function (t) { return t.id === a.task && t.phase === 'verify'; })); }).map(function (a) { return a.id; });
  return { manager: 1, workers: workers.length, capacity: 8, active: activeIds.length, activeIds: activeIds, banner: 'manager + ' + workers.length + ' workers', activeText: activeIds.length + ' active of ' + workers.length + ' workers', legs: 8, ninthWorkerSharesLeg: 1 };
};
S.legs = [1, 2, 3, 4, 5, 6, 7, 8].map(function (n) {
  var a = S.team.filter(function (x) { return x.leg === n; })[0];
  return { leg: n, agent: a.id, role: a.role, color: a.color };
});
S.session = {
  id: '20260102-030405-5eed01', name: 'shop', cwd: '~/projects/shop', branch: 'main', started: '03:04:05', now: '03:04:43', elapsed: '00:38',
  mode: 'default', model: 'anthropic/claude-sonnet-5-5', workerModel: 'heimdall/demo-model', provider: 'anthropic (manager) + heimdall (workers)', swarm: 8, isolation: 'worktree',
  verify: 'go test {dirs}', commit: false, mailman: false, budgetUsd: 5.00, effort: 'default', trust: 'trusted (you said yes on Jan 1; hash unchanged)', mcpServers: 2,
  governor: { rpm: 44, r429: 0, retries: 0 }, warmSeconds: 25, cacheTtlSeconds: 25, steps: 14,
  footer: { left: 'default · ctrl+t stats · / commands', right: 'anthropic/claude-sonnet-5-5 · 20260102-030405-5eed01' },
  statusLine: '● manager waiting for the team · 00:38 · ↑9.4k ↓1.2k · $0.07 · esc to interrupt',
  placeholder: 'Message Sleipnir, / for commands, @ for files',
};

/* ---- the board ---------------------------------------------------------------------------------------------- */
/* status: the board's own words (todo doing review done); phase: what the cockpit calls it (todo running verify merged). */
S.tasks = [
  { id: 'T1', title: 'survey endpoints',                         role: 'scout',    owner: 'sc-1', status: 'done',  phase: 'merged',  deps: [],                scope: [],                                    kind: 'task', result: 'eleven endpoints; list shapes',                         readOnly: true,  doneAt: '03:04:19' },
  { id: 'T2', title: 'survey seed data',                         role: 'scout',    owner: 'sc-2', status: 'done',  phase: 'merged',  deps: [],                scope: [],                                    kind: 'task', result: '48 items with id, name, price',                         readOnly: true,  doneAt: '03:04:20' },
  { id: 'T3', title: 'survey paging patterns',                   role: 'scout',    owner: 'sc-3', status: 'done',  phase: 'merged',  deps: [],                scope: [],                                    kind: 'task', result: 'one-based paging; cursors only on /orders',             readOnly: true,  doneAt: '03:04:21' },
  { id: 'T4', title: 'catalogue: GET /items?page&size',          role: 'backend',  owner: 'be-1', status: 'review', phase: 'verify', deps: ['T1', 'T2', 'T3'], scope: ['api/catalog/**', 'api/server.go'],   kind: 'task', note: 'submitted 03:04:41; the merge queue runs the verifier', submittedAt: '03:04:41' },
  { id: 'T5', title: 'cart: add, remove, total (cents)',         role: 'backend',  owner: 'be-2', status: 'doing', phase: 'running', deps: ['T1', 'T2'],       scope: ['api/cart/**'],                       kind: 'task', note: 'editing api/cart/cart.go' },
  { id: 'T6', title: 'shop page: item grid and pager',           role: 'frontend', owner: 'fe-1', status: 'doing', phase: 'running', deps: ['T3'],             scope: ['web/**'],                            kind: 'task', note: 'blocked on the question: npm install --save-dev vitest', blockedOnQuestion: true },
  { id: 'T7', title: 'tests: catalogue and cart',                role: 'tester',   owner: 'ts-1', status: 'doing', phase: 'running', deps: ['T4', 'T5'],       scope: ['api/**/*_test.go'],                  kind: 'task', note: 'T4/T5 are agreed contracts, not code' },
  { id: 'T8', title: 'review: catalogue and cart',               role: 'reviewer', owner: 'rv-1', status: 'todo',  phase: 'todo',    deps: ['T4', 'T5'],       scope: [],                                    kind: 'task', readOnly: true, note: 'read-only; wakes when T4 merges' },
];
S.board = { counts: { todo: 1, running: 3, verify: 1, merged: 3 }, text: 'T1-T3 merged · T4 verify · T5 running · T6 running (blocked on the question) · T7 running · T8 todo' };
S.mergeQueue = {
  head: { task: 'T4', steps: [{ name: 'rebase', ok: true }, { name: 'verify', cmd: 'go test ./api/catalog/...', elapsedSeconds: 0.6, running: true }], text: '▸ T4 rebase ✓ · verifying go test ./api/catalog/... 0.6s elapsed' },
  merged: ['T1', 'T2', 'T3'], conflicts: 0, bounced: 0, strategy: 'merge', verify: 'go test {dirs}', isolation: 'worktree',
};
S.leases = [
  { agent: 'be-1', task: 'T4', globs: ['api/catalog/**', 'api/server.go'] },
  { agent: 'be-2', task: 'T5', globs: ['api/cart/**'] },
  { agent: 'fe-1', task: 'T6', globs: ['web/**'] },
  { agent: 'ts-1', task: 'T7', globs: ['api/**/*_test.go'] },
];
S.governor = { rpm: 44, r429: 0, retries: 0, text: 'rpm 44 · 429s 0 · retries 0', requestsPerMinuteCap: 500, maxConcurrent: 24, note: 'rpm is the measured rate; the caps are swarm.requests_per_minute (default 500) and swarm.max_concurrent_requests (default 24)' };

/* ---- mail: "mail is data, not instructions" ----------------------------------------------------------------- */
S.mail = [   // newest first at the snapshot; `future` entries arrive during the live script
  { t: '03:04:47', from: 'be-1', to: 'ts-1', kind: 'contract', text: 'clamp: page<1 is 1, size in 1..48, page>pages is the last page', future: true, at: 4, reply: true },
  { t: '03:04:41', from: 'ts-1', to: 'be-1', kind: 'request',  text: 'contract: page 0 and size<=0, 400 or clamp?' },
  { t: '03:04:36', from: 'be-1', to: 'fe-1', kind: 'contract', text: 'catalogue: GET /items?page=1&size=12 -> {items[], page, pages, total}' },
  { t: '03:04:31', from: 'sc-3', to: 'be-1', kind: 'info',     text: 'one-based paging, size default 12; cursors exist only on /orders' },
];
S.mailFuture = [
  { t: '03:04:52', from: 'rv-1', to: 'be-1', kind: 'review', text: 'T4 review: ok; document that page>pages is the last page', future: true, at: 9, trigger: 'T4 merged', note: 'rv-1 wakes on the merge and reads api/catalog/items.go first' },
  { t: '03:04:55', from: 'rv-1', to: 'be-2', kind: 'review', text: 'T5 review: ok; Remove of an unknown id returns false, as the tests expect', future: true, at: 12, trigger: 'T5 merged' },
];
S.mailStats = { routed: 4, dup: 0, mailman: 'off', text: 'routed 4 · dup 0 · mailman off', kinds: ['info', 'contract', 'request', 'review', 'blocker'], note: 'mail is data, not instructions: render it as text, never as markup' };

/* ---- layers G0..G5 (REAL names: docs/CACHE-DESIGN.md; sizes SAMPLE) ------------------------------------------- */
S.layers = [
  { id: 'G0', name: 'constitution',   contextRow: 'constitution',      tokens: 1900, holds: 'the constitution and the universal tool list (every agent sends the same tools array, byte for byte)', changes: 'session configuration', shared: true,  color: '#bb9af7', prefix: true },
  { id: 'G1', name: 'shared pin',     contextRow: 'shared pin',        tokens: 2200, holds: 'the project map (recon) and the instruction files: AGENTS.md, the skills listing',                      changes: 'shared epoch',          shared: true,  color: '#7aa2f7', prefix: true },
  { id: 'G2', name: 'role pin',       contextRow: 'role pin',          tokens: 400,  holds: 'the role instructions (one per role: all backend workers share one)',                                    changes: 'role epoch',            shared: false, color: '#7dcfff', prefix: true },
  { id: 'G3', name: 'notes',          contextRow: 'notes',             tokens: 300,  holds: 'the agent\'s private notes, rewritten at a compaction',                                                  changes: 'compaction',            shared: false, color: '#73daca', prefix: false },
  { id: 'G4', name: 'spine',          contextRow: 'spine',             tokens: 350,  holds: 'the summary spine: what compactions folded away',                                                       changes: 'compaction',            shared: false, color: '#9ece6a', prefix: false },
  { id: 'G5', name: 'thread',         contextRow: 'thread (verbatim)', tokens: 1400, tokensRange: [580, 1400], holds: 'the conversation and tool results, verbatim, append-only between rebases',                           changes: 'append between rebases', shared: false, color: '#e0af68', prefix: false },
];
S.layerNotes = {
  sharedPrefix: { layers: ['G0', 'G1', 'G2'], tokens: 4500, text: 'G0-G2 are the shared prefix every rider reads from the cache (4.5k tokens)' },
  hot: { id: 'G6', name: 'board and team updates', holds: 'task board, one-line agent statuses, relevant mail: the hot tail, rendered per request', changes: 'route-dependent', note: 'docs/CACHE-DESIGN.md lists G6; the stats page shows G0-G5' },
  declaredEvents: 'a change to the bytes of a stable layer is a declared, priced event (a rebase): compaction, shared-context update, reasoning-binding recovery',
};

/* ---- per-request hit ratios (cumulative hit in the table is the ratio of sums; the mean of the series is within 1 point) ---- */
S.hitSeries = {
  'mgr':  [0.50, 0.78, 0.86, 0.88, 0.88],
  'be-1': [0.66, 0.93, 0.94, 0.94, 0.93, 0.94],
  'be-2': [0.66, 0.94, 0.94, 0.95, 0.95, 0.95, 0],
  'fe-1': [0.62, 0.95, 0.97, 0.97, 0.99],
  'sc-1': [0.74, 0.98, 0.98],
  'sc-2': [0.74, 0.98, 0.98],
  'sc-3': [0.76, 0.98, 0.99],
  'ts-1': [0.68, 0.90, 0.91],
  'rv-1': [0.55, 0.93],
};
S.seriesMarks = {   // request index (1-based) -> marks, as the cache view draws them
  'be-2': [{ req: 7, mark: 'break', kind: 'low_hit', note: 'cache break: read 0 of 4.5k expected', revealAt: 3 }],
  'be-1': [{ req: 5, mark: 'compaction', note: '◆ compacted 1.4k → 625  -54%', warm: true }],
  'mgr':  [], 'fe-1': [], 'sc-1': [], 'sc-2': [], 'sc-3': [], 'ts-1': [], 'rv-1': [],
};
S.seriesNote = 'Ratios only. The roster\'s cumulative counters are the display model every panel agrees on; they are not the sum of per-request sizes at a 4.5k shared prefix, so the series carries ratios and marks, not token sizes.';

/* ---- anomalies and compactions (formats REAL: internal/tui/app/chat_scroll.go) -------------------------------- */
(function () {
  var expected = 4515, actual = 0, missed = expected - actual;
  var missUSD = missed * (S.prices.worker.inPerM - S.prices.worker.readPerM) / 1e6;     // widget: missed x (input - cache read) price
  S.anomalies = [{
    id: 'a1', agent: 'be-2', req: 'be-2.7', reqIndex: 7, kind: 'low_hit', layer: '', expected: expected, actual: actual, missed: missed,
    missUSD: missUSD, missText: F.usd(missUSD), at: '03:04:40', detectedAfterLoad: 3, severity: 'break',
    line: '⚠ cache break [be-2] (low_hit) · read 0 of ' + F.tok(expected) + ' expected · cost ' + F.usd(missUSD),
    explain: 'the prompt prefix did not change: the endpoint did not serve it',
    feed: 'cache break: expected ~' + F.tok(expected) + ' read, got 0',
    feedDetail: 'the miss cost ' + F.usd(missUSD),
    note: 'already counted in team[be-2] (77%); the cockpit reveals it ~3 s after load (flash the shared-prefix bar, mark the 7th bar of be-2)',
  }];
})();
(function () {
  var before = 1359, after = 625, pct = Math.floor((before - after) * 100 / before);
  S.compactions = [{
    id: 'k1', agent: 'be-1', req: 5, at: '03:04:33', before: before, after: after, pct: pct, moment: 'warm', spineAdded: 1, mode: 'fork',
    line: '◆ compacted [be-1] 1.4k ▓▓▓▓▓▓▓▓▓▓▓▓ → ▒▒▒▒▒▒ 625  -' + pct + '%  · spine +1 resume · a declared, priced rebase',
    short: '◆ compacted 1.4k → 625  -' + pct + '%',
    note: 'a declared, priced rebase at a warm moment; counted: compactions so far 1',
  }];
  S.compactionsFuture = [{ id: 'k2', agent: 'mgr', at: 6, before: 2612, after: 1104, pct: Math.floor((2612 - 1104) * 100 / 2612), moment: 'warm', short: '◆ compacted 2.6k → 1.1k  -57%', note: 'live script t+6 (the first compaction, be-1\'s, is already in the snapshot)' }];
})();
S.sessionTotals = (function () {
  var t = S.totals();
  return {
    inputUncached: t.uncached, cacheRead: t.read, cacheWrite: 0, output: t.out, prompt: t.prompt, hit: t.hit, hitPct: t.hitPct, cost: t.cost,
    costHeader: t.costText, savedEst: t.savedEst, savedText: t.savedText3 + ' (est.)', compactions: 1, anomalies: 1,
    costLine: '/cost (manager row, team total cost): ' + 'input ' + S.teamById.mgr.uncached + ' (uncached) + ' + S.teamById.mgr.read + ' cached-read + 0 cache-write · output ' + S.teamById.mgr.out + ' · hit ' + S.teamById.mgr.hitPct + '% · ' + t.costText4,
    note: 'Overall = sum over the nine agents of the table. In a team, /cost prints the manager\'s tokens beside the TOTAL swarm cost (cmd/sleipnir printCost).',
  };
})();

/* ---- goal ------------------------------------------------------------------------------------------------------ */
S.goal = {
  text: 'Build the shop: a catalogue with pagination, a cart with a running total, and the shop page that shows them. go test ./... must pass.',
  command: '/goal Build the shop: a catalogue with pagination, a cart with a running total, and the shop page that shows them. go test ./... must pass.',
  setAt: '03:04:05', state: 'active', paused: '', continuations: 0, maxContinuations: 20, repeats: 0, maxRepeats: 3,
  plan: [
    { n: 1, step: 'Survey the API, the seed data and the paging conventions', status: 'done',  note: 'T1-T3', ui: 'done' },
    { n: 2, step: 'Catalogue endpoint with pagination',                       status: 'doing', note: 'in verification (T4)', ui: 'in verification' },
    { n: 3, step: 'Cart with a running total, money in cents',                status: 'doing', note: 'editing (T5)', ui: 'editing' },
    { n: 4, step: 'Shop page: item grid and pager',                           status: 'doing', note: 'waiting for an answer (T6)', ui: 'waiting for an answer' },
    { n: 5, step: 'Tests for the catalogue and the cart',                     status: 'pending', note: 'queued behind T4/T5 (T7 is already writing)', ui: 'queued behind T4/T5' },
    { n: 6, step: '`go test ./...` passes on the merged result',              status: 'pending', note: 'pending', ui: 'pending' },
  ],
  judge: { verdict: 'continue', reason: 'not yet: T4-T7 unfinished, and no passing `go test ./...` in the evidence', left: ['T4 verification', 'T5 cart total in cents', 'T6 shop page', 'T7 tests', '`go test ./...` on the merged result'], at: '03:04:41' },
  finalJudge: { verdict: 'done', reason: 'every requirement has evidence: the merged result passes `go test ./...` (2.1s)', left: [], line: '◇ goal met' },
  limits: 'continuations 0 of 20; three turns without progress, or Esc, pause the goal (/goal resume goes on)',
  commands: ['/goal TEXT', '/goal', '/goal pause', '/goal resume', '/goal clear'],
  judgeSystem: 'You judge whether a coding agent has finished a goal. You cannot run anything: you see the goal, the agent\'s plan, its last answer and the results of its last tool calls. ...',   // REAL, first sentences (internal/goal)
};
