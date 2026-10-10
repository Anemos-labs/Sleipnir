/* 97-ui-workspace.js: the Workspace view: Files, Changes and Checkpoints are three tabs of ONE screen (the Forge tracker in Cockpit's idiom).
 *   left   the tree (ownership stripes in the writer's role colour, leases, protected paths, `ask` markers), the changes grouped by task or
 *          by agent, or the checkpoint list (Diff and Restore with a preview and an in-page confirm)
 *   centre the diff of the selected file with a per-line attribution gutter, hunk revert, Reviewed marks; a whole-file view with blame
 *   top    the time-travel scrubber: the project as it was at any checkpoint, from the base to the newest
 *   bottom the verify / merge strip: the harness running the verify command on what is submitted
 * Everything is data from the pack (SL.ws) and the session's model; the person's marks (reviewed, reverted hunks, a restore) live in S.ws and
 * change only through SL.act. Text from files is data: it is escaped, never markup. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, fmtUsd, agCol, mmss } = U, D = SL.D, calc = SL.calc, ui = SL.ui = SL.ui || {}, ws = SL.ws;
  const CODE_OF = (S, id) => { const r = S.roster.find(x => x.id === id); return r ? r.code : null; };
  const aCol = (S, id) => id && id !== '-' ? agCol(id) : 'var(--faint)';
  const aTint = (S, id) => { const c = CODE_OF(S, id); return c ? 'var(--' + c + '-a)' : 'var(--ok-a)'; };
  const LOCK = '<svg class="lk" viewBox="0 0 12 12" width="11" height="11" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"><rect x="2.4" y="5.4" width="7.2" height="5" rx="1"/><path d="M4 5.4V4a2 2 0 0 1 4 0v1.4"/></svg>';
  const ST_TAG = { A: ['A', 'ok', 'added'], M: ['M', 'warm', 'modified'], D: ['D', 'err', 'deleted'], R: ['R', 'fe', 'restored'] };
  const TASK_ST = { merged: ['✓ merged', 'ok'], verify: ['▸ verifying', 'warm'], running: ['● running', 'fe'], todo: ['◌ todo', ''] };

  /** a little syntax colour for Go, JS, CSS, HTML and JSON: comments dim, strings green, keywords violet. Input is data: every piece is escaped. */
  const KW = /^(func|return|type|package|import|const|var|for|range|if|else|struct|interface|map|chan|go|defer|switch|case|default|break|continue|nil|true|false|let|function|async|await|new|null)$/;
  function hl(t) {
    if (t.length > 400) return esc(t);
    const re = /(\/\/.*$)|("(?:[^"\\]|\\.)*"|`[^`]*`|'(?:[^'\\]|\\.)*')|([A-Za-z_][A-Za-z0-9_]*)/g; let out = '', i = 0, m;
    while ((m = re.exec(t))) { out += esc(t.slice(i, m.index)); i = m.index + m[0].length; if (m[1]) out += '<em class="c">' + esc(m[1]) + '</em>'; else if (m[2]) out += '<em class="s">' + esc(m[2]) + '</em>'; else out += KW.test(m[3]) ? '<em class="k">' + m[3] + '</em>' : m[3]; }
    return out + esc(t.slice(i));
  }
  const W0 = () => ({ tab: 'files', file: null, cp: null, mode: 'since', view: 'diff', group: 'task', q: '', changed: false, closed: {}, unreviewed: false, task: null });
  const wsOf = S => (S.ui.ws = Object.assign(W0(), S.ui.ws || {}));
  const wsState = S => S.ws || (S.ws = { reviewed: {}, reverted: {}, restore: null });

  /** the counts the left rail shows next to Changes and Checkpoints (cheap, cached on the session's history) */
  function counts(S) {
    const I = ws.info(S); if (!I) { const c = derived(S); return { changed: c.length, ckpts: S.m ? S.m.ckpts.filter(x => !x.skipped).length : 0, reviewed: 0 }; }
    const steps = ws.setAt(I, I.pos.length), k = I.key + '|' + steps.length; if (S._wsc && S._wsc.k === k) return S._wsc.v;
    const changed = ws.rows(S, I, steps).filter(r => r.status !== '-').length, v = { changed, ckpts: I.cps.filter(c => c.step || c.safety).length, reviewed: 0 }; S._wsc = { k, v }; return v;
  }
  /** a session without recorded history (a new run, the headless job): the files its tool calls touched */
  function derived(S) {
    const m = S.m || S.wm, by = {}; if (!m) return [];
    m.order.forEach(id => (m.chan[id] || []).forEach(e => { if (e.k === 'tool' && e.file && !e.refused) { const r = by[e.file] || (by[e.file] = { path: e.file, ag: id, add: 0, del: 0, n: 0, task: e.task }); r.add += e.add || 0; r.del += e.del || 0; r.n++; r.ag = id; if (e.task) r.task = e.task; } }));
    return Object.keys(by).sort().map(k => by[k]);
  }

  function restoreDialog(S, id) {
      const I = ws.info(S), idx = I ? I.pos.findIndex(x => x.id === id) : -1; if (!I || idx < 0) { ui.toast(id + ' has nothing to put back', 'warm'); return; } const cp = I.pos[idx], files = ws.filesFrom(I, idx), before = ws.setAt(I, idx), now = ws.setAt(I, I.pos.length);
      const rows = files.map(p => { const h = ws.hunks(I, p, before, now), sb = ws.textAt(I, p, before), sn = ws.textAt(I, p, now), to = sb == null ? 'removed' : 'back to ' + (idx ? I.pos[idx - 1].id : I.base.id); return '<tr><td class="bright mono">' + esc(p) + '</td><td>' + (sb == null ? '<span class="tag err">removed</span>' : '<span class="tag warm">put back</span>') + '</td><td class="r num"><b class="ok">+' + h.removed + '</b> <b class="err">−' + h.added + '</b></td><td class="dim">' + esc(to) + '</td></tr>'; }).join('');
      ui.modal({ title: 'Restore ' + cp.id, kicker: '/rewind ' + cp.id, desc: 'puts the files back to before this turn; the conversation stays', wide: true, color: 'var(--rv)', focus: '[data-no]', body: '<p class="stubnote"><b>' + esc(cp.label) + '</b> · ' + esc(cp.time) + ' · ' + files.length + ' file' + (files.length === 1 ? '' : 's') + ' touched in this checkpoint or later. The agents are told to read them again. <span class="warm">A safety checkpoint is taken first, so this can be undone.</span></p><table class="tbl"><thead><tr><th>file</th><th>what happens</th><th class="r">lines</th><th>result</th></tr></thead><tbody>' + rows + '</tbody></table><div class="row2"><button class="btn danger" type="button" data-ok>Restore the files</button><button class="btn" type="button" data-no>Cancel</button></div>',
        onMount(b, scm, close) { scm.listen($('[data-ok]', b), 'click', () => { close(); SL.act.rewind(cp.id); ui.toast('files put back to before ' + cp.id + ' (mock: nothing is written)', 'ok'); }); scm.listen($('[data-no]', b), 'click', close); } });
    }

  /** is this file being edited right now (the agent is in state edit on it and its next version is not recorded yet)? */
  function isLive(c, live, path) {
    if (!live || live.path !== path || c.k !== c.n || (c.m.diff[path] && c.m.diff[path].done)) return false;
    return (c.I.raw.versions[path] || []).some(v => v[0] !== 'c04' && c.all.indexOf(v[0]) < 0);
  }

  function mount(sc, root, params, S0) {
    const W = wsOf(S0); if (params && params.tab) W.tab = params.tab; if (params && params.file) { W.file = params.file; W.view = 'diff'; }
    root.innerHTML = '<div class="wsv" data-tab="' + W.tab + '">' +
      '<section class="panel ws-bar" aria-label="Workspace controls"><div class="ws-tabs seg" role="tablist" aria-label="Workspace"><button type="button" role="tab" data-tab="files">Files</button><button type="button" role="tab" data-tab="changes">Changes</button><button type="button" role="tab" data-tab="checkpoints">Checkpoints</button></div>' +
      '<div class="ws-tt"><label class="lab" for="wsScrub">time travel</label><div class="ws-scr"><input id="wsScrub" class="scrub" type="range" min="0" max="1" step="1" value="1" aria-label="Time travel across checkpoints: the project as it was at one of them"><div class="ws-ticks" aria-hidden="true"></div></div><span class="ws-at num"></span><button class="btn sm ws-live" type="button">● live</button></div>' +
      '<div class="ws-sum"></div></section>' +
      '<section class="panel ws-left" aria-label="Files, changes and checkpoints"><div class="ph"><h2 class="ws-lh">Files</h2><div class="r ws-lr"></div></div><div class="ws-ctl"></div><div class="ws-list" role="group"></div></section>' +
      '<section class="panel ws-main" aria-label="The selected file"><div class="ws-fh"></div><div class="ws-note" hidden></div><div class="ws-body" tabindex="0"></div></section>' +
      '<section class="panel ws-strip" aria-label="Verify and merge"></section></div>';
    const q = s => $(s, root), left = q('.ws-list'), ctl = q('.ws-ctl'), fh = q('.ws-fh'), note = q('.ws-note'), body = q('.ws-body'), strip = q('.ws-strip'), scrub = q('#wsScrub'), ticks = q('.ws-ticks'), at = q('.ws-at'), live = q('.ws-live'), sum = q('.ws-sum');
    const H = { typed: false };

    /** replace innerHTML only when it changed; keep the keyboard focus on the same row */
    function put(el, h) {
      if (el._h === h) return false; el._h = h; const a = document.activeElement, key = a && el.contains(a) ? (a.dataset.file || a.dataset.cp || a.dataset.dir || a.dataset.task || a.id || '') : null, tag = a && el.contains(a) ? a.tagName + (a.dataset.file ? 'f' : a.dataset.cp ? 'c' : a.dataset.dir ? 'd' : a.dataset.task ? 't' : '') : null;
      el.innerHTML = h;
      if (key && tag) { const sel = tag.endsWith('f') ? '[data-file="' + CSS.escape(key) + '"]' : tag.endsWith('c') ? '[data-cp="' + CSS.escape(key) + '"]' : tag.endsWith('d') ? '[data-dir="' + CSS.escape(key) + '"]' : tag.endsWith('t') ? '[data-task="' + CSS.escape(key) + '"]' : '#' + CSS.escape(key); const n = key ? el.querySelector(sel) : null; if (n && n.focus) n.focus({ preventScroll: true }); }
      return true;
    }

    /* ---------------- the view of the world at the scrubber ---------------- */
    function ctx() {
      const S = SL.sessions.active, W2 = wsOf(S), I = ws.info(S), m = S.m; if (!I || !m) return { S, W: W2, I, m, st: wsState(S) };
      const n = I.pos.length; let k = n; if (W2.cp === 'base') k = 0; else if (W2.cp) { const i = I.pos.findIndex(c => c.id === W2.cp); if (i >= 0) k = i + 1; else W2.cp = null; }
      const steps = ws.setAt(I, k), prev = ws.setAt(I, Math.max(0, k - 1)), all = ws.setAt(I, n), st = wsState(S);
      const range = W2.mode === 'step' ? [prev, steps] : W2.mode === 'from' ? [prev, all] : [[], steps];
      const rows = ws.rows(S, I, W2.mode === 'from' ? all : steps), pend = k === n ? ws.pending(S, I, 'api/catalog/items_test.go', S.vt) : null;
      if (pend && !rows.some(r => r.path === 'api/catalog/items_test.go')) { const L = I.raw.tree.find(f => f.path === 'api/catalog/items_test.go'); rows.push({ path: 'api/catalog/items_test.go', dir: 'api/catalog', name: 'items_test.go', status: 'A', owner: 'ts-1', task: 'T7', cp: null, lease: L && L.lease, protected: null, ask: null, add: ws.lines(pend.text).length, del: 0, writing: true, kind: 'text' }); }
      const lastId = p => { const t = ws.touches(I, p, all); return t.length ? t[t.length - 1].id : null; };
      return { S, W: W2, I, m, n, k, steps, prev, all, range, st, cur: k > 0 ? I.pos[k - 1] : null, pinned: k < n, rows, lastId };
    }

    /* ---------------- left: files ---------------- */
    function owners(rows) { const o = []; rows.forEach(r => { if (r.owner && o.indexOf(r.owner) < 0) o.push(r.owner); }); return o; }
    function fileRow(c, r, depth, opt) {
      const S = c.S, sel = c.W.file === r.path, tag = ST_TAG[opt && opt.restored ? 'R' : r.status], col = r.owner ? aCol(S, r.owner) : 'transparent', rv = c.st.reviewed[r.path] && c.st.reviewed[r.path] === c.lastId(r.path);
      const icons = (r.protected ? '<span class="wi deny" title="denied: ' + esc(r.protected.rule) + ' (' + esc(r.protected.origin) + '): ' + esc(r.protected.why) + '">' + LOCK + '</span>' : '') + (r.lease ? '<span class="wi lease" style="color:' + aCol(S, r.lease.agent) + '" title="leased to ' + esc(r.lease.agent) + ' for ' + esc(r.lease.task) + ': ' + esc(r.lease.glob) + '">' + LOCK + '</span>' : '') + (r.ask ? '<span class="wi ask" title="a write here always asks (' + esc(r.ask.rule) + '): ' + esc(r.ask.why) + '">?</span>' : '');
      const rvt = Object.keys(c.st.reverted).filter(k => k.indexOf(r.path + '#') === 0).length;
      return '<button type="button" class="wf' + (sel ? ' sel' : '') + (r.protected ? ' prot' : '') + '" data-file="' + esc(r.path) + '"' + (r.owner ? ' data-ag="' + esc(r.owner) + '"' : '') + (r.task ? ' data-task="' + esc(r.task) + '"' : '') + ' style="--c:' + col + ';padding-left:' + (10 + depth * 14) + 'px"' + (sel ? ' aria-current="true"' : '') + '>' +
        '<span class="wn">' + esc(opt && opt.full ? r.path : r.name) + '</span>' + icons + '<span class="wr">' + (r.writing ? '<span class="warm" title="being written now">✎</span>' : '') + (rvt ? '<span class="tag warm" title="' + rvt + ' hunk' + (rvt > 1 ? 's' : '') + ' reverted by you">⟲ ' + rvt + '</span>' : '') + (r.status !== '-' ? '<span class="wcnt"><b class="ok">+' + r.add + '</b>' + (r.del ? ' <b class="err">−' + r.del + '</b>' : '') + '</span>' : '') + (r.owner && !(opt && opt.noowner) ? '<span class="wo" style="color:' + col + '">' + esc(r.owner) + '</span>' : '') + (tag ? '<span class="tag ' + tag[1] + '" title="' + tag[2] + '">' + tag[0] + '</span>' : '') + (rv ? '<span class="wrv" title="reviewed">✓</span>' : '') + '</span></button>';
    }
    function treeHtml(c) {
      const W2 = c.W, qq = W2.q.trim().toLowerCase(); let rows = c.rows.filter(r => (!qq || r.path.toLowerCase().indexOf(qq) >= 0) && (!W2.changed || r.status !== '-'));
      const root = { dirs: {}, files: [] };
      rows.forEach(r => { let n = root, p = ''; if (r.dir) r.dir.split('/').forEach(seg => { p = p ? p + '/' + seg : seg; n = n.dirs[seg] || (n.dirs[seg] = { name: seg, path: p, dirs: {}, files: [] }); }); n.files.push(r); });
      let out = '';
      const walk = (n, depth) => {
        Object.keys(n.dirs).sort().forEach(k => { const d = n.dirs[k], all = []; (function g(x) { x.files.forEach(f => all.push(f)); Object.keys(x.dirs).forEach(y => g(x.dirs[y])); })(d); const chg = all.filter(f => f.status !== '-').length, closed = W2.closed[d.path] && !qq, ow = owners(all);
          out += '<button type="button" class="wd" data-dir="' + esc(d.path) + '" aria-expanded="' + !closed + '" style="padding-left:' + (6 + depth * 14) + 'px"><span class="wcar" aria-hidden="true">' + (closed ? '▸' : '▾') + '</span><span class="wn">' + esc(d.name) + '/</span><span class="wr">' + ow.slice(0, 4).map(o => '<i class="wdot" style="background:' + aCol(c.S, o) + '" title="' + esc(o) + '"></i>').join('') + (chg ? '<span class="dim num">' + chg + '</span>' : '') + '</span></button>';
          if (!closed) walk(d, depth + 1); });
        n.files.sort((a, b) => a.name < b.name ? -1 : 1).forEach(r => { out += fileRow(c, r, depth, { restored: c.st.restore && c.st.restore.files.indexOf(r.path) >= 0 && r.status === '-' }); });
      };
      walk(root, 0); return out || '<p class="ws-none">No file matches.</p>';
    }
    /* ---------------- left: changes ---------------- */
    function changeRows(c) {
      const [a, b] = c.range; const out = [];
      c.rows.forEach(r => {
        const sa = ws.textAt(c.I, r.path, a), sb = ws.textAt(c.I, r.path, b); if ((sa || '') === (sb || '') && !(c.st.restore && c.st.restore.files.indexOf(r.path) >= 0 && c.W.mode === 'since' && false)) return;
        const h = ws.counts(c.I, r.path, a, b), tch = ws.touches(c.I, r.path, c.W.mode === 'step' ? c.steps.slice(c.steps.length - 1) : c.W.mode === 'from' ? c.all.slice(c.prev.length) : c.steps), last = tch[tch.length - 1];
        out.push(Object.assign({}, r, { add: h.added, del: h.removed, status: sa == null && sb != null ? 'A' : r.status === '-' ? 'M' : r.status, owner: last ? last.ag : r.owner, task: last ? last.task : r.task, cp: last ? last.id : r.cp, writers: tch.map(t => t.ag).filter((v, i, x) => x.indexOf(v) === i) }));
      });
      return out;
    }
    function changesHtml(c) {
      const W2 = c.W, rows = changeRows(c).filter(r => !W2.unreviewed || c.st.reviewed[r.path] !== r.cp), live2 = ws.liveFile(c.S, c.I), pend = c.m ? ws.pending(c.S, c.I, 'api/catalog/items_test.go', c.S.vt) : null;
      c._changes = rows; let h = '';
      const grp = {}, order = [];
      rows.forEach(r => { const key = W2.group === 'agent' ? (r.owner || '?') : (r.task || 'no task'); if (!grp[key]) { grp[key] = []; order.push(key); } grp[key].push(r); });
      if (pend && c.k === c.n && !rows.some(r => r.path === 'api/catalog/items_test.go')) { const key = W2.group === 'agent' ? 'ts-1' : 'T7'; if (!grp[key]) { grp[key] = []; order.push(key); } grp[key].unshift({ path: 'api/catalog/items_test.go', name: 'items_test.go', dir: 'api/catalog', status: 'A', owner: 'ts-1', task: 'T7', add: ws.lines(pend.text).length, del: 0, writing: true, cp: null }); }
      order.sort((x, y) => x < y ? -1 : 1);
      order.forEach(key => {
        const list = grp[key], add = list.reduce((s, r) => s + r.add, 0), del = list.reduce((s, r) => s + r.del, 0);
        if (W2.group === 'agent') { const A = c.m.ag[key], col = aCol(c.S, key); h += '<div class="wg" style="--c:' + col + '" data-ag="' + esc(key) + '"><b class="gid">' + esc(key) + '</b><span class="dim">' + esc(A ? A.role : '') + '</span><span class="wr"><b class="ok">+' + add + '</b> <b class="err">−' + del + '</b></span></div>'; }
        else { const T = c.m.tasks[key], col = T && T.owner ? aCol(c.S, T.owner) : 'var(--dim)', st = T ? TASK_ST[T.st] || ['', ''] : ['', '']; h += '<div class="wg" style="--c:' + col + '" data-task="' + esc(key) + '"><b class="gid">' + (T ? esc(key) : '◇') + '</b><span class="gti">' + esc(T ? T.title : 'what the agent changed (a single agent keeps no task board)') + '</span>' + (T ? '<span class="tag ' + st[1] + '" title="verification state of the task">' + st[0] + '</span>' : '') + '<span class="wr"><b class="ok">+' + add + '</b> <b class="err">−' + del + '</b></span></div>'; }
        list.forEach(r => {
          const lid = c.lastId(r.path), rv = lid && c.st.reviewed[r.path] === lid, tag = ST_TAG[r.status];
          h += '<div class="wfr' + (c.W.file === r.path ? ' sel' : '') + '"><button type="button" class="wf" data-file="' + esc(r.path) + '"' + (r.owner ? ' data-ag="' + esc(r.owner) + '"' : '') + (r.task ? ' data-task="' + esc(r.task) + '"' : '') + ' style="--c:' + aCol(c.S, r.owner) + '"' + (c.W.file === r.path ? ' aria-current="true"' : '') + '><span class="wn"><span class="dim">' + esc(r.dir ? r.dir + '/' : '') + '</span>' + esc(r.name) + '</span><span class="wr">' + (r.writing ? '<span class="warm" title="being written now">✎ writing</span>' : '') + '<span class="wcnt"><b class="ok">+' + r.add + '</b>' + (r.del ? ' <b class="err">−' + r.del + '</b>' : '') + '</span>' + (W2.group === 'task' ? '<span class="wo" style="color:' + aCol(c.S, r.owner) + '">' + esc(r.owner || '') + '</span>' : '') + (tag ? '<span class="tag ' + tag[1] + '" title="' + tag[2] + '">' + tag[0] + '</span>' : '') + '</span></button>' + (r.writing ? '' : '<button type="button" class="wrvb' + (rv ? ' on' : '') + '" data-review="' + esc(r.path) + '" data-cpid="' + esc(lid || '') + '" aria-pressed="' + rv + '" title="' + (rv ? 'reviewed: click to unmark' : 'mark as reviewed') + '"><span class="sr">Reviewed: ' + esc(r.path) + '</span>' + (rv ? '✓' : '○') + '</button>') + '</div>';
        });
      });
      if (c.st.restore) h += '<div class="wg rest"><b class="gid">↺ restored</b><span class="gti">to before ' + esc(c.st.restore.to) + ': ' + c.st.restore.files.length + ' file' + (c.st.restore.files.length === 1 ? '' : 's') + '</span></div>';
      return h || '<p class="ws-none">' + (W2.unreviewed ? 'Everything here is reviewed.' : 'No file changed in this view.') + '</p>';
    }
    /* ---------------- left: checkpoints ---------------- */
    function cpsHtml(c) {
      const I = c.I; let h = ''; const list = I.cps.slice().reverse();
      list.forEach(cp => {
        const idx = cp.step ? I.pos.indexOf(cp) + 1 : -1, sel = c.cur && c.cur.id === cp.id, newest = I.pos.length && I.pos[I.pos.length - 1] === cp;
        h += '<div class="wc' + (sel ? ' sel' : '') + (cp.skipped ? ' skipped' : '') + (cp.safety ? ' safe' : '') + '"' + (cp.agents.length ? ' data-ag="' + esc(cp.agents.join(' ')) + '"' : '') + '><button type="button" class="wcm" data-cp="' + (cp.step ? esc(cp.id) : 'base') + '"' + (cp.step || (cp.id === I.base.id && I.pos.length) ? '' : ' disabled') + (sel || (c.k === 0 && cp.id === I.base.id) ? ' aria-current="true"' : '') + '><b class="cid">' + esc(cp.id) + '</b><span class="ctm num">' + esc(cp.time || '') + '</span><span class="clb">' + esc(cp.label) + '</span><span class="cmeta">' + (newest ? '<em class="newest">● newest</em>' : '') + (cp.skipped ? '<span class="dim">skipped: nothing to put back</span>' : '<span class="num">' + cp.nfiles + ' file' + (cp.nfiles === 1 ? '' : 's') + '</span>' + (cp.added || cp.removed ? '<span class="num"><b class="ok">+' + cp.added + '</b> <b class="err">−' + cp.removed + '</b></span>' : '') + cp.agents.map(a => '<span class="wo" style="color:' + aCol(c.S, a) + '">' + esc(a) + '</span>').join('') + cp.tasks.map(t => '<span class="chip">' + esc(t) + '</span>').join('')) + '</span></button>' +
          (cp.step ? '<span class="cact"><button type="button" class="btn sm" data-cpdiff="' + esc(cp.id) + '">Diff</button><button type="button" class="btn sm" data-restore="' + esc(cp.id) + '">Restore</button></span>' : '') + '</div>';
      });
      h += '<p class="ws-help"><span class="mono">/rewind ID</span> puts every file touched in ID or later back as it was when ID began; the conversation stays. <span class="mono">/diff ID</span> shows that difference. A checkpoint is taken before every restore, so a restore can be undone.</p>';
      return h;
    }

    /* ---------------- centre: the selected file ---------------- */
    function pickDefault(c) {
      const W2 = c.W; if (W2.file && c.rows.some(r => r.path === W2.file)) return; const ch = c.rows.filter(r => r.status !== '-'); const cand = W2.tab === 'files' ? c.rows.find(r => r.status !== '-' && !r.protected) || c.rows.find(r => !r.protected) : (changeRows(c)[0] || ch[0]);
      W2.file = cand ? cand.path : null;
    }
    function bodyHtml(c) {
      const W2 = c.W, S = c.S; const r = c.rows.find(x => x.path === W2.file), path = W2.file;
      if (!path) return { head: '<div class="ph"><h2>No file selected</h2></div>', body: '<p class="ws-none">Select a file on the left: its diff, with the agent who wrote each line, shows here.</p>' };
      const a = c.range[0], b = c.range[1], pend = ws.pending(S, c.I, path, S.vt), live2 = ws.liveFile(S, c.I);
      const tNow = pend ? pend.text : ws.textAt(c.I, path, b), tOld = ws.textAt(c.I, path, a);
      const rawRow = c.I.raw.tree.find(f => f.path === path) || {}, prot = rawRow.protected;
      const tch = ws.touches(c.I, path, c.all), last = tch[tch.length - 1], owner = r && r.owner, status = pend ? 'A' : ws.statusOf(c.I, path, b), h0 = ws.hunks(c.I, path, a, b), cn = ws.counts(c.I, path, a, b), partial = /seed\/items\.json$/.test(path);
      const rv = c.st.reviewed[path] && last && c.st.reviewed[path] === last.id;
      const vt = c.m && c.m.tasks[(last || {}).task], tstate = vt ? TASK_ST[vt.st] || ['', ''] : null, tag = ST_TAG[status];
      let head = '<div class="ws-ph"><div class="wtop"><h2 class="wpath"><span class="dim">' + esc(path.indexOf('/') >= 0 ? path.slice(0, path.lastIndexOf('/') + 1) : '') + '</span>' + esc(path.slice(path.lastIndexOf('/') + 1)) + '</h2>' + (tag ? '<span class="tag ' + tag[1] + '" title="' + tag[2] + ' against the start of the session">' + tag[0] + '</span>' : '') +
        (owner || pend ? '<span class="chip task" style="--c:' + aCol(S, pend ? 'ts-1' : owner) + '" data-ag="' + esc(pend ? 'ts-1' : owner) + '">' + esc(pend ? 'ts-1' : owner) + '</span>' : '') + ((last && last.task) || pend ? '<span class="chip" data-task="' + esc(pend ? 'T7' : last.task) + '">' + esc(pend ? 'T7' : last.task) + '</span>' : '') + (tstate && !pend ? '<span class="tag ' + tstate[1] + '">' + tstate[0] + '</span>' : '') +
        (last ? '<span class="chip" title="the checkpoint of the last change">' + esc(last.id) + '</span>' : '') + '<span class="num wcounts"' + (partial ? ' title="the counts are the real ones; the sample keeps the first rows of the file"' : '') + '><b class="ok">+' + (pend ? ws.lines(pend.text).length : cn.added) + '</b> <b class="err">−' + (pend ? 0 : cn.removed) + '</b></span></div>' +
        '<div class="wtool"><div class="seg" role="group" aria-label="Show"><button type="button" data-vw="diff" aria-pressed="' + (W2.view === 'diff') + '">Diff</button><button type="button" data-vw="file" aria-pressed="' + (W2.view === 'file') + '">Whole file</button></div>' +
        (last && !pend ? '<button type="button" class="btn sm' + (rv ? ' on' : '') + '" data-review="' + esc(path) + '" data-cpid="' + esc(last.id) + '" aria-pressed="' + rv + '">' + (rv ? '✓ Reviewed' : '○ Mark reviewed') + '</button>' : '') + '<button type="button" class="btn sm" data-copy="' + esc(path) + '" title="copy the path">Copy path</button><span class="dim wnote">' + (W2.mode === 'step' ? 'this checkpoint only' : W2.mode === 'from' ? 'from the start of ' + esc(c.cur ? c.cur.id : '') + ' to now' : 'since the start of the session') + '</span></div></div>';
      if (prot) return { head, body: '<div class="ws-lock"><div class="lockbig">' + LOCK + '</div><b>Denied path</b><p>Agents cannot read <code>' + esc(path) + '</code>: the rule <code>' + esc(prot.rule) + '</code> (' + esc(prot.origin) + ', ' + esc(prot.tier) + ') applies. ' + esc(prot.why) + '.</p><p class="dim">This page does not show it either: file contents are data from the project, and a denied path has none in this session.</p></div>', pend: null };
      if (tNow == null && !pend) return { head, body: '<p class="ws-none">' + (r ? 'No content for this file is kept in the sample data.' : 'This file does not exist at this point in time.') + '</p>' };
      let html = '';
      const mode = (W2.view === 'file' || (status === '-' && !pend)) ? 'file' : 'diff';
      if (mode === 'diff') {
        const hh = pend ? { added: ws.lines(pend.text).length, removed: 0, hunks: [{ oldStart: 0, oldLines: 0, newStart: 1, newLines: ws.lines(pend.text).length, section: '', lines: ws.lines(pend.text).map(s => ({ t: '+', s })) }] } : h0;
        const bl = ws.blame(c.I, path, pend ? c.all : b);
        if (!hh.hunks.length) html = '<p class="ws-none">No difference in this view. <button type="button" class="btn sm" data-vw="file">Show the whole file</button></p>';
        else { html = '<div class="diff wdiff">'; const caretTarget = pend ? (pend.done ? -1 : 1) : (isLive(c, live2, path) ? 1 : 0);
          hh.hunks.forEach((hk, hi) => {
            const key = hk.oldStart + ':' + hk.newStart, rvd = c.st.reverted[path + '#' + key];
            html += '<div class="ln hunk wh"><b class="who"></b><i></i><s></s><span class="hh2">@@ -' + hk.oldStart + ',' + hk.oldLines + ' +' + hk.newStart + ',' + hk.newLines + ' @@ ' + esc(hk.section || '') + '</span>' + (rvd ? '<span class="rvd">⟲ reverted by you · nothing is written in this mock</span><button type="button" class="btn sm" data-unrev="' + esc(key) + '">Undo</button>' : (pend ? '' : '<button type="button" class="btn sm" data-rev="' + esc(key) + '" title="put this hunk back as it was">Revert hunk</button>')) + '</div>';
            if (rvd) return; let nn = hk.newStart, on = hk.oldStart, lastAdd = -1;
            hk.lines.forEach((l, li) => { if (l.t === '+') lastAdd = li; });
            hk.lines.forEach((l, li) => {
              if (l.t === '…') { html += '<div class="ln hunk"><b class="who"></b><i></i><s></s><span>… ' + esc(l.s) + '</span></div>'; return; }
              const bi = bl[nn - 1], ag = l.t === '+' ? (pend ? 'ts-1' : (bi && bi.ag) || owner) : null;
              if (l.t === '-') { html += '<div class="ln del"><b class="who"></b><i>' + on + '</i><s>−</s><span>' + hl(l.s) + '</span></div>'; on++; }
              else if (l.t === '+') { const car = caretTarget === 1 && hi === hh.hunks.length - 1 && li === lastAdd; html += '<div class="ln add' + (car ? ' caretline' : '') + '" style="--ag:' + aCol(S, ag) + ';--ag-a:' + aTint(S, ag) + '"><b class="who" data-ag="' + esc(ag || '') + '" title="written by ' + esc(ag || '') + (bi && bi.task ? ' · ' + esc(bi.task) : '') + (bi && bi.id ? ' · ' + esc(bi.id) : '') + '">' + esc(ag || '') + '</b><i>' + nn + '</i><s>+</s><span>' + hl(l.s) + '</span></div>'; nn++; }
              else { const o = bl[nn - 1]; html += '<div class="ln ctx" style="--ag:' + aCol(S, o && o.ag) + '"><b class="who dimw">' + (o && o.ag && o.ag !== '-' ? esc(o.ag) : '') + '</b><i>' + nn + '</i><s></s><span>' + hl(l.s) + '</span></div>'; nn++; on++; }
            });
          });
          html += '</div>'; }
      } else {
        const text = pend ? pend.text : tNow, bl = ws.blame(c.I, path, pend ? c.all : b), ls = ws.lines(text); let prevAg = null;
        html = '<div class="diff wdiff whole">' + ls.map((t, i) => { const o = bl[i] || {}, first = o.ag !== prevAg; prevAg = o.ag; const mine = o.ag && o.ag !== '-'; return '<div class="ln' + (mine ? ' add own' : '') + (pend && !pend.done && i === ls.length - 1 ? ' caretline' : '') + '" style="--ag:' + aCol(S, o.ag) + ';--ag-a:' + aTint(S, o.ag) + '"><b class="who" ' + (mine ? 'data-ag="' + esc(o.ag) + '" title="written by ' + esc(o.ag) + (o.task ? ' · ' + esc(o.task) : '') + (o.id ? ' · ' + esc(o.id) : '') + '"' : '') + '>' + (first && mine ? esc(o.ag) : '') + '</b><i>' + (i + 1) + '</i><s></s><span>' + hl(t) + '</span></div>'; }).join('') + '</div>';
      }
      return { head, body: html, pend, mode };
    }

    /* ---------------- bottom: the verify / merge strip ---------------- */
    function stripHtml(c) {
      const m = c.m, S = c.S, qh = m.qHead, merged = m.merged.slice().sort(), isGoal = qh && qh.task === 'goal';
      const chips = m.torder.map(t => { const T = m.tasks[t], st = TASK_ST[T.st] || ['', ''], blocked = T.st === 'running' && m.qs.some(q => !q.answered && q.task === t); return '<button type="button" class="wtk" data-taskf="' + esc(t) + '" data-task="' + esc(t) + '" style="--c:' + aCol(S, T.owner) + '" aria-pressed="' + (c.W.task === t) + '" title="' + esc(t + ' ' + T.title + ' · ' + (T.owner || '') + ' · ' + (blocked ? 'asks you' : st[0]) + ' (click: show only its changes)') + '"><b>' + esc(t) + '</b><span class="' + (blocked ? 'warm' : st[1]) + '" aria-hidden="true">' + (blocked ? '?' : st[0].split(' ')[0]) + '</span><span class="sr">' + esc(t + ' ' + (blocked ? 'asks you' : st[0])) + '</span></button>'; }).join('');
      const chg = c.I ? changeRows(c) : derived(S).map(r => ({ path: r.path })), rvd = c.I ? chg.filter(r => c.lastId(r.path) && c.st.reviewed[r.path] === c.lastId(r.path)).length : 0;
      return '<div class="ws-sh"><h2>Verify and merge</h2><span class="mono dim">--verify "' + esc(S.meta.verify || '(none)') + '"</span></div>' +
        '<div class="ws-sb"><div class="wq">' + (qh ? '<div class="wqh"><span class="tri">▸</span><b style="color:' + (isGoal ? 'var(--mgr)' : aCol(S, (m.tasks[qh.task] || {}).owner)) + '">' + (isGoal ? '◇ goal' : esc(qh.task)) + '</b>' + (isGoal ? '<span>all merged</span>' : '<span>rebase <span class="ok">✓</span></span>') + '<span class="dim">·</span><span>' + (qh.step === 'verified' ? 'verified' : 'verifying') + '</span><span class="mono cmd">' + esc(qh.cmd) + '</span></div><div class="qbar" aria-hidden="true">' + (qh.step === 'verified' ? '<i style="width:100%;left:0;animation:none;background:var(--ok)"></i>' : '<i></i>') + '</div>' : '<div class="wqh dim"><span>▹ queue empty</span><span>·</span><span>the next submission is rebased, then verified</span></div><div class="qbar" aria-hidden="true"></div>') +
        '</div><div class="wtks" role="group" aria-label="Tasks: click to show only its changes">' + chips + '</div><div class="wmeta"><span>merged <b>' + merged.length + '</b>/' + m.torder.length + '</span><span>conflicts <b>' + m.conflicts + '</b></span><span>bounced <b>' + m.bounced + '</b></span><span>reviewed <b>' + rvd + '</b>/' + chg.length + '</span></div></div>';
    }

    /* ---------------- paint ---------------- */
    function paint() {
      const c = ctx(), S = c.S, W2 = c.W; const wsv = q('.wsv'); wsv.dataset.tab = W2.tab;
      $$('.ws-tabs [data-tab]', root).forEach(b => { b.setAttribute('aria-selected', String(b.dataset.tab === W2.tab)); b.setAttribute('aria-pressed', String(b.dataset.tab === W2.tab)); });
      if (!c.I) return paintDerived(c);
      const nChg = changeRows(c).length;
      pickDefault(c);
      /* scrubber */
      scrub.max = String(Math.max(1, c.n)); scrub.min = '0'; if (+scrub.value !== c.k && document.activeElement !== scrub) scrub.value = String(c.k); scrub.disabled = !c.n;
      const tk = ['<span class="tk" style="left:0">' + esc(c.I.base.id) + '</span>'].concat(c.I.pos.map((p, i) => '<span class="tk' + (i + 1 === c.k ? ' on' : '') + '" style="left:' + ((i + 1) / Math.max(1, c.n) * 100) + '%">' + esc(p.id) + '</span>')).join(''); put(ticks, tk);
      at.textContent = c.cur ? c.cur.id + ' · ' + c.cur.time + ' · ' + c.cur.label : c.I.base.id + ' · ' + c.I.base.label; live.hidden = !c.pinned; scrub.setAttribute('aria-valuetext', c.cur ? c.cur.id + ' ' + c.cur.label : 'the base: ' + c.I.base.label);
      const tot = ws.rows(S, c.I, c.steps).filter(r => r.status !== '-'), ad = tot.reduce((s, r) => s + r.add, 0), de = tot.reduce((s, r) => s + r.del, 0);
      sum.innerHTML = '<span class="chip">' + tot.length + ' file' + (tot.length === 1 ? '' : 's') + ' changed</span><span class="num"><b class="ok">+' + ad + '</b> <b class="err">−' + de + '</b></span><span class="dim">' + esc(S.meta.isolation === 'worktree' ? 'each worker in its own worktree' : 'one shared checkout') + '</span>';
      /* left */
      const lh = { files: 'Files', changes: 'Changes', checkpoints: 'Checkpoints' }[W2.tab]; q('.ws-lh').textContent = lh;
      let ch = '', list = '';
      if (W2.tab === 'files') {
        ch = '<input class="wq-in" type="search" placeholder="filter paths" aria-label="Filter paths" value="' + esc(W2.q) + '" autocomplete="off"><label class="chk"><input type="checkbox" data-opt="changed"' + (W2.changed ? ' checked' : '') + '> changed only</label>';
        list = treeHtml(c); q('.ws-lr').innerHTML = '<span>' + c.rows.length + ' files</span>';
      } else if (W2.tab === 'changes') {
        ch = '<div class="seg" role="group" aria-label="Group by"><button type="button" data-grp="task" aria-pressed="' + (W2.group === 'task') + '">by task</button><button type="button" data-grp="agent" aria-pressed="' + (W2.group === 'agent') + '">by agent</button></div><div class="seg" role="group" aria-label="Range"><button type="button" data-mode="since" aria-pressed="' + (W2.mode === 'since') + '" title="everything since the base, up to the point in time">since start</button><button type="button" data-mode="step" aria-pressed="' + (W2.mode === 'step') + '" title="only the checkpoint at the point in time"' + (c.cur ? '' : ' disabled') + '>only ' + esc(c.cur ? c.cur.id : '·') + '</button><button type="button" data-mode="from" aria-pressed="' + (W2.mode === 'from') + '" title="/diff ID: from the start of that checkpoint to now"' + (c.cur ? '' : ' disabled') + '>' + esc(c.cur ? c.cur.id : '·') + ' to now</button></div><label class="chk"><input type="checkbox" data-opt="unreviewed"' + (W2.unreviewed ? ' checked' : '') + '> unreviewed</label>';
        list = changesHtml(c); q('.ws-lr').innerHTML = '<span>' + nChg + ' file' + (nChg === 1 ? '' : 's') + '</span>';
      } else { ch = ''; list = cpsHtml(c); q('.ws-lr').innerHTML = '<span>' + c.I.cps.length + ' · ' + c.I.pos.length + ' with files</span>'; }
      put(ctl, ch); put(left, list);
      /* centre */
      const o = bodyHtml(c); H.typed = !!(o.pend && !o.pend.done) || isLive(c, ws.liveFile(S, c.I), W2.file);
      put(fh, o.head); const bt = body.scrollTop; const changedBody = put(body, o.body); if (changedBody && H.typed) body.scrollTop = body.scrollHeight; else if (changedBody) body.scrollTop = bt;
      /* notes */
      let nt = '';
      if (c.pinned) nt += '<div class="wnr tt"><span><b>Time travel</b> · the project as it was after <b class="mono">' + esc(c.cur ? c.cur.id : c.I.base.id) + '</b>' + (c.cur ? ' (' + esc(c.cur.time) + ')' : '') + '; the session is at <b class="mono">' + esc(c.I.pos[c.n - 1].id) + '</b>.</span><button type="button" class="btn sm" data-golive>Return to live</button></div>';
      if (c.st.restore) nt += '<div class="wnr rs"><span><b>↺ Restored</b> · ' + c.st.restore.files.length + ' file' + (c.st.restore.files.length === 1 ? '' : 's') + ' put back to before <b class="mono">' + esc(c.st.restore.to) + '</b>; a safety checkpoint <b class="mono">' + esc(c.st.restore.safety) + '</b> was taken first (mock: nothing is written).</span><button type="button" class="btn sm" data-undorestore>Undo the restore</button></div>';
      note.hidden = !nt; put(note, nt);
      put(strip, stripHtml(c));
    }
    /** a session without recorded history: the files its tool calls touched, as a list; the transcript is the record */
    function paintDerived(c) {
      const rows = derived(c.S); scrub.disabled = true; live.hidden = true; at.textContent = 'no recorded history'; put(ticks, '');
      sum.innerHTML = '<span class="chip">' + rows.length + ' file' + (rows.length === 1 ? '' : 's') + ' touched</span><span class="dim">this session is not in the sample repository</span>';
      q('.ws-lh').textContent = { files: 'Files', changes: 'Changes', checkpoints: 'Checkpoints' }[c.W.tab]; q('.ws-lr').innerHTML = '<span>' + rows.length + '</span>'; put(ctl, '');
      put(left, c.W.tab === 'checkpoints' ? ((c.m.ckpts.length ? c.m.ckpts.map(k => '<div class="wc"><span class="wcm"><b class="cid">' + esc(k.id) + '</b><span class="ctm num">' + esc(k.ts || '') + '</span><span class="clb">' + esc(k.note) + '</span><span class="cmeta"><span class="num">' + k.files + ' file' + (k.files === 1 ? '' : 's') + '</span></span></span></div>').join('') : '<p class="ws-none">No checkpoint yet: the first turn that writes a file takes one.</p>')) : rows.length ? rows.map(r => '<div class="wfr' + (c.W.file === r.path ? ' sel' : '') + '"><button type="button" class="wf" data-ag="' + esc(r.ag) + '" data-file="' + esc(r.path) + '" style="--c:' + aCol(c.S, r.ag) + '"><span class="wn">' + esc(r.path) + '</span><span class="wr"><span class="wcnt"><b class="ok">+' + r.add + '</b> <b class="err">−' + r.del + '</b></span><span class="wo" style="color:' + aCol(c.S, r.ag) + '">' + esc(r.ag) + '</span></span></button></div>').join('') : '<p class="ws-none">No file has been touched yet.</p>');
      const pick = c.W.file && rows.some(r => r.path === c.W.file) ? c.W.file : (rows[0] && rows[0].path), calls = []; if (pick) c.m.order.forEach(id => (c.m.chan[id] || []).forEach(e => { if (e.k === 'tool' && e.file === pick && !e.refused) calls.push({ id, e }); })); calls.sort((a, b) => a.e.t - b.e.t);
      put(fh, '<div class="ws-ph"><div class="wtop"><h2 class="wpath">' + (pick ? esc(pick) : esc(c.S.name)) + '</h2></div></div>');
      put(body, (pick ? '<div class="wcalls"><h3 class="lab">what the agents did to this file</h3><table class="tbl"><thead><tr><th>time</th><th>agent</th><th>tool</th><th class="r">lines</th><th>result</th></tr></thead><tbody>' + calls.map(({ id, e }) => '<tr data-ag="' + esc(id) + '"><td class="num dim">' + U.tod(c.S.meta.t0, e.t) + '</td><td style="color:' + aCol(c.S, id) + ';font-weight:700">' + esc(id) + '</td><td class="bright mono">' + esc(e.name) + '</td><td class="r num"><b class="ok">+' + (e.add || 0) + '</b> <b class="err">−' + (e.del || 0) + '</b></td><td class="dim">' + esc(e.out || '') + '</td></tr>').join('') + '</tbody></table></div>' : '') + '<p class="ws-none">The sample data keeps file contents for the shop and orders-api sessions only; for this session the record is the tool calls above and each agent’s transcript in Radio. File names and tool output are shown as text.</p>'); note.hidden = true;
      put(strip, stripHtml(c));
    }

    /* ---------------- input ---------------- */
    const setW = (patch, scroll) => { const S = SL.sessions.active, W2 = wsOf(S); Object.assign(W2, patch); S.touch(); paint(); if (scroll) body.scrollTop = 0; };
    sc.listen(root, 'click', e => {
      const t = e.target, S = SL.sessions.active, c = ctx(), W2 = c.W;
      const tab = t.closest('.ws-tabs [data-tab]'); if (tab) { ui.ws.setTab(tab.dataset.tab); return; }
      const d = t.closest('[data-dir]'); if (d) { W2.closed[d.dataset.dir] = !W2.closed[d.dataset.dir]; paint(); return; }
      const rev = t.closest('[data-review]'); if (rev) { SL.act.markReviewed(rev.dataset.review, rev.dataset.cpid, rev.getAttribute('aria-pressed') !== 'true'); return; }
      const f = t.closest('[data-file]'); if (f && !t.closest('[data-rev],[data-unrev],[data-copy]')) { setW({ file: f.dataset.file, view: W2.view }, true); return; }
      const vw = t.closest('[data-vw]'); if (vw) { setW({ view: vw.dataset.vw }); return; }
      const gp = t.closest('[data-grp]'); if (gp) { setW({ group: gp.dataset.grp }); return; }
      const md = t.closest('[data-mode]'); if (md) { setW({ mode: md.dataset.mode }); return; }
      const cpd = t.closest('[data-cpdiff]'); if (cpd) { setW({ tab: 'changes', cp: c.n && c.I.pos[c.n - 1].id === cpd.dataset.cpdiff ? null : cpd.dataset.cpdiff, mode: 'from', file: null }); return; }
      const rst = t.closest('[data-restore]'); if (rst) { restoreDialog(SL.sessions.active, rst.dataset.restore); return; }
      const cp = t.closest('[data-cp]'); if (cp) { const last = c.I.pos[c.n - 1]; setW({ cp: last && last.id === cp.dataset.cp ? null : cp.dataset.cp }); return; }
      const tk = t.closest('[data-taskf]'); if (tk) { const on = W2.task !== tk.dataset.taskf; W2.task = on ? tk.dataset.taskf : null; if (on) { Object.assign(W2, { tab: 'changes', group: 'task' }); } S.touch(); paint(); if (on) { const first = changeRows(ctx()).find(r => r.task === tk.dataset.taskf); if (first) setW({ file: first.path }); } return; }
      const rv = t.closest('[data-rev]'); if (rv) { revertDialog(rv.dataset.rev); return; }
      const ur = t.closest('[data-unrev]'); if (ur) { SL.act.unrevertHunk(W2.file, ur.dataset.unrev); return; }
      const cpy = t.closest('[data-copy]'); if (cpy) { U.copy(cpy.dataset.copy).then(ok => ui.toast(ok ? 'path copied' : 'copy is not available here', ok ? 'ok' : 'warm')); return; }
      if (t.closest('[data-golive]') || t.closest('.ws-live')) { setW({ cp: null }); return; }
      if (t.closest('[data-undorestore]')) { SL.act.undoRewind(); return; }
    });
    sc.listen(root, 'input', e => { const t = e.target; if (t.id === 'wsScrub') { const c = ctx(), k = +t.value, last = c.I && c.I.pos[c.n - 1]; setW({ cp: !c.I ? null : k >= c.n ? null : k === 0 ? 'base' : c.I.pos[k - 1].id }); } else if (t.classList.contains('wq-in')) { const S = SL.sessions.active; wsOf(S).q = t.value; paintLeftOnly(); } });
    sc.listen(root, 'change', e => { const t = e.target; if (t.dataset && t.dataset.opt) { setW({ [t.dataset.opt]: t.checked }); } });
    function paintLeftOnly() { const c = ctx(); if (c.I) put(left, treeHtml(c)); }
    sc.listen(root, 'keydown', e => {
      const t = e.target; if (!t.closest || !t.closest('.ws-left')) return;
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { const bs = $$('.ws-list .wf, .ws-list .wd, .ws-list .wcm:not([disabled])', root), i = bs.indexOf(t); if (i >= 0) { e.preventDefault(); const n = bs[Math.max(0, Math.min(bs.length - 1, i + (e.key === 'ArrowDown' ? 1 : -1)))]; n.focus(); } else if (t.classList.contains('wq-in') && e.key === 'ArrowDown') { const f = bs[0]; if (f) { e.preventDefault(); f.focus(); } } }
    });
    sc.listen(root, 'pointerover', () => { /* linking is handled by SL.link; nothing to do here */ });
    scrub.addEventListener('keydown', e => { SL.time.noteKey(); }); sc.listen(scrub, 'focus', () => { /* keep the value in sync */ });

    /* ---------------- dialogs: restore preview and hunk revert ---------------- */
    function revertDialog(key) {
      const c = ctx(), path = c.W.file, h = ws.hunks(c.I, path, c.range[0], c.range[1]), hk = h.hunks.find(x => x.oldStart + ':' + x.newStart === key); if (!hk) return;
      const pv = hk.lines.filter(l => l.t !== ' ' && l.t !== '…').slice(0, 14).map(l => '<div class="ln ' + (l.t === '+' ? 'del' : 'add') + '"><b class="who"></b><i></i><s>' + (l.t === '+' ? '−' : '+') + '</s><span>' + hl(l.s) + '</span></div>').join('');
      ui.modal({ title: 'Revert this hunk', kicker: esc(path).slice(0, 60), desc: 'put these lines back as they were before', color: 'var(--rv)', focus: '[data-no]', body: '<p class="stubnote">The hunk at line ' + hk.newStart + ' of <b class="mono">' + esc(path) + '</b> goes back to its earlier text. What the revert would do, line by line:</p><div class="diff wdiff">' + pv + '</div><div class="row2"><button class="btn danger" type="button" data-ok>Revert the hunk</button><button class="btn" type="button" data-no>Cancel</button></div>',
        onMount(b, scm, close) { scm.listen($('[data-ok]', b), 'click', () => { close(); SL.act.revertHunk(path, key); }); scm.listen($('[data-no]', b), 'click', close); } });
    }

    /* ---------------- live: typing caret and the update hook ---------------- */
    sc.update(() => paint());
    let acc = 0;
    sc.frame((dt, vt, S2) => { if (!H.typed) return; acc += dt; if (acc < 0.12) return; acc = 0; const c = ctx(); if (!c.I) return; const o = bodyHtml(c); H.typed = !!(o.pend && !o.pend.done) || isLive(c, ws.liveFile(S2, c.I), c.W.file); if (put(body, o.body)) body.scrollTop = body.scrollHeight; if (c.W.tab === 'changes') put(left, changesHtml(c)); });
    paint();
  }
  SL.views.register({ name: 'workspace', title: 'Workspace', nav: true, mount });
  /** the Workspace's API for the rest of the page (rail, chat rows, palette): it only sets the session's own view state; the mounted view repaints from it */
  ui.ws = {
    counts, derived,
    setTab(t) { const S = SL.sessions.active; if (!S) return; wsOf(S).tab = t; S.touch(); },
    open(path, tab) { const S = SL.sessions.active; if (!S) return; Object.assign(wsOf(S), { file: path, view: 'diff', tab: tab || wsOf(S).tab || 'files' }); if (SL.views.current && SL.views.current.name === 'workspace') S.touch(); else SL.views.show('workspace', { tab: wsOf(S).tab }); },
    restore(id) { const S = SL.sessions.active; if (!S) return; Object.assign(wsOf(S), { tab: 'checkpoints', cp: null }); if (SL.views.current && SL.views.current.name === 'workspace') S.touch(); else SL.views.show('workspace', { tab: 'checkpoints' }); restoreDialog(S, id); },
    /** /diff [id]: the newest checkpoint's change, or everything from the start of id to now */
    diff(id) { const S = SL.sessions.active; if (!S) return; const I = ws.info(S); const cp = id && I ? I.cps.find(c => c.id === id && c.step) : null; if (id && !cp) { ui.toast(id + ' has nothing to diff', 'warm'); return; } Object.assign(wsOf(S), { tab: 'changes', mode: cp ? 'from' : 'step', cp: cp ? cp.id : null, file: null }); if (SL.views.current && SL.views.current.name === 'workspace') S.touch(); else SL.views.show('workspace', { tab: 'changes' }); },
    setCp(id, tab) { const S = SL.sessions.active; if (!S) return; Object.assign(wsOf(S), { cp: id }, tab ? { tab } : {}); if (SL.views.current && SL.views.current.name === 'workspace') S.touch(); else SL.views.show('workspace', { tab: tab || wsOf(S).tab }); },
  };
})(SL);
