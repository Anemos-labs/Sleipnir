// Sessions: shown when the inspector was pointed at a directory of sessions (or
// `sleipnir rl rollout` output). Each row opens one session.

import { h, mount, usd, pct, int, dur, ago, tok } from '../lib.js';
import { S, api, nav, resetSession } from '../state.js';

export function create() {
  const el = h('div', { class: 'stack-v' });
  const box = h('div');
  const filter = h('input', { type: 'search', placeholder: 'filter sessions…', 'aria-label': 'Filter sessions', on: { input: () => render() } });
  el.append(h('div', { class: 'card' },
    h('div', { class: 'card-h' }, h('h2', null, 'Sessions'), h('span', { class: 'sub', id: 'sesssub' }), h('span', { class: 'grow' }), h('label', { class: 'field' }, filter)),
    h('div', { class: 'card-b' }, box)));
  let list = null;

  async function update() {
    try { list = await api('sessions'); } catch { return; }
    S.mode = list.mode; S.sessions = list.sessions; S.root = list.root;
    render();
  }

  function open(id) {
    resetSession();
    S.sid = id === '.' ? '' : id;
    nav({ view: 'timeline', agent: '', req: '', layer: '' }, { merge: false });
  }

  function render() {
    if (!list) return;
    const q = filter.value.trim().toLowerCase();
    const rows = list.sessions.filter(s => !q || (s.name + ' ' + ((s.digest && s.digest.goal) || '') + ' ' + ((s.episode && s.episode.task_id) || '')).toLowerCase().includes(q));
    el.querySelector('#sesssub').textContent = `${list.sessions.length} under ${list.root}`;
    if (!rows.length) { mount(box, h('div', { class: 'empty' }, h('strong', null, 'No sessions'), 'Nothing matches.')); return; }
    const hasEp = rows.some(s => s.episode);
    mount(box, h('div', { class: 'tblwrap' }, h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, h('th', null, 'Session'), h('th', null, 'Model'), h('th', { class: 'r' }, 'Requests'), h('th', { class: 'r' }, 'Agents'), h('th', { class: 'r' }, 'Hit ratio'),
        h('th', { class: 'r' }, 'Cost'), h('th', { class: 'r' }, 'Saved'), h('th', { class: 'r' }, 'Commits'), h('th', { class: 'r' }, '▲'), hasEp ? h('th', null, 'Outcome') : null, h('th', null, 'Updated'))),
      h('tbody', null, ...rows.map(s => {
        const d = s.digest, e = s.episode;
        return h('tr', { class: 'clickable', tabindex: 0, on: { click: () => open(s.id), keydown: ev => { if (ev.key === 'Enter') open(s.id); } } },
          h('td', null, h('div', { class: 'row' }, h('span', { class: 'dot ' + (s.live ? 'live' : 'ended') }), h('b', null, s.name)), d && d.goal ? h('div', { class: 'sub ell', css: { maxWidth: '360px' } }, d.goal) : null,
            e && e.task_id ? h('div', { class: 'sub' }, 'task ' + e.task_id + (e.sample ? ' · sample ' + e.sample : '')) : null),
          h('td', { class: 'mono' }, d ? d.model || '' : ''), h('td', { class: 'r' }, d ? int(d.requests) : '…'), h('td', { class: 'r' }, d ? int(d.agents) : ''),
          h('td', { class: 'r' }, d ? pct(d.hit_ratio) : ''), h('td', { class: 'r' }, d ? usd(d.cost_usd) : ''), h('td', { class: 'r' }, d ? pct(d.saved_pct) : ''),
          h('td', { class: 'r' }, d ? int(d.commits) : ''), h('td', { class: 'r' }, d && d.anomalies ? h('span', { class: 'chip crit' }, d.anomalies) : d ? '0' : ''),
          hasEp ? h('td', null, e ? [e.pass == null ? null : h('span', { class: 'chip ' + (e.pass ? 'good' : 'crit') }, e.pass ? '✓ pass' : '✗ fail'), ' ', h('span', { class: 'muted' }, 'reward ' + e.reward.toFixed(2)), (e.flags || []).length ? h('span', { class: 'chip warn' }, e.flags[0]) : null] : '') : null,
          h('td', { class: 'muted nowrap' }, ago(s.updated)));
      })))));
  }

  return { el, update, destroy() {} };
}
