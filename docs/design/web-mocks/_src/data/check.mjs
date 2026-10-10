#!/usr/bin/env node
// check.mjs: loads data.js and outputs.js in a vm sandbox (no DOM, no node globals) and asserts the invariants of the pack.
//   node check.mjs          exit 0 and "ALL CHECKS PASSED" when every invariant holds
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';
const here = path.dirname(fileURLToPath(import.meta.url));
const read = (f) => fs.readFileSync(path.join(here, f), 'utf8');

let fails = 0, passes = 0;
const ok = (cond, msg) => { if (cond) passes++; else { fails++; console.error('FAIL: ' + msg); } };
const eq = (a, b, msg) => ok(JSON.stringify(a) === JSON.stringify(b), `${msg}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
const near = (a, b, tol, msg) => ok(Math.abs(a - b) <= tol, `${msg}: got ${a}, want ${b} ± ${tol}`);

// ---- load: a sandbox with nothing but the language
const sandbox = {};
sandbox.window = sandbox; sandbox.globalThis = sandbox;
vm.createContext(sandbox);
const dataSrc = read('data.js'), outSrc = read('outputs.js');
vm.runInContext(dataSrc, sandbox, { filename: 'data.js' });
const spec = JSON.parse(read('cli-spec.json'));
sandbox.SLDATA.cliSpec = spec;
vm.runInContext(outSrc, sandbox, { filename: 'outputs.js' });
const S = sandbox.SLDATA;

// ---- 0. safe to inline in a <script>
ok(!/<\/script/i.test(dataSrc) && !/<\/script/i.test(outSrc), 'no </script in the files (they are inlined in a <script>)');
ok(!dataSrc.includes('<!--') && !outSrc.includes('<!--'), 'no <!-- in the files');
// ---- 1. sizes
const bytes = Buffer.byteLength(dataSrc) + Buffer.byteLength(outSrc);
ok(bytes <= 450000, `data.js + outputs.js = ${bytes} bytes, budget 450000`);
console.log(`sizes: data.js ${Buffer.byteLength(dataSrc)}, outputs.js ${Buffer.byteLength(outSrc)}, together ${bytes} (budget 450000), cli-spec.json ${Buffer.byteLength(read('cli-spec.json'))}`);

// ---- 2. the roster and its totals
const t = S.totals(S.team);
eq([t.prompt, t.read, t.uncached, t.out], [59900, 50844, 9056, 9650], 'totals (prompt, read, uncached, out)');
near(t.hit * 100, 84.9, 0.05, 'overall hit');
eq(S.fmt.pct(t.hit), '85%', 'overall hit as shown');
near(t.cost, 0.0715, 0.00005, 'total cost');
eq(t.costText, '$0.07', 'header cost');
eq(S.fmt.usd4(t.cost), '$0.0715', 'cost to four places');
near(t.savedEst, 0.059, 0.0005, 'saved estimate at list price');
eq(S.team.length, 9, 'nine agents (rider + eight workers)');
eq(S.team.filter((a) => a.rider).length, 1, 'one rider');
eq(S.team.filter((a) => !a.rider).length, 8, 'eight workers');
eq(S.team.filter((a) => !a.rider).map((a) => a.leg).sort(), [1, 2, 3, 4, 5, 6, 7, 8], 'eight legs, numbered 1-8, one per worker');
ok(S.teamById.mgr.leg === null, 'the manager takes no leg');
for (const a of S.team) {
  ok(a.prompt === a.read + a.uncached, `${a.id}: prompt = read + uncached`);
  near(a.hit * 100, a.hitPct, 0.5, `${a.id}: hitPct`);
  near(a.cost, S.costOf(a), 1e-12, `${a.id}: cost from the price card`);
}
eq(S.team.map((a) => a.hitPct), [78, 89, 77, 90, 90, 90, 91, 83, 74], 'per-agent hit percent as the table says');
eq(S.team.map((a) => a.costText), ['$0.0264', '$0.0108', '$0.0110', '$0.0074', '$0.0032', '$0.0033', '$0.0030', '$0.0051', '$0.0014'], 'per-agent cost to four places');
eq(S.team.filter((a) => a.state === 'ask').map((a) => a.id), ['fe-1'], 'one agent asks');
eq(S.teamSummary().banner, 'manager + 8 workers', 'banner');
eq(S.teamSummary().activeText, '4 active of 8 workers', 'active text');
eq(S.legs.length, 8, 'legs list');

// ---- 3. hit series: mean within one point of the cumulative hit; be-2's last request is a break
for (const a of S.team) {
  const ser = S.hitSeries[a.id];
  ok(Array.isArray(ser) && ser.length >= 2, `${a.id}: has a hit series`);
  const mean = ser.reduce((x, y) => x + y, 0) / ser.length;
  near(mean * 100, a.hitPct, 1, `${a.id}: mean of the series (${(mean * 100).toFixed(1)}) vs cumulative hit (${a.hitPct})`);
}
eq(S.hitSeries['be-2'][6], 0, 'be-2 request 7 read nothing');
eq(S.hitSeries['be-2'].length, 7, 'be-2 has 7 requests');
eq(S.anomalies[0].agent, 'be-2', 'the anomaly is on be-2'); eq(S.anomalies[0].reqIndex, 7, 'at request 7'); eq(S.anomalies[0].actual, 0, 'read 0');
ok(/read 0 of 4\.5k expected/.test(S.anomalies[0].line), 'anomaly line wording: ' + S.anomalies[0].line);
near(S.anomalies[0].missUSD, 4515 * 0.9 / 1e6, 1e-9, 'the miss costs expected x (input - cached read) at worker prices');
ok(S.anomalies[0].missUSD < S.teamById['be-2'].cost, 'a break costs less than the agent has spent in all');
eq(S.compactions.length, 1, 'one compaction so far'); eq(S.compactions[0].pct, 54, 'compaction 1.4k -> 625 is -54%'); eq(S.fmt.tok(S.compactions[0].before), '1.4k', 'before shows 1.4k');
eq(S.seriesMarks['be-1'][0].req, 5, 'be-1 compaction mark');

for (const a of S.docsSweep.team) { const ser = S.docsSweep.hitSeries[a.id], mean = ser.reduce((x, y) => x + y, 0) / ser.length; near(mean * 100, a.hitPct, 1, `docs-sweep ${a.id}: series mean vs cumulative hit`); ok(a.prompt === a.read + a.uncached, `docs-sweep ${a.id}: prompt = read + uncached`); }
// ---- 4. board, mail, layers, goal, question
eq(S.tasks.map((x) => x.phase), ['merged', 'merged', 'merged', 'verify', 'running', 'running', 'running', 'todo'], 'board phases');
eq(S.tasks.length, 8, 'T1..T8'); eq(S.tasks[7].title, 'review: catalogue and cart', 'T8 title'); eq(S.tasks[7].owner, 'rv-1', 'T8 owner'); eq(S.tasks[7].deps, ['T4', 'T5'], 'T8 deps');
for (const x of S.tasks) for (const d of x.deps) ok(S.tasks.some((y) => y.id === d), `${x.id} depends on a task that exists: ${d}`);
for (const x of S.tasks) if (x.owner) ok(S.teamById[x.owner], `${x.id} owner ${x.owner} is on the roster`);
eq(S.mergeQueue.head.text, '▸ T4 rebase ✓ · verifying go test ./api/catalog/... 0.6s elapsed', 'merge queue head');
eq(S.mail.filter((m) => !m.future).length, 3, 'three mails at the snapshot'); eq(S.mail.filter((m) => m.future).length + S.mailFuture.length, 3, 'three future mails (be-1 reply, rv-1 x2)');
eq(S.mailStats.text, 'routed 4 · dup 0 · mailman off', 'mail digests');
eq(S.layers.map((l) => l.id), ['G0', 'G1', 'G2', 'G3', 'G4', 'G5'], 'six layers');
eq(S.layers.slice(0, 3).reduce((a, l) => a + l.tokens, 0), 4500, 'G0-G2 are 4.5k');
eq(S.goal.plan.length, 6, 'a plan of six'); ok(/not yet: T4-T7 unfinished/.test(S.goal.judge.reason), 'judge verdict');
eq(S.question.agent, 'fe-1', 'the question is fe-1\'s'); eq(S.question.options.length, 3, 'three choices'); eq(S.question.keys.quietPeriodMs, 350, 'quiet period from the code');
ok(S.question.options[1].label.indexOf("don't ask again") >= 0, 'option 2 wording');
eq(S.script.finalTotals.costHeader, '$0.11', 'the run ends at $0.11');
ok(S.script.finalTotals.cost > 0.0715 && S.script.finalTotals.cost < 0.115, 'final cost between the snapshot and $0.115');
const ft = S.script.finalPerAgent;
for (const id of Object.keys(ft)) ok(ft[id].prompt === ft[id].read + ft[id].uncached, `${id}: final prompt = read + uncached`);
eq(S.channels.mgr.entries.filter((e) => e.tool === 'edit' && e.status === 'refused').length, 1, 'the manager has one refused Edit');
ok(S.channels.mgr.entries.some((e) => e.result === S.managerRefusal), 'with the real refusal text');
for (const id of Object.keys(S.teamById)) ok(S.channels[id], `channel for ${id}`);
ok(S.channels.mail, 'a mail channel');


// ---- 4b. the story is consistent: clocks run forward, counts in transcripts are the real line counts of the files, checkpoints begin before their files are written
{
  const sorted = (arr, key) => arr.every((x, i) => i === 0 || key(arr[i - 1]) <= key(x));
  for (const [id, ch] of Object.entries(S.channels)) ok(sorted(ch.entries, (e) => e.t), `channel ${id}: entries are in time order`);
  ok(sorted(S.conversation, (e) => e.t), 'the manager conversation is in time order');
  ok(sorted(S.script.beats, (b) => b.at), 'the live script is in time order');
  const P = S.files.shop, cp = Object.fromEntries(P.steps.map((s) => [s.id, s]));
  for (const [id, ch] of Object.entries(S.channels)) for (const e of ch.entries) {
    if (e.cp) {
      ok(cp[e.cp], `${id}: ${e.arg} names checkpoint ${e.cp}`);
      ok(e.t >= cp[e.cp].time, `${id}: ${e.arg} is written at ${e.t}, not before checkpoint ${e.cp} began (${cp[e.cp].time})`);
      const f = (P.diffs[e.cp] || []).find((x) => x.path === e.arg);
      ok(f, `${id}: ${e.arg} is one of the files of ${e.cp}`);
      if (f) eq([e.added, e.removed], [f.added, f.removed], `${id}: ${e.arg} +/- in the transcript equals git's`);
    }
  }
  const t4 = S.tasks.find((x) => x.id === 'T4');
  ok(S.channels['be-1'].entries.some((e) => e.tool === 'task' && /done T4/.test(e.arg) && e.t === t4.submittedAt), 'T4 submitted when be-1 says so');
  for (const c of S.checkpoints) if (!c.skipped) eq(c.files.length, P.diffs[c.id].length, `${c.id}: file count`);
  eq(S.checkpoints.filter((c) => !c.future && c.filesCount > 0).map((c) => [c.id, c.time, c.filesCount, c.label]), [['c05', '03:04:21', 2, 'seed items + loader'], ['c06', '03:04:29', 1, 'T5 cart total in cents'], ['c07', '03:04:38', 3, 'T4 catalogue handler']], '/rewind at the snapshot');
  ok(S.checkpoints.filter((c) => c.future).length === 5, 'five future checkpoints');
  // the feed counts are the files'
  const feed = S.conversation.filter((e) => e.kind === 'feed').map((e) => e.text);
  ok(feed.includes('Write api/catalog/items.go +' + P.diffs.c07.find((f) => f.path === 'api/catalog/items.go').added) && feed.includes('Edit api/server.go +18 −4') && feed.includes('Write web/shop.js +14'), 'feed counts equal the files');
  // the roster's 'doing' strings and the board agree with the channels' last state
  for (const a of S.team) eq(S.channels[a.id].state, a.state, `${a.id}: channel state equals roster state`);
}

// ---- 5. files: every file referenced by a diff or a checkpoint exists; derived diffs match the stored stats
for (const pk of ['shop', 'orders']) {
  const P = S.files[pk], paths = new Set(P.tree.map((f) => f.path));
  for (const [cp, list] of Object.entries(P.diffs)) for (const f of list) {
    ok(paths.has(f.path), `${pk}: ${cp} touches ${f.path}, which is in the tree`);
    ok(P.versions[f.path], `${pk}: ${f.path} has versions`);
    const d = S.stepDiff(pk, cp).find((x) => x.path === f.path);
    ok(d && d.hunks.length > 0, `${pk}: ${cp} ${f.path}: hunks derive`);
    if (!/seed\/items\.json$/.test(f.path)) { eq([d.added, d.removed], [f.added, f.removed], `${pk}: ${cp} ${f.path}: derived +/- equals git's`); }
  }
  for (const [p, rows] of Object.entries(P.blameFinal)) { const total = rows.reduce((a, r) => a + r[1], 0); const text = S.fileAt(pk, p, 'final'); const n = text.replace(/\n$/, '').split('\n').length; if (/seed\/items\.json$/.test(p)) ok(total === 8 && n === 10, `${pk}: blame of the seed excerpt covers its 8 rows`); else eq(total, n, `${pk}: blame covers every line of ${p}`); }
  for (const f of P.tree) if (!f.protected && f.kind === 'text') ok(S.fileAt(pk, f.path, 'final') !== null || S.fileAt(pk, f.path, 'now') !== null || f.status === 'D', `${pk}: ${f.path} has content`);
}
{
  const P = S.files.shop;
  eq(P.steps.map((s) => s.id), ['c05', 'c06', 'c07', 'c08', 'c09', 'c10', 'c11', 'c12'], 'shop checkpoints');
  eq(P.diffs.c05.map((f) => f.path), ['api/catalog/load.go', 'seed/items.json'], 'c05: seed items + loader (2 files)');
  eq(P.diffs.c06.length, 1, 'c06: 1 file'); eq(P.diffs.c07.length, 3, 'c07: 3 files');
  eq(S.diffSince('shop', 'c07', 'now').map((f) => f.path), ['api/catalog/items.go', 'api/server.go', 'web/shop.js'], '/diff c07 at the snapshot');
  const items = S.fileAt('shop', 'api/catalog/items.go', 'now');
  ok(items.includes('func (s *Store) List(page, size int) Page {') && items.includes('size = min(max(size, 1), MaxSize)'), 'items.go has the brief\'s code');
  ok(S.fileAt('shop', 'api/catalog/items_test.go', 'now') === null, 'items_test.go is not written yet at the snapshot');
  const tl = S.fileAt('shop', 'api/catalog/items_test.go', 'final'); ok(tl && tl.split('\n').length >= 40, 'items_test.go is ~40+ lines at the end: ' + (tl && tl.split('\n').length));
  ok(S.fileAt('shop', 'web/shop.js', 'now').startsWith("const grid = document.querySelector('#items');"), 'shop.js at the snapshot');
  ok(/func \(c \*Cart\) Total\(\) int64/.test(S.fileAt('shop', 'api/cart/cart.go', 'now')), 'cart.go Total is int64 at the snapshot');
  ok(/func \(c \*Cart\) Total\(\) float64/.test(S.fileAt('shop', 'api/cart/cart.go', 'c04')), 'cart.go Total is float64 before');
  const prot = P.tree.filter((f) => f.protected).map((f) => f.path).sort(); eq(prot, ['.env', 'secrets/signing.pem'], 'protected paths');
  ok(P.tree.every((f) => !f.protected || !S.files.shop.contents[f.path]), 'protected files have no content in the pack');
  for (const f of P.tree) if (f.lease) ok(['be-1', 'be-2', 'fe-1', 'ts-1'].includes(f.lease.agent), `${f.path}: lease belongs to a writer`);
  const O = S.files.orders;
  ok(/offset := page \* size/.test(S.fileAt('orders', 'orders/list.go', 'c04')) && /offset := \(page - 1\) \* size/.test(S.fileAt('orders', 'orders/list.go', 'c01')), 'orders-api: the real off-by-one fix');
  eq(O.steps.length, 2, 'orders-api: 2 checkpoints');
}

// ---- 6. sessions and prune arithmetic
eq(S.sessions.live.length, 3, 'three live sessions'); eq(S.sessions.recorded.length, 12, '12 recorded sessions');
near(S.sessions.live[0].cost, t.cost, 1e-12, 'the shop session costs what the roster costs');
for (const r of S.sessions.recorded.concat(S.sessions.archive)) ok(/^\d{8}-\d{6}-[0-9a-f]{6}$/.test(r.id), 'session id shape: ' + r.id);
const idsAll = S.sessions.all().map((x) => x.id); eq(new Set(idsAll).size, idsAll.length, 'session ids are unique');
for (const r of S.sessions.archive) ok(Date.parse(S.meta.sampleNow) - Date.parse(r.lastWritten) > 30 * 86400e3, 'archive session is older than 30 days: ' + r.id);
{
  const all = S.sessions.all(), now = S.meta.sampleNow;
  const p = S.planPrune(all, now, 30 * 86400, 20);
  eq(p.total, 29, 'sessions in the state directory'); eq(p.keptNewest, 20, 'newest 20 kept');
  const sorted = all.slice().sort((a, b) => Date.parse(b.lastWritten) - Date.parse(a.lastWritten));
  const expectGone = sorted.slice(20).filter((x) => Date.parse(now) - Date.parse(x.lastWritten) >= 30 * 86400e3);
  eq(p.items.map((x) => x.id).sort(), expectGone.map((x) => x.id).sort(), 'prune picks exactly the old ones beyond the newest 20');
  eq(p.items.length, 9, 'nine to delete');
  eq(p.freed, expectGone.reduce((a, x) => a + x.size, 0), 'freed bytes are the sum of the sizes');
  const dry = S.runOutput('sessions prune', {}, { state: S.newState() });
  ok(dry.lines.some((l) => /^would delete 9 of 29 sessions/.test(l.t)), 'dry run says what would go');
  ok(dry.lines.some((l) => /nothing deleted: run again with --yes/.test(l.t)), 'dry run deletes nothing');
  const state = S.newState();
  const sum = (lines) => lines.filter((l) => /^ {2}\d{8}-/.test(l.t)).length;
  const real = S.runOutput('sessions prune', { yes: true }, { state });
  eq(sum(real.lines), 9, 'apply lists the same nine'); ok(real.lines.some((l) => /^deleted 9 sessions, /.test(l.t)), 'apply says what went');
  eq(state.sessions.length, 20, 'twenty sessions are left');
  const again = S.runOutput('sessions prune', { yes: true }, { state });
  ok(/^nothing to prune: 20 sessions, 20 of the newest kept/.test(again.lines[0].t), 'a second apply has nothing to do');
  const zero = S.runOutput('sessions prune', { 'older-than': '0', keep: 0 }, { state: S.newState() });
  ok(zero.lines.some((l) => /^would delete 26 of 29/.test(l.t) || /^would delete 2[0-9] of 29/.test(l.t)), 'older-than 0 keep 0 takes everything not written in the last ten minutes: ' + zero.lines.map((l) => l.t).slice(-3).join(' | '));
  ok(zero.lines.some((l) => /3 old enough but written to in the last ten minutes: left alone/.test(l.t)), 'the three live sessions are left alone');
  const badAge = S.runOutput('sessions prune', { 'older-than': 'soon' }, { state: S.newState() });
  eq(badAge.exit, 1, 'a bad age is an error');
}
// parseAge units and the list format
eq(S.fmt.ageText(125 * 86400), '4mo', 'ageText 4mo'); eq(S.fmt.ageText(84 * 86400), '84d', 'ageText 84d'); eq(S.fmt.sizeText(1048576 * 5.4), '5 MB', 'sizeText');
{
  const r = S.runOutput('sessions', { n: 3 }, { state: S.newState() });
  ok(/^↺ 20260102-030405-5eed01  just now  anthropic\/claude-sonnet-5-5 {2}\$0\.0715   /.test(r.lines[0].t), 'sessions line format: ' + r.lines[0].t);
}

// ---- 7. models, providers, permissions, config, mcp, schedule
ok(S.models.length >= 36, 'at least 36 models: ' + S.models.length);
eq([...new Set(S.models.map((m) => m.provider))].sort(), ['anthropic', 'chatgpt', 'heimdall', 'local', 'openai', 'openrouter'], 'model providers');
ok(S.models.every((m) => m.sample === true), 'every model is marked sample');
eq(S.models.filter((m) => m.provider === 'anthropic').map((m) => m.id), ['claude-fable-5-1', 'claude-opus-5-5', 'claude-sonnet-5-5', 'claude-haiku-5-5'], 'the four anthropic ids');
ok(S.models.filter((m) => m.inPerM === null && !m.plan).length >= 5, 'several models with price unknown');
ok(S.models.filter((m) => m.favourite).length === 3, 'three favourites'); eq(S.providers.length, 6, 'six providers');
eq(S.roleModels.defaults.manager, 'anthropic/claude-sonnet-5-5', 'manager model'); eq(S.roleModels.defaults.worker, 'heimdall/demo-model', 'worker model'); eq(S.roleModels.defaults.mailman, null, 'mailman model unset');
{
  const m1 = S.runOutput('models', { tools: true, 'min-context': '128k', 'max-price': 5 }, { state: S.newState() });
  ok(m1.lines.length > 3 && m1.lines.every((l, i) => i === 0 || /^\*? ?[a-z]/.test(l.t)), 'models filter output');
  const rows = m1.lines.slice(1); ok(rows.every((l) => /^\*? ?[a-z]/.test(l.t)), 'rows start with a ref');
  const free = S.runOutput('models', { fav: true }, { state: S.newState() }); eq(free.lines.length, 4, 'only favourites: header + 3');
  ok(free.lines[1].t.startsWith('* anthropic/claude-haiku-5-5'), 'favourites are starred and sorted');
  const none = S.runOutput('models', { 'max-price': 0.0001, words: [] , _: ['zzz'] }, { state: S.newState() }); ok(none.lines.some((l) => /nothing matches/.test(l.t)), 'a filter that matches nothing says so');
  const bad = S.runOutput('models', { 'min-context': 'lots' }, { state: S.newState() }); eq(bad.exit, 1, 'bad --min-context');
}
ok(S.permissions.modes.length === 5 && S.permissions.cycle.join() === 'default,accept-edits,plan', 'modes and the shift+tab cycle never enter bypass/yolo');
eq(S.permissions.testsPreset.rules.length, 34, 'the tests preset has 34 rules (REAL list)');
ok(S.permissions.rules.ask.some((r) => r.rule === 'Bash(git push:*)'), 'git push asks'); ok(S.permissions.rules.deny.some((r) => r.rule === 'Read(./.env)') && S.permissions.rules.deny.some((r) => r.rule === 'Read(./secrets/**)'), '.env and secrets denied');
ok([].concat(...Object.values(S.permissions.rules)).every((r) => r.origin), 'every rule has an origin');
ok(S.config.effective.some((r) => r.key === 'swarm.max_workers' && r.layer === 'project' && r.value === 12), 'swarm.max_workers 12 from the project file');
ok(S.config.effective.some((r) => r.key === 'models.default' && r.layer === 'user'), 'models.default from the user file');
ok(S.config.effective.some((r) => r.key === 'permissions.mode'), 'permissions.mode is listed'); ok(S.config.effective.some((r) => /^cache\./.test(r.key)), 'cache.* is listed');
eq(S.mcp.servers.length, 4, 'four MCP servers'); eq(S.mcp.servers.find((s) => s.name === 'docs').tools, 14, 'docs has 14 tools'); eq(S.mcp.servers.find((s) => s.name === 'docs').toolNames.length, 14, 'and lists them');
eq(S.skills.length, 3, 'three skills'); eq(S.skills.filter((k) => k.youOnly).length, 1, 'one is (you only)');
eq(S.schedule.jobs.length, 4, 'four schedule jobs'); eq(S.schedule.logs.length, 6, 'six log excerpts');
for (const j of S.schedule.jobs) eq(S.cronNext(j.cron, S.meta.sampleNow), j.next, `cron next of ${j.id} (${j.cron})`);
eq(S.cronNext('@weekly', '2026-01-02T03:04:43'), '2026-01-04T00:00:00', '@weekly is Sunday 00:00'); eq(S.cronNext('0 9 * * 1-5', '2026-01-02T10:00:00'), '2026-01-05T09:00:00', 'weekday 9:00 skips the weekend'); eq(S.cronNext('61 * * * *', '2026-01-02T00:00:00'), null, 'bad cron');
{
  const st = S.newState();
  const add = S.runOutput('schedule add', { cron: '0 3 * * *', _: ['sweep', 'the', 'docs'] }, { state: st });
  ok(/^j5: runs next at 2026-01-03 03:00; `sleipnir daemon` starts the jobs that are due$/.test(add.lines[0].t), 'schedule add: ' + add.lines[0].t);
  const lst = S.runOutput('schedule', {}, { state: st }); eq(lst.lines.length, 6, 'schedule lists the new job (header + 5)');
  eq(S.runOutput('schedule add', { cron: '61 * * * *', _: ['x'] }, { state: st }).exit, 1, 'bad cron fails');
  const t0 = S.runOutput('trust', {}, { state: st }); ok(/trusted since 2026-01-01/.test(t0.lines.map((l) => l.t).join('\n')), 'trust shows the ledger');
  S.runOutput('trust forget', {}, { state: st }); ok(/not trusted/.test(S.runOutput('trust', {}, { state: st }).lines.map((l) => l.t).join('\n')), 'forget takes it back');
  eq(S.runOutput('trust add', {}, { state: st }).exit, 1, 'trust add without --yes needs an answer');
  S.runOutput('trust add', { yes: true }, { state: st }); ok(/trusted since/.test(S.runOutput('trust', {}, { state: st }).lines.map((l) => l.t).join('\n')), 'add trusts again');
  const lst2 = S.runOutput('mcp list', { 'trust-project': true }, { state: S.newState() }).lines.map((l) => l.t).join('\n');
  eq(lst2, S.realBlock('mcp', 'mcp list --trust-project').text, 'mcp list equals the real capture');
  const st2 = S.newState(); S.runOutput('mcp approve', { _: ['tracker'], yes: true }, { state: st2 });
  { const cap = read('real/mcp.cap').split(/^### /m).slice(1).filter((b) => /sleipnir mcp list --trust-project/.test(b.split('\n')[0])); const after = cap[1].split('\n').slice(1).join('\n').replace(/\n\[exit 0\]\n?$/, '').replace(/\n+$/, ''); eq(S.runOutput('mcp list', { 'trust-project': true }, { state: st2 }).lines.map((l) => l.t).join('\n'), after, 'after approve the list equals the real capture'); }
  eq(S.runOutput('mcp test', { 'trust-project': true }, { state: st2 }).lines.map((l) => l.t).join('\n'), S.realBlock('mcp', 'mcp test --trust-project').text.replace(/^[\s\S]*?(?=warn)/, '').trim(), 'mcp test equals the real capture');
  eq(S.runOutput('config', {}).lines.map((l) => l.t).join('\n'), S.realBlock('config', 'config').text, 'config equals the real capture');
}

// ---- 8. the real captures and the ports of the Go formatters agree
{
  const fr = read('real/friction.txt').replace(/data\/demo-shop\/session/g, '~/.sleipnir/sessions/20260101-171204-7c1e3a').replaceAll(process.env.S || '/nonexistent', '<scratch>').replace(/\n+$/, '');
  eq(S.frictionText(S.friction.report), fr.replace(/\/.*?demo-shop\/session/g, '~/.sleipnir/sessions/20260101-171204-7c1e3a'), 'friction text re-rendered from its JSON equals the binary\'s text');
  const raw = read('real/doctor-mock-deep.raw').split('\n'), at = raw.findIndex((l) => l.startsWith('Endpoint probe for'));
  const shape = (s) => String(s).replace(/\d+(\.\d+)?/g, '#').replace(/ +/g, ' ').trim();
  eq(shape(S.doctorText(S.doctor.mock.report)), shape(raw.slice(at).join('\n').replace(/\n+$/, '')), 'doctor text re-rendered from its JSON has the shape of the binary\'s report (digits and padding aside; the JSON and the text are two probe runs)');
  const logWant = raw.slice(raw.findIndex((l) => l === 'basic'), at).join('\n').replace(/\n+$/, '');
  eq(shape(S.doctorSteps(S.doctor.mock.report).join('\n')), shape(logWant), 'doctor step log has the binary\'s shape');
}

// ---- 9. the command runner: a form for every flag, an output for every command, the slash commands that print
const keys = Object.keys(S.outputs);
for (const c of spec.commands) {
  const p = c.path.join(' ');
  ok(S.outputs[p], `outputs entry for "${p}"`);
  for (const f of c.flags) { ok(f.arg && ['bool', 'string', 'int', 'uint', 'float', 'duration'].includes(f.arg), `${p} --${f.name} has a type (${f.arg})`); ok(f.desc && f.desc.length > 3, `${p} --${f.name} has a description`); ok(typeof f.repeatable === 'boolean', `${p} --${f.name} repeatable flag`); }
}
eq(spec.commands.length, 61, 'commands in cli-spec.json'); ok(spec.chatSlash.length >= 33, 'slash table: ' + spec.chatSlash.length);
for (const s of ['/cost', '/context', '/status', '/permissions', '/trust', '/mcp', '/skills', '/sessions', '/rewind', '/diff', '/recon', '/help', '/agents']) ok(S.outputs[s], `slash output ${s}`);
const kinds = new Set(['out', 'err', 'dim', 'ok', 'warn', 'bad', 'head']);
for (const k of keys) {
  const r = S.runOutput(k, k.startsWith('rl ') ? { _: ['runs/r001'], tasks: 't.jsonl', model: 'sample-policy-v1', repo: '.', dir: '.' } : { _: ['x'] }, { state: S.newState(), session: S.newSessionState() });
  ok(r && Array.isArray(r.lines) && typeof r.exit === 'number' && typeof r.ms === 'number', `${k}: result shape`);
  for (const l of r.lines) { ok(kinds.has(l.k) && typeof l.t === 'string', `${k}: line kind ${l.k}`); if (!kinds.has(l.k)) break; }
  if (r.card) ok(typeof r.card.title === 'string' && Array.isArray(r.card.rows), `${k}: card shape`);
  try { JSON.stringify(r); } catch (e) { ok(false, `${k}: output serialisable`); }
}
{ // slash commands change the state they are given, and the change shows in the next answer
  const sx = S.newSessionState(), cx = { session: sx, state: S.newState() };
  S.runOutput('/mode', { _: ['accept-edits'] }, cx); ok(/mode +accept-edits/.test(S.runOutput('/status', {}, cx).lines[1].t), '/mode shows in /status');
  S.runOutput('/budget', { _: ['9'] }, cx); ok(/budget   \$9\.00/.test(S.runOutput('/status', {}, cx).lines.map((l) => l.t).join('\n')), '/budget shows in /status');
  S.runOutput('/allow', { _: ['tests'] }, cx); ok(/allow \(41\)/.test(S.runOutput('/permissions', {}, cx).lines.map((l) => l.t).join('\n')), '/allow tests adds 34 rules: allow (41) = 7 + 34');
  eq(S.runOutput('/rewind', {}, cx).lines.length, 4, '/rewind lists three checkpoints and the hint (c04 touched nothing)');
  ok(S.runOutput('/diff', {}, cx).lines[0].t.startsWith('checkpoint c07, the newest that changed a file'), '/diff without an id');
  eq(S.runOutput('/goal', { _: ['pause'] }, cx).lines[0].t, 'goal paused; /goal resume goes on', '/goal pause');
  ok(/goal \(paused: paused by you\)/.test(S.runOutput('/goal', {}, cx).lines[0].t), '/goal status says paused');
}
// the strings the UI will print contain no HTML (the pack is text; the UI renders it as text). File contents are the one place markup is expected.
{
  const bad = /<\/?(script|img|iframe|style|svg|object|embed|link|meta|div|span|a|b|i|p|br|ul|ol|li|table|tr|td|form|input|button|h[1-6]|html|body|head)(\s[^>]*)?\/?>/i;
  let n = 0;
  const walk = (o, p) => { if (typeof o === 'string') { if (p.startsWith('files.') ) return; if (bad.test(o) || /\bon[a-z]+\s*=\s*["']/i.test(o)) { n++; console.error('FAIL: markup-like string at ' + p + ': ' + o.slice(0, 80)); } } else if (o && typeof o === 'object') for (const [k, v] of Object.entries(o)) walk(v, p ? p + '.' + k : k); };
  walk(JSON.parse(JSON.stringify(S, (k, v) => (typeof v === 'function' ? undefined : v))), '');
  ok(n === 0, 'no markup-like strings outside files');
  ok(!/<script/i.test(Object.values(S.outputs).map((f) => { try { return JSON.stringify(f({ _: ['x'] }, { state: S.newState(), session: S.newSessionState() })); } catch { return ''; } }).join('')), 'no <script in any output');
}
// serialisable
try { const s = JSON.stringify(S, (k, v) => (typeof v === 'function' ? undefined : v)); ok(s.length > 100000, 'SLDATA serialises (' + s.length + ' chars of JSON)'); } catch (e) { ok(false, 'SLDATA is not serialisable: ' + e.message); }
// determinism: a second load gives the same JSON
{
  const sb1 = {}; sb1.window = sb1; vm.createContext(sb1); vm.runInContext(dataSrc, sb1);
  const sb2 = {}; sb2.window = sb2; vm.createContext(sb2); vm.runInContext(dataSrc, sb2);
  const a = JSON.stringify(sb1.SLDATA, (k, v) => (typeof v === 'function' ? undefined : v)), b = JSON.stringify(sb2.SLDATA, (k, v) => (typeof v === 'function' ? undefined : v));
  ok(a === b, 'data.js is deterministic (two loads, same JSON)');
}
console.log(`\n${passes} checks passed, ${fails} failed`);
if (fails) { console.error('CHECKS FAILED'); process.exit(1); }
console.log('ALL CHECKS PASSED');
