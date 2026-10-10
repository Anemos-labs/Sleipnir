// confirm.browser.mjs: the server's confirmation of privilege raises, end to end on the REAL server (`sleipnir web --fixture all`) in
// headless Chromium: New session with mode yolo, with an allow rule, with a verify command; `/mode yolo` in the composer; an allow rule
// added on Settings › Permissions; Run settings Apply after a new verify command; `/swarm 3`; then the hint button of a `sys` row with
// `open` and the session directory in `/status`. Each step records what the page asked (the server's scope and reasons) and checks the
// effect arrived; screenshots of the questions go to OUT (default a temporary directory).
//
//   go build -o /tmp/x/sleipnir ./cmd/sleipnir
//   SLEIPNIR_BIN=/tmp/x/sleipnir [OUT=dir] node internal/web/uidev/test/confirm.browser.mjs
//
// The server runs with HOME and SLEIPNIR_HOME in a fresh temporary directory, removed with the server at the end. It needs a Chromium
// (CHROME_PATH, or the Playwright headless shell the mock's tests use); it is not part of `node --test` and not of CI.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url)), repo = path.resolve(here, '../../../..');
const { open } = await import(path.join(repo, 'docs/design/web-mocks/_src/v3/test/cdp.mjs'));
const bin = process.env.SLEIPNIR_BIN; if (!bin) { console.log('skip: set SLEIPNIR_BIN to a built ./cmd/sleipnir'); process.exit(0); }
const home = fs.mkdtempSync(path.join(process.env.SCRATCH || os.tmpdir(), 'web-confirm-')), out = process.env.OUT || fs.mkdtempSync(path.join(process.env.SCRATCH || os.tmpdir(), 'web-confirm-shots-'));
const srv = spawn(bin, ['web', '--addr', '127.0.0.1:0', '--fixture', 'all'], { stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, HOME: home, SLEIPNIR_HOME: path.join(home, 'state') } });
let serr = ''; srv.stderr.on('data', d => { serr += d; });
const cleanup = () => { try { srv.kill('SIGKILL'); } catch { /* gone */ } try { fs.rmSync(home, { recursive: true, force: true }); } catch { /* busy */ } };
process.once('exit', cleanup);
/** Stop the server as Ctrl-C would (it removes its fixture projects), then make sure. */
const stop = async () => { if (srv.exitCode == null) { srv.kill('SIGINT'); await Promise.race([new Promise(r => srv.once('exit', r)), new Promise(r => setTimeout(r, 10000))]); } cleanup(); };
const url = await new Promise((res, rej) => { let b = ''; srv.stdout.on('data', d => { b += d; const i = b.indexOf('\n'); if (i >= 0) res(b.slice(0, i).trim()); }); setTimeout(() => rej(new Error('no address: ' + serr.slice(-500))), 20000); });
const p = await open(url, { w: 1440, h: 900 });
const ev = js => p.eval('(async () => { ' + js + ' })()');
const waitFor = async (expr, ms = 15000) => { const t0 = Date.now(); for (;;) { if (await p.eval('!!(' + expr + ')').catch(() => false)) return true; if (Date.now() - t0 > ms) throw new Error('timed out waiting for ' + expr); await p.sleep(100); } };
let failed = 0; const results = [];
async function step(name, fn) { try { const r = await fn(); results.push(['ok', name, r]); console.log('ok  ', name, r ? JSON.stringify(r) : ''); } catch (e) { failed++; results.push(['FAIL', name, e.message]); console.log('FAIL', name, e.message); await p.shot(path.join(out, 'fail-' + name.replace(/\W+/g, '-') + '.png')).catch(() => {}); await ev(`SL.ui.closeModal && SL.ui.closeModal()`).catch(() => {}); } }
/** the question the page shows now: its title, reasons, whether it wants the mode typed */
const question = () => ev(`const s = document.querySelector('.sheet.confirm'); if (!s) return null; return { title: s.querySelector('.sh-h h2').textContent, text: s.querySelector('.sh-b').innerText.slice(0, 400), typed: !!s.querySelector('#cfType') };`);
/** answer the open confirm with OK (typing the mode first when asked) */
const okConfirm = async mode => { await ev(`const i = document.querySelector('.sheet.confirm #cfType'); if (i) { i.value = ${JSON.stringify(mode || '')}; i.dispatchEvent(new Event('input')); }`); await waitFor(`document.querySelector('.sheet.confirm [data-ok]:not([disabled])')`, 5000); await ev(`document.querySelector('.sheet.confirm [data-ok]').click();`); };
const typeIn = (sel, v) => ev(`const i = document.querySelector(${JSON.stringify(sel)}); i.value = ${JSON.stringify(v)}; i.dispatchEvent(new Event('input', { bubbles: true })); i.dispatchEvent(new Event('change', { bubbles: true }));`);
const composer = async line => { await ev(`const i = document.getElementById('input'); i.focus(); i.value = ${JSON.stringify(line)}; i.dispatchEvent(new Event('input'));`); await p.key('Escape'); await ev(`document.getElementById('input').focus()`); await p.key('Enter'); };
try {
  await waitFor(`window.SL && SL.live && SL.live.state === 'open' && SL.sessions.list.length >= 1`, 30000);
  await ev(`window.__asks = []; const o = SL.api.cfg.askConfirm; SL.api.cfg.askConfirm = a => { window.__asks.push({ scope: a.scope, reasons: a.reasons, path: a.path }); return o(a); };`);
  const asks = () => ev(`return window.__asks.splice(0);`);
  /** New session in the directory of the fixture tab `from` (the fixture's mock provider serves its own projects), with the model it uses. */
  const newSession = async (name, set, from) => {
    await ev(`SL.ui.dialogs.newSession()`); await waitFor(`document.getElementById('nsStart')`);
    if (from) await ev(`const T = SL.sessions.get(${JSON.stringify(from)}), c = document.getElementById('nsCwd'); if (T && Array.from(c.options).some(o => o.value === T.meta.cwd)) { c.value = T.meta.cwd; c.dispatchEvent(new Event('change', { bubbles: true })); }`);
    /* a local model name passes the server's model check without a key (the fixture's own provider serves only its tabs) */
    await typeIn('#nsName', name); await ev(`const m = document.getElementById('nsModel'), o = document.createElement('option'); o.textContent = 'ollama/confirm-test'; m.appendChild(o); m.value = 'ollama/confirm-test'; m.dispatchEvent(new Event('change', { bubbles: true }));`); await set();
    await ev(`document.getElementById('nsStart').click()`);
    let seen = [];
    for (let i = 0; i < 3; i++) {   // the trust question and/or the server's confirmation
      const q = await waitFor(`document.querySelector('.sheet.confirm') || SL.sessions.list.some(S => S.name === ${JSON.stringify(name)})`, 20000).then(question);
      if (!q) break; seen.push(q); await p.shot(path.join(out, 'new-' + name + '-' + i + '.png')); await okConfirm(); await p.sleep(300);
    }
    await waitFor(`SL.sessions.list.some(S => S.name === ${JSON.stringify(name)})`, 30000);
    const S = await ev(`const S = SL.sessions.list.find(S => S.name === ${JSON.stringify(name)}); return { id: S.id, mode: S.meta.mode, rules: (S.meta.rules || []).map(r => r.rule), verify: S.meta.verify };`);
    return { asked: await asks(), seen: seen.map(q => q.title + (q.typed ? ' (typed)' : '')), texts: seen.map(q => q.text.replace(/\s+/g, ' ')), session: S };
  };
  await step('new session with mode yolo', async () => { const r = await newSession('yolo-one', () => ev(`document.querySelector('[data-nsmode="yolo"]').click()`)); if (r.session.mode !== 'yolo') throw new Error('mode is ' + r.session.mode + ' ' + JSON.stringify(r)); if (!r.asked.some(a => a.reasons.includes('permission mode yolo'))) throw new Error('no question named the mode: ' + JSON.stringify(r)); if (!r.texts.some(t => /the request POST \/api\/sessions\s+its scope session:/i.test(t))) throw new Error('the question does not name the request and its scope: ' + JSON.stringify(r.texts)); return r; });
  await step('new session with an allow rule', async () => { const r = await newSession('allow-one', async () => { await typeIn('#nsRule', 'Bash(make lint:*)'); await ev(`document.getElementById('nsAddRule').click()`); }, 'orders-api'); if (!r.asked.some(a => a.reasons.some(x => /^allow .*make lint/.test(x)))) throw new Error('no question named the rule: ' + JSON.stringify(r)); return r; });
  await step('new session with a verify command', async () => { const r = await newSession('verify-one', () => typeIn('#nsVerify', 'go vet ./...'), 'docs-sweep'); if (!r.asked.some(a => a.reasons.some(x => /verify command go vet/.test(x)))) throw new Error('no question named the verify command: ' + JSON.stringify(r)); return r; });
  const shop = await ev(`return SL.sessions.list[0].id;`);
  await ev(`SL.act.switchSession(${JSON.stringify(shop)}); SL.views.show('cockpit');`); await p.sleep(500);
  await step('/mode yolo in the composer (typed in the Mode sheet, asked once)', async () => {
    await composer('/mode yolo'); await waitFor(`document.getElementById('cfIn')`); await p.shot(path.join(out, 'mode-yolo.png'));
    await typeIn('#cfIn', 'yolo'); await ev(`document.getElementById('cfGo').click()`);
    await waitFor(`SL.sessions.get(${JSON.stringify(shop)}).meta.mode === 'yolo'`); const a = await asks(); if (a.length) throw new Error('asked again: ' + JSON.stringify(a)); return { mode: 'yolo' };
  });
  await step('Settings › Permissions: add an allow rule', async () => {
    await ev(`SL.ui.settingsPage('permissions')`); await waitFor(`document.getElementById('ruleIn')`);
    await ev(`document.getElementById('ruleEff').value = 'allow'; document.getElementById('ruleIn').value = 'Bash(make check:*)'; document.querySelector('[data-do="rule-add"]').click();`);
    const q = await waitFor(`document.querySelector('.sheet.confirm')`).then(question); await p.shot(path.join(out, 'permissions-allow.png')); await okConfirm();
    await waitFor(`(SL.sessions.get(${JSON.stringify(shop)}).meta.rules || []).some(r => /make check/.test(r.rule))`); return { question: q.title, asked: await asks() };
  });
  await step('Run settings Apply after a new verify command', async () => {
    const gen = await ev(`return SL.sessions.get(${JSON.stringify(shop)}).gen;`);
    await ev(`SL.ui.settingsPage('run')`); await waitFor(`document.getElementById('rv')`);
    await typeIn('#rv', 'go build ./...'); await ev(`document.querySelector('[data-do="verify"]').click()`); await p.sleep(800);
    await ev(`document.querySelector('[data-do="apply"]').click()`); await waitFor(`document.querySelector('.sheet.confirm')`); const first = await question(); await okConfirm();
    let second = null; try { await waitFor(`document.querySelector('.sheet.confirm')`, 8000); second = await question(); await p.shot(path.join(out, 'apply-raise.png')); await okConfirm(); } catch (e) { /* nothing raised */ }
    await waitFor(`SL.sessions.get(${JSON.stringify(shop)}).gen !== ${JSON.stringify(gen)}`, 30000); return { first: first.title, second: second && second.title, asked: await asks() };
  });
  await step('/swarm 3 in the composer', async () => {
    await ev(`SL.views.show('cockpit')`); await p.sleep(300); await composer('/swarm 3'); await waitFor(`document.querySelector('.sheet.confirm')`); const first = await question(); await okConfirm();
    let second = null; try { await waitFor(`document.querySelector('.sheet.confirm')`, 6000); second = await question(); await okConfirm(second.typed ? 'yolo' : ''); } catch (e) { /* nothing raised */ }
    await waitFor(`SL.sessions.get(${JSON.stringify(shop)}).meta.swarm === 3`, 40000); return { first: first.title, second: second && (second.title + (second.typed ? ' (typed)' : '')), asked: await asks() };
  });
  await step('/restart --mode bypass asks for the typed mode name (the server\'s scope)', async () => {
    const tab = await ev(`return (SL.sessions.list.find(S => S.id === 'orders-api') || SL.sessions.list[1]).id;`);
    await ev(`SL.act.switchSession(${JSON.stringify(tab)}); SL.views.show('cockpit');`); await p.sleep(500);
    await composer('/restart --mode bypass'); await waitFor(`document.querySelector('.sheet.confirm')`); const first = await question(); await okConfirm();
    await waitFor(`document.querySelector('.sheet.confirm #cfType')`, 10000); const second = await question(); await p.shot(path.join(out, 'restart-bypass-typed.png'));
    const before = await ev(`return !document.querySelector('.sheet.confirm [data-ok]').disabled;`); await okConfirm('bypass');
    await waitFor(`SL.sessions.get(${JSON.stringify(tab)}).meta.mode === 'bypass'`, 30000);
    await ev(`SL.act.switchSession(${JSON.stringify(shop)})`); return { first: first.title, second: second.title + (second.typed ? ' (typed)' : ''), okBeforeTyping: before, asked: await asks() };
  });
  await step('a hint row with open shows its button and opens the page', async () => {
    await ev(`SL.views.show('cockpit'); const S = SL.sessions.get(${JSON.stringify(shop)}); SL.live.applyEv(S, { seq: S.lastSeq + 1, t: S.wt, k: 'sys', ch: 'mgr', glyph: '⚠', text: 'The provider refused the key.', open: 'providers' });`);
    await waitFor(`document.querySelector('#talk [data-open-page="providers"]')`); await ev(`document.querySelector('#talk [data-open-page="providers"]').click()`);
    await waitFor(`SL.views.current && SL.views.current.name === 'settings'`); return { opened: await ev(`return SL.ui.setPage;`) };
  });
  await step('/status shows the session directory', async () => {
    await ev(`SL.views.show('cockpit')`); await composer('/status'); await waitFor(`Array.from(document.querySelectorAll('#talk .msg.local')).some(m => /session directory/.test(m.textContent))`);
    return { dir: await ev(`return SL.sessions.get(${JSON.stringify(shop)}).meta.sessionDir;`) };
  });
  const errs = p.errors.filter(e => !/^log: Failed to load resource: the server responded with a status of (404|409|428)/.test(e));
  if (errs.length) { failed++; console.log('FAIL page errors:\n' + errs.slice(0, 10).join('\n')); }
} finally { await p.close(); await stop(); }
console.log((failed ? failed + ' failed' : 'all steps passed') + '; screenshots in ' + out);
process.exit(failed ? 1 : 0);
