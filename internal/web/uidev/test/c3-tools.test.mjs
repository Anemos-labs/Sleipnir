// c3-tools.test.mjs: Doctor and Schedule (98b-ui-tools.js): the rows of the probe and of the jobs table, the verdict, the daemon line,
// the checks a job form makes before it asks the server, the request it sends, and hostile text in everything a probe or a job prints.
import test from 'node:test';
import assert from 'node:assert/strict';
import { page, EVIL, hasMarkup, scriptAttrs } from './c3-harness.mjs';

const plain = x => JSON.parse(JSON.stringify(x));
const setup = () => page({ modules: ['91-views-b.js', '92-runner.js', '98b-ui-tools.js'] });

test('a step of the probe: its group once, the cache bar for cache steps, the failure mark', () => {
  const { SL } = setup(), D = SL.c3.doctor;
  const a = D.stepHtml({ ok: true, name: 'cache-2', ms: 280, grp: 'prefix cache', in: 1792, cached: 1664, out: 5 }, true);
  assert.match(a, /<div class="dgrp">prefix cache<\/div>/); assert.match(a, /dstep ok/); assert.match(a, /cached 1,664/); assert.match(a, /width:93%/); assert.match(a, /class="num hp">93%/);
  const b = D.stepHtml({ ok: false, name: 'tools', ms: 380, grp: 'basics', in: 80, cached: 0, out: 22 }, false);
  assert.doesNotMatch(b, /dgrp/); assert.match(b, /dstep bad/); assert.doesNotMatch(b, /hbar2/, 'only cache steps have a bar');
});

test('the verdict: yes is a tick, NO a cross, the summary card, every word as text', () => {
  const { SL } = setup(), D = SL.c3.doctor;
  const h = D.verdictHtml({ kv: [['streaming', 'yes: first token 412 ms'], ['prefix cache', 'NO: nothing read'], ['other', 'unknown'], [EVIL, EVIL]], card: { title: EVIL, rows: [[EVIL, EVIL]] } });
  assert.match(h, /<dd class="ok">✓ yes: first token 412 ms/); assert.match(h, /<dd class="err">✗ NO: nothing read/); assert.match(h, /<dd class="">unknown/);
  assert.ok(!hasMarkup(h) && h.includes('&lt;img'));
  assert.ok(!hasMarkup(D.lineHtml({ k: 'err', t: EVIL })) && D.lineHtml({ k: 'err', t: 'x' }).includes('dline bad') && D.lineHtml({ k: 'warn', t: 'x' }).includes('dline warm'));
});

test('the probe request: the model, the base URL, --deep', () => {
  const { SL } = setup(), r = SL.c3.doctor.doctorRequest;
  assert.deepEqual(plain(r({}, { model: 'a/m', deep: true })), { model: 'a/m', deep: true });
  assert.deepEqual(plain(r({}, { 'base-url': 'http://127.0.0.1:8089/v1', model: 'mock-1', deep: true })), { model: 'mock-1', baseUrl: 'http://127.0.0.1:8089/v1', deep: true });
  assert.deepEqual(plain(r({}, { model: 'a/m' })), { model: 'a/m', deep: false });
});

test('times and exits of the jobs table read as the CLI prints them', () => {
  const { SL } = setup(), S = SL.c3.schedule;
  assert.equal(S.hm('2026-10-12 07:00'), '10/12 07:00'); assert.equal(S.hm('2026-10-12T07:00:11'), '10/12 07:00'); assert.equal(S.hm('never'), 'never'); assert.equal(S.hm(''), '–');
  assert.deepEqual(plain([S.exitInfo('0'), S.exitInfo('ok'), S.exitInfo('-'), S.exitInfo(''), S.exitInfo('2'), S.exitInfo('timed out after 1h0m0s'), S.exitInfo('running')]),
    [{ txt: 'ok', bad: false }, { txt: 'ok', bad: false }, { none: true, txt: '', bad: false }, { none: true, txt: '', bad: false }, { txt: 'exit 2', bad: true }, { txt: 'timed out after 1h0m0s', bad: true }, { txt: 'running', bad: false, run: true }]);
});

const job = o => Object.assign({ id: 'j1', cron: '0 9 * * 1-5', goal: 'check', dir: '/p', model: '', mode: '', budgetUsd: 1, lastRun: '2026-10-09 07:00', lastExit: '0', next: '2026-10-12 07:00' }, o || {});

test('a job row: Run now, Edit, Pause or Resume, Log, Remove; a paused job says so in its next column', () => {
  const { SL } = setup(), S = SL.c3.schedule;
  const run = S.jobRow(job(), { log: null }, '10/12 07:00'), paused = S.jobRow(job({ paused: true }), { log: 'j1' }, '10/12 07:00');
  assert.deepEqual(plain([...run.matchAll(/<button[^>]*data-(run|edit|pause|lg|rm)=/g)].map(m => m[1])), ['run', 'edit', 'pause', 'lg', 'rm']);
  assert.match(run, /data-pause="j1" data-to="1">Pause/); assert.match(run, /10\/12 07:00/); assert.doesNotMatch(run, /tag warm/);
  assert.match(paused, /<span class="tag warm"[^>]*>paused<\/span>/); assert.match(paused, /data-to="0">Resume/); assert.match(paused, /class="sel paused"/); assert.doesNotMatch(paused, />10\/12 07:00</, 'the next time is not shown for a paused job');
  assert.match(S.jobRow(job({ lastExit: '2' }), {}, 'x'), /class="err">✗ exit 2/); assert.match(S.jobRow(job({ lastRun: 'never', lastExit: '-' }), {}, 'x'), /class="dim">never/);
  const going = S.jobRow(job({ lastExit: 'running' }), {}, 'x'); assert.match(going, /class="warm">● running/); assert.match(going, /data-run="j1" disabled/, 'a job that is running now cannot be started again');
  assert.match(S.jobRow(job({ lastRun: '', lastExit: '' }), {}, 'x'), /<td>– <span/, 'a job that never ran shows a dash');
});

test('a job row shows every field as text', () => {
  const { SL } = setup(), h = SL.c3.schedule.jobRow(job({ id: 'j' + EVIL, cron: EVIL, goal: EVIL, dir: EVIL, model: EVIL, mode: EVIL, budgetUsd: EVIL, lastRun: EVIL, lastExit: EVIL }), { log: null }, EVIL);
  assert.ok(!hasMarkup(h)); assert.deepEqual(plain(scriptAttrs(h)), []); assert.ok(h.includes('&lt;img'));
});

test('the daemon line: stopped offers every and Start; here offers Stop; held by another process disables it', () => {
  const { SL } = setup(), d = SL.c3.schedule.daemonHtml;
  const stopped = d({ running: false, owner: 'none', every: '1m' }, { every: '45s' });
  assert.match(stopped, /◌ stopped/); assert.match(stopped, /id="dmEvery" type="text" value="45s"/); assert.match(stopped, />Start the daemon</); assert.match(stopped, /Run what is due \(--once\)/);
  const here = d({ running: true, owner: 'here', pid: 4321, every: '1m', timeout: '30m' }, {});
  assert.match(here, /● running/); assert.match(here, /pid 4321 · every 1m · 30m/); assert.match(here, />Stop the daemon</); assert.doesNotMatch(here, /dmEvery/); assert.doesNotMatch(here, /disabled/);
  const ext = d({ running: true, owner: 'external', pid: 4242, every: '30s', timeout: '' }, {});
  assert.match(ext, /another process holds it/); assert.match(ext, /data-dm disabled/);
  assert.ok(!hasMarkup(d({ running: true, owner: EVIL, pid: EVIL, every: EVIL, timeout: EVIL }, {})) && !hasMarkup(d({ running: false }, { every: EVIL })));
});

test('the form of a job: what is checked before the server is asked, and the body it sends', () => {
  const { SL } = setup(), S = SL.c3.schedule;
  assert.deepEqual(plain(S.validateJob({ goal: '', budget: 'x' }, false).map(b => b.text)), ['the cron expression is not valid', 'a goal is required', 'the budget is a number of dollars']);
  assert.deepEqual(plain(S.validateJob({ goal: 'g', budget: '0.5' }, true)), []);
  assert.equal(S.validateJob({ goal: 'g', budget: '0' }, true)[0].text, 'the budget is a number of dollars');
  assert.deepEqual(plain(S.jobRequest({ cron: ' 0 9 * * * ', goal: ' go ', dir: '/p', model: '', mode: 'plan', budget: '0.5' })), { cron: '0 9 * * *', goal: 'go', dir: '/p', model: '', mode: 'plan', budgetUsd: 0.5 });
});
