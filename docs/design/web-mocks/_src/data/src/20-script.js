/* ---------------------------------------------------------------------------------------------------------------
 * conversation, channels and the live script of the shop session (SAMPLE text; tool names, refusal wording and
 * formats are REAL). Times are the sample clock (session started 03:04:05, snapshot 03:04:43).
 * kinds in a transcript: say (assistant text), tool (a tool call with its result), mail (in/out), note (harness),
 * user (what the person typed), state (state change), ask (an open question), diff (a change to a file).
 * ------------------------------------------------------------------------------------------------------------- */
var T = function (t, o) { o.t = t; return o; };
var say = function (t, text) { return T(t, { kind: 'say', text: text }); };
var tool = function (t, name, arg, o) { o = o || {}; o.kind = 'tool'; o.tool = name; o.arg = arg; o.label = name.charAt(0).toUpperCase() + name.slice(1); if (!o.status) o.status = 'ok'; return T(t, o); };
var note = function (t, text) { return T(t, { kind: 'note', text: text }); };
var mailIn = function (t, from, text) { return T(t, { kind: 'mail', dir: 'in', from: from, text: text }); };
var mailOut = function (t, to, text, kind) { return T(t, { kind: 'mail', dir: 'out', to: to, text: text, mailKind: kind || 'info' }); };

/* the manager's chat, as the person sees it */
S.conversation = [
  T('03:04:05', { kind: 'user', text: S.goal.command }),
  T('03:04:06', { kind: 'note', glyph: '◇', text: 'goal set: checked after each turn. Esc pauses it; /goal shows where it stands' }),
  T('03:04:06', { kind: 'plan', steps: S.goal.plan.map(function (p) { return p.step; }), text: 'plan: six steps' }),
  T('03:04:09', { kind: 'say', agent: 'mgr', text: 'I\'ll have three scouts map the API, the seed data and the paging conventions, then split the build by directory so no two workers touch the same file.' }),
  T('03:04:09', { kind: 'tool', agent: 'mgr', tool: 'task', label: 'TaskBoard', arg: 'create T1 T2 T3', status: 'ok' }),
  T('03:04:09', { kind: 'tool', agent: 'mgr', tool: 'spawn', label: 'Spawn', arg: 'sc-1 sc-2 sc-3', status: 'ok' }),
  T('03:04:19', { kind: 'report', agent: 'sc-1', task: 'T1', text: 'eleven endpoints; list shapes' }),
  T('03:04:20', { kind: 'report', agent: 'sc-2', task: 'T2', text: '48 items with id, name, price' }),
  T('03:04:20', { kind: 'tool', agent: 'mgr', tool: 'edit', label: 'Edit', arg: 'seed/items.json', status: 'refused', reason: S.managerRefusal }),
  T('03:04:21', { kind: 'report', agent: 'sc-3', task: 'T3', text: 'one-based paging; cursors only on /orders' }),
  T('03:04:21', { kind: 'say', agent: 'mgr', text: 'Eleven endpoints, 48 seed items, paging is one-based with cursors only on /orders. Splitting the build now: be-1 takes the catalogue, be-2 the cart, fe-1 the page, ts-1 the tests.' }),
  T('03:04:21', { kind: 'tool', agent: 'mgr', tool: 'task', label: 'TaskBoard', arg: 'create T4 T5 T6 T7 T8', status: 'ok' }),
  T('03:04:21', { kind: 'tool', agent: 'mgr', tool: 'spawn', label: 'Spawn', arg: 'be-1 be-2 fe-1 ts-1 rv-1', status: 'ok' }),
  T('03:04:21', { kind: 'lease', text: 'lease api/catalog/**, api/server.go → be-1 · api/cart/** → be-2 · web/** → fe-1 · api/**/*_test.go → ts-1' }),
  // the live feed of the workers, one-liners, 03:04:31-03:04:41
  T('03:04:31', { kind: 'feed', agent: 'be-1', text: 'Read api/server.go' }),
  T('03:04:32', { kind: 'feed', agent: 'be-2', text: 'Read api/cart/cart.go' }),
  T('03:04:34', { kind: 'feed', agent: 'be-2', text: 'Edit api/cart/cart.go' }),
  T('03:04:38', { kind: 'feed', agent: 'be-1', text: 'Write api/catalog/items.go +42' }),
  T('03:04:38', { kind: 'feed', agent: 'fe-1', text: 'Write web/shop.js +14' }),
  T('03:04:39', { kind: 'feed', agent: 'be-1', text: 'Edit api/server.go +18 −4' }),
  T('03:04:41', { kind: 'feed', agent: 'be-1', text: 'submitted T4' }),
  T('03:04:41', { kind: 'feed', agent: 'ts-1', text: 'mail → be-1' }),
  T('03:04:42', { kind: 'feed', agent: 'fe-1', text: 'asks: Bash npm install --save-dev vitest' }),
  T('03:04:43', { kind: 'status', text: S.session.statusLine }),
];
S.feedNote = 'The feed lines are what the manager\'s chat shows for workers (one line each). The counts (+42, +18 −4, +14) are the real line counts of the sample files in S.files.shop.';

/* the question (opens 03:04:42, fe-1, 1 waiting) */
S.question = {
  id: 'q1', opensAt: '03:04:42', opensAtLoadOffset: -1, agent: 'fe-1', role: 'frontend', task: 'T6', waiting: 1, tool: 'bash',
  title: 'fe-1 · frontend · T6 wants to run a command',
  operation: 'run a command', command: 'npm install --save-dev vitest', cwd: 'web/',
  scope: 'cwd web/ (inside fe-1\'s lease web/**)',
  why: 'installs a package from the network and edits web/package.json; it is not a build or test command',
  summaryFormat: 'summary [reason]  (REAL: the engine appends the reason to the one-line summary)',
  options: [   // REAL labels (internal/tui/app/chat_dialog.go); the second one names "this command" because npm install is not a runner command
    { n: 1, label: 'Yes', decision: { allow: true, reason: 'allowed by user' }, effect: 'run it once' },
    { n: 2, label: 'Yes, and don\'t ask again for this command this session', decision: { allow: true, reason: 'allowed by user for the session', remember: 'session' }, rule: 'Bash(npm install --save-dev vitest)', effect: 'adds the session rule below and runs it' },
    { n: 3, label: 'No, and tell Sleipnir what to do instead', hint: '(esc)', keys: ['3', 'esc', 'n'], decision: { allow: false, reason: 'denied by user' }, advice: ' (the person said no to this: do not make it another way, with another tool or command; say what you wanted and ask what they want instead)', effect: 'refuses and opens a composer for the instruction' },
  ],
  keys: { answer: ['1', '2', '3'], also: 'up/down or Tab and Enter choose; esc is no; letters never answer', quietPeriodMs: 350, quietNote: 'REAL: defaultAnswerAfter = 350 ms of a quiet keyboard before a question takes keys; the dialog says "your typing goes to the prompt until you pause, so that it cannot answer this"' },
  hint: 'your typing goes to the prompt until you pause',
  moreWaitingText: '1 waiting',
  afterAnswer: { yes: 'fe-1 runs `npm install --save-dev vitest` ✓ 3.1s, then Edit web/shop.js, T6 submitted', no: 'a composer opens ("tell Sleipnir what to do instead"); on send fe-1 goes to think' },
  waitingSince: '03:04:42',
};
S.sessionRule = { // what "2" adds (visible on the Permissions page, origin "don't ask again")
  rule: 'Bash(npm install --save-dev vitest)', list: 'allow', origin: 'don\'t ask again', session: true, addedBy: 'you', via: 'question q1 (fe-1, T6)',
};

/* per-agent channels: transcripts up to the snapshot (03:04:43) */
S.channels = {
  'mgr': { agent: 'mgr', title: 'manager', entries: [
    T('03:04:05', { kind: 'user', text: S.goal.command }),
    say('03:04:09', 'I\'ll have three scouts map the API, the seed data and the paging conventions, then split the build by directory so no two workers touch the same file.'),
    tool('03:04:09', 'task', 'create T1 T2 T3', { label: 'TaskBoard', ms: 3 }),
    tool('03:04:09', 'spawn', 'sc-1 sc-2 sc-3', { ms: 11 }),
    tool('03:04:10', 'wait', 'until T1 T2 T3', { ms: 11200, result: 'T1 T2 T3 done' }),
    say('03:04:20', 'The seed keeps prices as dollars in a float, and the catalogue works in cents. I\'ll convert the seed myself so the catalogue can start from it.'),
    tool('03:04:20', 'edit', 'seed/items.json', { status: 'refused', ms: 1, result: S.managerRefusal, diff: null }),
    say('03:04:21', 'Right: the conversion goes to be-1 with the catalogue. Eleven endpoints, 48 seed items, paging is one-based with cursors only on /orders. Splitting the build now: be-1 takes the catalogue, be-2 the cart, fe-1 the page, ts-1 the tests.'),
    tool('03:04:21', 'task', 'create T4 T5 T6 T7 T8', { label: 'TaskBoard', ms: 4 }),
    tool('03:04:21', 'spawn', 'be-1 be-2 fe-1 ts-1 rv-1', { ms: 14 }),
    tool('03:04:22', 'wait', 'until T4 T5 T6 T7 T8', { status: 'running', result: '', ms: null }),
  ], state: 'wait' },
  'sc-1': { agent: 'sc-1', title: 'scout · T1', entries: [
    note('03:04:10', 'task T1 assigned: survey endpoints (read-only)'),
    say('03:04:10', 'Reading the router and the handlers before I report.'),
    tool('03:04:11', 'read', 'api/server.go', { ms: 2, result: '60 lines' }),
    tool('03:04:12', 'grep', 'HandleFunc api/', { ms: 18, result: '11 matches in 1 file' }),
    tool('03:04:14', 'read', 'api/orders.go', { ms: 2, result: '104 lines' }),
    tool('03:04:19', 'task', 'done T1 "eleven endpoints; list shapes"', { ms: 5 }),
  ], state: 'done' },
  'sc-2': { agent: 'sc-2', title: 'scout · T2', entries: [
    note('03:04:10', 'task T2 assigned: survey seed data (read-only)'),
    tool('03:04:11', 'read', 'seed/items.json', { ms: 2, result: '50 lines' }),
    say('03:04:16', 'Forty-eight items, ids itm-001 to itm-048 in order, prices in dollars as a float; no stock field.'),
    tool('03:04:20', 'task', 'done T2 "48 items with id, name, price"', { ms: 5 }),
  ], state: 'done' },
  'sc-3': { agent: 'sc-3', title: 'scout · T3', entries: [
    note('03:04:10', 'task T3 assigned: survey paging patterns (read-only)'),
    tool('03:04:12', 'read', 'api/orders.go', { ms: 2, result: '104 lines' }),
    tool('03:04:15', 'grep', 'page|cursor|limit api/', { ms: 15, result: '9 matches in 2 files' }),
    mailOut('03:04:31', 'be-1', 'one-based paging, size default 12; cursors exist only on /orders', 'info'),
    tool('03:04:21', 'task', 'done T3 "one-based paging; cursors only on /orders"', { ms: 4 }),
  ].sort(function (a, b) { return a.t < b.t ? -1 : a.t > b.t ? 1 : 0; }), state: 'done' },
  'be-1': { agent: 'be-1', title: 'backend · T4', entries: [
    note('03:04:21', 'task T4 assigned: catalogue: GET /items?page&size · lease api/catalog/**, api/server.go'),
    say('03:04:22', 'T4 needs the seed in cents and a loader before the handler. Reading the server first.'),
    tool('03:04:22', 'read', 'api/server.go', { ms: 2, result: '41 lines' }),
    tool('03:04:23', 'read', 'seed/items.json', { ms: 2, result: '50 lines' }),
    tool('03:04:24', 'write', 'seed/items.json', { ms: 4, added: 48, removed: 48, cp: 'c05', result: 'prices as price_cents' }),
    tool('03:04:25', 'write', 'api/catalog/load.go', { ms: 3, added: 19, removed: 0, cp: 'c05' }),
    mailIn('03:04:31', 'sc-3', 'one-based paging, size default 12; cursors exist only on /orders'),
    say('03:04:32', 'Pages count from 1 and the size defaults to 12. List clamps instead of failing, so a client can never get a 400 for paging.'),
    T('03:04:33', { kind: 'compaction', ref: 'k1', text: S.compactions[0].short + '  · a declared, priced rebase' }),
    mailOut('03:04:36', 'fe-1', 'catalogue: GET /items?page=1&size=12 -> {items[], page, pages, total}', 'contract'),
    tool('03:04:38', 'write', 'api/catalog/items.go', { ms: 4, added: 42, removed: 0, cp: 'c07' }),
    tool('03:04:39', 'edit', 'api/server.go', { ms: 3, added: 18, removed: 4, cp: 'c07', result: 'route /items, handler, catalog field' }),
    tool('03:04:40', 'bash', 'go vet ./api/...', { ms: 812, result: 'no output', exit: 0, allowedBy: 'project allow Bash(go vet:*)' }),
    tool('03:04:41', 'task', 'done T4 "GET /items?page&size: clamped paging, 48 max"', { ms: 6 }),
    note('03:04:41', 'T4 submitted: the merge queue rebases and runs `go test ./api/catalog/...`'),
    mailIn('03:04:41', 'ts-1', 'contract: page 0 and size<=0, 400 or clamp?'),
  ], state: 'wait' },
  'be-2': { agent: 'be-2', title: 'backend · T5', entries: [
    note('03:04:21', 'task T5 assigned: cart: add, remove, total (cents) · lease api/cart/**'),
    say('03:04:29', 'The cart stores PriceCents already; only Total() still does float arithmetic in dollars.'),
    tool('03:04:32', 'read', 'api/cart/cart.go', { ms: 2, result: '39 lines' }),
    tool('03:04:34', 'edit', 'api/cart/cart.go', { ms: 3, added: 3, removed: 3, cp: 'c06', status: 'running', result: 'Total() in cents (int64)' }),
  ], state: 'edit' },
  'fe-1': { agent: 'fe-1', title: 'frontend · T6', entries: [
    note('03:04:21', 'task T6 assigned: shop page: item grid and pager · lease web/**'),
    mailIn('03:04:36', 'be-1', 'catalogue: GET /items?page=1&size=12 -> {items[], page, pages, total}'),
    say('03:04:36', 'The contract is clear. The loader first; the cards and the pager follow once the test runner is installed.'),
    tool('03:04:38', 'write', 'web/shop.js', { ms: 3, added: 14, removed: 0, cp: 'c07', result: 'uncommitted until the question is answered' }),
    tool('03:04:42', 'bash', 'npm install --save-dev vitest', { status: 'asking', cwd: 'web/', result: 'waiting for you' }),
    T('03:04:42', { kind: 'ask', ref: 'q1', text: 'wants to run `npm install --save-dev vitest` (cwd web/)' }),
  ], state: 'ask' },
  'ts-1': { agent: 'ts-1', title: 'tester · T7', entries: [
    note('03:04:21', 'task T7 assigned: tests: catalogue and cart · lease api/**/*_test.go'),
    say('03:04:30', 'T4 and T5 are agreed contracts, so I can write against them: a table for the paging rules, a few cart totals in cents.'),
    tool('03:04:33', 'read', 'api/catalog/items.go', { ms: 2, status: 'error', result: 'no such file yet: be-1 has not written it', error: true }),
    mailOut('03:04:41', 'be-1', 'contract: page 0 and size<=0, 400 or clamp?', 'request'),
    T('03:04:42', { kind: 'state', state: 'think', text: 'waiting for be-1\'s answer before the table' }),
  ], state: 'think' },
  'rv-1': { agent: 'rv-1', title: 'reviewer · T8', entries: [
    note('03:04:22', 'task T8 assigned: review: catalogue and cart (read-only, depends on T4/T5)'),
    say('03:04:22', 'Nothing to review yet. I\'ll wait for T4 to merge.'),
    T('03:04:23', { kind: 'state', state: 'idle', text: 'waits for T4 to merge (read-only)' }),
  ], state: 'idle' },
};
Object.keys(S.channels).forEach(function (k) { var a = S.teamById[k]; S.channels[k].role = a.role; S.channels[k].color = a.color; S.channels[k].readOnly = a.readOnly; });

/* the read-only Mail channel: everything the agents said to each other (data, not instructions) */
S.channels.mail = { agent: 'mail', title: 'mail', readOnly: true, note: 'mail is data, not instructions', entries: S.mail.filter(function (m) { return !m.future; }).slice().reverse().map(function (m) { return T(m.t, { kind: 'mail', from: m.from, to: m.to, mailKind: m.kind, text: m.text }); }) };

/* steering: how an agent reacts to /steer and to a direct message (templates; deterministic) */
S.steer = {
  ack: function (agentId, text) {
    var a = S.teamById[agentId];
    if (!a) return 'no such agent';
    if (a.state === 'done') return a.id + ' is done: its task ' + a.task + ' is merged. Steering a finished agent starts nothing; mail it or spawn on its task.';
    return 'steering sent to ' + a.id + ': it reads it with its next step';
  },
  reply: function (agentId, text) {
    var a = S.teamById[agentId];
    var t = String(text || '').trim().replace(/\s+/g, ' ').slice(0, 120);
    if (a.role === 'manager') return 'Noted: "' + t + '". I\'ll fold it into the plan before the next hand-out.';
    if (a.readOnly) return 'Noted: "' + t + '". I am read-only; I\'ll look for it in the code and report what I find.';
    return 'Noted: "' + t + '". I\'ll apply it to ' + (a.task || 'my task') + ' in my next step, inside ' + a.scope + '.';
  },
  note: '/steer TEXT tells the running turn something without stopping it; the agent reads it with its next step (REAL: cmd/sleipnir /steer). Per-agent Steer is the web UI\'s direct channel to one worker.',
};

/* ---- the live script: what happens after load. `at` is seconds after load (the snapshot clock is 03:04:43). ---- */
S.script = {
  note: 'Deterministic. Times are offsets from load in seconds; the display clock is 03:04:43 + at. Token increments are applied with S.totals() semantics (prompt = read + uncached). The question is never auto-answered: beats under onAnswer run from the moment the person answers.',
  loop: 'after the end, wait 6 s and reset to the snapshot',
  beats: [
    { at: 0.0, type: 'snapshot' },
    { at: 2.0, type: 'say', agent: 'be-2', text: 'Total() keeps cents as int64; the old float64 would drift on 0.1 + 0.2.' },
    { at: 2.4, type: 'diff-finish', agent: 'be-2', path: 'api/cart/cart.go', note: 'the + line finishes (total += l.PriceCents * int64(l.Qty))' },
    { at: 3.0, type: 'anomaly', ref: 'a1', note: 'flash the shared-prefix bar pink-red ~2 s; mark be-2\'s 7th request; the cumulative numbers already include it' },
    { at: 4.0, type: 'mail', from: 'be-1', to: 'ts-1', kind: 'contract', text: 'clamp: page<1 is 1, size in 1..48, page>pages is the last page' },
    { at: 4.2, type: 'state', agent: 'ts-1', state: 'edit', doing: 'creating api/catalog/items_test.go' },
    { at: 4.6, type: 'tool', agent: 'ts-1', tool: 'write', arg: 'api/catalog/items_test.go', added: 46, cp: 'c08' },
    { at: 6.0, type: 'merge-verify', task: 'T4', cmd: 'go test ./api/catalog/...', ok: true, seconds: 0.9 },
    { at: 6.6, type: 'merge', task: 'T4', by: 'be-1', merged: 4 },
    { at: 6.6, type: 'state', agent: 'be-1', state: 'idle', doing: 'T4 merged' },
    { at: 6.8, type: 'compaction', ref: 'k2' },
    { at: 7.0, type: 'state', agent: 'rv-1', state: 'tool', doing: 'Read api/catalog/items.go' },
    { at: 7.2, type: 'tool', agent: 'rv-1', tool: 'read', arg: 'api/catalog/items.go' },
    { at: 8.0, type: 'waiting', note: 'the question is still waiting: show "waiting for you: Ns" counting from 03:04:42; nothing moves for fe-1' },
    { at: 9.0, type: 'mail', from: 'rv-1', to: 'be-1', kind: 'review', text: 'T4 review: ok; document that page>pages is the last page' },
    { at: 9.6, type: 'tool', agent: 'be-1', tool: 'edit', arg: 'api/server.go', added: 1, removed: 0, cp: 'c12', note: 'the handler comment now says a page past the end answers with the last page' },
    { at: 10.0, type: 'state', agent: 'be-2', state: 'tool', doing: 'Remove(id) written; T5 submitted' },
    { at: 10.2, type: 'tool', agent: 'be-2', tool: 'write', arg: 'api/cart/cart.go', added: 11, removed: 0, cp: 'c09' },
    { at: 11.0, type: 'merge-verify', task: 'T5', cmd: 'go test ./api/cart/...', ok: true, seconds: 0.7 },
    { at: 11.6, type: 'merge', task: 'T5', by: 'be-2', merged: 5 },
    { at: 12.0, type: 'mail', from: 'rv-1', to: 'be-2', kind: 'review', text: 'T5 review: ok; Remove of an unknown id returns false, as the tests expect' },
    { at: 12.2, type: 'tool', agent: 'ts-1', tool: 'write', arg: 'api/cart/cart_test.go', added: 35, cp: 'c11' },
    { at: 12.8, type: 'task-submit', task: 'T7', by: 'ts-1' },
    { at: 13.0, type: 'merge-verify', task: 'T7', cmd: 'go test ./api/...', ok: true, seconds: 0.8 },
    { at: 13.4, type: 'merge', task: 'T7', by: 'ts-1', merged: 6 },
  ],
  onAnswer: {   // offsets from the moment the person answers; `tail` is when the run can finish
    yes: [
      { at: 0.2, type: 'state', agent: 'fe-1', state: 'tool', doing: 'npm install --save-dev vitest' },
      { at: 0.2, type: 'tool', agent: 'fe-1', tool: 'bash', arg: 'npm install --save-dev vitest', result: '✓ 3.1s' },
      { at: 3.3, type: 'tool', agent: 'fe-1', tool: 'write', arg: 'web/package.json', added: 11, cp: 'c10' },
      { at: 3.6, type: 'tool', agent: 'fe-1', tool: 'edit', arg: 'web/shop.js', added: 24, cp: 'c10' },
      { at: 4.2, type: 'tool', agent: 'fe-1', tool: 'edit', arg: 'web/index.html web/shop.css', added: 36, cp: 'c10' },
      { at: 5.0, type: 'task-submit', task: 'T6', by: 'fe-1' },
      { at: 5.4, type: 'merge-verify', task: 'T6', cmd: 'make lint', ok: true, seconds: 0.6 },
      { at: 5.8, type: 'merge', task: 'T6', by: 'fe-1', merged: 7 },
    ],
    'yes-always': 'same as yes, and the session rule Bash(npm install --save-dev vitest) is added (S.sessionRule)',
    no: [
      { at: 0.0, type: 'composer', text: 'tell Sleipnir what to do instead' },
      { at: 0.4, type: 'note', text: 'send → fe-1 goes to think; the answer text reaches fe-1 as the refusal\'s instruction' },
      { at: 1.2, type: 'state', agent: 'fe-1', state: 'think', doing: 'rethinking the page without a test runner' },
      { at: 4.0, type: 'tool', agent: 'fe-1', tool: 'edit', arg: 'web/shop.js', added: 24, cp: 'c10' },
      { at: 5.0, type: 'task-submit', task: 'T6', by: 'fe-1' },
      { at: 5.8, type: 'merge', task: 'T6', by: 'fe-1', merged: 7 },
    ],
  },
  end: {
    gate: 'max(load + 14 s, answer + 6 s); the goal judge runs after T6, T7 and T8 are merged',
    beats: [
      { at: 0.0, type: 'task-submit', task: 'T8', by: 'rv-1', note: 'rv-1 reports "reviewed"; T8 merges last' },
      { at: 0.4, type: 'merge', task: 'T8', by: 'rv-1', merged: 8 },
      { at: 0.8, type: 'merge-verify', task: '*', cmd: 'go test ./...', ok: true, seconds: 2.1, note: 'on the merged result' },
      { at: 1.6, type: 'goal', verdict: S.goal.finalJudge },
      { at: 2.0, type: 'plan-all-done' },
      { at: 2.4, type: 'final', line: '-- 51s · 23 steps · $0.11' },
    ],
  },
  tokens: [   // [at, agent, prompt, read, out]  (applied in order; `answer+` means relative to the answer)
    { at: 1.0, agent: 'be-2', prompt: 900,  read: 500,  out: 160 },
    { at: 4.4, agent: 'ts-1', prompt: 1000, read: 900,  out: 700 },
    { at: 5.0, agent: 'be-1', prompt: 800,  read: 740,  out: 90 },
    { at: 6.4, agent: 'rv-1', prompt: 1100, read: 850,  out: 120 },
    { at: 7.0, agent: 'mgr',  prompt: 1250, read: 1100, out: 200 },
    { at: 8.6, agent: 'rv-1', prompt: 900,  read: 840,  out: 150 },
    { at: 9.2, agent: 'be-2', prompt: 1000, read: 930,  out: 480 },
    { at: 9.6, agent: 'be-1', prompt: 850,  read: 800,  out: 160 },
    { at: 10.1, agent: 'be-2', prompt: 1050, read: 990, out: 90 },
    { at: 11.8, agent: 'rv-1', prompt: 1000, read: 940, out: 160 },
    { at: 12.1, agent: 'ts-1', prompt: 1000, read: 940, out: 520 },
    { at: 12.7, agent: 'ts-1', prompt: 1100, read: 1040, out: 100 },
    { at: 13.2, agent: 'mgr',  prompt: 1500, read: 1360, out: 300 },
    { at: 13.5, agent: 'rv-1', prompt: 1100, read: 1040, out: 140 },
    { at: 'answer+0.1', agent: 'fe-1', prompt: 700,  read: 650,  out: 80 },
    { at: 'answer+3.5', agent: 'fe-1', prompt: 1100, read: 1030, out: 520 },
    { at: 'answer+4.9', agent: 'fe-1', prompt: 1150, read: 1090, out: 110 },
    { at: 'answer+6.0', agent: 'mgr',  prompt: 1600, read: 1450, out: 500 },
  ],
};
(function () {   // the end state of the table after every increment (so a builder can assert it) and the final line
  var team = S.clone(S.team);
  var by = {}; team.forEach(function (a) { by[a.id] = a; });
  S.script.tokens.forEach(function (r) { var a = by[r.agent]; a.prompt += r.prompt; a.read += r.read; a.uncached += r.prompt - r.read; a.out += r.out; });
  var t = S.totals(team);
  S.script.finalTotals = { prompt: t.prompt, read: t.read, uncached: t.uncached, out: t.out, hit: t.hit, hitPct: t.hitPct, cost: t.cost, costHeader: t.costText, costText4: t.costText4, savedEst: t.savedEst };
  S.script.finalPerAgent = {}; team.forEach(function (a) { S.script.finalPerAgent[a.id] = { prompt: a.prompt, read: a.read, uncached: a.uncached, out: a.out, hitPct: Math.round(a.read / a.prompt * 100), cost: S.costOf(a) }; });
})();
