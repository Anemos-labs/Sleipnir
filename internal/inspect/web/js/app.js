// Entry point: header, KPI tiles, tabs, keyboard, live polling and view routing.

import { h, s, mount, tok, usd, pct, int, dur, store, ago } from './lib.js';
import { S, VIEWS, api, nav, onRoute, readRoute, refreshCore, resetSession, setFollow, mainRows } from './state.js';
import { sparkline } from './charts.js';
import * as timeline from './views/timeline.js';
import * as layers from './views/layers.js';
import * as compaction from './views/compaction.js';
import * as swarm from './views/swarm.js';
import * as cost from './views/cost.js';
import * as anomalies from './views/anomalies.js';
import * as events from './views/events.js';
import * as sessions from './views/sessions.js';

const VIEW_MODULES = { timeline, layers, compaction, swarm, cost, anomalies, events, sessions };
const TAB_LABEL = { timeline: 'Timeline', layers: 'Layers', compaction: 'Compaction', swarm: 'Swarm', cost: 'Cost', anomalies: 'Anomalies', events: 'Events' };

const $ = id => document.getElementById(id);
let cur = null, curName = '', busy = false, sessionsTimer = 0, lastPoll = 0;

// ---- theme --------------------------------------------------------------------------------

function applyTheme(t) {
  if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t; else delete document.documentElement.dataset.theme;
}
function cycleTheme() {
  const order = ['auto', 'light', 'dark'];
  const next = order[(order.indexOf(store.get('theme', 'auto')) + 1) % 3];
  store.set('theme', next); applyTheme(next);
  renderTop(); if (cur) cur.update();
}

// ---- header -------------------------------------------------------------------------------------

/** The mark: the eight-legged horse, reduced for small sizes (docs/media/favicon.svg draws the same). */
function logo() {
  const legs = ['#bb9af7', '#7aa2f7', '#7aa2f7', '#7dcfff', '#7dcfff', '#9ece6a', '#e0af68', '#ff9e64'];
  const len = [14, 16, 12, 16, 14, 16, 12, 15];
  return s('svg', { viewBox: '0 0 64 64', width: 24, height: 24, 'aria-hidden': 'true' },
    s('rect', { width: 64, height: 64, rx: 14, fill: '#24232e' }),
    ...legs.map((c, k) => s('rect', { x: 13.5 + 4.1 * k, y: 35, width: 3.4, height: len[k], rx: 1.7, fill: c })),
    s('path', { d: 'M13 28 C11 23 14 20 20 21 L40 21 C45 21 47 19 48 14 L46 9 L51 9 L53 6 L55 10 L61 16 C62 19 60 22 57 21 L53 20 L50 26 C48 33 42 37 36 37 L22 37 C16 37 13 33 13 28 Z', fill: '#e9e7f5' }),
    s('path', { d: 'M13 26 C8 26 5 30 5 35 C8 32 10 31 13 32 Z', fill: '#b79cf9' }),
    s('path', { d: 'M44 13 L41 19 M47 12 L44 19', stroke: '#b79cf9', 'stroke-width': 2, 'stroke-linecap': 'round', fill: 'none' }),
    s('circle', { cx: 54, cy: 13, r: 1.6, fill: '#24232e' }));
}

function stateChip(st) {
  const label = { live: 'live', ended: 'ended', idle: 'idle', empty: 'empty' }[st] || st;
  const title = { live: 'the log is being written', ended: 'session.end was logged', idle: 'no session.end and no recent writes: interrupted, or an old log', empty: 'no events yet' }[st] || '';
  return h('span', { class: 'chip', title }, h('span', { class: 'dot ' + (st === 'live' ? 'live' : st) }), label);
}

/** The RL episode's outcome, when the log or an episode.json carries one. */
function episodeChip(rl) {
  if (!rl) return null;
  const ep = rl.episode, last = rl.outcomes && rl.outcomes.length ? rl.outcomes[rl.outcomes.length - 1] : null;
  let pass = null, text = '';
  const tip = [];
  if (ep) {
    pass = ep.pass == null ? null : ep.pass;
    text = (pass == null ? 'episode' : pass ? '\u2713 pass' : '\u2717 fail') + ' \u00b7 reward ' + Number(ep.reward).toFixed(2);
    tip.push([ep.task_id ? 'task ' + ep.task_id : '', 'sample ' + ep.sample, ep.policy ? 'policy ' + ep.policy : ''].filter(Boolean).join(' \u00b7 '));
    if (ep.flags && ep.flags.length) tip.push('flags: ' + ep.flags.join(', '));
    const comps = Object.entries(ep.components || {});
    if (comps.length) tip.push('reward: ' + comps.map(([k, v]) => k + ' ' + Number(v).toFixed(2)).join(', '));
  } else if (last) {
    pass = last.pass;
    text = (pass ? '\u2713 ' : '\u2717 ') + last.kind + ' ' + Number(last.score).toFixed(2);
  } else return null;
  tip.push(`${rl.wire_hashes} requests with a wire hash, ${rl.token_traces} responses with token ids`);
  return h('span', { class: 'chip ' + (pass == null ? 'accent' : pass ? 'good' : 'crit'), title: tip.join('\n') }, text);
}

function renderTop() {
  const sum = S.sum;
  const theme = store.get('theme', 'auto');
  const name = sum ? sum.session.name : S.root || '';
  const multi = S.mode === 'multi';
  const sess = multi
    ? h('button', { class: 'sessbtn', type: 'button', title: 'Switch session', on: { click: () => { resetSession(); S.sid = ''; nav({ view: 'sessions' }, { merge: false }); } } }, h('span', { class: 'ell' }, name || 'Sessions'), h('span', { class: 'muted' }, '▾'))
    : (name ? h('span', { class: 'sessbtn', title: sum ? 'session ' + sum.session.id : '' }, h('span', { class: 'ell' }, name)) : null);
  mount($('top'),
    h('a', { class: 'brand', href: '#/timeline' + (S.sid ? '?session=' + encodeURIComponent(S.sid) : '') }, logo(), 'Sleipnir', h('small', null, 'cache inspector')),
    sess,
    sum && S.view !== 'sessions' ? [
      sum.session.model ? h('span', { class: 'chip', title: (sum.session.models || []).join(', ') }, sum.session.model) : null,
      sum.session.provider ? h('span', { class: 'chip' }, sum.session.provider) : null,
      sum.session.swarm ? h('span', { class: 'chip accent' }, 'swarm · ' + sum.totals.agents + ' agents') : null,
      sum.session.isolation === 'worktree' ? h('span', { class: 'chip', title: 'every writer had a git worktree of its own; work was merged and verified through a queue' }, 'worktrees') : null,
      sum.session.mailman ? h('span', { class: 'chip', title: 'worker mail was digested by a mailman agent' }, 'mailman') : null,
      sum.session.ended && sum.session.end_reason && sum.session.end_reason !== 'completed' ? h('span', { class: 'chip warn', title: 'why the session ended' }, sum.session.end_reason) : null,
      episodeChip(sum.rl),
    ] : null,
    h('span', { class: 'spacer' }),
    sum && S.view !== 'sessions' ? stateChip(sum.state) : null,
    h('button', { class: 'iconbtn', type: 'button', title: 'Theme: ' + theme + ' (click to change)', 'aria-label': 'Theme: ' + theme, on: { click: cycleTheme } }, theme === 'dark' ? '☾' : theme === 'light' ? '☀' : '◐'),
    h('button', { class: 'iconbtn', type: 'button', title: 'Keyboard shortcuts (?)', 'aria-label': 'Keyboard shortcuts', on: { click: showHelp } }, '?'));
}

// ---- KPI tiles -----------------------------------------------------------------------------------------

function tile(label, value, sub, opts = {}) {
  return h('div', { class: 'tile' + (opts.href ? ' link' : ''), on: opts.href ? { click: () => nav({ view: opts.href }) } : null },
    h('div', { class: 'lbl' }, label), h('div', { class: 'val' }, value), sub ? h('div', { class: 'sub ' + (opts.tone || '') }, sub) : null);
}

function renderKpis() {
  const sum = S.sum;
  const box = $('kpis');
  if (!sum || S.view === 'sessions') { mount(box); box.hidden = true; return; }
  box.hidden = false;
  const c = sum.cache, cost = sum.cost, t = sum.totals, comp = sum.compaction, an = sum.anomalies;
  const hits = sum.series.points.map(p => p.hit).slice(-40);
  const anomN = an.total + an.undeclared;
  const hero = h('div', { class: 'hero' },
    h('div', { class: 'lbl' }, 'Cache hit ratio'),
    h('div', { class: 'val' }, pct(c.hit_ratio, 0).replace('%', ''), h('small', null, '%')),
    h('div', { class: 'spark' }, sparkline(hits, { w: 150, hgt: 40, lo: 0, hi: 1, title: 'hit ratio per request group' })),
    h('div', { class: 'sub' }, c.steady_requests ? `steady state ${pct(c.steady_hit_ratio)}` : 'no steady state yet', c.rebase_requests ? ` \u00b7 right after a rebase ${pct(c.rebase_hit_ratio)}` : ''));
  const paid = cost.reported_requests ? cost.reported : cost.actual.total;
  const tiles = h('div', { class: 'tiles' },
    tile('Cost', usd(paid), cost.no_cache.total > 0 ? `${pct(Math.abs(cost.saved_no_cache_pct))} ${cost.saved_no_cache >= 0 ? 'below' : 'above'} no cache` : '', { href: 'cost', tone: cost.saved_no_cache >= 0 ? 'good' : 'crit' }),
    tile('Average context', tok(c.avg_context), c.avg_context_naive > 0 ? `${pct(Math.abs(c.context_reduction))} ${c.context_reduction >= 0 ? 'smaller' : 'larger'} than whole-history (est.)` : ''),
    tile('Requests', int(t.requests), `${int(t.main)} agent · ${int(t.side)} compactor` + (t.pending ? ` · ${t.pending} in flight` : '')),
    tile('Compactions', int(comp.commits), comp.commits ? `${tok(comp.net)} tokens folded` : 'none yet', { href: 'compaction' }),
    tile('Cold starts', int(c.cold_starts), c.first_requests ? `${c.warm_first} of ${c.first_requests} first requests read the shared prefix` : 'requests that read nothing from cache'),
    tile('Anomalies', int(anomN), anomN ? [an.drift ? an.drift + ' drift' : '', an.low_hit ? an.low_hit + ' low read' : '', an.undeclared ? an.undeclared + ' undeclared' : ''].filter(Boolean).join(' · ') : '✓ clean', { href: 'anomalies', tone: anomN ? 'crit' : 'good' }));
  mount(box, hero, tiles);
}

// ---- tabs ------------------------------------------------------------------------------------------------

function renderTabs() {
  const nav_ = $('tabs');
  if (S.view === 'sessions' || !S.sum) { nav_.hidden = true; return; }
  nav_.hidden = false;
  const an = S.sum ? S.sum.anomalies.total + S.sum.anomalies.undeclared : 0;
  mount(nav_, ...VIEWS.map((v, i) => h('button', { class: 'tab', type: 'button', 'aria-current': S.view === v ? 'page' : null, title: `${TAB_LABEL[v]} (${i + 1})`, on: { click: () => nav({ view: v }) } },
    TAB_LABEL[v], v === 'anomalies' && an ? h('span', { class: 'n crit' }, an) : null, v === 'compaction' && S.sum && S.sum.compaction.commits ? h('span', { class: 'n' }, S.sum.compaction.commits) : null)));
}

// ---- views -----------------------------------------------------------------------------------------------------

function banner(kind, title, text) {
  mount($('view'), h('div', { class: 'banner ' + (kind || '') }, h('span', { class: 't' }, title), ' ', text));
  cur = null; curName = '';
}

function ensureView() {
  const name = S.view;
  if (name === curName && cur) return;
  if (cur) cur.destroy();
  const mod = VIEW_MODULES[name] || timeline;
  cur = mod.create();
  curName = name;
  mount($('view'), cur.el);
  $('view').focus({ preventScroll: true });
}

function route() {
  readRoute();
  clearInterval(sessionsTimer);
  if (S.mode === 'multi' && !S.sid && S.view !== 'sessions') { nav({ view: 'sessions' }, { replace: true, merge: false }); return; }
  if (S.mode === 'single' && S.view === 'sessions') { nav({ view: 'timeline' }, { replace: true }); return; }
  if (S.view !== 'sessions' && !S.sum && !S.error) { /* first data pending */ }
  if (S.view === 'sessions' || S.sum) { ensureView(); }
  renderTop(); renderKpis(); renderTabs();
  document.title = (S.sum && S.view !== 'sessions' ? S.sum.session.name + ' · ' : '') + 'Sleipnir cache inspector';
  if (cur) cur.update();
  if (S.view === 'sessions') sessionsTimer = setInterval(() => cur && curName === 'sessions' && cur.update(), 5000);
}

// ---- polling ------------------------------------------------------------------------------------------------------------

async function tick() {
  if (document.hidden || busy) return;
  if (S.view === 'sessions') return;
  // A finished session rarely changes: look every five seconds instead of every one.
  if (S.sum && S.sum.state === 'ended' && Date.now() - lastPoll < 5000) return;
  busy = true;
  lastPoll = Date.now();
  try {
    const first = !S.sum;
    const changed = await refreshCore();
    if (S.loading) {
      const l = S.loading;
      banner('', 'Loading the log…', l.total ? `${Math.round(100 * l.read / l.total)}% of ${(l.total / 1e6).toFixed(1)} MB read.` : '');
      return;
    }
    if (first) { route(); return; }
    renderTop(); renderKpis(); renderTabs();
    if (changed || S.view === 'events') { ensureView(); cur.update(); }
    S.error = null;
  } catch (e) {
    if (e.status === 401) banner('warn', 'Token required.', 'Open the URL that sleipnir inspect printed (it carries the token), or send Authorization: Bearer <token>.');
    else if (e.status === 404 && S.sid) banner('warn', 'Session not found.', 'It may have been removed; pick another one.');
    else { S.error = e; if (!S.sum) banner('warn', 'Cannot reach the inspector.', String(e.message || e)); }
  } finally { busy = false; }
}

// ---- help and keyboard ---------------------------------------------------------------------------------------------------------

function showHelp() {
  const o = $('overlay');
  const key = (k, d) => [h('kbd', null, k), h('span', null, d)];
  mount(o, h('div', { class: 'panel', role: 'dialog', 'aria-label': 'Keyboard shortcuts' },
    h('h2', null, 'Keyboard'),
    h('div', { class: 'keys' },
      ...key('1 … 7', 'Timeline, Layers, Compaction, Swarm, Cost, Anomalies, Events'),
      ...key('j / →', 'next request'), ...key('k / ←', 'previous request'), ...key('Home / End', 'first / last request'),
      ...key('f', 'follow the newest request (live sessions)'), ...key('t', 'timeline: time or request number on the x axis'), ...key('c', 'timeline: color by cache class or by layer'),
      ...key('g s', 'session list (when browsing a directory)'), ...key('?', 'this help'), ...key('Esc', 'close')),
    h('hr', { class: 'hr' }),
    h('div', { class: 'note' },
      h('p', null, 'Every view has a permalink: the address bar always holds the session, agent and request.'),
      h('p', null, 'The stack of a request is drawn in prompt order. A provider cache matches a prefix, so a change in one layer re-processes everything after it.'),
      h('p', null, 'Numbers marked “est.” are estimates and the assumptions are written out on the Cost tab.')),
    h('div', { class: 'row', css: { marginTop: '10px' } }, h('button', { class: 'btn primary', type: 'button', on: { click: closeOverlay } }, 'Close'))));
  o.hidden = false;
  o.onclick = e => { if (e.target === o) closeOverlay(); };
  o.querySelector('button').focus();
}
function closeOverlay() { $('overlay').hidden = true; mount($('overlay')); }

let gPending = 0;
document.addEventListener('keydown', e => {
  const t = e.target;
  if (t && (t.tagName === 'INPUT' || t.tagName === 'SELECT' || t.tagName === 'TEXTAREA') && e.key !== 'Escape') return;
  if (e.ctrlKey || e.metaKey || e.altKey) return;
  if (e.key === 'Escape') { if (!$('overlay').hidden) closeOverlay(); return; }
  if (!$('overlay').hidden) return;
  if (e.key === '?') { showHelp(); return; }
  if (gPending && Date.now() - gPending < 1200 && e.key === 's' && S.mode === 'multi') { gPending = 0; resetSession(); S.sid = ''; nav({ view: 'sessions' }, { merge: false }); return; }
  gPending = e.key === 'g' ? Date.now() : 0;
  if (/^[1-7]$/.test(e.key) && S.view !== 'sessions') { nav({ view: VIEWS[+e.key - 1] }); return; }
  if (e.key === 'f' && S.sum && S.sum.state === 'live') { setFollow(!S.follow); if (cur) cur.update(); return; }
  if (e.key === 't' && S.view === 'timeline') { nav({ x: (S.q.x || (S.sum && S.sum.session.duration_ms < 3000 ? 'req' : 'time')) === 'time' ? 'req' : 'time' }, { replace: true }); return; }
  if (e.key === 'c' && S.view === 'timeline') { nav({ color: S.q.color === 'layer' ? 'cache' : 'layer' }, { replace: true }); return; }
  if (cur && cur.key && cur.key(e)) e.preventDefault();
});

// ---- boot ------------------------------------------------------------------------------------------------------------------------------

async function boot() {
  applyTheme(store.get('theme', 'auto'));
  readRoute();
  onRoute(route);
  window.addEventListener('hashchange', () => { readRoute(); route(); });
  try {
    const list = await api('sessions');
    S.mode = list.mode; S.sessions = list.sessions; S.root = list.root;
    if (list.mode === 'single') S.sid = '';
  } catch (e) {
    if (e.status === 401) banner('warn', 'Token required.', 'Open the URL that sleipnir inspect printed (it carries the token), or send Authorization: Bearer <token>.');
    else banner('warn', 'Cannot reach the inspector.', String(e.message || e));
    renderTop();
    return;
  }
  renderTop();
  if (S.mode === 'multi' && (!S.sid || S.view === 'sessions')) { route(); }
  else await tick();
  setInterval(tick, 1000);
}

boot();
