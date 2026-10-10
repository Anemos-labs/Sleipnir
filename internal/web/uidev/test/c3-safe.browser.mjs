// c3-safe.browser.mjs: hostile text renders as text on the pages of Settings, Tools, the Runner, Doctor, Schedule and Sessions (TEST-PLAN.md
// S-08). Runs the real page in headless Chromium against the fake server (internal/web/webtest), fills every cache those pages read with
// markup (provider and model names, MCP text, hook output, rules and their origins, configuration warnings, schedule goals, recorded
// prompts, a probe's steps, a run's output, the CLI spec itself), shows each page and checks that no element was made from the text,
// that no handler ran and that the text shows as typed.
//
//   go build -o /tmp/fakeserver ./internal/web/webtest/cmd/fakeserver
//   FAKESERVER=/tmp/fakeserver node internal/web/uidev/test/c3-safe.browser.mjs
//
// It needs a Chromium (CHROME_PATH, or the Playwright headless shell the mock's tests use); it is not part of `node --test` and not of CI.
import { spawn } from 'node:child_process';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url)), repo = path.resolve(here, '../../../..');
const { open } = await import(path.join(repo, 'docs/design/web-mocks/_src/v3/test/cdp.mjs'));
const bin = process.env.FAKESERVER; if (!bin) { console.log('skip: set FAKESERVER to a built internal/web/webtest/cmd/fakeserver'); process.exit(0); }
const srv = spawn(bin, ['-addr', '127.0.0.1:0', '-ui', path.join(repo, 'internal/web/ui'), '-speed', '0'], { stdio: ['ignore', 'pipe', 'inherit'] });
process.once('exit', () => { try { srv.kill('SIGKILL'); } catch { /* gone */ } });
const url = await new Promise((res, rej) => { let b = ''; srv.stdout.on('data', d => { b += d; const i = b.indexOf('\n'); if (i >= 0) res(b.slice(0, i).trim()); }); setTimeout(() => rej(new Error('the fake server printed no address')), 15000); });
const p = await open(url, { w: 1440, h: 900 });
const X = '<img src=x data-pwn=1 onerror="window.__pwn=1">';
let failed = 0, scripts = 0;
const check = async what => {
  const r = await p.eval(`(() => ({ imgs: document.querySelectorAll('img,[data-pwn],iframe,object').length, scripts: document.scripts.length, pwn: window.__pwn || 0, text: document.body.innerText.includes(${JSON.stringify(X)}) }))()`);
  try { assert.equal(r.imgs, 0, what + ': an element was made from text'); assert.equal(r.pwn, 0, what + ': a handler ran'); assert.equal(r.scripts, scripts, what + ': a script element appeared'); console.log('ok', what, r.text ? '(shown as text)' : ''); }
  catch (e) { failed++; console.log('FAIL', e.message); }
};
/** every cache the pages read, filled with the hostile text through the data layer's own mappers */
const FILL = `(() => { const X = ${JSON.stringify(X)}, M = SL.data.map, f = [X, X];
  M.models({ models: [{ ref: X, provider: X, ctx: 1000, in: 1, out: 2, tools: true, reasoning: true }], favs: [X], errors: [X], roles: [{ name: X, code: 'x', desc: X }], roleOrder: [X], roleModels: { [X]: X }, efforts: ['default', X], configNote: X });
  M.permissions({ mode: X, modes: [{ id: X, danger: false, text: X }, { id: 'yolo', danger: true, text: X }], order: [X], managerWrites: { text: X, note: X }, testsPreset: { name: 'tests', summary: X, rules: [X] }, rules: { allow: [{ rule: X, origin: X, file: X, note: X, effect: 'allow' }], deny: [], ask: [] }, session: [] });
  M.trust({ project: { dir: X, state: 'changed: ' + X, savedDay: X, digest: X, unlocks: X }, files: [{ path: X, kind: X, bytes: X, hash: X }], covers: X, ledger: [{ dir: X, saved: X, files: 1, now: X, state: 'changed: ' + X }], question: { options: [X] } });
  M.mcp({ servers: [{ name: X, origin: X, transport: X, state: 'needs approval', tools: [X], command: X, args: [X], server: X, describe: X, error: X, note: X, prompts: [{ command: X, description: X }] }, { name: 'ok', origin: 'o', transport: 'stdio', state: 'running', tools: [X], prompts: [{ command: X, description: X }] }], sessionNote: X, root: X });
  M.skills({ skills: [{ name: X, summary: X, source: X, scope: X, youOnly: true, note: X, argumentHint: X }], skillsBudget: { tokens: X, used: X, note: X }, commands: [{ name: X, description: X, argumentHint: X, source: X }], hooks: { events: [X], configured: [{ event: X, matcher: X, command: X, timeout: X, origin: X, purpose: X }] } });
  M.providers({ providers: [{ id: X, name: X, base: X, key: 'env', env: X, state: X, keyWhere: X, dialect: X, usedBy: X, who: X, recommended: true }, { id: 'p2', name: X, base: X, key: 'none', state: 'no key' }], providerNote: X });
  M.config({ layers: [{ kind: X, source: X, state: X, trusted: X }], precedence: X, effective: [{ key: X, value: X, layer: X, file: X, note: X, below: { [X]: X } }], issues: [{ file: X, line: 3, path: X, message: X, severity: X }], risks: [{ file: X, message: X }], valid: X, ok: false });
  M.schedule({ jobs: [{ id: 'j1', cron: X, goal: X, dir: X, model: X, mode: X, budgetUsd: X, lastRun: X, lastExit: X, next: X }], daemon: { running: true, owner: X, pid: X, every: X, timeout: X, line: X }, logs: [{ job: 'j1', file: X, exit: X, text: X }] });
  M.projects({ projects: [{ dir: X + '1', root: X, name: X, trust: 'partial', files: 2 }, { dir: X + '2', root: X, name: X, trust: 'unreadable', files: 1 }] });
  M.doctor({ endpoints: [{ ref: X, where: X }] }); M.runs({ runs: [{ id: 'r_x', cmd: X, exit: 0, ms: 1, path: ['sessions'], flags: {}, running: false }] });
  SL.sessions.reg.recorded = [{ id: '20261001-000000-aaaaaa', name: X, first: X, model: X, cost: 0, mb: 0, ageS: 9e5, agents: 1, resumable: true, cwd: X, integration: { applied: false, message: X, hint: X } }];
  SL.bus.emit('recorded-changed'); SL.G.mcpOut = { [X]: { t: X, cls: X } }; SL.loop.dirty = true; return true; })()`;
try {
  await p.sleep(2500); scripts = await p.eval('document.scripts.length');
  for (const pg of ['models', 'roles', 'budget', 'permissions', 'trust', 'run', 'mcp', 'skills', 'providers', 'config', 'look']) {
    await p.eval(`SL.views.show('settings', { page: ${JSON.stringify(pg)} })`); await p.sleep(700); await p.eval(FILL); await p.sleep(500); await check('settings ' + pg);
  }
  await p.eval(`SL.views.show('settings', { page: 'trust' })`); await p.sleep(700); await p.eval(FILL); await p.sleep(500);
  // a project that could not be read: the word, and the trust step with every file and each path it could not read
  const tags = await p.eval(`[...document.querySelectorAll('.setpage .tag')].map(t => t.textContent)`); if (!tags.includes('partial') || !tags.includes('unreadable')) { failed++; console.log('FAIL the Trust page does not show the words partial and unreadable: ' + tags.join(' | ')); } else console.log('ok trust page shows partial and unreadable');
  await p.eval(`(() => { SL.ui.trustStep({ dir: ${JSON.stringify(X)}, files: [{ path: ${JSON.stringify(X)}, kind: 'unread' }, { path: 'a/b', kind: 'config', bytes: 3, hash: 'ab' }], partial: true, confirm: 'cf_x' }, { dir: ${JSON.stringify(X)} }).then(id => { window.__trustId = id; }); return true; })()`); await p.sleep(500);
  await check('trust step with a hostile unread path');
  const st = await p.eval(`(() => { const d = document.querySelector('.sheet'); return { list: [...d.querySelectorAll('#trList li')].map(l => l.textContent), yes: !d.querySelector('[data-ok]').disabled, once: /this session only/.test(d.innerText) }; })()`);
  if (st.list.length === 2 && st.list[0] === 'could not be read: ' + X && st.once) console.log('ok the trust step lists the unread path as text and says the yes is for this session only'); else { failed++; console.log('FAIL trust step: ' + JSON.stringify(st)); }
  await p.key('Escape'); await p.sleep(200); if ((await p.eval('window.__trustId')) !== null) { failed++; console.log('FAIL the trust step resolved an id without a yes'); }
  await p.eval(`SL.views.show('sessions')`); await p.sleep(700); await p.eval(FILL); await p.sleep(500); await check('sessions');
  await p.eval(`document.querySelector('[data-pick]').click()`); await p.sleep(200); await p.eval(`document.querySelector('[data-del]').click()`); await p.sleep(300); await check('sessions: delete selected confirm'); await p.key('Escape');
  await p.eval(`SL.views.show('tools')`); await p.sleep(700); await p.eval(`SL.D.spec.commands[0].summary = ${JSON.stringify(X)}; SL.D.spec.commands[0].usage = ${JSON.stringify(X)}; SL.loop.dirty = true; SL.bus.emit('data', 'cli')`); await p.sleep(500); await check('tools');
  await p.eval(`SL.ui.runCli(['sessions', 'prune'])`); await p.sleep(500); await p.eval(`(() => { const c = SL.runner.find(['sessions', 'prune']); c.summary = ${JSON.stringify(X)}; c.flags[0].desc = ${JSON.stringify(X)}; c.flags[0].choices = [${JSON.stringify(X)}]; SL.ui.runCli(['sessions', 'prune']); })()`); await p.sleep(500); await check('runner form');
  await p.eval(`SL.bus.emit('run', { id: 'none', lines: [{ k: 'out', t: ${JSON.stringify(X)} }] }); document.querySelector('.rrun').click()`); await p.sleep(1500); await check('runner output');
  await p.eval(`SL.views.show('doctor')`); await p.sleep(700); await p.eval(FILL); await p.sleep(500); await check('doctor');
  await p.eval(`SL.views.show('schedule')`); await p.sleep(700); await p.eval(FILL); await p.sleep(500); await check('schedule');
  await p.eval(`document.querySelector('[data-edit]').click()`); await p.sleep(300); await check('schedule: edit form'); await p.eval(`document.querySelector('[data-lg]').click()`); await p.sleep(500); await check('schedule: log');
  const errs = p.errors.filter(e => !/api\/recorded\/watching/.test(e));   // the fake server has no list of watched runs
  if (errs.length) { failed++; console.log('FAIL page errors:\n' + errs.join('\n')); }
} finally { await p.close(); srv.kill('SIGTERM'); }
console.log(failed ? failed + ' failed' : 'all checks passed');
process.exit(failed ? 1 : 0);
