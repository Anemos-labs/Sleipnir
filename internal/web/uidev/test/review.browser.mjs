// review.browser.mjs: what a person does with the page while the server keeps talking, on the REAL server (`sleipnir web --fixture
// all`) in headless Chromium: typing in a form while a roster frame arrives; writing "what to do instead" while a second question
// arrives; a question while the view is held or paused; the arrow keys on the session tabs and the views rail; Enter while an input
// method composes; the Doctor, Runner, Schedule and Sessions views and the New session dialog at phone and short-window sizes; what a
// screen reader hears; a message or a New session the server refuses; error toasts; the session menu opened and closed many times.
//
//   go build -o /tmp/x/sleipnir ./cmd/sleipnir
//   SLEIPNIR_BIN=/tmp/x/sleipnir [OUT=dir] [UI_REV=<git revision>] node internal/web/uidev/test/review.browser.mjs
//
// The page's files come from this checkout (uisource.mjs): the files on disk, or with UI_REV those of a git revision (an older one shows
// which checks fail there). The server runs with HOME and SLEIPNIR_HOME in a fresh temporary directory, outside the repository, removed
// with the server at the end. It needs a Chromium (CHROME_PATH, or the Playwright headless shell: see cdp.mjs); not part of CI.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { serveUi } from './uisource.mjs';

const here = path.dirname(fileURLToPath(import.meta.url)), repo = path.resolve(here, '../../../..');
const { open } = await import(path.join(here, 'cdp.mjs'));
const bin = process.env.SLEIPNIR_BIN; if (!bin) { console.log('skip: set SLEIPNIR_BIN to a built ./cmd/sleipnir'); process.exit(0); }
const base = process.env.SCRATCH || os.tmpdir(); if (path.resolve(base).startsWith(repo + path.sep)) throw new Error('the server runs outside the repository');
const home = fs.mkdtempSync(path.join(base, 'web-review-')), out = process.env.OUT || fs.mkdtempSync(path.join(base, 'web-review-shots-'));
const srv = spawn(bin, ['web', '--addr', '127.0.0.1:0', '--fixture', 'all'], { cwd: home, stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, HOME: home, SLEIPNIR_HOME: path.join(home, 'state') } });
let serr = ''; srv.stderr.on('data', d => { serr += d; });
const cleanup = () => { try { srv.kill('SIGKILL'); } catch { /* gone */ } try { fs.rmSync(home, { recursive: true, force: true }); } catch { /* busy */ } };
process.once('exit', cleanup);
const stop = async () => { if (srv.exitCode == null) { srv.kill('SIGINT'); await Promise.race([new Promise(r => srv.once('exit', r)), new Promise(r => setTimeout(r, 10000))]); } cleanup(); };
const url = await new Promise((res, rej) => { let b = ''; srv.stdout.on('data', d => { b += d; const i = b.indexOf('\n'); if (i >= 0) res(b.slice(0, i).trim()); }); setTimeout(() => rej(new Error('no address: ' + serr.slice(-500))), 20000); });
const p = await open('about:blank', { w: 1440, h: 900 });
await p.send('Page.addScriptToEvaluateOnNewDocument', { source: fs.readFileSync(path.join(here, '..', 'hooks-real.js'), 'utf8') });
await serveUi(p, { rev: process.env.UI_REV || '' });
const ev = js => p.eval('(async () => { ' + js + ' })()');
const waitFor = async (expr, ms = 15000) => { const t0 = Date.now(); for (;;) { if (await p.eval('!!(' + expr + ')').catch(() => false)) return true; if (Date.now() - t0 > ms) throw new Error('timed out waiting for ' + expr); await p.sleep(100); } };
let failed = 0;
async function step(name, fn) { await ev(`const S = SL.sessions.active; if (S && S.replay) { S.goLive(); S.touch(); } SL.ui.closeModal && SL.ui.closeModal(); for (const q of (S ? S.wm.qs : []).filter(q => !q.answered && /^qr-/.test(q.id))) SL.live.applyEv(S, { seq: S.lastSeq + 1, t: S.wt, k: 'answer', qid: q.id, choice: 3, by: 'you' });`).catch(() => {}); try { const r = await fn(); console.log('ok  ', name, r ? JSON.stringify(r) : ''); } catch (e) { failed++; console.log('FAIL', name, '·', e.message); await p.shot(path.join(out, 'fail-' + name.replace(/\W+/g, '-').slice(0, 60) + '.png')).catch(() => {}); await ev(`SL.ui.closeModal && SL.ui.closeModal(); SL.ui.inbox && SL.ui.inbox.close()`).catch(() => {}); } }
const must = (c, why) => { if (!c) throw new Error(why); };
const ask = (id, cmd) => ev(`const S = SL.sessions.active; SL.live.applyEv(S, { seq: S.lastSeq + 1, t: S.wt, k: 'ask', q: { id: ${JSON.stringify(id)}, agent: 'mgr', kind: 'command', cmd: ${JSON.stringify(cmd)}, cwd: '.', why: 'w', what: 'this command', rule: 'Bash(x)' } });`);
const closeAll = () => ev(`const S = SL.sessions.active; for (const q of S.wm.qs.filter(q => !q.answered && /^qr-/.test(q.id))) SL.live.applyEv(S, { seq: S.lastSeq + 1, t: S.wt, k: 'answer', qid: q.id, choice: 3, by: 'you' });`);
/* a button a person cannot reach: outside the box of an ancestor that clips without scrolling */
/* (a box that scrolls on an axis makes what is in it reachable on that axis, as long as the box itself is) */
const CLIPPED = `const clipped = (el, ax) => { const r = el.getBoundingClientRect(); if (!r.width) return false; for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) { const cs = getComputedStyle(a), ar = a.getBoundingClientRect(), outY = r.bottom > ar.bottom + 1 || r.top < ar.top - 1, outX = r.right > ar.right + 1 || r.left < ar.left - 1;
  if (outY && /auto|scroll/.test(cs.overflowY)) return clipped(a); if (outX && /auto|scroll/.test(cs.overflowX)) return clipped(a);
  if (outY && /hidden|clip/.test(cs.overflowY)) return true; if (outX && /hidden|clip/.test(cs.overflowX)) return true; } return false; };`;

try {
  await p.send('Page.navigate', { url });
  await waitFor(`window.SL && SL.live && SL.live.state === 'open' && SL.sessions.list.length >= 3 && SL.test`, 30000);
  /* a tab with no question of its own: the injected questions are the front ones */
  await p.sleep(4000); const quiet = await ev(`const S = SL.sessions.list.find(S => !S.recorded && !SL.calc.openQuestion(S.wm)) || SL.sessions.list[0]; return S.id;`);
  await ev(`SL.act.switchSession(${JSON.stringify(quiet)}); SL.views.show('cockpit');`); await p.sleep(1500);

  await step('(2) a roster frame keeps what is typed in Schedule and in Settings › Budget; the cockpit takes the new worker', async () => {
    const frame = n => ev(`const S = SL.sessions.active, r = S.roster.map(x => Object.assign({}, x)); SL.live.H.roster({ tab: S.id, roster: r.concat([{ id: 'be-${n}', role: 'backend', code: 'be', nth: ${n}, k: r.length, leg: r.length - 1, scope: '', ro: false, model: '', spawn: 0 }]) });`);
    await ev(`SL.views.show('schedule')`); await waitFor(`document.querySelector('.schv textarea')`);
    await ev(`const g = document.querySelector('.schv textarea'); g.focus(); g.value = 'summarise the open PRs every morning'; g.dispatchEvent(new Event('input', { bubbles: true })); g.setSelectionRange(5, 9);`);
    await frame(7); await p.sleep(600);
    const a = await ev(`const g = document.querySelector('.schv textarea'); return { v: g && g.value, focus: document.activeElement === g, sel: g && [g.selectionStart, g.selectionEnd], view: SL.views.current.name };`);
    must(a.v === 'summarise the open PRs every morning' && a.focus && a.view === 'schedule', 'schedule: ' + JSON.stringify(a));
    await ev(`SL.ui.settingsPage('budget')`); await waitFor(`document.getElementById('bIn')`);
    await ev(`const i = document.getElementById('bIn'); i.focus(); i.value = '12.5'; i.dispatchEvent(new Event('input', { bubbles: true }));`);
    await frame(8); await p.sleep(600);
    const b = await ev(`const i = document.getElementById('bIn'); return { v: i && i.value, focus: document.activeElement === i };`);
    must(b.v === '12.5' && b.focus, 'budget: ' + JSON.stringify(b));
    await ev(`SL.views.show('cockpit')`); await p.sleep(500); const n0 = await ev(`return document.querySelectorAll('.stall:not(.ghost)').length;`);
    await frame(9); await p.sleep(600); const n1 = await ev(`return document.querySelectorAll('.stall:not(.ghost)').length;`);
    must(n1 === n0 + 1, 'cockpit stalls ' + n0 + ' -> ' + n1);
    return { schedule: a.sel, stalls: [n0, n1] };
  });

  await step('(3) a second question keeps a half-written "what to do instead" and its focus, in the strip and in the inbox; keys type into it', async () => {
    await ev(`SL.views.show('cockpit'); document.activeElement && document.activeElement.blur();`); await ask('qr-a', 'git push origin main');
    await waitFor(`document.querySelector('#qSlot .qopts[data-q="qr-a"]')`); await p.sleep(1300); await p.key('3'); await waitFor(`document.querySelector('#qSlot .tellIn')`);
    await p.send('Input.insertText', { text: 'use a branch and open a PR instead' });
    await ask('qr-b', 'rm -rf build'); await p.sleep(800); await p.key('o'); await p.key('Space'); await p.sleep(300);
    const s = await ev(`const i = document.querySelector('#qSlot .tellIn'); return { v: i && i.value, focus: document.activeElement === i, more: (document.querySelector('#qSlot .qmore') || {}).textContent, view: SL.views.current.name, replay: !!SL.sessions.active.replay };`);
    must(s.v === 'use a branch and open a PR insteado ' && s.focus && /\+1 waiting/.test(s.more) && s.view === 'cockpit' && !s.replay, 'strip: ' + JSON.stringify(s));
    await p.key('Escape'); await closeAll(); await p.sleep(300);
    await ask('qr-c', 'npm publish'); await ev(`SL.ui.inbox.toggle()`); await waitFor(`document.querySelector('.ibxpop .qopts[data-q="qr-c"]')`); await p.sleep(1300);
    await ev(`document.querySelector('.ibxpop .qopts[data-q="qr-c"] [data-choice="3"]').click();`); await waitFor(`document.querySelector('.ibxpop .tellIn')`);
    await ev(`document.querySelector('.ibxpop .tellIn').focus();`); await p.send('Input.insertText', { text: 'do not publish' });
    await ask('qr-d', 'npm run deploy'); await p.sleep(600);
    const i = await ev(`const t = document.querySelector('.ibxpop .tellIn'); return { v: t && t.value, focus: document.activeElement === t };`);
    must(i.v === 'do not publish' && i.focus, 'inbox: ' + JSON.stringify(i));
    await ev(`SL.ui.inbox.close()`); await closeAll(); return { strip: s.v, inbox: i.v };
  });

  await step('(5) a question shows at once while the view is held (pointer over the chat) or paused (Space), with a toast when paused', async () => {
    const r = await ev(`const r = document.querySelector('#talk').getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2];`);
    await p.move(r[0], r[1]); await p.sleep(700); await ask('qr-hold', 'git push');
    await waitFor(`document.querySelector('#qSlot .qopts[data-q="qr-hold"]')`, 3000).catch(() => {});
    const held = await ev(`return { strip: !!document.querySelector('#qSlot .qopts[data-q="qr-hold"]'), hover: SL.time.T.hover };`);
    await p.shot(path.join(out, 'q-held.png')); await p.move(5, 5); await p.sleep(500); await closeAll(); await p.sleep(300);
    await ev(`document.activeElement && document.activeElement.blur();`); await p.key(' '); await p.sleep(300); await ask('qr-pause', 'git push --force');
    await waitFor(`document.querySelector('#qSlot .qopts[data-q="qr-pause"]')`, 3000).catch(() => {});
    const paused = await ev(`return { strip: !!document.querySelector('#qSlot .qopts[data-q="qr-pause"]'), replay: !!SL.sessions.active.replay, toast: Array.from(document.querySelectorAll('.toast')).map(t => t.textContent).join(' | ') };`);
    await p.shot(path.join(out, 'q-paused.png')); await ev(`SL.sessions.active.goLive(); SL.sessions.active.touch();`); await closeAll();
    must(held.strip, 'held: ' + JSON.stringify(held)); must(paused.strip && paused.replay && /question is waiting \(the view is paused\)/.test(paused.toast), 'paused: ' + JSON.stringify(paused));
    return { held: held.strip, paused: paused.strip };
  });

  await step('(9) the arrows, Home and End on the session tabs and the views rail move there, never the view\'s clock or the agent', async () => {
    const st = () => ev(`const S = SL.sessions.active; return { replay: !!S.replay, sel: SL.link.sel || null, focus: document.activeElement && (document.activeElement.dataset.sid || document.activeElement.dataset.nav || document.activeElement.tagName) };`);
    await ev(`SL.sessions.active.goLive(); document.querySelector('.stab.sel').focus();`); await p.key('ArrowRight'); await p.sleep(200); const a = await st();
    await ev(`SL.sessions.active.goLive(); document.querySelector('.stab.sel').focus();`); await p.key('Home'); await p.sleep(200); const b = await st();
    await ev(`SL.sessions.active.goLive(); document.querySelector('.stab.sel').focus();`); await p.key('End'); await p.sleep(200); const c = await st();
    await ev(`SL.sessions.active.goLive(); SL.link.select(null); document.querySelector('#nav .nvi').focus();`); await p.key('ArrowDown'); await p.sleep(200); const d = await st();
    must(!a.replay && !b.replay && !c.replay && !d.replay && !d.sel, JSON.stringify({ a, b, c, d }));
    await ev(`SL.act.switchSession(${JSON.stringify(quiet)}); SL.views.show('cockpit');`); return { tabs: a.focus, nav: d.focus };
  });

  await step('(10) Enter while an input method composes sends nothing and keeps the text; a composing key runs no shortcut', async () => {
    await ev(`window.__sent = 0; const f0 = window.fetch; window.fetch = (u, i) => { if (/\\/messages$/.test(String(u))) window.__sent++; return f0(u, i); };
      const i = document.getElementById('input'); i.focus(); i.value = 'にほんご'; i.dispatchEvent(new Event('input', { bubbles: true }));
      i.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, bubbles: true, cancelable: true }));
      document.body.focus(); document.activeElement.blur(); document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'm', keyCode: 229, bubbles: true, cancelable: true }));`);
    await p.sleep(400);
    const r = await ev(`return { v: document.getElementById('input').value, sent: window.__sent, view: SL.views.current.name };`);
    await ev(`const i = document.getElementById('input'); i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true }));`);
    must(r.v === 'にほんご' && r.sent === 0 && r.view === 'cockpit', JSON.stringify(r)); return r;
  });

  await step('(11) Doctor, Runner, Schedule and Sessions keep every button reachable on a phone and in a short window', async () => {
    const res = {};
    for (const [w, h] of [[375, 812], [1024, 600]]) {
      await p.viewport(w, h); await p.sleep(400);
      for (const v of ['doctor', 'runner', 'schedule', 'sessions']) {
        await ev(`SL.views.show(${JSON.stringify(v)}); document.getElementById('app').dataset.pv = 'main';`); await p.sleep(700);
        const c = await ev(CLIPPED + ` return Array.from(document.querySelectorAll('#views button, #views input, #views select')).filter(b => b.offsetParent && clipped(b)).map(b => b.id || b.textContent.trim().slice(0, 24));`);
        await p.shot(path.join(out, 'view-' + v + '-' + w + 'x' + h + '.png')); res[v + '@' + w] = c.length; must(!c.length, v + ' at ' + w + 'x' + h + ': clipped ' + JSON.stringify(c));
      }
    }
    await p.viewport(1440, 900); await ev(`SL.views.show('cockpit')`); return res;
  });

  await step('(12) the New session dialog wraps its rows on a phone and a tablet: nothing scrolls sideways, Add stays in reach', async () => {
    const res = {};
    for (const [w, h] of [[375, 812], [768, 1024]]) {
      await p.viewport(w, h); await p.sleep(300); await ev(`SL.ui.dialogs.newSession()`); await waitFor(`document.getElementById('nsAddRole')`); await p.sleep(300);
      const r = await ev(`const b = document.querySelector('.sheet .sh-b'), s = document.querySelector('.sheet').getBoundingClientRect(), a = document.getElementById('nsAddRole').getBoundingClientRect(), m = document.getElementById('nsRoleM').getBoundingClientRect();
        return { sideways: b.scrollWidth - b.clientWidth, addIn: a.right <= s.right + 1 && a.left >= s.left - 1, modelIn: m.right <= s.right + 1 };`);
      await ev(`document.getElementById('nsAddRole').scrollIntoView({ block: 'center' })`); await p.shot(path.join(out, 'newsession-' + w + '.png')); await ev(`SL.ui.closeModal()`);
      res[w] = r; must(r.sideways <= 1 && r.addIn && r.modelIn, w + ': ' + JSON.stringify(r));
    }
    await p.viewport(1440, 900); return res;
  });

  await step('(13) a screen reader hears a question once (not the ticking wait), a toast once; small buttons take a 24 px press', async () => {
    const live = await ev(`return { slot: document.getElementById('qSlot').getAttribute('aria-live'), status: (document.getElementById('qStatus') || {}).getAttribute && document.getElementById('qStatus').getAttribute('role') };`);
    await ask('qr-sr', 'make deploy'); await waitFor(`document.querySelector('#qSlot .qopts[data-q="qr-sr"]')`); await p.sleep(300);
    const t1 = await ev(`return document.getElementById('qStatus').textContent;`); await p.sleep(1600); const t2 = await ev(`return document.getElementById('qStatus').textContent;`);
    await closeAll();
    const an0 = await ev(`return document.getElementById('announcer').textContent;`); await ev(`SL.ui.toast('a toast to hear once', 'ok')`); const an1 = await ev(`return document.getElementById('announcer').textContent;`);
    const hit = await ev(`const probe = el => { const r = el.getBoundingClientRect(), cx = r.left + r.width / 2, cy = r.top + r.height / 2; return [[cx, cy - 11.5], [cx, cy + 11.5], [cx - 11.5, cy], [cx + 11.5, cy]].every(([x, y]) => { const h = document.elementFromPoint(x, y); return h && (h === el || el.contains(h)); }); };
      const sm = Array.from(document.querySelectorAll('.btn.sm')).find(b => b.offsetParent && b.getBoundingClientRect().height < 24), x = document.querySelector('.stabw.sel .stx');
      return { sm: sm ? [Math.round(sm.getBoundingClientRect().height), probe(sm)] : null, x: x ? [Math.round(x.getBoundingClientRect().height), probe(x)] : null };`);
    must(!live.slot && live.status === 'alert', 'live regions: ' + JSON.stringify(live)); must(/asks: make deploy/.test(t1) && t1 === t2, 'question status: ' + JSON.stringify([t1, t2]));
    must(an0 === an1, 'the toast was also put into the announcer'); must(hit.sm && hit.sm[1] && hit.x && hit.x[1], 'hit areas: ' + JSON.stringify(hit));
    return { status: t1, hit };
  });

  await step('(14) a refused message comes back into the composer; a refused New session reopens with its fields and why; an error toast stays', async () => {
    await ev(`window.__fail = true; const f1 = window.fetch; window.fetch = (u, i) => (window.__fail && /\\/messages$/.test(String(u))) ? Promise.resolve(new Response(JSON.stringify({ error: 'the session is busy', code: 'busy' }), { status: 409, headers: { 'Content-Type': 'application/json' } })) : f1(u, i);
      const i = document.getElementById('input'); i.focus(); i.value = 'please keep me'; i.dispatchEvent(new Event('input', { bubbles: true }));`);
    await p.key('Enter'); await p.sleep(800);
    const c = await ev(`return document.getElementById('input').value;`); await ev(`window.__fail = false; const i = document.getElementById('input'); i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true }));`);
    await ev(`SL.sessions.active.seek(0, false); SL.sessions.active.touch(); const i = document.getElementById('input'); i.focus(); i.value = 'not in a replay'; i.dispatchEvent(new Event('input', { bubbles: true }));`); await p.key('Enter'); await p.sleep(300);
    const rp = await ev(`const v = document.getElementById('input').value; SL.sessions.active.goLive(); SL.sessions.active.touch(); const i = document.getElementById('input'); i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true })); return v;`);
    await ev(`SL.ui.dialogs.newSession()`); await waitFor(`document.getElementById('nsStart')`);
    await ev(`const n = document.getElementById('nsName'); n.value = 'kept-name'; n.dispatchEvent(new Event('input', { bubbles: true })); const r = document.getElementById('nsRule'); r.value = 'Bash(make lint:*)';
      const m = document.getElementById('nsModel'), o = document.createElement('option'); o.textContent = 'nowhere/no-such-model'; m.appendChild(o); m.value = o.textContent; m.dispatchEvent(new Event('change', { bubbles: true })); document.getElementById('nsStart').click();`);
    await waitFor(`document.querySelector('.sheet .nserr')`, 20000).catch(() => {}); await p.sleep(300);
    const ns = await ev(`const e = document.querySelector('.sheet .nserr'); return { err: e && e.textContent, name: (document.getElementById('nsName') || {}).value, rules: (document.getElementById('nsRules') || {}).textContent };`);
    await p.shot(path.join(out, 'newsession-refused.png')); await ev(`SL.ui.closeModal()`);
    await ev(`SL.ui.toast('an error that stays', 'err')`); await p.sleep(5000); const stays = await ev(`return Array.from(document.querySelectorAll('.toast.err')).some(t => /an error that stays/.test(t.textContent));`);
    await ev(`const t = Array.from(document.querySelectorAll('.toast.err')).find(t => /an error that stays/.test(t.textContent)); t && t.click();`); await p.sleep(200);
    const gone = await ev(`return !Array.from(document.querySelectorAll('.toast.err')).some(t => /an error that stays/.test(t.textContent));`);
    must(c === 'please keep me', 'composer after a refusal: ' + JSON.stringify(c)); must(rp === 'not in a replay', 'composer in a replay: ' + JSON.stringify(rp));
    must(ns.err && ns.name === 'kept-name' && /make lint/.test(ns.rules || ''), 'New session: ' + JSON.stringify(ns)); must(stays && gone, 'error toast: ' + JSON.stringify({ stays, gone }));
    return { ns: ns.err };
  });

  await step('(17) the session menu opened and closed 15 times leaves no listener behind; its button closes it; Escape closes it once', async () => {
    const count = () => ev(`return SL.test.counts().listeners;`); const n0 = await count();
    for (let i = 0; i < 15; i++) { await ev(`document.getElementById('sOpt').click()`); await ev(`document.getElementById('sOpt').click()`); }
    await p.sleep(200); const n1 = await count();
    const b = await ev(`const r = document.getElementById('sOpt').getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2];`);
    await p.click(b[0], b[1]); await p.sleep(150); const opened = await ev(`return !!document.querySelector('.sessmenu');`);
    await p.click(b[0], b[1]); await p.sleep(150); const closed = await ev(`return !document.querySelector('.sessmenu');`);
    await p.click(b[0], b[1]); await p.sleep(150); await p.key('Escape'); await p.sleep(150);
    await ev(`SL.ui.dialogs.rename(SL.sessions.active)`); await p.sleep(200); await p.key('Escape'); await p.sleep(200);
    const esc = await ev(`return { menu: !!document.querySelector('.sessmenu'), modal: SL.ui.hasModal() };`);
    must(n1 <= n0, 'listeners ' + n0 + ' -> ' + n1); must(opened && closed, JSON.stringify({ opened, closed })); must(!esc.menu && !esc.modal, 'Escape after the menu: ' + JSON.stringify(esc));
    return { listeners: [n0, n1] };
  });

  const errs = p.errors.filter(e => !/^log: Failed to load resource/.test(e)); must(!errs.length, 'page errors: ' + errs.join(' | '));
} catch (e) { failed++; console.log('FAIL', e.message); }
finally { await p.close(); await stop(); }
console.log(failed ? failed + ' failed' : 'all passed', '· screenshots in', out);
process.exit(failed ? 1 : 0);
