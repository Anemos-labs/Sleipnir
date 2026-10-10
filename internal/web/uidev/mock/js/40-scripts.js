/* 40-scripts.js: SL.scripts, the deterministic timelines of the fixture sessions, the generic new-run script and the director.
 *
 * A timeline is an array of events (see 30-model.js) built with the SB helper. A script is a pure function of its inputs: the same call
 * returns the same events, so two runs of the page (held or not, tab switched or not) end in the same state.
 * The DIRECTOR is the only code that adds events while a session runs without a person: it schedules the verification slots in order,
 * and, when every task is merged, the end of the goal. Everything a person does (answer, steer, interrupt, type) goes through actions.js. */
(function (SL) {
  'use strict';
  const D = SL.D, { rng } = SL.u;
  const L0 = 38;                      // the shop snapshot: seconds since the session started
  const ROLE_SEQ = ['scout', 'backend', 'frontend', 'tester', 'reviewer', 'docs', 'fullstack', 'backend', 'scout', 'frontend', 'tester', 'reviewer'];

  /* ---------------- roster ---------------- */
  function legOf(k) { return (k - 1) % 8; }
  function mkAgent(role, nth, k, meta, spawn, scope) {
    const code = D.roles[role].code, id = code + '-' + nth;
    return { id, role, code, nth, k, leg: legOf(k), scope: scope || (D.roles[role].ro ? '- (read-only)' : '**'), ro: D.roles[role].ro, model: (meta.roleModels && meta.roleModels[role]) || D.roleModels[role] || 'heimdall/demo-model', spawn: spawn || 0 };
  }
  function buildRoster(S) {
    const meta = S.meta, out = [{ id: 'mgr', role: 'manager', code: 'mgr', nth: 0, k: 0, leg: -1, scope: meta.swarm ? '- (edits no file)' : '**', ro: false, model: meta.model, spawn: 0 }];
    if (S.kind === 'shop') {
      D.roster.filter(r => r.id !== 'mgr').forEach((r, i) => out.push(Object.assign(mkAgent(r.role, +r.id.split('-')[1], i + 1, meta, r.spawn, r.scope), { id: r.id })));
      for (let k = 9; k <= (meta.swarm || 8); k++) { const role = ROLE_SEQ[(k - 1) % ROLE_SEQ.length]; out.push(mkAgent(role, out.filter(x => x.role === role).length + 1, k, meta, 18 + k)); }
      return out.slice(0, 1 + Math.max(0, meta.swarm == null ? 8 : meta.swarm));
    }
    if (S.kind === 'docs') { for (let k = 1; k <= (meta.swarm == null ? 2 : meta.swarm); k++) out.push(mkAgent('docs', k, k, meta, 4 + k, 'docs/**')); return out; }
    const n = meta.swarm || 0, cnt = {};
    for (let k = 1; k <= n; k++) { const role = ROLE_SEQ[(k - 1) % ROLE_SEQ.length]; cnt[role] = (cnt[role] || 0) + 1; out.push(mkAgent(role, cnt[role], k, meta, 5 + k * 0.7)); }
    return out;
  }

  /* ---------------- builder ---------------- */
  class SB {
    constructor() { this.ev = []; this.vqFree = 0; }
    e(t, k, o) { this.ev.push(Object.assign({ t, k }, o)); return this; }
    say(t, who, text, o) { return this.e(t, 'say', Object.assign({ who, text }, o)); }
    sys(t, glyph, text, o) { return this.e(t, 'say', Object.assign({ who: 'sys', glyph, text }, o)); }
    tool(t, id, name, arg, out, o) { return this.e(t, 'tool', Object.assign({ id, name, arg, out }, o)); }
    note(t, id, g, text, o) { return this.e(t, 'note', Object.assign({ id, g, text }, o)); }
    state(t, id, s, doing, task) { return this.e(t, 'state', { id, s, doing, task }); }
    /** a state change plus the tool call that explains it */
    step(t, id, s, name, arg, out, task, o) { this.state(t, id, s, name + ' ' + arg + ((o && o.add) ? ' +' + o.add : ''), task); return this.tool(t + 0.01, id, name, arg, out, Object.assign({ task }, o)); }
    req(t, id, ratio, o) { return this.e(t, 'req', Object.assign({ id, ratio }, o)); }
    mail(t, from, to, text) { return this.e(t, 'mail', { from, to, text }); }
    task(t, id, s, o) { return this.e(t, 'task', Object.assign({ id, s }, o)); }
    plan(t, n, s) { return this.e(t, 'plan', { n, s }); }
    /** Put a submitted task in the verification queue: the next free slot, `dur` seconds, then merge. Returns the merge time. */
    submit(t, agent, task, cmd, dur, planN, o) {
      const start = Math.max(t + 0.15, this.vqFree), end = start + dur; o = o || {};
      this.state(t, agent, 'wait', task + ' submitted: harness runs `' + cmd + '`', task); this.note(t + 0.02, agent, 'done', 'submitted ' + task, { task });
      this.task(t + 0.03, task, 'verify', { own: agent }); if (planN != null) this.e(t + 0.03, 'plan', { n: planN, s: 'verify', own: agent });
      this.e(start, 'queue', { head: task, cmd, step: 'verifying', own: agent });
      this.e(end - 0.3, 'queue', { head: task, cmd, step: 'verified', ms: Math.round(dur * 1000), own: agent });
      this.e(end, 'merge', { id: task, ms: Math.round(dur * 1000), cmd, own: agent });
      this.state(end, agent, 'idle', o.idleDoing || 'waits for work', task);
      if (planN != null && !o.keepPlan) this.e(end, 'plan', { n: planN, s: 'done', own: agent });
      this.e(end + 0.6, 'queue', { head: null, own: agent });
      this.vqFree = end + 0.7; return end;
    }
  }

  /* hit ratio of a live request: the plateau of the agent's series, deterministic jitter */
  function liveRatio(id, n) {
    const s = D.hitSeries[id] || [0.9], mid = s.length > 2 ? s.slice(1, -1).concat(id === 'be-2' ? [] : s.slice(-1)) : s.slice(-1);
    const base = mid.reduce((a, b) => a + b, 0) / mid.length, r = rng(SL.u.hash(id) + n * 977)();
    return Math.round(Math.min(0.985, Math.max(0.6, base + (r - 0.5) * 0.03)) * 100) / 100;
  }
  const promptPer = id => { const r = D.roster.find(x => x.id === id); return r ? Math.round(r.prompt / (D.nreq[id] || 4)) : 1300; };
  const outPer = id => { const r = D.roster.find(x => x.id === id); return r ? Math.round(r.out / (D.nreq[id] || 4)) : 300; };

  /* ======================================================================== the shop (manager + 8 workers, snapshot at 00:38) */
  function shopScript(S) {
    const b = new SB(), L = tau => L0 + tau;
    const roster = D.roster, T = D.tasks;
    // ---- manager, before the snapshot
    b.say(0, 'you', '/goal ' + D.goal);
    b.sys(1, '◇', 'goal set; plan:', { plan: true }); b.e(1, 'verdict', { text: 'not yet: no work is done' });
    b.say(4, 'mgr', 'I’ll have three scouts map the API, the seed data and the paging conventions, then split the build by directory so no two workers touch the same file.');
    b.tool(4.2, 'mgr', 'TaskBoard', 'create T1 T2 T3', 'created T1 survey endpoints, T2 survey seed data, T3 survey paging patterns');
    ['T1', 'T2', 'T3'].forEach(t => b.task(4.4, t, 'running', { title: T[t].title, owner: T[t].owner, deps: T[t].deps, scope: T[t].scope }));
    b.tool(4.6, 'mgr', 'Spawn', 'sc-1 sc-2 sc-3', '3 scouts started (read-only, heimdall/demo-model), legs 1 to 3');
    // ---- scouts
    b.step(5, 'sc-1', 'tool', 'Glob', 'api/**', '11 files', 'T1'); b.state(9, 'sc-1', 'think', 'listing endpoints', 'T1');
    b.step(11, 'sc-1', 'tool', 'Read', 'api/server.go', '212 lines', 'T1'); b.state(15, 'sc-1', 'done', 'eleven endpoints; list shapes', 'T1');
    b.step(5, 'sc-2', 'tool', 'Read', 'seed/items.json', '48 items', 'T2'); b.state(8, 'sc-2', 'think', 'counting items', 'T2');
    b.step(12, 'sc-2', 'tool', 'Grep', 'price seed/', '48 matches', 'T2'); b.state(15.5, 'sc-2', 'done', '48 items with id, name, price', 'T2');
    b.step(5, 'sc-3', 'tool', 'Grep', 'page api/', '7 matches in 3 files', 'T3'); b.state(7, 'sc-3', 'think', 'comparing handlers', 'T3');
    b.step(13, 'sc-3', 'tool', 'Read', 'api/server.go', '212 lines', 'T3'); b.state(16, 'sc-3', 'done', 'one-based paging; cursors only on /orders', 'T3');
    b.task(15, 'T1', 'merged'); b.task(15.5, 'T2', 'merged'); b.task(16, 'T3', 'merged'); b.plan(15, 0, 'done');
    b.say(16, 'scouts', '', { lines: [['sc-1', 'eleven endpoints; list shapes'], ['sc-2', '48 items with id, name, price'], ['sc-3', 'one-based paging; cursors only on /orders']] });
    b.say(16.4, 'mgr', 'Eleven endpoints, 48 seed items, paging is one-based with cursors only on /orders. Splitting the build now: be-1 takes the catalogue, be-2 the cart, fe-1 the page, ts-1 the tests, rv-1 reviews both when they land.');
    b.tool(16.6, 'mgr', 'TaskBoard', 'create T4 T5 T6 T7 T8', 'created T4 catalogue, T5 cart, T6 shop page, T7 tests, T8 review (todo: waits for T4 and T5)');
    ['T4', 'T5', 'T6', 'T7'].forEach(t => b.task(16.7, t, 'running', { title: T[t].title, owner: T[t].owner, deps: T[t].deps, scope: T[t].scope }));
    b.task(16.7, 'T8', 'todo', { title: T.T8.title, owner: 'rv-1', deps: T.T8.deps, scope: T.T8.scope });
    b.tool(16.9, 'mgr', 'Spawn', 'be-1 be-2 fe-1 ts-1 rv-1', '5 workers started, each in its own git worktree, legs 4 to 8');
    b.sys(17.1, '⚙', 'lease  api/catalog/** → be-1  ·  api/cart/** → be-2  ·  web/** → fe-1');
    b.tool(17.4, 'mgr', 'Edit', 'api/server.go', '', { refused: true, reason: 'the manager edits no file: spawn a worker (be-1 holds the lease on api/server.go)' });
    b.e(17, 'verdict', { text: 'not yet: T4-T8 unfinished, and no passing `go test ./...` in the evidence' });
    b.plan(16.8, 2, 'edit'); b.plan(16.8, 4, 'queued'); b.plan(35, 1, 'verify'); b.plan(37, 3, 'ask');
    b.state(4, 'mgr', 'tool', 'TaskBoard create T1 T2 T3 · Spawn sc-1 sc-2 sc-3'); b.state(0, 'mgr', 'think', 'reading the goal, drafting the plan'); b.state(6, 'mgr', 'wait', 'waits for the scouts');
    b.state(16, 'mgr', 'think', 'splitting the build by directory'); b.state(18, 'mgr', 'tool', 'TaskBoard create T4 T5 T6 T7 T8 · Spawn be-1 be-2 fe-1 ts-1 rv-1'); b.state(19.5, 'mgr', 'wait', 'waits for the team (T4 T5 T6 T7 T8)');
    // ---- workers, before the snapshot
    b.state(16.9, 'be-1', 'think', 'reading the scouts’ notes', 'T4'); b.step(26.2, 'be-1', 'tool', 'Read', 'api/server.go', '212 lines', 'T4');
    b.step(27.2, 'be-1', 'edit', 'Write', 'api/catalog/items.go', 'wrote 31 lines', 'T4', { file: 'api/catalog/items.go', add: 31, del: 0 });
    b.step(29.8, 'be-1', 'edit', 'Edit', 'api/server.go', 'routes GET /items', 'T4', { file: 'api/server.go', add: 7, del: 0 });
    b.note(33.4, 'be-1', 'done', 'submitted T4', { task: 'T4' }); b.state(35, 'be-1', 'wait', 'T4 submitted: harness runs `go test ./api/catalog/...`', 'T4'); b.task(35, 'T4', 'verify');
    b.state(16.9, 'be-2', 'think', 'reading the cart', 'T5'); b.step(28.2, 'be-2', 'tool', 'Read', 'api/cart/cart.go', '84 lines', 'T5');
    b.step(30.4, 'be-2', 'edit', 'Edit', 'api/cart/cart.go', 'in progress', 'T5', { file: 'api/cart/cart.go', add: 4, del: 4 }); b.state(30.5, 'be-2', 'edit', 'editing api/cart/cart.go: Total() in cents', 'T5');
    b.state(16.9, 'fe-1', 'think', 'reading web/index.html', 'T6'); b.step(23, 'fe-1', 'tool', 'Read', 'web/index.html', '31 lines', 'T6');
    b.step(32.5, 'fe-1', 'edit', 'Write', 'web/shop.js', 'wrote 14 lines', 'T6', { file: 'web/shop.js', add: 14, del: 0 });
    b.state(37, 'fe-1', 'ask', 'wants to run `npm install --save-dev vitest`', 'T6');
    b.state(16.9, 'ts-1', 'think', 'table tests for the catalogue contract', 'T7'); b.step(30, 'ts-1', 'tool', 'Read', 'api/catalog/items.go', '37 lines', 'T7'); b.state(30.2, 'ts-1', 'think', 'table tests for the catalogue contract', 'T7');
    b.state(16.9, 'rv-1', 'idle', 'waits for T4 to merge (read-only)', 'T8');
    // ---- requests (the cache clock refills on each); history only sets the ratio series, tokens come from `use`
    const REQ_T = { mgr: [0.5, 4, 16, 18.5, 37.9], 'sc-1': [5.5, 9, 12.5], 'sc-2': [5.6, 9.5, 13.5], 'sc-3': [5.7, 8, 14], 'be-1': [17, 21, 25.5, 28, 31, 34.5], 'be-2': [17, 22, 27, 30, 33, 36, 37.6], 'fe-1': [17.2, 23, 29, 33, 36.5], 'ts-1': [17.4, 28, 36.2], 'rv-1': [17.6, 30] };
    Object.keys(REQ_T).forEach(id => REQ_T[id].forEach((t, i) => b.req(t, id, D.hitSeries[id][i], { hist: true })));
    roster.forEach(a => {
      for (let t = a.spawn + 2; t < L0; t += 2) { const f = Math.pow((t - a.spawn) / (L0 - a.spawn), 1.15); b.e(t, 'use', { id: a.id, rd: a.read * f, un: (a.prompt - a.read) * f, out: a.out * f }); }
      b.e(L0 - 0.01, 'use', { id: a.id, rd: a.read, un: a.prompt - a.read, out: a.out });
    });
    [[8, 4], [14, 11], [20, 22], [26, 31], [32, 38], [36.5, 44]].forEach(([t, r]) => b.e(t, 'gov', { rpm: r }));
    b.mail(26, 'sc-3', 'be-1', 'one-based paging, size default 12; cursors exist only on /orders');
    b.mail(31, 'be-1', 'fe-1', 'catalogue: GET /items?page=1&size=12 -> {items[], page, pages, total}');
    b.mail(36, 'ts-1', 'be-1', 'contract: page 0 and size<=0, 400 or clamp?');
    const PK = (D.extra.checkpoints || []).reduce((o, c) => { o[c.id] = c; return o; }, {});
    if (PK.c12) [['c01', 0.2], ['c02', 4], ['c03', 5], ['c04', 7], ['c05', 16], ['c06', 24], ['c07', 33]].forEach(([id, t]) => { const c = PK[id]; b.e(t, 'ckpt', c.skipped ? { cid: id, ts: c.time, files: 0, note: c.note || c.label, skipped: true } : { step: id, ts: c.time, files: c.filesCount, note: c.label, skipped: false }); });   // the data pack's checkpoints; ids are numbered by arrival
    else D.ckpts.slice().reverse().forEach(c => b.e(c.id === 'c07' ? 33 : c.id === 'c06' ? 24 : c.id === 'c05' ? 16 : 7, 'ckpt', { cid: c.id, ts: c.ts, files: c.files, note: c.note, skipped: c.skipped }));
    b.e(37, 'ask', { q: { id: 'q1', agent: 'fe-1', task: 'T6', cmd: 'npm install --save-dev vitest', cwd: 'web/', why: (D.extra.question && D.extra.question.why) || 'installs a package from the network and edits web/package.json; it is not a build or test command', scope: (D.extra.question && D.extra.question.scope) || 'cwd web/ (inside fe-1\'s lease web/**)', what: 'this command', rule: (D.extra.sessionRule && D.extra.sessionRule.rule) || 'Bash(npm install --save-dev vitest)', cont: 'shop-t6' } });
    b.e(L0 - 0.003, 'warm', {});
    // the verification queue holds T4 at the snapshot (0.6 s elapsed); merge below
    b.e(37.4, 'queue', { head: 'T4', cmd: 'go test ./api/catalog/...', step: 'verifying' });

    // ---- the live script (seconds after the snapshot)
    b.e(L(2), 'stream', { id: 'be-2', text: 'Total() keeps cents as int64; the old float64 would drift on 0.1 + 0.2.', rate: 46 });
    b.e(L(2.2), 'diff', { file: 'api/cart/cart.go', done: true });
    b.e(L(3), 'break', { id: 'be-2', kind: 'low_hit', read: 0, expected: 4500, why: 'the prompt prefix did not change: the endpoint did not serve it' });
    b.mail(L(4), 'be-1', 'ts-1', 'clamp: page<1 is 1, size in 1..48, page>pages is the last page');
    b.step(L(4.2), 'ts-1', 'edit', 'Write', 'api/catalog/items_test.go', 'creating the table test', 'T7', { file: 'api/catalog/items_test.go' });
    b.state(L(4.25), 'ts-1', 'edit', 'creating api/catalog/items_test.go', 'T7');
    b.e(L(4.3), 'stream', { id: 'ts-1', text: D.streamTest, rate: 90, code: true });
    b.req(L(4.6), 'ts-1', liveRatio('ts-1', 4), { p: promptPer('ts-1'), o: outPer('ts-1') });
    b.e(L(5), 'compact', { id: 'be-1', from: 1400, to: 625, pct: -54 }); b.req(L(5.02), 'be-1', liveRatio('be-1', 7), { p: promptPer('be-1'), o: outPer('be-1') });
    // T4 merges at L(6): verification began at 37.4
    b.e(L(5.7), 'queue', { head: 'T4', cmd: 'go test ./api/catalog/...', step: 'verified', ms: Math.round((L(6) - 37.4) * 1000) });
    b.e(L(6), 'merge', { id: 'T4', ms: Math.round((L(6) - 37.4) * 1000), cmd: 'go test ./api/catalog/...' }); b.state(L(6), 'be-1', 'idle', 'waits for work', 'T4'); b.plan(L(6), 1, 'done');
    const ck = (t, step) => { const c = PK[step]; b.e(t, 'ckpt', { step, ts: SL.u.tod(S.meta.t0, t), files: c.filesCount, note: c.label, skipped: false }); };
    if (PK.c12) ck(L(8.2), 'c08'); else b.e(L(6.2), 'ckpt', { cid: 'c08', ts: SL.u.tod(S.meta.t0, L(6.2)), files: 3, note: 'T4 merged', skipped: false });
    b.e(L(6.6), 'queue', { head: null }); b.vqFree = L(7);
    // rv-1 wakes when T4 has merged
    b.task(L(6.4), 'T8', 'running');
    b.step(L(6.5), 'rv-1', 'tool', 'Read', 'api/catalog/items.go', '37 lines', 'T8'); b.req(L(6.6), 'rv-1', liveRatio('rv-1', 3), { p: promptPer('rv-1'), o: outPer('rv-1') });
    b.state(L(7.8), 'rv-1', 'think', 'reviewing T4: clamping and the last page', 'T8');
    b.mail(L(9.5), 'rv-1', 'be-1', 'T4 review: ok; document that page>pages is the last page');
    b.state(L(10), 'rv-1', 'wait', 'T4 reviewed; waits for T5 to merge', 'T8');
    b.req(L(11), 'ts-1', liveRatio('ts-1', 5), { p: promptPer('ts-1'), o: outPer('ts-1') });
    if (PK.c12) {
      b.step(L(10.2), 'be-2', 'edit', 'Edit', 'api/cart/cart.go', 'Remove(id)', 'T5', { file: 'api/cart/cart.go', add: 11, del: 0 }); ck(L(10.8), 'c09'); b.state(L(11), 'be-2', 'think', 'checking Remove against the seed prices', 'T5');
      b.step(L(11.4), 'be-1', 'edit', 'Edit', 'api/server.go', 'documents page>pages', 'T4', { file: 'api/server.go', add: 1, del: 0 }); ck(L(12), 'c12'); b.state(L(12.2), 'be-1', 'idle', 'waits for work', 'T4');
    }
    // T5, T7, T8 go through the queue in order; T6 waits for the person
    const t5 = b.submit(L(14), 'be-2', 'T5', 'go test ./api/cart/...', 0.7, 2); b.req(L(14.2), 'be-2', liveRatio('be-2', 8), { p: promptPer('be-2'), o: outPer('be-2') });
    b.step(t5 + 0.3, 'rv-1', 'tool', 'Read', 'api/cart/cart.go', '84 lines', 'T8'); b.state(t5 + 1.4, 'rv-1', 'think', 'reviewing T5: total in cents', 'T8');
    b.mail(t5 + 3.2, 'rv-1', 'be-2', 'T5 review: ok; Total() in cents matches the seed prices');
    if (PK.c12) { b.step(L(15.6), 'ts-1', 'edit', 'Write', 'api/cart/cart_test.go', 'cart table test', 'T7', { file: 'api/cart/cart_test.go', add: 35, del: 0 }); ck(L(16.2), 'c11'); }
    const t7 = b.submit(L(16.5), 'ts-1', 'T7', 'go test ./api/...', 1.1, 4, { keepPlan: true }); b.note(L(16.7), 'ts-1', 'edit', 'items_test.go: 5 cases', { task: 'T7' });
    const t8s = Math.max(t5 + 3.6, L(18.5));
    b.e(t8s, 'say', { who: 'sys', glyph: '◇', text: 'rv-1 submitted T8: review, read-only, nothing to verify' }); b.note(t8s + 0.01, 'rv-1', 'done', 'submitted T8', { task: 'T8' });
    b.task(t8s + 0.02, 'T8', 'verify'); b.state(t8s + 0.02, 'rv-1', 'wait', 'T8 submitted: a review has no tests to run', 'T8');
    const t8e = Math.max(t8s + 0.6, b.vqFree); b.e(t8e, 'queue', { head: 'T8', cmd: 'review: read-only, nothing to verify', step: 'verified', ms: 500 });
    b.e(t8e + 0.2, 'merge', { id: 'T8', ms: 500, cmd: 'review (read-only)' }); b.plan(t8e + 0.2, 4, 'done'); b.state(t8e + 0.2, 'rv-1', 'done', 'review done: T4 and T5 ok', 'T8'); b.e(t8e + 0.8, 'queue', { head: null }); b.vqFree = t8e + 1;
    b.e(L(2.4), 'gov', { rpm: 47 }); b.e(L(9), 'gov', { rpm: 41 }); b.e(L(18), 'gov', { rpm: 38 });
    b.vqFreeEnd = b.vqFree;
    return { events: b.ev, vqFree: b.vqFree, wt0: L0 };
  }

  /* ======================================================================== orders-api: a single agent, its turn just finished */
  function ordersScript(S) {
    const b = new SB(), WT0 = 813;
    b.say(0, 'sys', 'orders-api · single agent (--swarm 0) · mode accept-edits · ready', { glyph: '◇' });
    b.say(700, 'you', 'orders pagination looks off by one: page 1 returns the second page. fix it and add a test');
    b.state(700.5, 'mgr', 'think', 'reading orders/list.go');
    b.step(702, 'mgr', 'tool', 'Read', 'orders/list.go', '58 lines'); b.req(702.2, 'mgr', 0.62, { hist: true });
    b.step(706, 'mgr', 'tool', 'Grep', 'List( orders/', '4 matches'); b.say(709, 'mgr', 'The offset is page*size, so page 1 starts at the second page. Pages count from 1 everywhere else, so it is (page-1)*size.');
    b.step(713, 'mgr', 'edit', 'Edit', 'orders/list.go', '1 line changed', null, { file: 'orders/list.go', add: 1, del: 1 });
    b.step(719, 'mgr', 'edit', 'Edit', 'orders/list_test.go', 'added TestListEdges', null, { file: 'orders/list_test.go', add: 20, del: 0 });
    b.step(726, 'mgr', 'tool', 'Bash', 'go test ./orders/...', 'ok  example.com/orders  0.41s'); b.req(726.2, 'mgr', 0.93, { hist: true });
    b.say(731, 'mgr', 'Fixed: List now starts at (page-1)*size and clamps the upper bound; TestListEdges covers page 0, a negative page, size 0 and a negative size. go test ./orders/... passes.');
    b.state(731, 'mgr', 'wait', 'waits at the prompt', null);
    b.e(738, 'final', {});
    const OS = (D.extra.files && D.extra.files.orders && D.extra.files.orders.steps) || null;
    if (OS) { b.e(714, 'ckpt', { step: 'c01', ts: OS[0].time, files: 1, note: 'orders: one-based pages', skipped: false }); b.e(720, 'ckpt', { step: 'c02', ts: OS[1].time, files: 1, note: 'orders: page 0 and a negative size', skipped: false }); }
    else { b.e(738.5, 'ckpt', { cid: 'c02', ts: '03:03:48', files: 2, note: 'orders: one-based pages', skipped: false }); b.e(740, 'ckpt', { cid: 'c01', ts: '03:03:50', files: 0, note: 'skipped: nothing to put back', skipped: true }); }
    b.req(0.5, 'mgr', 0.55, { hist: true }); b.req(710, 'mgr', 0.88, { hist: true }); b.req(730, 'mgr', 0.9, { hist: true });
    for (let t = 2; t < WT0; t += 25) { const f = t / WT0; b.e(t, 'use', { id: 'mgr', rd: 10752 * f, un: 2048 * f, out: 1150 * f }); }
    b.e(WT0 - 0.01, 'use', { id: 'mgr', rd: 10752, un: 2048, out: 1150 }); b.e(WT0 - 0.003, 'warm', {});
    [[300, 6], [700, 18], [726, 22], [800, 4]].forEach(([t, r]) => b.e(t, 'gov', { rpm: r }));
    return { events: b.ev, vqFree: 0, wt0: WT0 };
  }

  /* ======================================================================== docs-sweep: a headless job (nobody can answer) */
  const DOC_FILES = ['docs/CLI.md', 'docs/BUILDING.md', 'docs/TESTING.md', 'docs/UX.md', 'docs/CACHE-DESIGN.md', 'docs/ROADMAP.md', 'docs/SECURITY.md', 'docs/EXTENDING.md', 'docs/CONFIGURATION.md', 'docs/VALIDATION.md', 'docs/MAINTENANCE.md', 'docs/ARCHITECTURE.md', 'docs/REPO-SETUP.md', 'docs/TRAINING-DATA.md'];
  const DOCS_PLAN = ['List the docs and their links', 'Fix stale command names', 'Fix broken relative links', 'Check every link of every file', 'Merge each file after its check', 'Write the sweep report'];
  function docsScript(S) {
    const b = new SB(), WT0 = 283, n = DOC_FILES.length, ids = ['dc-1', 'dc-2'];
    b.say(0, 'you', 'run: Sweep docs/: fix stale command names and broken relative links');
    b.sys(1, '◇', 'headless job from the scheduler: nobody can answer a question; an action that needs approval is refused (--ask-timeout 10m)');
    b.say(4, 'mgr', 'Fourteen files under docs/. dc-1 takes the even ones and dc-2 the odd ones; I merge each file after its link check.');
    b.tool(4.2, 'mgr', 'TaskBoard', 'create T1..T' + n, 'created T1..T' + n + ', one per docs file');
    DOC_FILES.forEach((f, i) => b.task(4.4, 'T' + (i + 1), 'running', { title: 'sweep ' + f.replace('docs/', ''), owner: ids[i % 2], deps: [], scope: f }));
    b.tool(4.6, 'mgr', 'Spawn', 'dc-1 dc-2', '2 workers started, each in its own git worktree, legs 1 and 2');
    b.state(0, 'mgr', 'think', 'reading the goal'); b.state(4, 'mgr', 'wait', 'waits for the team (T1..T' + n + ')', null);
    b.plan(5, 0, 'done'); b.plan(5.2, 1, 'act'); b.plan(5.2, 2, 'act');
    DOC_FILES.forEach((f, i) => {
      const id = ids[i % 2], T = 'T' + (i + 1), s = 8 + Math.floor(i / 2) * 55 + (i % 2) * 9;
      b.step(s, id, 'tool', 'Read', f, (180 + i * 37) + ' lines', T); b.req(s + 0.4, id, liveRatio(id, i), { hist: s < WT0, p: 1500, o: 320 });
      if (i === 11) { b.state(s + 3, id, 'tool', 'Bash npm run lint:md docs/', T); b.e(s + 3.2, 'refuse', { id, name: 'Bash', arg: 'npm run lint:md docs/', reason: 'needs approval and nobody can answer: headless run, --ask-timeout 10m (mode accept-edits approves edits only)' }); b.mail(s + 4, id, ids[(i + 1) % 2], 'lint:md was refused (headless); I check links with `sleipnir links` instead'); }
      b.step(s + 5, id, 'edit', 'Edit', f, (1 + i % 4) + ' stale references fixed', T, { file: f, add: 1 + i % 4, del: 1 + i % 4 });
      b.step(s + 11, id, 'tool', 'Bash', 'sleipnir links ' + f, 'links ok', T);
      b.submit(s + 14, id, T, 'sleipnir links ' + f, 1.2, null, { keepPlan: true });
    });
    b.plan(60, 1, 'done'); b.plan(120, 2, 'done'); b.plan(150, 3, 'act'); b.plan(200, 4, 'act');
    b.req(WT0 + 20, 'mgr', liveRatio('mgr', 9), { p: 1700, o: 260 });
    for (let tt = 4; tt < WT0; tt += 12) { const f = tt / WT0; b.e(tt, 'use', { id: 'mgr', rd: 18360 * f, un: 3240 * f, out: 1900 * f }); ['dc-1', 'dc-2'].forEach((id, j) => b.e(tt + 1, 'use', { id, rd: (j ? 11400 : 12070) * f, un: (j ? 2400 : 2130) * f, out: (j ? 2600 : 2800) * f })); }
    b.e(WT0 - 0.02, 'use', { id: 'mgr', rd: 18360, un: 3240, out: 1900 }); b.e(WT0 - 0.015, 'use', { id: 'dc-1', rd: 12070, un: 2130, out: 2800 }); b.e(WT0 - 0.01, 'use', { id: 'dc-2', rd: 11400, un: 2400, out: 2600 });
    b.e(WT0 - 0.003, 'warm', {}); [[60, 9], [120, 14], [200, 17], [280, 15]].forEach(([tt, r]) => b.e(tt, 'gov', { rpm: r }));
    b.e(5, 'verdict', { text: 'not yet: T1..T' + n + ' unfinished' });
    b.req(8, 'mgr', 0.5, { hist: true }); b.req(50, 'mgr', 0.84, { hist: true }); b.req(140, 'mgr', 0.88, { hist: true });
    return { events: b.ev, vqFree: b.vqFree, wt0: WT0 };
  }

  /* ======================================================================== a new session (also: a team restarted with /swarm N) */
  const NEW_TITLES = ['survey the layout', 'catalogue endpoint', 'shop page', 'tests for the catalogue', 'review the diff', 'document the endpoints', 'cart total in cents', 'order status endpoint', 'seed loader', 'pager component', 'integration tests', 'second review'];
  function newRunScript(S) {
    const b = new SB(), meta = S.meta, n = (S.roster.length - 1), ws = S.roster.slice(1), goal = meta.goalText || 'Survey the project and propose a plan';
    b.say(0, 'sys', meta.launch || 'sleipnir chat', { glyph: '❯' });
    if (meta.goalText) b.say(0.4, 'you', '/goal ' + goal);
    b.sys(1.2, '◇', meta.goalText ? 'goal set; plan:' : 'ready; no goal set', { plan: !!meta.goalText });
    b.state(1, 'mgr', 'tool', 'Recon: surveying the project'); b.tool(1.4, 'mgr', 'Recon', meta.cwd, '41 files, go + a static page, AGENTS.md found; the survey is the shared pin (2.2k tokens)');
    b.req(1.6, 'mgr', 0.55, { hist: true }); b.state(3.4, 'mgr', 'think', 'drafting the plan');
    if (n === 0) {
      b.say(5, 'mgr', 'I will work alone: read the layout, make the change, run the tests.');
      b.step(7, 'mgr', 'tool', 'Read', 'go.mod', '5 lines'); b.step(9.5, 'mgr', 'tool', 'Glob', '**/*.go', '14 files'); b.step(12, 'mgr', 'edit', 'Edit', 'api/server.go', '2 lines changed', null, { add: 2, del: 1, file: 'api/server.go' });
      b.step(15, 'mgr', 'tool', 'Bash', 'go test ./...', 'ok  3 packages'); b.req(7, 'mgr', 0.9, { p: 1500, o: 300 }); b.req(15.2, 'mgr', 0.93, { p: 1500, o: 300 });
      b.say(17, 'mgr', 'Done: the change is in and go test ./... passes.'); b.state(17, 'mgr', 'wait', 'waits at the prompt', null); b.e(17.5, 'final', {});
      return { events: b.ev, vqFree: 0, wt0: 0, single: true };
    }
    b.say(4.8, 'mgr', 'Splitting the work across ' + n + ' worker' + (n === 1 ? '' : 's') + ' by directory; each gets a lease so no two touch the same file.');
    b.tool(5, 'mgr', 'TaskBoard', 'create T1..T' + n, 'created ' + n + ' task' + (n === 1 ? '' : 's'));
    ws.forEach((w, i) => b.task(5.2, 'T' + (i + 1), 'running', { title: NEW_TITLES[i % NEW_TITLES.length], owner: w.id, deps: [], scope: w.scope }));
    b.tool(5.4, 'mgr', 'Spawn', ws.map(w => w.id).join(' '), n + ' workers started in git worktrees, legs ' + ws.map(w => w.leg + 1).filter((v, i, a) => a.indexOf(v) === i).join(', '));
    b.state(6, 'mgr', 'wait', 'waits for the team (' + ws.map((w, i) => 'T' + (i + 1)).join(' ') + ')', null);
    ws.forEach((w, i) => {
      const T = 'T' + (i + 1), s = 6.5 + i * 0.9, ask = i === Math.min(1, n - 1) && !/^(bypass|yolo)$/.test(meta.mode) && !(meta.rules || []).some(r => r.effect === 'allow' && /go get/.test(r.rule));
      b.state(w.spawn + 0.5, w.id, 'think', 'reading the layout', T);
      b.step(s + 2, w.id, 'tool', 'Read', 'go.mod', '5 lines', T); b.req(s + 2.2, w.id, 0.62, { p: 1400, o: 200 });
      if (w.ro) { b.step(s + 5, w.id, 'tool', 'Grep', 'TODO', '3 matches', T); b.state(s + 8, w.id, 'done', 'report ready: 3 findings', T); b.task(s + 8, T, 'merged'); return; }
      b.step(s + 5, w.id, 'edit', 'Write', (w.role === 'frontend' ? 'web/page.js' : 'api/part' + (i + 1) + '.go'), 'wrote 22 lines', T, { add: 22, del: 0, file: 'api/part' + (i + 1) + '.go' });
      if (ask) {
        b.state(s + 9, w.id, 'ask', 'wants to run `go get golang.org/x/text@latest`', T);
        b.e(s + 9, 'ask', { q: { id: 'q-' + w.id, agent: w.id, task: T, cmd: 'go get golang.org/x/text@latest', cwd: '.', why: 'adds a dependency from the network and edits go.mod and go.sum', scope: 'cwd . (inside ' + w.id + '\'s lease ' + w.scope + ')', what: 'this command', cont: 'generic' } });
      } else {
        b.step(s + 8, w.id, 'tool', 'Bash', 'go test ./...', 'ok', T); b.req(s + 8.2, w.id, 0.9, { p: 1500, o: 260 });
        b.submit(s + 10, w.id, T, 'go test ./...', 1, null, { keepPlan: true });
      }
    });
    b.plan(6, 1, 'act'); b.e(2.5, 'verdict', { text: 'not yet: the workers have not reported' });
    b.req(3.8, 'mgr', 0.8, { hist: true });
    for (let t = 2; t < 6; t += 2) b.e(t, 'use', { id: 'mgr', rd: 200 * t * 8, un: 120 * t * 5, out: 40 * t });
    b.e(6, 'warm', {}); b.e(10, 'gov', { rpm: 12 });
    return { events: b.ev, vqFree: b.vqFree, wt0: 0 };
  }

  /* a resumed recorded session: its history is a summary, the agent waits at the prompt */
  function resumedScript(S) {
    const b = new SB(), r = S.meta.resumedFrom || {};
    b.say(0, 'sys', '↺ resumed ' + (r.id || 'a recorded session') + (r.agents ? ' (' + r.agents + ' agents)' : ''), { glyph: '↺' });
    b.say(0.5, 'you', r.first || 'earlier work');
    b.say(1, 'mgr', 'Resumed. Last time: ' + (r.first || 'the earlier task') + '. The files are as that session left them; tell me where to go from here.', { stream: false });
    b.state(1.2, 'mgr', 'wait', 'waits at the prompt', null);
    b.e(0.6, 'use', { id: 'mgr', rd: 6000, un: 1400, out: 700 }); b.req(0.7, 'mgr', 0.8, { hist: true }); b.e(1.3, 'warm', {});
    return { events: b.ev, vqFree: 0, wt0: 2 };
  }

  /* ======================================================================== director: the end of a goal, and what happens after an answer */
  /** All tasks merged and nothing scheduled: verify the merged result, judge, finish. */
  function endgame(S, tFrom) {
    const sh = S.kind === 'shop';
    const start = Math.max(tFrom, S.vqFree || 0), cmd = 'go test ./...   (the merged result)', dur = 2.1;
    const ev = [];
    const e = (t, k, o) => ev.push(Object.assign({ t, k }, o));
    e(start, 'queue', { head: 'goal', cmd, step: 'verifying' }); e(start, 'plan', { n: 5, s: 'verify' });
    e(start, 'verdict', { text: 'checking: all tasks merged; running `go test ./...` on the merged result' });
    e(start, 'state', { id: 'mgr', s: 'tool', doing: 'go test ./... on the merged result', task: null });
    e(start + dur, 'say', { who: 'sys', glyph: '✓', text: 'go test ./... passes on the merged result ✓ 2.1s' }); e(start + dur, 'queue', { head: null }); e(start + dur, 'plan', { n: 5, s: 'done' });
    e(start + dur + 0.1, 'req', { id: 'mgr', ratio: liveRatio('mgr', 6), p: 1500, o: 300 });
    e(start + dur + 0.7, 'verdict', { text: 'met: all tasks merged and `go test ./...` passes on the merged result' });
    e(start + dur + 0.9, 'goal', { s: 'met' }); e(start + dur + 1, 'say', { who: 'sys', glyph: '◇', text: 'goal met' });
    S.roster.forEach(r => { const a = S.wm && S.wm.ag[r.id]; if (r.id === 'mgr') e(start + dur + 1.1, 'state', { id: 'mgr', s: 'done', doing: 'goal met', task: null }); else if (a && a.spawned && a.state !== 'done') e(start + dur + 1.1, 'state', { id: r.id, s: 'done', doing: (a.task ? a.task + ' merged' : 'done'), task: a.task }); });
    e(start + dur + 1.2, 'final', {});
    S.vqFree = start + dur + 1.3;
    return ev;
  }

  /** Events that follow an answer to question q at world time a (choice 1, 2 or 3). */
  function afterAnswer(S, q, choice, note, a) {
    const b = new SB(); b.vqFree = Math.max(S.vqFree || 0, a);
    const id = q.agent, task = q.task;
    if (choice === 3) {
      b.say(a + 0.1, 'you', note || 'don’t install anything: use what is already there');
      b.e(a + 0.1, 'steer', { to: id, text: note || 'don’t install anything: use what is already there' });
      b.state(a + 0.4, id, 'think', 'rethinking: no new dependency; the plain way', task);
      b.req(a + 0.5, id, liveRatio(id, 20), { p: promptPer(id), o: outPer(id) });
      b.step(a + 3.2, id, 'edit', 'Edit', q.cwd === 'web/' ? 'web/shop.js' : 'go.mod', 'no new dependency', task, { add: 9, del: 0, file: q.cwd === 'web/' ? 'web/shop.js' : 'go.mod' });
    } else {
      b.step(a + 0.05, id, 'tool', 'Bash', q.cmd, '', task); b.req(a + 0.15, id, liveRatio(id, 21), { p: promptPer(id), o: outPer(id) });
      b.e(a + 3.1, 'tool', { id, name: 'Bash', arg: q.cmd, out: 'added 1 package in 3.1s', task });
      b.step(a + 3.15, id, 'edit', 'Edit', q.cwd === 'web/' ? 'web/shop.js' : 'go.mod', '+9 lines', task, { add: 9, del: 0, file: q.cwd === 'web/' ? 'web/shop.js' : 'go.mod' });
    }
    const sub = a + (choice === 3 ? 6 : 5.2);
    if (!task) {       // the single agent: no task board, it simply carries on
      b.state(sub, id, 'wait', 'waits at the prompt', null);
      b.say(sub + 0.2, 'mgr', choice === 3 ? 'Understood, I will not do that.' : 'Done: ' + q.cmd + ' ran.');
      return b.ev;
    }
    if (S.kind === 'shop' && D.extra.checkpoints && D.extra.checkpoints.some(c => c.id === 'c10')) { const c10 = D.extra.checkpoints.find(c => c.id === 'c10'); b.e(sub - 0.2, 'ckpt', { step: 'c10', ts: SL.u.tod(S.meta.t0, sub - 0.2), files: c10.filesCount, note: c10.label, skipped: false }); }
    const cmd = q.cwd === 'web/' ? 'node --check web/shop.js' : 'go test ./...';
    b.req(sub + 0.3, id, liveRatio(id, 22), { p: promptPer(id), o: outPer(id) });
    b.submit(sub, id, task, cmd, 1.4, S.kind === 'shop' ? 3 : null, { keepPlan: S.kind !== 'shop' });
    S.vqFree = b.vqFree;
    return b.ev;
  }

  /* what the manager says back to a line typed in the composer: keyword answers read from the live model */
  function chatReply(S, text) {
    const m = S.wm, t = text.toLowerCase(), c = SL.calc.totals(m), tk = m.torder.map(id => id + ' ' + m.tasks[id].st).join(', ');
    const fu = SL.u.fmtUsd;
    if (/status|how|where|progress|going/.test(t)) return (m.torder.length ? 'Tasks: ' + tk + '. ' : 'No tasks yet. ') + (SL.calc.openQuestion(m) ? SL.calc.openQuestion(m).agent + ' is waiting for your answer on ' + SL.calc.openQuestion(m).cmd + '. ' : 'Nothing is waiting on you. ') + 'Spent ' + fu(c.cost) + ' of ' + (S.meta.budget ? fu(S.meta.budget, 2) : 'no budget') + '.';
    if (/cost|spent|budget|\$|cache|hit/.test(t)) return fu(c.cost) + ' spent' + (S.meta.budget ? ' of ' + fu(S.meta.budget, 2) : '') + ', ' + c.pct + '% of the prompt read from the cache. Saved est. ' + fu(c.saved, 3) + ' at list price (an estimate).';
    if (/test/.test(t)) return S.kind === 'shop' ? 'ts-1 owns the tests (T7). I will pass that to it when its current turn ends.' : 'I will run the tests after the next change.';
    if (/stop|pause|halt/.test(t)) return 'Esc interrupts the turn and pauses the goal. I have not stopped anything yet; say so and I will.';
    if (/commit|push/.test(t) && S.kind === 'orders') return 'I will commit once you confirm: git commit -am "orders: fix off-by-one in List". That asks first.';
    return 'Noted. I will fold that in at the next turn boundary' + (S.kind === 'shop' ? ', after the merge queue drains.' : '.');
  }
  /** What an agent says when steered, and its next step. */
  function steerReply(S, id, text) {
    const A = S.wm.ag[id], short = text.length > 60 ? text.slice(0, 57) + '...' : text;
    return { say: 'Noted: ' + short + '. I will apply it in my next step.', tool: { name: 'Notes', arg: 'append', out: 'steer from you: ' + short } };
  }

  SL.scripts = { shopScript, ordersScript, docsScript, newRunScript, resumedScript, buildRoster, endgame, afterAnswer, chatReply, steerReply, liveRatio, promptPer, outPer, SB, ROLE_SEQ, mkAgent, legOf, DOC_FILES };
})(SL);
