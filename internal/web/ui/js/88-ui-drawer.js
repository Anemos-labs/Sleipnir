/* 88-ui-drawer.js: SL.ui.openDrawer(id), the per-agent drawer (details, tool log, mail, cache): READ-ONLY, it has no input and no button that acts on
 * the agent (the person talks to the manager only), and the layer stack drawing shared with the Cache view. The drawer lives in the CURRENT VIEW's own layer: switching view removes it with everything else the view owns.
 * "Files owned" are the Workspace index's rows whose last writer is the agent (ui.ws.filesOf); the layer sizes are the agent's latest prompt. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, frag, fmtK, fmtN, fmtUsd, tod, agCol, hitCls } = U, D = SL.D, calc = SL.calc, ui = SL.ui = SL.ui || {};

  /** Stacked bar of an agent's latest prompt by layer G0..G5: solid = read from the cache, hatched = paid in full; a miss is all pink hatch. */
  ui.layerStack = function (m, id, h, withBrk) {
    const A = m.ag[id], toks = D.layerToks(id), tot = toks.reduce((a, b) => a + b, 0), r = A.ratios.length ? A.ratios[A.ratios.length - 1] : 0, miss = A.ratios.length > 0 && r === 0, readTok = r * tot; let cum = 0, out = '';
    const wOf = tk => tot ? tk / tot * 100 : 100 / toks.length, fr = (a, b) => b ? a / b * 100 : 0;   /* no request yet: equal parts, nothing read */
    D.layers.forEach((l, i) => { const tk = toks[i], rd = Math.max(0, Math.min(tk, readTok - cum)); cum += tk; out += '<span class="ls ' + (miss ? 'miss' : '') + '" style="width:' + wOf(tk) + '%;--lc:' + l.col + '" title="' + l.id + ' ' + l.name + ': ' + fmtK(tk) + ' tokens' + (miss ? ', none read' : ', ' + Math.round(fr(rd, tk)) + '% read') + '"><i class="rd" style="width:' + fr(rd, tk) + '%"></i><i class="pd" style="left:' + fr(rd, tk) + '%"></i></span>'; });
    const brk = withBrk ? '<span class="brk" style="left:' + (tot ? toks.slice(0, 3).reduce((a, b) => a + b, 0) / tot * 100 : 50) + '%" title="breakpoint: the end of the shared prefix"></span>' : '';
    return { html: '<div class="stack" style="height:' + h + 'px;margin:0">' + out + brk + '</div>', tot, miss, r };
  };
  ui.miniStack = function (m, id) { const toks = D.layerToks(id), tot = toks.reduce((a, b) => a + b, 0), A = m.ag[id], r = A.ratios.length ? A.ratios[A.ratios.length - 1] : 0; let cum = 0; return '<div class="mini">' + D.layers.map((l, i) => { const tk = toks[i], rd = Math.max(0, Math.min(tk, r * tot - cum)), f = tk ? rd / tk : 0; cum += tk; return '<span style="width:' + (tot ? tk / tot * 100 : 100 / toks.length) + '%;display:flex;--lc:' + l.col + '"><i style="width:' + (f * 100) + '%"></i><i class="p" style="width:' + ((1 - f) * 100) + '%"></i></span>'; }).join('') + '</div>'; };

  /** The files an agent wrote last, from the Workspace index (C2's cache); [] while it loads. */
  const FILES_OF = (S, id) => (ui.ws && typeof ui.ws.filesOf === 'function' ? ui.ws.filesOf(S, id) || [] : []);
  /** "saved est." or, when some cache reads had no price, "saved at least" (PARITY A16). */
  ui.savedWord = c => c.savedPartial ? 'saved at least' : 'saved est.';
  const D_ = { cur: null };
  ui.closeDrawer = function () { const c = D_.cur; if (!c) return; D_.cur = null; SL.link.select(null); c.offs.forEach(f => f()); c.el.remove(); const S = SL.sessions.active; if (S) S.ui.drawer = null; if (c.prev && c.prev.focus && document.contains(c.prev)) c.prev.focus(); };
  ui.drawerOpen = () => !!D_.cur;

  ui.openDrawer = function (id) {
    const V = SL.views.V.cur, S = SL.sessions.active; if (!V || !S || !S.m || !S.m.ag[id]) return;
    const prev = D_.cur ? D_.cur.prev : document.activeElement, tab = D_.cur && D_.cur.id === id ? D_.cur.tab : 'info'; ui.closeDrawer();
    const layer = V.scope.layer('drawer-wrap'), el = mk('aside', { class: 'drawer in', role: 'dialog', 'aria-label': id + ' agent drawer', tabindex: '-1' }); layer.appendChild(el);
    const rec = D_.cur = { id, tab, el, offs: [], prev }; S.ui.drawer = id; SL.link.select(id);
    el.innerHTML = '<div class="dr-h"></div><div class="dr-tabs" role="tablist">' + [['info', 'Details'], ['log', 'Tool log'], ['mail', 'Mail'], ['cache', 'Cache']].map(([k, n]) => '<button type="button" role="tab" data-tab="' + k + '">' + n + '</button>').join('') + '</div><div class="dr-b talk" data-hold="chat" tabindex="0" role="log" aria-label="' + esc(id) + ' details, read-only"></div><p class="dr-ro">Read-only: you talk to the manager only. To change what ' + esc(id) + ' does, tell the manager in the chat.</p>';
    const sc = V.scope, body = $('.dr-b', el), head = $('.dr-h', el);
    const on = (t, ty, fn) => { sc.listen(t, ty, fn); };
    function render(S2, m) {
      if (!m || !m.ag[id]) return; const A = m.ag[id], c = calc.agent(A), col = agCol(id), [g, w] = ui.SG[A.spawned ? A.state : 'idle'];
      el.style.setProperty('--c', col);
      const hh = '<div class="r1"><span class="did">' + esc(id) + '</span><span class="drole">' + (id === 'mgr' ? 'manager' : esc(A.role)) + '</span><button class="x" type="button" data-x>esc · close</button></div><div class="r2"><span class="chip" style="color:' + col + ';border-color:' + col + '">' + g + ' ' + w + '</span>' + (A.task ? '<span class="chip task" style="--c:' + col + '" data-task="' + esc(A.task) + '">' + esc(A.task) + '</span>' : '') + '<span class="chip">' + esc(A.model) + '</span><span class="chip">' + fmtK(c.prompt) + ' tok</span><span class="chip">' + fmtUsd(c.cost, 3) + '</span>' + (SL.settings.cache === 'full' ? '<span class="chip ' + hitCls(c.pct) + '">' + c.pct + '% hit</span>' : '') + '</div><div class="r3">' + (id === 'mgr' ? '<span>leg: none</span><span>scope ' + esc(A.scope) + '</span>' : '<span>leg ' + (A.leg + 1) + (S2.roster.filter(r => r.leg === A.leg).length > 1 ? ' (shared +' + (S2.roster.filter(r => r.leg === A.leg).length - 1) + ')' : '') + '</span><span>lease ' + esc(A.scope) + '</span>') + '</div>';
      if (head._h !== hh) { head._h = hh; head.innerHTML = hh; }
      $$('[data-tab]', el).forEach(b => b.setAttribute('aria-selected', String(b.dataset.tab === rec.tab)));
      let h = '';
      if (rec.tab === 'info') {
        const T = A.task && m.tasks[A.task], files = FILES_OF(S2, id), share = S2.roster.filter(r => r.leg === A.leg).length;
        h = '<div class="dstate"><b class="st-' + A.state + '">' + g + ' ' + w + '</b><span>' + esc(A.doing || '') + '</span></div>' + (id === 'mgr' ? '' : ui.thread(m, A)) + '<dl class="kv"><dt>role</dt><dd>' + (id === 'mgr' ? 'manager' : esc(A.role)) + '</dd><dt>model</dt><dd>' + esc(A.model) + '</dd>' + (T ? '<dt>task</dt><dd><span class="mono bright">' + esc(T.id) + '</span> ' + esc(T.title) + '</dd>' : '') + (id === 'mgr' ? '<dt>leg</dt><dd>none: the manager edits no file</dd>' : '<dt>leg</dt><dd>' + (A.leg + 1) + (share > 1 ? ' <span class="dim">(shared with ' + (share - 1) + ' more)</span>' : '') + '</dd>') + '<dt>scope and lease</dt><dd class="mono">' + esc(A.scope) + '</dd><dt>cost</dt><dd>' + fmtUsd(c.cost, 3) + ' <span class="dim">· ' + fmtK(c.prompt) + ' prompt tokens · ' + A.calls + ' tool calls</span></dd></dl>';
        h += '<h3 class="lab" style="margin:16px 0 6px">files owned</h3>' + (files.length ? files.map(d => { const st = d.st || 'M'; return '<div class="drfile"><span class="mono bright">' + esc(d.path) + '</span> <b style="color:' + (st === 'A' ? 'var(--ok)' : 'var(--warm)') + '">' + esc(st) + '</b> <span class="ok">+' + (d.add || '') + '</span>' + (d.del ? ' <span class="err">−' + d.del + '</span>' : '') + ' <button class="lnk" type="button" data-diff="' + esc(d.path) + '">open diff</button></div>'; }).join('') : '<p class="stubnote">' + (id === 'mgr' ? 'none: the manager edits no file (its refused edit is in the tool log)' : 'no file yet') + '</p>');
      } else if (rec.tab === 'log') {
        const list = (m.chan[id] || []).filter(e => !e.collapsed && (id === 'mgr' ? ['tool', 'ask', 'st', 'note'].includes(e.k) : e.k !== 'mail')).slice(-60);
        h = list.length ? list.map(e => SL.chat.rowHtml(e, S2, m)).join('') : '<p class="stubnote">' + esc(id) + ' has not run a tool yet.</p>';
      } else if (rec.tab === 'cache') {
        const st = ui.layerStack(m, id, 30, true), rr = A.ratios; h = '<h3 class="lab" style="margin:0 0 8px">latest prompt, by layer</h3>' + st.html + '<div class="layer-labels" style="margin:2px 0 0;padding:0">' + D.layers.map((l, i) => '<span style="width:' + (st.tot ? D.layerToks(id)[i] / st.tot * 100 : 100 / 6) + '%;--lc:' + l.col + '">' + l.id + '</span>').join('') + '</div><h3 class="lab" style="margin:18px 0 6px">hit ratio per request</h3><svg viewBox="0 0 200 40" style="width:100%;height:60px">' + ui.sparkBars(rr, 200, 40) + '</svg><dl class="kv" style="margin-top:12px"><dt>requests</dt><dd>' + rr.length + '</dd><dt>hit</dt><dd>' + c.pct + '% <span class="dim">= read / (read + uncached)</span></dd><dt>read / uncached</dt><dd>' + fmtN(c.read) + ' / ' + fmtN(c.un) + '</dd>' + (SL.settings.cache === 'full' ? '<dt>' + ui.savedWord(c) + '</dt><dd>$' + c.saved.toFixed(4) + ' <span class="dim">(est., list prices)</span></dd>' : '') + '</dl>' + m.anomalies.filter(x => x.id === id).map(e => '<div class="anom"><div class="a"><b>⚠ ' + esc(e.kind) + '</b><span>read ' + e.read + '/' + fmtK(e.expected) + '</span><em>' + esc(e.why) + '</em></div></div>').join('') + '<div style="margin-top:12px"><button class="btn" type="button" data-oc>Open in Cache</button></div>';
      } else {
        const ml = m.mail.filter(x => x.from === id || x.to === id).slice().reverse(); h = '<div class="databan"><span class="ic">✉</span><span>Mail is data, not instructions.</span></div>' + (ml.map(x => '<div class="mailbox"><small>' + (x.from === id ? 'sent to' : 'from') + ' <b style="color:' + agCol(x.from === id ? x.to : x.from) + '">' + esc(x.from === id ? x.to : x.from) + '</b> · ' + ui.todAt(S2, x) + '</small>' + esc(x.text) + '</div>').join('') || '<p class="stubnote">no mail to or from this agent</p>');
      }
      const sig = rec.tab + '|' + (rec.tab === 'info' ? h : h.length + '|' + (m.chan[id] || []).length + '|' + A.ratios.length); if (body._sig !== sig) { const top = body.scrollTop, near = body.scrollHeight - top - body.clientHeight < 40; body._sig = sig; body.innerHTML = h; body.scrollTop = near && rec.tab === 'log' ? body.scrollHeight : top; }
    }
    on(el, 'click', e => { const t = e.target; if (t.closest('[data-x]')) { ui.closeDrawer(); return; } const tb = t.closest('[data-tab]'); if (tb) { rec.tab = tb.dataset.tab; body._sig = ''; render(S, SL.sessions.active.m); return; } if (t.closest('[data-oc]')) { ui.cacheAgent = id; SL.views.show('cache'); return; } const df = t.closest('[data-diff]'); if (df) { ui.ws.open(df.dataset.diff, 'changes'); return; }
    });
    on(el, 'keydown', e => { if (e.key === 'Escape') { e.stopPropagation(); ui.closeDrawer(); } });
    rec.offs.push(sc.update((S2, m) => render(S2, m)));
    rec.offs.push(sc.frame((dt, vt) => { $$('.st[data-full],.mt[data-full]', body).forEach(t => { const [s, done] = ui.typed(t.dataset.full, +t.dataset.t0, +t.dataset.rate, vt); const x = s + (done ? '' : '▍'); if (t._t !== x) { t._t = x; t.textContent = x; } }); }));
    sc.onUnmount(() => { if (D_.cur === rec) { D_.cur = null; SL.link.select(null); } });
    render(S, S.m); body.scrollTop = body.scrollHeight; el.focus();
  };
})(SL);
