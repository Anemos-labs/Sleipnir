// Anomalies: what the drift guard flagged, with the evidence the log holds and a
// plain-language explanation. The point of the view is to turn a silent cost
// regression into something a person can act on.

import { h, mount, tok, clock, dur, int } from '../lib.js';
import { S, nav, memo } from '../state.js';
import { seg } from './timeline.js';

const KIND_LABEL = { drift: 'prefix drift', low_hit: 'low cache read', undeclared: 'undeclared change' };

export function create() {
  const el = h('div', { class: 'stack-v' });
  const st = { data: null, kind: 'all' };
  const filter = seg([['all', 'All'], ['drift', 'Drift'], ['low_hit', 'Low read'], ['undeclared', 'Undeclared']], () => st.kind, v => { st.kind = v; render(); });
  const body = h('div', { class: 'stack-v' });
  el.append(h('div', { class: 'card' },
    h('div', { class: 'card-h' }, h('h2', null, 'Cache anomalies'),
      h('span', { class: 'sub' }, 'the guard compares every request with the agent’s previous one: an unexplained shrink of the shared prefix is drift; a provider read well under the expectation is a low read'),
      h('span', { class: 'grow' }), filter),
    h('div', { class: 'card-b stack-v' }, body)));

  async function update() {
    try { st.data = await memo('anoms', 'anomalies'); } catch { return; }
    filter.sync();
    render();
  }

  function render() {
    const d = st.data;
    if (!d) return;
    const list = d.anomalies.filter(a => st.kind === 'all' || a.kind === st.kind).slice().reverse();
    if (!list.length) {
      mount(body, d.anomalies.length === 0
        ? h('div', { class: 'empty' }, h('strong', null, '✓ No anomalies'),
          `The guard compared ${int(d.checked)} consecutive request${d.checked === 1 ? '' : 's'} and every prefix was either intact or changed at a declared rebase.`)
        : h('div', { class: 'empty' }, 'Nothing of this kind.'));
      return;
    }
    mount(body, ...list.map(card));
  }

  function card(a) {
    const fact = (k, v) => h('span', { class: 'chip' }, h('span', { class: 'muted' }, k), ' ', v);
    const link = a.req ? h('a', { href: '#/layers?agent=' + encodeURIComponent(a.agent) + '&req=' + encodeURIComponent(a.req) + (S.sid ? '&session=' + encodeURIComponent(S.sid) : '') }, a.req) : null;
    return h('article', { class: 'anom ' + (a.severity === 'error' ? 'error' : '') },
      h('h3', null,
        h('span', { class: 'chip ' + (a.severity === 'error' ? 'crit' : 'warn') }, (a.severity === 'error' ? '▲ error' : '▲ warning') + ' · ' + (KIND_LABEL[a.kind] || a.kind)),
        a.title),
      h('div', { class: 'row muted' }, a.agent, ' · at ', clock(a.t_ms), a.req ? [' · request ', link] : null),
      ...a.explain.map(t => h('p', null, t)),
      a.causes && a.causes.length ? h('div', null, h('b', null, 'Likely causes'), h('ul', { class: 'plain' }, ...a.causes.map(c => h('li', null, c)))) : null,
      h('div', { class: 'facts' },
        a.expected ? fact('expected read', tok(a.expected)) : null, a.actual != null && a.kind === 'low_hit' ? fact('actual read', tok(a.actual)) : null,
        a.diverged ? fact('diverged layer', a.diverged) : null, a.kind === 'drift' ? fact('shared blocks', String(a.shared_blocks || 0)) : null,
        a.gap_ms ? fact('gap since previous', dur(a.gap_ms)) : null, a.ttl_ms ? fact('cache lifetime', dur(a.ttl_ms)) : null,
        a.changed && a.changed.length ? fact('layers changed', a.changed.join(' ')) : null, a.rebase ? fact('declared rebase', a.rebase) : null,
        a.key_changed ? fact('routing key', 'changed') : null));
  }

  return { el, update, destroy() {} };
}
