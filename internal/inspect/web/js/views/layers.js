// Layers: pick any request and see the seven-layer prompt stack as the provider
// billed it, what changed since the previous request, and (on demand) the layer
// text with the first differing byte of every rewritten layer.

import { h, mount, tok, pct, usd, clock, hideTip, int, revealIn } from '../lib.js';
import { S, nav, api, memo, mainRows, sideRows, agentById, defaultAgent, setFollow } from '../state.js';
import { TimelineChart } from '../charts.js';
import { LayerStack, verdict, layerTable, stackLegend } from '../layerstack.js';
import { tipFor, seg } from './timeline.js';

const REBASE = { commit: 'compaction commit', sync: 'shared-layer sync', epoch: 'shared epoch' };

export function create() {
  const el = h('div', { class: 'stack-v' });
  const st = { chart: null, stack: null, text: false, rep: null, repKey: '', open: new Set(), scrolled: '' };

  const agentSel = h('select', { 'aria-label': 'Agent', on: { change: e => nav({ agent: e.target.value, req: '' }) } });
  const textBox = h('input', { type: 'checkbox', on: { change: e => { st.text = e.target.checked; st.repKey = ''; update(); } } });
  const navBox = h('div', { class: 'chart' });
  const list = h('div', { class: 'reqlist', role: 'listbox', 'aria-label': 'Requests' });
  const detail = h('div', { class: 'stack-v' });

  el.append(
    h('div', { class: 'card' },
      h('div', { class: 'card-h' }, h('h2', null, 'Layers'), h('span', { class: 'sub' }, 'the prompt stack of one request, as billed · j / k step through requests')),
      h('div', { class: 'card-b stack-v' },
        h('div', { class: 'filters' }, h('label', { class: 'field' }, 'Agent', agentSel),
          h('label', { class: 'check' }, textBox, 'Show layer text and first differing byte')),
        navBox)),
    h('div', { class: 'split' }, list, detail));

  let idsKey = '';
  function curAgent() {
    const a = S.q.agent;
    if (a && agentById(a)) return a;
    const q = S.byId.get(S.q.req);
    if (q) return q.agent;
    return defaultAgent();
  }
  function selected(agent) {
    const rows = mainRows(agent);
    return (S.q.req && rows.find(r => r.id === S.q.req)) || rows[rows.length - 1] || null;
  }

  function step(dir) {
    const agent = curAgent(), rows = mainRows(agent), cur = selected(agent);
    const i = cur ? rows.findIndex(r => r.id === cur.id) : -1;
    const n = rows[Math.min(rows.length - 1, Math.max(0, i + dir))];
    if (n) { setFollow(false); nav({ agent, req: n.id }, { replace: true }); }
  }

  async function update() {
    hideTip();
    const agent = curAgent();
    const ids = S.agents.map(a => a.id).join('|');
    if (ids !== idsKey) {
      idsKey = ids;
      mount(agentSel, ...S.agents.map(a => h('option', { value: a.id }, a.id + (a.role ? ' · ' + a.role : ''))));
    }
    agentSel.value = agent;
    textBox.checked = st.text;
    const rows = mainRows(agent);
    const sel = selected(agent);

    // navigator: the same timeline, coloured by layer, used as a scrubber
    if (!st.chart) {
      st.chart = new TimelineChart(navBox, {
        compact: true,
        tooltip: (r, i) => tipFor(r, i > 0 ? st.chart.rows[i - 1] : null),
        onSelect: r => { setFollow(false); nav({ agent: r.agent, req: r.id }, { replace: true }); },
        onStep: step,
      });
    }
    st.chart.setData({ rows, commits: [], ticks: [], mode: 'req', color: 'layer', domain: null, selId: sel ? sel.id : null });

    // list, newest first
    const sorted = rows.slice().reverse().slice(0, 400);
    mount(list, ...sorted.map(r => h('button', { type: 'button', role: 'option', 'aria-current': String(sel && sel.id === r.id), on: { click: () => { setFollow(false); nav({ agent, req: r.id }, { replace: true }); } } },
      h('span', { class: 'a mono' }, r.id, r.anomaly ? h('span', { class: 'chip crit', css: { marginLeft: '6px' } }, '▲') : null, r.rebase ? h('span', { class: 'chip warn', css: { marginLeft: '6px' } }, r.rebase) : null),
      h('span', { class: 'b' }, tok(r.prompt) + ' tokens' + (r.changed && r.changed.length ? ' · ' + r.changed.join(' ') : '')),
      h('span', { class: 'c' }, pct(r.hit)))));
    if (!sorted.length) mount(list, h('div', { class: 'empty' }, 'No requests for this agent yet.'));
    const cur = list.querySelector('[aria-current="true"]');
    if (cur && st.scrolled !== (sel && sel.id)) { st.scrolled = sel && sel.id; revealIn(list, cur); }

    if (!sel) { mount(detail, h('div', { class: 'card' }, h('div', { class: 'empty' }, h('strong', null, 'No request selected'), 'Pick one from the list.'))); return; }
    const key = sel.id + '|' + st.text + '|' + (S.sum ? S.sum.rev : 0);
    if (st.repKey !== key) {
      try { st.rep = await memo('layers', 'layers', { req: sel.id, text: st.text ? '1' : '' }); st.repKey = key; } catch (e) { st.rep = null; }
    }
    const rep = st.rep;
    if (!rep || rep.req.id !== sel.id) return;
    render(rep, sel, rows);
  }

  function render(rep, sel, rows) {
    const prevRow = rep.prev ? S.byId.get(rep.prev) : null;
    const v = verdict(rep, prevRow);
    const i = rows.findIndex(r => r.id === sel.id);
    const stackHost = h('div');
    const focusKey = S.q.layer || null;
    const head = h('div', { class: 'card' },
      h('div', { class: 'card-h' }, h('h2', null, sel.id),
        h('span', { class: 'sub' }, `${sel.agent} · ${clock(sel.t)} · ${tok(rep.prompt)} tokens · read ${tok(rep.read)}, written ${tok(rep.write)}, uncached ${tok(rep.fresh)} · ${usd(sel.usd)}`),
        rep.rebase ? h('span', { class: 'chip warn' }, 'after ' + (REBASE[rep.rebase] || rep.rebase)) : null,
        sel.anomaly ? h('span', { class: 'chip crit' }, '▲ anomaly') : null,
        h('span', { class: 'grow' }),
        h('button', { class: 'btn', disabled: i <= 0, on: { click: () => step(-1) } }, '← prev'),
        h('button', { class: 'btn', disabled: i < 0 || i >= rows.length - 1, on: { click: () => step(1) } }, 'next →')),
      h('div', { class: 'card-b stack-v' },
        h('div', { class: 'callout ' + v.tone }, v.text),
        stackHost, stackLegend(),
        layerTable(rep, prevRow, k => { st.open.add(k); if (!st.text) { st.text = true; textBox.checked = true; st.repKey = ''; } nav({ layer: k }, { replace: true }); }, focusKey),
        rep.notes && rep.notes.length ? h('div', { class: 'note' }, ...rep.notes.map(n => h('p', null, n))) : null,
        rep.cache_key ? h('div', { class: 'note' }, 'routing key ', h('span', { class: 'mono' }, rep.cache_key), rep.key_changed ? h('b', null, ' (changed since the previous request)') : null,
          rep.prefix_key ? h('span', null, ' · shared prefix ', h('span', { class: 'mono' }, rep.prefix_key)) : null) : null));
    const parts = [head];
    if (rep.diffs && rep.diffs.length) parts.push(diffCard(rep));
    if (st.text) parts.push(textCard(rep, focusKey));
    mount(detail, ...parts);
    if (st.stack) st.stack.destroy();
    st.stack = new LayerStack(stackHost, { onLayer: k => { st.open.add(k); if (!st.text) { st.text = true; textBox.checked = true; st.repKey = ''; } nav({ layer: k }, { replace: true }); } });
    st.stack.set(rep, prevRow, focusKey);
  }

  function diffCard(rep) {
    return h('div', { class: 'card' },
      h('div', { class: 'card-h' }, h('h2', null, 'First differing byte'), h('span', { class: 'sub' }, 'where a rewritten layer stopped matching the previous request')),
      h('div', { class: 'card-b stack-v' }, ...rep.diffs.map(d => {
        const before = Array.from(d.before), after = Array.from(d.after), sp = Math.max(0, d.split || 0);
        const line = (cls, cs, mark) => h('div', { class: 'ln ' + cls }, cls === 'a' ? '− ' : '+ ', cs.slice(0, sp).join(''), cs.length > sp ? h('mark', null, cs[sp]) : null, cs.slice(sp + 1).join(''));
        return h('div', { class: 'diff' },
          h('div', { class: 'lh' }, h('b', null, d.layer), ` differs at byte ${int(d.at)} (previous ${int(d.prev_bytes)} bytes, now ${int(d.bytes)}). Everything from here on is a cache miss.`),
          line('a', before), line('b', after));
      })));
  }

  function textCard(rep, focus) {
    const items = rep.layers.filter(l => l.text);
    return h('div', { class: 'card' },
      h('div', { class: 'card-h' }, h('h2', null, 'Layer text'), h('span', { class: 'sub' }, 'first 8 KB of each layer; text comes from the session’s blob store')),
      h('div', { class: 'card-b stack-v' }, items.length ? items.map(l => {
        const d = h('details', { open: focus === l.key || st.open.has(l.key) }, h('summary', null, h('b', null, l.key), ' ' + l.title + ' · ' + int(l.bytes) + ' bytes' + (l.text_cut ? ' (cut)' : '')),
          h('pre', { class: 'text' }, l.text));
        d.addEventListener('toggle', () => { if (d.open) st.open.add(l.key); else st.open.delete(l.key); });
        return d;
      }) : h('div', { class: 'empty' }, h('strong', null, 'No layer text'), 'This session has no blobs directory, or the blobs for this request are missing.')));
  }

  return {
    el, update,
    destroy() { if (st.chart) st.chart.destroy(); if (st.stack) st.stack.destroy(); hideTip(); },
    key(e) {
      if (e.key === 'j' || e.key === 'ArrowDown' || e.key === 'ArrowRight') { step(1); return true; }
      if (e.key === 'k' || e.key === 'ArrowUp' || e.key === 'ArrowLeft') { step(-1); return true; }
      return false;
    },
  };
}
