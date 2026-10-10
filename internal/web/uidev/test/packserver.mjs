// packserver.mjs: a development server for the Workspace views (96b-ws-data.js, 97-ui-workspace.js) with the answers of the workspace API
// (CONTRACT.md 12) computed from the mock's own data pack. Three jobs:
//
//   1. Parity. The page of the mock (internal/web/uidev/mock) with the live Workspace modules swapped in is served here, and the three
//      workspace routes answer with what the mock's own data layer computes for the same checkpoints (the page tells the server which ones
//      its model holds), so the live Workspace is drawn from the mock's content and must look like the mock.
//   2. Scale. `big` answers with a project of thousands of files and a diff of tens of thousands of lines, and `hostile` adds paths and
//      contents that try to break the layout or to pass as markup.
//   3. Flows. Reviewed marks, hunk reverts, restores, the merge queue, the worktrees and the verify output keep a little state, so that the
//      screens can be driven end to end.
//
// It serves files with the content security policy of `sleipnir web` and answers nothing else of the API. Nothing here ships.
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
export const REPO = path.resolve(here, '..', '..', '..', '..');
const CSP = ["default-src 'none'", "script-src 'self'", "style-src 'self'", "style-src-attr 'unsafe-inline'", "font-src 'self'", "img-src 'self' data:", "connect-src 'self'", "frame-ancestors 'none'", "form-action 'none'", "base-uri 'none'"].join('; ');
const TYPES = { '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.json': 'application/json; charset=utf-8', '.woff2': 'font/woff2', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon' };

/** The data pack of the mock and its data layer (the oracle), in a vm of their own. */
export function loadOracle(mockDir = path.join(REPO, 'internal/web/uidev/mock/js')) {
  const sb = { console }; sb.window = sb; vm.createContext(sb);
  vm.runInContext(fs.readFileSync(path.join(mockDir, 'data.js'), 'utf8'), sb, { filename: 'data.js' });
  const pack = sb.SLDATA;
  sb.SL = { D: { extra: { files: pack.files, diffText: pack.diffText } }, u: {} };
  vm.runInContext(fs.readFileSync(path.join(mockDir, '96b-ws-data.js'), 'utf8'), sb, { filename: '96b-ws-data.mock.js' });
  return { pack, ws: sb.SL.ws };
}

const CP = n => String.fromCodePoint(n);
const WORD = { A: 'added', M: 'modified', D: 'deleted', '-': 'unchanged' };
const word = s => WORD[s] || 'unchanged';
const d16 = s => createHash('sha256').update(s).digest('hex').slice(0, 16);
/** The confirmation scope of exactly this: the page confirms what the server issued (as the real server does) and sends it back. */
const scopeOf = (kind, ...parts) => kind + ':' + d16(parts.join('\u0001'));

/** The server's index of the pack's session `kind` for the checkpoints the page's model holds (oldest first: {id, step, ts, note, files, skipped, safety}). */
export function packIndex(oracle, kind, list, st) {
  const { ws } = oracle, S = { id: kind, kind, runs: 0, m: { ckpts: list.slice().reverse(), tasks: {} } }, I = ws.info(S);
  if (!I) return { root: '/home/me/projects/' + kind, isolation: 'worktree', base: { id: 'base', label: 'before the session', time: '' }, cps: [], tree: [], reviewed: {}, version: 'v0' };
  const cps = I.cps.map(c => {
    const changes = (c.step ? I.raw.diffs[c.step] || [] : []).map(f => ({ path: f.path, status: f.status, added: f.added, removed: f.removed, agents: f.agents, task: f.task }));
    return { id: c.id, time: c.time, at: 0, label: c.label, skipped: !!c.skipped, safety: !!c.safety, files: c.files, agents: c.agents, tasks: c.tasks, added: c.added, removed: c.removed, nfiles: c.nfiles, changes };
  });
  const all = ws.setAt(I, I.pos.length), rows = ws.rows(S, I, all), byPath = new Map(rows.map(r => [r.path, r]));
  const tree = I.raw.tree.filter(f => byPath.has(f.path)).map(f => {
    const r = byPath.get(f.path);
    return { path: f.path, dir: r.dir, name: r.name, kind: f.kind || 'text', status: word(r.status), owner: r.owner || undefined, task: r.task || undefined, cp: r.cp || undefined, lease: f.lease || undefined, protected: f.protected || undefined, ask: f.ask || undefined, add: r.add, del: r.del, size: f.sizeFinal || 0, exists: r.status !== 'D' };
  });
  const body = { root: '/home/me/projects/' + kind, isolation: kind === 'shop' ? 'worktree' : 'none', base: I.base.id === 'start' ? { id: 'base', label: I.base.label, time: I.base.time } : { id: I.base.id, label: I.base.label, time: I.base.time }, cps, tree, reviewed: st.reviewed, reverted: st.reverted.slice(), restore: st.restore || undefined };
  body.version = d16(JSON.stringify(body));
  return body;
}
const kOfPoint = (I, id) => (id === 'base' ? 0 : id === 'live' ? I.pos.length : I.pos.findIndex(c => c.id === id));
/** A file at a point, with its authorship as runs; null when the pack keeps nothing for it. */
export function packFile(oracle, kind, list, p, at) {
  const { ws } = oracle, S = { id: kind, kind, runs: 0, m: { ckpts: list.slice().reverse(), tasks: {} } }, I = ws.info(S); if (!I) return null;
  const k = kOfPoint(I, at); if (k < 0) return null;
  const steps = ws.setAt(I, k), text = ws.textAt(I, p, steps); if (text == null) return null;
  const bl = ws.blame(I, p, steps), runs = [];
  bl.forEach((b, i) => { const last = runs[runs.length - 1]; if (last && last.ag === b.ag && last.id === b.id && last.task === b.task) last.count++; else runs.push({ line: i + 1, count: 1, ag: b.ag, id: b.id || undefined, task: b.task || undefined }); });
  return { path: p, at, exists: true, text, size: text.length, blame: runs, exact: true };
}
export function packDiff(oracle, kind, list, p, from, to) {
  const { ws } = oracle, S = { id: kind, kind, runs: 0, m: { ckpts: list.slice().reverse(), tasks: {} } }, I = ws.info(S); if (!I) return null;
  const a = kOfPoint(I, from), b = kOfPoint(I, to); if (a < 0 || b < 0) return null;
  const sa = ws.setAt(I, a), sb = ws.setAt(I, b), h = ws.hunks(I, p, sa, sb), c = ws.counts(I, p, sa, sb);
  return { path: p, from, to, added: c.added, removed: c.removed, hunks: h.hunks };
}

/** The hunks of the big diff: `lines` is the number of rows the diff has (a head and 13 lines per hunk: 3 of context, 4 deleted, 4 added, 2 of context). */
export const bigHunks = lines => Math.ceil(lines / 14);
/** A project of `files` files and a file whose diff has `lines` rows (for the scale tests). */
export function bigProject({ files = 10000, lines = 50000, hostile = false, changedEvery = 97, extraCps = 0 } = {}) {
  const tree = [], dirs = Math.ceil(files / 100);
  for (let i = 0; i < files; i++) {
    const d = 'pkg' + String(Math.floor(i / 100)).padStart(4, '0') + '/sub' + (i % 5), name = 'file' + String(i).padStart(5, '0') + '.go', changed = i % changedEvery === 0;
    tree.push({ path: d + '/' + name, dir: d, name, kind: 'go', status: changed ? 'modified' : 'unchanged', owner: changed ? 'be-' + (1 + i % 2) : undefined, task: changed ? 'T' + (1 + i % 5) : undefined, cp: changed ? 'c02' : undefined, add: changed ? 3 : 0, del: changed ? 1 : 0, size: 800, exists: true });
  }
  tree.push({ path: 'big/huge.go', dir: 'big', name: 'huge.go', kind: 'go', status: 'modified', owner: 'be-1', task: 'T2', cp: 'c02', add: bigHunks(lines) * 4, del: bigHunks(lines) * 4, size: bigHunks(lines) * 20 * 20, exists: true });
  if (hostile) {
    const bad = ['<script>alert(1)<b>.go', 'dir<svg onload=alert(1)>/x.go', '<img src=x onerror=alert(1)>.txt', 'rtl' + CP(0x202e) + 'gnp.exe', 'zero' + CP(0x200b) + 'width.go', 'a'.repeat(300) + '.go', 'bin/blob.bin', 'link/to-outside', '"quote\'s&amp;.go', 'new\\nline.go'];
    ['constructor/a.go', '__proto__/b.go', 'toString/c.go', 'hasOwnProperty', 'valueOf', '__defineGetter__', 'isPrototypeOf'].forEach(p => bad.push(p));   // names of Object.prototype members: directories, files, tasks, agents
    bad.forEach(p => { const cut = p.lastIndexOf('/'); tree.push({ path: p, dir: cut < 0 ? '' : p.slice(0, cut), name: p.slice(cut + 1), kind: p === 'link/to-outside' ? 'symlink' : p.endsWith('.bin') ? 'other' : 'go', status: 'modified', owner: /proto|constructor/.test(p) ? '__proto__' : 'be-1', task: /proto|constructor|toString/.test(p) ? 'constructor' : 'T2', cp: 'c02', add: 1, del: 1, size: 10, exists: true }); });
  }
  const cps = [
    { id: 'c01', time: '22:15:42', at: 1, label: 'turn 1', skipped: false, files: ['big/huge.go'], agents: ['be-1'], tasks: ['T2'], added: bigHunks(lines) * 20, removed: 0, nfiles: 1, changes: [{ path: 'big/huge.go', status: 'added', added: bigHunks(lines) * 20, removed: 0, agents: ['be-1'], task: 'T2' }] },
    { id: 'c02', time: '22:16:05', at: 2, label: 'turn 2: changes everywhere', skipped: false, files: tree.filter(f => f.status === 'modified').map(f => f.path), agents: ['be-1', 'be-2'], tasks: ['T1', 'T2'], added: 0, removed: 0, nfiles: 0,
      changes: tree.filter(f => f.status === 'modified').map(f => ({ path: f.path, status: 'modified', added: f.add, removed: f.del, agents: [f.owner || 'be-1'], task: f.task || 'T2' })) },
  ];
  cps[1].nfiles = cps[1].files.length;
  for (let i = 0; i < extraCps; i++) cps.push({ id: 'c' + String(3 + i).padStart(2, '0'), time: '22:17:' + String(i % 60).padStart(2, '0'), at: 3 + i, label: 'turn ' + (3 + i), skipped: false, files: ['pkg0000/sub0/file00000.go'], agents: ['be-1'], tasks: ['T1'], added: 1, removed: 0, nfiles: 1, changes: [{ path: 'pkg0000/sub0/file00000.go', status: 'modified', added: 1, removed: 0, agents: ['be-1'], task: 'T1' }] });
  return { tree, cps, lines };
}
const hostileLine = i => ['    return "<script>alert(' + i + ')</script>"', '\tx := "rtl' + CP(0x202e) + 'txt.exe" // ' + CP(0x2066) + 'isolate' + CP(0x2069) + '', 'zero' + CP(0x200b) + 'width' + CP(0x200d) + 'here' + CP(0x2060) + '', '    // START ' + 'long '.repeat(900) + 'THE-END', '\u0007bell and \u0001 control and \u001b[31m escape'][i % 5];
export function bigContent(p, at, { lines = 50000, hostile = false } = {}) {
  if (p === 'big/huge.go') {
    const n = bigHunks(lines) * 20, ls = []; for (let i = 0; i < n; i++) ls.push(hostile && i % 70 === 3 ? hostileLine(i) : i % 20 >= 3 && i % 20 < 7 ? 'func f' + i + '() int { return ' + i + ' } // changed' : 'x' + i + ' := ' + i + ' // line ' + i);
    const text = ls.join('\n') + '\n', runs = [{ line: 1, count: Math.floor(n / 2), ag: 'be-1', id: 'c01', task: 'T2' }, { line: Math.floor(n / 2) + 1, count: n - Math.floor(n / 2), ag: 'be-2', id: 'c02', task: 'T1' }];
    const cut = text.length > 2097152; return { path: p, at, exists: true, text: cut ? text.slice(0, 2097152) : text, size: text.length, blame: runs, exact: true, truncated: cut };
  }
  if (p.endsWith('.bin')) return { path: p, at, exists: true, binary: true, size: 4096, blame: [], exact: true };
  if (p === 'link/to-outside') return null;
  const text = hostile ? [hostileLine(0), hostileLine(1), hostileLine(2), hostileLine(4)].join('\n') + '\n' : 'package x\n\nfunc F() int { return 1 }\n';
  return { path: p, at, exists: true, text, size: text.length, blame: [{ line: 1, count: 4, ag: 'be-1', id: 'c02', task: 'T2' }], exact: true };
}
export function bigDiff(p, from, to, { lines = 50000, hostile = false, wide = 0 } = {}) {
  if (p === 'big/huge.go') {
    const hunks = [], nh = bigHunks(lines); let add = 0, del = 0;
    for (let k = 0; k < nh; k++) {
      const o = k * 20 + 1, ls = [];
      for (let j = 0; j < 3; j++) ls.push({ t: ' ', s: 'x' + (o + j) + ' := ' + (o + j) + ' // line ' + (o + j) });
      const nd = k === 0 && wide ? wide : 4;
      for (let j = 0; j < nd; j++) ls.push({ t: '-', s: hostile && (k * 4 + j) % 70 === 3 ? hostileLine(k + j) : 'x' + (o + 3 + j) + ' := ' + (o + 3 + j) + ' // old' });
      for (let j = 0; j < nd; j++) ls.push({ t: '+', s: hostile && (k * 4 + j) % 70 === 3 ? hostileLine(k + j + 1) : 'func f' + (o + 3 + j) + '() int { return ' + (o + 3 + j) + ' } // changed' });
      for (let j = 0; j < 2; j++) ls.push({ t: ' ', s: 'x' + (o + 7 + j) + ' := ' + (o + 7 + j) + ' // line ' + (o + 7 + j) });
      hunks.push({ oldStart: o, oldLines: 5 + nd, newStart: o, newLines: 5 + nd, section: 'func f' + k, lines: ls }); add += nd; del += nd;
    }
    return { path: p, from, to, added: add, removed: del, hunks };
  }
  if (p.endsWith('.bin')) return { path: p, from, to, added: 0, removed: 0, hunks: [], binary: true };
  return { path: p, from, to, added: 1, removed: 1, hunks: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, section: '', lines: [{ t: '-', s: hostile ? hostileLine(0) : 'package old' }, { t: '+', s: hostile ? hostileLine(1) : 'package x' }] }] };
}

/** The shim the page loads before the Workspace asks anything: it tells the server which checkpoints the model of the session holds. */
const SHIM = `(function () {
  var f = window.fetch.bind(window), INST = Math.random().toString(36).slice(2);
  window.fetch = function (u, init) {
    try {
      var m = /^\\/api\\/sessions\\/([^/]+)\\/ws\\//.exec(String(u));
      if (m) u = String(u) + (String(u).indexOf('?') < 0 ? '?' : '&') + 'inst=' + INST;
      m = /^\\/api\\/sessions\\/([^/]+)\\/ws\\//.exec(String(u));
      if (m && window.SL && window.SL.sessions) {
        var S = window.SL.sessions.get(decodeURIComponent(m[1])), mm = S && (S.m || S.wm);
        if (mm) { var list = mm.ckpts.slice().reverse().map(function (c) { return { id: c.id, step: c.step || '', ts: c.ts, note: c.note, files: c.files, skipped: !!c.skipped, safety: !!c.safety }; });
          u = String(u) + (String(u).indexOf('?') < 0 ? '?' : '&') + 'cps=' + encodeURIComponent(JSON.stringify(list)); }
      }
    } catch (e) { /* the page decides */ }
    return f(u, init);
  };
})();
`;

/**
 * The server. opts: root (the directory of the page), mode 'pack' | 'big' | 'hostile', big {files, lines}, tab (the tab id the Workspace API answers
 * for, default the sessions of the mock), log (a function that is given each API request), delay (ms added to ws/file and ws/diff).
 * Resolves {server, url, state, close()}.
 */
export async function startPackServer(opts) {
  const root = path.resolve(opts.root), mode = opts.mode || 'pack', oracle = mode === 'pack' ? loadOracle(opts.mockDir) : null;
  const big = bigProject(Object.assign({ hostile: mode === 'hostile' }, opts.big || {}));
  const fresh = () => ({ reviewed: Object.create(null), reverted: [], restore: null, nrev: 0 });
  const state = { reqs: [], posts: [], confirms: 0, accepted: [], dirty: !!opts.dirty, insts: new Map() };
  const stOf = inst => { let s = state.insts.get(inst || ''); if (!s) { s = fresh(); if (mode === 'hostile') { s.reviewed.hasOwnProperty = 'c02'; s.reviewed.__proto__ = 'c02'; s.reverted.push({ id: 'v_hostile00000001', path: 'valueOf', key: '1:1', t: 1 }); } state.insts.set(inst || '', s); } return s; };
  const kindOf = id => (id === 'shop' ? 'shop' : id === 'orders-api' ? 'orders' : null);
  const send = (res, code, body, type, extra) => { res.writeHead(code, Object.assign({ 'Content-Type': type || 'application/json; charset=utf-8', 'Content-Security-Policy': CSP, 'X-Content-Type-Options': 'nosniff', 'Cache-Control': 'no-store' }, extra || {})); res.end(typeof body === 'string' || Buffer.isBuffer(body) ? body : JSON.stringify(body)); };
  const err = (res, code, status, msg, detail) => send(res, status, { error: msg, code, detail });
  const readBody = req => new Promise(r => { let b = ''; req.on('data', d => { b += d; }); req.on('end', () => { let v = {}; try { v = b ? JSON.parse(b) : {}; } catch { v = {}; } state.posts.push({ method: req.method, url: req.url.split('?')[0], body: v, confirm: String(req.headers['x-confirm'] || '') }); r(v); }); });
  const queue = { branch: 'sleipnir/20261009-221530-a91c3e/integration', tip: 'ab12cd34ef56ab78', healthy: true, active: 'T4', phase: 'verifying', waiting: ['T5'], landed: [{ agent: 'be-1', task: 'T2', commit: '1a2b3c4d5e6f7a8b', files: ['api/catalog/load.go', 'seed/items.json'] }, { agent: 'fe-1', task: 'T3', commit: '9f8e7d6c5b4a3921', files: ['web/shop.js'] }], verify: 'go test {dirs}' };
  const worktrees = ['be-1', 'be-2', 'fe-1', 'ts-1'].map((a, i) => ({ agent: a, path: '/home/me/.sleipnir/work/' + a, branch: 'sleipnir/20261009-221530-a91c3e/' + a, base: 'ab12cd3', head: i === 0 ? '1a2b3c4' : 'ab12cd3', dirty: i === 1, files: i === 1 ? ['api/cart/cart.go'] : [] }));
  const verify = { T2: { task: 'T2', cmd: 'go test ./api/catalog/...', exitCode: 0, output: 'ok  \tshop/api/catalog\t0.412s\n' }, T4: { task: 'T4', cmd: 'go test ./api/catalog/...', runs: [{ attempt: 1, exit: 1, ms: 5100, at: '03:04:41', out: '--- FAIL: TestPage (0.00s)\n    items_test.go:31: got 13 items, want 12\nFAIL\nFAIL\tshop/api/catalog\t0.4s\n' }, { attempt: 2, exit: 0, ms: 6300, at: '03:04:50', out: 'ok  \tshop/api/catalog\t0.4s\n' }] } };
  const url = u => new URL(u, 'http://x');
  const server = http.createServer((req, res) => handle(req, res).catch(e => { console.error(e); try { err(res, 'internal', 500, String(e && e.message)); } catch { /* sent */ } }));
  async function handle(req, res) {
    const u = url(req.url), p = decodeURIComponent(u.pathname);
    if (opts.log) opts.log(req.method + ' ' + req.url);
    if (p.startsWith('/api/')) {
      state.reqs.push(req.method + ' ' + p + u.search);
      if (req.method === 'POST' && p === '/api/confirm') { const b = await readBody(req); return send(res, 200, { id: 'cf' + (++state.confirms) + '_' + b.scope, scope: b.scope, expires_in: 60 }); }
      const m = /^\/api\/sessions\/([^/]+)\/ws\/([a-z/]+?)(?:\/([^/]+))?(?:\/(undo))?$/.exec(p);
      if (!m) return err(res, 'not_found', 404, 'no such route');
      const id = decodeURIComponent(m[1]), route = m[2], kind = mode === 'pack' ? kindOf(id) : 'big';
      if (kind == null && opts.tabs !== true) { if (route === 'index') return send(res, 200, { root: '/x', isolation: 'none', base: { id: 'base', label: 'before the session', time: '' }, cps: [], tree: [], version: 'v0' }); return err(res, 'no_session', 404, 'no such session'); }
      const st = stOf(u.searchParams.get('inst'));
      const need = k => req.method === k || (err(res, 'method_not_allowed', 405, 'method not allowed'), false);
      let list = []; try { list = JSON.parse(u.searchParams.get('cps') || '[]'); } catch { list = []; }
      const delay = opts.delay ? new Promise(r => setTimeout(r, opts.delay)) : null;
      if (route === 'index' && opts.indexFails) return err(res, 'internal', 500, 'the checkpoints could not be read');
      if (route === 'index' && need('GET')) {
        if (mode !== 'pack') { const b = { root: '/home/me/big', isolation: 'worktree', base: { id: 'base', label: 'before the session', time: '21:00:00' }, cps: big.cps, tree: big.tree, reviewed: st.reviewed, reverted: st.reverted, restore: st.restore || undefined }; b.version = d16(JSON.stringify([big.cps.length, st.reviewed, st.reverted, st.restore])); return send(res, 200, b); }
        return send(res, 200, packIndex(oracle, kind, list, st));
      }
      if (route === 'file' && need('GET')) {
        const f = u.searchParams.get('path') || '', at = u.searchParams.get('at') || 'live'; if (delay) await delay;
        if (f === '.env' || f === 'secrets/signing.pem') return err(res, 'denied', 403, 'agents may not read this file, and the page does not show it');
        if (opts.gone && opts.gone.includes(f)) return err(res, 'no_file', 404, 'this file does not exist at that point');
        if (opts.broken && opts.broken.includes(f)) return err(res, 'internal', 500, 'the file could not be read');
        const c = mode === 'pack' ? packFile(oracle, kind, list, f, at) : bigContent(f, at, { lines: big.lines, hostile: mode === 'hostile' });
        return c ? send(res, 200, c) : err(res, 'no_file', 404, 'this file does not exist at that point');
      }
      if (route === 'diff' && need('GET')) {
        const f = u.searchParams.get('path') || '', from = u.searchParams.get('from') || 'base', to = u.searchParams.get('to') || 'live'; if (delay) await delay;
        const d = mode === 'pack' ? packDiff(oracle, kind, list, f, from, to) : bigDiff(f, from, to, { lines: big.lines, hostile: mode === 'hostile', wide: opts.wideHunk });
        if (d && to === 'live') d.hunks.forEach(h => { h.scope = 'revert:' + id + ':' + scopeOf('h', f, h.oldStart + ':' + h.newStart, from, to, st.rvdrift || 0); });
        if (d && to === 'live') { const gone = st.reverted.filter(r => r.path === f).map(r => r.key); if (gone.length) d.hunks = d.hunks.filter(h => gone.indexOf(h.oldStart + ':' + h.newStart) < 0); }   // a reverted hunk is no longer in the file
        return d ? send(res, 200, d) : err(res, 'no_file', 404, 'this file does not exist at that point');
      }
      if (route === 'reviewed' && need('PUT')) { const b = await readBody(req); if (!b.path) return err(res, 'bad_path', 400, 'a project-relative path is needed'); if (b.on) st.reviewed[b.path] = b.cp; else delete st.reviewed[b.path]; return send(res, 200, { ok: true }); }
      if (route === 'revert' && req.method === 'POST' && !m[4]) {
        const b = await readBody(req); if (!req.headers['x-confirm']) return err(res, 'confirm_required', 428, 'confirm first');
        const scopeNow = () => 'revert:' + id + ':' + scopeOf('h', b.path, b.key, b.from, b.to, st.rvdrift || 0);
        if (opts.revertFails) return err(res, 'changed', 409, 'the file changed since the diff was drawn: look again');
        if (opts.revertDrift && !st.rvdrifted) { st.rvdrifted = true; st.rvdrift = 1; }   // the file changes under the person once: the hunk's scope is another
        if (b.scope !== undefined && (b.scope !== scopeNow() || !String(req.headers['x-confirm']).endsWith('_' + b.scope))) {
          const d = mode === 'pack' ? packDiff(oracle, kind, list, b.path, b.from, b.to) : bigDiff(b.path, b.from, b.to, { lines: big.lines, hostile: mode === 'hostile', wide: opts.wideHunk });
          d.hunks.forEach(h => { h.scope = 'revert:' + id + ':' + scopeOf('h', b.path, h.oldStart + ':' + h.newStart, b.from, b.to, st.rvdrift || 0); });
          return err(res, 'changed', 409, 'the file changed since the diff was drawn: look at the new diff and confirm again', d);
        }
        const r = { id: 'v_' + String(++st.nrev).padStart(16, 'a'), path: b.path, key: b.key, t: 1 }; st.reverted.push(r); return send(res, 200, r);
      }
      if (route === 'revert' && m[4] === 'undo' && req.method === 'POST') { st.reverted = st.reverted.filter(r => r.id !== m[3]); return send(res, 200, { ok: true }); }
      if (route === 'restore' && req.method === 'POST' && !m[3]) {
        const b = await readBody(req); const cp = (mode === 'pack' ? list : big.cps).find(c => c.id === b.id);
        if (!cp || cp.skipped) return err(res, 'nothing', 409, b.id + ' has nothing to put back');
        const planFiles = () => {
          const files = mode === 'pack' ? (() => { const { ws } = oracle, S = { id: kind, kind, runs: 0, m: { ckpts: list.slice().reverse(), tasks: {} } }, I = ws.info(S), idx = I.pos.findIndex(c => c.id === b.id), fs2 = ws.filesFrom(I, idx), before = ws.setAt(I, idx), now = ws.setAt(I, I.pos.length); return fs2.map(f => { const h = ws.hunks(I, f, before, now), sb = ws.textAt(I, f, before); return { path: f, action: sb == null ? 'delete' : 'restore', outcome: b.dryRun ? 'planned' : 'done', added: h.removed, removed: h.added, to: sb == null ? 'removed' : 'back to ' + (idx ? I.pos[idx - 1].id : 'c04') }; }); })() : cp.files.map(f => ({ path: f, action: 'restore', outcome: b.dryRun ? 'planned' : 'done', added: 1, removed: 1, to: 'back to base' }));
          if (st.rdrift) files.push({ path: 'drifted.go', action: 'restore', outcome: b.dryRun ? 'planned' : 'done', added: 2, removed: 1, to: 'back to base' });
          if (opts.restoreConflict) files.push({ path: 'edited.go', action: 'restore', outcome: 'conflict', added: 0, removed: 0, reason: 'edited since the checkpoint' });
          if (opts.restoreFolders) { files.push({ path: 'newdir', action: 'rmdir', outcome: b.dryRun ? 'planned' : 'done', added: 0, removed: 0 }); files.push({ path: 'same.go', action: 'none', outcome: 'unchanged', added: 0, removed: 0 }); }
          return files;
        };
        const scopeNow = () => 'restore:' + id + ':' + b.id + ':' + scopeOf('p', b.id, JSON.stringify(planFiles().map(f => f.path + f.action)));
        if (!b.dryRun) {
          if (!req.headers['x-confirm']) return err(res, 'confirm_required', 428, 'confirm first');
          if (opts.restoreDrift && !st.rdrifted) { st.rdrifted = true; st.rdrift = 1; }
          if (opts.restoreConflict) return err(res, 'conflict', 409, 'files were edited since: nothing was written');
          if (b.scope !== undefined && (b.scope !== scopeNow() || !String(req.headers['x-confirm']).endsWith('_' + b.scope))) { b.dryRun = true; const files = planFiles(); b.dryRun = false; return err(res, 'changed', 409, 'the files changed since the preview: look at the new plan and confirm it again', { id: b.id, label: cp.label || cp.note, time: cp.time || cp.ts || '', files, applied: false, summary: 'would restore ' + files.length, scope: scopeNow() }); }
        }
        const files = planFiles();
        if (!b.dryRun) st.restore = { to: b.id, files: files.map(f => f.path), at: 1, safety: b.id + 's' };
        return send(res, 200, { id: b.id, label: cp.label || cp.note, time: cp.time || cp.ts || '', files, applied: !b.dryRun, safety: b.dryRun ? undefined : b.id + 's', summary: 'checkpoint ' + b.id + ': would restore ' + files.length, scope: scopeNow() });
      }
      if (route === 'restore' && m[3] === 'undo' && req.method === 'POST') { if (!st.restore) return err(res, 'nothing', 409, 'nothing to undo'); const files = st.restore.files.map(f => ({ path: f, action: 'restore', outcome: 'done', added: 0, removed: 0 })); st.restore = null; return send(res, 200, { id: 'c00', label: '', time: '', files, applied: true, summary: 'undone' }); }
      if (route === 'worktrees' && need('GET')) return mode === 'pack' && kind !== 'shop' ? err(res, 'not_isolated', 409, 'this team does not use worktrees') : send(res, 200, { worktrees });
      if (route === 'queue' && need('GET')) return mode === 'pack' && kind !== 'shop' ? err(res, 'not_isolated', 409, 'this team does not use worktrees') : send(res, 200, queue);
      if (route === 'verify' && need('GET')) { const tk = decodeURIComponent(m[3] || ''), v = verify[tk] || (tk === 'T8' ? null : { task: tk, cmd: 'go test ./...', exitCode: 0, output: 'ok  \tshop\t0.2s\n' }); return v ? send(res, 200, v) : err(res, 'not_found', 404, 'no verification has run for this task'); }
      if (route === 'accept' && req.method === 'POST') {
        const b = await readBody(req), md = b.mode || 'commits';
        const filesNow = () => ['api/catalog/load.go', 'seed/items.json', 'web/shop.js'].concat(st.adrift ? ['web/extra.js'] : []);
        const scopeNow = () => 'accept:' + id + ':' + scopeOf('a', md, JSON.stringify(filesNow()));
        const view = dry => ({ dryRun: dry || undefined, waiting: true, canCommit: !state.dirty, commitBlocked: state.dirty ? 'dirty' : '', files: filesNow(), tasks: ['T2', 'T3'], branch: 'main', message: 'verified work of 2 tasks is waiting', scope: scopeNow() });
        if (b.dryRun) return send(res, 200, view(true));
        if (!req.headers['x-confirm']) return err(res, 'confirm_required', 428, 'confirm first');
        if (opts.acceptDrift && !st.adrifted) { st.adrifted = true; st.adrift = 1; }
        if (b.scope !== undefined && (b.scope !== scopeNow() || !String(req.headers['x-confirm']).endsWith('_' + b.scope))) return err(res, 'changed', 409, 'the verified work changed since it was shown: look at it again and confirm', view(true));
        if (state.dirty) return send(res, 409, { error: 'your checkout has uncommitted changes in the files to be applied', code: 'dirty', detail: { hint: 'git stash && sleipnir web accept' } });
        state.accepted.push({ mode: b.mode, message: b.message, scope: b.scope }); return send(res, 200, { commit: 'deadbeefcafe0123', files: filesNow(), branch: 'main', applied: true, committed: md !== 'edits' });
      }
      return err(res, 'not_found', 404, 'no such route');
    }
    // files
    let rel = p; if (rel === '/') rel = '/' + (opts.index || 'index.html'); if (rel === '/__ws-shim.js') return send(res, 200, SHIM, TYPES['.js']);
    const file = path.resolve(root, '.' + rel); if (file !== root && !file.startsWith(root + path.sep)) return send(res, 404, 'not found', 'text/plain');
    fs.readFile(file, (e, buf) => {
      if (e) return send(res, 404, 'not found', 'text/plain');
      let body = buf; if (rel.endsWith('.html')) { let h = buf.toString('utf8'); if (opts.inject !== false) { if (!/js\/api\.js/.test(h)) h = h.replace('<script src="js/00-util.js"></script>', '<script src="js/00-util.js"></script><script src="js/api.js"></script>'); h = h.replace('<script src="js/00-namespace.js"></script>', '<script src="js/00-namespace.js"></script><script src="__ws-shim.js"></script>'); } body = h; }
      send(res, 200, body, TYPES[path.extname(file)] || 'application/octet-stream');
    });
  }
  await new Promise((ok, bad) => { server.once('error', bad); server.listen(opts.port || 0, '127.0.0.1', ok); });
  const port = server.address().port;
  return { server, port, opts, url: 'http://127.0.0.1:' + port + '/', state, close: () => new Promise(r => server.close(r)) };
}
