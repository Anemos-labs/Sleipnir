/* 80-ui-shell.js: the persistent chrome (SL.ui.shell): HUD, session strip, view tabs, replay banner, footer, phone nav.
 * It is mounted once, under the shell scope, and re-renders from the active session. Nothing here is view-specific.
 * The warm ring's 25 ticks are a fraction of the prefix's real cache lifetime (m.ttl); the connection chip shows the server's
 * address and the stream's state (connected, reconnecting, disconnected). */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, fmtUsd, mmss, clock, pct, hitCls } = U, calc = SL.calc;
  const ui = SL.ui = SL.ui || {};
  const WARM_LIFE = 25, WARM_RED = 8, RING_C = 2 * Math.PI * 22, HIT_C = 2 * Math.PI * 19;
  const SESS_GLYPH = { run: '●', ask: '?', idle: '◌', done: '✓', paused: '⏸' };
  const ST_WORD = { run: 'running', ask: 'needs you', idle: 'at the prompt', done: 'done', paused: 'paused' };
  /** "N tokens at unknown prices are not counted" when some are, else ''. */
  const unpricedNote = n => n > 0 ? '; ' + U.fmtN(n) + ' tokens at unknown prices are not counted' : '';
  /** A turn's model request without an answer for 45 s or more: "waiting for the model (1m 05s)". */
  ui.waitingFor = (A, vt) => { if (!A || A.reqSince == null || !['think', 'tool', 'edit', 'wait'].includes(A.state)) return ''; const s = vt - A.reqSince; if (s < 45) return ''; const n = Math.floor(s); return 'waiting for the model (' + (n < 60 ? n + 's' : Math.floor(n / 60) + 'm ' + String(n % 60).padStart(2, '0') + 's') + ')'; };

  function mount(sc) {
    const H = { disp: 1, txt: {}, ticks: [], lastReq: -9999, pulseT: -1 };
    const txt = (key, el, v) => { if (H.txt[key] !== v) { H.txt[key] = v; el.textContent = v; } };
    /* ---- HUD ---- */
    const g = $('#warmTicks');
    for (let i = 0; i < WARM_LIFE; i++) { const a = -Math.PI / 2 + i / WARM_LIFE * 2 * Math.PI, c = Math.cos(a), s = Math.sin(a), ln = U.sv('line', { class: 'ring-tick', x1: 32 + 26 * c, y1: 32 + 26 * s, x2: 32 + 30 * c, y2: 32 + 30 * s }, g); H.ticks.push(ln); }
    $('#warmArc').style.strokeDasharray = RING_C; $('#hitArc').style.strokeDasharray = HIT_C;
    sc.update((S, m) => {
      if (!S || !m) return; const c = calc.totals(m), meta = S.meta, nW = S.roster.length - 1;
      const goalText = meta.goalText || (m.goal.state !== 'cleared' && m.goal.objective) || '';   /* the server's goal events name it too */
      $('#goalLab').textContent = goalText ? 'Goal' : 'Session'; const gs = $('#goalState'); gs.hidden = !goalText; const gst = m.goal.state; gs.textContent = gst === 'met' ? 'met ✓' : gst === 'paused' ? 'paused' : gst === 'cleared' ? 'cleared' : 'active'; gs.style.color = gst === 'met' ? 'var(--ok)' : gst === 'paused' ? 'var(--warm)' : '';
      txt('goalT', $('#goalT'), goalText || 'no goal: a chat with the manager (type /goal TEXT to set one)');
      txt('cost', $('#vCost'), fmtUsd(c.cost, 2)); $('#vCost').title = fmtUsd(c.cost, 4) + ' (list prices)' + unpricedNote(c.unpriced);
      txt('budget', $('#vBudget'), meta.budget ? 'of ' + fmtUsd(meta.budget, 2) : 'no budget');
      $('#gaugeFill').style.width = (meta.budget ? Math.min(100, Math.max(1.6, c.cost / meta.budget * 100)) : 0) + '%';
      $('#hitArc').style.strokeDashoffset = HIT_C * (1 - c.pct / 100); txt('hit', $('#hitTxt'), String(c.pct)); txt('hitc', $('#hitChipN'), String(c.pct)); $('#ringHit').title = 'read ' + U.fmtN(c.read) + ' / prompt ' + U.fmtN(c.prompt) + ' = ' + c.hit1 + '%';
      $('#ringHit').classList.toggle('dip', c.pct < 80);
      txt('teamLab', $('#teamLab'), nW ? 'manager + ' + nW + ' worker' + (nW === 1 ? '' : 's') : 'single agent');
      txt('act', $('#vActN'), String(calc.active(m))); txt('actOf', $('#vActOf'), nW ? 'of ' + nW + ' worker' + (nW === 1 ? '' : 's') : 'workers: the manager works alone');
      { const d = meta.mode === 'bypass' || meta.mode === 'yolo', gh = '<span class="hmode ' + (d ? 'dng' : meta.mode === 'default' ? '' : 'on') + '" title="permission mode: shift+tab cycles default, accept-edits, plan">' + (d ? '⚠ ' : '') + esc(meta.mode) + '</span> · rpm ' + (m.rpm || 0) + ' · 429s ' + (m.r429 || 0) + ' · retries ' + (m.retries || 0); const ge = $('#govTxt'); if (H.txt.gov !== gh) { H.txt.gov = gh; ge.innerHTML = gh; } }
      $('#vAct').parentNode.title = nW ? 'capacity ' + nW + ' workers · ' + calc.started(m) + ' started · ' + calc.active(m) + ' active (not idle, not done)' : 'swarm 0: one agent, no workers';
      const b = $('#banner'); if (S.replay) { b.hidden = false; b.innerHTML = '<b>Replay</b><span>' + mmss(S.vt) + ' of ' + mmss(S.wt) + '</span><span>' + (S.replay.playing ? 'playing ' + S.replay.speed + 'x' : 'paused') + '</span><span class="dim">' + (S.recorded ? (S.follow ? 'watching ' + esc(S.sid) + ' · read-only · the run belongs to another process' : esc(S.sid) + ' · a recorded session, read-only') : 'the whole cockpit follows the log; actions act on the live session') + '</span><span class="sp"></span><button type="button" data-act="live">go live</button>'; }
      else if (S.recorded) { b.hidden = false; b.innerHTML = '<b>' + (S.follow ? 'Watching' : 'Recorded') + '</b><span class="dim">' + (S.follow ? 'watching ' + esc(S.sid) + ' · read-only · the run belongs to another process' : esc(S.sid) + ' · a recorded session, read-only: ↺ Resume continues it') + '</span><span class="sp"></span>'; }
      else b.hidden = true;
      const q = calc.openQuestion(m), qb = $('#qBanner'); qb.hidden = !q; if (q) $('#qBannerT').textContent = q.agent + ' asks: ' + q.cmd;
      renderTabs(S); renderFooter(S, m); paintMode(S);
    });
    sc.frame((dtView, vt, S) => {
      if (!S || !S.m) return; const m = S.m;
      txt('el', $('#vElapsed'), mmss(vt)); const gap = S.wt - vt, lh = $('#liveHint'); lh.hidden = gap < 1; if (gap >= 1) txt('lh', lh, ' · live +' + Math.round(gap) + 's');
      const left = calc.warmLeft(m, vt), tgt = Math.max(0, left) / (m.ttl || WARM_LIFE), rw = $('#ringWarm'), low = left > 0 && left < WARM_RED, cold = left <= 0, still = ui.still();
      if (tgt > H.disp) H.disp = still ? tgt : Math.min(tgt, H.disp + dtView * 3.4); else H.disp = tgt;
      rw.classList.toggle('low', low); rw.classList.toggle('cold', cold); $('#warmArc').style.strokeDashoffset = RING_C * (1 - H.disp);
      const lit = Math.ceil(H.disp * WARM_LIFE - 1e-6); for (let i = 0; i < H.ticks.length; i++) { const on = i < lit; if (H.ticks[i]._on !== on) { H.ticks[i]._on = on; H.ticks[i].classList.toggle('on', on); } }
      txt('warm', $('#warmTxt'), cold ? 'cold' : clock(left)); const wt = $('#warmTxt'); wt.style.fill = cold || low ? 'var(--err)' : ''; wt.style.fontSize = cold ? '12px' : '15px';
      txt('wnote', $('#warmNote'), cold ? 'cold: the next request pays full price (est.)' : low ? 'goes cold in ' + clock(left) + ' unless a request lands' : 'cache lifetime, refills on each request');
      if (H.lastReq !== m.lastReq) { const fresh = m.lastReq > H.lastReq && Math.abs(vt - m.lastReq) < 1.5; H.lastReq = m.lastReq; if (fresh && !ui.still() && !S.replay) { rw.classList.remove('pulse'); void rw.offsetWidth; rw.classList.add('pulse'); const last = m.reqLog[m.reqLog.length - 1]; $('#warmFlash').style.stroke = last && last.ratio === 0 ? 'var(--err)' : 'var(--ok)'; } }
      if ((SL.loop.frameNo & 7) === 0) { renderFooter(S, m); stripCosts(); }
    });
    sc.listen($('#goalBtn'), 'click', () => ui.sheets.goal());
    sc.listen($('#brand'), 'click', e => { e.preventDefault(); SL.views.show('cockpit'); });
    sc.listen($('#banner'), 'click', e => { if (e.target.closest('[data-act="live"]')) { const S = SL.sessions.active; S.goLive(); S.touch(); } });

    /* ---- the left rail (views) lives in 96-ui-nav.js; it re-renders from the same update hook ---- */
    function renderTabs(S) { if (ui.nav) ui.nav.render(S); }
    sc.on('view-mounted', () => { const S = SL.sessions.active; if (S) renderTabs(S); });
    sc.on('needs-changed', () => { if (ui.nav) ui.nav.sig = ''; });

    /* ---- session strip: browser-like tabs, + New, ↺ Resume; on the right the inbox, the all-sessions budget and the connection chip ---- */
    const strip = $('#sstrip');
    const SESS_ICON = { run: '●', ask: '?', idle: '◌', done: '✓', paused: '⏸' };
    function stripHtml() {
      const act = SL.sessions.active, nd = SL.sessions.needs(); let spent = 0, cap = 0, n = SL.sessions.list.length;
      SL.sessions.list.forEach(S => { spent += calc.totals(S.wm).cost; cap += S.meta.budget || 0; });
      let h = '<div class="stabs" role="tablist" aria-label="Sessions">' + SL.sessions.list.map(S => {
        const st = S.state(), q = nd.filter(x => x.S === S).length, sel = S === act, gl = (S.meta.headless || S.follow) && st === 'run' ? '⟳' : SESS_ICON[st];
        return '<div class="stabw' + (sel ? ' sel' : '') + '" role="presentation" data-state="' + st + '"><button class="stab' + (sel ? ' sel' : '') + '" role="tab" type="button" data-sid="' + esc(S.id) + '" aria-selected="' + sel + '" data-state="' + st + '" title="' + esc(S.name + ' · ' + S.meta.cwd + ' · ' + ST_WORD[st] + (S.meta.headless ? ' · headless job: nobody can answer' : '') + ' (double-click to rename, middle-click to close)') + '"><i class="sg" aria-hidden="true">' + gl + '</i><span class="sn">' + esc(S.name) + '</span><span class="sc num" data-cost="' + esc(S.id) + '"></span>' + (S.meta.mode === 'bypass' || S.meta.mode === 'yolo' ? '<span class="dgm" title="' + esc(S.meta.mode) + ' mode: this session does not ask before it acts">⚠<span class="sr"> ' + esc(S.meta.mode) + ' mode</span></span>' : '') + (q ? '<span class="nb" title="a question waits for you">? ' + q + '</span>' : '') + '<span class="sr">' + ST_WORD[st] + (q ? ', ' + q + ' question' + (q > 1 ? 's' : '') + ' waiting' : '') + '</span></button><button class="stx" type="button" data-close="' + esc(S.id) + '" aria-label="Close ' + esc(S.name) + '" title="Close this session…">×</button></div>';
      }).join('') + '</div>';
      h += '<button class="sact" type="button" id="sNew" title="start another session (all the chat flags)"><span aria-hidden="true">+</span> New</button><button class="sact" type="button" id="sRes" title="resume a recorded session (↺)"><span aria-hidden="true">↺</span> Resume</button><button class="sact" type="button" id="sOpt" aria-haspopup="menu" title="rename, restart, stop or close the active session" aria-label="Session menu">⋯</button><span class="sp"></span>';
      h += '<button class="sact inbox' + (nd.length ? ' has' : '') + '" type="button" id="sInbox" aria-haspopup="dialog" aria-expanded="false" title="open questions in every session">Needs you <b class="num">' + nd.length + '</b></button>';
      h += '<button class="sact gbud" type="button" id="sBud" aria-haspopup="dialog" title="spent by every session this page runs (list prices)"><span class="dim">all ' + n + '</span> <b class="num" data-gbud>' + fmtUsd(spent, 2) + '</b>' + (cap ? ' <span class="dim">of ' + fmtUsd(cap, 2) + '</span>' : '') + '</button>';
      const upd = SL.live.hello && SL.live.hello.update; if (upd && upd.latest && upd.latest !== upd.current) h += '<button class="tag ok" type="button" id="sUpd" title="' + esc(upd.notice || 'a newer release is out: sleipnir update installs it') + '">↑ ' + esc(upd.latest) + '</button>';
      h += connHtml();
      return h;
    }
    /** The connection chip: the server's address; `· loopback · token ✓` while the stream is up, `○ … · reconnecting` (warm) while not. */
    function connHtml() {
      const L = SL.live, addr = esc(L.addr), st = L.state;
      if (st === 'open' || st === 'connecting') return '<span class="conn" id="conn" title="the page is served from loopback and carries a launch token; the token is held in this tab"><span class="on">●</span> ' + addr + ' <span class="cx">· loopback · token <span class="tk">✓</span></span></span>';
      return '<span class="conn" id="conn" role="status" tabindex="0" style="color:var(--warm)" title="' + esc(st === 'down' ? 'the server is gone: ' + (L.why || 'it stopped') + '; the page tries again every 2 s' : 'the stream is down; the page reconnects by itself') + '"><span class="on" style="color:var(--warm)">○</span> ' + addr + ' <span class="cx" style="color:var(--warm)">· ' + (st === 'down' ? 'disconnected' : 'reconnecting') + '</span></span>';
    }
    sc.on('conn', () => { strip._sig = ''; renderStrip(); });
    function renderStrip() {
      const sig = SL.sessions.list.map(S => S.id + S.name + S.state() + (S.meta.headless ? 'h' : '') + S.meta.mode).join('|') + '|' + (SL.sessions.active && SL.sessions.active.id) + '|' + SL.sessions.needs().map(x => x.S.id + x.q.id).join(',') + '|' + SL.live.state;
      if (strip._sig !== sig) { strip._sig = sig; const fx = document.activeElement && document.activeElement.dataset ? (document.activeElement.dataset.sid || document.activeElement.id) : null; const tb = strip.querySelector('.stabs'), keep = tb ? tb.scrollLeft : 0; strip.innerHTML = stripHtml(); const tb2 = strip.querySelector('.stabs'); if (tb2) tb2.scrollLeft = keep; if (fx && !strip.contains(document.activeElement)) { const b = strip.querySelector('[data-sid="' + fx + '"]') || (fx && strip.querySelector('#' + CSS.escape(fx))); if (b) b.focus({ preventScroll: true }); } const sel = strip.querySelector('.stab.sel'); if (sel && tb2 && tb2.scrollWidth > tb2.clientWidth) { const r = sel.closest('.stabw'); if (r.offsetLeft < tb2.scrollLeft || r.offsetLeft + r.offsetWidth > tb2.scrollLeft + tb2.clientWidth) tb2.scrollLeft = r.offsetLeft - 8; } }
      stripCosts();
    }
    function stripCosts() {
      let spent = 0; SL.sessions.list.forEach(S => { spent += calc.totals(S.wm).cost; });
      $$('[data-cost]', strip).forEach(el => { const S = SL.sessions.get(el.dataset.cost); if (S) { const t = fmtUsd(calc.totals(S.wm).cost, 2); if (el.textContent !== t) el.textContent = t; } });
      const g = $('[data-gbud]', strip); if (g) { const t = fmtUsd(spent, 2); if (g.textContent !== t) g.textContent = t; }
    }
    sc.update(renderStrip); sc.on('sessions-changed', () => { strip._sig = ''; renderStrip(); }); sc.on('needs-changed', () => { strip._sig = ''; renderStrip(); }); sc.on('activated', () => { strip._sig = ''; renderStrip(); });
    /** The Close confirm. An isolated team's verified work is applied first: the confirm says how, and the result toast is the
     *  server's integration report. */
    ui.closeSessionAsk = S => { if (S.placeholder) return; const iso = S.meta.isolation === 'worktree' && S.roster.length > 1 && !S.recorded;
      ui.confirm({ title: 'Close the session', text: 'Close <b>' + esc(S.name) + '</b>? Its team stops and the tab goes away. The recorded log stays on disk and can be resumed with ↺.' + (iso ? ' Its verified work is applied to your checkout first (' + (S.meta.commit ? 'as commits on the current branch' : 'as uncommitted edits') + ').' : ''), detail: SL.sessions.list.length < 2 && !S.recorded ? '<span class="warm">This is the last session: it cannot be closed. Start another first.</span>' : (SL.calc.openQuestion(S.wm) ? '<span class="warm">A question is still open in this session; closing it refuses the command.</span>' : ''), ok: 'Close it', danger: true, run: () => { const r = SL.act.closeSession(S.id); if (r && r.ok === false) { ui.toast(r.why || 'the last session cannot be closed: start another first', 'warm'); return; }
        if (r && r.done) r.done.then(res => { if (!res || !res.ok) return; const ig = res.data && res.data.integration; if (ig && typeof ig === 'object' && ig.message) ui.toast(ig.message + (ig.applied === false && ig.hint ? ' (' + ig.hint + ')' : ''), ig.applied === false ? 'err' : 'ok'); else if (typeof ig === 'string' && ig) ui.toast(ig, / \(.*\)$/.test(ig) ? 'err' : 'ok'); else ui.toast('closed ' + S.name); }); } }); };
    sc.listen(strip, 'click', e => {
      const x = e.target.closest('[data-close]'); if (x) { const S = SL.sessions.get(x.dataset.close); if (S) ui.closeSessionAsk(S); return; }
      const t = e.target.closest('[data-sid]'); if (t) { SL.act.switchSession(t.dataset.sid); return; }
      if (e.target.closest('#sUpd')) { ui.openCommand ? ui.openCommand(['update']) : SL.views.show('tools'); return; }
      if (e.target.closest('#conn')) { const L = SL.live; ui.toast(L.state === 'open' ? 'connected to ' + L.addr : L.state === 'down' ? 'disconnected: the page tries again every 2 s' : 'reconnecting to ' + L.addr, L.state === 'open' ? '' : 'warm'); return; }
      if (e.target.closest('#sNew')) ui.dialogs.newSession(); else if (e.target.closest('#sRes')) ui.dialogs.resume(); else if (e.target.closest('#sInbox')) ui.inbox.toggle(e.target.closest('#sInbox')); else if (e.target.closest('#sOpt')) ui.sessionMenu(e.target.closest('#sOpt')); else if (e.target.closest('#sBud')) budgetPop(e.target.closest('#sBud'));
    });
    sc.listen(strip, 'dblclick', e => { const t = e.target.closest('[data-sid]'); if (t) { const S = SL.sessions.get(t.dataset.sid); if (S) ui.dialogs.rename(S); } });
    sc.listen(strip, 'auxclick', e => { if (e.button !== 1) return; const t = e.target.closest('[data-sid]'); if (t) { e.preventDefault(); const S = SL.sessions.get(t.dataset.sid); if (S) ui.closeSessionAsk(S); } });
    sc.listen(strip, 'keydown', e => {
      const bs = $$('.stab', strip), i = bs.indexOf(document.activeElement);
      if ((e.key === 'ArrowRight' || e.key === 'ArrowLeft') && i >= 0) { e.preventDefault(); bs[(i + (e.key === 'ArrowRight' ? 1 : bs.length - 1)) % bs.length].focus(); }
      else if (e.key === 'Home' && i >= 0) { e.preventDefault(); bs[0].focus(); } else if (e.key === 'End' && i >= 0) { e.preventDefault(); bs[bs.length - 1].focus(); }
      else if ((e.key === 'Enter' || e.key === ' ') && i >= 0) { e.preventDefault(); SL.act.switchSession(bs[i].dataset.sid); }
      else if (e.key === 'F2' && i >= 0) { const S = SL.sessions.get(bs[i].dataset.sid); if (S) ui.dialogs.rename(S); }
      else if (e.key === 'Delete' && i >= 0) { const S = SL.sessions.get(bs[i].dataset.sid); if (S) ui.closeSessionAsk(S); }
    });
    /** the all-sessions budget: spent against each session's own budget (the budget is per session; this is the sum) */
    function budgetPop(btn) {
      const ex = $('.budpop'); if (ex) { if (ex._off) ex._off(); else ex.remove(); return; }
      const rows = SL.sessions.list.map(S => { const c = calc.totals(S.wm), b = S.meta.budget; return '<tr><td class="bright">' + esc(S.name) + '</td><td class="r num">' + fmtUsd(c.cost, 3) + '</td><td class="r dim num">' + (b ? 'of ' + fmtUsd(b, 2) : 'no budget') + '</td><td><div class="gauge wide"><i style="width:' + (b ? Math.min(100, c.cost / b * 100) : 0) + '%"></i></div></td></tr>'; }).join('');
      const unp = SL.sessions.list.reduce((s, S) => s + calc.totals(S.wm).unpriced, 0);
      const p = mk('div', { class: 'popover budpop', role: 'dialog', 'aria-label': 'Spend of every session' }, '<h3 class="lab" style="margin:0 0 8px">spend of every session</h3><table class="tbl"><tbody>' + rows + '</tbody></table><p class="stubnote" style="margin:8px 0 0">Each session has its own budget (--budget-usd); a turn pauses itself at its limit. List prices; the sum is an estimate' + (unp > 0 ? '; ' + U.fmtN(unp) + ' tokens at unknown prices are not counted' : '') + '.</p>');
      const app = $('#app'); app.appendChild(p); const r = btn.getBoundingClientRect(), ar = app.getBoundingClientRect(); p.style.top = (r.bottom - ar.top + 4) + 'px'; p.style.right = Math.max(8, ar.right - r.right) + 'px';
      const off = () => { p.remove(); document.removeEventListener('pointerdown', away, true); document.removeEventListener('keydown', esck, true); };
      const away = e => { if (!p.contains(e.target) && !e.target.closest('#sBud')) off(); }, esck = e => { if (e.key === 'Escape') { e.stopPropagation(); off(); btn.focus(); } };
      document.addEventListener('pointerdown', away, true); document.addEventListener('keydown', esck, true); p._off = off; }
    sc.interval(() => { if (!document.hidden) stripCosts(); }, 1000);

    /* ---- footer ---- */
    function renderFooter(S, m) {
      const mg = m.ag.mgr, c = calc.totals(m), q = calc.openQuestion(m), slow = ui.waitingFor(mg, S.vt), word = m.goal.state === 'met' ? 'goal met' : q ? q.agent + ' waits for your answer' : mg.state === 'wait' ? 'manager waiting for the team' : S.replay ? 'replay' : slow ? 'manager ' + slow : 'manager ' + (mg.doing || mg.state);
      const col = m.goal.state === 'met' ? 'var(--ok)' : q ? 'var(--warm)' : 'var(--mgr)', sl = $('#statusLine');
      const h = '<span class="dot" style="--c:' + col + '">●</span><span>' + esc(word) + '</span><span class="dim">·</span><span class="num">' + mmss(S.vt) + '</span><span class="dim">·</span><span class="num">↑' + U.fmtK(mg.rd + mg.un) + ' ↓' + U.fmtK(mg.out) + '</span><span class="dim">·</span><span class="num">' + fmtUsd(c.cost, 2) + '</span><span class="dim">·</span><span><kbd>esc</kbd> to interrupt</span>' + (m.goal.state === 'met' ? '<button class="btn sm" type="button" data-act="again">↺ run it again</button>' : '');
      if (sl._h !== h) { sl._h = h; sl.innerHTML = h; }
      txt('fm', $('#footModel'), S.placeholder ? '' : S.meta.model + ' · ' + S.sid);
      txt('rm', $('#railModel'), S.meta.model);
    }
    sc.listen($('#statusLine'), 'click', e => { if (e.target.closest('[data-act="again"]')) SL.act.restartTeam({ swarm: SL.sessions.active.meta.swarm, force: true }); });

    /* ---- the mode chip (composer) ---- */
    /* the permission mode of the chat box: a dropdown. The button only opens the menu; a mode changes when an item is picked (or by shift+tab in the message box, which cycles default, accept-edits, plan) */
    const mBtn = $('#modeBtn');
    function paintMode(S) { const d = S.meta.mode === 'bypass' || S.meta.mode === 'yolo'; txt('mode', $('#modeTxt'), (d ? '⚠ ' : '') + S.meta.mode); mBtn.classList.toggle('danger', d); mBtn.setAttribute('aria-label', 'Permission mode: ' + S.meta.mode + (d ? ' (dangerous)' : '') + '. Opens a menu.'); mBtn.title = 'Permission mode: ' + S.meta.mode + '. Opens a menu; nothing changes until you pick one. shift+tab in the message box cycles default, accept-edits, plan.'; }
    const MODE_MENU = ['default', 'accept-edits', 'plan'];
    /** Open or close the mode menu above the button. Esc, a click outside or Tab closes it; a pick applies and closes it. */
    ui.modeMenu = {
      isOpen: () => !!$('.modemenu'),
      close(refocus) { const p = $('.modemenu'); if (p && p._off) p._off(refocus); },
      toggle() {
        const old = $('.modemenu'); if (old) { old._off(true); return; }
        const S = SL.sessions.active, app = $('#app'), cur = S.meta.mode, dng = cur === 'bypass' || cur === 'yolo';
        const p = mk('div', { class: 'popover modemenu', id: 'modeMenu', role: 'menu', 'aria-label': 'Permission mode' },
          MODE_MENU.map(id => '<button role="menuitemradio" type="button" data-mode="' + id + '" aria-checked="' + (cur === id) + '" tabindex="-1"><i class="ck" aria-hidden="true">' + (cur === id ? '✓' : '') + '</i><span class="mn">' + id + '</span><span class="md">' + esc(SL.D.modes.find(x => x.id === id).desc) + '</span></button>').join('') +
          '<hr role="separator"><button role="menuitem" type="button" class="dngi" data-dangerous tabindex="-1"><i class="ck" aria-hidden="true">' + (dng ? '⚠' : '') + '</i><span class="mn">Dangerous modes…</span><span class="md">' + (dng ? esc(cur) + ' is on now. ' : '') + 'bypass and yolo: set by typing the name to confirm</span></button>');
        app.appendChild(p); mBtn.setAttribute('aria-expanded', 'true');
        const r = mBtn.getBoundingClientRect(), ar = app.getBoundingClientRect(), w = Math.min(p.offsetWidth || 300, ar.width - 16);
        p.style.bottom = (ar.bottom - r.top + 6) + 'px'; p.style.left = Math.max(8, Math.min(r.left - ar.left, ar.width - w - 8)) + 'px';
        const items = () => Array.from(p.querySelectorAll('[role^="menuitem"]'));
        const away = e => { if (!p.contains(e.target) && !mBtn.contains(e.target)) off(false); };
        const key = e => {
          const its = items(), i = its.indexOf(document.activeElement);
          if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); off(true); }
          else if (e.key === 'ArrowDown') { e.preventDefault(); e.stopPropagation(); its[(i + 1) % its.length].focus(); }
          else if (e.key === 'ArrowUp') { e.preventDefault(); e.stopPropagation(); its[(i + its.length - 1) % its.length].focus(); }
          else if (e.key === 'Home') { e.preventDefault(); e.stopPropagation(); its[0].focus(); }
          else if (e.key === 'End') { e.preventDefault(); e.stopPropagation(); its[its.length - 1].focus(); }
          else if (e.key === 'Tab') off(false);
        };
        function off(refocus) { if (!p.parentNode) return; p.remove(); p._off = null; mBtn.setAttribute('aria-expanded', 'false'); document.removeEventListener('pointerdown', away, true); document.removeEventListener('keydown', key, true); if (refocus) mBtn.focus(); }
        p._off = off; document.addEventListener('pointerdown', away, true); document.addEventListener('keydown', key, true);
        p.addEventListener('click', e => {
          const it = e.target.closest('[data-mode],[data-dangerous]'); if (!it) return;
          if (it.hasAttribute('data-dangerous')) { off(true); ui.sheets.mode(); return; }
          const id = it.dataset.mode; off(true);
          if (id !== SL.sessions.active.meta.mode) { const r2 = SL.act.setMode(id); if (r2 && r2.ok === false) ui.toast(r2.why, 'err'); else ui.toast('mode: ' + id); }
        });
        const first = p.querySelector('[aria-checked="true"]') || items()[0]; first.focus();
      },
    };
    sc.listen(mBtn, 'click', () => ui.modeMenu.toggle());
    sc.listen(mBtn, 'keydown', e => { if ((e.key === 'ArrowUp' || e.key === 'ArrowDown') && !ui.modeMenu.isOpen()) { e.preventDefault(); ui.modeMenu.toggle(); } });
    sc.onUnmount(() => ui.modeMenu.close(false));
    sc.listen($('#qBanner'), 'click', () => { if (ui.rail) ui.rail.open(); $('#app').dataset.pv = 'radio'; });
  }
  ui.shell = { mount };
})(SL);
