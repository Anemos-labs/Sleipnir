// ws-live.browser.mjs: the Workspace of the real page (internal/web/ui, with every module of the live data layer) against A2's fake server
// (internal/web/webtest/cmd/fakeserver, built here with the Go toolchain). Needs Go and a Chromium; not part of the `*.test.mjs` set that CI runs:
//   node --test internal/web/uidev/test/ws-live.browser.mjs
import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { Browser, findChrome } from '../../../../scripts/web-parity.mjs';
import { openPlain } from './plainpage.mjs';
import { REPO } from './packserver.mjs';

let skip = false, chrome = null;
try { chrome = findChrome(); } catch { skip = 'no Chromium'; }
if (!skip && spawnSync('go', ['version']).status !== 0) skip = 'no Go toolchain';
let dir = null, server = null, browser = null, url = '';
before(async () => {
  if (skip) return;
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ws-live-'));
  const b = spawnSync('go', ['build', '-o', path.join(dir, 'fakeserver'), './internal/web/webtest/cmd/fakeserver'], { cwd: REPO, encoding: 'utf8' });
  if (b.status !== 0) throw new Error('go build failed: ' + b.stderr);
  server = spawn(path.join(dir, 'fakeserver'), ['-addr', '127.0.0.1:0', '-ui', path.join(REPO, 'internal/web/ui'), '-speed', '0'], { stdio: ['ignore', 'pipe', 'inherit'], cwd: REPO });
  url = await new Promise(r => { let s = ''; server.stdout.on('data', d => { s += d; const m = /(http:\S+)/.exec(s); if (m) r(m[1]); }); });
  browser = await Browser.launch(chrome);
});
after(async () => { if (browser) await browser.close(); if (server) server.kill(); if (dir) fs.rmSync(dir, { recursive: true, force: true }); });

test('the Workspace of the live page shows the fake server\'s index, file and diff; its tabs and the strip answer', { skip }, async () => {
  const p = await openPlain(browser, url, { width: 1440, height: 900 });
  await p.waitFor('window.SL && SL.sessions && SL.sessions.active && SL.sessions.active.id === "shop"', 20000);
  await p.eval('document.querySelector("[data-nav=files]").click()'); await p.waitFor('document.querySelectorAll(".ws-list .wf").length >= 5');
  await p.waitFor('document.querySelector(".ws-body .ln.add")');
  assert.equal(await p.eval('document.querySelector(".ws-list .wf.sel .wn").textContent'), 'items.go');
  assert.match(await p.eval('document.querySelector(".ws-fh .wcounts").textContent'), /^\+\d+ −\d+$/); assert.ok(await p.eval('!!document.querySelector(".ws-body [data-rev]")'));
  assert.deepEqual(await p.eval('Array.from(document.querySelectorAll(".ws-ticks .tk")).map(e => e.textContent)'), ['base', 'c01', 'c02']);
  for (const t of ['changes', 'checkpoints', 'merge']) { await p.eval(t === 'merge' ? 'document.querySelector(".ws-tabs [data-tab=merge]").click()' : 'document.querySelector("[data-nav=' + t + ']").click()'); await p.waitFor('document.querySelector(".wsv[data-tab=' + t + ']")'); }
  await p.waitFor('document.querySelector(".ws-body .ws-none, .ws-body table")'); assert.deepEqual(p.problems.filter(x => !/Failed to load resource.*(409|404)/.test(x)), []);
  await p.close();
});
