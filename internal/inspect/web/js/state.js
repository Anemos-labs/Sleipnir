// Shared state, the API client and the hash router. Views read S and call
// nav(); app.js owns the render loop.

import { parseHash, buildHash, store } from './lib.js';

export const VIEWS = ['timeline', 'layers', 'compaction', 'swarm', 'cost', 'anomalies', 'events'];

export const S = {
  mode: 'single',
  root: '',
  sessions: [],
  sid: '',
  sum: null,
  agents: [],
  reqs: [],
  byId: new Map(),
  cursor: 0,
  dropped: 0,
  view: 'timeline',
  q: {},
  follow: store.get('follow', true),
  loading: null,
  error: null,
  version: 0, // bumps when reqs change
  memo: new Map(),
};

const routeListeners = new Set();
export function onRoute(fn) { routeListeners.add(fn); return () => routeListeners.delete(fn); }

/** Fetches JSON from the API. Returns {loading:true,...} while a big log is still loading. */
export async function api(path, params = {}) {
  const p = new URLSearchParams();
  if (S.sid) p.set('session', S.sid);
  for (const [k, v] of Object.entries(params)) if (v != null && v !== '') p.set(k, v);
  const qs = p.toString();
  const res = await fetch('/api/' + path + (qs ? '?' + qs : ''), { headers: { Accept: 'application/json' }, credentials: 'same-origin', cache: 'no-store' });
  if (res.status === 401) throw Object.assign(new Error('unauthorized'), { status: 401 });
  let body = null;
  try { body = await res.json(); } catch { /* not JSON */ }
  if (res.status === 202) return { loading: true, ...(body || {}) };
  if (!res.ok) throw Object.assign(new Error((body && body.error) || res.statusText), { status: res.status });
  return body;
}

/** Applies the location hash to S.view and S.q. */
export function readRoute() {
  const { view, q } = parseHash();
  const v = VIEWS.includes(view) || view === 'sessions' ? view : 'timeline';
  S.view = v;
  const o = {};
  for (const [k, val] of q.entries()) o[k] = val;
  S.q = o;
  if (o.session != null) S.sid = o.session;
  return S;
}

/** Navigates: nav({view:'layers', req:'be-1.3'}). Unspecified query keys are kept unless merge=false. */
export function nav(next, { replace = false, merge = true } = {}) {
  const view = next.view || S.view;
  const q = merge ? { ...S.q } : {};
  for (const [k, v] of Object.entries(next)) if (k !== 'view') { if (v == null || v === '') delete q[k]; else q[k] = v; }
  if (S.sid) q.session = S.sid; else delete q.session;
  const hash = buildHash(view, q);
  if (hash === location.hash) { readRoute(); routeListeners.forEach(f => f()); return; }
  if (replace) history.replaceState(null, '', hash); else history.pushState(null, '', hash);
  readRoute();
  routeListeners.forEach(f => f());
}

// ---- data --------------------------------------------------------------------------------

export function resetSession() {
  S.sum = null; S.agents = []; S.reqs = []; S.byId = new Map(); S.cursor = 0; S.dropped = 0; S.version++; S.memo = new Map(); S.error = null;
}

function upsert(rows) {
  let sorted = true;
  for (const r of rows) {
    const old = S.byId.get(r.id);
    if (old) Object.assign(old, r);
    else {
      const last = S.reqs[S.reqs.length - 1];
      if (last && last.seq > r.seq) sorted = false;
      S.reqs.push(r); S.byId.set(r.id, r);
    }
  }
  if (!sorted) S.reqs.sort((a, b) => a.seq - b.seq);
}

/** Pulls the summary, agents and any new or changed requests. Returns true when anything changed. */
export async function refreshCore() {
  const sum = await api('summary');
  if (sum.loading) { S.loading = sum; return false; }
  S.loading = null;
  const changed = !S.sum || sum.rev !== S.sum.rev;
  S.sum = sum;
  if (!changed) return false;
  const first = S.cursor === 0 && S.reqs.length === 0;
  const [agents, page] = await Promise.all([
    api('agents'),
    api('requests', first ? { tail: 1, limit: 8000 } : { since: S.cursor, limit: 5000 }),
  ]);
  S.agents = agents;
  upsert(page.requests);
  S.cursor = page.rev;
  S.dropped = page.dropped;
  S.version++;
  if (page.more && !first) await refreshMore();
  return true;
}

async function refreshMore() {
  for (let i = 0; i < 20; i++) {
    const page = await api('requests', { since: S.cursor, limit: 5000 });
    upsert(page.requests);
    S.cursor = page.rev;
    if (!page.more) break;
  }
  S.version++;
}

/** Memoised by summary revision: fetches once per change. */
export async function memo(key, path, params) {
  const rev = S.sum ? S.sum.rev : 0;
  const k = key + '|' + JSON.stringify(params || {});
  const hit = S.memo.get(k);
  if (hit && hit.rev === rev) return hit.data;
  const data = await api(path, params);
  S.memo.set(k, { rev, data });
  if (S.memo.size > 64) S.memo.delete(S.memo.keys().next().value);
  return data;
}

export function agentIds() { return S.agents.map(a => a.id); }
export function agentById(id) { return S.agents.find(a => a.id === id); }

/** Main requests that have a response, oldest first. */
export function mainRows(agentId) {
  return S.reqs.filter(r => r.kind === 'main' && r.done && (!agentId || r.agent === agentId));
}

export function sideRows(agentId) {
  return S.reqs.filter(r => r.kind !== 'main' && r.done && (!agentId || r.agent === agentId));
}

/** The default agent for views that show one: the only one, or the one with the most requests. */
export function defaultAgent() {
  if (!S.agents.length) return '';
  if (S.agents.length === 1) return S.agents[0].id;
  return [...S.agents].sort((a, b) => b.main - a.main)[0].id;
}

export function setFollow(v) { S.follow = v; store.set('follow', v); }
