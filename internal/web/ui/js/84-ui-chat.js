/* 84-ui-chat.js: SL.chat, the Radio rail: the conversation with the manager (the person talks to the manager only), the read-only Team activity
 * feed below it, the plan box and the composer. A row is drawn as soon as its event reaches the view (live, the frame after it arrived),
 * wherever the pointer or the focus is; a log that is scrolled to its end stays at the end, one scrolled up counts what came below it.
 *
 * DOM budget: at most ROW_CAP rows exist per log; older rows are folded into one marker.
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
  /** The picture under a tool row that read or wrote an image of the project (as the file is now), in a session that has a workspace. A
   *  read names its file in its argument (the row's file is the file a tool changed). */
  function thumb(e, S) {
    const M = SL.media, f = e.file || (/^read$/i.test(String(e.name)) && typeof e.arg === 'string' ? e.arg : ''), k = f && M ? M.kind(f) : '';
    if ((k !== 'image' && k !== 'svg') || !S || S.id == null || S.id === '' || S.placeholder || S.recorded || S.follow || S.readOnly || !SL.api) return '';
    return M.thumbHtml(M.rawUrl(S.id, f, 'live', String(nm(e.t))), f);
  }
  function rowHtml(e, S, m) {
    const T = todAt(S, e), at = ' data-ag="' + esc(e.ag || '') + '"' + (e.task ? ' data-task="' + esc(e.task) + '"' : '');
    switch (e.k) {
      case 'you': return '<div class="msg you" data-ag="you"><div class="who">You <time>' + T + '</time></div><span class="' + (e.text.charAt(0) === '/' ? 'code' : '') + '">' + chip(e.text) + '</span></div>';
      case 'say': { const who = e.ag === 'mgr' ? 'Manager' : e.ag, col = agCol(e.ag); return '<div class="msg ' + (e.ag === 'mgr' ? 'mgr' : 'agsay') + '"' + at + ' style="--c:' + col + '"><div class="who">' + esc(who) + ' <time>' + T + '</time></div><span class="mt" ' + (e.stream ? 'data-full="' + esc(e.text) + '" data-t0="' + nm(e.t) + '" data-rate="' + nm(e.rate, 70) + '"' : '') + '>' + (e.stream ? '' : esc(e.text)) + '</span></div>'; }
      case 'sys': return '<div class="msg sys"' + (e.ag ? at : '') + '><span class="sg">' + esc(e.glyph || '◇') + '</span><span>' + esc(e.text) + (e.plan ? ' <span class="dim">' + nm(S.plan.length) + ' steps, see Goal plan</span>' : '') + (own(OPEN, e.open) ? ' <button class="btn sm" type="button" data-open-page="' + esc(e.open) + '">' + esc(OPEN[e.open]) + '</button>' : '') + '</span></div>';
      case 'tool': {
        if (e.refused) return '<div class="msg tool refused"' + at + '><span class="tg bad">⊘</span><span class="tb"><b>' + esc(e.name) + '</b> ' + esc(e.arg || '') + ' <span class="bad">refused</span><span class="rsn">' + esc(e.reason || '') + '</span></span></div>';
        const fl = e.file ? ' data-file="' + esc(e.file) + '"' : '';
        return '<div class="msg tool' + (e.file ? ' openable' : '') + '"' + at + fl + (e.file ? ' title="open ' + esc(e.file) + ' in the Workspace"' : '') + '><span class="tg">' + TOOL_GLYPH(e.name) + '</span><span class="tb"><b>' + esc(e.name) + '</b> ' + esc(e.arg || '') + (nm(e.add) ? ' <span class="ok">+' + nm(e.add) + '</span>' + (nm(e.del) ? ' <span class="err">−' + nm(e.del) + '</span>' : '') : '') + (e.out ? '<span class="out">' + esc(e.out) + '</span>' : '') + thumb(e, S) + '</span><span class="ts ok">✓</span></div>';
      }
      case 'feed': return '<div class="msg live" data-g="' + esc(e.g || '') + '"' + at + (e.to ? ' data-to="' + esc(e.to) + '"' : '') + ' style="--c:' + agCol(e.ag) + '"><span class="lid">' + esc(e.ag) + '</span><span class="lg ' + (e.g === 'ask' ? 'warm' : e.g === 'done' ? 'ok' : e.g === 'x' ? 'bad' : '') + '">' + (own(FEED_G, e.g) || '·') + '</span><span>' + esc(e.text) + '</span></div>';
      case 'note': return '<div class="msg note"' + at + '><span class="lg ' + (e.g === 'done' ? 'ok' : '') + '">' + (own(FEED_G, e.g) || '·') + '</span><span>' + esc(e.text) + '</span></div>';
      case 'st': return '<div class="msg st"' + at + '><span class="lg">' + (own(ui.SG, e.s) ? ui.SG[e.s][0] : '·') + '</span><span><b>' + esc(e.s) + '</b> ' + esc(e.text || '') + '</span></div>';
      case 'scouts': return '<div class="msg scouts">' + (Array.isArray(e.lines) ? e.lines : []).map(([id, t]) => '<div data-ag="' + esc(id) + '"><b style="color:' + agCol(id) + '">' + esc(id) + '</b><span class="ok">✓</span><span>' + esc(t) + '</span></div>').join('') + '</div>';
      /* a quiet break (wire.Break.Quiet) is drawn with qbrk: the feed shows it only with /verbose on */
      case 'break': { const qb = e.quiet ? ' qbrk' : '', lay = e.layer ? ' · layer ' + esc(e.layer) : ''; if (SL.settings.cache === 'quiet') return '<div class="msg break calm' + qb + '"' + at + ' title="' + esc('cache break (' + e.kind + (e.layer ? ', layer ' + e.layer : '') + '): ' + e.why) + '"><span class="lg warm">⚠</span><span class="bt"><b style="color:' + agCol(e.ag) + '">' + esc(e.ag) + '</b> cache break · ' + esc(e.why) + '</span></div>';
        return '<div class="msg break' + qb + '"' + at + '><b>⚠ cache break</b> (' + esc(e.kind) + lay + ') · <b style="color:' + agCol(e.ag) + '">' + esc(e.ag) + '</b> · read ' + nm(e.read) + ' of ' + fmtK(nm(e.expected)) + ' expected · extra cost <span class="warm">≈ ' + fmtUsd(e.missUsd > 0 ? e.missUsd : nm(e.expected) * 0.9 / 1e6, 4) + (e.missUsd > 0 ? '' : ' (est.)') + '</span><em>' + esc(e.why) + '</em></div>'; }
      case 'compact': { const note = calc.compactNote(e, false); return '<div class="msg compact"' + at + '><b>◆ compacted</b><span class="num">' + fmtK(nm(e.from)) + ' → ' + fmtK(nm(e.to)) + '</span><span class="dim">' + (nm(e.pct) > 0 ? '+' : '−') + Math.abs(nm(e.pct)) + '%</span><span class="dim">· <span style="color:' + agCol(e.ag) + '">' + esc(e.ag) + '</span>' + (note ? ' · ' + note : '') + '</span></div>'; }
      case 'stream': return e.code ? '<div class="msg stream code"' + at + ' style="--c:' + agCol(e.ag) + '"><div class="who">' + esc(e.ag) + ' <time>writing</time></div><pre class="st" data-full="' + esc(e.text) + '" data-t0="' + nm(e.t) + '" data-rate="' + nm(e.rate, 70) + '"></pre></div>' : '<div class="msg stream"' + at + ' style="--c:' + agCol(e.ag) + '"><span class="lid">' + esc(e.ag) + '</span> <span class="st" data-full="' + esc(e.text) + '" data-t0="' + nm(e.t) + '" data-rate="' + nm(e.rate, 70) + '"></span></div>';
      case 'final': { const c = calc.totals(m); return '<div class="msg final">-- ' + Math.round(e.t) + 's · ' + (m.final ? m.final.steps : m.steps) + ' steps · ' + U.fmtSpend(c, 2) + U.planTag(c) + (SL.settings.cache === 'full' && c.prompt ? ' · cache hit ' + c.pct + '%' : '') + '</div>'; }
      case 'local': return '<div class="msg local"><div class="who">' + esc(e.title) + ' <time>' + T + '</time></div>' + e.html + '</div>';
      case 'mail': return '<div class="msg mailrow" data-ag="' + esc(e.from) + ' ' + esc(e.to) + '"><div class="who"><span style="color:' + agCol(e.from) + '">' + esc(e.from) + '</span> <span class="dim">→</span> <span style="color:' + agCol(e.to) + '">' + esc(e.to) + '</span> <time>' + T + '</time></div><span class="mailtxt">“' + esc(e.text) + '”</span></div>';
      case 'steer': return '<div class="msg steer" data-ag="' + esc(e.to) + '"><div class="who">You → <span style="color:' + agCol(e.to) + '">' + esc(e.to) + '</span> <time>' + T + '</time></div><span>' + esc(e.text) + '</span></div>';
      /* the manager's answers to the workers' routine questions: one collapsed line that grows while no other row comes between */
      case 'mgrdec': return '<div class="msg mgrdec">' + (ui.approvals ? ui.approvals.mgrHtml(Array.isArray(e.list) ? e.list : [], !!e.open, 'qmgr', typeof e.n === 'number' ? { n: e.n, d: e.nd || 0 } : null) : '') + '</div>';
      case 'ask': return '<div class="msg askrow"' + at + '><span class="lg warm">?</span><span><b>asks:</b> <span class="mono">' + esc(U.clip1(e.q.cmd, 200)) + '</span><span class="rsn">' + esc(U.clip1(e.q.why, 300)) + '</span></span></div>';
    }
    return '';
  }

  /** An entry of the manager's log that is team activity (a worker's tool call, mail, a cache break, a compaction): it is shown
   * read-only in the Team activity feed. Everything else is the conversation with the manager. */
  function isFeed(e) { return e.k === 'feed' || e.k === 'break' || e.k === 'compact' || (e.k === 'stream' && e.ag !== 'mgr'); }
  /** What the harness says about itself rather than about the work: the start-up lines (starting…, ready: model · mode · session) and
   *  the warnings of loading the configuration and the extensions (a skill whose name differs from its directory, a hook, a command,
   *  an agent definition, settings of the project that were ignored, a flag that has no effect). They go to the Notices line above the
   *  conversation, collapsed, never into the conversation itself. */
  const NOTICE_RE = /^(skills?|hooks?|commands?|agents?|roles?|mcp|config|plugins?|settings|output[- ]styles?|rules|permissions|trust)\b[^:]{0,240}:|^ignored security-sensitive settings|^--[a-z][a-z-]* (has no effect|is ignored)/i;
  function isNotice(e) { return e.k === 'sys' && !e.ag && !e.open && !e.plan && typeof e.text === 'string' && (e.text === 'starting…' || /^ready: /.test(e.text) || NOTICE_RE.test(e.text)); }

  function mount(sc) {
    const talk = $('#talk'), feed = $('#feed'), pill = $('#newPill');
    /** a log view over the manager's channel: its own rows, scroll position and typing state (the chat and the Team activity feed are two of them) */
    const mkLog = (el, want, cap, none) => ({ el, want, cap, none, pos: 0, m: null, sid: null, mode: null, rows: [], stream: [], stick: true, newCount: 0, foldEl: null, nfold: 0, programmatic: false });
    const TR = mkLog(talk, e => !isFeed(e) && !isNotice(e), ROW_CAP, 'Your conversation with the manager appears here. Write below what you want done and press Enter; / lists the commands.'), FD = mkLog(feed, isFeed, 90, 'What the workers do (their tool calls, edits, mail and merges) appears here once the manager hands them work.');

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
      if (e.k === 'mgrdec') { rec.v = e.v; L.dec = rec; }
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
        L.el.innerHTML = ''; L.rows = []; L.stream = []; L.dec = null; L.foldEl = null; L.nfold = 0; L.pos = 0; L.m = m; L.sid = S.id; L.mode = mode; L.newCount = 0;
        let start = list.length; for (let n = 0, i = list.length - 1; i >= 0 && n < L.cap; i--) { start = i; if (L.want(list[i])) n++; }
        L.nfold = list.slice(0, start).filter(e => L.want(e)).length; L.pos = start;
        if (L.nfold) { L.foldEl = mk('div', { class: 'msg foldmark' }, L.nfold + ' earlier rows folded (the log keeps them)'); L.el.appendChild(L.foldEl); }
        if (!list.some(e => L.want(e))) L.el.appendChild(mk('div', { class: 'msg empty' }, L.none));
        const empty = $('.empty', L.el);
        while (L.pos < list.length) { const e = list[L.pos++]; if (!L.want(e)) continue; if (empty && empty.parentNode) empty.remove(); addRow(L, e, S, m); }
        L.stick = true; toEdge(L); if (L === TR) pillUpdate(); return;
      }
      let added = 0, empty = $('.empty', L.el);
      /* the manager's line of decisions grew: drawn again in place, open if it was open */
      const dec = L.dec; if (dec && dec.v !== dec.e.v && dec.el.parentNode) { const t = $('[data-mgrtog]', dec.el); dec.e.open = !!t && t.getAttribute('aria-expanded') === 'true'; const nx = frag(rowHtml(dec.e, S, m)); if (nx) { dec.el.parentNode.replaceChild(nx, dec.el); dec.el = nx; } dec.v = dec.e.v; }
      while (L.pos < list.length) { const e = list[L.pos++]; if (!L.want(e)) continue; if (empty && empty.parentNode) { empty.remove(); empty = null; } if (addRow(L, e, S, m)) added++; }
      if (added) { foldOld(L); if (L.stick) toEdge(L); else if (L === TR) { L.newCount += added; pillUpdate(); } }
    }
    function sync(force) {
      const S = SL.sessions.active; if (!S || !S.m) return; const m = S.m;
      chHead(S, m); syncNotices(S, m, force); syncLog(TR, S, m, force); syncLog(FD, S, m, force); feedHead(S);
    }
    /* ---------- the Notices line: the harness's own start-up lines and warnings, one collapsed line above the conversation ---------- */
    const nBar = $('#noticeBar'), NT = { pos: 0, m: null, sid: null, list: [], sig: '' };
    function syncNotices(S, m, force) {
      if (!nBar) return; const list = m.chan.mgr || [];
      if (force || NT.m !== m || NT.sid !== S.id || list.length < NT.pos) { NT.m = m; NT.sid = S.id; NT.pos = 0; NT.list = []; }
      while (NT.pos < list.length) { const e = list[NT.pos++]; if (isNotice(e)) NT.list.push(e); }
      const open = !!S.ui.noticesOpen, warn = NT.list.filter(e => e.glyph === '⚠').length, sig = S.id + '|' + NT.list.length + '|' + open; if (NT.sig === sig) return; NT.sig = sig;
      nBar.hidden = !NT.list.length; if (!NT.list.length) { nBar.innerHTML = ''; return; }
      const sum = warn ? warn + ' warning' + (warn === 1 ? '' : 's') + ' from loading the configuration' : 'start-up messages, nothing to fix';
      nBar.classList.toggle('warn', !!warn);
      nBar.innerHTML = '<button type="button" class="nb-tog" data-ntog aria-expanded="' + open + '" aria-controls="noticeList" title="' + (open ? 'Hide' : 'Show') + ' what Sleipnir said while it started this session"><span class="nb-g" aria-hidden="true">' + (warn ? '⚠' : 'ⓘ') + '</span><span class="nb-t">Notices</span><span class="nb-s">' + esc(sum) + '</span><span class="nb-c" aria-hidden="true">' + (open ? '▾' : '▸') + '</span></button>' +
        (open ? '<ul class="nb-list" id="noticeList">' + NT.list.map(e => '<li class="' + (e.glyph === '⚠' ? 'warm' : '') + '"><span class="lg" aria-hidden="true">' + esc(e.glyph || '◇') + '</span><span>' + esc(e.text) + '</span></li>').join('') + '</ul>' : '');
    }
    if (nBar) sc.listen(nBar, 'click', e => { if (!e.target.closest('[data-ntog]')) return; const S = SL.sessions.active; if (!S) return; S.ui.noticesOpen = !S.ui.noticesOpen; NT.sig = ''; syncNotices(S, S.m, false); });
    /** the task thread of an agent as a stepper: lease, work, submit, verify, merge (drawn in the read-only Details drawer) */
    function thread(m, A) {
      const T = A.task && m.tasks[A.task]; if (!T) return '<div class="tsteps off">no task: ' + esc(A.doing || '') + '</div>';
      const verifying = m.qHead && m.qHead.task === T.id, asks = A.state === 'ask', idx = T.st === 'merged' ? 5 : verifying ? 4 : T.st === 'verify' ? 3 : T.st === 'running' ? 2 : 1;
      const names = ['lease', 'work', 'submit', 'verify', 'merge'], deps = T.deps && T.deps.length ? T.deps.filter(d => m.tasks[d] && m.tasks[d].st !== 'merged') : [];
      return '<div class="tsteps" role="img" aria-label="' + esc(T.id + ' ' + T.title + ': ' + (T.st === 'merged' ? 'merged' : T.st === 'todo' ? 'waits' + (deps.length ? ' for ' + deps.join(' ') : '') : asks ? 'asks you' : names[Math.min(4, idx - 1)])) + '"><span class="tt" data-task="' + esc(T.id) + '"><b>' + esc(T.id) + '</b> ' + esc(T.title) + '</span><ol>' + names.map((n, i) => { const done = i + 1 < idx || idx === 5, cur = i + 1 === idx && idx < 5; return '<li class="' + (done ? 'done' : cur ? 'cur' + (asks ? ' ask' : '') : '') + '"><i aria-hidden="true">' + (done ? '✓' : cur ? (asks ? '?' : '●') : '·') + '</i>' + n + '</li>'; }).join('') + '</ol>' + (T.st === 'todo' && deps.length ? '<span class="dim tw">waits for ' + esc(deps.join(' ')) + '</span>' : '') + '</div>';
    }
    ui.thread = thread;
    /** the line that interrupts the running turn: shown only while a turn runs (what the manager does is the status bar's) */
    function chHead(S, m) {
      let hd = $('#chHead'); if (!hd) { hd = mk('div', { id: 'chHead', class: 'chhead' }); talk.parentNode.parentNode.insertBefore(hd, $('#qSlot')); }
      const run = !S.readOnly && turnBusy(S); hd.hidden = !run;
      const h = run ? '<span class="dim">the manager is working on your message</span><span class="sp"></span><button class="btn sm" type="button" data-ch="turn" title="interrupt the turn and pause the goal">Interrupt turn <kbd>esc</kbd></button>' : '';
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
    sc.listen(document.getElementById('rail'), 'click', e => { const b = e.target.closest('#chHead [data-ch]'); if (!b) return; const S = SL.sessions.active, r = SL.act.interrupt('turn'); ui.toast(r.ok ? SL.interruptText(S) : (r.why || 'nothing to interrupt'), r.ok ? 'warm' : ''); });

    /* ---------- per-frame: streaming text ---------- */
    function frameFn(dt, vt, S) {
      if (!S || !S.m) return;
      [TR, FD].forEach(L => { for (let i = L.stream.length - 1; i >= 0; i--) { const r = L.stream[i], el = r.t; if (!el || el.dataset.full == null) continue; const growing = !!(r.e.mid && !r.e.done), full = r.e.mid ? r.e.text : el.dataset.full; const [s, typedAll] = ui.typed(full, +el.dataset.t0, +el.dataset.rate, vt), done = typedAll && !growing; const t = s + (done ? '' : '▍'); if (el._t !== t) { el._t = t; el.textContent = t; if (L.stick) toEdge(L); } if (done) { r.done = true; L.stream.splice(i, 1); } } });
    }

    /* ---------- plan box ---------- */
    const planBox = $('#planBox'), GL = { done: '✓', verify: '▸', act: '✎', edit: '✎', ask: '?', queued: '◌', pending: '◌' };
    const SHORT = { done: 'done', verify: 'verify', act: 'running', edit: 'editing', ask: 'asks you', queued: 'queued', pending: 'pending' };
    function renderPlan(S, m) {
      if (!S.meta.goalText && !(m.goal.objective && m.goal.state !== 'cleared') && !m.torder.length) { planBox.hidden = true; return; } planBox.hidden = false;
      const n = m.plan.filter(s => s === 'done').length, open = S.ui.planOpen, segs = m.plan.map(s => '<i class="' + (s === 'done' ? 'done' : ['verify', 'edit', 'act', 'ask'].includes(s) ? 'act' : '') + '"></i>').join('');
      const ms = m.goal.milestones || [], msTag = ms.length ? '<span class="num dim" style="font-size:11px" title="milestones the judge found met">· ' + ms.filter(x => x.met).length + '/' + ms.length + ' milestones</span>' : '';
      const verdict = m.verdict ? '<div class="verdict ' + (m.goal.state === 'met' ? 'met' : '') + '"><b>judge</b>' + esc(m.verdict.replace(/`/g, '')) + '</div>' : '';
      const list = '<ul class="plist cp">' + S.plan.map((p, i) => '<li class="' + m.plan[i] + '"><span class="pg">' + GL[m.plan[i]] + '</span><span class="pt">' + (i + 1) + '. ' + esc(p) + '</span><span class="pc">' + (SHORT[m.plan[i]] || m.plan[i]) + '</span></li>').join('') + '</ul>';
      const html = '<button type="button" id="planTgl" aria-expanded="' + open + '"><span class="ptitle">Goal plan</span><span class="num dim" style="font-size:11px">' + n + '/' + S.plan.length + '</span>' + msTag + '<span class="pprog">' + segs + '</span><span class="dim" aria-hidden="true">' + (open ? '▾' : '▸') + '</span></button>' + (open ? '<div>' + list + verdict + '</div>' : '');
      if (planBox._h !== html) { planBox._h = html; planBox.innerHTML = html; }
    }
    sc.listen(planBox, 'click', e => { if (e.target.closest('#planTgl')) { const S = SL.sessions.active; S.ui.planOpen = !S.ui.planOpen; planBox._h = ''; renderPlan(S, S.m); } });

    /* ---------- composer ---------- */
    const inp = $('#input'), form = $('#composer'), SLASH = { open: false, items: [], idx: 0, mode: 'cmd' }, slashEl = $('#slash'), RS = { pasted: [], hi: -1 };
    const autosize = () => { inp.style.height = 'auto'; inp.style.height = Math.min(120, inp.scrollHeight) + 'px'; };
    /** A queued line: its text and, once the server has it, Send now (a message only: it steers the running turn), Edit (takes it out and
     *  puts it back in the composer) and × (takes it out). An action of the session (a goal change) has no buttons: it stays. */
    function queuedHtml(q) {
      const k = q.kind || 'message', can = q.id && !q.local && k !== 'action', t = clip1(q.text, 80);
      return '<div class="ql" data-qid="' + esc(q.id || '') + '"><span class="qt" title="' + esc(q.text) + '">' + esc(q.text) + '</span>' + (can ? '<span class="qb">' +
        (k === 'message' ? '<button type="button" data-qa="now" title="send it to the running turn now, as a steer: the agent reads it with its next step" aria-label="Send now: ' + esc(t) + '">Send now</button>' : '') +
        '<button type="button" data-qa="edit" title="take it out of the queue and back into the message box" aria-label="Edit: ' + esc(t) + '">Edit</button>' +
        '<button type="button" data-qa="drop" class="qx" title="remove it from the queue" aria-label="Remove from the queue: ' + esc(t) + '">×</button></span>' : '') + '</div>';
    }
    const turnBusy = S => SL.calc.turnRunning(S.wm) || S.meta.running === true;
    /** Edit a queued line: it leaves the queue, and its whole text comes into the composer (ahead of what is typed there). */
    function editQueued(S, q) {
      const r = SL.act.dropQueued(q.id); if (!r || r.ok === false) { if (r && r.why) ui.toast(r.why, 'warm'); return; }
      r.done.then(res => { if (!res || !res.ok || !res.data) return;
        if (S !== SL.sessions.active) { const draft = S.ui.draft || ''; S.ui.draft = res.data.text + (draft.trim() ? '\n' + draft : ''); return; }
        const cur = inp.value.trim(); inp.value = res.data.text + (cur ? '\n' + inp.value : ''); S.ui.draft = inp.value; autosize(); inp.focus(); });
    }
    sc.listen($('#queuedLines'), 'click', e => {
      const b = e.target.closest('[data-qa]'), row = b && b.closest('.ql'), S = SL.sessions.active; if (!b || !row || !S) return;
      const q = S.ui.queued.find(x => x.id && x.id === row.dataset.qid); if (!q) return;
      if (b.dataset.qa === 'edit') { editQueued(S, q); return; }
      const r = b.dataset.qa === 'now' ? SL.act.sendQueuedNow(q.id) : SL.act.dropQueued(q.id); if (r && r.ok === false && r.why) ui.toast(r.why, 'warm');
    });
    /** Ctrl+Enter while a turn runs: what the composer holds goes to the running turn as a steer (/steer), not into the queue. */
    function steerNow() {
      const S = SL.sessions.active, v = inp.value.trim(); if (!S || !v) return;
      if (!turnBusy(S) || S.readOnly) { submit(); return; }
      if (v.charAt(0) === '/') { ui.toast('a command cannot steer: Enter queues it', 'warm'); return; }
      const pasted = RS.pasted.slice(), full = v.replace(/\[pasted text #(\d+) \+\d+ lines\]/g, (m0, n) => pasted[+n - 1] != null ? pasted[+n - 1] : m0);
      const r = SL.act.steer('mgr', full); if (!r || r.ok === false) { if (r && r.why) ui.toast(r.why, 'warm'); return; }
      inp.value = ''; S.ui.draft = ''; autosize(); closeSlash(); RS.pasted = []; RS.hi = -1; if (S.hist[S.hist.length - 1] !== v) S.hist.push(v);
      r.done.then(res => { if (res && res.ok === false) restore(S, v, pasted); });
    }
    /* while a turn runs the hint says what Enter and Ctrl+Enter do: short in the bar, the whole sentence in its title */
    const HINT_IDLE = '/ commands', HINT_BUSY = 'Enter queues · Ctrl+Enter steers', HINT_BUSY_T = 'Enter queues for after this turn · Ctrl+Enter sends now (steer)';
    function composerUpdate(S) {
      feed.classList.toggle('quiet', S.ui.verbose === false); feed.classList.toggle('verbose', S.ui.verbose === true); const qh = S.ui.queued.map(queuedHtml).join(''), ql = $('#queuedLines'); if (ql._h !== qh) { ql._h = qh; ql.innerHTML = qh; }
      const hint = $('#cHint'), ht = !S.readOnly && turnBusy(S) ? HINT_BUSY : HINT_IDLE; if (hint && hint.textContent !== ht) { hint.textContent = ht; hint.title = ht === HINT_BUSY ? HINT_BUSY_T : ''; }
      paintDelivery(S);
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
      /* a slash line that is an action of the session (/goal TEXT) comes back into the composer when it is refused, as a message does */
      if (v.charAt(0) === '/') { const r = SL.palette.runLine(v); if (r && r.ok === false) restore(S, v, pasted); else if (r && r.done) r.done.then(res => { if (res && res.ok === false && res.code !== 'aborted' && !restore(S, v, pasted)) ui.toast('not sent: ' + clip1(v, 60) + ' (↑ brings it back)', 'warm'); }); return; }
      /* the agent gets the pasted text; the transcript keeps the chips */
      const full = v.replace(/\[pasted text #(\d+) \+\d+ lines\]/g, (m0, n) => pasted[+n - 1] != null ? pasted[+n - 1] : m0);
      const off = !!deliveryBlock(S), delivery = { autoFix: !off && !!$('#autoFixCI').checked, autoMerge: !off && !!$('#autoMergePR').checked };
      const r = SL.act.send(v, undefined, Object.assign({ text: full }, delivery));
      if (r && r.ok !== false) { S.ui.delivery = {}; $('#autoFixCI').checked = false; $('#autoMergePR').checked = false; }
      /* until the server took it, the line is not gone: refused, it comes back into the composer */
      if (r && r.ok === false) { restore(S, v, pasted); if (r.why) ui.toast(r.why, 'warm'); }
      else if (r && r.done) r.done.then(res => { if (res && res.ok === false && res.code !== 'aborted') { const back = restore(S, v, pasted); if (back) { S.ui.delivery = delivery; $('#autoFixCI').checked = delivery.autoFix; $('#autoMergePR').checked = delivery.autoMerge; } if (!back) ui.toast('not sent: ' + clip1(v, 60) + ' (↑ brings it back)', 'warm'); } });
      composerUpdate(S);
    }
    ['autoFixCI','autoMergePR'].forEach(id => sc.listen($('#'+id), 'change', () => { const S = SL.sessions.active; if (S) S.ui.delivery = { autoFix: !!$('#autoFixCI').checked, autoMerge: !!$('#autoMergePR').checked }; }));
    /* ---------- the GitHub switches: what they do, and why they are off when they cannot work ---------- */
    const DLV_ON = 'GitHub, for your next message only: open a pull request, fix CI and reviews, merge when green.';
    const DLV_TIP = { autoFixCI: 'Sleipnir opens a pull request for the work of your next message, then fixes failing CI checks and review comments until they pass (needs the gh command signed in to GitHub)', autoMergePR: 'Sleipnir merges that pull request on GitHub once its checks and the repository\'s rules pass (needs the gh command signed in to GitHub)' };
    /** Why the switches cannot be used now ('' when they can): the Delivery plugin is off, or the session is read-only. Whether the gh
     *  command is signed in is not known to the page (no route or field reports it); a send whose GitHub step fails says so in the chat. */
    function deliveryBlock(S) {
      if (SL.plugins && !SL.plugins.on('delivery')) return 'plugin';
      if (S.readOnly) return 'ro';
      const dp = SL.plugins && SL.plugins.find && SL.plugins.find('delivery'); if (dp && dp.unavailable) return 'gh';   // the server found no gh command
      return '';
    }
    const DLV = { sig: '' };
    function paintDelivery(S) {
      const why = deliveryBlock(S), sig = S.id + '|' + why; if (DLV.sig === sig) return; DLV.sig = sig;
      const fx = $('#autoFixCI'), mg = $('#autoMergePR'), line = $('#deliveryWhy'), box = $('#deliveryCtl'); if (!fx || !mg || !line) return;
      fx.disabled = mg.disabled = !!why; if (box) box.classList.toggle('off', !!why);
      if (why) { fx.checked = mg.checked = false; if (S.ui.delivery && (S.ui.delivery.autoFix || S.ui.delivery.autoMerge)) S.ui.delivery = {}; }
      const reason = why === 'plugin' ? 'Off: the Delivery plugin is turned off. ' : why === 'ro' ? 'Off: a read-only session starts no GitHub work. ' : why === 'gh' ? 'Off: GitHub delivery needs the gh command, which is not installed. ' : '';
      [['autoFixLab', 'autoFixCI'], ['autoMergeLab', 'autoMergePR']].forEach(([l, id]) => { const el = $('#' + l); if (el) el.title = reason + DLV_TIP[id]; });
      line.innerHTML = why === 'plugin' ? 'Off: turn on the Delivery plugin to let Sleipnir open, fix and merge pull requests (needs gh signed in to GitHub). <button type="button" class="linkbtn" data-dlv="plugins">Open Plugins</button>'
        : why === 'ro' ? 'Off: a read-only session starts no GitHub work.'
        : why === 'gh' ? 'Off: GitHub delivery needs the <span class="mono">gh</span> command, which is not installed on the machine running Sleipnir. Install it and run <span class="mono">gh auth login</span>.' : esc(DLV_ON);
    }
    sc.listen(form, 'click', e => { if (e.target.closest('[data-dlv="plugins"]')) { e.preventDefault(); if (ui.nav) { $('#app').dataset.pv = 'main'; ui.nav.go('plugins'); } else SL.views.show('plugins'); } });
    sc.on('plugins-changed', () => { DLV.sig = ''; const S = SL.sessions.active; if (S) paintDelivery(S); });
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
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey) { e.preventDefault(); steerNow(); return; }
      if (e.key === 'Enter' && !e.shiftKey && !e.altKey) { const v = inp.value; if (v.endsWith('\\')) { e.preventDefault(); inp.value = v.slice(0, -1) + '\n'; autosize(); return; } e.preventDefault(); submit(); return; }
      if (e.key === 'Enter' && e.altKey) { e.preventDefault(); inp.setRangeText('\n', inp.selectionStart, inp.selectionEnd, 'end'); autosize(); return; }
      if (e.key === 'j' && e.ctrlKey) { e.preventDefault(); inp.setRangeText('\n', inp.selectionStart, inp.selectionEnd, 'end'); autosize(); return; }
      if (e.key === 'r' && e.ctrlKey) { e.preventDefault(); ui.sheets.history(); return; }
      if (e.key === 'ArrowUp' && !e.shiftKey && !inp.value && SL.sessions.active) {
        /* as in the terminal: Up on an empty composer takes the last queued line back to edit it, before the history */
        const S = SL.sessions.active, q = S.ui.queued.slice().reverse().find(x => x.id && !x.local && (x.kind || 'message') !== 'action');
        if (q && !S.readOnly) { e.preventDefault(); editQueued(S, q); return; }
      }
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
    sc.on('delivery-choice', e => { const S = SL.sessions.active; if (S && S.id === e.sid) { const d = S.ui.delivery || {}; $('#autoFixCI').checked = !!d.autoFix; $('#autoMergePR').checked = !!d.autoMerge; } });
    sc.on('activated', () => { const S = SL.sessions.active, d = S && S.ui.delivery || {}; $('#autoFixCI').checked = !!d.autoFix; $('#autoMergePR').checked = !!d.autoMerge; TR.m = null; FD.m = null; sync(true); });
    sc.on('rebuilt', () => { TR.m = null; FD.m = null; });
    sc.on('cache-detail', () => { TR.m = null; FD.m = null; sync(true); });
    sc.on('files-ready', () => { if (SLASH.mode === 'file' && document.activeElement === inp) updateSlash(); });
    ui.chat = { sync, TR, FD };
  }
  SL.chat = { mount, rowHtml, ROW_CAP, isNotice };
})(SL);
