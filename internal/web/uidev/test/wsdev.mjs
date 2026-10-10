// wsdev.mjs: builds the page of the mock (internal/web/uidev/mock) with the live Workspace modules (96b-ws-data.js, 97-ui-workspace.js) and
// api.js swapped in, and serves it with the workspace answers of packserver.mjs.
//
//   node internal/web/uidev/test/wsdev.mjs [--mode pack|big|hostile] [--port 0] [--dir DIR]
//
// The mock's own simulation drives the sessions; the Workspace takes its history from the server, so the screens can be compared with the
// mock (scripts/web-parity.mjs --b http://127.0.0.1:PORT/index.html) and driven at scale.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { REPO, startPackServer } from './packserver.mjs';

/**
 * The Workspace actions of the live page (markReviewed, revertHunk, unrevertHunk, rewind, undoRewind), cut verbatim out of 60-actions.js with the few
 * helpers they use, installed over the mock's own: the hybrid page then runs the same SL.act code the live page does, against the pack server.
 */
export function wsActs(file) {
  const src = fs.readFileSync(file, 'utf8'), lines = src.split('\n');
  const pick = re => { const l = lines.find(x => re.test(x)); if (!l) throw new Error('60-actions.js: no line matches ' + re); return l; };
  const helpers = [/^  const act = /, /^  const ses = /, /^  const toast = /, /^  const fail = /, /^  const send = /, /^  const tab = /, /^  const why = /, /^  const refuse = /, /^  const api = /].map(pick).join('\n');
  const a = src.indexOf('  const WS = S =>'), b = src.indexOf("  /** Expand the composer's paste chips");
  if (a < 0 || b < 0) throw new Error('60-actions.js: the workspace section moved');
  return "(function (SL) {\n  'use strict';\n  const ACT = SL.act;\n" + helpers + '\n' + src.slice(a, b) + '})(SL);\n';
}
/** Copy the mock page into `dir` and lay the live Workspace modules over it. Returns dir. */
export function makeHybrid(dir) {
  const src = path.join(REPO, 'internal/web/uidev/mock'), ui = path.join(REPO, 'internal/web/ui');
  fs.rmSync(dir, { recursive: true, force: true }); fs.mkdirSync(dir, { recursive: true });
  fs.cpSync(src, dir, { recursive: true });
  const html = fs.readFileSync(path.join(dir, 'index-mock.html'), 'utf8').replaceAll('../../ui/', '');
  fs.writeFileSync(path.join(dir, 'index.html'), html); fs.rmSync(path.join(dir, 'index-mock.html'));
  for (const f of ['96b-ws-data.js', '97-ui-workspace.js', 'api.js']) fs.copyFileSync(path.join(ui, 'js', f), path.join(dir, 'js', f));
  try {
    fs.writeFileSync(path.join(dir, 'js', 'wsacts.js'), wsActs(path.join(ui, 'js', '60-actions.js')));
    fs.writeFileSync(path.join(dir, 'index.html'), fs.readFileSync(path.join(dir, 'index.html'), 'utf8').replace('<script src="js/99-app.js"></script>', '<script src="js/wsacts.js"></script>\n<script src="js/99-app.js"></script>'));
    makeHybrid.acts = true;
  } catch (e) { makeHybrid.acts = false; makeHybrid.why = e.message; }
  for (const d of ['css', 'fonts']) if (!fs.existsSync(path.join(dir, d))) fs.cpSync(path.join(ui, d), path.join(dir, d), { recursive: true });
  return dir;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const a = process.argv.slice(2), o = { mode: 'pack', port: 0, dir: path.join(os.tmpdir(), 'ws-hybrid') };
  for (let i = 0; i < a.length; i++) { if (a[i] === '--mode') o.mode = a[++i]; else if (a[i] === '--port') o.port = +a[++i]; else if (a[i] === '--dir') o.dir = a[++i]; }
  makeHybrid(o.dir);
  const s = await startPackServer({ root: o.dir, mode: o.mode, port: o.port });
  console.log(s.url + 'index.html');
}
