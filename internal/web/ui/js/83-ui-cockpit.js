/* 83-ui-cockpit.js: the Cockpit view: the drawn horse (eight worker legs), the manager's own card and one stall per worker, the gantt,
 * the task board, the merge queue, mail and the governor. Mounted through the View API: everything it starts dies with it
 * (the mail arcs live in a layer inside this view's root). The governor's 429s and retries, the merge queue's counters and the
 * board's failed and blocked tasks (D-04: a failed task stays in todo with a ✗ mark and its closure as the title) are the server's. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, sv, fmtK, fmtUsd, fmtMs, mmss, tod, hitCls, agCol } = U, calc = SL.calc, D = SL.D, ui = SL.ui = SL.ui || {};
  const SG = { think: ['◇', 'think'], tool: ['⚙', 'tool'], edit: ['✎', 'edit'], wait: ['✉', 'wait'], ask: ['?', 'ask'], idle: ['◌', 'idle'], done: ['✓', 'done'], stuck: ['⚠', 'stuck'] };
  ui.SG = SG;

  /** typed text of a streaming row/stall at view time vt: [shown, done]. */
  const typed = (full, t0, rate, vt) => { const n = Math.max(0, Math.floor((vt - t0) * rate)); return n >= full.length ? [full, true] : [full.slice(0, n), false]; };
  ui.typed = typed;
  function sparkBars(rs, W, H) {
    const n = Math.max(rs.length, 1), bw = Math.min(W / 8, W / n), gap = bw * .26; let out = '<line x1="0" x2="' + W + '" y1="' + (H - .5) + '" y2="' + (H - .5) + '" stroke="var(--line2)" stroke-width="1"/>';
    rs.forEach((r, i) => { const h = Math.max(2, r * (H - 4)), x = i * bw + 1, col = r === 0 ? 'var(--err)' : r < .7 ? 'var(--warm)' : 'var(--ok)'; out += '<rect x="' + x.toFixed(1) + '" y="' + (H - h).toFixed(1) + '" width="' + (bw - gap).toFixed(1) + '" height="' + h.toFixed(1) + '" fill="' + col + '" opacity="' + (r === 0 ? 1 : .85) + '"/>'; if (r === 0) out += '<text x="' + (x + (bw - gap) / 2).toFixed(1) + '" y="' + (H - 6) + '" font-size="10" fill="var(--err)" text-anchor="middle" font-family="var(--f-num)" font-weight="700">0</text>'; });
    return out;
  }
  ui.sparkBars = sparkBars;
  /** The board's additions (PARITY A17, D-04): a ✗ mark on a failed card (its closure as the title), `try N` when a task took more
   *  than one attempt; todo's count leaves failed tasks out. Returns {failed, blocked}. Runs after ui.board.update on the same board. */
  ui.boardMarks = function (B, m) {
    let failed = 0, blocked = 0, todoFailed = 0;
    m.torder.forEach(id => {
      const t = m.tasks[id], n = B.nodes[id]; if (!t || !n) return; const tow = n.querySelector('.tcr .tow');
      const isF = !!t.failed, isB = /^blocked_on/.test(t.closure || ''); if (isF) { failed++; if (t.st === 'todo') todoFailed++; } if (isB) blocked++;
      let mk2 = n.querySelector('.fmark');
      if (isF && tow) { if (!mk2) { mk2 = document.createElement('span'); mk2.className = 'bad fmark'; mk2.textContent = '✗'; tow.appendChild(mk2); } mk2.title = 'failed: ' + (t.closure || 'closed'); } else if (mk2) mk2.remove();
      let tr = n.querySelector('.tmark'); const nTry = t.attempts > 1 ? 'try ' + t.attempts : '';
      if (nTry && tow) { if (!tr) { tr = document.createElement('span'); tr.className = 'dim tmark'; tow.appendChild(tr); } tr.textContent = nTry; } else if (tr) tr.remove();
    });
    if (todoFailed) { const c = B.host.querySelector('[data-col="todo"] .cnt'); if (c) c.textContent = String(Math.max(0, (+c.textContent || 0) - todoFailed)); }
    return { failed, blocked };
  };
  /** The words added to a board summary: " (N blocked)", " · N failed". */
  ui.boardExtra = x => (x.blocked ? ' (' + x.blocked + ' blocked)' : '') + (x.failed ? ' · ' + x.failed + ' failed' : '');
  /** The board's alerts (stalls raised and board alerts) as a popover under btn. */
  ui.alertsPop = function (btn, S, m) {
    const ex = document.querySelector('.alertpop'); if (ex) { ex._off(); return; }
    const list = alertList(m);
    const p = mk('div', { class: 'popover alertpop', role: 'dialog', 'aria-label': 'Board alerts' }, '<h3 class="lab" style="margin:0 0 8px">alerts</h3>' + (list.length ? '<table class="tbl"><tbody>' + list.map(a => '<tr><td class="warm">' + esc(a.kind || '') + '</td><td>' + esc(a.text || '') + '</td><td class="dim r">' + ui.todAt(S, a) + '</td></tr>').join('') + '</tbody></table>' : '<p class="stubnote" style="margin:0">no alert</p>'));
    const app = document.getElementById('app'); app.appendChild(p); const r = btn.getBoundingClientRect(), ar = app.getBoundingClientRect(); p.style.top = (r.bottom - ar.top + 4) + 'px'; p.style.right = Math.max(8, ar.right - r.right) + 'px';
    const off = () => { p.remove(); document.removeEventListener('pointerdown', away, true); document.removeEventListener('keydown', esck, true); };
    const away = e => { if (!p.contains(e.target) && e.target !== btn) off(); }, esck = e => { if (e.key === 'Escape') { e.stopPropagation(); off(); btn.focus(); } };
    document.addEventListener('pointerdown', away, true); document.addEventListener('keydown', esck, true); p._off = off;
  };
  /** The alerts tag of a board head: `⚠ N alerts`, or nothing. */
  /** The board's alerts and the stalls no alert already names (the server raises both for a stalled agent). */
  function alertList(m) { const al = Object.keys(m.alerts || {}).map(k => Object.assign({ key: k }, m.alerts[k])); return al.concat(Object.keys(m.stalls || {}).map(k => m.stalls[k]).filter(s => !al.some(a => a.key === s.id || a.key === s.task)).map(s => ({ kind: s.kind, text: s.text, t: s.t, at: s.at }))); }
  ui.alertsTag = m => { const n = alertList(m).length; return n ? '<button class="tag warm" type="button" data-alerts title="what the board and the stall detector raised">⚠ ' + n + ' alert' + (n === 1 ? '' : 's') + '</button>' : ''; };
  /** Keep the alerts tag just before `before` (a board head's summary) while there are alerts; nothing otherwise. */
  ui.alertsSlot = (before, m) => { const html = ui.alertsTag(m); let el = before.parentNode.querySelector('.balert'); if (!html) { if (el) el.remove(); return; } if (!el) { el = document.createElement('span'); el.className = 'balert'; before.parentNode.insertBefore(el, before); } if (el._h !== html) { el._h = html; el.innerHTML = html; } };
  /** The governor note: the mock's sentence while nothing throttles, the counts when something does (D-07). */
  ui.govNote = m => (m.r429 || m.retries) ? m.r429 + ' 429' + (m.r429 === 1 ? '' : 's') + ', ' + m.retries + ' retr' + (m.retries === 1 ? 'y' : 'ies') + ': the endpoint is throttling the team' : 'no 429s, no retries: nothing is throttling the team';
  /** `routed N · dup N · mailman on/off`: the dup term only when the server counts duplicates (D-07). */
  ui.mailNote = (m, S) => { const st = m.mailstat; return 'routed ' + (st && st.sent != null ? st.sent : m.mail.length) + (st && st.dup != null ? ' · dup ' + st.dup : '') + ' · mailman ' + (S.meta.mailman ? 'on' : 'off'); };
  function miniTl(A, vt, col) {
    let out = '', w0 = vt - 60;
    A.segs.forEach(sg => {
      const a = Math.max(0, sg.t0 - w0), b = Math.min(60, (sg.t1 == null ? vt : sg.t1) - w0); if (b <= 0 || a >= 60 || sg.t0 > vt) return;
      const op = { tool: .85, edit: 1, think: .4, wait: .22, ask: 1, done: .9, idle: 0, stuck: 1 }[sg.s]; if (!op) return;
      const c = sg.s === 'ask' ? 'var(--warm)' : sg.s === 'stuck' ? 'var(--err)' : sg.s === 'done' ? 'var(--ok)' : col, y = sg.s === 'done' ? 3 : 0, h = sg.s === 'done' ? 2 : 8;
      out += '<rect x="' + a.toFixed(2) + '" y="' + y + '" width="' + Math.max(.4, b - a).toFixed(2) + '" height="' + h + '" fill="' + c + '" opacity="' + op + '"/>';
    });
    return out;
  }

  function mount(sc, root, params, S) {
    const workers = S.roster.filter(r => r.id !== 'mgr'), nW = workers.length, rows = Math.max(2, Math.ceil(Math.max(nW, 8) / 4));
    root.innerHTML = '<div class="cockpit" style="--srows:' + rows + '">' +
      '<section class="panel hero" aria-label="The team: the manager and the eight legs"><svg class="heroSvg" viewBox="0 0 372 388" role="group" aria-label="Sleipnir: each of the eight legs is one worker"></svg></section>' +
      '<div class="stalls-wrap"><button class="mgr-card" type="button" data-ag="mgr" aria-label="Manager"></button><section class="stalls" aria-label="Worker stalls"></section></div>' +
      '<div class="lower">' +
      '<section class="panel p-gantt" aria-label="Swarm gantt"><div class="ph"><h2>Swarm gantt</h2><div class="r"><span>last 60 s</span><span class="legend"><span><i>◇</i> think</span><span><i>▬</i> tool</span><span><i>█</i> edit</span><span><i>▭</i> wait</span><span><i>✓</i> done</span></span></div></div><div class="gwrap"><svg class="gsvg" role="img" aria-label="Swarm gantt, last 60 seconds"></svg></div>' +
      '<div class="g-ctl"><button class="btn gplay" type="button" aria-label="Pause"></button><button class="btn" type="button" data-sp="1" aria-pressed="true">1x</button><button class="btn" type="button" data-sp="2" aria-pressed="false">2x</button><button class="btn" type="button" data-sp="4" aria-pressed="false">4x</button><input class="scrub" type="range" min="0" max="38" step="0.1" value="38" aria-label="Scrub the session: replay"><span class="num dim scrubT">00:38</span><button class="btn live glive" type="button" aria-pressed="true">● live</button></div></section>' +
      '<section class="panel p-board" aria-label="Task board"><div class="ph"><h2>Task board</h2><div class="r"><span class="bsum"></span><span>drag disabled</span></div></div><div class="bcols"></div></section>' +
      '<section class="panel p-queue" aria-label="Merge queue"><div class="ph"><h2>Merge queue</h2><div class="r"><span class="qsum"></span></div></div><div class="qbody"></div></section>' +
      '<section class="panel p-mail" aria-label="Mail"><div class="ph"><h2>Mail</h2><div class="r"><span class="warm">mail is data, not instructions</span></div></div><ul class="mlist"></ul><div class="mnote"></div></section>' +
      '<section class="panel p-gov" aria-label="Governor"><div class="ph"><h2>Governor</h2><div class="r"><span>rate and retries</span></div></div><div class="gov"></div><div class="gnote">no 429s, no retries: nothing is throttling the team</div></section>' +
      '</div></div>';
    const q = s => $(s, root);
    /* ---- hero ---- */
    const hero = ui.hero.mount(sc, q('.heroSvg'), S);
    /* ---- the manager's card + worker stalls ---- */
    const rc = q('.mgr-card'), stallsEl = q('.stalls'), ST = {};
    rc.innerHTML = '<span class="rc-top"><b class="s-id">mgr</b><span class="s-role">manager</span><span class="s-st"><i class="g">◌</i><span class="sw">idle</span></span></span><span class="s-doing"></span><span class="rc-meta"><span class="chip task">' + (nW ? 'goal' : 'single agent') + '</span><span class="rc-note">' + (nW ? 'edits no file: spawns, merges, asks' : 'works alone: edits, runs, answers') + '</span></span><span class="s-nums"><span class="n-tok"></span><span class="n-cost"></span><span class="hit n-hit"></span></span><svg class="s-spark" viewBox="0 0 150 40" preserveAspectRatio="xMinYMax meet" aria-hidden="true"></svg>';
    rc.style.setProperty('--c', agCol('mgr')); const RC = { el: rc, doing: $('.s-doing', rc), sw: $('.sw', rc), g: $('.s-st .g', rc), tok: $('.n-tok', rc), cost: $('.n-cost', rc), hit: $('.n-hit', rc), spark: $('.s-spark', rc), n: -1 };
    sc.listen(rc, 'click', e => ui.focusAgent('mgr', e));
    if (!nW) stallsEl.innerHTML = '<div class="solo"><b>The manager works alone</b><p>--swarm 0: one agent, no workers and no leases. The eight legs stand; the manager edits, runs and answers itself.</p><ol class="sololog"></ol></div>';
    else {
      workers.forEach(w => {
        const b = mk('button', { class: 'stall', type: 'button', 'data-ag': w.id, 'data-state': 'idle', 'data-role': w.role, style: '--c:' + agCol(w.id) },
          '<span class="s-top"><b class="s-id">' + esc(w.id) + '</b><span class="s-role">' + esc(w.role) + '</span><span class="s-st"><i class="g">◌</i><span class="sw">idle</span></span></span><span class="s-doing"></span>' +
          '<span class="s-meta"><span class="chip task"></span><span class="s-scope"><i class="legn">leg ' + (w.leg + 1) + '</i>' + (w.scope === '-' || /^- /.test(w.scope) ? ' · read-only' : ' · ' + esc(w.scope)) + '</span><span class="brk" title="cache break: the prompt prefix did not change, the endpoint did not serve it"><span aria-hidden="true">⚠</span><span class="bx"> cache break</span></span></span>' +
          '<span class="s-nums"><span class="n-tok"></span><span class="n-cost"></span><span class="hit n-hit"></span></span><svg class="s-spark" viewBox="0 0 150 40" preserveAspectRatio="xMinYMax meet" aria-hidden="true"></svg><svg class="s-tl" viewBox="0 0 60 8" preserveAspectRatio="none" aria-hidden="true"></svg>');
        sc.listen(b, 'click', e => ui.focusAgent(w.id, e)); stallsEl.appendChild(b);
        ST[w.id] = { el: b, doing: $('.s-doing', b), sw: $('.sw', b), g: $('.s-st .g', b), chip: $('.chip', b), tok: $('.n-tok', b), cost: $('.n-cost', b), hit: $('.n-hit', b), spark: $('.s-spark', b), tl: $('.s-tl', b), n: -1, streaming: false };
      });
      for (let i = nW; i < 8; i++) stallsEl.appendChild(mk('div', { class: 'stall ghost', 'aria-label': 'leg ' + (i + 1) + ' is free' }, '<span class="s-top"><b class="s-id">leg ' + (i + 1) + '</b><span class="s-role">free</span></span><span class="s-doing">no worker on this leg: <kbd>/swarm N</kbd> starts the team again with more</span>'));
    }
    sc.listen(stallsEl, 'keydown', e => { if (!/^Arrow/.test(e.key)) return; const cards = $$('.stall:not(.ghost)', stallsEl), i = cards.indexOf(document.activeElement); if (i < 0) return; const d = { ArrowRight: 1, ArrowLeft: -1, ArrowDown: 4, ArrowUp: -4 }[e.key], n = cards[i + d]; if (n) { e.preventDefault(); e.stopPropagation(); n.focus(); } }, true);

    function fillCard(R, A, c, spawned, m) {
      const s = spawned ? A.state : 'idle'; R.el.dataset.state = s; const [g, w] = SG[s]; R.g.textContent = g; R.sw.textContent = w;
      if (!R.streaming) R.doing.textContent = spawned ? (ui.waitingFor(A, SL.sessions.active ? SL.sessions.active.vt : 0) || A.doing) : 'not started';
      if (R.chip) { R.chip.textContent = A.id === 'mgr' ? R.chip.textContent : (A.task || '–'); R.chip.className = 'chip task' + (A.task && m.tasks[A.task] && m.tasks[A.task].st === 'merged' ? ' t-merged' : ''); if (A.id !== 'mgr') R.el.setAttribute('data-task', A.task || ''); }
      R.tok.innerHTML = fmtK(c.prompt) + '<span class="u"> tok</span>'; R.cost.textContent = fmtUsd(c.cost, 3); R.hit.textContent = c.prompt ? c.pct + '%' : '–'; R.hit.className = 'hit n-hit ' + (c.prompt ? hitCls(c.pct) : '');
      R.el.setAttribute('aria-label', A.id + ' ' + A.role + ', ' + w + ': ' + A.doing);
      if (R.n !== A.ratios.length) { R.n = A.ratios.length; R.spark.innerHTML = sparkBars(A.ratios, 150, 40); }
    }
    /* ---- board, queue, mail, governor ---- */
    const B = ui.board.make(sc, q('.bcols'), false), qbody = q('.qbody'), mlist = q('.mlist');
    function renderQueue(m) {
      const h = m.qHead, merged = m.merged.slice().sort(), isGoal = h && h.task === 'goal'; let html = '';
      if (h) html += '<div class="qhead"><div class="qline"><span class="tri">▸</span><b style="color:' + (isGoal ? 'var(--mgr)' : agCol((m.tasks[h.task] || {}).owner || 'mgr')) + '">' + (isGoal ? '◇ goal' : esc(h.task)) + '</b>' + (isGoal ? '<span>all merged</span>' : '<span>rebase <span class="ok">✓</span></span>') + '<span class="dim">·</span><span>' + (h.step === 'verified' ? 'verified' : 'verifying') + '</span></div><div class="qline"><span class="dim">$</span><span class="cmd">' + esc(h.cmd) + '</span></div><div class="qbar" aria-hidden="true">' + (h.step === 'verified' ? '<i style="width:100%;left:0;animation:none;background:var(--ok)"></i>' : '<i></i>') + '</div><div class="qline dim"><span class="qel num">0.0s elapsed</span></div></div>';
      else html += '<div class="qhead"><div class="qline dim"><span>▹ queue empty</span><span>·</span><span>the next submission is rebased, then verified' + (S.meta.verify ? ' with <span class="mono">' + esc(S.meta.verify) + '</span>' : '') + '</span></div></div>';
      html += '<div class="qmeta"><span>merged ' + (merged.map(id => '<b style="color:' + agCol((m.tasks[id] || {}).owner || 'mgr') + '">' + esc(id) + '</b>').join(' ') || '–') + '</span><span>conflicts <b>' + esc(m.conflicts) + '</b></span><span>bounced <b>' + esc(m.bounced) + '</b></span></div>';
      if (qbody._h !== html) { qbody._h = html; qbody.innerHTML = html; } q('.qsum').textContent = merged.length + ' merged';
    }
    function renderMail(m) {
      const list = m.mail.slice(-4).reverse();
      const h = list.map(x => '<li><button class="mrow" type="button" data-ag="' + esc(x.from) + ' ' + esc(x.to) + '" data-mail="' + esc(x.t) + '"><span class="mt">' + ui.todAt(S, x) + '</span><span class="mi">✉</span><span class="mf"><span style="color:' + agCol(x.from) + '">' + esc(x.from) + '</span> <span class="dim">→</span> <span style="color:' + agCol(x.to) + '">' + esc(x.to) + '</span></span><span class="mx">“' + esc(x.text) + '”</span></button></li>').join('') || '<li class="mnote">no mail yet</li>';
      if (mlist._h !== h) { mlist._h = h; mlist.innerHTML = h; } q('.mnote').textContent = ui.mailNote(m, S);
    }
    sc.listen(mlist, 'click', e => { const r = e.target.closest('[data-mail]'); if (r) { ui.mailSel = +r.dataset.mail; SL.views.show('mail'); } });
    sc.listen(root, 'click', e => { const a = e.target.closest('[data-alerts]'); if (a) { const S2 = SL.sessions.active; ui.alertsPop(a, S2, S2.m); } });
    function renderGov(m) {
      const rs = m.rpmHist.slice(-14), mx = Math.max(60, ...rs.map(r => r.rpm)); let spk = ''; rs.forEach((r, i) => { const h = Math.max(2, r.rpm / mx * 30); spk += '<rect x="' + i * 7 + '" y="' + (34 - h) + '" width="5" height="' + h + '" fill="var(--fe)" opacity=".75"/>'; });
      const line = '<line x1="0" x2="98" y1="33" y2="33" stroke="var(--ok)" stroke-width="2"/>', g = q('.gov');
      const bad = '<line x1="0" x2="98" y1="33" y2="33" stroke="var(--warm)" stroke-width="2"/>', r4 = m.r429 || 0, rt = m.retries || 0;
      const h = '<div class="gauge2"><svg viewBox="0 0 98 34" preserveAspectRatio="none" aria-hidden="true">' + spk + '</svg><b class="num">' + (m.rpm || 0) + '</b><span>rpm</span></div><div class="gauge2"><svg viewBox="0 0 98 34" aria-hidden="true">' + (r4 ? bad : line) + '</svg><b class="num ' + (r4 ? 'warm' : 'ok') + '">' + r4 + '</b><span>429s</span></div><div class="gauge2"><svg viewBox="0 0 98 34" aria-hidden="true">' + (rt ? bad : line) + '</svg><b class="num ' + (rt ? 'warm' : 'ok') + '">' + rt + '</b><span>retries</span></div>';
      if (g._h !== h) { g._h = h; g.innerHTML = h; } const gn = q('.gnote'), gt = ui.govNote(m); if (gn.textContent !== gt) gn.textContent = gt;
    }

    /* ---- gantt (rows: manager + workers) ---- */
    const GT = { W: 0, H: 0, rowH: 16, left: 34, axisH: 15, pps: 6, built: false, builtAt: -99, open: [], axis: [] }, gsvg = q('.gsvg'), gwrap = q('.gwrap'), ids = S.roster.map(r => r.id);
    function buildGantt() {
      if (!gwrap.clientWidth) return; GT.W = gwrap.clientWidth - 20; GT.H = gwrap.clientHeight; GT.rowH = Math.max(9, Math.min(23, Math.floor((GT.H - GT.axisH - 6) / ids.length))); GT.pps = (GT.W - GT.left - 4) / 60;
      gsvg.setAttribute('viewBox', '0 0 ' + GT.W + ' ' + GT.H); gsvg.setAttribute('preserveAspectRatio', 'xMinYMin meet'); gsvg.style.height = GT.H + 'px';
      let h = '<defs>'; ['manager', 'backend', 'frontend', 'scout', 'tester', 'reviewer', 'docs', 'fullstack'].concat(S.roster.map(x => x.role)).filter((r, i, a) => a.indexOf(r) === i).forEach(r => { const c = 'var(--c-' + ((D.roles[r] || SL.data.ROLES[r] || { code: r }).code) + ')'; h += '<pattern id="hx-' + r + '" width="5" height="5" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="2" height="5" fill="' + c + '" opacity=".7"/></pattern>'; });
      h += '<pattern id="hx-ask" width="5" height="5" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="2.6" height="5" fill="var(--warm)"/></pattern><clipPath id="gclip"><rect x="' + GT.left + '" y="0" width="' + (GT.W - GT.left) + '" height="' + GT.H + '"/></clipPath></defs>';
      ids.forEach((id, i) => { const y = 2 + i * GT.rowH; h += '<g class="g-row" data-ag="' + esc(id) + '"><rect x="0" y="' + y + '" width="' + GT.W + '" height="' + GT.rowH + '" fill="transparent" class="g-hit"/><line x1="' + GT.left + '" x2="' + GT.W + '" y1="' + (y + GT.rowH - .5) + '" y2="' + (y + GT.rowH - .5) + '" class="g-grid"/><text x="0" y="' + (y + GT.rowH * .72) + '" class="g-lab" fill="' + agCol(id) + '" font-size="' + (GT.rowH > 13 ? 11 : 9.5) + '">' + esc(id) + '</text><g clip-path="url(#gclip)"><g class="gbars" data-i="' + i + '"></g></g></g>'; });
      h += '<g class="g-axis-g"></g><line class="g-now" x1="' + (GT.W - 4) + '" x2="' + (GT.W - 4) + '" y1="0" y2="' + (GT.H - GT.axisH) + '"/>';
      gsvg.innerHTML = h; GT.axisG = $('.g-axis-g', gsvg); GT.bars = $$('.gbars', gsvg); GT.axis = []; GT.built = true; GT.builtAt = -99; ganttBars(S.m, S.vt);
    }
    function ganttBars(m, vt) {
      if (!GT.built || !m) return; const rowH = GT.rowH, pps = GT.pps, w0 = vt - 62; GT.open = [];
      ids.forEach((id, i) => {
        const A = m.ag[id], y = 2 + i * rowH, role = A.role; let out = '';
        A.segs.forEach(sg => {
          const t1 = sg.t1 == null ? vt : sg.t1; if (t1 < w0 || sg.t0 > vt + 1) return; const x = sg.t0 * pps, w = Math.max(1.2, (t1 - sg.t0) * pps), open = sg.t1 == null, cls = open ? ' class="open"' : '', col = 'var(--c-' + A.code + ')', by = y + 2, bh = rowH - 4; let r = '';
          if (sg.s === 'think') r = '<rect' + cls + ' x="' + x + '" y="' + by + '" width="' + w + '" height="' + bh + '" fill="url(#hx-' + role + ')" stroke="' + col + '" stroke-opacity=".5" stroke-width="1"/>';
          else if (sg.s === 'tool') r = '<rect' + cls + ' x="' + x + '" y="' + (by + bh * .22) + '" width="' + w + '" height="' + (bh * .56) + '" fill="' + col + '" opacity=".9"/>';
          else if (sg.s === 'edit') r = '<rect' + cls + ' x="' + x + '" y="' + by + '" width="' + w + '" height="' + bh + '" fill="' + col + '"/>';
          else if (sg.s === 'wait') r = '<rect' + cls + ' x="' + (x + .5) + '" y="' + (by + 1) + '" width="' + Math.max(1, w - 1) + '" height="' + (bh - 2) + '" fill="none" stroke="' + col + '" stroke-opacity=".75" stroke-width="1.2"/>';
          else if (sg.s === 'ask') r = '<rect' + cls + ' x="' + x + '" y="' + by + '" width="' + w + '" height="' + bh + '" fill="url(#hx-ask)" stroke="var(--warm)" stroke-width="1"/>';
          else if (sg.s === 'done') r = '<rect' + cls + ' x="' + x + '" y="' + (y + rowH / 2 - 1) + '" width="' + w + '" height="2" fill="var(--ok)" opacity=".8"/><text x="' + (x + 1) + '" y="' + (y + rowH * .72) + '" font-size="10" fill="var(--ok)" font-family="var(--f-num)" font-weight="700">✓</text>';
          else if (sg.s === 'idle') r = '<rect' + cls + ' x="' + x + '" y="' + (y + rowH / 2) + '" width="' + w + '" height="1" fill="var(--faint)" opacity=".6"/>';
          else if (sg.s === 'stuck') r = '<rect' + cls + ' x="' + x + '" y="' + by + '" width="' + w + '" height="' + bh + '" fill="var(--err)" opacity=".8"/>';
          out += r;
        });
        m.marks.forEach(mk2 => { if (mk2.id !== id || mk2.t < w0 || mk2.t > vt) return; const x = mk2.t * pps; if (mk2.g === 'req') out += '<rect x="' + (x - .6) + '" y="' + (y + 1) + '" width="1.2" height="3" fill="var(--fg2)" opacity=".7"/>'; else { const gl = { mail: ['✉', 'var(--warm)'], compact: ['◆', 'var(--mgr)'], break: ['⚠', 'var(--err)'], merge: ['✓', 'var(--ok)'], ask: ['?', 'var(--warm)'], refuse: ['⊘', 'var(--err)'] }[mk2.g]; if (gl) out += '<text class="g-glyph" x="' + (x - 4) + '" y="' + (y + rowH * .78) + '" fill="' + gl[1] + '" stroke="var(--panel)" stroke-width="2.5" paint-order="stroke">' + gl[0] + '</text>'; } });
        GT.bars[i].innerHTML = out;
      });
      GT.open = $$('.open', gsvg).map(e => ({ e, t0: +e.getAttribute('x') / pps })); GT.builtAt = vt;
    }
    function ganttFrame(vt, m) {
      if (!GT.built) return; const tx = GT.left - (vt - 60) * GT.pps; GT.bars.forEach(g => g.setAttribute('transform', 'translate(' + tx.toFixed(2) + ' 0)'));
      GT.open.forEach(o => o.e.setAttribute('width', Math.max(1.2, (vt - o.t0) * GT.pps).toFixed(1)));
      if (!GT.axis.length) for (let i = 0; i < 7; i++) { const t = sv('text', { class: 'g-axis', y: GT.H - 3 }, GT.axisG); GT.axis.push(t); }
      GT.axis.forEach((t, i) => { const back = i * 10, xx = GT.W - 4 - back * GT.pps; t.setAttribute('text-anchor', i === 0 ? 'end' : 'middle'); t.setAttribute('x', xx.toFixed(1)); const s = i === 0 ? 'now' : '-' + back + 's'; if (t.textContent !== s) t.textContent = s; t.style.display = xx < GT.left + 6 ? 'none' : ''; });
      if (vt - GT.builtAt > 2 || vt < GT.builtAt) ganttBars(m, vt);
    }
    sc.observe(gwrap, () => { if (gwrap.clientWidth) buildGantt(); });
    sc.listen(gsvg, 'click', e => { const r = e.target.closest('.g-row'); if (r) ui.focusAgent(r.dataset.ag, e); });
    /* gantt controls = the replay controls: pause enters replay at the current time, the scrub bar seeks, live returns */
    const gplay = q('.gplay'), scrub = q('.scrub'), glive = q('.glive'), scrubT = q('.scrubT');
    function ctl(S2) { const rep = S2.replay, playing = !rep || rep.playing; gplay.innerHTML = playing ? ui.IC.pause : ui.IC.play; gplay.setAttribute('aria-label', playing ? 'Pause' : 'Play'); $$('[data-sp]', root).forEach(b => b.setAttribute('aria-pressed', String(+b.dataset.sp === (rep ? rep.speed : 1)))); glive.setAttribute('aria-pressed', String(!rep)); }
    sc.listen(gplay, 'click', () => { const S2 = SL.sessions.active; if (!S2.replay) S2.seek(S2.vt, false); else S2.replay.playing = !S2.replay.playing; S2.touch(); });
    $$('[data-sp]', root).forEach(b => sc.listen(b, 'click', () => { const S2 = SL.sessions.active; if (!S2.replay) S2.seek(S2.vt, true); S2.replay.speed = +b.dataset.sp; S2.replay.playing = true; S2.touch(); }));
    sc.listen(scrub, 'input', () => { const S2 = SL.sessions.active; S2.seek(+scrub.value, S2.replay ? S2.replay.playing : false); S2.touch(); });
    sc.listen(glive, 'click', () => { const S2 = SL.sessions.active; S2.goLive(); S2.touch(); });

    /* ---- mail arcs: transient, in this view's own layer, advanced by the sim clock ---- */
    const arcLayer = sc.layer('arcs'), arcSvg = sv('svg', { class: 'arcs', 'aria-hidden': 'true' }, arcLayer), arcs = [];
    function launchArc(from, to) {
      const a = ST[from] ? ST[from].el : from === 'mgr' ? rc : null, b = ST[to] ? ST[to].el : to === 'mgr' ? rc : null; if (!a || !b || ui.still()) return;
      const rr = root.getBoundingClientRect(), r1 = a.getBoundingClientRect(), r2 = b.getBoundingClientRect(), x1 = r1.left + r1.width / 2 - rr.left, y1 = r1.top + r1.height / 2 - rr.top, x2 = r2.left + r2.width / 2 - rr.left, y2 = r2.top + r2.height / 2 - rr.top;
      const cx = (x1 + x2) / 2, cy = Math.min(y1, y2) - 70 - Math.abs(x1 - x2) * .12, g = sv('g', {}, arcSvg), p = sv('path', { d: 'M' + x1 + ' ' + y1 + ' Q' + cx + ' ' + cy + ' ' + x2 + ' ' + y2, fill: 'none', stroke: 'var(--warm)', 'stroke-width': 1.6, 'stroke-dasharray': '3 5', opacity: .8 }, g);
      const env = sv('text', { class: 'arc-env', 'text-anchor': 'middle' }, g, '✉'), lb = sv('text', { fill: 'var(--warm)', 'font-size': 11, 'font-family': 'var(--f-num)', 'text-anchor': 'middle' }, g, from + ' → ' + to);
      arcs.push({ g, p, env, lb, len: p.getTotalLength(), t: 0 });
    }
    sc.on('ev', ({ ev }) => { if (ev.k === 'mail') launchArc(ev.from, ev.to); });
    function arcsFrame(dt) { for (let i = arcs.length - 1; i >= 0; i--) { const a = arcs[i]; a.t += dt / 1.3; const k = Math.min(1, a.t), e = k < .5 ? 2 * k * k : 1 - Math.pow(-2 * k + 2, 2) / 2, pt = a.p.getPointAtLength(a.len * e); a.env.setAttribute('x', pt.x); a.env.setAttribute('y', pt.y + 5); a.lb.setAttribute('x', pt.x); a.lb.setAttribute('y', pt.y - 12); a.g.setAttribute('opacity', k > .8 ? ((1 - k) * 5).toFixed(2) : 1); if (k >= 1) { a.g.remove(); arcs.splice(i, 1); } } }

    /* ---- update (model changed) and frame (every frame, sim clock) ---- */
    function update(S2, m) {
      if (!m) return; const mg = m.ag.mgr, cm = calc.agent(mg); fillCard(RC, mg, cm, true, m);
      if (!nW) { const log = (m.chan.mgr || []).filter(e => e.k === 'tool' && !e.collapsed).slice(-6).reverse(); $('.sololog', root).innerHTML = log.map(e => '<li><b>' + esc(e.name) + '</b> ' + esc(e.arg || '') + (e.out ? ' <span class="dim">' + esc(e.out) + '</span>' : '') + '</li>').join('') || '<li class="dim">nothing yet</li>'; }
      workers.forEach(w => { const A = m.ag[w.id]; fillCard(ST[w.id], A, calc.agent(A), A.spawned, m); });
      const cnt = ui.board.update(B, m), bx = ui.boardMarks(B, m); q('.bsum').textContent = (cnt.merged || 0) + ' merged · ' + (cnt.verify || 0) + ' verify · ' + (cnt.running || 0) + ' running' + ui.boardExtra(bx);
      ui.alertsSlot(q('.bsum'), m);
      renderQueue(m); renderMail(m); renderGov(m); ctl(S2); scrub.max = Math.max(S2.wt, 1).toFixed(1);
    }
    function frame(dt, vt, S2) {
      const m = S2.m; if (!m) return; ganttFrame(vt, m); arcsFrame(dt);
      const els = $$('.qel', root); if (els[0] && m.qHead) { const t = (m.qHead.step === 'verified' ? (m.qHead.ms || 900) / 1000 : Math.max(0, vt - m.qHead.t0)).toFixed(1) + 's elapsed'; if (els[0].textContent !== t) els[0].textContent = t; }
      /* streaming text on the cards */
      const rows = [[RC, 'mgr']].concat(workers.map(w => [ST[w.id], w.id]));
      rows.forEach(([R, id]) => { const s = m.streams[id], A = m.ag[id]; const brk = m.lastBreakT > -900 && m.flash.id === id && vt - m.lastBreakT < (SL.settings.cache === 'quiet' ? 90 : 8) && vt >= m.lastBreakT; if (R.el.dataset.brk !== (brk ? '1' : '0')) R.el.dataset.brk = brk ? '1' : '0';
        if (s && !s.code && vt - s.t0 < s.text.length / s.rate + 5 && A.state !== 'idle') { const [t, done] = typed(s.text, s.t0, s.rate, vt); R.streaming = true; const h = esc(t) + (done ? '' : '<span class="caret"></span>'); if (R.doing._h !== h) { R.doing._h = h; R.doing.innerHTML = h; } } else if (R.streaming) { R.streaming = false; R.doing._h = null; R.doing.textContent = A.spawned ? A.doing : 'not started'; } });
      if ((SL.loop.frameNo % 30) === 0) workers.forEach(w => { const R = ST[w.id]; R.tl.style.setProperty('--c', agCol(w.id)); R.tl.innerHTML = miniTl(m.ag[w.id], vt, agCol(w.id)); });
      if ((SL.loop.frameNo & 15) === 0 && !S2.replay) { const st = mmss(vt); if (scrubT.textContent !== st) scrubT.textContent = st; if (document.activeElement !== scrub) scrub.value = vt.toFixed(1); }
      if ((SL.loop.frameNo % 30) === 15) rows.forEach(([R, id]) => { const A = m.ag[id]; if (R.streaming || !A || !A.spawned) return; const t = ui.waitingFor(A, vt) || A.doing; if (R.doing.textContent !== t) R.doing.textContent = t; });
    }
    sc.update(update); sc.frame(frame); update(S, S.m); buildGantt();
  }
  SL.views.register({ name: 'cockpit', title: 'Cockpit', mount });
})(SL);
