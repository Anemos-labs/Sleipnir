// ws-browser.test.mjs: the Workspace views (97-ui-workspace.js on 96b-ws-data.js) in a headless Chromium, on the page of the mock with the live
// modules swapped in (wsdev.mjs) and the answers of packserver.mjs: what the screens show from the server's index, files and diffs; time travel;
// reviewed marks; hunk revert and undo; restore preview, apply and undo; apply verified work; the Merge tab; denied, binary and failing files;
// hostile names and contents; and a project of 10,000 files with a diff of 50,000 lines. Serial; the browser and the servers end with the file.
// Skipped when no Chromium is installed or the mock's data pack is not in the tree.
import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { Browser, Page, findChrome } from '../../../../scripts/web-parity.mjs';
import { startPackServer, bigHunks, REPO } from './packserver.mjs';
import { makeHybrid } from './wsdev.mjs';

let chrome = null, skip = false;
try { chrome = findChrome(); } catch { skip = 'no Chromium'; }
if (!skip && !fs.existsSync(path.join(REPO, 'internal/web/uidev/mock/js/data.js'))) skip = 'the mock data pack is not in the tree';
const FONTS = path.join(REPO, 'internal/web/ui/fonts');
const T = { skip };
/** the flows of SL.act (reviewed, revert, restore) need the live page's own actions, cut out of 60-actions.js: a test that needs them skips when they are not there */
const needActs = t => { if (makeHybrid.acts === false) { t.skip('the workspace actions of 60-actions.js could not be cut out: ' + makeHybrid.why); return true; } return false; };
let browser = null, root = null; const servers = [], pages = [];
const sleep = ms => new Promise(r => setTimeout(r, ms));

before(async () => {
  if (skip) return;
  root = fs.mkdtempSync(path.join(process.env.WS_TEST_TMP || os.tmpdir(), 'ws-hybrid-')); makeHybrid(root);
  if (makeHybrid.acts === false) console.log('# the workspace actions of 60-actions.js could not be cut out: ' + makeHybrid.why);
  browser = await Browser.launch(chrome);
});
after(async () => {
  for (const p of pages.splice(0)) await p.close().catch(() => {});
  for (const s of servers.splice(0)) await s.close().catch(() => {});
  if (browser) await browser.close().catch(() => {});
  if (root) fs.rmSync(root, { recursive: true, force: true });
});

async function serve(mode, o) { const s = await startPackServer(Object.assign({ root, mode }, o || {})); servers.push(s); return s; }
async function open(srv, w, h) {
  const p = await Page.open(browser, srv.url + 'index.html', { width: w || 1440, height: h || 900, mobile: (w || 1440) <= 760, fonts: FONTS, mock: true });
  pages.push(p); return p;
}
/** run JS in the page and return its value */
const ev = (p, js) => p.eval(/^\s*(const|let) /.test(js) ? '(() => { ' + js + ' })()' : js);
/** advance virtual time and real time until the predicate holds */
async function until(p, expr, max = 8000) { if (/^[.#\[]/.test(expr)) expr = 'document.querySelector(' + JSON.stringify(expr) + ')'; const t0 = Date.now(); for (;;) { if (await p.eval('!!(' + expr + ')')) return; if (Date.now() - t0 > max) throw new Error('timed out: ' + expr); await p.wait(100); await p.settle(); await sleep(20); } }
async function click(p, css, text, nth) { await p.step({ click: css, text, nth }); await p.wait(100); await p.settle(); }
async function tab(p, name) { await click(p, '[data-nav=' + name + ']'); await until(p, 'document.querySelector(".wsv")'); }
const text = (p, css) => ev(p, `(() => { const e = document.querySelector(${JSON.stringify(css)}); return e ? e.textContent.replace(/\\s+/g, ' ').trim() : null; })()`);
const count = (p, css) => ev(p, `document.querySelectorAll(${JSON.stringify(css)}).length`);
const reqs = (srv, re) => srv.state.reqs.filter(r => re.test(r));
const q = r => Object.fromEntries(new URL('http://x' + r.slice(r.indexOf(' ') + 1)).searchParams);
async function close(p) { await p.close(); pages.splice(pages.indexOf(p), 1); }

test('Files, Changes and Checkpoints are drawn from the server\'s index, files and diffs', T, async () => {
  const srv = await serve('pack'), p = await open(srv);
  await tab(p, 'files'); await until(p, '.ws-list .wf');
  assert.ok((await count(p, '.ws-list .wf')) >= 10, 'the tree');
  assert.equal(await ev(p, 'document.querySelector(".ws-list .wf.sel .wn").textContent'), 'cart.go', 'the first changed file is selected');
  await until(p, '.ws-body .wdiff .ln.add');
  assert.match(await text(p, '.ws-fh .wcounts'), /^\+\d+ −\d+$/); assert.ok((await count(p, '.ws-body .ln.hunk.wh .btn')) >= 1, 'Revert hunk');
  assert.ok((await count(p, '.ws-body .ln.add b.who[data-ag]')) >= 1, 'the attribution gutter');
  const idx = reqs(srv, /ws\/index/); assert.ok(idx.length >= 1);
  const f = reqs(srv, /ws\/file/).map(q).pop(), d = reqs(srv, /ws\/diff/).map(q).pop();
  assert.equal(f.at, 'live'); assert.equal(d.from, 'base'); assert.equal(d.to, 'live'); assert.match(d.path, /cart\.go$/);
  assert.equal(await count(p, '.ws-list .wf .wi.deny'), 2, 'the protected paths show their lock'); assert.ok((await count(p, '.ws-list .wf .wi.lease')) >= 1, 'leases');
  // typing in the filter narrows the tree and keeps the focus in the field
  await ev(p, 'document.querySelector(".wq-in").focus()'); await p.step({ type: 'cart' }); await p.wait(100); await p.settle();
  assert.equal(await ev(p, 'document.activeElement.classList.contains("wq-in") && document.activeElement.value'), 'cart'); assert.ok((await count(p, '.ws-list .wf')) <= 3); await p.step({ type: 'zz' }); await p.wait(100); await p.settle(); assert.match(await text(p, '.ws-list .ws-none'), /No file matches/);
  await ev(p, 'const i = document.querySelector(".wq-in"); i.value = ""; i.dispatchEvent(new Event("input", { bubbles: true }))'); await p.wait(100); await p.settle();
  await tab(p, 'changes'); await until(p, '.ws-list .wg'); assert.ok((await count(p, '.ws-list .wfr')) >= 4);
  assert.match(await text(p, '.ws-list .wg'), /^T\d/); await tab(p, 'checkpoints'); await until(p, '.ws-list .wc');
  assert.ok((await count(p, '.ws-list .wc')) >= 4); assert.ok((await count(p, '.ws-list .wc .cact .btn')) >= 6, 'Diff and Restore on the checkpoints with files'); assert.match(await text(p, '.ws-list .wc.skipped'), /skipped: nothing to put back/);
  assert.equal(await count(p, '.ws-list .wc .newest'), 1);
  await close(p);
});

test('time travel asks for the content at the point it names and says so; going live comes back', T, async () => {
  const srv = await serve('pack'), p = await open(srv);
  await tab(p, 'files'); await until(p, '.ws-body .wdiff .ln');
  const n = await ev(p, '+document.getElementById("wsScrub").max'); assert.ok(n >= 3);
  srv.state.reqs.length = 0;
  await ev(p, 'const s = document.getElementById("wsScrub"); s.value = "1"; s.dispatchEvent(new Event("input", { bubbles: true }))'); await p.wait(200); await p.settle();
  await until(p, 'document.querySelector(".wnr.tt")'); assert.match(await text(p, '.wnr.tt'), /Time travel · the project as it was after c\d\d/);
  const file = reqs(srv, /ws\/file/).map(q), pos = await ev(p, 'SL.ws.info(SL.sessions.active).pos.map(c => c.id)');
  assert.ok(file.some(f => f.at === pos[1]), 'position 1 is the content when the second change set began: ' + JSON.stringify(file) + ' ' + pos);
  assert.equal(await ev(p, 'document.querySelector(".ws-live").hidden'), false);
  await click(p, '[data-golive]'); await until(p, '!document.querySelector(".wnr.tt")'); assert.equal(reqs(srv, /ws\/file/).map(q).filter(f => f.at === 'live').length, 0, 'the live answer is still cached: no new request');
  // the range buttons name points too
  await tab(p, 'checkpoints'); await until(p, '[data-cpdiff]'); await click(p, '[data-cpdiff]', '', 1); await until(p, 'document.querySelector("[data-mode=from][aria-pressed=true]")');
  await until(p, 'document.querySelector(".ws-body .wdiff, .ws-body .ws-none")'); const dd = reqs(srv, /ws\/diff/).map(q).pop(); assert.equal(dd.to, 'live'); assert.ok(dd.from !== 'base' && dd.from !== 'live', 'from the start of that checkpoint: ' + JSON.stringify(dd));
  await close(p);
});

test('a reviewed mark goes to the server at once and comes back from the index', T, async t => {
  if (needActs(t)) return;
  const srv = await serve('pack'), p = await open(srv);
  await tab(p, 'changes'); await until(p, '.ws-list .wrvb');
  const before = await text(p, '.ws-sb .wmeta span:last-child'); assert.match(before, /reviewed 0\/\d+/);
  await click(p, '.ws-list .wrvb'); await until(p, '.ws-list .wrvb.on');
  const put = reqs(srv, /^PUT .*ws\/reviewed/); assert.equal(put.length, 1);
  assert.match(await text(p, '.ws-sb .wmeta span:last-child'), /reviewed 1\/\d+/);
  assert.equal(Object.keys([...srv.state.insts.values()].pop().reviewed).length, 1, 'the server holds it');
  await p.wait(1500); await until(p, 'SL.ws.info(SL.sessions.active).reviewed && Object.keys(SL.ws.info(SL.sessions.active).reviewed).length === 1'); assert.ok((await count(p, '.ws-list .wrvb.on')) === 1, 'still marked after the index came back');
  await click(p, '.ws-list .wrvb.on'); await until(p, '!document.querySelector(".ws-list .wrvb.on")');
  await close(p);
});

test('hunk revert: preview, a confirmation of its scope, the hunk becomes a "reverted by you" row, undo brings it back', T, async t => {
  if (needActs(t)) return;
  const srv = await serve('pack'), p = await open(srv);
  await tab(p, 'changes'); await until(p, '.ws-body .ln.hunk.wh [data-rev]');
  const key = await ev(p, 'document.querySelector(".ws-body [data-rev]").dataset.rev'), hunks0 = await count(p, '.ws-body .ln.hunk.wh');
  await click(p, '.ws-body [data-rev]'); await until(p, '.sheet');
  assert.match(await text(p, '.sheet .sh-h h2'), /Revert this hunk/); assert.ok((await count(p, '.sheet .diff .ln')) >= 1);
  await click(p, '.sheet [data-no]'); await p.wait(100); assert.equal(reqs(srv, /^POST .*ws\/revert/).length, 0, 'cancel writes nothing');
  await click(p, '.ws-body [data-rev]'); await until(p, '.sheet'); await click(p, '.sheet [data-ok]');
  await until(p, 'document.querySelector(".ws-body [data-unrev]")');
  const post = reqs(srv, /^POST \S*ws\/revert(\?|$)/); assert.equal(post.length, 1);
  const path0 = await ev(p, 'SL.sessions.active.ui.ws.file'), cid = srv.state.confirms;
  assert.ok(cid >= 1, 'a confirmation was asked for');
  assert.match(await text(p, '.ws-body .rvd'), /reverted by you/); assert.doesNotMatch(await text(p, '.ws-body .rvd'), /mock/); assert.equal(await count(p, '.ws-body .ln.hunk.wh'), hunks0, 'the row stands where the hunk was');
  assert.ok((await count(p, '.ws-list .wf .tag.warm')) >= 1, 'the file row says a hunk of it was reverted');
  await click(p, '.ws-body [data-unrev]'); await until(p, '!document.querySelector(".ws-body [data-unrev]") && document.querySelector(".ws-body [data-rev]")');
  assert.equal(reqs(srv, /^POST \S*revert\/v_[a-z0-9]+\/undo(\?|$)/).length, 1); assert.ok(key && path0);
  await close(p);
  // a file that changed under the person: the server's sentence, the page looks again
  const bad = await serve('pack', { revertFails: true }), p2 = await open(bad);
  await tab(p2, 'changes'); await until(p2, '.ws-body [data-rev]'); await click(p2, '.ws-body [data-rev]'); await until(p2, '.sheet'); await click(p2, '.sheet [data-ok]');
  await until(p2, '.toast'); assert.match(await text(p2, '.toast'), /the file changed since the diff was drawn/); assert.ok((await count(p2, '.ws-body [data-rev]')) >= 1); await close(p2);
});

test('restore: a dry run lists what would happen, cancel writes nothing, apply asks for its confirmation and shows the banner, undo takes it away', T, async t => {
  if (needActs(t)) return;
  const srv = await serve('pack'), p = await open(srv);
  await tab(p, 'checkpoints'); await until(p, '.ws-list [data-restore]');
  const id = await ev(p, 'document.querySelectorAll(".ws-list [data-restore]")[1].dataset.restore');
  await click(p, '.ws-list [data-restore]', '', 1); await until(p, '.sheet');
  const dry = reqs(srv, /^POST \S*ws\/restore(\?|$)/); assert.equal(dry.length, 1, 'the preview is one dry run');
  assert.match(await text(p, '.sheet .sh-h h2'), new RegExp('Restore ' + id)); assert.match(await text(p, '.sheet .stubnote'), /The agents are told to read them again\. A safety checkpoint is taken first/);
  assert.ok((await count(p, '.sheet table tbody tr')) >= 1); assert.match(await text(p, '.sheet table'), /put back|removed/); assert.doesNotMatch(await text(p, '.sheet'), /mock/);
  await click(p, '.sheet [data-no]'); await p.wait(100); assert.equal(reqs(srv, /^POST \S*ws\/restore(\?|$)/).length, 1, 'cancel: nothing more');
  await click(p, '.ws-list [data-restore]', '', 1); await until(p, '.sheet'); await click(p, '.sheet [data-ok]');
  await until(p, 'document.querySelector(".wnr.rs")');
  assert.equal(reqs(srv, /^POST \S*ws\/restore(\?|$)/).length, 3, 'preview twice, one apply'); assert.match(await text(p, '.wnr.rs'), /Restored · \d+ files? put back to before c\d\d; a safety checkpoint c\d\ds was taken first\./);
  assert.match(await text(p, '.toast'), new RegExp('files put back to before ' + id + '$'));
  await click(p, '[data-undorestore]'); await until(p, '!document.querySelector(".wnr.rs")'); assert.equal(reqs(srv, /^POST \S*restore\/undo(\?|$)/).length, 1);
  await close(p);
  // a file that cannot be put back: the preview says so and the confirm is off
  const c = await serve('pack', { restoreConflict: true }), p2 = await open(c);
  await tab(p2, 'checkpoints'); await until(p2, '.ws-list [data-restore]'); await click(p2, '.ws-list [data-restore]', '', 1); await until(p2, '.sheet');
  assert.equal(await ev(p2, 'document.querySelector(".sheet [data-ok]").disabled'), true); assert.match(await text(p2, '.sheet'), /1 file cannot be put back/); assert.match(await text(p2, '.sheet table'), /edited since/); await close(p2);
});

test('Merge: worktrees, the queue in order and the verify output; not isolated: the one sentence', T, async () => {
  const srv = await serve('pack'), p = await open(srv);
  await tab(p, 'files'); await click(p, '.ws-tabs [data-tab=merge]'); await until(p, '.ws-list .wc');
  assert.equal(await count(p, '.ws-list .wc'), 4); assert.match(await text(p, '.ws-list .wc'), /be-1.*sleipnir\/.*\/be-1.*merged|be-1.*clean/); assert.match(await text(p, '.ws-list .wc:nth-child(2)'), /dirty/); assert.equal(await count(p, '.ws-list .wc [data-copy]'), 4);
  await until(p, '.ws-body table tbody tr'); assert.match(await text(p, '.ws-fh'), /Merge queue.*healthy/); const rows = await ev(p, 'Array.from(document.querySelectorAll(".ws-body tbody tr")).map(r => r.cells[0].textContent + ":" + r.cells[1].textContent)');
  assert.deepEqual(rows, ['T4:verifying', 'T5:waiting', 'T3:merged', 'T2:merged'], 'head first, then waiting, then what landed');
  await click(p, '[data-mv=verify]'); await until(p, '.ws-body .term'); assert.ok((await count(p, '.ws-body .wtks .wtk')) >= 6);
  await click(p, '.ws-body [data-vtask=T4]'); await until(p, '.ws-body table tbody tr:nth-child(2)'); assert.equal(await count(p, '.ws-body table tbody tr'), 2, 'one row per gate attempt'); assert.match(await text(p, '.ws-body table'), /failed.*ok/);
  assert.match(await text(p, '.ws-body .term'), /go test .*FAIL|^\$ go test/); await click(p, '.ws-body [data-attempt="0"]'); await until(p, '.ws-body .term .tl.err'); assert.match(await text(p, '.ws-body .term'), /got 13 items, want 12/);
  // alt-click on a strip chip opens its verify output
  await click(p, '.ws-tabs [data-tab=files]'); await ev(p, 'document.querySelector("[data-taskf=T2]").dispatchEvent(new MouseEvent("click", { bubbles: true, altKey: true }))'); await until(p, '.ws-tabs [data-tab=merge][aria-selected=true]');
  assert.equal(await ev(p, 'document.querySelector("[data-vtask=T2]").getAttribute("aria-pressed")'), 'true'); await close(p);
  // the single agent of the mock: no worktrees
  const p2 = await open(srv); await ev(p2, 'SL.act.switchSession("orders-api")'); await p2.wait(300); await tab(p2, 'files'); await click(p2, '.ws-tabs [data-tab=merge]');
  await until(p2, '.ws-body .ws-none'); assert.equal(await text(p2, '.ws-body .ws-none'), 'no worktree isolation in this run: work is written to the checkout directly'); assert.equal(await count(p2, '[data-accept]'), 0); await close(p2);
});

test('Apply verified work: disabled until something landed, a dry run, the choice of commits or edits, its confirmation, the answer', T, async () => {
  const srv = await serve('pack'), p = await open(srv);
  await tab(p, 'files'); await until(p, '[data-accept]'); await until(p, '[data-accept]:not([disabled])');
  assert.equal(await text(p, '[data-accept]'), 'Apply verified work…');
  await click(p, '[data-accept]'); await until(p, '.sheet [data-am]');
  const dry = reqs(srv, /^POST \S*ws\/accept(\?|$)/); assert.equal(dry.length, 1); assert.equal(srv.state.confirms, 0, 'the dry run needs no confirmation');
  assert.match(await text(p, '.sheet .sh-h h2'), /Apply the verified work/); assert.equal(await count(p, '.sheet .diff .ln'), 3, 'the files the queue landed');
  assert.equal(await ev(p, 'document.querySelector("[data-am=commits]").getAttribute("aria-pressed")'), 'true'); assert.equal(await ev(p, 'document.querySelector("[data-mf]").hidden'), false);
  await click(p, '.sheet [data-am=edits]'); assert.equal(await ev(p, 'document.querySelector("[data-mf]").hidden'), true, 'no commit message for edits');
  await click(p, '.sheet [data-am=commits]'); await ev(p, 'const i = document.querySelector("[data-msg]"); i.value = "ship it"; i.dispatchEvent(new Event("input", { bubbles: true }))');
  await click(p, '.sheet [data-ok]'); await until(p, '.toast'); const done = srv.state.accepted; assert.equal(done.length, 1); assert.deepEqual(done[0], { mode: 'commits', message: 'ship it' });
  assert.match(await text(p, '.toast'), /applied: 3 files committed as deadbeef on main/); assert.equal(srv.state.confirms, 1);
  await until(p, '[data-accept][disabled]'); assert.equal(await ev(p, 'document.querySelector("[data-accept]").title'), 'nothing verified is waiting', 'what landed is applied: nothing waits');
  await close(p);
  const dirty = await serve('pack', { dirty: true }), p2 = await open(dirty); await tab(p2, 'files'); await until(p2, '[data-accept]:not([disabled])'); await click(p2, '[data-accept]'); await until(p2, '.sheet [data-ok]'); await click(p2, '.sheet [data-ok]');
  await until(p2, '.sheet .wsres .err'); assert.match(await text(p2, '.sheet .wsres'), /uncommitted changes.*git stash/); assert.equal(await count(p2, '.sheet [data-hint]'), 1, 'Copy the command'); assert.equal(await ev(p2, 'document.querySelector("[data-ok]").disabled'), false, 'the person can choose again'); await close(p2);
});

test('a denied path shows its card; a file that is not there, or cannot be read, says so', T, async () => {
  const srv = await serve('pack', { gone: ['api/json.go'], broken: ['api/orders.go'] }), p = await open(srv);
  await tab(p, 'files'); await until(p, '.ws-list .wf');
  await click(p, '.ws-list .wf.prot'); await until(p, '.ws-lock'); assert.match(await text(p, '.ws-lock'), /Denied path.*Agents cannot read/); assert.equal(await count(p, '.ws-body .ln'), 0, 'nothing of it is fetched or shown');
  assert.equal(reqs(srv, /ws\/(file|diff)\?path=\.env/).length, 0);
  await click(p, '.ws-list .wf', 'json.go'); await until(p, '.ws-body .ws-none'); assert.match(await text(p, '.ws-body .ws-none'), /does not exist at this point in time/);
  await click(p, '.ws-list .wf', 'orders.go'); await until(p, '.ws-body [data-retry]'); assert.match(await text(p, '.ws-body .ws-none'), /the file could not be read Try again/);
  const n0 = reqs(srv, /ws\/file\?path=api%2Forders\.go/).length; await click(p, ".ws-body [data-retry]"); await until(p, 'document.querySelector(".ws-body .ws-none")'); await p.wait(500); await p.settle(); await sleep(200);
  assert.ok(reqs(srv, /ws\/file\?path=api%2Forders\.go/).length > n0, 'Try again asks again');
  await close(p);
});

test('hostile names and contents are text: nothing runs, nothing leaves its box, odd characters are visible', T, async () => {
  const srv = await serve('hostile', { big: { files: 40, lines: 2000 } }), p = await open(srv);
  await ev(p, 'window.__pwned = 0; window.alert = () => { window.__pwned++; }');
  await tab(p, 'files'); await until(p, '.ws-list .wf'); 
  const names = await ev(p, 'Array.from(document.querySelectorAll(".ws-list .wf .wn")).map(e => e.textContent)'); const all = names.join('\n');
  assert.ok(names.some(n => n.includes('<script>alert(1)<b>.go')), 'the name is shown as it is, as text'); assert.ok(names.some(n => n.includes('rtl\u27e8U+202E\u27e9gnp.exe') || n.includes('rtl⟨U+202E⟩gnp.exe')), 'the override is a visible code point: ' + all.slice(0, 300));
  assert.ok(names.some(n => n.includes('zero⟨U+200B⟩width.go')));
  assert.equal(await count(p, '.ws-list script, .ws-list img, .ws-body script, .ws-body img, .ws-fh img, .ws-fh script'), 0);
  let visited = 0;
  for (const sel of ['.ws-list .wf[data-file*="script"]', '.ws-list .wf[data-file*="rtl"]', '.ws-list .wf[data-file*="aaaa"]', '.ws-list .wf[data-file*="zero"]', '.ws-list .wf[data-file*="blob"]', '.ws-list .wf[data-file*="to-outside"]']) {
    if (!(await count(p, sel))) continue; visited++; await ev(p, `document.querySelector(${JSON.stringify(sel)}).click()`); await p.wait(300); await p.settle(); await sleep(150); await p.wait(300); await p.settle();
    const geo = await ev(p, '({ sw: document.documentElement.scrollWidth, iw: window.innerWidth, list: document.querySelector(".ws-list").getBoundingClientRect().right, row: document.querySelector(".ws-list .wf.sel") ? document.querySelector(".ws-list .wf.sel").getBoundingClientRect().right : 0, body: document.querySelector(".ws-main").getBoundingClientRect().right, bodyw: document.querySelector(".ws-body").scrollWidth, pw: window.__pwned })');
    if (/blob/.test(sel)) assert.match(await text(p, '.ws-body'), /A binary file \(\d+ bytes\): there is no text to show/); if (/zero/.test(sel)) assert.match(await text(p, '.wpath'), /zero⟨U\+200B⟩width\.go/);
    assert.ok(geo.sw <= geo.iw + 1, sel + ' page scrolls sideways: ' + JSON.stringify(geo)); assert.ok(geo.row <= geo.list + 1, sel + ' row leaves the list'); assert.equal(geo.pw, 0, sel + ' ran script');
  }
  assert.equal(visited, 6, 'every hostile row was opened');
  assert.match(await text(p, '.ws-body .ws-none'), /A symbolic link: this page does not follow it/);
  await ev(p, 'document.querySelector(".ws-list .wf[data-file*=huge]").click()'); await p.wait(300); await until(p, '.ws-body .ln', 8000);
  assert.ok((await count(p, '.ws-body .ln')) <= 120, 'a long diff draws a window'); const html = await ev(p, 'document.querySelector(".ws-body").innerHTML'); assert.ok(!/<script|<img/i.test(html), 'the content is escaped'); assert.match(html, /⟨U\+202E⟩|characters not shown|&lt;script&gt;/);
  assert.equal(await ev(p, 'window.__pwned'), 0); const pr = await p.pageProblems(); assert.deepEqual(pr.filter(x => /csp/.test(x)), [], 'no policy violation');
  await close(p);
});

test('10,000 files and a 50,000-line diff stay responsive: a window of rows, not the whole', T, async () => {
  const srv = await serve('big', { big: { files: 10000, lines: 50000 } }), p = await open(srv);
  const t0 = Date.now(); await tab(p, 'files'); await until(p, '.ws-list .wf', 15000); const tTree = Date.now() - t0;
  const nodes = await count(p, '.ws-list *'); assert.ok(nodes < 1500, 'the tree draws a window: ' + nodes + ' nodes'); const total = await ev(p, 'SL.ws.info(SL.sessions.active).tree.length'); assert.equal(total, 10001 + 0);
  // scrolling the whole tree: every position draws and the rows of the position are there
  const times = []; for (const f of [0.1, 0.5, 0.9, 1]) { const t = Date.now(); const first = await ev(p, `(() => { const l = document.querySelector(".ws-list"); l.scrollTop = (l.scrollHeight - l.clientHeight) * ${f}; return l.scrollTop; })()`); await p.wait(50); await p.settle(); times.push(Date.now() - t);
    const vis = await ev(p, '(() => { const l = document.querySelector(".ws-list").getBoundingClientRect(); return Array.from(document.querySelectorAll(".ws-list [data-vi]")).filter(e => { const r = e.getBoundingClientRect(); return r.bottom > l.top && r.top < l.bottom; }).length; })()'); assert.ok(vis >= 5, 'rows in view at ' + f + ': ' + vis + ' (scrollTop ' + first + ')'); }
  assert.ok((await count(p, '.ws-list *')) < 1500);
  // the filter works over everything
  await ev(p, 'const i = document.querySelector(".wq-in"); i.value = "file09999"; i.dispatchEvent(new Event("input", { bubbles: true }))'); await p.wait(100); await p.settle(); assert.equal(await count(p, '.ws-list .wf'), 1);
  await ev(p, 'const i = document.querySelector(".wq-in"); i.value = ""; i.dispatchEvent(new Event("input", { bubbles: true }))'); await p.wait(100); await p.settle();
  // keyboard: the arrow key moves through rows that are not drawn yet
  await ev(p, 'document.querySelector(".ws-list [data-vi]").focus()'); const before = await ev(p, 'document.activeElement.dataset.vi'); await p.key('ArrowDown'); await p.key('ArrowDown'); await p.wait(50); await p.settle(); assert.equal(+(await ev(p, 'document.activeElement.dataset.vi')), +before + 2);
  // a 50,000-line diff
  const td = Date.now(); await ev(p, 'SL.ws.dataOf(SL.sessions.active); SL.sessions.active.ui.ws.q = ""; SL.sessions.active.ui.ws.file = "big/huge.go"; SL.sessions.active.touch()'); await p.wait(200); await until(p, '.ws-body .ln.add', 15000); const tDiff = Date.now() - td;
  const lines = await count(p, '.ws-body .ln'); assert.ok(lines > 20 && lines < 200, 'rows drawn: ' + lines); const hunks = await text(p, '.ws-fh .wcounts'); assert.equal(hunks, '+' + bigHunks(50000) * 4 + ' −' + bigHunks(50000) * 4);
  const h = await ev(p, 'document.querySelector(".ws-body").scrollHeight'); assert.ok(h > 50000 * 17, 'the scroll height is that of all the rows: ' + h);
  for (const f of [0.3, 0.7, 1]) { await ev(p, `(() => { const b = document.querySelector(".ws-body"); b.scrollTop = (b.scrollHeight - b.clientHeight) * ${f}; })()`); await p.wait(50); await p.settle(); assert.ok((await count(p, '.ws-body .ln')) < 200); assert.ok((await count(p, '.ws-body .ln.hunk.wh')) >= 1, 'a hunk head is drawn at ' + f); }
  // the whole file: 50,000 lines too
  await click(p, '[data-vw=file]'); await until(p, '.ws-body .diff.whole .ln'); assert.ok((await count(p, '.ws-body .ln')) < 200); assert.equal(await ev(p, 'document.querySelector(".ws-body").scrollHeight > 50000 * 17'), true);
  console.log('# scale: tree first paint ' + tTree + ' ms, scroll to a position ' + times.join('/') + ' ms (incl. virtual frames), diff first paint ' + tDiff + ' ms, ' + nodes + ' tree nodes, ' + lines + ' diff rows');
  await close(p);
});

test('the Changes list of 600 files is grouped, windowed, and keeps the head of its group in view', T, async () => {
  const srv = await serve('big', { big: { files: 3000, lines: 400, changedEvery: 5 } }), p = await open(srv);
  await tab(p, 'changes'); await until(p, '.ws-list .wfr', 15000);
  assert.ok((await count(p, '.ws-list *')) < 700, 'a window of the list'); assert.match(await text(p, '.ws-lr'), /6\d\d files/);
  await ev(p, 'const l = document.querySelector(".ws-list"); l.scrollTop = l.scrollHeight * 0.5'); await p.wait(60); await p.settle();
  assert.ok((await count(p, '.ws-list .wfr')) >= 5 && (await count(p, '.ws-list .wfr')) < 150); assert.equal(await ev(p, 'document.querySelector(".ws-list [data-vi]").classList.contains("wg")'), true, 'the head of the group of the first row in view is drawn first, and sticks');
  await ev(p, 'const l = document.querySelector(".ws-list"); l.scrollTop = l.scrollHeight'); await p.wait(60); await p.settle(); assert.ok((await count(p, '.ws-list .wfr')) >= 5);
  await ev(p, 'const c = document.querySelector("[data-grp=agent]"); c.click()'); await p.wait(100); await p.settle(); await until(p, '.ws-list .wg[data-ag]'); assert.ok((await count(p, '.ws-list *')) < 700);
  await close(p);
});

test('many checkpoints: the newest first, older ones on request, the scrubber labels thinned', T, async () => {
  const srv = await serve('big', { big: { files: 30, lines: 100, extraCps: 400 } }), p = await open(srv);
  await tab(p, 'checkpoints'); await until(p, '.ws-list .wc', 15000);
  assert.equal(await count(p, '.ws-list .wc'), 150); assert.match(await text(p, '.ws-list [data-cpmore]'), /Show older checkpoints \(\d+\)/); assert.equal(await text(p, '.ws-list .wc .cid'), 'c402', 'newest first');
  const labels = await count(p, '.ws-ticks .tk'); assert.ok(labels > 10 && labels < 40, 'tick labels: ' + labels); assert.equal(await ev(p, '+document.getElementById("wsScrub").max'), 402);
  await click(p, '[data-cpmore]'); await until(p, '(document.querySelectorAll(".ws-list .wc").length === 300)'); await click(p, '[data-cpmore]'); await until(p, '(document.querySelectorAll(".ws-list .wc").length === 402)'); assert.equal(await count(p, '.ws-list [data-cpmore]'), 0);
  await close(p);
});

test('while the history loads, and when it cannot be read: the loading text, the server\'s sentence, Try again', T, async () => {
  const srv = await serve('pack', { indexFails: true, delay: 0 }), p = await open(srv);
  await tab(p, 'files'); await until(p, '.ws-body .ws-none [data-retry]');
  assert.match(await text(p, '.ws-body .ws-none'), /the checkpoints could not be read Try again/); assert.match(await text(p, '.ws-at'), /history not readable/); assert.equal(await ev(p, 'document.getElementById("wsScrub").disabled'), true);
  assert.match(await text(p, '.toast'), /the checkpoints could not be read/);
  srv.opts.indexFails = false; await click(p, '.ws-body [data-retry]'); await until(p, '.ws-list .wf'); assert.ok((await count(p, '.ws-list .wf')) >= 10); assert.equal(await ev(p, 'document.getElementById("wsScrub").disabled'), false);
  await close(p);
});

test('a file a worker is writing is listed as "writing" and typed in the diff from the stream, then gives way to the record', T, async () => {
  const srv = await serve('pack'), p = await open(srv);
  await tab(p, 'changes'); await until(p, '.ws-list .wfr');
  await ev(p, 'const S = SL.sessions.active; S.m.streams["ts-1"] = { t0: S.vt, text: "package catalog\\n\\nfunc New() int {\\n\\treturn 1\\n}\\n", rate: 12, code: true, file: "api/catalog/new_test.go" }; S.m.ver++; S.touch()');
  await until(p, '.ws-list .wf[data-file="api/catalog/new_test.go"] .warm'); assert.match(await text(p, '.ws-list .wf[data-file="api/catalog/new_test.go"] .wr'), /✎ writing/); assert.equal(await count(p, '.ws-list .wf[data-file="api/catalog/new_test.go"] ~ [data-review], .ws-list .wfr:has([data-file="api/catalog/new_test.go"]) [data-review]'), 0, 'nothing to review while it is being written');
  await click(p, '.ws-list .wf[data-file="api/catalog/new_test.go"]'); await until(p, '.ws-body .ln.add'); const n1 = await count(p, '.ws-body .ln.add'); assert.equal(await count(p, '.ws-body [data-rev]'), 0, 'no revert of what is not there yet');
  await p.wait(1500); await p.settle(); const n2 = await count(p, '.ws-body .ln.add'); assert.ok(n2 > n1, 'the text grows: ' + n1 + ' then ' + n2); assert.equal(await count(p, '.ws-body .ln.caretline'), 1, 'the caret is on the last line');
  assert.match(await text(p, '.ws-fh'), /new_test\.go.*A/); await p.wait(6000); await p.settle(); await sleep(200);
  assert.equal(await count(p, '.ws-list .wf[data-file="api/catalog/new_test.go"]'), 0, 'the record takes over (nothing is recorded in this fixture, so the row goes)');
  await close(p);
});
