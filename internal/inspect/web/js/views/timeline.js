// Timeline: per-agent context size (stacked by cache class or by layer) with
// compaction commits and anomaly flags, the hit-ratio line beneath it, and the
// layer stack of the selected request below.

import { h, mount, clear, tok, usd, pct, clock, dur, int, hideTip } from '../lib.js';
import { S, nav, api, memo, mainRows, sideRows, agentById, defaultAgent, setFollow } from '../state.js';
import { TimelineChart } from '../charts.js';
import { LayerStack, verdict, stackLegend, stateLabel } from '../layerstack.js';

const MAX_SMALL = 12;
const REBASE = { commit: 'compaction commit', sync: 'shared-layer sync', epoch: 'shared epoch' };

export function tipFor(r, prev) {
  const rows = [h('div', { class: 'h' }, h('b', null, r.id), h('span', { class: 'muted' }, clock(r.t)))];
  const row = (cls, k, v) => rows.push(h('div', { class: 'r' }, h('span', { class: 's', css: { background: cls } }), h('span', { class: 'k' }, k), h('span', { class: 'v' }, v)));
  const dlt = prev ? r.prompt - prev.prompt : null;
  rows.push(h('div', { class: 'r' }, h('span', { class: 'k' }, 'context'), h('span', { class: 'v' }, tok(r.prompt), dlt != null && dlt !== 0 ? h('span', { class: 'muted' }, ' (' + (dlt > 0 ? '+' : '') + tok(dlt) + ')') : null)));
  row('var(--c-read)', 'read from cache', tok(r.read));
  row('var(--c-write)', 'written', tok(r.write));
  row('var(--c-fresh)', 'uncached', tok(r.in));
  rows.push(h('div', { class: 'r' }, h('span', { class: 'k' }, 'hit ratio'), h('span', { class: 'v' }, pct(r.hit))));
  rows.push(h('div', { class: 'r' }, h('span', { class: 'k' }, 'cost'), h('span', { class: 'v' }, usd(r.usd))));
  if (r.rebase) rows.push(h('div', { class: 'flag' }, 'After a ' + (REBASE[r.rebase] || r.rebase) + (r.changed && r.changed.length ? ': ' + r.changed.join(', ') + ' rewritten' : '')));
  if (r.anomaly) rows.push(h('div', { class: 'flag crit' }, 'Cache anomaly: read below the guard’s expectation'));
  else if (r.undeclared && !r.rebase) rows.push(h('div', { class: 'flag crit' }, 'Layers changed with no declared rebase'));
  if (r.cold && !r.first) rows.push(h('div', { class: 'flag' }, 'Cold start: nothing read from cache'));
  if (r.first) rows.push(h('div', { class: 'flag' }, 'First request of ' + r.agent));
  return h('div', null, ...rows);
}

export function create() {
  const el = h('div', { class: 'stack-v' });
  const state = { charts: new Map(), zoom: null, table: false, comps: null, compsRev: -1, layers: null, layersKey: '', stack: null, loadingLayers: false };
  let filtersKey = '';

  const agentSel = h('select', { 'aria-label': 'Agent', on: { change: e => { state.zoom = null; nav({ agent: e.target.value, req: '' }); } } });
  const xSeg = seg([['time', 'Time'], ['req', 'Request #']], () => xMode(), v => { state.zoom = null; nav({ x: v }, { replace: true }); });
  const cSeg = seg([['cache', 'Cache class'], ['layer', 'Layer']], () => cMode(), v => nav({ color: v }, { replace: true }));
  const zoomBtn = h('button', { class: 'btn', hidden: true, on: { click: () => { state.zoom = null; update(); } } }, 'Reset zoom');
  const tableBtn = h('button', { class: 'btn', 'aria-pressed': 'false', on: { click: () => { state.table = !state.table; update(); } } }, 'Table');
  const followBtn = h('button', { class: 'btn', 'aria-pressed': 'false', title: 'Keep the newest request selected (f)', on: { click: () => { setFollow(!S.follow); if (S.follow) selectLatest(); update(); } } }, 'Follow');
  const legendBox = h('div');
  const chartsBox = h('div', { class: 'stack-v' });
  const tableBox = h('div', { hidden: true });
  const selCard = h('div', { class: 'card' });

  const filters = h('div', { class: 'filters' },
    h('label', { class: 'field' }, 'Agent', agentSel), h('span', { class: 'field' }, 'X axis', xSeg), h('span', { class: 'field' }, 'Color by', cSeg),
    followBtn, tableBtn, zoomBtn);
  const card = h('div', { class: 'card' },
    h('div', { class: 'card-h' }, h('h2', null, 'Timeline'), h('span', { class: 'sub' }, 'context size per request, with the hit ratio beneath · drag to zoom, double-click to reset, click a request to inspect it')),
    h('div', { class: 'card-b stack-v' }, filters, legendBox, chartsBox, tableBox));
  el.append(card, selCard);

  function xMode() {
    if (S.q.x === 'time' || S.q.x === 'req') return S.q.x;
    const d = S.sum ? S.sum.session.duration_ms : 0;
    return d < 3000 ? 'req' : 'time';
  }
  function cMode() { return S.q.color === 'layer' ? 'layer' : 'cache'; }
  function curAgent() {
    const a = S.q.agent;
    if (a === 'all') return '';
    if (a && agentById(a)) return a;
    return S.agents.length > 1 ? '' : defaultAgent();
  }

  function selectLatest() {
    const agent = curAgent();
    const rows = mainRows(agent);
    const last = rows[rows.length - 1];
    if (last) nav({ agent: agent || 'all', req: last.id }, { replace: true });
  }

  function selected() {
    const id = S.q.req;
    if (id && S.byId.has(id)) return S.byId.get(id);
    const rows = mainRows(curAgent());
    return rows[rows.length - 1] || null;
  }

  function buildLegend() {
    const it = (cls, text, extra) => h('span', { class: 'it' }, h('span', { class: cls }), text);
    mount(legendBox, h('div', { class: 'legend' },
      cMode() === 'cache'
        ? [it('sw read', 'served from cache'), it('sw write', 'written to cache'), it('sw fresh', 'processed uncached')]
        : [0, 1, 2, 3, 4, 5, 6].map(i => it('sw g' + i, 'G' + i + ' ' + ['constitution + tools', 'shared pin', 'role pin', 'notes', 'spine', 'thread', 'hot tail'][i])),
      it('sw diamond', 'compaction commit'), it('sw tri', 'cache anomaly'),
      h('span', { class: 'it' }, h('span', { class: 'sw line', css: { background: 'var(--c-read)' } }), 'hit ratio')));
  }

  async function loadComps() {
    const rev = S.sum ? S.sum.rev : 0;
    if (state.compsRev === rev) return;
    try { state.comps = (await memo('comps', 'compactions')).compactions; state.compsRev = rev; } catch { /* keep old */ }
  }

  function chartInput(agent, domain) {
    const rows = mainRows(agent);
    const seqs = rows.map(r => r.seq);
    const before = seq => { let lo = 0, hi = seqs.length; while (lo < hi) { const m = (lo + hi) >> 1; if (seqs[m] < seq) lo = m + 1; else hi = m; } return lo; };
    const commits = [];
    for (const c of (state.comps || [])) {
      if (c.agent !== agent || c.status !== 'committed') continue;
      const idx = c.next ? rows.findIndex(r => r.id === c.next.req) : -1;
      commits.push({ t: c.commit_ms, idx: idx >= 0 ? idx - 0.5 : rows.length - 0.5, c });
    }
    const ticks = sideRows(agent).map(r => ({ t: r.t, idx: before(r.seq) - 0.5 }));
    return { rows, commits, ticks, domain };
  }

  function ensureCharts(agents, compact) {
    for (const [id, c] of state.charts) if (!agents.includes(id)) { c.chart.destroy(); c.box.remove(); state.charts.delete(id); }
    const order = [];
    for (const id of agents) {
      let c = state.charts.get(id);
      if (!c || c.compact !== compact) {
        if (c) { c.chart.destroy(); c.box.remove(); }
        const box = h('div', { class: 'chart' });
        const chart = new TimelineChart(box, {
          compact,
          tooltip: (r, i) => tipFor(r, i > 0 ? chart.rows[i - 1] : null),
          onSelect: r => { setFollow(false); nav({ agent: r.agent, req: r.id }, { replace: true }); },
          onZoom: d => { state.zoom = d; update(); },
          onStep: dir => stepRequest(dir),
        });
        c = { chart, box, compact };
        if (compact) box.appendChild(h('div', { class: 'agent-label' }, h('span', { class: 'dot ' + ((agentById(id) || {}).state || '') }), id, h('span', { class: 'muted' }, ' ' + ((agentById(id) || {}).role || ''))));
        state.charts.set(id, c);
      }
      order.push(c.box);
    }
    chartsBox.replaceChildren(...order);
  }

  function stepRequest(dir) {
    const agent = curAgent() || (selected() || {}).agent;
    const rows = mainRows(agent);
    const cur = selected();
    const i = cur ? rows.findIndex(r => r.id === cur.id) : -1;
    const n = rows[Math.min(rows.length - 1, Math.max(0, (i < 0 ? rows.length - 1 : i) + dir))];
    if (n) { setFollow(false); nav({ agent: agent || n.agent, req: n.id }, { replace: true }); }
  }

  async function update() {
    hideTip();
    const agent = curAgent();
    const ids = S.agents.map(a => a.id);
    // filters
    const fk = ids.join('|');
    if (fk !== filtersKey) {
      filtersKey = fk;
      const opts = (ids.length > 1 ? [h('option', { value: 'all' }, 'All agents (' + ids.length + ')')] : [])
        .concat(S.agents.map(a => h('option', { value: a.id }, a.id + (a.role ? ' · ' + a.role : ''))));
      mount(agentSel, ...opts);
    }
    agentSel.value = agent || 'all';
    xSeg.sync(); cSeg.sync();
    zoomBtn.hidden = !state.zoom;
    tableBtn.setAttribute('aria-pressed', String(state.table));
    followBtn.setAttribute('aria-pressed', String(S.follow));
    buildLegend();
    await loadComps();
    if (S.follow && S.sum && S.sum.state === 'live') {
      const rows = mainRows(agent), last = rows[rows.length - 1];
      if (last && S.q.req !== last.id) { S.q.req = last.id; nav({ agent: agent || 'all', req: last.id }, { replace: true }); return; }
    }
    const mode = xMode(), color = cMode();
    const sel = selected();

    // charts
    if (!S.reqs.length) {
      chartsBox.replaceChildren(h('div', { class: 'empty' }, h('strong', null, 'No requests yet'), 'This log has no model requests so far. It updates as the session writes.'));
      tableBox.hidden = true;
    } else if (state.table) {
      chartsBox.replaceChildren();
      tableBox.hidden = false;
      renderTable(agent);
    } else {
      tableBox.hidden = true;
      const compact = !agent;
      const shown = agent ? [agent] : ids.slice(0, MAX_SMALL);
      ensureCharts(shown, compact);
      let shared = null;
      if (!agent && mode === 'time') {
        let a = Infinity, b = -Infinity;
        for (const id of shown) for (const r of mainRows(id)) { a = Math.min(a, r.t); b = Math.max(b, r.t); }
        if (isFinite(a)) shared = [a - (b - a) * 0.01 - 1, b + (b - a) * 0.01 + 1];
      }
      for (const id of shown) {
        const inp = chartInput(id);
        state.charts.get(id).chart.setData({ ...inp, mode, color, domain: state.zoom || shared, selId: sel && sel.agent === id ? sel.id : null });
      }
      if (!agent && ids.length > MAX_SMALL) chartsBox.appendChild(h('div', { class: 'note' }, `Showing ${MAX_SMALL} of ${ids.length} agents. Pick one in the Agent menu to see all of its requests.`));
    }

    // selected request
    await renderSelected(sel);
  }

  function renderTable(agent) {
    const rows = S.reqs.filter(r => r.done && (!agent || r.agent === agent)).slice(-300).reverse();
    const cls = r => (r.anomaly ? 'crit' : r.rebase ? 'warn' : '');
    mount(tableBox, h('div', { class: 'tblwrap' }, h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, ['Request', 'Agent', 'Kind', 'Time', 'Context', 'Read', 'Written', 'Uncached', 'Hit', 'Cost', 'Notes'].map((t, i) => h('th', { class: i > 3 && i < 10 ? 'r' : '' }, t)))),
      h('tbody', null, ...rows.map(r => h('tr', { class: 'clickable' + (S.q.req === r.id ? ' sel' : ''), on: { click: () => { if (r.kind === 'main') { setFollow(false); nav({ agent: r.agent, req: r.id }, { replace: true }); } } } },
        h('td', { class: 'mono' }, r.id), h('td', null, r.agent), h('td', null, r.kind), h('td', { class: 'num' }, clock(r.t)),
        h('td', { class: 'r' }, tok(r.prompt)), h('td', { class: 'r' }, tok(r.read)), h('td', { class: 'r' }, tok(r.write)), h('td', { class: 'r' }, tok(r.in)),
        h('td', { class: 'r' }, pct(r.hit)), h('td', { class: 'r' }, usd(r.usd)),
        h('td', null, r.anomaly ? h('span', { class: 'chip crit' }, 'anomaly') : null, r.rebase ? h('span', { class: 'chip warn' }, r.rebase) : null, r.cold && !r.first && r.kind === 'main' ? h('span', { class: 'chip' }, 'cold') : null)))))),
      h('div', { class: 'note' }, 'Newest 300 requests. The same numbers the chart draws.'));
  }

  async function renderSelected(sel) {
    if (!sel) { mount(selCard, h('div', { class: 'empty' }, h('strong', null, 'Nothing selected'), 'Click a request in the chart to see its layers.')); return; }
    const key = sel.id + '|' + (S.sum ? S.sum.rev : 0);
    if (state.layersKey !== key) {
      try { state.layers = await memo('layers', 'layers', { req: sel.id }); state.layersKey = key; } catch { state.layers = null; }
    }
    const rep = state.layers;
    if (!rep || rep.req.id !== sel.id) return;
    const prevRow = rep.prev ? S.byId.get(rep.prev) : null;
    const v = verdict(rep, prevRow);
    const rows = mainRows(sel.agent);
    const i = rows.findIndex(r => r.id === sel.id);
    const nav_ = h('div', { class: 'reqnav' },
      h('button', { class: 'btn', disabled: i <= 0, title: 'Previous request (k or ←)', on: { click: () => stepRequest(-1) } }, '← prev'),
      h('button', { class: 'btn', disabled: i < 0 || i >= rows.length - 1, title: 'Next request (j or →)', on: { click: () => stepRequest(1) } }, 'next →'),
      h('a', { class: 'btn', href: '#/layers?agent=' + encodeURIComponent(sel.agent) + '&req=' + encodeURIComponent(sel.id) + (S.sid ? '&session=' + encodeURIComponent(S.sid) : ''), css: { display: 'inline-flex', alignItems: 'center' } }, 'Open in Layers'));
    if (!state.stack || state.stackHost !== selCard) {
      // built once; the SVG is redrawn when data or width changes
    }
    const stackHost = h('div');
    mount(selCard,
      h('div', { class: 'card-h' }, h('h2', null, 'Request ' + sel.id),
        h('span', { class: 'sub' }, `${sel.agent}${sel.role ? ' · ' + sel.role : ''} · at ${clock(sel.t)} · ${tok(sel.prompt)} tokens · ${pct(sel.hit)} from cache`),
        rep.rebase ? h('span', { class: 'chip warn' }, 'after ' + (REBASE[rep.rebase] || rep.rebase)) : null,
        sel.anomaly ? h('span', { class: 'chip crit' }, '▲ anomaly') : null,
        h('span', { class: 'grow' }), nav_),
      h('div', { class: 'card-b stack-v' },
        h('div', { class: 'callout ' + v.tone }, v.text),
        stackHost, stackLegend()));
    if (state.stack) state.stack.destroy();
    state.stack = new LayerStack(stackHost, { onLayer: k => nav({ view: 'layers', layer: k }) });
    state.stack.set(rep, prevRow, null);
  }

  return {
    el, update,
    destroy() { for (const c of state.charts.values()) c.chart.destroy(); if (state.stack) state.stack.destroy(); hideTip(); },
    key(e) {
      if (e.key === 'j' || e.key === 'ArrowRight') { stepRequest(1); return true; }
      if (e.key === 'k' || e.key === 'ArrowLeft') { stepRequest(-1); return true; }
      if (e.key === 'Home' || e.key === 'End') {
        const rows = mainRows(curAgent()); const r = e.key === 'Home' ? rows[0] : rows[rows.length - 1];
        if (r) { setFollow(false); nav({ agent: curAgent() || r.agent, req: r.id }, { replace: true }); return true; }
      }
      return false;
    },
  };
}

/** A segmented control: options [[value,label]], get() reads the current value. */
export function seg(options, get, set) {
  const box = h('span', { class: 'seg', role: 'group' });
  const btns = options.map(([v, label]) => h('button', { type: 'button', 'aria-pressed': 'false', on: { click: () => set(v) } }, label));
  box.append(...btns);
  box.sync = () => btns.forEach((b, i) => b.setAttribute('aria-pressed', String(options[i][0] === get())));
  return box;
}
