/* 96b-ws-data.js: SL.ws, the data layer of the Workspace (Files / Changes / Checkpoints): the history of a project as the data pack records it
 * (a base, then one change set per checkpoint) turned into what the screen needs at any point in time:
 *
 *   info(S)                      the checkpoints of the session (arrival order), the scrubber positions, the project
 *   textAt(info, path, steps)    the content of a file after the change sets `steps` (arrival order) were applied
 *   status / owner / blame       A, M or - against the base; who wrote the file last; who wrote each line (a line diff carried step by step)
 *   rows(S, info, steps)         the tree with ownership, leases, protected paths and `ask` markers
 *   hunks(info, path, a, b)      the diff between two states, in the pack's own hunk format
 *
 * A "step" is the pack's id of a change set (c05 .. c12); the id a person sees is numbered by ARRIVAL (m.ckpts[i].id), so the screen is
 * right whatever order the agents finish in. Everything here is pure: it reads the model, never changes it. */
(function (SL) {
  'use strict';
  const D = SL.D, X = D.extra, ws = SL.ws = {};

  const rawOf = S => !X.files || !S ? null : S.kind === 'shop' ? X.files.shop : S.kind === 'orders' ? X.files.orders : null;
  const lines = t => t == null || t === '' ? [] : String(t).replace(/\n$/, '').split('\n');

  /** line diff (LCS): ops over a and b, ' ' kept, '-' only in a, '+' only in b */
  function lineOps(a, b) {
    const n = a.length, m = b.length, w = m + 1, T = new Uint16Array((n + 1) * w);
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) T[i * w + j] = a[i] === b[j] ? T[(i + 1) * w + j + 1] + 1 : Math.max(T[(i + 1) * w + j], T[i * w + j + 1]);
    const ops = []; let i = 0, j = 0;
    while (i < n && j < m) { if (a[i] === b[j]) { ops.push(' '); i++; j++; } else if (T[(i + 1) * w + j] >= T[i * w + j + 1]) { ops.push('-'); i++; } else { ops.push('+'); j++; } }
    while (i < n) { ops.push('-'); i++; } while (j < m) { ops.push('+'); j++; }
    return ops;
  }

  /** The session's checkpoints and the positions of the time-travel scrubber. null when the sample data has no history for this session. */
  function info(S) {
    const raw = rawOf(S); if (!raw) return null; const m = S.m || S.wm; if (!m) return null;
    const key = S.id + '|' + S.runs + '|' + m.ckpts.map(c => c.id + (c.step || '')).join(',');
    if (S._wsi && S._wsi.key === key) return S._wsi;
    const stepBy = {}; raw.steps.forEach(s => { stepBy[s.id] = s; });
    const cps = m.ckpts.slice().reverse().map(c => {
      const st = c.step && stepBy[c.step] ? stepBy[c.step] : null, ds = st ? (raw.diffs[st.id] || []) : [];
      const agents = [], tasks = []; let added = 0, removed = 0; ds.forEach(f => { f.agents.forEach(a => { if (agents.indexOf(a) < 0) agents.push(a); }); if (f.task && tasks.indexOf(f.task) < 0) tasks.push(f.task); added += f.added; removed += f.removed; });
      return { id: c.id, step: st ? st.id : null, time: c.ts, label: c.note, skipped: !!c.skipped || (!st && !c.safety), safety: !!c.safety, files: ds.map(f => f.path), agents, tasks, added, removed, nfiles: c.files };
    });
    const pos = []; let lastSkip = null; cps.forEach(c => { if (c.step) pos.push(c); else if (!pos.length && c.skipped) lastSkip = c; });
    const I = { key, raw, cps, steps: stepBy, base: { id: lastSkip ? lastSkip.id : 'start', label: lastSkip ? lastSkip.label : 'before the session', time: lastSkip ? lastSkip.time : '' }, pos, cache: {}, stepOfId: {} };
    cps.forEach(c => { if (c.step) I.stepOfId[c.id] = c.step; });
    S._wsi = I; return I;
  }
  /** the change sets applied at scrubber index k (0 = the base, 1 = the first checkpoint ...) */
  const setAt = (I, k) => I.pos.slice(0, Math.max(0, k)).map(c => c.step);

  function textAt(I, path, steps) {
    const v = I.raw.versions[path];
    if (!v) { const c = I.raw.contents[path]; return c == null ? null : c; }
    let out = v[0][0] === 'c04' ? v[0][1] : null; const have = {}; steps.forEach(s => { have[s] = 1; });
    for (let i = 1; i < v.length; i++) if (have[v[i][0]]) out = v[i][1];
    return out;
  }
  /** touches of a file by the applied change sets, in order: [{step, id, ag, task}] */
  function touches(I, path, steps) {
    const out = [];
    steps.forEach(s => { const f = (I.raw.diffs[s] || []).find(d => d.path === path); if (f) { const cp = I.pos.find(c => c.step === s); out.push({ step: s, id: cp ? cp.id : s, ag: f.agents[0], task: f.task, status: f.status, added: f.added, removed: f.removed }); } });
    return out;
  }
  function statusOf(I, path, steps) {
    const base = textAt(I, path, []), now = textAt(I, path, steps);
    if (now == null && base == null) return '-'; if (base == null && now != null) return 'A'; if (now == null) return 'D'; return base === now ? '-' : 'M';
  }
  /** who wrote each line: [{ag, id, task}] ('-' = there before the session) */
  function blame(I, path, steps) {
    const k = path + '|' + steps.join(','); if (I.cache[k]) return I.cache[k];
    const v = I.raw.versions[path]; let prev = lines(v ? (v[0][0] === 'c04' ? v[0][1] : null) : I.raw.contents[path]), cur = prev.map(() => ({ ag: '-', id: '', task: '' }));
    if (v) steps.forEach(s => {
      const ver = v.find(x => x[0] === s); if (!ver) return; const t = touches(I, path, [s])[0] || { ag: '?', id: s, task: '' }, nl = lines(ver[1]), ops = lineOps(prev, nl), out = []; let i = 0;
      ops.forEach(o => { if (o === ' ') { out.push(cur[i]); i++; } else if (o === '-') i++; else out.push({ ag: t.ag, id: t.id, task: t.task }); });
      prev = nl; cur = out;
    });
    return (I.cache[k] = cur);
  }
  /** the diff between the file after steps `a` and after steps `b` in the pack's hunk format ({added, removed, hunks}) */
  function hunks(I, path, a, b) {
    const k = 'h|' + path + '|' + a.join(',') + '>' + b.join(','); if (I.cache[k]) return I.cache[k];
    return (I.cache[k] = X.diffText(textAt(I, path, a) || '', textAt(I, path, b) || ''));
  }
  /** +/- of a file between two states, as the pack counts them (the sum of its change sets in b and not in a): exact even where the sample keeps only part of a file */
  function counts(I, path, a, b) {
    let add = 0, del = 0; b.forEach(s => { if (a.indexOf(s) >= 0) return; const f = (I.raw.diffs[s] || []).find(d => d.path === path); if (f) { add += f.added; del += f.removed; } }); return { added: add, removed: del };
  }
  const dirOf = p => p.indexOf('/') < 0 ? '' : p.slice(0, p.lastIndexOf('/'));

  /** The tree at a point in time: one row per file that exists, with ownership (last writer), lease (until its task merges), protection and ask markers. */
  function rows(S, I, steps) {
    const m = S.m || S.wm, out = [];
    I.raw.tree.forEach(f => {
      const st = statusOf(I, f.path, steps), tch = touches(I, f.path, steps), last = tch[tch.length - 1], exists = f.existsAtSnapshot || st === 'A' || st === 'M' || st === 'D';
      if (!exists && !tch.length) return;
      const T = f.lease && m.tasks[f.lease.task], leased = !!(f.lease && T && T.st !== 'merged');
      let add = 0, del = 0; if (st !== '-') { const h = counts(I, f.path, [], steps); add = h.added; del = h.removed; }
      out.push({ path: f.path, dir: f.dir == null ? dirOf(f.path) : f.dir, name: f.path.slice(f.path.lastIndexOf('/') + 1), status: st, owner: last ? last.ag : null, task: last ? last.task : null, cp: last ? last.id : null, lease: leased ? f.lease : null, protected: f.protected, ask: f.ask, size: 0, add, del, kind: f.kind });
    });
    return out;
  }
  /** the file being written right now: the agent in state `edit` whose `doing` names a path of the tree, or the code stream of a tester */
  function liveFile(S, I) {
    const m = S.m; if (!m || !I) return null; let out = null;
    m.order.forEach(id => { const A = m.ag[id]; if (!A || id === 'mgr' || A.state !== 'edit') return; const p = I.raw.tree.find(f => A.doing && A.doing.indexOf(f.path) >= 0); if (p) out = { path: p.path, ag: id, task: A.task }; });
    return out;
  }
  /** the not-yet-recorded file a tester is streaming: typed over the stream's own time from the final version in the data. */
  function pending(S, I, path, vt) {
    const m = S.m; if (!m || path !== 'api/catalog/items_test.go' || S.kind !== 'shop') return null; const st = m.streams['ts-1']; if (!st || !st.code) return null;
    const full = textAt(I, path, ['c08']); if (!full) return null; const dur = st.text.length / st.rate, n = Math.max(0, Math.floor(full.length * Math.min(1, (vt - st.t0) / dur)));
    if (vt - st.t0 > dur + 1.5) return null; return { text: full.slice(0, n), done: n >= full.length, ag: 'ts-1', task: 'T7' };
  }
  /** files touched in change set `step` and later, up to the applied ones (what /rewind puts back) */
  function filesFrom(I, stepIdx) {
    const seen = []; I.pos.slice(stepIdx).forEach(c => c.files.forEach(f => { if (seen.indexOf(f) < 0) seen.push(f); })); return seen;
  }
  ws.rawOf = rawOf; ws.info = info; ws.setAt = setAt; ws.textAt = textAt; ws.touches = touches; ws.statusOf = statusOf; ws.blame = blame; ws.hunks = hunks; ws.counts = counts; ws.rows = rows; ws.liveFile = liveFile; ws.pending = pending; ws.filesFrom = filesFrom; ws.lineOps = lineOps; ws.lines = lines;
  /** hover linking: the owner of a file at the live edge (the engine asks through SL.fileOwner) */
  SL.fileOwner = f => { const S = SL.sessions.active, I = S && info(S); if (!I) return null; const t = touches(I, f, setAt(I, I.pos.length)); return t.length ? t[t.length - 1].ag : null; };
})(SL);
