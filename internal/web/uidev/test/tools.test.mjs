// tools.test.mjs: Doctor and Schedule (98b-ui-tools.js): the rows of the probe and of the jobs table, the verdict, the daemon line,
// the checks a job form makes before it asks the server, the request it sends, and hostile text in everything a probe or a job prints.
import test from 'node:test';
import assert from 'node:assert/strict';
import { page, EVIL, hasMarkup, scriptAttrs } from './pages-harness.mjs';

const plain = x => JSON.parse(JSON.stringify(x));
const setup = () => page({ modules: ['91-views-b.js', '92-runner.js', '98b-ui-tools.js'] });

test('a step of the probe: its group once, the cache bar for cache steps, the failure mark', () => {
  const { SL } = setup(), D = SL.toolkit.doctor;
  const a = D.stepHtml({ ok: true, name: 'cache-2', ms: 280, grp: 'prefix cache', in: 1792, cached: 1664, out: 5 }, true);
  assert.match(a, /<div class="dgrp">prefix cache<\/div>/); assert.match(a, /dstep ok/); assert.match(a, /cached 1,664/); assert.match(a, /width:93%/); assert.match(a, /class="num hp">93%/);
  const b = D.stepHtml({ ok: false, name: 'tools', ms: 380, grp: 'basics', in: 80, cached: 0, out: 22 }, false);
  assert.doesNotMatch(b, /dgrp/); assert.match(b, /dstep bad/); assert.doesNotMatch(b, /hbar2/, 'only cache steps have a bar');
});

test('the verdict: yes is a tick, NO a cross, the summary card, every word as text', () => {
  const { SL } = setup(), D = SL.toolkit.doctor;
  const h = D.verdictHtml({ kv: [['streaming', 'yes: first token 412 ms'], ['prefix cache', 'NO: nothing read'], ['other', 'unknown'], [EVIL, EVIL]], card: { title: EVIL, rows: [[EVIL, EVIL]] } });
  assert.match(h, /<dd class="ok">✓ yes: first token 412 ms/); assert.match(h, /<dd class="err">✗ NO: nothing read/); assert.match(h, /<dd class="">unknown/);
  assert.ok(!hasMarkup(h) && h.includes('&lt;img'));
  assert.ok(!hasMarkup(D.lineHtml({ k: 'err', t: EVIL })) && D.lineHtml({ k: 'err', t: 'x' }).includes('dline bad') && D.lineHtml({ k: 'warn', t: 'x' }).includes('dline warm'));
});

test('the probe request: the model, the base URL, --deep', () => {
  const { SL } = setup(), r = SL.toolkit.doctor.doctorRequest;
  assert.deepEqual(plain(r({}, { model: 'a/m', deep: true })), { model: 'a/m', deep: true });
  assert.deepEqual(plain(r({}, { 'base-url': 'http://127.0.0.1:8089/v1', model: 'mock-1', deep: true })), { model: 'mock-1', baseUrl: 'http://127.0.0.1:8089/v1', deep: true });
  assert.deepEqual(plain(r({}, { model: 'a/m' })), { model: 'a/m', deep: false });
});

test('times and exits of the jobs table read as the CLI prints them', () => {
  const { SL } = setup(), S = SL.toolkit.schedule;
  assert.equal(S.hm('2026-10-12 07:00'), '10/12 07:00'); assert.equal(S.hm('2026-10-12T07:00:11'), '10/12 07:00'); assert.equal(S.hm('never'), 'never'); assert.equal(S.hm(''), '–');
  assert.deepEqual(plain([S.exitInfo('0'), S.exitInfo('ok'), S.exitInfo('-'), S.exitInfo(''), S.exitInfo('2'), S.exitInfo('timed out after 1h0m0s'), S.exitInfo('running'), S.exitInfo('no change'), S.exitInfo('check failed: exit status 1')]),
    [{ txt: 'ok', bad: false }, { txt: 'ok', bad: false }, { none: true, txt: '', bad: false }, { none: true, txt: '', bad: false }, { txt: 'exit 2', bad: true }, { txt: 'timed out after 1h0m0s', bad: true }, { txt: 'running', bad: false, run: true }, { txt: 'no change', bad: false, skip: true }, { txt: 'check failed: exit status 1', bad: true }]);
});

const job = o => Object.assign({ id: 'j1', cron: '0 9 * * 1-5', goal: 'check', dir: '/p', model: '', mode: '', budgetUsd: 1, lastRun: '2026-10-09 07:00', lastExit: '0', next: '2026-10-12 07:00' }, o || {});

test('a job row: Run now, Edit, Pause or Resume, Log, Remove; a paused job says so in its next column', () => {
  const { SL } = setup(), S = SL.toolkit.schedule;
  const run = S.jobRow(job(), { log: null }, '10/12 07:00'), paused = S.jobRow(job({ paused: true }), { log: 'j1' }, '10/12 07:00');
  assert.deepEqual(plain([...run.matchAll(/<button[^>]*data-(run|edit|pause|lg|rm)=/g)].map(m => m[1])), ['run', 'edit', 'pause', 'lg', 'rm']);
  assert.match(run, /data-pause="j1" data-to="1">Pause/); assert.match(run, /10\/12 07:00/); assert.doesNotMatch(run, /tag warm/);
  assert.match(paused, /<span class="tag warm"[^>]*>paused<\/span>/); assert.match(paused, /data-to="0">Resume/); assert.match(paused, /class="sel paused"/); assert.doesNotMatch(paused, />10\/12 07:00</, 'the next time is not shown for a paused job');
  assert.match(S.jobRow(job({ lastExit: '2' }), {}, 'x'), /class="err">✗ exit 2/); assert.match(S.jobRow(job({ lastRun: 'never', lastExit: '-' }), {}, 'x'), /class="dim">never/);
  const going = S.jobRow(job({ lastExit: 'running' }), {}, 'x'); assert.match(going, /class="warm">● running/); assert.match(going, /data-run="j1" disabled/, 'a job that is running now cannot be started again');
  assert.match(S.jobRow(job({ lastRun: '', lastExit: '' }), {}, 'x'), /<td>– <span/, 'a job that never ran shows a dash');
  const quiet = S.jobRow(job({ lastExit: 'no change', check: 'git ls-remote origin main' }), {}, 'x');
  assert.match(quiet, /class="dim">◌ no change/, 'a slot its check skipped is neither a success nor a failure'); assert.match(quiet, /when changed: git ls-remote origin main/);
  assert.doesNotMatch(run, /when changed/, 'a job without a check says nothing about one');
});

test('a job row shows every field as text', () => {
  const { SL } = setup(), h = SL.toolkit.schedule.jobRow(job({ id: 'j' + EVIL, cron: EVIL, goal: EVIL, dir: EVIL, model: EVIL, mode: EVIL, budgetUsd: EVIL, lastRun: EVIL, lastExit: EVIL, check: EVIL }), { log: null }, EVIL);
  assert.ok(!hasMarkup(h)); assert.deepEqual(plain(scriptAttrs(h)), []); assert.ok(h.includes('&lt;img'));
});

test('the daemon line: stopped offers every and Start; here offers Stop; held by another process disables it', () => {
  const { SL } = setup(), d = SL.toolkit.schedule.daemonHtml;
  const stopped = d({ running: false, owner: 'none', every: '1m' }, { every: '45s' });
  assert.match(stopped, /◌ stopped/); assert.match(stopped, /id="dmEvery" type="text" value="45s"/); assert.match(stopped, />Start the scheduler</); assert.match(stopped, /title="sleipnir daemon --once[^"]*">Run what is due now</);
  const here = d({ running: true, owner: 'here', pid: 4321, every: '1m', timeout: '30m' }, {});
  assert.match(here, /● running/); assert.match(here, /pid 4321 · every 1m · 30m/); assert.match(here, />Stop the scheduler</); assert.doesNotMatch(here, /dmEvery/); assert.doesNotMatch(here, /disabled/);
  const ext = d({ running: true, owner: 'external', pid: 4242, every: '30s', timeout: '' }, {});
  assert.match(ext, /another process holds it/); assert.match(ext, /data-dm disabled/);
  assert.ok(!hasMarkup(d({ running: true, owner: EVIL, pid: EVIL, every: EVIL, timeout: EVIL }, {})) && !hasMarkup(d({ running: false }, { every: EVIL })));
});

test('the form of a job: what is checked before the server is asked, and the body it sends', () => {
  const { SL } = setup(), S = SL.toolkit.schedule;
  assert.deepEqual(plain(S.validateJob({ goal: '', budget: 'x' }, false).map(b => b.text)), ['the cron expression is not valid', 'a goal is required', 'the budget is a number of dollars, 0 for no limit']);
  assert.deepEqual(plain(S.validateJob({ goal: 'g', budget: '0.5' }, true)), []);
  assert.deepEqual(plain(S.validateJob({ goal: 'g', budget: '0' }, true)), []); // 0 is no limit, as in the CLI
  assert.equal(S.validateJob({ goal: 'g', budget: '-1' }, true)[0].text, 'the budget is a number of dollars, 0 for no limit');
  assert.deepEqual(plain(S.jobRequest({ cron: ' 0 9 * * * ', goal: ' go ', dir: '/p', model: '', mode: 'plan', budget: '0.5' })), { cron: '0 9 * * *', goal: 'go', dir: '/p', model: '', mode: 'plan', budgetUsd: 0.5, check: '' });
  assert.equal(S.jobRequest({ cron: '@hourly', goal: 'g', budget: '1', check: '  git rev-parse HEAD ' }).check, 'git rev-parse HEAD');
});

/* the catalogue of Tools: a list grouped by task, one action a command, every command reachable */
import fs from 'node:fs';
const SPEC = JSON.parse(fs.readFileSync(new URL('../../clispec/clispec.json', import.meta.url), 'utf8'));
const withSpec = () => { const p = setup(); p.SL.D.spec = { commands: SPEC.commands, chatSlash: [] }; return p; };

test('the catalogue lists every top-level command once, under a task, with exactly one action button and a data-cli hook', () => {
  const { SL } = withSpec(), c = SL.toolkit.tools.catalogue(''), html = Object.values(c.cards).join('');
  const tops = SPEC.commands.filter(x => x.path.length === 1).map(x => x.path[0]);
  assert.equal(c.total, tops.length); assert.equal(c.shown, tops.length);
  const rows = [...html.matchAll(/<li class="trow" data-cmd="([^"]+)">([\s\S]*?)<\/li>/g)];
  assert.deepEqual(rows.map(r => r[1]).sort(), tops.slice().sort(), 'every command is a row, once');
  rows.forEach(([, n, body]) => {
    assert.equal((body.match(/class="btn sm"/g) || []).length, 1, n + ': one action button');
    assert.match(body, new RegExp('data-cli="' + n + '"'), n + ': the runner is reachable');
  });
  assert.deepEqual(Object.keys(c.cards).sort(), ['tools.evaluate', 'tools.observe', 'tools.other', 'tools.run', 'tools.setup'], 'the groups are drawn into the page\'s cards of 83b');
  for (const t of ['Run work', 'Sessions', 'Models &amp; providers', 'Skills &amp; plugins', 'Maintenance', 'Data']) assert.ok(html.includes('<h2>' + t + '</h2>'), t);
  assert.doesNotMatch(html, /<h2>Other<\/h2>/, 'the spec has no command of no group');
});

test('the catalogue: a command with its own page opens it; a word finds commands by name, summary or subcommand', () => {
  const { SL } = withSpec(), T = SL.toolkit.tools;
  const doc = Object.values(T.catalogue('').cards).join('');
  assert.match(doc, /data-cmd="doctor">[\s\S]*?data-open="doctor">Open Doctor<\/button><button type="button" class="lnk" data-cli="doctor"/);
  const f = T.catalogue('rollout'), h = Object.values(f.cards).join('');
  assert.equal(f.shown, 1); assert.match(h, /data-cmd="rl"/); assert.match(h, /class="chip sub hit" data-sub="rl rollout"/, 'the subcommand the word names is marked and shown');
  assert.match(f.cards['tools.run'], /Nothing here matches “rollout”/, 'a card with nothing that matches says so');
  assert.equal(T.catalogue('zzzz').shown, 0);
  assert.ok(!hasMarkup(Object.values(T.catalogue(EVIL).cards).join('')), 'the filter text is shown as text');
});

test('the runner lists the commands in the same task groups, every command once', () => {
  const { SL } = withSpec(), R = SL.runner, shown = R.shownCmds({ q: '' });
  assert.equal(shown.length, SPEC.commands.length);
  const groups = [...R.listHtml({ q: '', c: null }).matchAll(/<div class="cgrp">([^<]+)<\/div>/g)].map(m => m[1]);
  assert.deepEqual(groups, ['Run work', 'Sessions', 'Models &amp; providers', 'Skills &amp; plugins', 'Maintenance', 'Data']);
  assert.equal(R.brief(SPEC.commands.find(c => c.path.join(' ') === 'sessions prune')).includes('('), false, 'a short line has no cut parenthesis');
});
