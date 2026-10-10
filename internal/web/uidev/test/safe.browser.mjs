// safe.browser.mjs: hostile text renders as text. Runs the real page in headless Chromium against the fake server
// (internal/web/webtest), puts markup into every place untrusted text reaches (model text, tool output, file names, mail, session
// names, question commands and changes, the recorded list, MCP and provider names) and checks that no element was created from it
// and that the text shows as typed.
//
//   go build -o /tmp/fakeserver ./internal/web/webtest/cmd/fakeserver
//   FAKESERVER=/tmp/fakeserver node internal/web/uidev/test/safe.browser.mjs
//
// It needs a Chromium (CHROME_PATH, or the Playwright headless shell: see cdp.mjs); it is not part of `node --test` and not of CI.
import { spawn } from 'node:child_process';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url)), repo = path.resolve(here, '../../../..');
const { open } = await import(path.join(here, 'cdp.mjs'));
const bin = process.env.FAKESERVER; if (!bin) { console.log('skip: set FAKESERVER to a built internal/web/webtest/cmd/fakeserver'); process.exit(0); }
const srv = spawn(bin, ['-addr', '127.0.0.1:0', '-ui', path.join(repo, 'internal/web/ui'), '-speed', '0'], { stdio: ['ignore', 'pipe', 'inherit'] });
process.once('exit', () => { try { srv.kill('SIGKILL'); } catch { /* gone */ } });
const url = await new Promise((res, rej) => { let b = ''; srv.stdout.on('data', d => { b += d; const i = b.indexOf('\n'); if (i >= 0) res(b.slice(0, i).trim()); }); setTimeout(() => rej(new Error('the fake server printed no address')), 15000); });
const p = await open(url, { w: 1440, h: 900 });
const X = '<img src=x data-pwn=1 onerror="window.__pwn=1">';
let failed = 0;
const check = async (what) => {
  const r = await p.eval(`(() => ({ imgs: document.querySelectorAll('img,[data-pwn],iframe,object').length, scripts: document.scripts.length, pwn: window.__pwn || 0, text: document.body.innerText.includes(${JSON.stringify(X)}) }))()`);
  try { assert.equal(r.imgs, 0, what + ': an element was made from text'); assert.equal(r.pwn, 0, what + ': a handler ran'); assert.equal(r.scripts, 30, what + ': a script element appeared'); console.log('ok', what, r.text ? '(shown as text)' : ''); }
  catch (e) { failed++; console.log('FAIL', e.message); }
};
try {
  await p.sleep(2500);
  await p.eval(`(() => { const S = SL.sessions.active, X = ${JSON.stringify(X)}; let seq = S.lastSeq;
    const ev = e => SL.live.applyEv(S, Object.assign({ seq: ++seq, t: S.wt }, e));
    ev({ k: 'say', who: 'mgr', text: X, stream: false });
    ev({ k: 'say', who: 'you', text: X });
    ev({ k: 'sys', ch: 'mgr', glyph: '⚠', text: X });
    ev({ k: 'tool', id: 'be-1', name: X, arg: X, out: X, ok: true, file: X, add: 1, del: 0 });
    ev({ k: 'tool', id: 'mgr', name: 'Edit', arg: X, out: '', ok: false, refused: true, reason: X });
    ev({ k: 'mail', from: 'be-1', to: 'fe-1', text: X });
    ev({ k: 'note', id: 'be-1', g: 'done', text: X });
    ev({ k: 'break', id: 'be-1', kind: X, read: 0, expected: 100, why: X });
    ev({ k: 'stream', id: 'be-1', text: X, rate: 9999, mid: 'mx' });
    ev({ k: 'state', id: 'be-1', s: 'tool', doing: X, task: 'T2' });
    ev({ k: 'task', id: 'T9', title: X, owner: 'be-1', s: 'todo', failed: true, closure: X });
    ev({ k: 'verdict', text: X, kind: 'continue', left: [X] });
    ev({ k: 'plan', steps: [X, 'b'], st: ['act', 'pending'] });
    ev({ k: 'ask', q: { id: 'q_hostile', agent: 'be-1', task: 'T2', cmd: X, cwd: X, why: X, scope: X, what: X, kind: 'edit', tool: 'edit', path: X, change: '@@ -1 +1 @@\\n-' + X + '\\n+' + X } });
    SL.live.H.tab({ op: 'update', tab: Object.assign({}, { id: S.id, sid: S.sid, name: X }) });
    SL.live.H.toast({ text: X, kind: 'err' });
    SL.sessions.reg.recorded.push({ id: '20261001-000000-aaaaaa', first: X, model: X, cost: 0, mb: 0, ageS: 9e5, agents: 1, resumable: true });
    SL.data.map.mcp({ servers: [{ name: X, origin: X, transport: 'stdio', state: X, tools: [X] }] });
    SL.loop.dirty = true; return true; })()`);
  await p.sleep(1500); await check('transcript, feed, stalls, question, strip');
  for (const v of ['cache', 'mail', 'board', 'replay', 'sessions']) { await p.eval(`SL.views.show(${JSON.stringify(v)})`); await p.sleep(600); await check('view ' + v); }
  await p.eval(`SL.views.show('cockpit')`); await p.sleep(400);
  await p.eval(`document.querySelector('.stall[data-ag="be-1"]').click()`); await p.sleep(400);
  for (const t of ['info', 'log', 'mail', 'cache']) { await p.eval(`document.querySelector('.drawer [data-tab="${t}"]').click()`); await p.sleep(300); await check('drawer ' + t); }
  await p.key('Escape');
  await p.eval(`document.getElementById('sInbox').click()`); await p.sleep(400); await check('inbox'); await p.key('Escape');
  await p.eval(`document.getElementById('sRes').click()`); await p.sleep(400); await check('resume dialog'); await p.key('Escape');
  await p.eval(`document.getElementById('goalBtn').click()`); await p.sleep(400); await check('goal sheet'); await p.key('Escape');
  await p.eval(`SL.palette.open('')`); await p.sleep(400); await check('palette'); await p.key('Escape');
  await p.eval(`SL.palette.runLine('/status')`); await p.sleep(400); await check('/status card');
  if (await p.eval(`!!(SL.ui.settingsPage)`)) { await p.eval(`SL.ui.settingsPage('mcp')`); await p.sleep(600); await check('settings mcp'); }
  const errs = p.errors.filter(e => !/^log: Failed to load resource: the server responded with a status of 404/.test(e));   /* a route the test server does not have is not a page error */
  if (errs.length) { failed++; console.log('FAIL page errors:\n' + errs.slice(0, 10).join('\n')); }
} finally { await p.close(); srv.kill('SIGTERM'); }
console.log(failed ? failed + ' failed' : 'all checks passed');
process.exit(failed ? 1 : 0);
