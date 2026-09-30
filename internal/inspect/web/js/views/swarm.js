// Swarm: who is doing what, the task board, and the coordination traffic
// (board operations, mail, spawns, leases) next to what the request stream says
// about the governor.

import { h, mount, tok, usd, pct, int, dur, timeOfDay, clock, ago } from '../lib.js';
import { S, nav, memo } from '../state.js';
import { sparkline, bars } from '../charts.js';

const STATUS_ORDER = ['todo', 'doing', 'blocked', 'review', 'done', 'failed'];
const STATE_LABEL = { running: 'running', waiting: 'waiting', idle: 'idle', done: 'done', failed: 'failed' };

export function create() {
  const el = h('div', { class: 'stack-v' });
  const agentsBox = h('div'), boardBox = h('div'), activityBox = h('div'), govBox = h('div'), mailBox = h('div'), spawnBox = h('div'), leaseBox = h('div');
  el.append(
    card('Agents', 'click one to open its timeline', agentsBox),
    card('Task board', null, boardBox, 'boardnote'),
    h('div', { class: 'grid g2' }, card('Coordination per minute', 'each bar is one minute of the session', activityBox), card('Governor and concurrency', 'what the request stream shows of admission control', govBox)),
    h('div', { class: 'grid g2' }, card('Mail', 'newest first', mailBox), h('div', { class: 'stack-v' }, card('Spawns', null, spawnBox), card('Leases', null, leaseBox))));
  const noteEl = el.querySelector('.boardnote');

  function card(title, sub, body, cls) {
    return h('div', { class: 'card' }, h('div', { class: 'card-h' }, h('h2', null, title), sub ? h('span', { class: 'sub' }, sub) : null, cls ? h('span', { class: 'sub ' + cls }) : null), h('div', { class: 'card-b' }, body));
  }

  async function update() {
    let d;
    try { d = await memo('swarm', 'swarm'); } catch { return; }
    renderAgents(d);
    renderBoard(d);
    renderActivity(d);
    renderGov(d);
    renderMail(d);
    renderSpawns(d);
    renderLeases(d);
  }

  function renderAgents(d) {
    const rank = { running: 0, waiting: 1, idle: 2, done: 3, failed: 4 };
    const agents = d.agents.slice().sort((a, b) => (a.role === 'manager' ? -1 : 0) - (b.role === 'manager' ? -1 : 0) || (rank[a.state] - rank[b.state]) || a.id.localeCompare(b.id));
    if (!agents.length) { mount(agentsBox, h('div', { class: 'empty' }, 'No agents yet.')); return; }
    mount(agentsBox, h('div', { class: 'agents' }, ...agents.map(a =>
      h('button', { class: 'agent', type: 'button', on: { click: () => nav({ view: 'timeline', agent: a.id, req: '' }) } },
        h('div', { class: 'h' }, h('span', { class: 'dot ' + a.state, title: STATE_LABEL[a.state] || a.state }), h('b', { class: 'ell' }, a.id), h('span', { class: 'chip' }, a.role || 'agent'),
          h('span', { class: 'grow' }), h('span', { class: 'muted' }, STATE_LABEL[a.state] || a.state)),
        h('div', { class: 'line ell', title: a.line || a.evidence || '' }, a.line || (a.task ? 'task ' + a.task : '') || h('span', { class: 'muted' }, ' ')),
        a.spark_ctx && a.spark_ctx.length > 1 ? sparkline(a.spark_ctx, { w: 220, hgt: 26, title: 'context size' }) : h('div', { css: { height: '26px' } }),
        h('div', { class: 'm' },
          h('div', null, h('span', null, 'context'), tok(a.context)), h('div', null, h('span', null, 'hit ratio'), a.main ? pct(a.hit_ratio) : '–'), h('div', null, h('span', null, 'cost'), usd(a.cost_usd)),
          h('div', null, h('span', null, 'requests'), int(a.requests)), h('div', null, h('span', null, 'tool calls'), int(a.tool_calls) + (a.tool_errors ? ' (' + a.tool_errors + ' err)' : '')),
          h('div', null, h('span', null, 'compactions'), int(a.compactions) + (a.anomalies ? ' · ▲' + a.anomalies : '')))))));
  }

  function renderBoard(d) {
    if (noteEl) noteEl.textContent = '';
    if (!d.tasks.length) {
      mount(boardBox, h('div', { class: 'empty' }, h('strong', null, 'No board activity'), 'This log has no task or spawn calls. ' + (d.tasks_source || '')));
      return;
    }
    const by = {};
    for (const t of d.tasks) (by[t.status] = by[t.status] || []).push(t);
    const cols = STATUS_ORDER.concat(Object.keys(by).filter(k => !STATUS_ORDER.includes(k)));
    mount(boardBox, h('div', { class: 'board' }, ...cols.filter(c => (by[c] || []).length || ['todo', 'doing', 'review', 'done'].includes(c)).map(c =>
      h('div', { class: 'col' }, h('h3', null, h('span', null, c), h('span', { class: 'muted' }, (by[c] || []).length)),
        ...(by[c] || []).map(t => h('div', { class: 'task' }, h('div', { class: 't' }, h('span', { class: 'mono muted' }, t.id + ' '), t.title || '(untitled)'),
          h('div', { class: 'm' }, [t.owner ? '→ ' + t.owner : 'unassigned', t.role ? ' · ' + t.role : ''].join('')),
          t.line || t.result ? h('div', { class: 'm' }, t.line || t.result) : null))))),
      h('div', { class: 'note' }, 'Tasks are ' + (d.tasks_source || '') + '.'));
  }

  function renderActivity(d) {
    const mins = d.minutes;
    if (!mins.length) { mount(activityBox, h('div', { class: 'empty' }, 'No activity yet.')); return; }
    // Fill gaps so silence is visible as silence.
    const t0 = mins[0].t, t1 = mins[mins.length - 1].t;
    const by = new Map(mins.map(m => [m.t, m]));
    const filled = [];
    for (let t = t0; t <= t1 && filled.length < 720; t += 60000) filled.push(by.get(t) || { t, board: 0, mail_sent: 0, mail_delivered: 0, spawns: 0, leases: 0, requests: 0 });
    const labels = filled.map(m => timeOfDay(m.t));
    const row = (name, key, sub) => {
      const vals = filled.map(m => m[key] || 0);
      const total = vals.reduce((a, b) => a + b, 0);
      return h('div', { class: 'rowspark' }, h('div', null, h('b', null, name), h('div', { class: 'sub muted' }, sub || '')), bars(vals, labels), h('div', { class: 'tot' }, h('b', null, int(total)), h('div', { class: 'sub muted' }, 'peak ' + int(Math.max(0, ...vals)))));
    };
    mount(activityBox,
      row('Model requests', 'requests', 'all agents'), row('Board operations', 'board', 'create, claim, update, finish…'),
      row('Mail sent', 'mail_sent', 'typed, rate-limited'), row('Mail delivered', 'mail_delivered'), row('Spawns', 'spawns'), row('Lease events', 'leases'),
      h('div', { class: 'note' }, `${filled.length} minute${filled.length === 1 ? '' : 's'}, from ${timeOfDay(t0)} to ${timeOfDay(t1)}. Board operations by kind: ` +
        (Object.entries(d.board_ops).sort((a, b) => b[1] - a[1]).map(([k, v]) => k + ' ' + v).join(', ') || 'none') + '.'));
  }

  function renderGov(d) {
    const g = d.governor;
    const tile = (k, v, sub) => h('div', { class: 'tile' }, h('div', { class: 'lbl' }, k), h('div', { class: 'val' }, v), sub ? h('div', { class: 'sub' }, sub) : null);
    const errs = Object.entries(g.errors || {}).map(([k, v]) => k + ' ×' + v).join(', ');
    mount(govBox,
      h('div', { class: 'tiles', css: { gridTemplateColumns: 'repeat(2, 1fr)' } },
        tile('Peak in flight', int(g.peak_in_flight), 'requests at once'), tile('Peak requests / min', int(g.peak_rpm), 'average ' + g.avg_rpm.toFixed(1)),
        tile('Retries', int(g.retries), g.rate_limited ? g.rate_limited + ' rate-limited (429)' : 'none rate-limited'), tile('Requests', int(g.requests), errs || 'no provider errors')),
      g.events ? h('div', { class: 'note' }, h('p', null, 'Last governor event: ', h('span', { class: 'mono' }, Object.entries(g.last || {}).map(([k, v]) => k + '=' + v).join(' ')))) :
        h('div', { class: 'note' }, h('p', null, 'This log has no governor events, so admission stats are derived from the request stream: in-flight requests are counted from request and response events, requests per minute from request times.')));
  }

  function renderMail(d) {
    if (!d.mail.length) { mount(mailBox, h('div', { class: 'empty' }, h('strong', null, 'No mail'), 'Agents have not messaged each other in this log.')); return; }
    const kinds = Object.entries(d.mail_kinds).map(([k, v]) => h('span', { class: 'chip' }, k + ' ' + v));
    mount(mailBox,
      h('div', { class: 'row' }, ...kinds, h('span', { class: 'muted' }, d.totals.mail_sent + ' sent · ' + d.totals.mail_delivered + ' delivered')),
      h('div', { class: 'row', css: { marginTop: '8px' } }, ...d.mail_pairs.slice(0, 8).map(p => h('span', { class: 'chip' }, p.from + ' → ' + p.to + ' ×' + p.n))),
      h('div', { css: { marginTop: '8px', maxHeight: '340px', overflow: 'auto' } }, ...d.mail.slice(0, 40).map(m =>
        h('div', { class: 'mail' }, h('span', { class: 'muted num' }, timeOfDay(m.t)), h('span', null, h('b', null, m.from), ' → ', h('b', null, m.to), ' ', m.kind && m.kind !== 'info' ? h('span', { class: 'chip warn' }, m.kind) : null, m.latency_ms >= 0 ? h('span', { class: 'muted' }, ' · delivered in ' + dur(m.latency_ms)) : null),
          h('span', { class: 'txt' }, m.text || '')))));
  }

  function renderSpawns(d) {
    if (!d.spawns.length) { mount(spawnBox, h('div', { class: 'empty' }, 'No spawns.')); return; }
    mount(spawnBox, h('div', { class: 'tblwrap' }, h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, ['Time', 'Agent', 'Role', 'Parent', 'Task'].map(t => h('th', null, t)))),
      h('tbody', null, ...d.spawns.slice(0, 40).map(s => h('tr', { class: 'clickable', on: { click: () => nav({ view: 'timeline', agent: s.id, req: '' }) } },
        h('td', { class: 'num' }, timeOfDay(s.t)), h('td', null, h('b', null, s.id)), h('td', null, s.role), h('td', null, s.parent || '–'), h('td', { class: 'mono' }, s.task || '')))))));
  }

  function renderLeases(d) {
    const t = d.totals;
    if (!d.leases.length) {
      mount(leaseBox, h('div', { class: 'note' }, h('p', null, t.alerts ? `${t.alerts} board alert${t.alerts === 1 ? '' : 's'} (lease conflicts and stalls raise alerts) but no lease events: this version of the runtime enforces leases without logging them.` : 'No lease events or board alerts in this log.')));
      return;
    }
    mount(leaseBox, h('div', { class: 'tblwrap' }, h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, ['Time', 'Agent', 'Action', 'Path', 'Holder'].map(t => h('th', null, t)))),
      h('tbody', null, ...d.leases.slice(0, 30).map(l => h('tr', null, h('td', { class: 'num' }, timeOfDay(l.t)), h('td', null, l.agent || ''), h('td', null, l.action || ''), h('td', { class: 'mono' }, l.path || ''), h('td', null, l.holder || '')))))));
  }

  return { el, update, destroy() {} };
}
