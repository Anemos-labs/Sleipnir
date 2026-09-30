// Events: the raw log, tailed live. Payloads are shown as text and never
// interpreted; oversized ones arrive elided by the server.

import { h, mount, timeOfDay, int } from '../lib.js';
import { S, api } from '../state.js';

const MAX_ROWS = 1500;

export function create() {
  const el = h('div', { class: 'stack-v' });
  const st = { rows: [], last: 0, agent: '', type: '', text: '', follow: true, open: new Set(), busy: false, key: '' };
  const agentSel = h('select', { 'aria-label': 'Agent', on: { change: e => { st.agent = e.target.value; reset(); } } });
  const typeSel = h('select', { 'aria-label': 'Event type', on: { change: e => { st.type = e.target.value; reset(); } } });
  const search = h('input', { type: 'search', placeholder: 'filter text…', 'aria-label': 'Filter text', on: { input: e => { st.text = e.target.value.toLowerCase(); render(); } } });
  const follow = h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: true, on: { change: e => { st.follow = e.target.checked; if (st.follow) scroll(); } } }), 'Follow');
  const list = h('div', { class: 'evlist', role: 'log', 'aria-live': 'off' });
  const count = h('span', { class: 'sub' });
  el.append(h('div', { class: 'card' },
    h('div', { class: 'card-h' }, h('h2', null, 'Events'), count),
    h('div', { class: 'card-b stack-v' },
      h('div', { class: 'filters' }, h('label', { class: 'field' }, 'Agent', agentSel), h('label', { class: 'field' }, 'Type', typeSel), h('label', { class: 'field' }, 'Find', search), follow),
      list)));
  let sync = '';

  function reset() { st.rows = []; st.last = 0; st.open.clear(); update(); }

  async function update() {
    if (st.busy) return;
    st.busy = true;
    try {
      const agents = ['', ...S.agents.map(a => a.id)];
      const types = ['', ...Object.keys((S.sum && S.sum.log.types) || {}).sort()];
      const k = agents.join('|') + '#' + types.join('|');
      if (k !== sync) {
        sync = k;
        mount(agentSel, ...agents.map(a => h('option', { value: a }, a || 'All agents')));
        mount(typeSel, ...types.map(t => h('option', { value: t }, t || 'All types')));
        agentSel.value = st.agent; typeSel.value = st.type;
      }
      const params = { limit: 500, agent: st.agent, type: st.type };
      if (st.last) params.since = st.last;
      const page = await api('events', params);
      if (page.events.length) {
        st.rows.push(...page.events);
        if (st.rows.length > MAX_ROWS) st.rows.splice(0, st.rows.length - MAX_ROWS);
      }
      st.last = Math.max(st.last, page.next || 0);
      if (page.more) { st.busy = false; return update(); }
      render();
    } catch { /* the next poll retries */ } finally { st.busy = false; }
  }

  function preview(d) {
    if (d == null) return '';
    const t = JSON.stringify(d);
    return t.length > 240 ? t.slice(0, 240) + '…' : t;
  }

  function render() {
    const rows = st.text ? st.rows.filter(r => (r.type + ' ' + (r.agent || '') + ' ' + JSON.stringify(r.data || '')).toLowerCase().includes(st.text)) : st.rows;
    count.textContent = `${int(rows.length)} shown of ${int(st.rows.length)} loaded · ${S.sum ? int(S.sum.log.events) : 0} in the log`;
    const atBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 40;
    mount(list, ...rows.map(r => {
      const open = st.open.has(r.seq);
      const line = h('div', { class: 'ev', role: 'button', tabindex: 0, 'aria-expanded': String(open),
        on: { click: () => { if (open) st.open.delete(r.seq); else st.open.add(r.seq); render(); },
          keydown: e => { if (e.key === 'Enter') { e.preventDefault(); if (open) st.open.delete(r.seq); else st.open.add(r.seq); render(); } } } },
        h('span', { class: 'seq' }, r.seq), h('span', { class: 'muted num' }, timeOfDay(r.ts)), h('span', { class: 'evag ell' }, r.agent || '–'), h('span', { class: 'type mono ell' }, r.type),
        h('span', { class: 'data mono ell' }, preview(r.data)));
      if (open) line.appendChild(h('pre', { class: 'text' }, JSON.stringify(r.data === undefined ? null : r.data, null, 2)));
      return line;
    }));
    if (!rows.length) mount(list, h('div', { class: 'empty' }, 'No events match.'));
    if (st.follow && atBottom !== false) scroll();
  }

  function scroll() { list.scrollTop = list.scrollHeight; }

  return { el, update, destroy() {} };
}
