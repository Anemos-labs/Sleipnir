/* 80-ui-shell.js: the persistent chrome (SL.ui.shell): HUD, session strip, view tabs, replay banner, footer, phone nav.
 * It is mounted once, under the shell scope, and re-renders from the active session. Nothing here is view-specific.
 *
 * Each fact is said once. The header holds the goal and how long the session has run, what it spent against its budget, and the
 * team (how many workers are busy, and a warning only while rate limits or provider errors slow it down). The cache rings (reuse, and how long the
 * cache stays warm) show only with the full cache detail (Settings › Appearance); otherwise that detail is the Cache view's cards
 * (cache.sum, cache.hit, cache.head) and the rate detail is the Cockpit's Governor card (gov). The session strip names the sessions,
 * and the spend of all of them only when there are several; the status bar says what the session is doing and its tokens, and the
 * model and session id. The warm ring's 25 ticks are a fraction of the prefix's real cache lifetime (m.ttl); the connection chip
 * says connected or reconnecting, with the server's address in its tooltip. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, fmtUsd, fmtSpend, planTag, spendOf, mmss, clock, pct, hitCls } = U, calc = SL.calc;
  const ui = SL.ui = SL.ui || {};
  const WARM_LIFE = 25, WARM_RED = 8, RING_C = 2 * Math.PI * 22, HIT_C = 2 * Math.PI * 19;
  const SESS_GLYPH = { run: '●', ask: '?', idle: '◌', done: '✓', paused: '⏸' };
  const ST_WORD = { run: 'running', ask: 'needs you', idle: 'at the prompt', done: 'done', paused: 'paused' };
  /** "N tokens at unknown prices are not counted" when some are, else ''. */
  const unpricedNote = n => n > 0 ? '; ' + U.fmtN(n) + ' tokens at unknown prices are not counted' : '';
  /** A turn's model request without an answer for 45 s or more: "waiting for the model (1m 05s)". */
  ui.waitingFor = (A, vt) => { if (!A || A.reqSince == null || !['think', 'tool', 'edit', 'wait'].includes(A.state)) return ''; const s = vt - A.reqSince; if (s < 45) return ''; const n = Math.floor(s); return 'waiting for the model (' + (n < 60 ? n + 's' : Math.floor(n / 60) + 'm ' + String(n % 60).padStart(2, '0') + 's') + ')'; };

  /** The background jobs of a session (meta.jobs: the server's list, oldest first). */
  const jobsOf = S => (S && S.meta && Array.isArray(S.meta.jobs) ? S.meta.jobs : []);
  /** A local address a job named, as a link that opens it in a new tab; anything that is not an http(s) address stays text. */
  const jobLink = u => (/^https?:\/\//.test(String(u)) ? '<a class="mono" href="' + esc(u) + '" target="_blank" rel="noopener noreferrer">' + esc(u) + '</a>' : '<span class="mono">' + esc(u) + '</span>');
  /** The rows of the jobs dialog: running jobs first, each with its addresses under it and a Stop button. */
  function jobsHtml(S) {
    const js = jobsOf(S); if (!js.length) return '<p class="stubnote">No background jobs. An agent starts one with <span class="mono">bash</span> and <span class="mono">run_in_background</span>: a dev server, a watcher, a long test run. When its output names a local address (<span class="mono">http://localhost:5173/</span>), the address shows here to open.</p>';
    const order = js.filter(j => j.status === 'running').concat(js.filter(j => j.status !== 'running'));
    const st = j => j.status === 'running' ? '<span class="tag ok">running</span>' : j.status === 'killed' ? '<span class="tag warm">killed</span>' : '<span class="tag' + (j.exit ? ' err' : '') + '">exited ' + esc(String(j.exit == null ? '' : j.exit)) + '</span>';
    return '<table class="tbl jobs"><thead><tr><th>job</th><th>agent</th><th>command</th><th>state</th><th></th></tr></thead><tbody>' + order.map(j => '<tr><td class="mono bright">' + esc(j.id) + '</td><td style="color:' + U.agCol(String(j.agent).replace(/[^A-Za-z0-9_-]/g, '_')) + ';font-weight:700">' + esc(j.agent) + '</td><td class="mono">' + esc(j.command) + '</td><td>' + st(j) + '</td><td class="r">' + (j.status === 'running' ? '<button class="btn sm" type="button" data-stopjob="' + esc(j.id) + '">Stop</button>' : '') + '</td></tr>' +
      (j.urls && j.urls.length ? '<tr class="jurls"><td></td><td colspan="4">' + j.urls.map(jobLink).join(' · ') + '</td></tr>' : '')).join('') + '</tbody></table>';
  }
  /** The jobs dialog (the status bar's chip, /jobs): the session's background jobs, live, with their addresses to open and Stop. */
  ui.jobs = {
    html: jobsHtml,
    open() {
      const S0 = SL.sessions.active; if (!S0) return;
      ui.modal({ title: 'Background jobs', kicker: '/jobs', desc: 'what the agents left running', wide: true, body: '<div class="jobsb">' + jobsHtml(S0) + '</div>',
        onMount(b, scm) {
          const box = $('.jobsb', b); let last = box.innerHTML;
          scm.update(S => { if (!S) return; const h = jobsHtml(S); if (h !== last) { last = h; box.innerHTML = h; } });
          scm.listen(b, 'click', e => {
            const btn = e.target.closest('[data-stopjob]'); if (!btn) return; const id = btn.dataset.stopjob; btn.disabled = true;
            const r = SL.act.command('/jobs stop ' + id); if (r && r.ok === false) { btn.disabled = false; if (r.why) ui.toast(r.why, 'warm'); return; }
            r.done.then(x => { if (!x.ok) { btn.disabled = false; ui.toast(x.message || 'the job could not be stopped', 'warm'); return; } const out = String((x.data && x.data.output) || '').trim(); ui.toast(out || 'stopped ' + id, /^stopped/.test(out) ? 'ok' : 'warm'); });
          });
        } });
    }
  };

  function mount(sc) {
    const H = { disp: 1, txt: {}, ticks: [], lastReq: -9999, pulseT: -1 };
    const txt = (key, el, v) => { if (H.txt[key] !== v) { H.txt[key] = v; el.textContent = v; } };
    /* ---- HUD ---- */
    const g = $('#warmTicks');
    for (let i = 0; i < WARM_LIFE; i++) { const a = -Math.PI / 2 + i / WARM_LIFE * 2 * Math.PI, c = Math.cos(a), s = Math.sin(a), ln = U.sv('line', { class: 'ring-tick', x1: 32 + 26 * c, y1: 32 + 26 * s, x2: 32 + 30 * c, y2: 32 + 30 * s }, g); H.ticks.push(ln); }
    $('#warmArc').style.strokeDasharray = RING_C; $('#hitArc').style.strokeDasharray = HIT_C;
    sc.update((S, m) => {
      if (!S || !m) return; const c = calc.totals(m), meta = S.meta, nW = Math.max(S.roster.length - 1, meta.swarm || 0);   // a team's size is its slots, not the workers started so far
      const goalText = meta.goalText || (m.goal.state !== 'cleared' && m.goal.objective) || '';   /* the server's goal events name it too */
      $('#goalLab').textContent = 'Goal'; const gs = $('#goalState'); gs.hidden = !goalText; const gst = m.goal.state; gs.textContent = gst === 'met' ? 'met ✓' : gst === 'paused' ? 'paused' : gst === 'cleared' ? 'cleared' : 'active'; gs.style.color = gst === 'met' ? 'var(--ok)' : gst === 'paused' ? 'var(--warm)' : '';
      txt('goalT', $('#goalT'), goalText || 'No goal yet: talk with the manager, or type /goal to give the team one');
      txt('cost', $('#vCost'), fmtSpend(c, 2)); $('#vCost').title = c.plan ? fmtSpend(c, 4) + ' ' + U.PLAN_HELP : fmtUsd(c.cost, 4) + ' (list prices)' + unpricedNote(c.unpriced);
      /* a plan's usage fills no budget: the budget counts what is billed per token, and the plan bills nothing per token. A session that
         mixes the two fills its gauge with the billed part; one that is all the plan's says the budget is not touched */
      { const only = U.planOnly(c); txt('budget', $('#vBudget'), only ? 'on your plan' : (c.plan ? 'plan · ' : '') + (meta.budget ? 'of ' + fmtUsd(meta.budget, 2) + ' budget' : 'no budget limit')); $('#vBudget').className = c.plan ? 'plantag' : '';
        const gf = $('#gaugeFill'), gw = gf.parentNode, hb = gw.parentNode; gw.classList.toggle('plan', !!c.plan); gw.setAttribute('aria-label', only ? 'budget not used: the plan bills nothing per token' : !meta.budget ? 'no budget set' : 'budget used: ' + Math.round(U.budgetPct(c, meta.budget)) + '% billed, ' + fmtUsd(c.cost, 2) + ' of ' + fmtUsd(meta.budget, 2));
        hb.title = c.plan ? U.PLAN_HELP + ': the budget (--budget-usd) counts only what is billed per token' + (meta.budget ? ', ' + fmtUsd(c.cost, 2) + ' of ' + fmtUsd(meta.budget, 2) + ' so far' : '') : 'the budget of this session (--budget-usd)';
        gf.style.width = (meta.budget && !only ? Math.max(1.6, U.budgetPct(c, meta.budget)) : 0) + '%'; }
      $('#hitArc').style.strokeDashoffset = HIT_C * (1 - c.pct / 100); txt('hit', $('#hitTxt'), String(c.pct)); $('#ringHit').title = 'cache hit ratio: ' + U.fmtN(c.read) + ' of ' + U.fmtN(c.prompt) + ' prompt tokens were read from the cache (' + c.hit1 + '%). The Cache view has the detail.';
      $('#ringHit').classList.toggle('dip', c.pct < 80);
      txt('teamLab', $('#teamLab'), nW ? 'manager + ' + nW + ' worker' + (nW === 1 ? '' : 's') : 'single agent');
      { const solo = !nW; $('#vAct').hidden = solo; if (!solo) { txt('act', $('#vActN'), String(calc.active(m))); txt('actOf', $('#vActOf'), 'of ' + nW); } }
      /* rate limits are said only while they slow the team down; the rates themselves are the Cockpit's Governor card */
      { const r4 = m.r429 || 0, rt = m.retries || 0, slow = r4 + rt > 0, ge = $('#govTxt'), gt = slow ? (r4 ? 'slowed by rate limits: ' + r4 + ' refused (429) · ' : 'slowed by provider errors: ') + rt + ' retr' + (rt === 1 ? 'y' : 'ies') : ''; ge.hidden = !slow; txt('gov', ge, gt); ge.title = slow ? (r4 ? 'the provider refused or delayed requests' : 'requests failed (network errors, 5xx) and were retried') + '; the Governor card of the Cockpit shows requests per minute, 429s and retries' : ''; }
      $('#vAct').parentNode.title = nW ? 'the team: ' + nW + ' worker slots · ' + calc.started(m) + ' started · ' + calc.active(m) + ' working now (not idle, not done)' : 'one agent: the manager works alone (no workers)';
      const cut = S.truncated ? '<span class="warm">showing the first ' + U.fmtN(S.truncated) + ' events: the log has more</span>' : '';
      const b = $('#banner'); if (S.loading) { const sec = Math.max(0, Math.ceil((S.loading.next - Date.now()) / 1000)); b.hidden = false; b.innerHTML = '<b>Not loaded</b><span class="warm">could not load ' + esc(S.name) + ': ' + (sec ? 'retrying in ' + sec + ' s' : 'retrying') + '</span><span class="dim">' + esc(S.loading.why) + '</span><span class="sp"></span>'; }
      else if (S.replay) { b.hidden = false; b.innerHTML = '<b>Replay</b><span>' + mmss(S.vt) + ' of ' + mmss(S.wt) + '</span><span>' + (S.replay.playing ? 'playing ' + S.replay.speed + 'x' : 'paused') + '</span><span class="dim">' + (S.recorded ? (S.follow ? 'watching ' + esc(S.sid) + ' · read-only · the run belongs to another process' : esc(S.sid) + ' · a recorded session, read-only') : 'the whole cockpit follows the log; actions act on the live session') + '</span>' + cut + '<span class="sp"></span><button type="button" data-act="live">go live</button>'; }
      else if (S.recorded) { b.hidden = false; b.innerHTML = '<b>' + (S.follow ? 'Watching' : 'Recorded') + '</b><span class="dim">' + (S.follow ? 'watching ' + esc(S.sid) + ' · read-only · the run belongs to another process' : esc(S.sid) + ' · a recorded session, read-only: ↺ Resume continues it') + '</span>' + cut + '<span class="sp"></span>'; }
      else b.hidden = true;
      const q = calc.openQuestion(S.wm), qb = $('#qBanner');   /* questions come from the world model: a replay never hides one */ qb.hidden = !q; if (q) $('#qBannerT').textContent = q.agent + ' asks: ' + q.cmd;
      renderTabs(S); renderFooter(S, m); paintMode(S);
    });
    sc.frame((dtView, vt, S) => {
      if (!S || !S.m) return; const m = S.m;
      txt('el', $('#vElapsed'), mmss(vt)); const gap = S.replay ? S.wt - vt : 0, lh = $('#liveHint'); lh.hidden = gap < 1; if (gap >= 1) txt('lh', lh, ' · live +' + Math.round(gap) + 's');   /* a replay: how far behind the live edge it is */
      const left = calc.warmLeft(m, vt), tgt = Math.max(0, left) / (m.ttl || WARM_LIFE), rw = $('#ringWarm'), low = left > 0 && left < WARM_RED, cold = left <= 0, still = ui.still();
      if (tgt > H.disp) H.disp = still ? tgt : Math.min(tgt, H.disp + dtView * 3.4); else H.disp = tgt;
      rw.classList.toggle('low', low); rw.classList.toggle('cold', cold); $('#warmArc').style.strokeDashoffset = RING_C * (1 - H.disp);
      const lit = Math.ceil(H.disp * WARM_LIFE - 1e-6); for (let i = 0; i < H.ticks.length; i++) { const on = i < lit; if (H.ticks[i]._on !== on) { H.ticks[i]._on = on; H.ticks[i].classList.toggle('on', on); } }
      txt('warm', $('#warmTxt'), cold ? 'cold' : clock(left)); const wt = $('#warmTxt'); wt.style.fill = cold || low ? 'var(--err)' : ''; wt.style.fontSize = cold ? '12px' : '15px';
      txt('wnote', $('#warmNote'), cold ? 'expired: the next request pays full price' : low ? 'expires in ' + clock(left) + ' unless a request lands' : 'refills on each request');
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
      const act = SL.sessions.active, nd = SL.sessions.needs(); let spent = 0, cap = 0, plans = false, n = SL.sessions.list.length;
      SL.sessions.list.forEach(S => { const t = calc.totals(S.wm); spent += spendOf(t); if (t.plan) plans = true; cap += S.meta.budget || 0; });
      let h = '<div class="stabs" role="tablist" aria-label="Sessions">' + SL.sessions.list.map(S => {
        const st = S.state(), q = nd.filter(x => x.S === S).length, sel = S === act, gl = (S.meta.headless || S.follow) && st === 'run' ? '⟳' : SESS_ICON[st];
        return '<div class="stabw' + (sel ? ' sel' : '') + '" role="presentation" data-state="' + st + '"><button class="stab' + (sel ? ' sel' : '') + '" role="tab" type="button" data-sid="' + esc(S.id) + '" aria-selected="' + sel + '" data-state="' + st + '" title="' + esc(S.name + ' · ' + S.meta.cwd + ' · ' + ST_WORD[st] + (S.meta.headless ? ' · headless job: nobody can answer' : '') + ' (double-click to rename, middle-click to close)') + '"><i class="sg" aria-hidden="true">' + gl + '</i><span class="sn">' + esc(S.name) + '</span>' + (S.meta.mode === 'bypass' || S.meta.mode === 'yolo' ? '<span class="dgm" title="' + esc(S.meta.mode) + ' mode: this session does not ask before it acts">⚠<span class="sr"> ' + esc(S.meta.mode) + ' mode</span></span>' : '') + (q ? '<span class="nb" title="a question waits for you">? ' + q + '</span>' : '') + '<span class="sr">' + ST_WORD[st] + (q ? ', ' + q + ' question' + (q > 1 ? 's' : '') + ' waiting' : '') + '</span></button><button class="stx" type="button" data-close="' + esc(S.id) + '" aria-label="Close ' + esc(S.name) + '" title="Close this session…">×</button></div>';
      }).join('') + '</div>';
      h += '<button class="sact" type="button" id="sNew" title="start another session (all the chat flags)"><span aria-hidden="true">+</span> New</button><button class="sact" type="button" id="sRes" title="resume a recorded session (↺)"><span aria-hidden="true">↺</span> Resume</button><button class="sact" type="button" id="sOpt" aria-haspopup="menu" title="rename, restart, stop or close the active session" aria-label="Session menu">⋯</button><span class="sp"></span>';
      /* Needs you: with a question open anywhere, a loud badge (how many, how long the oldest has waited) that opens the oldest one, and
         beside it the list of all of them; with none, a quiet button that opens the empty list */
      const nq = nd.reduce((n, x) => n + (x.waiting || 1), 0);
      h += nd.length ? '<button class="sact inbox has inboxbtn" type="button" id="sInbox" data-needgo title="open the question that has waited longest (switches to its session)" aria-label="' + nq + ' question' + (nq === 1 ? '' : 's') + ' wait for you: open the oldest"><i class="nyq" aria-hidden="true">?</i>Needs you <b class="num">' + nq + '</b><span class="nyage num" data-needage></span></button><button class="sact inboxall inboxbtn" type="button" id="sInboxAll" aria-haspopup="dialog" aria-expanded="false" title="every open question, in every session" aria-label="Every open question">▾</button>'
        : '<button class="sact inbox inboxbtn" type="button" id="sInbox" aria-haspopup="dialog" aria-expanded="false" title="open questions in every session">Needs you <b class="num">0</b></button>';
      /* the spend of every session: one session's spend is the header's, so the sum shows only beside a second session */
      if (n > 1) h += '<button class="sact gbud" type="button" id="sBud" aria-haspopup="dialog" title="spent by every session this page runs, each against its own budget (list prices' + (plans ? '; ≈: ' + U.PLAN_HELP : '') + ')"><span class="dim">' + n + ' sessions spent</span> <b class="num" data-gbud>' + fmtSpend({ cost: spent, plan: plans }, 2) + '</b>' + (cap ? ' <span class="dim">of ' + fmtUsd(cap, 2) + '</span>' : '') + '</button>';
      const upd = SL.live.hello && SL.live.hello.update; if (upd && upd.latest && upd.latest !== upd.current) h += '<button class="tag ok" type="button" id="sUpd" title="' + esc(upd.notice || 'a newer release is out: sleipnir update installs it') + '">↑ ' + esc(upd.latest) + '</button>';
      h += connHtml();
      return h;
    }
    /** The connection chip: `● connected` while the stream is up (the address, loopback and the token in its tooltip), `○ reconnecting`
     *  or `○ disconnected` (warm) while not. */
    function connHtml() {
      const L = SL.live, addr = esc(L.addr), st = L.state;
      if (st === 'open' || st === 'connecting') return '<span class="conn" id="conn" title="connected to the Sleipnir server at ' + addr + ': served from loopback, with a launch token held in this tab"><span class="on">●</span> <span class="cx">connected</span></span>';
      return '<span class="conn" id="conn" role="status" tabindex="0" style="color:var(--warm)" title="' + esc(st === 'down' ? 'the server at ' + L.addr + ' is gone: ' + (L.why || 'it stopped') + '; the page tries again every 2 s' : 'the stream from ' + L.addr + ' is down; the page reconnects by itself') + '"><span class="on" style="color:var(--warm)">○</span> <span class="cx" style="color:var(--warm)">' + (st === 'down' ? (L.stopped ? 'the server stopped' : 'disconnected') : 'reconnecting') + '</span></span>';
    }
    sc.on('conn', () => { strip._sig = ''; renderStrip(); });
    /** The version beside the logo: the short label (v0.2.28), the full build in its tooltip (hello.server). */
    function paintVer() {
      const v = $('#brandVer'), sv = SL.live.hello && SL.live.hello.server; if (!v || !sv) return;
      const t = sv.label || sv.version || ''; if (v.textContent !== t) { v.textContent = t; v.title = 'sleipnir ' + (sv.build || sv.version || ''); }
    }
    function renderStrip() {
      paintVer();
      const sig = SL.sessions.list.length + '#' + SL.sessions.list.map(S => S.id + S.name + S.state() + (S.meta.headless ? 'h' : '') + S.meta.mode).join('|') + '|' + (SL.sessions.active && SL.sessions.active.id) + '|' + SL.sessions.needs().map(x => x.S.id + x.q.id + ':' + x.waiting).join(',') + '|' + SL.live.state;
      if (strip._sig !== sig) { strip._sig = sig; const fx = document.activeElement && document.activeElement.dataset ? (document.activeElement.dataset.sid || document.activeElement.id) : null; const tb = strip.querySelector('.stabs'), keep = tb ? tb.scrollLeft : 0; strip.innerHTML = stripHtml(); if (ui.approvals && ui.approvals.paintAges) ui.approvals.paintAges(); const tb2 = strip.querySelector('.stabs'); if (tb2) tb2.scrollLeft = keep; if (fx && !strip.contains(document.activeElement)) { const b = strip.querySelector('[data-sid="' + fx + '"]') || (fx && strip.querySelector('#' + CSS.escape(fx))); if (b) b.focus({ preventScroll: true }); } const sel = strip.querySelector('.stab.sel'); if (sel && tb2 && tb2.scrollWidth > tb2.clientWidth) { const r = sel.closest('.stabw'); if (r.offsetLeft < tb2.scrollLeft || r.offsetLeft + r.offsetWidth > tb2.scrollLeft + tb2.clientWidth) tb2.scrollLeft = r.offsetLeft - 8; } }
      stripCosts();
    }
    function stripCosts() {
      const g = $('[data-gbud]', strip); if (!g) return;
      let spent = 0, plans = false; SL.sessions.list.forEach(S => { const t = calc.totals(S.wm); spent += spendOf(t); if (t.plan) plans = true; });
      const t = fmtSpend({ cost: spent, plan: plans }, 2); if (g.textContent !== t) g.textContent = t;
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
      if (e.target.closest('#sNew')) ui.dialogs.newSession(); else if (e.target.closest('#sRes')) ui.dialogs.resume(); else if (e.target.closest('[data-needgo]')) ui.approvals.goto(); else if (e.target.closest('#sInbox,#sInboxAll')) ui.inbox.toggle(e.target.closest('#sInbox,#sInboxAll')); else if (e.target.closest('#sOpt')) ui.sessionMenu(e.target.closest('#sOpt')); else if (e.target.closest('#sBud')) budgetPop(e.target.closest('#sBud'));
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
      const rows = SL.sessions.list.map(S => { const c = calc.totals(S.wm), b = S.meta.budget; return '<tr><td class="bright">' + esc(S.name) + '</td><td class="r num">' + fmtSpend(c, 3) + planTag(c) + '</td><td class="r dim num">' + (b ? 'of ' + fmtUsd(b, 2) : 'no budget') + '</td><td><div class="gauge wide' + (c.plan ? ' plan' : '') + '"><i style="width:' + U.budgetPct(c, b) + '%"></i></div></td></tr>'; }).join('');
      const unp = SL.sessions.list.reduce((s, S) => s + calc.totals(S.wm).unpriced, 0), plans = SL.sessions.list.some(S => calc.totals(S.wm).plan);
      const p = mk('div', { class: 'popover budpop', role: 'dialog', 'aria-label': 'Spend of every session' }, '<h3 class="lab" style="margin:0 0 8px">spend of every session</h3><table class="tbl"><tbody>' + rows + '</tbody></table><p class="stubnote" style="margin:8px 0 0">Each session has its own budget (--budget-usd); a turn pauses itself at its limit. List prices; the sum is an estimate' + (unp > 0 ? '; ' + U.fmtN(unp) + ' tokens at unknown prices are not counted' : '') + '.' + (plans ? ' A figure marked <span class="plantag">plan</span> is ' + U.PLAN_HELP + ': no budget counts the plan\'s part, and its gauge fills with what was billed.' : '') + '</p>');
      const app = $('#app'); app.appendChild(p); const r = btn.getBoundingClientRect(), ar = app.getBoundingClientRect(); p.style.top = (r.bottom - ar.top + 4) + 'px'; p.style.right = Math.max(8, ar.right - r.right) + 'px';
      const off = () => { p.remove(); document.removeEventListener('pointerdown', away, true); document.removeEventListener('keydown', esck, true); };
      const away = e => { if (!p.contains(e.target) && !e.target.closest('#sBud')) off(); }, esck = e => { if (e.key === 'Escape') { e.stopPropagation(); off(); btn.focus(); } };
      document.addEventListener('pointerdown', away, true); document.addEventListener('keydown', esck, true); p._off = off; }
    sc.interval(() => { if (!document.hidden) stripCosts(); }, 1000);

    /* ---- footer ---- */
    function renderFooter(S, m) {
      const mg = m.ag.mgr, c = calc.totals(m), q = calc.openQuestion(S.wm), slow = ui.waitingFor(mg, S.vt), word = m.goal.state === 'met' ? 'goal met' : q ? q.agent + ' waits for your answer' : mg.state === 'wait' ? 'manager waiting for the team' : S.replay ? 'replay' : slow ? 'manager ' + slow : 'manager ' + (mg.doing || mg.state);
      const col = m.goal.state === 'met' ? 'var(--ok)' : q ? 'var(--warm)' : 'var(--mgr)', sl = $('#statusLine');
      /* the status bar says what the session does and its tokens; the time and the spend are the header's (on a phone, whose header
         has no room for the time, the time is said here instead); Esc is named by the rail's Interrupt turn while a turn runs */
      const h = '<span class="dot" style="--c:' + col + '">●</span><span>' + esc(word) + '</span><span class="dim ph-only">·</span><span class="num ph-only">' + mmss(S.vt) + '</span><span class="dim">·</span><span class="num" title="tokens of every agent of the session: ↑ sent to the model (input), ↓ written by it (output)">tokens ↑' + U.fmtK(c.prompt) + ' ↓' + U.fmtK(c.out) + '</span>' + (m.goal.state === 'met' ? '<button class="btn sm" type="button" data-act="again">↺ run it again</button>' : '') + jobsChip(S);
      if (sl._h !== h) { sl._h = h; sl.innerHTML = h; }
      txt('fm', $('#footModel'), S.placeholder ? '' : S.meta.model + ' · ' + S.sid);
    }
    sc.listen($('#statusLine'), 'click', e => { if (e.target.closest('[data-act="again"]')) SL.act.restartTeam({ swarm: SL.sessions.active.meta.swarm, force: true }); else if (e.target.closest('[data-act="jobs"]')) ui.jobs.open(); });
    /** The status bar's chip of the background jobs that run (a dev server, a watcher): how many, and how many serve a local address. */
    function jobsChip(S) {
      const run = jobsOf(S).filter(j => j.status === 'running'); if (!run.length) return '';
      const srv = run.filter(j => j.urls && j.urls.length).length;
      return '<button class="btn sm jobchip" type="button" data-act="jobs" title="the background jobs the agents started: open them, or stop one (/jobs)">' + (srv ? '▶ ' + srv + ' serving' + (run.length > srv ? ' · ' + run.length + ' jobs' : '') : '◷ ' + run.length + ' job' + (run.length === 1 ? '' : 's')) + '</button>';
    }

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
