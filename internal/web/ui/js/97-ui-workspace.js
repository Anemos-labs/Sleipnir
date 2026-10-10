/* 97-ui-workspace.js: the Workspace view: Files, Changes, Checkpoints and Merge are four tabs of ONE screen (the Forge tracker in Cockpit's idiom).
 *   left   the tree (ownership stripes in the writer's role colour, leases, protected paths, `ask` markers), the changes grouped by task or
 *          by agent, the checkpoint list (Diff and Restore with a preview and an in-page confirm), or the worktrees of an isolated team
 *   centre the diff of the selected file with a per-line attribution gutter, hunk revert, Reviewed marks; a whole-file view with blame;
 *          for Merge the queue and the output of a task's verification
 *   top    the time-travel scrubber: the project as it was at any checkpoint, from the base to the newest
 *   bottom the verify / merge strip: the harness running the verify command on what is submitted, and "Apply verified work"
 * The history is the server's (SL.ws, 96b-ws-data.js) and the session's model; the person's marks (reviewed files, reverted hunks, a
 * restore) are the server's too and change only through SL.act. Text from files and paths is data: it is escaped, made printable
 * (control, zero-width and bidirectional characters show as code points) and bounded, never markup. A list or a diff of more than a few
 * hundred rows draws only the rows in view, with the same markup as the short ones. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, agCol, mmss } = U, ui = SL.ui = SL.ui || {}, ws = SL.ws, F = ws.fmt, hl = F.hl;
  const arr = x => (Array.isArray(x) ? x : []);
  const WIN_LIST = 250, WIN_DIFF = 600, OVER = 12;                       // rows above which a list or a diff draws only the rows in view; rows drawn beyond the view
  const BIG_TREE = 1500, BIG_DIR = 200, CPS_PAGE = 150;                 // a tree of more files than this starts with its large, unchanged directories closed; checkpoints listed at first
  /** An id from the server inside a CSS value or a class: letters, digits, `_` and `-` only. */
  const sid = s => String(s == null ? '' : s).replace(/[^A-Za-z0-9_-]/g, '_');
  const CODE_OF = (S, id) => { const r = S.roster.find(x => x.id === id); return r ? sid(r.code) : null; };
  const aCol = (S, id) => id && id !== '-' ? agCol(sid(id)) : 'var(--faint)';
  const aTint = (S, id) => { const c = CODE_OF(S, id); return c ? 'var(--' + c + '-a)' : 'var(--ok-a)'; };
  const vt = s => esc(F.vis(s));                                         // text of a path or a name: printable, escaped
  const LOCK = '<svg class="lk" viewBox="0 0 12 12" width="11" height="11" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"><rect x="2.4" y="5.4" width="7.2" height="5" rx="1"/><path d="M4 5.4V4a2 2 0 0 1 4 0v1.4"/></svg>';
  const ST_TAG = { A: ['A', 'ok', 'added'], M: ['M', 'warm', 'modified'], D: ['D', 'err', 'deleted'], R: ['R', 'fe', 'restored'] };
  const TASK_ST = { merged: ['✓ merged', 'ok'], verify: ['▸ verifying', 'warm'], running: ['● running', 'fe'], todo: ['◌ todo', ''] };
  const plural = (n, w) => n + ' ' + w + (n === 1 ? '' : 's');
  const LOADING = 'Loading the workspace…';
  const NO_ISOLATION = 'no worktree isolation in this run: work is written to the checkout directly';

  const W0 = () => ({ tab: 'files', file: null, cp: null, mode: 'since', view: 'diff', group: 'task', q: '', changed: false, closed: {}, unreviewed: false, task: null, mview: 'queue', mtask: null, mattempt: null, wt: null, cpMore: 0 });
  const wsOf = S => (S.ui.ws = Object.assign(W0(), S.ui.ws || {}));
  const hasOpenAsk = (m, t) => m.qs.some(q => !q.answered && q.task === t);

  /** The scrubber position the view state names, and the range of the diff it shows: [after a, after b] as lists of applied change sets. */
  function rangeFor(S, I, W2) {
    const n = I.pos.length; let k = n; if (W2.cp === 'base') k = 0; else if (W2.cp) { const i = I.pos.findIndex(c => c.id === W2.cp); if (i >= 0) k = i + 1; else W2.cp = null; }
    const steps = ws.setAt(I, k), prev = ws.setAt(I, Math.max(0, k - 1)), all = ws.setAt(I, n);
    return { n, k, steps, prev, all, range: W2.mode === 'step' ? [prev, steps] : W2.mode === 'from' ? [prev, all] : [[], steps] };
  }
  /** The points ("base", a checkpoint id, "live") of the diff the Workspace shows now: what a hunk revert is asked against (SL.act.revertHunk reads it). */
  ws.rangeOf = function (S) {
    const I = ws.info(S); if (!I || !S.ui || !S.ui.ws) return null; const R = rangeFor(S, I, wsOf(S));
    return { from: ws.pointId(I, ws.pointOf(I, R.range[0])), to: ws.pointId(I, ws.pointOf(I, R.range[1])) };
  };
  /** the counts the left rail shows next to Changes and Checkpoints (cheap, cached on the session's history) */
  function counts(S) {
    const I = ws.info(S); if (!I) { const c = derived(S); return { changed: c.length, ckpts: S.m ? S.m.ckpts.filter(x => !x.skipped).length : 0, reviewed: 0 }; }
    const k = I.key + '|' + (S.m ? S.m.merged.length : 0); if (S._wsc && S._wsc.k === k) return S._wsc.v;
    const changed = ws.rows(S, I, ws.setAt(I, I.pos.length)).filter(r => r.status !== '-').length, v = { changed, ckpts: I.cps.filter(c => c.step || c.safety).length, reviewed: 0 }; S._wsc = { k, v }; return v;
  }
  /** a session without a readable history (the index loading, or not readable): the files its tool calls touched */
  function derived(S) {
    const m = S.m || S.wm, by = {}; if (!m) return [];
    m.order.forEach(id => (m.chan[id] || []).forEach(e => { if (e.k === 'tool' && e.file && !e.refused) { const r = by[e.file] || (by[e.file] = { path: e.file, ag: id, add: 0, del: 0, n: 0, task: e.task }); r.add += e.add || 0; r.del += e.del || 0; r.n++; r.ag = id; if (e.task) r.task = e.task; } }));
    return Object.keys(by).sort().map(k => by[k]);
  }
  const rowMapOf = rows => rows._map || (rows._map = new Map(rows.map(r => [r.path, r])));

  /* ================================================================ the restore dialog: preview (dry run) -> confirm -> apply ============ */

  const ACT_TAG = { restore: ['put back', 'warm'], recreate: ['put back', 'warm'], delete: ['removed', 'err'], none: ['unchanged', ''], chmod: ['mode', 'warm'], relink: ['link', 'warm'], mkdir: ['folder', ''], rmdir: ['folder', ''] };
  const REFUSED = { conflict: 'edited since', unrestorable: 'cannot be put back', failed: 'failed' };
  /** the table of a restore plan: file / what happens / lines / result */
  function planRows(I, id, plan) {
    const idx = I.pos.findIndex(x => x.id === id), before = idx > 0 ? I.pos[idx - 1].id : I.base.id;
    return plan.files.filter(f => f.action !== 'mkdir' && f.action !== 'rmdir').map(f => {
      const a = ACT_TAG[f.action] || [F.vis(f.action) || 'change', ''], bad = REFUSED[f.outcome];
      const res = bad ? '<span class="err">' + esc(bad) + '</span>' + (f.reason ? ' <span class="dim">' + esc(F.vis(f.reason)) + '</span>' : '') : esc(F.vis(f.to) || (f.action === 'delete' ? 'removed' : 'back to ' + before));
      return '<tr><td class="bright mono">' + vt(f.path) + '</td><td><span class="tag ' + a[1] + '">' + esc(a[0]) + '</span></td><td class="r num"><b class="ok">+' + f.added + '</b> <b class="err">−' + f.removed + '</b></td><td class="dim">' + res + '</td></tr>';
    }).join('');
  }
  /** Restore ID: ask the server what each file would do, then show it with its confirm. Nothing is written until the person says so. */
  async function restoreDialog(S, id) {
    const I = ws.info(S), cp = I ? I.pos.find(x => x.id === id) : null;
    if (!I || !cp) { ui.toast(id + ' has nothing to put back', 'warm'); return; }
    const r = await ws.ops.previewRestore(S, id);
    if (!r.ok) { ui.toast(r.message || id + ' has nothing to put back', 'warm'); return; }
    const plan = r.plan, bad = plan.files.filter(f => REFUSED[f.outcome]).length, n = plan.files.filter(f => f.action !== 'mkdir' && f.action !== 'rmdir').length;
    ui.modal({ title: 'Restore ' + cp.id, kicker: '/rewind ' + cp.id, desc: 'puts the files back to before this turn; the conversation stays', wide: true, color: 'var(--rv)', focus: '[data-no]',
      body: '<p class="stubnote"><b>' + esc(F.vis(plan.label || cp.label)) + '</b> · ' + esc(plan.time || cp.time) + ' · ' + plural(n, 'file') + ' touched in this checkpoint or later. The agents are told to read them again. <span class="warm">A safety checkpoint is taken first, so this can be undone.</span></p>' +
        (bad ? '<p class="stubnote"><span class="err">' + plural(bad, 'file') + ' cannot be put back</span> (edited since, or never saved): nothing is written until that is resolved.</p>' : '') +
        '<table class="tbl"><thead><tr><th>file</th><th>what happens</th><th class="r">lines</th><th>result</th></tr></thead><tbody>' + planRows(I, cp.id, plan) + '</tbody></table><div class="row2"><button class="btn danger" type="button" data-ok' + (bad ? ' disabled title="a file cannot be put back"' : '') + '>Restore the files</button><button class="btn" type="button" data-no>Cancel</button></div>',
      onMount(b, scm, close) {
        scm.listen($('[data-ok]', b), 'click', () => { close(); const r = SL.act.rewind(cp.id); if (!r.ok) ui.toast(r.why || cp.id + ' has nothing to put back', 'warm'); else if (r.done) r.done.then(x => { if (x && x.ok) ui.toast('files put back to before ' + cp.id, 'ok'); }); });
        scm.listen($('[data-no]', b), 'click', close);
      } });
  }

  /* ================================================================ the Apply verified work dialog (accept -> commit) =================== */

  const BLOCKED = { dirty: 'your checkout has uncommitted changes', moved: 'your branch moved since the team started', branch: 'your checkout is not on the session’s branch' };
  /** "Apply verified work…": what the server says it would apply (a dry run), the choice between commits and uncommitted edits, and the real call. */
  async function acceptDialog(S) {
    const q = ws.merge.queue(S).data || {}, pv = await ws.ops.accept(S, { dryRun: true });
    if (!pv.ok && pv.code === 'nothing') { ui.toast(pv.message || 'nothing verified is waiting', 'warm'); return; }
    const info = pv.ok ? pv.result : ws.normAccept({}), files = info.files.slice(), tasks = info.tasks.slice();
    arr(q.landed).forEach(l => { if (l.task && tasks.indexOf(l.task) < 0) tasks.push(l.task); arr(l.files).forEach(f => { if (files.indexOf(f) < 0) files.push(f); }); });
    if (!files.length && !tasks.length) { ui.toast('nothing verified is waiting', 'warm'); return; }
    const branch = info.branch || q.branch || 'your branch', why = BLOCKED[info.commitBlocked] || info.commitBlocked;
    let mode = info.canCommit ? 'commits' : 'edits';
    ui.modal({ title: 'Apply the verified work', kicker: 'apply', desc: 'what passed verification reaches your checkout now', color: 'var(--ok)', focus: '[data-msg]',
      body: '<p class="stubnote">The work of ' + plural(tasks.length, 'task') + (tasks.length ? ' (<span class="mono">' + esc(tasks.map(F.vis).join(' ')) + '</span>)' : '') + ' that passed <span class="mono">' + esc(F.vis(q.verify || 'the merge queue')) + '</span> is applied: ' + plural(files.length, 'file') + '. Nothing else in your checkout is touched.' + (info.message ? ' <span class="dim">' + esc(F.vis(info.message)) + '</span>' : '') + '</p>' +
        '<div class="fld"><label>apply</label><div class="fc"><div class="seg" role="group" aria-label="Apply as"><button type="button" data-am="commits" aria-pressed="' + (mode === 'commits') + '"' + (info.canCommit ? '' : ' disabled title="' + esc(why) + '"') + '>as commits on <span class="mono">' + esc(F.vis(branch)) + '</span></button><button type="button" data-am="edits" aria-pressed="' + (mode === 'edits') + '">as uncommitted edits</button></div>' + (info.canCommit ? '' : '<small class="hint">committing is not offered: ' + esc(F.vis(why)) + '</small>') + '</div></div>' +
        '<div class="fld" data-mf><label for="wsMsg">commit message</label><div class="fc"><input id="wsMsg" data-msg type="text" maxlength="200" placeholder="optional: the harness writes one" autocomplete="off"><small class="hint">one line; leave it empty for the default</small></div></div>' +
        '<div class="diff wdiff" style="max-height:180px;overflow:auto">' + files.slice(0, 200).map(f => '<div class="ln ctx"><b class="who"></b><i></i><s></s><span>' + vt(f) + '</span></div>').join('') + (files.length > 200 ? '<div class="ln hunk"><b class="who"></b><i></i><s></s><span>… ' + (files.length - 200) + ' more</span></div>' : '') + '</div>' +
        '<div class="wsres" aria-live="polite"></div><div class="row2"><button class="btn pri" type="button" data-ok>Apply them</button><button class="btn" type="button" data-no>Cancel</button></div>',
      onMount(b, scm, close) {
        const ok = $('[data-ok]', b), res = $('.wsres', b), mf = $('[data-mf]', b), sync = () => { $$('[data-am]', b).forEach(x => x.setAttribute('aria-pressed', String(x.dataset.am === mode))); mf.hidden = mode !== 'commits'; }; sync();
        $$('[data-am]', b).forEach(x => scm.listen(x, 'click', () => { if (x.disabled) return; mode = x.dataset.am; sync(); }));
        scm.listen($('[data-no]', b), 'click', close);
        scm.listen(ok, 'click', async () => {
          ok.disabled = true; const r = await ws.ops.accept(S, { mode, message: mode === 'commits' ? $('[data-msg]', b).value.trim() : '' });
          if (r.ok) { close(); const x = r.result; ui.toast(x.message || ('applied: ' + plural(x.files.length || files.length, 'file') + (x.committed || x.commit ? ' committed' + (x.commit ? ' as ' + x.commit.slice(0, 8) : '') + (x.branch ? ' on ' + x.branch : '') : ' as uncommitted edits')), 'ok'); return; }
          ok.disabled = false; const hint = r.detail && r.detail.hint;
          res.innerHTML = '<p class="stubnote"><span class="err">' + esc(F.vis(r.message || 'it could not be applied')) + '</span></p>' + (hint ? '<pre class="pre cli">' + esc(F.vis(hint)) + '</pre><button class="btn sm" type="button" data-hint>Copy the command</button>' : '');
          const hb = $('[data-hint]', res); if (hb) scm.listen(hb, 'click', () => U.copy(hint).then(c => ui.toast(c ? 'command copied' : 'copy is not available here', c ? 'ok' : 'warm')));
        });
      } });
  }

  /* ================================================================ the view ======================================================== */

  function mount(sc, root, params, S0) {
    const W = wsOf(S0); if (params && params.tab) W.tab = params.tab; if (params && params.file) { W.file = params.file; W.view = 'diff'; }
    root.innerHTML = '<div class="wsv" data-tab="' + esc(W.tab) + '">' +
      '<section class="panel ws-bar" aria-label="Workspace controls"><div class="ws-tabs seg" role="tablist" aria-label="Workspace"><button type="button" role="tab" data-tab="files">Files</button><button type="button" role="tab" data-tab="changes">Changes</button><button type="button" role="tab" data-tab="checkpoints">Checkpoints</button><button type="button" role="tab" data-tab="merge">Merge</button></div>' +
      '<div class="ws-tt" style="flex-basis:200px"><label class="lab" for="wsScrub">time travel</label><div class="ws-scr"><input id="wsScrub" class="scrub" type="range" min="0" max="1" step="1" value="1" aria-label="Time travel across checkpoints: the project as it was at one of them"><div class="ws-ticks" aria-hidden="true"></div></div><span class="ws-at num"></span><button class="btn sm ws-live" type="button">● live</button></div>' +
      '<div class="ws-sum"></div></section>' +
      '<section class="panel ws-left" aria-label="Files, changes and checkpoints"><div class="ph"><h2 class="ws-lh">Files</h2><div class="r ws-lr"></div></div><div class="ws-ctl"></div><div class="ws-list" role="group"></div></section>' +
      '<section class="panel ws-main" aria-label="The selected file"><div class="ws-fh"></div><div class="ws-note" hidden></div><div class="ws-body" tabindex="0"></div></section>' +
      '<section class="panel ws-strip" aria-label="Verify and merge"></section></div>';
    const q = s => $(s, root), left = q('.ws-list'), ctl = q('.ws-ctl'), fh = q('.ws-fh'), note = q('.ws-note'), body = q('.ws-body'), strip = q('.ws-strip'), scrub = q('#wsScrub'), ticks = q('.ws-ticks'), at = q('.ws-at'), live = q('.ws-live'), sum = q('.ws-sum');
    const H = { typed: false, sig: {} };

    /** replace innerHTML only when it changed; keep the keyboard focus on the same row */
    function put(el, h) {
      if (el._h === h) return false; el._h = h; const a = document.activeElement, own = a && el.contains(a) && a !== el, key = own ? (a.dataset.file || a.dataset.cp || a.dataset.dir || a.dataset.task || a.dataset.wt || a.id || '') : null, tag = own ? a.tagName + (a.dataset.file ? 'f' : a.dataset.cp ? 'c' : a.dataset.dir ? 'd' : a.dataset.task ? 't' : a.dataset.wt ? 'w' : '') : null;
      el.innerHTML = h;
      if (key && tag) { const sel = tag.endsWith('f') ? '[data-file="' + CSS.escape(key) + '"]' : tag.endsWith('c') ? '[data-cp="' + CSS.escape(key) + '"]' : tag.endsWith('d') ? '[data-dir="' + CSS.escape(key) + '"]' : tag.endsWith('t') ? '[data-task="' + CSS.escape(key) + '"]' : tag.endsWith('w') ? '[data-wt="' + CSS.escape(key) + '"]' : '#' + CSS.escape(key); const n = key ? el.querySelector(sel) : null; if (n && n.focus) n.focus({ preventScroll: true }); }
      return true;
    }

    /* ---------------- windows: draw only the rows in view of a long list or diff ---------------- */
    /** A window over a scroller: set(items, htmlOf, kindOf, pinOf) then draw(). Rows of one kind have one height, measured from the first one drawn. */
    function mkWin(el, wrap, hs0) {
      const V = { el, items: 0, htmlOf: null, kindOf: null, pinOf: null, o: null, hs: Object.assign({}, hs0), key: '', ver: 0, on: false, tail: '' };
      const tag = (h, i, k) => h.replace(/^<(\w+)/, '<$1 data-vk="' + k + '" data-vi="' + i + '"');
      V.set = (n, htmlOf, kindOf, pinOf, tail) => { V.items = n; V.htmlOf = htmlOf; V.kindOf = kindOf; V.pinOf = pinOf || null; V.tail = tail || ''; V.o = F.offsets(n, i => V.hs[V.kindOf(i)] || 26); V.key = ''; V.ver++; V.on = true; el.style.overflowAnchor = 'none'; };
      V.off = () => { V.on = false; V.key = ''; };
      V.draw = force => {
        if (!V.on) return false;
        const w = F.windowOf(V.o, el.scrollTop, el.clientHeight || 480, OVER); let pin = V.pinOf ? V.pinOf(w.a) : -1; if (pin >= w.a) pin = -1;
        const key = w.a + ':' + w.b + ':' + pin + ':' + V.ver; if (!force && key === V.key) return false; V.key = key;
        const a = document.activeElement, fi = a && el.contains(a) && a.dataset && a.dataset.vi != null ? a.dataset.vi : null;
        let h = '', ph = 0; if (pin >= 0) { const k = V.kindOf(pin); h += tag(V.htmlOf(pin), pin, k); ph = V.hs[k] || 0; }
        for (let i = w.a; i < w.b; i++) h += tag(V.htmlOf(i), i, V.kindOf(i));
        const total = V.o[V.o.length - 1];
        const inner = '<div style="height:' + Math.max(0, V.o[w.a] - ph) + 'px"></div>' + h + '<div style="height:' + Math.max(0, total - V.o[w.b]) + 'px"></div>' + V.tail;
        el._h = null; (wrap ? wrap(el) : el).innerHTML = inner;
        if (fi != null) { const n = el.querySelector('[data-vi="' + fi + '"]'); if (n && n.focus) n.focus({ preventScroll: true }); }
        // measure: the real height of a row of each kind decides where the others are
        let changed = false;
        Object.keys(V.hs).forEach(k => { const n = el.querySelector('[data-vk="' + k + '"]'); if (!n) return; const r = n.getBoundingClientRect().height; if (r > 0 && Math.abs(r - V.hs[k]) > 0.4) { V.hs[k] = r; changed = true; } });
        if (changed) { V.o = F.offsets(V.items, i => V.hs[V.kindOf(i)] || 26); V.key = ''; V.draw(true); }
        return true;
      };
      V.reveal = i => { const top = V.o[i], bot = V.o[i + 1], v = el.clientHeight; if (top < el.scrollTop) el.scrollTop = top; else if (bot > el.scrollTop + v) el.scrollTop = bot - v; V.draw(); };
      return V;
    }
    const VL = mkWin(left, null, { d: 26, f: 26, r: 27, g: 33 }), VB = mkWin(body, el => $('.wdiff', el) || el, { l: 18, h: 26 });
    sc.listen(left, 'scroll', () => { if (VL.on) VL.draw(); }); sc.listen(body, 'scroll', () => { if (VB.on) VB.draw(); });

    /* ---------------- the view of the world at the scrubber ---------------- */
    function ctx() {
      const S = SL.sessions.active, W2 = wsOf(S), I = ws.info(S), m = S.m, mkv = ws.marks(S);
      if (!I || !m) return { S, W: W2, I: null, m, st: ws.status(S), mk: mkv };
      const R = rangeFor(S, I, W2), n = R.n, k = R.k, steps = R.steps, prev = R.prev, all = R.all, range = R.range;
      const base = ws.rows(S, I, W2.mode === 'from' ? all : steps), bmap = rowMapOf(base), pends = k === n ? ws.pendingAll(S, I, S.vt) : [];
      const extra = pends.filter(p => !bmap.has(p.path)).map(p => { const f = I.byPath.get(p.path), cut = p.path.lastIndexOf('/'); return { path: p.path, dir: cut < 0 ? '' : p.path.slice(0, cut), name: p.path.slice(cut + 1), status: 'A', owner: p.ag, task: p.task, cp: null, lease: f && f.lease, protected: null, ask: null, add: F.lines(p.text).length, del: 0, writing: true, kind: 'text' }; });
      const rows = extra.length ? base.concat(extra) : base, rmap = extra.length ? new Map(Array.from(bmap).concat(extra.map(r => [r.path, r]))) : bmap;
      return { S, W: W2, I, m, n, k, steps, prev, all, range, mk: mkv, cur: k > 0 ? I.pos[k - 1] : null, pinned: k < n, rows, rmap, pends, lastId: p => ws.lastId(I, p), reviewed: p => { const l = ws.lastId(I, p); return !!(l && mkv.reviewed[p] === l); } };
    }
    const pointsOf = c => [ws.pointId(c.I, ws.pointOf(c.I, c.range[0])), ws.pointId(c.I, ws.pointOf(c.I, c.range[1]))];

    /* ---------------- left: files ---------------- */
    function owners(rows) { const o = []; rows.forEach(r => { if (r.owner && o.indexOf(r.owner) < 0) o.push(r.owner); }); return o; }
    function fileRow(c, r, depth, opt) {
      const S = c.S, sel = c.W.file === r.path, tag = ST_TAG[opt && opt.restored ? 'R' : r.status], col = r.owner ? aCol(S, r.owner) : 'transparent', rv = c.reviewed(r.path);
      const icons = (r.protected ? '<span class="wi deny" title="denied: ' + esc(r.protected.rule) + ' (' + esc(r.protected.origin) + '): ' + esc(r.protected.why) + '">' + LOCK + '</span>' : '') + (r.lease ? '<span class="wi lease" style="color:' + aCol(S, r.lease.agent) + '" title="leased to ' + esc(r.lease.agent) + ' for ' + esc(r.lease.task) + ': ' + esc(r.lease.glob) + '">' + LOCK + '</span>' : '') + (r.ask ? '<span class="wi ask" title="a write here always asks (' + esc(r.ask.rule) + '): ' + esc(r.ask.why) + '">?</span>' : '');
      const rvt = (c.mk.revs[r.path] || []).length;
      return '<button type="button" class="wf' + (sel ? ' sel' : '') + (r.protected ? ' prot' : '') + '" data-file="' + esc(r.path) + '"' + (r.owner ? ' data-ag="' + esc(r.owner) + '"' : '') + (r.task ? ' data-task="' + esc(r.task) + '"' : '') + ' style="--c:' + col + ';padding-left:' + (10 + depth * 14) + 'px"' + (sel ? ' aria-current="true"' : '') + '>' +
        '<span class="wn"' + (r.ignored ? ' title="ignored by the project’s ignore rules"' : r.kind === 'symlink' ? ' title="a symbolic link: this page does not follow it"' : '') + '>' + vt(opt && opt.full ? r.path : r.name) + '</span>' + icons + '<span class="wr">' + (r.writing ? '<span class="warm" title="being written now">✎</span>' : '') + (rvt ? '<span class="tag warm" title="' + plural(rvt, 'hunk') + ' reverted by you">⟲ ' + rvt + '</span>' : '') + (r.status !== '-' ? '<span class="wcnt"><b class="ok">+' + r.add + '</b>' + (r.del ? ' <b class="err">−' + r.del + '</b>' : '') + '</span>' : '') + (r.owner && !(opt && opt.noowner) ? '<span class="wo" style="color:' + col + '">' + vt(r.owner) + '</span>' : '') + (tag ? '<span class="tag ' + tag[1] + '" title="' + tag[2] + '">' + tag[0] + '</span>' : '') + (rv ? '<span class="wrv" title="reviewed">✓</span>' : '') + '</span></button>';
    }
    /** the tree as a flat list of rows: directories (with their owners and change count) and files, in the order they are drawn; collapsed directories hold nothing */
    function treeItems(c) {
      const W2 = c.W, qq = W2.q.trim().toLowerCase(), rows = c.rows.filter(r => (!qq || r.path.toLowerCase().indexOf(qq) >= 0) && (!W2.changed || r.status !== '-'));
      const root = { dirs: {}, files: [] };
      rows.forEach(r => { let n = root, p = ''; if (r.dir) r.dir.split('/').forEach(seg => { p = p ? p + '/' + seg : seg; n = n.dirs[seg] || (n.dirs[seg] = { name: seg, path: p, dirs: {}, files: [] }); }); n.files.push(r); });
      const big = !qq && c.rows.length > BIG_TREE, items = [];
      const stat = d => { let cnt = 0, chg = 0, sel = false; const ow = []; (function g(x) { x.files.forEach(f => { cnt++; if (f.status !== '-') chg++; if (f.path === W2.file) sel = true; if (f.owner && ow.indexOf(f.owner) < 0) ow.push(f.owner); }); Object.keys(x.dirs).forEach(y => g(x.dirs[y])); })(d); return { cnt, chg, sel, ow }; };
      const walk = (n, depth) => {
        Object.keys(n.dirs).sort().forEach(k => {
          const d = n.dirs[k], s = stat(d), set = W2.closed[d.path], closed = !qq && (set !== undefined ? !!set : big && s.cnt > BIG_DIR && !s.chg && !s.sel);
          items.push({ k: 'd', d, depth, closed, chg: s.chg, ow: s.ow });
          if (!closed) walk(d, depth + 1);
        });
        n.files.sort((a, b) => a.name < b.name ? -1 : 1).forEach(r => items.push({ k: 'f', r, depth, restored: !!(c.mk.restore && c.mk.restore.files.indexOf(r.path) >= 0 && r.status === '-') }));
      };
      walk(root, 0); return items;
    }
    function treeItemHtml(c, it) {
      if (it.k === 'f') return fileRow(c, it.r, it.depth, { restored: it.restored });
      return '<button type="button" class="wd" data-dir="' + esc(it.d.path) + '" aria-expanded="' + !it.closed + '" style="padding-left:' + (6 + it.depth * 14) + 'px"><span class="wcar" aria-hidden="true">' + (it.closed ? '▸' : '▾') + '</span><span class="wn">' + vt(it.d.name) + '/</span><span class="wr">' + it.ow.slice(0, 4).map(o => '<i class="wdot" style="background:' + aCol(c.S, o) + '" title="' + esc(o) + '"></i>').join('') + (it.chg ? '<span class="dim num">' + it.chg + '</span>' : '') + '</span></button>';
    }
    /* ---------------- left: changes ---------------- */
    /** the files that changed in the range: against the range's start, who last touched them and what they add up to */
    function changeRows(c) {
      const [a, b] = c.range, ka = ws.pointOf(c.I, a), kb = ws.pointOf(c.I, b), out = [];
      c.rows.forEach(r => {
        const tch = ws.touchesIn(c.I, r.path, ka, kb); if (!tch.length || (ka === 0 && r.status === '-')) return;
        const h = ws.counts(c.I, r.path, a, b), last = tch[tch.length - 1], ea = ws.existsAt(c.I, r.path, ka), eb = ws.existsAt(c.I, r.path, kb);
        out.push(Object.assign({}, r, { add: h.added, del: h.removed, status: !ea && eb ? 'A' : ea && !eb ? 'D' : r.status === '-' ? 'M' : r.status, owner: last.ag || r.owner, task: last.task || r.task, cp: last.id, writers: tch.map(t => t.ag).filter((v, i, x) => x.indexOf(v) === i) }));
      });
      return out;
    }
    /** the Changes list as items: a head per task (or agent), then its files */
    function changeItems(c) {
      const W2 = c.W, rows = changeRows(c).filter(r => !W2.unreviewed || c.mk.reviewed[r.path] !== r.cp); c._changes = rows;
      const grp = {}, order = [];
      rows.forEach(r => { const key = W2.group === 'agent' ? (r.owner || '?') : (r.task || 'no task'); if (!grp[key]) { grp[key] = []; order.push(key); } grp[key].push(r); });
      c.pends.forEach(p => { if (rows.some(r => r.path === p.path)) return; const key = W2.group === 'agent' ? p.ag : (p.task || 'no task'); if (!grp[key]) { grp[key] = []; order.push(key); } const cut = p.path.lastIndexOf('/'); grp[key].unshift({ path: p.path, name: p.path.slice(cut + 1), dir: cut < 0 ? '' : p.path.slice(0, cut), status: 'A', owner: p.ag, task: p.task, add: F.lines(p.text).length, del: 0, writing: true, cp: null }); });
      order.sort((x, y) => x < y ? -1 : 1);
      const items = [];
      order.forEach(key => { const list = grp[key]; items.push({ k: 'g', key, add: list.reduce((s, r) => s + r.add, 0), del: list.reduce((s, r) => s + r.del, 0) }); list.forEach(r => items.push({ k: 'r', r })); });
      if (c.mk.restore) items.push({ k: 'g', rest: true });
      return items;
    }
    function changeItemHtml(c, it) {
      const W2 = c.W;
      if (it.k === 'g') {
        if (it.rest) return '<div class="wg rest"><b class="gid">↺ restored</b><span class="gti">to before ' + esc(c.mk.restore.to) + ': ' + plural(c.mk.restore.files.length, 'file') + '</span></div>';
        const key = it.key; if (W2.group === 'agent') { const A = c.m.ag[key], col = aCol(c.S, key); return '<div class="wg" style="--c:' + col + '" data-ag="' + esc(key) + '"><b class="gid">' + vt(key) + '</b><span class="dim">' + esc(A ? A.role : '') + '</span><span class="wr"><b class="ok">+' + it.add + '</b> <b class="err">−' + it.del + '</b></span></div>'; }
        const T = c.m.tasks[key], col = T && T.owner ? aCol(c.S, T.owner) : 'var(--dim)', fail = !!(T && T.failed && T.st === 'todo'), st = fail ? ['✗ failed', 'err'] : T ? TASK_ST[T.st] || ['', ''] : ['', ''];
        return '<div class="wg" style="--c:' + col + '" data-task="' + esc(key) + '"><b class="gid">' + (T ? vt(key) : '◇') + '</b><span class="gti">' + vt(T ? T.title : 'what the agent changed (a single agent keeps no task board)') + '</span>' + (T ? '<span class="tag ' + st[1] + '" title="verification state of the task">' + st[0] + '</span>' : '') + '<span class="wr"><b class="ok">+' + it.add + '</b> <b class="err">−' + it.del + '</b></span></div>';
      }
      const r = it.r, lid = c.lastId(r.path), rv = lid && c.mk.reviewed[r.path] === lid, tag = ST_TAG[r.status];
      return '<div class="wfr' + (W2.file === r.path ? ' sel' : '') + '"><button type="button" class="wf" data-file="' + esc(r.path) + '"' + (r.owner ? ' data-ag="' + esc(r.owner) + '"' : '') + (r.task ? ' data-task="' + esc(r.task) + '"' : '') + ' style="--c:' + aCol(c.S, r.owner) + '"' + (W2.file === r.path ? ' aria-current="true"' : '') + '><span class="wn"><span class="dim">' + vt(r.dir ? r.dir + '/' : '') + '</span>' + vt(r.name) + '</span><span class="wr">' + (r.writing ? '<span class="warm" title="being written now">✎ writing</span>' : '') + '<span class="wcnt"><b class="ok">+' + r.add + '</b>' + (r.del ? ' <b class="err">−' + r.del + '</b>' : '') + '</span>' + (W2.group === 'task' ? '<span class="wo" style="color:' + aCol(c.S, r.owner) + '">' + vt(r.owner || '') + '</span>' : '') + (tag ? '<span class="tag ' + tag[1] + '" title="' + tag[2] + '">' + tag[0] + '</span>' : '') + '</span></button>' + (r.writing ? '' : '<button type="button" class="wrvb' + (rv ? ' on' : '') + '" data-review="' + esc(r.path) + '" data-cpid="' + esc(lid || '') + '" aria-pressed="' + !!rv + '" title="' + (rv ? 'reviewed: click to unmark' : 'mark as reviewed') + '"><span class="sr">Reviewed: ' + vt(r.path) + '</span>' + (rv ? '✓' : '○') + '</button>') + '</div>';
    }
    /* ---------------- left: checkpoints ---------------- */
    function cpsHtml(c) {
      const I = c.I; let h = ''; const all = I.cps.slice().reverse(), shown = all.slice(0, CPS_PAGE + c.W.cpMore);
      shown.forEach(cp => {
        const sel = c.cur && c.cur.id === cp.id, newest = I.pos.length && I.pos[I.pos.length - 1] === cp;
        h += '<div class="wc' + (sel ? ' sel' : '') + (cp.skipped ? ' skipped' : '') + (cp.safety ? ' safe' : '') + '"' + (cp.agents.length ? ' data-ag="' + esc(cp.agents.join(' ')) + '"' : '') + '><button type="button" class="wcm" data-cp="' + (cp.step ? esc(cp.id) : 'base') + '"' + (cp.step || (cp.id === I.base.id && I.pos.length) ? '' : ' disabled') + (sel || (c.k === 0 && cp.id === I.base.id) ? ' aria-current="true"' : '') + '><b class="cid">' + vt(cp.id) + '</b><span class="ctm num">' + esc(cp.time || '') + '</span><span class="clb">' + vt(cp.label) + '</span><span class="cmeta">' + (newest ? '<em class="newest">● newest</em>' : '') + (cp.skipped ? '<span class="dim">skipped: nothing to put back</span>' : '<span class="num">' + plural(cp.nfiles, 'file') + '</span>' + (cp.added || cp.removed ? '<span class="num"><b class="ok">+' + cp.added + '</b> <b class="err">−' + cp.removed + '</b></span>' : '') + cp.agents.map(a => '<span class="wo" style="color:' + aCol(c.S, a) + '">' + vt(a) + '</span>').join('') + cp.tasks.map(t => '<span class="chip">' + vt(t) + '</span>').join('')) + '</span></button>' +
          (cp.step ? '<span class="cact"><button type="button" class="btn sm" data-cpdiff="' + esc(cp.id) + '">Diff</button><button type="button" class="btn sm" data-restore="' + esc(cp.id) + '">Restore</button></span>' : '') + '</div>';
      });
      if (all.length > shown.length) h += '<div class="ws-help"><button type="button" class="btn sm" data-cpmore>Show older checkpoints (' + (all.length - shown.length) + ')</button></div>';
      h += '<p class="ws-help"><span class="mono">/rewind ID</span> puts every file touched in ID or later back as it was when ID began; the conversation stays. <span class="mono">/diff ID</span> shows that difference. A checkpoint is taken before every restore, so a restore can be undone.</p>';
      return h;
    }
    /* ---------------- left: the worktrees of an isolated team (Merge) ---------------- */
    function stateNote(e, what) {
      if (e.state === 'loading') return '<p class="ws-none">Loading the ' + what + '…</p>';
      if (e.state === 'none') return '<p class="ws-none">' + esc(F.vis(e.message || NO_ISOLATION)) + '</p>';
      if (e.state === 'error') return '<p class="ws-none">' + esc(F.vis(e.message || 'the ' + what + ' could not be read')) + ' <button type="button" class="btn sm" data-retry>Try again</button></p>';
      return '';
    }
    function worktreesHtml(c, e) {
      const note = stateNote(e, 'worktrees'); if (note) return note;
      const list = arr(e.data && e.data.worktrees || e.data);
      if (!list.length) return '<p class="ws-none">No worker has a worktree yet.</p>';
      const S = c.S, m = c.m;
      return list.map(t => {
        const ag = String(t.agent || ''), sel = c.W.wt === ag, A = m.ag[ag], T = A && A.task ? m.tasks[A.task] : null, merged = T && T.st === 'merged', tag = t.dirty ? ['dirty', 'warm'] : merged ? ['merged', 'ok'] : ['clean', ''];
        return '<div class="wc' + (sel ? ' sel' : '') + '" data-ag="' + esc(ag) + '"><button type="button" class="wcm" data-wt="' + esc(ag) + '" style="box-shadow:inset 3px 0 ' + aCol(S, ag) + '"' + (sel ? ' aria-current="true"' : '') + '><b class="cid" style="color:' + aCol(S, ag) + '">' + vt(ag) + '</b><span class="ctm num">' + esc(String(t.head || '').slice(0, 8)) + '</span><span class="clb mono">' + vt(t.branch) + '</span><span class="cmeta">' + (A && A.task ? '<span class="chip" data-task="' + esc(A.task) + '">' + vt(A.task) + '</span>' : '') + '<span class="tag ' + tag[1] + '">' + tag[0] + '</span>' + (arr(t.files).length ? '<span class="num">' + plural(arr(t.files).length, 'file') + '</span>' : '') + '</span></button><span class="cact"><button type="button" class="btn sm" data-copy="' + esc(t.path) + '" title="copy the path of the worktree">Copy path</button></span></div>';
      }).join('');
    }

    /* ---------------- centre: the selected file ---------------- */
    function pickDefault(c) {
      const W2 = c.W; if (W2.file && c.rmap.has(W2.file)) return; const ch = c.rows.filter(r => r.status !== '-'); const cand = W2.tab === 'files' ? c.rows.find(r => r.status !== '-' && !r.protected) || c.rows.find(r => !r.protected) : (changeRows(c)[0] || ch[0]);
      W2.file = cand ? cand.path : null;
    }
    const noneP = (text, extra) => '<p class="ws-none">' + text + (extra || '') + '</p>';
    const retryBtn = ' <button type="button" class="btn sm" data-retry>Try again</button>';
    /** the header of the file pane and what its body shows: html, or a flat diff / a file to draw in a window */
    function bodyOf(c) {
      const W2 = c.W, S = c.S, path = W2.file;
      if (!path) return { head: '<div class="ph"><h2>No file selected</h2></div>', html: noneP('Select a file on the left: its diff, with the agent who wrote each line, shows here.') };
      const r = c.rmap.get(path), a = c.range[0], b = c.range[1], pend = c.pends.find(p => p.path === path) || null, [from, to] = pointsOf(c), isLiveEnd = to === 'live';
      const row = c.I.byPath.get(path) || {}, prot = row.protected, tch = ws.touches(c.I, path, c.all), last = tch[tch.length - 1], owner = r && r.owner, status = pend ? 'A' : ws.statusOf(c.I, path, b);
      const rv = c.reviewed(path), lastCp = ws.lastId(c.I, path);
      const vtask = c.m && c.m.tasks[(last || {}).task], tstate = vtask ? TASK_ST[vtask.st] || ['', ''] : null, tag = ST_TAG[status];
      const wantDiff = !(W2.view === 'file' || (status === '-' && !pend));
      let eNow = null, eDiff = null;
      if (!prot && !pend) { eNow = ws.fileEntry(c.I, path, to); if (wantDiff) eDiff = ws.diffEntry(c.I, path, from, to); }
      const cn = pend ? { added: F.lines(pend.text).length, removed: 0 } : eDiff && eDiff.state === 'ready' ? { added: eDiff.c.added, removed: eDiff.c.removed } : ws.counts(c.I, path, a, b);
      const approx = eNow && eNow.state === 'ready' && eNow.c.exact === false;
      const head = '<div class="ws-ph"><div class="wtop"><h2 class="wpath"><span class="dim">' + vt(path.indexOf('/') >= 0 ? path.slice(0, path.lastIndexOf('/') + 1) : '') + '</span>' + vt(path.slice(path.lastIndexOf('/') + 1)) + '</h2>' + (tag ? '<span class="tag ' + tag[1] + '" title="' + tag[2] + ' against the start of the session">' + tag[0] + '</span>' : '') +
        (owner || pend ? '<span class="chip task" style="--c:' + aCol(S, pend ? pend.ag : owner) + '" data-ag="' + esc(pend ? pend.ag : owner) + '">' + vt(pend ? pend.ag : owner) + '</span>' : '') + ((last && last.task) || (pend && pend.task) ? '<span class="chip" data-task="' + esc(pend ? pend.task : last.task) + '">' + vt(pend ? pend.task : last.task) + '</span>' : '') + (tstate && !pend ? '<span class="tag ' + tstate[1] + '">' + tstate[0] + '</span>' : '') +
        (last ? '<span class="chip" title="the checkpoint of the last change">' + vt(last.id) + '</span>' : '') + '<span class="num wcounts"' + (approx ? ' title="the counts are the real ones; who wrote each line is approximate here: part of this file was changed outside the agents’ tools"' : '') + '><b class="ok">+' + cn.added + '</b> <b class="err">−' + cn.removed + '</b></span></div>' +
        '<div class="wtool"><div class="seg" role="group" aria-label="Show"><button type="button" data-vw="diff" aria-pressed="' + (W2.view === 'diff') + '">Diff</button><button type="button" data-vw="file" aria-pressed="' + (W2.view === 'file') + '">Whole file</button></div>' +
        (lastCp && !pend ? '<button type="button" class="btn sm' + (rv ? ' on' : '') + '" data-review="' + esc(path) + '" data-cpid="' + esc(lastCp) + '" aria-pressed="' + rv + '">' + (rv ? '✓ Reviewed' : '○ Mark reviewed') + '</button>' : '') + '<button type="button" class="btn sm" data-copy="' + esc(path) + '" title="copy the path">Copy path</button><span class="dim wnote">' + (W2.mode === 'step' ? 'this checkpoint only' : W2.mode === 'from' ? 'from the start of ' + vt(c.cur ? c.cur.id : '') + ' to now' : 'since the start of the session') + '</span></div></div>';
      if (prot) return { head, html: '<div class="ws-lock"><div class="lockbig">' + LOCK + '</div><b>Denied path</b><p>Agents cannot read <code>' + vt(path) + '</code>: the rule <code>' + esc(prot.rule) + '</code> (' + esc(prot.origin) + ', ' + esc(prot.tier) + ') applies. ' + esc(prot.why) + '.</p><p class="dim">This page does not show it either: file contents are data from the project, and a denied path has none in this session.</p></div>', pend: null };
      if (row.kind === 'symlink' && !pend) return { head, html: noneP('A symbolic link: this page does not follow it.') };
      if (pend) return { head, pend, mode: wantDiff ? 'diff' : 'file', flat: wantDiff ? F.flatDiff({ hunks: [{ oldStart: 0, oldLines: 0, newStart: 1, newLines: F.lines(pend.text).length, section: '', lines: F.lines(pend.text).map(s => ({ t: '+', s })) }] }, []) : null, ls: F.lines(pend.text), bl: [], ag: pend.ag, caret: pend.done ? -1 : 1, path };
      // the file at the end of the range decides whether there is anything to show
      if (eNow.state === 'loading' && !eNow.c) return { head, html: noneP('Loading the file…') };
      if (eNow.state === 'error') {
        if (eNow.code === 'denied') return { head, html: '<div class="ws-lock"><div class="lockbig">' + LOCK + '</div><b>Denied path</b><p>' + esc(F.vis(eNow.message)) + '</p><p class="dim">This page does not show it either.</p></div>' };
        if (eNow.status === 404 || eNow.code === 'no_file') return { head, html: noneP('This file does not exist at this point in time.') };
        return { head, html: noneP(esc(F.vis(eNow.message || 'the file could not be read')), retryBtn) };
      }
      const cNow = eNow.c;
      if (cNow.binary) return { head, html: noneP('A binary file (' + cNow.size + ' bytes): there is no text to show.') };
      if (wantDiff) {
        if (eDiff.state === 'loading' && !eDiff.c) return { head, html: noneP('Loading the difference…') };
        if (eDiff.state === 'error') return { head, html: noneP(esc(F.vis(eDiff.message || 'the difference could not be read')), retryBtn) };
        const dd = eDiff.c;
        if (dd.binary) return { head, html: noneP('A binary file: there is no line difference to show.') };
        if (!cNow.exists && !arr(dd.hunks).length) return { head, html: noneP('This file does not exist at this point in time.') };
        const revs = isLiveEnd ? (c.mk.revs[path] || []).filter(v => !arr(dd.hunks).some(h => h.oldStart + ':' + h.newStart === v.key)).map(v => Object.assign({ rid: v.id, key: v.key }, F.keyParts(v.key))) : [];
        if (!arr(dd.hunks).length && !revs.length) return { head, html: '<p class="ws-none">No difference in this view. <button type="button" class="btn sm" data-vw="file">Show the whole file</button></p>' };
        const live2 = ws.liveFile(S, c.I), car = ws.editing(c.m, live2, path) && c.k === c.n ? 1 : 0;
        return { head, mode: 'diff', flat: F.flatDiff(dd, revs), bl: ws.blameOf(eNow), owner, caret: car, truncated: !!dd.truncated, canRevert: isLiveEnd, from, to, path };
      }
      if (!cNow.exists) return { head, html: noneP('This file does not exist at this point in time.') };
      return { head, mode: 'file', ls: ws.linesOf(eNow), bl: ws.blameOf(eNow), caret: ws.editing(c.m, ws.liveFile(S, c.I), path) && c.k === c.n ? 1 : 0, truncated: !!cNow.truncated, path };
    }
    /** one row of a diff (see F.flatDiff): the hunk head, a context, added, deleted or elided line */
    function diffRowHtml(c, o, i) {
      const R = o.flat, k = R.kind[i], S = c.S;
      if (k === 0 || k === 5) {
        if (k === 5) { const v = R.reverted[R.hunk[i]]; return '<div class="ln hunk wh"><b class="who"></b><i></i><s></s><span class="hh2">@@ -' + v.oldStart + ' +' + v.newStart + ' @@</span><span class="rvd">⟲ reverted by you</span><button type="button" class="btn sm" data-unrev="' + esc(v.rid) + '" data-key="' + esc(v.key) + '">Undo</button></div>'; }
        const hk = R.hunks[R.hunk[i]], key = hk.oldStart + ':' + hk.newStart;
        return '<div class="ln hunk wh"><b class="who"></b><i></i><s></s><span class="hh2">@@ -' + hk.oldStart + ',' + hk.oldLines + ' +' + hk.newStart + ',' + hk.newLines + ' @@ ' + vt(hk.section || '') + '</span>' + (o.pend ? '' : '<button type="button" class="btn sm" data-rev="' + esc(key) + '"' + (o.canRevert ? ' title="put this hunk back as it was"' : ' disabled title="return to live to revert: a hunk is put back in the file as it is now"') + '>Revert hunk</button>') + '</div>';
      }
      const l = R.hunks[R.hunk[i]].lines[R.line[i]];
      if (k === 4) return '<div class="ln hunk"><b class="who"></b><i></i><s></s><span>… ' + vt(l.s) + '</span></div>';
      if (k === 3) return '<div class="ln del"><b class="who"></b><i>' + R.oldNo[i] + '</i><s>−</s><span>' + hl(l.s) + '</span></div>';
      const nn = R.newNo[i], bi = o.bl && o.bl[nn - 1];
      if (k === 2) {
        const ag = o.pend ? o.ag : (bi && bi.ag !== '-' && bi.ag) || o.owner || '-', lastAdd = R.lastAdd, car = o.caret === 1 && i === lastAdd;
        return '<div class="ln add' + (car ? ' caretline' : '') + '" style="--ag:' + aCol(S, ag) + ';--ag-a:' + aTint(S, ag) + '"><b class="who" data-ag="' + esc(ag === '-' ? '' : ag) + '" title="written by ' + esc(ag === '-' ? '' : ag) + (bi && bi.task ? ' · ' + esc(bi.task) : '') + (bi && bi.id ? ' · ' + esc(bi.id) : '') + '">' + vt(ag === '-' ? '' : ag) + '</b><i>' + nn + '</i><s>+</s><span>' + hl(l.s) + '</span></div>';
      }
      const ob = bi || {};
      return '<div class="ln ctx" style="--ag:' + aCol(S, ob.ag) + '"><b class="who dimw">' + (ob.ag && ob.ag !== '-' ? vt(ob.ag) : '') + '</b><i>' + nn + '</i><s></s><span>' + hl(l.s) + '</span></div>';
    }
    function fileRowHtml(c, o, i) {
      const bl = o.bl, b = bl[i] || {}, prev = i ? bl[i - 1] : null, first = i === 0 || !prev || prev.ag !== b.ag, mine = b.ag && b.ag !== '-', last = i === o.ls.length - 1;
      return '<div class="ln' + (mine ? ' add own' : '') + (o.caret === 1 && last ? ' caretline' : '') + '" style="--ag:' + aCol(c.S, b.ag) + ';--ag-a:' + aTint(c.S, b.ag) + '"><b class="who" ' + (mine ? 'data-ag="' + esc(b.ag) + '" title="written by ' + esc(b.ag) + (b.task ? ' · ' + esc(b.task) : '') + (b.id ? ' · ' + esc(b.id) : '') + '"' : '') + '>' + (first && mine ? vt(b.ag) : '') + '</b><i>' + (i + 1) + '</i><s></s><span>' + hl(o.ls[i]) + '</span></div>';
    }
    const TRUNC = '<div class="ln hunk"><b class="who"></b><i></i><s></s><span>… truncated: the page shows the first part of this file</span></div>';
    const TRUNC_D = '<div class="ln hunk"><b class="who"></b><i></i><s></s><span>… the difference is truncated: the page shows its first part</span></div>';
    /** draw the body of the file pane: short ones in full (the markup of the mock), long ones as a window */
    function paintBody(c, o) {
      if (o.html != null) { VB.off(); return put(body, o.html); }
      const R = o.flat; let n, rowHtml, kindOf, pinOf = null; const tail = o.truncated ? (o.mode === 'diff' ? TRUNC_D : TRUNC) : '';
      if (o.mode === 'diff') {
        let la = -1; const lh = R.n ? R.head[R.n - 1] : -1; for (let i = R.n - 1; i >= 0 && R.head[i] === lh; i--) if (R.kind[i] === 2) { la = i; break; } R.lastAdd = la;
        n = R.n; rowHtml = i => diffRowHtml(c, o, i); kindOf = i => (R.kind[i] === 0 || R.kind[i] === 5 ? 'h' : 'l'); pinOf = i => R.head[i];
      } else { n = o.ls.length; rowHtml = i => fileRowHtml(c, o, i); kindOf = () => 'l'; }
      const cls = 'diff wdiff' + (o.mode === 'file' ? ' whole' : '');
      if (n <= WIN_DIFF) { VB.off(); let h = ''; for (let i = 0; i < n; i++) h += rowHtml(i); return put(body, '<div class="' + cls + '">' + h + tail + '</div>'); }
      if (body._h !== 'win' || !$('.wdiff', body)) { body.innerHTML = '<div class="' + cls + '"></div>'; body.scrollTop = 0; }
      $('.wdiff', body).className = cls; VB.set(n, rowHtml, kindOf, pinOf, tail); VB.draw(true); body._h = 'win'; return true;
    }

    /* ---------------- the merge queue and the verify output (Merge tab) ---------------- */
    const phaseTag = p => ({ rebase: ['rebase', 'warm'], rebasing: ['rebase', 'warm'], verifying: ['verifying', 'warm'], verified: ['verified', 'ok'], merging: ['merging', 'warm'], merged: ['merged', 'ok'] }[String(p || '')] || [String(p || 'queued'), '']);
    function queueRows(c, d) {
      const m = c.m, T = id => m.tasks[id], rows = [];
      const row = (task, step, ag, commit, files, el) => '<tr data-task="' + esc(task) + '"' + (ag ? ' data-ag="' + esc(ag) + '"' : '') + '><td class="bright mono">' + vt(task) + '</td><td>' + step + '</td><td style="color:' + aCol(c.S, ag) + ';font-weight:700">' + vt(ag || '') + '</td><td class="mono dim">' + esc(String(commit || '').slice(0, 10)) + '</td><td class="r num">' + (files == null ? '' : files) + '</td><td class="r num dim">' + esc(el || '') + '</td></tr>';
      const qh = m.qHead;
      if (d.active) { const t = T(d.active), pt = phaseTag(d.phase || (qh && qh.step)); rows.push(row(d.active, '<span class="tag ' + pt[1] + '">' + esc(pt[0]) + '</span>', t && t.owner, '', null, qh && qh.task === d.active ? mmss(Math.max(0, c.S.vt - qh.t0)) : '')); }
      arr(d.waiting).forEach(w => { const t = T(w); rows.push(row(w, '<span class="tag">waiting</span>', t && t.owner, '', null, '')); });
      arr(d.landed).slice().reverse().forEach(l => { const t = T(l.task); rows.push(row(l.task, '<span class="tag ok">merged</span>', l.agent || (t && t.owner), l.commit, arr(l.files).length, t && t.ms ? (t.ms < 1000 ? t.ms + 'ms' : (t.ms / 1000).toFixed(1) + 's') : '')); });
      return rows.join('');
    }
    /** the Merge tab: head, body and the left list for the current choice */
    function mergeView(c) {
      const S = c.S, W2 = c.W, isol = (c.I && c.I.isolation ? c.I.isolation : S.meta.isolation) === 'worktree';
      const qe = ws.merge.queue(S), we = ws.merge.worktrees(S), d = qe.data, m = c.m;
      const left = worktreesHtml(c, we);
      if (!isol || qe.state === 'none') return { left, head: '<div class="ws-ph"><div class="wtop"><h2 class="wpath">Merge queue</h2></div></div>', body: noneP(esc(NO_ISOLATION)), note: '' };
      const tabs = '<div class="seg" role="group" aria-label="Show"><button type="button" data-mv="queue" aria-pressed="' + (W2.mview === 'queue') + '">Queue</button><button type="button" data-mv="verify" aria-pressed="' + (W2.mview === 'verify') + '">Verify output</button></div>';
      const meta = d ? '<span class="tag ' + (d.healthy ? 'ok' : 'err') + '" title="' + (d.healthy ? 'the integration branch builds' : 'the integration branch does not verify') + '">' + (d.healthy ? 'healthy' : 'broken') + '</span><span class="chip mono" title="the integration branch">' + vt(d.branch) + '</span><span class="chip mono" title="its tip">' + esc(String(d.tip || '').slice(0, 10)) + '</span>' : '';
      const head = '<div class="ws-ph"><div class="wtop"><h2 class="wpath">' + (W2.mview === 'verify' ? 'Verify output' : 'Merge queue') + '</h2>' + meta + '<span class="num wcounts" title="merged / conflicts / bounced">merged <b class="ok">' + m.merged.length + '</b> · conflicts <b class="' + (m.conflicts ? 'err' : '') + '">' + m.conflicts + '</b> · bounced <b class="' + (m.bounced ? 'err' : '') + '">' + m.bounced + '</b></span></div><div class="wtool">' + tabs + '<span class="dim wnote">' + esc(F.vis(S.meta.verify ? '--verify "' + S.meta.verify + '"' : 'no verify command')) + '</span></div></div>';
      if (W2.mview === 'verify') {
        const tasks = m.torder, cur = W2.mtask && m.tasks[W2.mtask] ? W2.mtask : (m.qHead && m.qHead.task !== 'goal' ? m.qHead.task : tasks[0]);
        const chips = tasks.map(t => { const T = m.tasks[t], st = TASK_ST[T.st] || ['', '']; return '<button type="button" class="wtk" data-vtask="' + esc(t) + '" data-task="' + esc(t) + '" style="--c:' + aCol(S, T.owner) + '" aria-pressed="' + (cur === t) + '" title="' + esc(t + ' ' + T.title) + '"><b>' + vt(t) + '</b><span class="' + st[1] + '" aria-hidden="true">' + st[0].split(' ')[0] + '</span></button>'; }).join('');
        let out = '';
        if (!cur) out = noneP('No task has been submitted yet.');
        else {
          const ve = ws.merge.verify(S, cur), v = ve.data;
          if (ve.state === 'loading') out = noneP('Loading the verify output…');
          else if (ve.state === 'none') out = noneP('No verification has run for ' + vt(cur) + ' yet.');
          else if (ve.state === 'error') out = noneP(esc(F.vis(ve.message || 'the output could not be read')), retryBtn);
          else out = verifyHtml(v, W2.mattempt);
        }
        return { left, head, body: '<div class="wtks" style="padding:8px 12px;max-height:none;justify-content:flex-start" role="group" aria-label="Task">' + chips + '</div>' + out, note: '' };
      }
      let body;
      if (qe.state === 'loading') body = noneP('Loading the merge queue…');
      else if (qe.state === 'error') body = noneP(esc(F.vis(qe.message || 'the merge queue could not be read')), retryBtn);
      else { const rows = queueRows(c, d); body = rows ? '<table class="tbl"><thead><tr><th>task</th><th>step</th><th>agent</th><th>commit</th><th class="r">files</th><th class="r">elapsed</th></tr></thead><tbody>' + rows + '</tbody></table>' : noneP('The queue is empty: the next submission is rebased, then verified.'); }
      return { left, head, body, note: '' };
    }
    /** the gate runs of a task (one row per attempt) and the output of the one chosen, in the log pane of Schedule */
    function verifyHtml(v, want) {
      const runs = v.runs, sel = want == null ? runs.length - 1 : Math.max(0, Math.min(runs.length - 1, want));
      const lines = v.runs[sel].out.split('\n'), cut = Math.max(0, lines.length - 2000), shown = lines.slice(cut);
      const tbl = '<table class="tbl"><thead><tr><th>attempt</th><th>command</th><th>exit</th><th class="r">took</th><th>at</th><th></th></tr></thead><tbody>' + runs.map((r, i) => '<tr' + (i === sel ? ' class="sel"' : '') + '><td class="num">' + (runs.length > 1 ? '<button type="button" class="btn sm" data-attempt="' + i + '" aria-pressed="' + (i === sel) + '">' + r.attempt + '</button>' : r.attempt) + '</td><td class="mono bright">$ ' + vt(v.cmd) + '</td><td class="num">' + r.exit + '</td><td class="r num dim">' + (r.ms ? (r.ms < 1000 ? r.ms + 'ms' : (r.ms / 1000).toFixed(1) + 's') : '') + '</td><td class="num dim">' + esc(r.at) + '</td><td><span class="tag ' + (r.exit === 0 && !r.timedOut ? 'ok' : 'err') + '">' + (r.timedOut ? 'timed out' : r.exit === 0 ? 'ok' : 'failed') + '</span></td></tr>').join('') + '</tbody></table>';
      return '<div class="wcalls">' + tbl + '</div><div class="slog"><div class="term" tabindex="0"><div class="tl hd"><span class="prompt">$</span> ' + vt(v.cmd) + '</div>' + (cut ? '<div class="tl dim">… ' + cut + ' earlier lines not shown</div>' : '') + shown.map(l => '<div class="tl' + (/\b(FAIL|panic|error)\b/i.test(l) ? ' err' : '') + '">' + vt(F.clip(l, 4096).t) + '</div>').join('') + (runs[sel].truncated ? '<div class="tl dim">… the output is truncated: the start and the end are kept</div>' : '') + '</div></div>';
    }

    /* ---------------- bottom: the verify / merge strip ---------------- */
    function stripHtml(c) {
      const m = c.m, S = c.S, qh = m.qHead, merged = m.merged.slice().sort(), isGoal = qh && qh.task === 'goal';
      const chips = m.torder.map(t => { const T = m.tasks[t], failed = !!T.failed && T.st === 'todo', st = failed ? ['✗ failed', 'err'] : TASK_ST[T.st] || ['', ''], blocked = T.st === 'running' && hasOpenAsk(m, t); return '<button type="button" class="wtk" data-taskf="' + esc(t) + '" data-task="' + esc(t) + '" style="--c:' + aCol(S, T.owner) + '" aria-pressed="' + (c.W.task === t) + '" title="' + esc(t + ' ' + T.title + ' · ' + (T.owner || '') + ' · ' + (blocked ? 'asks you' : st[0]) + (failed && T.closure && T.closure.kind ? ' (' + T.closure.kind + ')' : '') + ' (click: show only its changes; alt-click: its verify output)') + '"><b>' + vt(t) + '</b><span class="' + (blocked ? 'warm' : st[1]) + '" aria-hidden="true">' + (blocked ? '?' : st[0].split(' ')[0]) + '</span><span class="sr">' + esc(t + ' ' + (blocked ? 'asks you' : st[0])) + '</span></button>'; }).join('');
      const chg = c.I ? changeRows(c) : derived(S).map(r => ({ path: r.path })), rvd = c.I ? chg.filter(r => c.lastId(r.path) && c.mk.reviewed[r.path] === c.lastId(r.path)).length : 0;
      const isol = (c.I && c.I.isolation ? c.I.isolation : S.meta.isolation) === 'worktree', gate = S.meta.verify ? merged.length + m.bounced : 0;
      let accept = '';
      if (isol) { ws.merge.queue(S); accept = '<button type="button" class="btn sm pri" data-accept' + (ws.acceptWaiting(S) ? '' : ' disabled title="nothing verified is waiting"') + '>Apply verified work…</button>'; }
      return '<div class="ws-sh"><h2>Verify and merge</h2><span class="mono dim">--verify "' + esc(F.vis(S.meta.verify || '(none)')) + '"</span>' + accept + '</div>' +
        '<div class="ws-sb"><div class="wq">' + (qh ? '<div class="wqh"><span class="tri">▸</span><b style="color:' + (isGoal ? 'var(--mgr)' : aCol(S, (m.tasks[qh.task] || {}).owner)) + '">' + (isGoal ? '◇ goal' : vt(qh.task)) + '</b>' + (isGoal ? '<span>all merged</span>' : '<span>rebase <span class="ok">✓</span></span>') + '<span class="dim">·</span><span>' + (qh.step === 'verified' ? 'verified' : 'verifying') + '</span><span class="mono cmd">' + vt(qh.cmd) + '</span></div><div class="qbar" aria-hidden="true">' + (qh.step === 'verified' ? '<i style="width:100%;left:0;animation:none;background:var(--ok)"></i>' : '<i></i>') + '</div>' : '<div class="wqh dim"><span>▹ queue empty</span><span>·</span><span>the next submission is rebased, then verified</span></div><div class="qbar" aria-hidden="true"></div>') +
        '</div><div class="wtks" role="group" aria-label="Tasks: click to show only its changes">' + chips + '</div><div class="wmeta"><span>merged <b>' + merged.length + '</b>/' + m.torder.length + '</span><span>conflicts <b>' + m.conflicts + '</b></span><span>bounced <b>' + m.bounced + '</b>' + (gate ? ' of <b>' + gate + '</b> gate runs' : '') + '</span><span>reviewed <b>' + rvd + '</b>/' + chg.length + '</span></div></div>';
    }

    /* ---------------- paint ---------------- */
    /** a short string that changes when anything the section draws changed: the section is drawn only then */
    function sigOf(c, extra) {
      const m = c.m, W2 = c.W, tk = m ? m.torder.map(t => t + m.tasks[t].st).join(',') : '';
      return [c.S.id, c.I && c.I.key, W2.tab, c.k, W2.cp, W2.mode, W2.view, W2.group, W2.q, W2.changed, W2.unreviewed, W2.task, W2.file, W2.mview, W2.mtask, W2.mattempt, W2.wt, W2.cpMore, W2.tab === 'merge' ? Math.floor(c.S.vt) : '', JSON.stringify(W2.closed), c.mk && c.mk.stamp, tk, c.pends && c.pends.map(p => p.path + p.text.length).join(','), SL.G ? SL.G.ver : 0, extra].join('|');
    }
    function paint() {
      const c = ctx(), S = c.S, W2 = c.W; const wsv = q('.wsv'); wsv.dataset.tab = W2.tab;
      $$('.ws-tabs [data-tab]', root).forEach(b => { b.setAttribute('aria-selected', String(b.dataset.tab === W2.tab)); b.setAttribute('aria-pressed', String(b.dataset.tab === W2.tab)); });
      if (!c.I) return paintDerived(c);
      const sg = sigOf(c, c.m.qHead ? c.m.qHead.task + c.m.qHead.step : '') + '|' + c.m.merged.length + c.m.conflicts + c.m.bounced + '|' + c.m.qs.filter(x => !x.answered).length + '|' + S.meta.verify + '|' + (S._wsd.merge.queue ? S._wsd.merge.queue.at : 0) + '|' + (S._wsd.merge.worktrees ? S._wsd.merge.worktrees.at : 0) + '|' + Object.keys(S._wsd.merge).map(k => S._wsd.merge[k].at).join(',');
      if (H.sig.all === sg && !H.typed) return; H.sig.all = sg;
      pickDefault(c);
      /* scrubber */
      const n = c.n; scrub.max = String(Math.max(1, n)); scrub.min = '0'; if (+scrub.value !== c.k && document.activeElement !== scrub) scrub.value = String(c.k); scrub.disabled = !n;
      const every = n > 24 ? Math.ceil(n / 14) : 1;
      const tk = ['<span class="tk" style="left:0">' + vt(c.I.base.id) + '</span>'].concat(c.I.pos.map((p, i) => (i + 1 === c.k || (i + 1) % every === 0 || i + 1 === n) ? '<span class="tk' + (i + 1 === c.k ? ' on' : '') + '" style="left:' + ((i + 1) / Math.max(1, n) * 100) + '%">' + vt(p.id) + '</span>' : '')).join(''); put(ticks, tk);
      at.textContent = c.cur ? F.vis(c.cur.id + ' · ' + c.cur.time + ' · ' + c.cur.label) : F.vis(c.I.base.id + ' · ' + c.I.base.label); live.hidden = !c.pinned; scrub.setAttribute('aria-valuetext', c.cur ? F.vis(c.cur.id + ' ' + c.cur.label) : 'the base: ' + F.vis(c.I.base.label));
      const tot = ws.rows(S, c.I, c.steps).filter(r => r.status !== '-'); let ad = 0, de = 0; tot.forEach(r => { ad += r.add; de += r.del; });
      sum.innerHTML = '<span class="chip">' + plural(tot.length, 'file') + ' changed</span><span class="num"><b class="ok">+' + ad + '</b> <b class="err">−' + de + '</b></span><span class="dim">' + esc((c.I.isolation || S.meta.isolation) === 'worktree' ? 'each worker in its own worktree' : 'one shared checkout') + '</span>';
      /* left */
      const lh = { files: 'Files', changes: 'Changes', checkpoints: 'Checkpoints', merge: 'Worktrees' }[W2.tab]; q('.ws-lh').textContent = lh;
      let ch = '', rcount = '', mergeV = null;
      if (W2.tab === 'files') {
        ch = '<input class="wq-in" type="search" placeholder="filter paths" aria-label="Filter paths" autocomplete="off"><label class="chk"><input type="checkbox" data-opt="changed"' + (W2.changed ? ' checked' : '') + '> changed only</label>';
        rcount = '<span>' + c.rows.length + ' files</span>';
        const items = treeItems(c); put(ctl, ch); const fi = $('.wq-in', ctl); if (fi && fi.value !== W2.q) fi.value = W2.q;   // the typed text is the input's own: it keeps its focus and caret
        paintList(c, items, it => treeItemHtml(c, it), it => it.k, false, '<p class="ws-none">No file matches.</p>');
      } else if (W2.tab === 'changes') {
        ch = '<div class="seg" role="group" aria-label="Group by"><button type="button" data-grp="task" aria-pressed="' + (W2.group === 'task') + '">by task</button><button type="button" data-grp="agent" aria-pressed="' + (W2.group === 'agent') + '">by agent</button></div><div class="seg" role="group" aria-label="Range"><button type="button" data-mode="since" aria-pressed="' + (W2.mode === 'since') + '" title="everything since the base, up to the point in time">since start</button><button type="button" data-mode="step" aria-pressed="' + (W2.mode === 'step') + '" title="only the checkpoint at the point in time"' + (c.cur ? '' : ' disabled') + '>only ' + vt(c.cur ? c.cur.id : '·') + '</button><button type="button" data-mode="from" aria-pressed="' + (W2.mode === 'from') + '" title="/diff ID: from the start of that checkpoint to now"' + (c.cur ? '' : ' disabled') + '>' + vt(c.cur ? c.cur.id : '·') + ' to now</button></div><label class="chk"><input type="checkbox" data-opt="unreviewed"' + (W2.unreviewed ? ' checked' : '') + '> unreviewed</label>';
        const items = changeItems(c), nChg = c._changes.length; rcount = '<span>' + plural(nChg, 'file') + '</span>'; put(ctl, ch);
        paintList(c, items, it => changeItemHtml(c, it), it => it.k === 'g' ? 'g' : 'r', true, '<p class="ws-none">' + (W2.unreviewed ? 'Everything here is reviewed.' : 'No file changed in this view.') + '</p>');
      } else if (W2.tab === 'checkpoints') { put(ctl, ''); VL.off(); put(left, cpsHtml(c)); rcount = '<span>' + c.I.cps.length + ' · ' + c.I.pos.length + ' with files</span>'; }
      else { mergeV = mergeView(c); put(ctl, ''); VL.off(); put(left, mergeV.left); rcount = '<span>' + arr(ws.merge.worktrees(S).data && ws.merge.worktrees(S).data.worktrees || ws.merge.worktrees(S).data).length + '</span>'; }
      q('.ws-lr').innerHTML = rcount;
      /* centre */
      if (mergeV) { H.typed = false; VB.off(); put(fh, mergeV.head); put(body, mergeV.body); note.hidden = true; put(note, ''); }
      else {
        const o = bodyOf(c); H.typed = !!(o.pend && !o.pend.done) || (!!o.caret && o.caret === 1);
        put(fh, o.head); const bt = body.scrollTop, changedBody = paintBody(c, o); if (changedBody && H.typed) body.scrollTop = body.scrollHeight; else if (changedBody && !VB.on) body.scrollTop = bt;
        /* notes */
        let nt = '';
        if (c.pinned) nt += '<div class="wnr tt"><span><b>Time travel</b> · the project as it was after <b class="mono">' + vt(c.cur ? c.cur.id : c.I.base.id) + '</b>' + (c.cur ? ' (' + esc(c.cur.time) + ')' : '') + '; the session is at <b class="mono">' + vt(c.I.pos[c.n - 1].id) + '</b>.</span><button type="button" class="btn sm" data-golive>Return to live</button></div>';
        if (c.mk.restore) nt += '<div class="wnr rs"><span><b>↺ Restored</b> · ' + plural(c.mk.restore.files.length, 'file') + ' put back to before <b class="mono">' + vt(c.mk.restore.to) + '</b>; a safety checkpoint <b class="mono">' + vt(c.mk.restore.safety) + '</b> was taken first.</span><button type="button" class="btn sm" data-undorestore>Undo the restore</button></div>';
        note.hidden = !nt; put(note, nt);
      }
      put(strip, stripHtml(c));
    }
    /** the left list: short ones whole, long ones as a window over `items` */
    function paintList(c, items, htmlOf, kindOf, grouped, empty) {
      if (!items.length) { VL.off(); put(left, empty); return; }
      if (items.length <= WIN_LIST) { VL.off(); put(left, items.map(htmlOf).join('')); return; }
      const key = c.W.tab + '|' + c.S.id; if (left._wsKey !== key) { left._wsKey = key; left.scrollTop = 0; }
      let pin = null;
      if (grouped) { const gi = new Int32Array(items.length); let g = -1; items.forEach((it, i) => { if (it.k === 'g' && !it.rest) g = i; gi[i] = g; }); pin = i => gi[i]; }
      VL.set(items.length, i => htmlOf(items[i]), i => kindOf(items[i]), pin, ''); VL.draw(true); left._h = 'win';
    }
    /** while the history loads or cannot be read: the files the tool calls touched, as a list; the transcript is the record */
    const err0 = st => st.state === 'error' || st.state === 'none';
    function paintDerived(c) {
      const rows = derived(c.S), st = c.st || ws.status(c.S); H.sig.all = null; VL.off(); VB.off(); scrub.disabled = true; live.hidden = true; at.textContent = err0(st) ? 'history not readable' : 'loading the history'; put(ticks, '');
      const err = st.state === 'error' || st.state === 'none', msg = err ? esc(F.vis(st.message || 'the workspace could not be read')) : LOADING;
      sum.innerHTML = '<span class="chip">' + plural(rows.length, 'file') + ' touched</span><span class="dim">' + (err ? (st.state === 'none' ? 'no history for this session' : 'the history is not readable') : 'loading the history') + '</span>';
      q('.ws-lh').textContent = { files: 'Files', changes: 'Changes', checkpoints: 'Checkpoints', merge: 'Worktrees' }[c.W.tab]; q('.ws-lr').innerHTML = '<span>' + rows.length + '</span>'; put(ctl, '');
      put(left, c.W.tab === 'checkpoints' ? ((c.m && c.m.ckpts.length ? c.m.ckpts.map(k => '<div class="wc"><span class="wcm"><b class="cid">' + vt(k.id) + '</b><span class="ctm num">' + esc(k.ts || '') + '</span><span class="clb">' + vt(k.note) + '</span><span class="cmeta"><span class="num">' + plural(k.files, 'file') + '</span></span></span></div>').join('') : '<p class="ws-none">No checkpoint yet: the first turn that writes a file takes one.</p>')) : rows.length ? rows.map(r => '<div class="wfr' + (c.W.file === r.path ? ' sel' : '') + '"><button type="button" class="wf" data-ag="' + esc(r.ag) + '" data-file="' + esc(r.path) + '" style="--c:' + aCol(c.S, r.ag) + '"><span class="wn">' + vt(r.path) + '</span><span class="wr"><span class="wcnt"><b class="ok">+' + r.add + '</b> <b class="err">−' + r.del + '</b></span><span class="wo" style="color:' + aCol(c.S, r.ag) + '">' + vt(r.ag) + '</span></span></button></div>').join('') : '<p class="ws-none">' + (err ? msg + (st.state === 'error' ? retryBtn : '') : LOADING) + '</p>');
      const pick = c.W.file && rows.some(r => r.path === c.W.file) ? c.W.file : (rows[0] && rows[0].path), calls = []; if (pick && c.m) c.m.order.forEach(id => (c.m.chan[id] || []).forEach(e => { if (e.k === 'tool' && e.file === pick && !e.refused) calls.push({ id, e }); })); calls.sort((a, b) => a.e.t - b.e.t);
      put(fh, '<div class="ws-ph"><div class="wtop"><h2 class="wpath">' + (pick ? vt(pick) : vt(c.S.name)) + '</h2></div></div>');
      put(body, (pick ? '<div class="wcalls"><h3 class="lab">what the agents did to this file</h3><table class="tbl"><thead><tr><th>time</th><th>agent</th><th>tool</th><th class="r">lines</th><th>result</th></tr></thead><tbody>' + calls.map(({ id, e }) => '<tr data-ag="' + esc(id) + '"><td class="num dim">' + (SL.u.todAt ? SL.u.todAt(c.S, e) : U.tod(c.S.meta.t0, e.t)) + '</td><td style="color:' + aCol(c.S, id) + ';font-weight:700">' + vt(id) + '</td><td class="bright mono">' + vt(e.name) + '</td><td class="r num"><b class="ok">+' + (e.add || 0) + '</b> <b class="err">−' + (e.del || 0) + '</b></td><td class="dim">' + vt(e.out || '') + '</td></tr>').join('') + '</tbody></table></div>' : '') + '<p class="ws-none">' + (err ? msg + (st.state === 'error' ? retryBtn : '') : LOADING) + '</p>'); note.hidden = true;
      put(strip, c.m ? stripHtml(c) : '');
    }

    /* ---------------- input ---------------- */
    const setW = (patch, scroll) => { const S = SL.sessions.active, W2 = wsOf(S); Object.assign(W2, patch); S.touch(); H.sig.all = null; paint(); if (scroll) body.scrollTop = 0; };
    const retry = () => { const S = SL.sessions.active, I = ws.info(S); if (I) { const D = ws.dataOf(S); D.lru.forEach((e, k) => { if (e.state === 'error') D.lru.delete(k); }); D.merge = {}; } ws.refresh(S); H.sig.all = null; SL.loop.dirty = true; };
    sc.listen(root, 'click', e => {
      const t = e.target, S = SL.sessions.active, c = ctx(), W2 = c.W;
      const tab = t.closest('.ws-tabs [data-tab]'); if (tab) { ui.ws.setTab(tab.dataset.tab); return; }
      if (t.closest('[data-retry]')) { retry(); return; }
      const d = t.closest('[data-dir]'); if (d) { W2.closed[d.dataset.dir] = d.getAttribute('aria-expanded') === 'true'; S.touch(); H.sig.all = null; paint(); return; }
      const rev = t.closest('[data-review]'); if (rev) { if (c.I) SL.act.markReviewed(rev.dataset.review, rev.dataset.cpid, rev.getAttribute('aria-pressed') !== 'true'); return; }
      const acc = t.closest('[data-accept]'); if (acc) { if (!acc.disabled) acceptDialog(S); return; }
      const f = t.closest('[data-file]'); if (f && !t.closest('[data-rev],[data-unrev],[data-copy]')) { setW({ file: f.dataset.file, view: W2.view }, true); return; }
      const vw = t.closest('[data-vw]'); if (vw) { setW({ view: vw.dataset.vw }); return; }
      const gp = t.closest('[data-grp]'); if (gp) { setW({ group: gp.dataset.grp }); return; }
      const md = t.closest('[data-mode]'); if (md) { setW({ mode: md.dataset.mode }); return; }
      const cpd = t.closest('[data-cpdiff]'); if (cpd) { if (!c.I) return; setW({ tab: 'changes', cp: c.n && c.I.pos[c.n - 1].id === cpd.dataset.cpdiff ? null : cpd.dataset.cpdiff, mode: 'from', file: null }); return; }
      const rst = t.closest('[data-restore]'); if (rst) { restoreDialog(SL.sessions.active, rst.dataset.restore); return; }
      if (t.closest('[data-cpmore]')) { setW({ cpMore: W2.cpMore + CPS_PAGE }); return; }
      const wt = t.closest('[data-wt]'); if (wt) { setW({ wt: wt.dataset.wt === W2.wt ? null : wt.dataset.wt }); return; }
      const mv = t.closest('[data-mv]'); if (mv) { setW({ mview: mv.dataset.mv }); return; }
      const vtk = t.closest('[data-vtask]'); if (vtk) { setW({ mtask: vtk.dataset.vtask, mattempt: null }); return; }
      const att = t.closest('[data-attempt]'); if (att) { setW({ mattempt: +att.dataset.attempt }); return; }
      const cp = t.closest('[data-cp]'); if (cp) { if (!c.I) return; const last = c.I.pos[c.n - 1]; setW({ cp: last && last.id === cp.dataset.cp ? null : cp.dataset.cp }); return; }
      const tk = t.closest('[data-taskf]'); if (tk) { if (e.altKey) { setW({ tab: 'merge', mview: 'verify', mtask: tk.dataset.taskf }); return; } const on = W2.task !== tk.dataset.taskf; W2.task = on ? tk.dataset.taskf : null; if (on) { Object.assign(W2, { tab: 'changes', group: 'task' }); } S.touch(); H.sig.all = null; paint(); if (on) { const first = c.I ? changeRows(ctx()).find(r => r.task === tk.dataset.taskf) : null; if (first) setW({ file: first.path }); } return; }
      const rv = t.closest('[data-rev]'); if (rv) { revertDialog(rv.dataset.rev); return; }
      const ur = t.closest('[data-unrev]'); if (ur) { SL.act.unrevertHunk(W2.file, ur.dataset.key, undefined, { rid: ur.dataset.unrev }); return; }
      const cpy = t.closest('[data-copy]'); if (cpy) { U.copy(cpy.dataset.copy).then(ok => ui.toast(ok ? 'path copied' : 'copy is not available here', ok ? 'ok' : 'warm')); return; }
      if (t.closest('[data-golive]') || t.closest('.ws-live')) { setW({ cp: null }); return; }
      if (t.closest('[data-undorestore]')) { SL.act.undoRewind(); return; }
    });
    sc.listen(root, 'input', e => { const t = e.target; if (t.id === 'wsScrub') { const c = ctx(), k = +t.value; if (!c.I) return; setW({ cp: k >= c.n ? null : k === 0 ? 'base' : c.I.pos[k - 1].id }); } else if (t.classList.contains('wq-in')) { const S = SL.sessions.active; wsOf(S).q = t.value; H.sig.all = null; paint(); } });
    sc.listen(root, 'change', e => { const t = e.target; if (t.dataset && t.dataset.opt) { setW({ [t.dataset.opt]: t.checked }); } });
    sc.listen(root, 'keydown', e => {
      const t = e.target; if (!t.closest || !t.closest('.ws-left')) return;
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        const step = e.key === 'ArrowDown' ? 1 : -1;
        const row = VL.on && t.closest ? t.closest('[data-vi]') : null;
        if (row) {   // a long list: the next row may not be drawn yet
          let i = +row.dataset.vi + step; const ok = k => k === 'f' || k === 'd' || k === 'r'; while (i >= 0 && i < VL.items && !ok(VL.kindOf(i))) i += step;
          if (i >= 0 && i < VL.items) { e.preventDefault(); VL.reveal(i); const n = left.querySelector('[data-vi="' + i + '"]'), f = n && (n.matches('.wf,.wd') ? n : n.querySelector('.wf')); if (f) f.focus({ preventScroll: true }); }
          return;
        }
        const bs = $$('.ws-list .wf, .ws-list .wd, .ws-list .wcm:not([disabled])', root), i = bs.indexOf(t);
        if (i >= 0) { e.preventDefault(); const n = bs[Math.max(0, Math.min(bs.length - 1, i + step))]; n.focus(); } else if (t.classList.contains('wq-in') && e.key === 'ArrowDown') { const f = bs[0]; if (f) { e.preventDefault(); f.focus(); } }
      }
    });
    scrub.addEventListener('keydown', () => { SL.time.noteKey(); });

    /* ---------------- dialogs: hunk revert ---------------- */
    function revertDialog(key) {
      const c = ctx(); if (!c.I) return; const path = c.W.file, [from, to] = pointsOf(c), h = ws.hunks(c.I, path, c.range[0], c.range[1]), hk = h.hunks.find(x => x.oldStart + ':' + x.newStart === key); if (!hk) return;
      const pv = hk.lines.filter(l => l.t !== ' ' && l.t !== '…').slice(0, 14).map(l => '<div class="ln ' + (l.t === '+' ? 'del' : 'add') + '"><b class="who"></b><i></i><s>' + (l.t === '+' ? '−' : '+') + '</s><span>' + hl(l.s) + '</span></div>').join('');
      ui.modal({ title: 'Revert this hunk', kicker: F.vis(path).slice(0, 60), desc: 'put these lines back as they were before', color: 'var(--rv)', focus: '[data-no]', body: '<p class="stubnote">The hunk at line ' + hk.newStart + ' of <b class="mono">' + vt(path) + '</b> goes back to its earlier text. What the revert would do, line by line:</p><div class="diff wdiff">' + pv + '</div><div class="row2"><button class="btn danger" type="button" data-ok>Revert the hunk</button><button class="btn" type="button" data-no>Cancel</button></div>',
        onMount(b, scm, close) { scm.listen($('[data-ok]', b), 'click', () => { close(); SL.act.revertHunk(path, key, undefined, { from, to }); }); scm.listen($('[data-no]', b), 'click', close); } });
    }

    /* ---------------- live: typing caret and the update hook ---------------- */
    sc.update(() => paint());
    let acc = 0;
    sc.frame((dt, vtm, S2) => { if (!H.typed) return; acc += dt; if (acc < 0.12) return; acc = 0; const c = ctx(); if (!c.I) return; const o = bodyOf(c); H.typed = !!(o.pend && !o.pend.done) || o.caret === 1; if (paintBody(c, o)) body.scrollTop = body.scrollHeight; if (c.W.tab === 'changes') { const items = changeItems(c); if (items.length <= WIN_LIST) put(left, items.map(it => changeItemHtml(c, it)).join('')); } });
    paint();
  }
  SL.views.register({ name: 'workspace', title: 'Workspace', nav: true, mount });
  /** the Workspace's API for the rest of the page (rail, chat rows, palette, drawer): it only sets the session's own view state; the mounted view repaints from it */
  ui.ws = {
    counts, derived,
    setTab(t) { const S = SL.sessions.active; if (!S) return; wsOf(S).tab = t; S.touch(); },
    open(path, tab) { const S = SL.sessions.active; if (!S) return; Object.assign(wsOf(S), { file: path, view: 'diff', tab: tab || wsOf(S).tab || 'files' }); if (SL.views.current && SL.views.current.name === 'workspace') S.touch(); else SL.views.show('workspace', { tab: wsOf(S).tab }); },
    restore(id) { const S = SL.sessions.active; if (!S) return; Object.assign(wsOf(S), { tab: 'checkpoints', cp: null }); if (SL.views.current && SL.views.current.name === 'workspace') S.touch(); else SL.views.show('workspace', { tab: 'checkpoints' }); restoreDialog(S, id); },
    /** /diff [id]: the newest checkpoint's change, or everything from the start of id to now */
    diff(id) { const S = SL.sessions.active; if (!S) return; const I = ws.info(S); const cp = id && I ? I.cps.find(c => c.id === id && c.step) : null; if (id && !cp) { ui.toast(id + ' has nothing to diff', 'warm'); return; } Object.assign(wsOf(S), { tab: 'changes', mode: cp ? 'from' : 'step', cp: cp ? cp.id : null, file: null }); if (SL.views.current && SL.views.current.name === 'workspace') S.touch(); else SL.views.show('workspace', { tab: 'changes' }); },
    setCp(id, tab) { const S = SL.sessions.active; if (!S) return; Object.assign(wsOf(S), { cp: id }, tab ? { tab } : {}); if (SL.views.current && SL.views.current.name === 'workspace') S.touch(); else SL.views.show('workspace', { tab: tab || wsOf(S).tab }); },
    /** the files an agent owns at the live edge: [{path, st, add, del}] (the drawer's "files owned") */
    filesOf(S, id) { return ws.filesOf(S, id); },
    /** the verify output of a task (the drawer's task stepper): the Merge tab on that task */
    verify(task) { const S = SL.sessions.active; if (!S) return; Object.assign(wsOf(S), { tab: 'merge', mview: 'verify', mtask: task }); if (SL.views.current && SL.views.current.name === 'workspace') S.touch(); else SL.views.show('workspace', { tab: 'merge' }); }
  };
})(SL);
