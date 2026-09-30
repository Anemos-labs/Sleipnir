// Compaction: generational collection of the thread. One row per episode, from
// the planner's decision to the commit, with what it folded, what it cost and what
// the next request paid for the rebase.

import { h, mount, tok, usd, pct, clock, dur, int } from '../lib.js';
import { S, nav, memo } from '../state.js';

const MODE_HELP = {
  fork: 'A model call that is a fork of the agent’s own request wrote the patch.',
  mask: 'A cold cache: bulky old tool results were masked deterministically, with no model call.',
  emergency: 'The prompt neared the window: a mechanical compaction ran at once.',
};

export function create() {
  const el = h('div', { class: 'stack-v' });
  const st = { data: null, open: new Set(), agent: '' };
  const tiles = h('div', { class: 'grid g6' });
  const agentSel = h('select', { 'aria-label': 'Agent', on: { change: e => { st.agent = e.target.value; render(); } } });
  const box = h('div');
  el.append(tiles, h('div', { class: 'card' },
    h('div', { class: 'card-h' }, h('h2', null, 'Compactions'), h('span', { class: 'sub' }, 'newest first · click a row for the planner’s decision trail'),
      h('span', { class: 'grow' }), h('label', { class: 'field' }, 'Agent', agentSel)),
    h('div', { class: 'card-b' }, box)));
  let idsKey = '';

  async function update() {
    try { st.data = await memo('comps', 'compactions'); } catch { return; }
    const ids = S.agents.map(a => a.id).join('|');
    if (ids !== idsKey) {
      idsKey = ids;
      mount(agentSel, h('option', { value: '' }, 'All agents'), ...S.agents.map(a => h('option', { value: a.id }, a.id)));
      agentSel.value = st.agent;
    }
    render();
  }

  function tile(label, value, sub, cls) {
    return h('div', { class: 'card' }, h('div', { class: 'card-b' }, h('div', { class: 'ink2' }, label), h('div', { class: 'val', css: { fontSize: '24px', fontWeight: '600' } }, value), h('div', { class: 'note ' + (cls || '') }, sub)));
  }

  function render() {
    const d = st.data;
    if (!d) return;
    const t = d.totals;
    const all = d.compactions;
    const committed = all.filter(c => c.status === 'committed');
    const rewrites = committed.filter(c => c.next).map(c => c.next.rewrite);
    const avgRewrite = rewrites.length ? rewrites.reduce((a, b) => a + b, 0) / rewrites.length : 0;
    mount(tiles,
      tile('Commits', int(t.commits), `${t.fork} fork · ${t.mask} mask · ${t.emergency} emergency`),
      tile('Tokens folded', tok(t.net), `${tok(t.folded)} removed, ${tok(t.spine_added)} added as spine lines`),
      tile('Rewrite per commit', tok(avgRewrite), 'tokens the next request processed again (average)'),
      tile('Compactor calls', usd(t.compactor_usd), S.sum && S.sum.cost.actual.total > 0 ? pct(t.compactor_usd / S.sum.cost.actual.total) + ' of the priced bill' : ''),
      tile('Mechanical fallbacks', int(t.fallbacks), `${t.rejects} rejection${t.rejects === 1 ? '' : 's'} · ${t.held} “not yet” decisions`, t.fallbacks ? '' : ''),
      tile('Declared rebases', int(t.rebases), 'commits and shared-layer syncs: the only times a prefix may change'));

    const rows = all.filter(c => !st.agent || c.agent === st.agent).slice().reverse();
    if (!rows.length) {
      mount(box, h('div', { class: 'empty' }, h('strong', null, 'No compaction yet'),
        'The thread has stayed under the planner’s soft limit, so nothing was folded. Commits appear here as they happen.'));
      return;
    }
    const body = [];
    for (const c of rows.slice(0, 300)) {
      const open = st.open.has(c.n);
      body.push(h('tr', { class: 'clickable', on: { click: () => { if (open) st.open.delete(c.n); else st.open.add(c.n); render(); } }, 'aria-expanded': String(open) },
        h('td', { class: 'r muted' }, c.n),
        h('td', { class: 'nowrap' }, c.status === 'committed' ? clock(c.commit_ms) : clock(c.start_ms), h('div', { class: 'sub' }, c.agent)),
        h('td', null, modeBadge(c), c.patch === 'mechanical' && c.mode === 'fork' ? h('span', { class: 'chip warn', title: c.fallback_reason || 'the model’s patch was unusable' }, 'mechanical fallback') : null,
          c.status !== 'committed' ? h('span', { class: 'chip ' + (c.status === 'failed' ? 'crit' : '') }, c.status) : null),
        h('td', { css: { maxWidth: '300px' } }, h('div', { class: 'ell', title: c.trigger }, c.trigger || '–'), c.reason && c.reason !== c.trigger ? h('div', { class: 'sub ell', title: c.reason }, 'decision: ' + c.reason) : null),
        h('td', null, c.warm == null ? h('span', { class: 'muted' }, '–') : h('span', { class: 'chip ' + (c.warm ? '' : 'accent') }, c.warm ? 'warm' : 'cold')),
        h('td', { css: { minWidth: '170px' } }, sizeBar(c)),
        h('td', { class: 'r' }, tok(c.net), h('div', { class: 'sub' }, c.prompt_before && c.next ? tok(c.prompt_before) + ' → ' + tok(c.next.prompt) : '')),
        h('td', { class: 'r' }, usd(c.compactor_usd), h('div', { class: 'sub' }, c.next ? tok(c.next.rewrite) + ' rewritten' : '')),
        h('td', { class: 'r' }, c.next ? pct(c.next.hit) : '–', h('div', { class: 'sub' }, c.next ? c.next.req : ''))));
      if (open) body.push(h('tr', null, h('td', { colspan: 10 }, detail(c))));
    }
    mount(box, h('div', { class: 'tblwrap' }, h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, [['#', 'r'], ['When', ''], ['Mode', ''], ['Why', ''], ['Cache', ''], ['Folded → spine, kept', ''], ['Net saved', 'r'], ['Cost', 'r'], ['Next hit', 'r']].map(([t, c]) => h('th', { class: c }, t)), h('th', null, ''))),
      h('tbody', null, ...body))),
      rows.length > 300 ? h('div', { class: 'note' }, `Showing the newest 300 of ${rows.length}.`) : null);
  }

  function modeBadge(c) {
    return h('span', { class: 'chip ' + (c.mode === 'emergency' ? 'crit' : c.mode === 'mask' ? 'accent' : ''), title: MODE_HELP[c.mode] || '' }, c.mode);
  }

  /** removed | retained | spine, drawn to the same scale within a row. */
  function sizeBar(c) {
    const total = Math.max(c.removed + c.retained + c.spine, 1);
    const seg = (cls, n, label) => n > 0 ? h('i', { class: cls, css: { flexGrow: String(n / total), flexBasis: '0' }, title: label + ' ' + tok(n) }) : null;
    return h('div', null,
      h('div', { class: 'bar tall prompt', role: 'img', 'aria-label': `folded ${c.removed}, kept ${c.retained}, spine ${c.spine} tokens` }, seg('a', c.removed, 'folded'), seg('b', c.retained, 'kept verbatim'), seg('c', c.spine, 'spine')),
      h('div', { class: 'sub' }, `${tok(c.removed)} folded → ${tok(c.spine)} spine · ${tok(c.retained)} kept`));
  }

  function detail(c) {
    const t0 = new Date(c.start).getTime();
    return h('div', { class: 'stack-v', css: { padding: '4px 4px 10px' } },
      h('div', { class: 'row' },
        chipKV('planner net', (c.net_ite >= 0 ? '+' : '') + int(c.net_ite) + ' ITE'), chipKV('held', dur(c.held_ms)), chipKV('patch ready after', dur(c.propose_ms)),
        chipKV('compactor calls', String(c.compactor_calls)), chipKV('masked results', String(c.masked)), chipKV('mechanical lines', String(c.mech_lines)),
        chipKV('notes', c.notes_changed ? 'changed (major commit)' : 'unchanged'), c.holds ? chipKV('“not yet”', String(c.holds)) : null),
      h('ol', { class: 'plain', css: { margin: '0', paddingLeft: '18px' } }, ...c.steps.map(s => h('li', null, h('span', { class: 'muted num' }, '+' + dur(new Date(s.t).getTime() - t0) + ' '), h('span', { class: 'chip' }, s.kind), ' ' + s.text))),
      c.fallback_reason ? h('div', { class: 'note' }, h('b', null, 'Why the model patch was replaced: '), c.fallback_reason) : null,
      c.rejects && c.rejects.length ? h('div', { class: 'note' }, h('b', null, 'Rejections: '), c.rejects.join('; ')) : null,
      c.warnings && c.warnings.length ? h('div', { class: 'note' }, h('b', null, 'Warnings: '), c.warnings.join('; ')) : null,
      h('div', { class: 'note' }, MODE_HELP[c.mode] || ''));
  }

  function chipKV(k, v) { return h('span', { class: 'chip' }, h('span', { class: 'muted' }, k), ' ', v); }

  return { el, update, destroy() {} };
}
