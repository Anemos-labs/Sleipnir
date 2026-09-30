// Cost: what was paid, against two counterfactuals. Every assumption is written
// out next to the numbers; the "naive" bill is an estimate and says so.

import { h, mount, tok, usd, pct, int, clock } from '../lib.js';
import { S, nav } from '../state.js';
import { LineChart } from '../charts.js';

export function create() {
  const el = h('div', { class: 'stack-v' });
  const scen = h('div'), race = h('div', { class: 'chart' }), agents = h('div'), assume = h('div'), prices = h('div'), recon = h('div');
  let chart = null;
  el.append(
    card('Actual against two counterfactuals', 'same requests, same prices; only the caching differs', scen),
    card('Cumulative cost', 'how the gap opens as the session runs', race),
    h('div', { class: 'grid g2' }, card('Where the money went, by agent', null, agents), card('Prices and reconciliation', null, prices)),
    card('Assumptions', 'written out, because two of these numbers are not in the log', assume));

  function card(title, sub, body) {
    return h('div', { class: 'card' }, h('div', { class: 'card-h' }, h('h2', null, title), sub ? h('span', { class: 'sub' }, sub) : null), h('div', { class: 'card-b' }, body));
  }

  function update() {
    const sum = S.sum;
    if (!sum) return;
    const c = sum.cost;
    renderScenarios(c);
    renderRace(sum);
    renderAgents();
    renderPrices(c, sum);
    mount(assume, h('ul', { class: 'plain note' }, ...c.assumptions.map(a => h('li', null, a))));
  }

  function classBar(m, max, est) {
    const seg = (cls, v, label) => v > 0 ? h('i', { class: cls, css: { flexGrow: String(v / max), flexBasis: '0' }, title: label + ' ' + usd(v) }) : null;
    return h('div', { class: 'bar', role: 'img', 'aria-label': `uncached ${usd(m.uncached)}, cache reads ${usd(m.read)}, cache writes ${usd(m.write)}, output ${usd(m.output)}` },
      seg('fresh', m.uncached, 'uncached input'), seg('read', m.read, 'cache reads'), seg('write', m.write, 'cache writes'), seg('out', m.output, 'output'));
  }

  function renderScenarios(c) {
    const max = Math.max(c.no_cache.total, c.naive.total, c.actual.total, 1e-9);
    const sav = (saved, p, vs) => {
      if (Math.abs(saved) < 1e-9) return h('div', { class: 'sav' }, 'equal to ' + vs);
      return h('div', { class: 'sav ' + (saved > 0 ? 'good' : 'crit') }, saved > 0 ? `▼ ${pct(Math.abs(p))} below ${vs}` : `▲ ${pct(Math.abs(p))} above ${vs}`);
    };
    const row = (name, sub, m, extra, est) => h('div', { class: 'costrow' + (est ? ' est' : '') },
      h('div', { class: 'name' }, name, h('small', null, sub)), classBar(m, max, est),
      h('div', { class: 'end' }, h('b', null, usd(m.total)), extra));
    mount(scen,
      row('No cache', 'every prompt token at the plain input price', c.no_cache, null),
      row('Naive whole history', 'ESTIMATE · hatched', c.naive, c.no_cache.total > 0 ? sav(c.no_cache.total - c.naive.total, (c.no_cache.total - c.naive.total) / c.no_cache.total, 'no cache') : null, true),
      row('Actual', 'usage × table prices', c.actual, h('div', null, sav(c.saved_no_cache, c.saved_no_cache_pct, 'no cache'), sav(c.saved_naive, c.saved_naive_pct, 'naive (est.)'))),
      h('div', { class: 'legend', css: { marginTop: '10px' } },
        h('span', { class: 'it' }, h('span', { class: 'sw fresh' }), 'uncached input'), h('span', { class: 'it' }, h('span', { class: 'sw read' }), 'cache reads'),
        h('span', { class: 'it' }, h('span', { class: 'sw write' }), 'cache writes'), h('span', { class: 'it' }, h('span', { class: 'sw out' }), 'output tokens')),
      h('div', { class: 'note' }, h('p', null, 'Bars share one scale. Caching turns orange (full price) into aqua (a tenth of it); the violet is the premium paid to write entries that later requests read.')));
  }

  function renderRace(sum) {
    const pts = sum.series.points;
    if (!pts.length) { mount(race, h('div', { class: 'empty' }, 'No completed requests yet.')); if (chart) { chart.destroy(); chart = null; } return; }
    if (!chart) { race.replaceChildren(); chart = new LineChart(race, { height: 250, label: 'Cumulative cost: actual, no cache, naive estimate', yLabel: v => usd(v), xLabel: v => v >= 0 ? (sum.series.group > 1 ? '#' + Math.round(v * sum.series.group) : '#' + Math.round(v + 1)) : '' }); }
    const cum = key => { let a = 0; return pts.map(p => (a += p[key])); };
    const xs = pts.map((p, i) => i);
    const actual = cum('priced'), nocache = cum('no_cache'), naive = cum('naive');
    chart.opts.tip = i => {
      const row = (k, v, sw) => h('div', { class: 'r' }, h('span', { class: 's', css: { background: sw } }), h('span', { class: 'k' }, k), h('span', { class: 'v' }, usd(v)));
      return h('div', null, h('div', { class: 'h' }, h('b', null, 'after request ' + int((pts[i].i + pts[i].n))), h('span', { class: 'muted' }, clock(pts[i].t))),
        row('no cache', nocache[i], 'var(--muted)'), row('naive (est.)', naive[i], 'var(--muted)'), row('actual', actual[i], 'var(--ink)'));
    };
    chart.setData(xs, [
      { name: 'actual', short: 'actual ' + usd(actual[actual.length - 1]), values: actual, color: P => P.ink, width: 2.5 },
      { name: 'naive', short: 'naive (est.) ' + usd(naive[naive.length - 1]), values: naive, color: P => P.muted, width: 2, dash: [6, 4] },
      { name: 'no cache', short: 'no cache ' + usd(nocache[nocache.length - 1]), values: nocache, color: P => P.muted, width: 2 },
    ]);
    if (!race.querySelector('.legend')) race.appendChild(h('div', { class: 'legend', css: { marginTop: '8px' } },
      h('span', { class: 'it' }, h('span', { class: 'sw line', css: { background: 'var(--ink)' } }), 'actual'),
      h('span', { class: 'it' }, h('span', { class: 'sw line', css: { background: 'var(--muted)' } }), 'no cache'),
      h('span', { class: 'it' }, h('span', { class: 'sw line', css: { background: 'repeating-linear-gradient(90deg, var(--muted) 0 6px, transparent 6px 10px)' } }), 'naive whole history (estimate)')));
  }

  function renderAgents() {
    const list = S.agents.slice().sort((a, b) => b.cost_usd - a.cost_usd);
    const max = Math.max(...list.map(a => a.cost_usd), 1e-9);
    const tot = list.reduce((a, b) => a + b.cost_usd, 0);
    if (!list.length) { mount(agents, h('div', { class: 'empty' }, 'No agents yet.')); return; }
    mount(agents, h('div', { class: 'tblwrap' }, h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, h('th', null, 'Agent'), h('th', { css: { width: '38%' } }, 'Share'), h('th', { class: 'r' }, 'Cost'), h('th', { class: 'r' }, 'Hit'), h('th', { class: 'r' }, 'Requests'))),
      h('tbody', null, ...list.map(a => h('tr', { class: 'clickable', on: { click: () => nav({ view: 'timeline', agent: a.id, req: '' }) } },
        h('td', null, h('b', null, a.id), ' ', h('span', { class: 'muted' }, a.role || '')),
        h('td', null, h('div', { class: 'bar tall' }, h('i', { class: 'fill', css: { flexGrow: String(a.cost_usd / max), flexBasis: '0', background: 'var(--accent)' } }), h('i', { css: { flexGrow: String(1 - a.cost_usd / max), flexBasis: '0', background: 'transparent', minWidth: '0' } }))),
        h('td', { class: 'r' }, usd(a.cost_usd), h('div', { class: 'sub' }, tot > 0 ? pct(a.cost_usd / tot) : '')), h('td', { class: 'r' }, a.main ? pct(a.hit_ratio) : '–'), h('td', { class: 'r' }, int(a.requests))))))));
  }

  function renderPrices(c, sum) {
    const div = c.divergence;
    mount(prices,
      h('div', { class: 'tblwrap' }, h('table', { class: 'tbl' },
        h('thead', null, h('tr', null, h('th', null, 'Model'), h('th', { class: 'r' }, 'Input'), h('th', { class: 'r' }, 'Read'), h('th', { class: 'r' }, 'Write'), h('th', { class: 'r' }, 'Output'))),
        h('tbody', null, ...c.prices.map(p => h('tr', null,
          h('td', null, h('div', { class: 'mono' }, p.model), h('div', { class: 'sub' }, p.source === 'table' ? 'price table' : h('span', { class: 'chip warn' }, 'fallback price'))),
          h('td', { class: 'r' }, usd(p.input_per_m)), h('td', { class: 'r' }, usd(p.read_per_m)),
          h('td', { class: 'r' }, p.explicit ? usd(p.write5m_per_m) : 'none'), h('td', { class: 'r' }, usd(p.output_per_m)))))),
        h('div', { class: 'note' }, 'US dollars per million tokens. Cache lifetime assumed ' + (c.prices[0] ? Math.round(c.prices[0].ttl_s / 60) + ' minutes' : '') + '.')),
      h('hr', { class: 'hr' }),
      h('div', { class: 'stack-v' },
        kv('Reported by the log', usd(c.reported), `${c.reported_requests} responses carried cost_usd, ${c.gateway_requests} from the gateway`),
        kv('Priced from usage', usd(c.actual.total), 'what the three bars above use'),
        Math.abs(div) > 0.005 && c.reported_requests ? kv('Difference', (div >= 0 ? '+' : '') + pct(div), 'the gateway bills at prices other than the table’s') : kv('Difference', 'none', 'the log’s costs match the table'),
        kv('Compactor calls', usd(c.compactor_usd), 'included in every bill above')));
  }

  function kv(k, v, sub) { return h('div', { class: 'row' }, h('span', { class: 'ink2', css: { minWidth: '150px' } }, k), h('b', null, v), h('span', { class: 'muted' }, sub)); }

  return { el, update, destroy() { if (chart) chart.destroy(); } };
}
