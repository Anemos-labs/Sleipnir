/* 84-ui-chat.js: SL.chat, the Radio rail: the conversation with the manager (the person talks to the manager only), the read-only Team activity
 * feed below it, the plan box, the hold chip and the composer. The transcript is the one place the time governor is felt most: while the pointer is over it (or focus is in it, or
 * the hold is pinned) the view clock stops and nothing is appended, scrolled or typed; on release it catches up (bounded, see 50-sessions.js).
 *
 * DOM budget: at most ROW_CAP rows exist per log; older rows are folded into one marker, a collapsed span of time is ONE digest row.
 * The entries themselves live in the model (m.chan.mgr); this file only renders them. A message the server is still streaming (an
 * entry with a `mid` that is not done) keeps growing: its row reads the entry's current text every frame until the server ends it. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, frag, tod, fmtK, fmtUsd, agCol, own, clip1 } = U, calc = SL.calc, ui = SL.ui = SL.ui || {};
  /** A number from an entry for markup: a finite number, else d (never the server's text). */
  const nm = (x, d) => typeof x === 'number' && isFinite(x) ? x : (d == null ? 0 : d);
  const ROW_CAP = 220;
  const FEED_G = { tool: '⚙', edit: '✎', done: '✓', mail: '✉', ask: '?', steer: '⤷', x: '⊘' };
  const TOOL_GLYPH = n => /^(Write|Edit)$/.test(String(n)) ? '✎' : '⚙';
  const WORDS = { think: 'thinking', tool: 'running a tool', edit: 'editing', wait: 'waiting', ask: 'asking you', idle: 'idle', done: 'done', stuck: 'stuck' };
  const chip = t => esc(t).replace(/\[pasted text #\d+ \+\d+ lines\]/g, m => '<span class="pastechip">' + m + '</span>');
  const hms = ms => { const d = new Date(ms); return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0') + ':' + String(d.getSeconds()).padStart(2, '0'); };
  /** The time of day of an entry or event: its real time when the server sent one (`at`, history), else the session's start plus t. */
  const todAt = (S, e) => e && e.at ? hms(e.at) : tod(S.meta.t0, e ? e.t : 0);
  U.todAt = todAt; ui.todAt = todAt;
  const OPEN = { providers: 'Open Providers', models: 'Open Models' };

  /* ---------- rows ---------- */
  function rowHtml(e, S, m) {
    const T = todAt(S, e), at = ' data-ag="' + esc(e.ag || '') + '"' + (e.task ? ' data-task="' + esc(e.task) + '"' : '');
    switch (e.k) {
      case 'you': return '<div class="msg you" data-ag="you"><div class="who">You <time>' + T + '</time></div><span class="' + (e.text.charAt(0) === '/' ? 'code' : '') + '">' + chip(e.text) + '</span></div>';
      case 'say': { const who = e.ag === 'mgr' ? 'Manager' : e.ag, col = agCol(e.ag); return '<div class="msg ' + (e.ag === 'mgr' ? 'mgr' : 'agsay') + '"' + at + ' style="--c:' + col + '"><div class="who">' + esc(who) + ' <time>' + T + '</time></div><span class="mt" ' + (e.stream ? 'data-full="' + esc(e.text) + '" data-t0="' + nm(e.t) + '" data-rate="' + nm(e.rate, 70) + '"' : '') + '>' + (e.stream ? '' : esc(e.text)) + '</span></div>'; }
      case 'sys': return '<div class="msg sys"' + (e.ag ? at : '') + '><span class="sg">' + esc(e.glyph || '◇') + '</span><span>' + esc(e.text) + (e.plan ? ' <span class="dim">' + nm(S.plan.length) + ' steps, see Goal plan</span>' : '') + (own(OPEN, e.open) ? ' <button class="btn sm" type="button" data-open-page="' + esc(e.open) + '">' + esc(OPEN[e.open]) + '</button>' : '') + '</span></div>';
      case 'tool': {
        if (e.refused) return '<div class="msg tool refused"' + at + '><span class="tg bad">⊘</span><span><b>' + esc(e.name) + '</b> ' + esc(e.arg || '') + ' <span class="bad">refused</span><span class="rsn">' + esc(e.reason || '') + '</span></span></div>';
        const fl = e.file ? ' data-file="' + esc(e.file) + '"' : '';
        return '<div class="msg tool' + (e.file ? ' openable' : '') + '"' + at + fl + (e.file ? ' title="open ' + esc(e.file) + ' in the Workspace"' : '') + '><span class="tg">' + TOOL_GLYPH(e.name) + '</span><span><b>' + esc(e.name) + '</b> ' + esc(e.arg || '') + (nm(e.add) ? ' <span class="ok">+' + nm(e.add) + '</span>' + (nm(e.del) ? ' <span class="err">−' + nm(e.del) + '</span>' : '') : '') + ' <span class="tg ok">✓</span>' + (e.out ? '<span class="out">' + esc(e.out) + '</span>' : '') + '</span></div>';
      }
      case 'feed': return '<div class="msg live" data-g="' + esc(e.g || '') + '"' + at + (e.to ? ' data-to="' + esc(e.to) + '"' : '') + ' style="--c:' + agCol(e.ag) + '"><span class="lid">' + esc(e.ag) + '</span><span class="lg ' + (e.g === 'ask' ? 'warm' : e.g === 'done' ? 'ok' : e.g === 'x' ? 'bad' : '') + '">' + (own(FEED_G, e.g) || '·') + '</span><span>' + esc(e.text) + '</span></div>';
      case 'note': return '<div class="msg note"' + at + '><span class="lg ' + (e.g === 'done' ? 'ok' : '') + '">' + (own(FEED_G, e.g) || '·') + '</span><span>' + esc(e.text) + '</span></div>';
      case 'st': return '<div class="msg st"' + at + '><span class="lg">' + (own(ui.SG, e.s) ? ui.SG[e.s][0] : '·') + '</span><span><b>' + esc(e.s) + '</b> ' + esc(e.text || '') + '</span></div>';
      case 'scouts': return '<div class="msg scouts">' + (Array.isArray(e.lines) ? e.lines : []).map(([id, t]) => '<div data-ag="' + esc(id) + '"><b style="color:' + agCol(id) + '">' + esc(id) + '</b><span class="ok">✓</span><span>' + esc(t) + '</span></div>').join('') + '</div>';
      case 'break': if (SL.settings.cache === 'quiet') return '<div class="msg break calm"' + at + ' title="' + esc('cache break (' + e.kind + '): ' + e.why) + '"><span class="lg warm">⚠</span><span class="bt"><b style="color:' + agCol(e.ag) + '">' + esc(e.ag) + '</b> cache break · ' + esc(e.why) + '</span></div>';
        return '<div class="msg break"' + at + '><b>⚠ cache break</b> (' + esc(e.kind) + ') · <b style="color:' + agCol(e.ag) + '">' + esc(e.ag) + '</b> · read ' + nm(e.read) + ' of ' + fmtK(nm(e.expected)) + ' expected · extra cost <span class="warm">≈ ' + fmtUsd(nm(e.expected) * 0.9 / 1e6, 4) + ' (est.)</span><em>' + esc(e.why) + '</em></div>';
      case 'compact': return '<div class="msg compact"' + at + '><b>◆ compacted</b><span class="num">' + fmtK(nm(e.from)) + ' → ' + fmtK(nm(e.to)) + '</span><span class="dim">' + (nm(e.pct) > 0 ? '+' : '−') + Math.abs(nm(e.pct)) + '%</span><span class="dim">· <span style="color:' + agCol(e.ag) + '">' + esc(e.ag) + '</span> · a declared, priced rebase</span></div>';
      case 'stream': return e.code ? '<div class="msg stream code"' + at + ' style="--c:' + agCol(e.ag) + '"><div class="who">' + esc(e.ag) + ' <time>writing</time></div><pre class="st" data-full="' + esc(e.text) + '" data-t0="' + nm(e.t) + '" data-rate="' + nm(e.rate, 70) + '"></pre></div>' : '<div class="msg stream"' + at + ' style="--c:' + agCol(e.ag) + '"><span class="lid">' + esc(e.ag) + '</span> <span class="st" data-full="' + esc(e.text) + '" data-t0="' + nm(e.t) + '" data-rate="' + nm(e.rate, 70) + '"></span></div>';
      case 'final': { const c = calc.totals(m); return '<div class="msg final">-- ' + Math.round(e.t) + 's · ' + (m.final ? m.final.steps : m.steps) + ' steps · ' + fmtUsd(c.cost, 2) + (SL.settings.cache === 'full' && c.prompt ? ' · cache hit ' + c.pct + '%' : '') + '</div>'; }
      case 'local': return '<div class="msg local"><div class="who">' + esc(e.title) + ' <time>' + T + '</time></div>' + e.html + '</div>';
      case 'mail': return '<div class="msg mailrow" data-ag="' + esc(e.from) + ' ' + esc(e.to) + '"><div class="who"><span style="color:' + agCol(e.from) + '">' + esc(e.from) + '</span> <span class="dim">→</span> <span style="color:' + agCol(e.to) + '">' + esc(e.to) + '</span> <time>' + T + '</time></div><span class="mailtxt">“' + esc(e.text) + '”</span></div>';
      case 'steer': return '<div class="msg steer" data-ag="' + esc(e.to) + '"><div class="who">You → <span style="color:' + agCol(e.to) + '">' + esc(e.to) + '</span> <time>' + T + '</time></div><span>' + esc(e.text) + '</span></div>';
      case 'ask': return '<div class="msg askrow"' + at + '><span class="lg warm">?</span><span><b>asks:</b> <span class="mono">' + esc(U.clip1(e.q.cmd, 200)) + '</span><span class="rsn">' + esc(U.clip1(e.q.why, 300)) + '</span></span></div>';
      case 'digest': { const d = e.dg, parts = []; if (d.merged.length) parts.push(d.merged.join(', ') + ' merged'); if (d.submitted.length) parts.push(d.submitted.filter((v, i, a) => a.indexOf(v) === i).join(', ') + ' submitted'); if (d.tools) parts.push(d.tools + ' tool calls'); if (d.mails) parts.push(d.mails + ' mail'); if (d.breaks) parts.push(d.breaks + ' cache break' + (d.breaks > 1 ? 's' : '')); if (d.compacts) parts.push(d.compacts + ' compaction' + (d.compacts > 1 ? 's' : '')); if (d.asks) parts.push(d.asks + ' question' + (d.asks > 1 ? 's' : ''));
        const span = d.t1 - d.t0, lab = span >= 90 ? Math.round(span / 60) + ' min' : Math.round(span) + ' s'; return '<div class="msg digest" data-eid="' + nm(e.id) + '"><button class="dtog" type="button" aria-expanded="false"><b>◆ while held · ' + lab + ':</b> ' + esc(parts.join(', ') || 'quiet') + ' <span class="dim">· ' + nm(d.n) + ' events</span><span class="dx">expand</span></button><ol class="drows" hidden data-lazy="1"></ol></div>'; }
    }
    return '';
  }

  /** An entry of the manager's log that is team activity (a worker's tool call, mail, a cache break, a compaction, a collapsed span): it is shown
   * read-only in the Team activity feed. Everything else is the conversation with the manager. */
  function isFeed(e) { return e.k === 'feed' || e.k === 'digest' || e.k === 'break' || e.k === 'compact' || (e.k === 'stream' && e.ag !== 'mgr'); }

  function mount(sc) {
    const talk = $('#talk'), feed = $('#feed'), pill = $('#newPill'), chip_ = $('#holdChip');
    /** a log view over the manager's channel: its own rows, scroll position and typing state (the chat and the Team activity feed are two of them) */
    const mkLog = (el, want, cap, none) => ({ el, want, cap, none, pos: 0, m: null, sid: null, mode: null, rows: [], stream: [], stick: true, newCount: 0, foldEl: null, nfold: 0, programmatic: false });
    const TR = mkLog(talk, e => !isFeed(e), ROW_CAP, 'Nothing yet. Type a message, or / for commands.'), FD = mkLog(feed, isFeed, 90, 'The team has not done anything yet.');

    /* ---------- the logs ---------- */
    const atEdge = L => L.el.scrollHeight - L.el.scrollTop - L.el.clientHeight < 40;
    const toEdge = L => { L.programmatic = true; L.el.scrollTop = L.el.scrollHeight; sc.raf(() => { L.programmatic = false; }); };
    function pillUpdate() { const n = TR.newCount; pill.hidden = !(n > 0 && !TR.stick); pill.textContent = n + ' new ↓'; }
    [TR, FD].forEach(L => {
      sc.listen(L.el, 'scroll', () => { if (!L.programmatic) { L.stick = atEdge(L); if (L.stick && L === TR) { TR.newCount = 0; pillUpdate(); } } });
      sc.listen(L.el, 'wheel', () => { L.programmatic = false; }, { passive: true });
    });
    sc.listen(pill, 'click', () => { TR.stick = true; TR.newCount = 0; pillUpdate(); toEdge(TR); });
    function addRow(L, e, S, m) {
      const el = frag(rowHtml(e, S, m)); if (!el) return null; const rec = { e, el }; L.el.appendChild(el); L.rows.push(rec);
      if (e.k === 'stream' || (e.k === 'say' && e.stream)) { const t = $('.st,.mt', el); if (t) { rec.t = t; rec.done = false; L.stream.push(rec); } }
      if (e.k === 'sys' && e.open) $$('[data-open-page]', el).forEach(b => b.addEventListener('click', () => ui.settingsPage(b.dataset.openPage)));
      return rec;
    }
    function foldOld(L) {
      const over = L.rows.length - L.cap; if (over <= 0) return;
      const gone = L.rows.splice(0, over); gone.forEach(r => r.el.remove()); L.stream = L.stream.filter(r => L.rows.includes(r)); L.nfold += over;
      if (!L.foldEl) { L.foldEl = mk('div', { class: 'msg foldmark' }); L.el.insertBefore(L.foldEl, L.el.firstChild); }
      L.foldEl.textContent = L.nfold + ' earlier rows folded (the log keeps them)';
    }
    /** Bring one log in line with the model: append new entries; rebuild after a session/model/cache-detail change. */
    function syncLog(L, S, m, force) {
      const list = m.chan.mgr || [], mode = SL.settings.cache;
      if (force || L.m !== m || L.sid !== S.id || L.mode !== mode || list.length < L.pos) {
        L.el.innerHTML = ''; L.rows = []; L.stream = []; L.foldEl = null; L.nfold = 0; L.pos = 0; L.m = m; L.sid = S.id; L.mode = mode; L.newCount = 0;
        let start = list.length; for (let n = 0, i = list.length - 1; i >= 0 && n < L.cap; i--) { start = i; if (!list[i].collapsed && L.want(list[i])) n++; }
        L.nfold = list.slice(0, start).filter(e => !e.collapsed && L.want(e)).length; L.pos = start;
        if (L.nfold) { L.foldEl = mk('div', { class: 'msg foldmark' }, L.nfold + ' earlier rows folded (the log keeps them)'); L.el.appendChild(L.foldEl); }
        if (!list.some(e => !e.collapsed && L.want(e))) L.el.appendChild(mk('div', { class: 'msg empty' }, L.none));
        const empty = $('.empty', L.el);
        while (L.pos < list.length) { const e = list[L.pos++]; if (e.collapsed || !L.want(e)) continue; if (empty && empty.parentNode) empty.remove(); addRow(L, e, S, m); }
        L.stick = true; toEdge(L); wireDigests(L); if (L === TR) pillUpdate(); return;
      }
      let added = 0, empty = $('.empty', L.el);
      while (L.pos < list.length) { const e = list[L.pos++]; if (e.collapsed || !L.want(e)) continue; if (empty && empty.parentNode) { empty.remove(); empty = null; } if (addRow(L, e, S, m)) added++; }
      if (added) { foldOld(L); wireDigests(L); if (L.stick) toEdge(L); else if (L === TR) { L.newCount += added; pillUpdate(); } }
    }
    function sync(force) {
      const S = SL.sessions.active; if (!S || !S.m) return; const m = S.m;
      chHead(S, m); syncLog(TR, S, m, force); syncLog(FD, S, m, force); feedHead(S);
    }
    function wireDigests(L) { $$('.digest .dtog:not([data-w])', L.el).forEach(b => { b.dataset.w = 1; sc.listen(b, 'click', () => { const o = b.nextElementSibling, open = o.hidden; if (open && o.dataset.lazy) { delete o.dataset.lazy; const S = SL.sessions.active, e = (S.m.chan.mgr || []).find(x => x.k === 'digest' && x.id === +b.closest('.digest').dataset.eid); if (e) o.innerHTML = e.dg.rows.slice(0, 60).map(r => '<li><time>' + todAt(S, r) + '</time> <span style="color:' + agCol(r.who || 'mgr') + '">' + esc(r.who || '') + '</span> ' + esc(r.k) + ' ' + esc(r.text || '') + '</li>').join('') + (e.dg.rows.length > 60 ? '<li class="dim">… ' + (e.dg.n - 60) + ' more</li>' : ''); } o.hidden = !open; b.setAttribute('aria-expanded', String(open)); $('.dx', b).textContent = open ? 'collapse' : 'expand'; }); }); }
    /** the task thread of an agent as a stepper: lease, work, submit, verify, merge (drawn in the read-only Details drawer) */
    function thread(m, A) {
      const T = A.task && m.tasks[A.task]; if (!T) return '<div class="tsteps off">no task: ' + esc(A.doing || '') + '</div>';
      const verifying = m.qHead && m.qHead.task === T.id, asks = A.state === 'ask', idx = T.st === 'merged' ? 5 : verifying ? 4 : T.st === 'verify' ? 3 : T.st === 'running' ? 2 : 1;
      const names = ['lease', 'work', 'submit', 'verify', 'merge'], deps = T.deps && T.deps.length ? T.deps.filter(d => m.tasks[d] && m.tasks[d].st !== 'merged') : [];
      return '<div class="tsteps" role="img" aria-label="' + esc(T.id + ' ' + T.title + ': ' + (T.st === 'merged' ? 'merged' : T.st === 'todo' ? 'waits' + (deps.length ? ' for ' + deps.join(' ') : '') : asks ? 'asks you' : names[Math.min(4, idx - 1)])) + '"><span class="tt" data-task="' + esc(T.id) + '"><b>' + esc(T.id) + '</b> ' + esc(T.title) + '</span><ol>' + names.map((n, i) => { const done = i + 1 < idx || idx === 5, cur = i + 1 === idx && idx < 5; return '<li class="' + (done ? 'done' : cur ? 'cur' + (asks ? ' ask' : '') : '') + '"><i aria-hidden="true">' + (done ? '✓' : cur ? (asks ? '?' : '●') : '·') + '</i>' + n + '</li>'; }).join('') + '</ol>' + (T.st === 'todo' && deps.length ? '<span class="dim tw">waits for ' + esc(deps.join(' ')) + '</span>' : '') + '</div>';
    }
    ui.thread = thread;
    /** the manager's header: the person talks to the manager only; the one button interrupts the running turn */
    function chHead(S, m) {
      let hd = $('#chHead'); if (!hd) { hd = mk('div', { id: 'chHead', class: 'chhead' }); talk.parentNode.parentNode.insertBefore(hd, $('#qSlot')); }
      const A = m.ag.mgr, st = A.spawned ? A.state : 'idle', g = ui.SG[st] ? ui.SG[st][0] : '·';
      const h = '<span class="agid" style="color:var(--c-mgr)">mgr</span><span class="st-' + st + ' mst" title="' + esc(A.doing || '') + '">' + g + ' ' + esc(A.doing || st) + '</span><span class="sp"></span><button class="btn sm" type="button" data-ch="turn" title="Esc: interrupt the turn and pause the goal">Interrupt turn</button>';
      if (hd._h !== h) { hd._h = h; hd.innerHTML = h; }
    }
    /** the Team activity section: read-only, collapsible; it never takes an input */
    const feedWrap = $('#feedWrap'), feedTog = $('#feedTog');
    function feedHead(S) {
      const open = S.ui.feedOpen !== false; feedWrap.classList.toggle('shut', !open); feedTog.setAttribute('aria-expanded', String(open)); feed.hidden = !open;
      feedTog.title = open ? 'Collapse Team activity' : 'Show Team activity';
    }
    sc.listen(feedTog, 'click', () => { const S = SL.sessions.active; S.ui.feedOpen = S.ui.feedOpen === false; feedHead(S); if (S.ui.feedOpen !== false) { FD.stick = true; toEdge(FD); } });
    sc.listen(talk, 'click', e => { const c = e.target.closest('[data-cli]'); if (c) { ui.runCli([c.dataset.cli], null, true); return; } const r = e.target.closest('.msg.tool[data-file]'); if (r && !window.getSelection().toString()) { ui.ws.open(r.dataset.file, 'changes'); } });
    sc.listen(feed, 'click', e => { const r = e.target.closest('.msg[data-ag]'); if (!r || window.getSelection().toString()) return; const id = (r.dataset.ag || '').split(' ')[0], S = SL.sessions.active; if (S && S.m && S.m.ag[id]) ui.openDrawer(id); });
    sc.listen(document.getElementById('rail'), 'click', e => { const b = e.target.closest('#chHead [data-ch]'); if (!b) return; const r = SL.act.interrupt('turn'); ui.toast(r.ok ? 'interrupted: the goal is paused' : (r.why || 'nothing to interrupt'), r.ok ? 'warm' : ''); });

    /* ---------- per-frame: streaming text, the hold chip, the held class ---------- */
    function frameFn(dt, vt, S) {
      if (!S || !S.m) return;
      [TR, FD].forEach(L => { for (let i = L.stream.length - 1; i >= 0; i--) { const r = L.stream[i], el = r.t; if (!el || el.dataset.full == null) continue; const growing = !!(r.e.mid && !r.e.done), full = r.e.mid ? r.e.text : el.dataset.full; const [s, typedAll] = ui.typed(full, +el.dataset.t0, +el.dataset.rate, vt), done = typedAll && !growing; const t = s + (done ? '' : '▍'); if (el._t !== t) { el._t = t; el.textContent = t; if (L.stick && !SL.time.held) toEdge(L); } if (done) { r.done = true; L.stream.splice(i, 1); } } });
      holdChip(S);
    }
    function holdChip(S) {
      const T = SL.time.T, held = SL.time.holdWanted(), n = S.unseen(), catching = T.catching, sig = (held ? (T.pinned ? 'p' : 'h') : catching ? 'c' : '') + n + '|' + Math.round(S.wt - S.vt);
      if (chip_._sig === sig) return; chip_._sig = sig;
      const on = held || catching; chip_.hidden = !on; document.body.classList.toggle('held', held && T.rate < .05);
      if (!on) { chip_.setAttribute('aria-pressed', 'false'); return; }
      chip_.setAttribute('aria-pressed', String(T.pinned)); chip_.className = 'holdchip' + (T.pinned ? ' pinned' : '') + (catching && !held ? ' catching' : '');
      chip_.textContent = held ? (T.pinned ? '◔ pinned · ' : '◔ holding · ') + n + ' new' : '▸▸ catching up · ' + Math.max(0, Math.round(S.wt - S.vt)) + 's';
      chip_.title = held ? (T.pinned ? 'Pinned: click or press Esc to release' : 'Holding while the pointer is over the chat. Click or press Space to pin; Esc releases.') : 'Replaying what happened while held, up to 6x';
    }
    sc.listen(chip_, 'click', () => togglePin());
    function togglePin() { const S = SL.sessions.active; if (!S) return; const on = !SL.time.T.pinned; S.hold.pinned = on; S.hold.vtSaved = on ? S.vt : null; SL.time.pin(on); holdChip(S); }
    ui.togglePin = togglePin;
    sc.on('hold', () => { const S = SL.sessions.active; if (S) { S.hold.pinned = SL.time.T.pinned; holdChip(S); } });
    sc.listen(talk, 'keydown', e => { if (e.key === ' ' && e.target === talk) { e.preventDefault(); togglePin(); } });

    /* ---------- plan box ---------- */
    const planBox = $('#planBox'), GL = { done: '✓', verify: '▸', act: '✎', edit: '✎', ask: '?', queued: '◌', pending: '◌' };
    const SHORT = { done: 'done', verify: 'verify', act: 'running', edit: 'editing', ask: 'asks you', queued: 'queued', pending: 'pending' };
    function renderPlan(S, m) {
      if (!S.meta.goalText && !(m.goal.objective && m.goal.state !== 'cleared') && !m.torder.length) { planBox.hidden = true; return; } planBox.hidden = false;
      const n = m.plan.filter(s => s === 'done').length, open = S.ui.planOpen, segs = m.plan.map(s => '<i class="' + (s === 'done' ? 'done' : ['verify', 'edit', 'act', 'ask'].includes(s) ? 'act' : '') + '"></i>').join('');
      const verdict = m.verdict ? '<div class="verdict ' + (m.goal.state === 'met' ? 'met' : '') + '"><b>judge</b>' + esc(m.verdict.replace(/`/g, '')) + '</div>' : '';
      const list = '<ul class="plist cp">' + S.plan.map((p, i) => '<li class="' + m.plan[i] + '"><span class="pg">' + GL[m.plan[i]] + '</span><span class="pt">' + (i + 1) + '. ' + esc(p) + '</span><span class="pc">' + (SHORT[m.plan[i]] || m.plan[i]) + '</span></li>').join('') + '</ul>';
      const html = '<button type="button" id="planTgl" aria-expanded="' + open + '"><span class="ptitle">Goal plan</span><span class="num dim" style="font-size:11px">' + n + '/' + S.plan.length + '</span><span class="pprog">' + segs + '</span><span class="dim" aria-hidden="true">' + (open ? '▾' : '▸') + '</span></button>' + (open ? '<div>' + list + verdict + '</div>' : '');
      if (planBox._h !== html) { planBox._h = html; planBox.innerHTML = html; }
    }
    sc.listen(planBox, 'click', e => { if (e.target.closest('#planTgl')) { const S = SL.sessions.active; S.ui.planOpen = !S.ui.planOpen; planBox._h = ''; renderPlan(S, S.m); } });

    /* ---------- composer ---------- */
    const inp = $('#input'), form = $('#composer'), SLASH = { open: false, items: [], idx: 0, mode: 'cmd' }, slashEl = $('#slash'), RS = { pasted: [], hi: -1, cc: 0 };
    const autosize = () => { inp.style.height = 'auto'; inp.style.height = Math.min(120, inp.scrollHeight) + 'px'; };
    function composerUpdate(S) {
      feed.classList.toggle('quiet', S.ui.verbose === false); const qh = S.ui.queued.map(q => '<div>' + esc(q.text) + '</div>').join(''), ql = $('#queuedLines'); if (ql._h !== qh) { ql._h = qh; ql.innerHTML = qh; }
      const ro = !!S.readOnly, t = S.placeholder ? 'start a session first: + New' : S.follow ? 'watching ' + S.sid + ' · read-only · the run belongs to another process' : S.recorded ? 'a recorded session is read-only: ↺ Resume continues it' : '';
      if (inp.disabled !== ro) { inp.disabled = ro; $('.send', form).disabled = ro; } if (inp.title !== t) inp.title = t;
    }
    function updateSlash() {
      const v = inp.value, pos = inp.selectionStart, at = /(^|\s)@([\w./-]*)$/.exec(v.slice(0, pos));
      if (at) { SLASH.mode = 'file'; const files = SL.palette.files(at[2]); SLASH.items = files.filter(f => f.includes(at[2])).slice(0, 8).map(f => ({ name: f })); SLASH.open = SLASH.items.length > 0; SLASH.idx = 0; renderSlash(); return; }
      if (v.charAt(0) === '/' && v.indexOf('\n') < 0 && v.indexOf(' ') < 0) { SLASH.mode = 'cmd'; SLASH.items = SL.palette.filter(v.slice(1), true).slice(0, 14); SLASH.open = true; SLASH.idx = Math.min(SLASH.idx, Math.max(0, SLASH.items.length - 1)); renderSlash(); return; }
      closeSlash();
    }
    function renderSlash() {
      slashEl.hidden = !SLASH.open; if (!SLASH.open) return;
      slashEl.innerHTML = SLASH.mode === 'file' ? '<ul class="cmds" role="presentation"><li class="grp">files · @ path completion</li>' + SLASH.items.map((c, i) => '<li><button class="cmd" type="button" role="option" aria-selected="' + (i === SLASH.idx) + '" data-i="' + i + '"><span class="cn">@' + esc(c.name) + '</span></button></li>').join('') + '</ul>' : SL.palette.listHtml(SLASH.items, SLASH.idx);
      const sel = slashEl.querySelector('[aria-selected="true"]'); if (sel) sel.scrollIntoView({ block: 'nearest' });
    }
    sc.listen(slashEl, 'mousedown', e => { const b = e.target.closest('.cmd'); if (b) { e.preventDefault(); pick(SLASH.items[+b.dataset.i], false); } });
    function closeSlash() { SLASH.open = false; slashEl.hidden = true; }
    function pick(c, run) {
      if (SLASH.mode === 'file') { const pos = inp.selectionStart, pre = inp.value.slice(0, pos).replace(/@[\w./-]*$/, '@' + c.name + ' '); inp.value = pre + inp.value.slice(pos); closeSlash(); inp.focus(); return; }
      closeSlash(); if (run && !c.args) { inp.value = ''; SL.palette.run(c, ''); } else { inp.value = c.name + ' '; inp.focus(); }
    }
    /** Put a line that was not sent back into the composer (when nothing new was typed there meanwhile), with its pasted text. */
    function restore(S, v, pasted) { if (S !== SL.sessions.active || inp.value.trim()) return false; inp.value = v; S.ui.draft = v; RS.pasted = pasted; autosize(); return true; }
    function submit() {
      const S = SL.sessions.active; let v = inp.value.trim(); if (!v) return;
      /* a replay never changes the session: the line stays in the composer */
      if (v.charAt(0) !== '/' && S.replay) { ui.toast('go live to talk: a replay never changes the session (the line is kept)', 'warm'); return; }
      const pasted = RS.pasted.slice(); inp.value = ''; S.ui.draft = ''; autosize(); closeSlash(); RS.pasted = []; RS.hi = -1; if (S.hist[S.hist.length - 1] !== v) S.hist.push(v);
      if (v.charAt(0) === '/') { SL.palette.runLine(v); return; }
      if (SL.time.holdWanted()) { SL.time.release(); SL.time.pin(false); S.hold.pinned = false; }
      /* the agent gets the pasted text; the transcript keeps the chips */
      const full = v.replace(/\[pasted text #(\d+) \+\d+ lines\]/g, (m0, n) => pasted[+n - 1] != null ? pasted[+n - 1] : m0);
      const r = SL.act.send(v, undefined, full !== v ? { text: full } : undefined);
      /* until the server took it, the line is not gone: refused, it comes back into the composer */
      if (r && r.ok === false) { restore(S, v, pasted); if (r.why) ui.toast(r.why, 'warm'); }
      else if (r && r.done) r.done.then(res => { if (res && res.ok === false && res.code !== 'aborted') { const back = restore(S, v, pasted); if (!back) ui.toast('not sent: ' + clip1(v, 60) + ' (↑ brings it back)', 'warm'); } });
      composerUpdate(S);
    }
    sc.listen(inp, 'input', () => { autosize(); SL.time.noteKey(); updateSlash(); const S = SL.sessions.active; if (S) S.ui.draft = inp.value; });
    sc.listen(inp, 'keydown', e => {
      SL.time.noteKey();
      if (SLASH.open) {
        if (e.key === 'ArrowDown') { e.preventDefault(); SLASH.idx = Math.min(SLASH.items.length - 1, SLASH.idx + 1); renderSlash(); return; }
        if (e.key === 'ArrowUp') { e.preventDefault(); SLASH.idx = Math.max(0, SLASH.idx - 1); renderSlash(); return; }
        if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) { if (SLASH.items.length) { e.preventDefault(); pick(SLASH.items[SLASH.idx], e.key === 'Enter'); return; } }
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeSlash(); return; }
      }
      if (e.key === 'Tab' && e.shiftKey) { e.preventDefault(); SL.act.cycleMode(); return; }
      if (e.key === 'Enter' && !e.shiftKey && !e.altKey) { const v = inp.value; if (v.endsWith('\\')) { e.preventDefault(); inp.value = v.slice(0, -1) + '\n'; autosize(); return; } e.preventDefault(); submit(); return; }
      if (e.key === 'Enter' && e.altKey) { e.preventDefault(); inp.setRangeText('\n', inp.selectionStart, inp.selectionEnd, 'end'); autosize(); return; }
      if (e.key === 'j' && e.ctrlKey) { e.preventDefault(); inp.setRangeText('\n', inp.selectionStart, inp.selectionEnd, 'end'); autosize(); return; }
      if (e.key === 'c' && e.ctrlKey && inp.selectionStart === inp.selectionEnd) { e.preventDefault(); const S = SL.sessions.active;
        if (inp.value) { inp.value = ''; autosize(); closeSlash(); ui.toast('line discarded (ctrl+c again at an empty prompt quits)'); }
        else if (S && !S.readOnly && (SL.calc.turnRunning(S.wm) || S.meta.running)) { RS.cc = 0; const r = SL.act.interrupt('turn'); ui.toast(r.ok ? 'interrupted: the turn is stopped and the goal is paused (/goal resume)' : (r.why || 'nothing is running to interrupt'), r.ok ? 'warm' : 'quiet'); }   /* as Esc does, and as the TUI does */
        else if (RS.cc && SL.time.wall - RS.cc < 1500) { RS.cc = 0; if (S) ui.closeSessionAsk(S); }   /* ctrl+c twice at an empty prompt closes the session (quitting a page's session is closing it) */
        else { RS.cc = SL.time.wall; ui.toast('ctrl+c again to quit'); } return; }
      if (e.key === 'r' && e.ctrlKey) { e.preventDefault(); ui.sheets.history(); return; }
      if ((e.key === 'ArrowUp' || e.key === 'ArrowDown') && !e.shiftKey) {
        const S = SL.sessions.active, h = S.hist, atTop = inp.selectionStart === 0 || inp.value.indexOf('\n') < 0, atEnd = inp.value.indexOf('\n') < 0 || inp.selectionStart === inp.value.length;
        if (e.key === 'ArrowUp' && atTop && h.length) { e.preventDefault(); RS.hi = Math.min(h.length - 1, RS.hi + 1); inp.value = h[h.length - 1 - RS.hi]; autosize(); } else if (e.key === 'ArrowDown' && atEnd && RS.hi >= 0) { e.preventDefault(); RS.hi--; inp.value = RS.hi < 0 ? '' : h[h.length - 1 - RS.hi]; autosize(); }
      }
    });
    sc.listen(inp, 'paste', e => { const t = (e.clipboardData || window.clipboardData || { getData() { return ''; } }).getData('text'), n = t.split('\n').length; if (n >= 5) { e.preventDefault(); RS.pasted.push(t); inp.setRangeText('[pasted text #' + RS.pasted.length + ' +' + n + ' lines]', inp.selectionStart, inp.selectionEnd, 'end'); autosize(); } });
    sc.listen(form, 'submit', e => { e.preventDefault(); submit(); });
    ui.setDraft = v => { inp.value = v; autosize(); inp.focus(); };
    ui.composeFocus = () => inp.focus();

    /* ---------- wiring ---------- */
    sc.update((S, m) => { if (!S || !m) return; sync(false); renderPlan(S, m); composerUpdate(S); if (inp.value !== S.ui.draft && document.activeElement !== inp) inp.value = S.ui.draft || ''; ui.approvals.render(S, S.wm); });
    sc.frame(frameFn);
    sc.on('activated', () => { TR.m = null; FD.m = null; sync(true); });
    sc.on('rebuilt', () => { TR.m = null; FD.m = null; });
    sc.on('cache-detail', () => { TR.m = null; FD.m = null; sync(true); });
    sc.on('files-ready', () => { if (SLASH.mode === 'file' && document.activeElement === inp) updateSlash(); });
    ui.chat = { sync, togglePin, TR, FD };
  }
  SL.chat = { mount, rowHtml, ROW_CAP };
})(SL);
