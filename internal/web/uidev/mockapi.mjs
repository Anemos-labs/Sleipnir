#!/usr/bin/env node
// mockapi.mjs: serves the REAL page (internal/web/ui, the live data layer and all) with the API answered from the reference page's own
// sessions, so that the page can be compared with the reference page on the same content (scripts/web-parity.mjs --b http://127.0.0.1:PORT/).
//
//   node internal/web/uidev/mockapi.mjs [--port 0] [--ui DIR]
//
// The reference page's modules (internal/web/uidev/mock/js: the data pack, the fixtures and the scripted timelines) run in a vm of their own
// and build its three live sessions; each becomes a tab whose snapshot holds the whole scripted log at the reference page's own start time
// (shop at 00:38), so that the page's clock reaches each event when the reference page's would. What the reference page computed itself and the real
// server sends as events is added: the plan's steps, every agent's prompt layers, and each agent's reported cost after a request (the
// reference page's sample prices), so that the two pages show the same numbers. Catalogues (models, roles, providers, projects, recorded
// sessions, the CLI spec, the slash commands, the settings views and the schedule) come from the same pack. The stream stays open with heartbeats; an answer to a question
// is accepted and published as its `answer` event. Anything else under /api/ is 404 (or 501 for a mutation). Nothing here ships.
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(here, '..', '..', '..');
const CSP = ["default-src 'none'", "script-src 'self'", "style-src 'self'", "style-src-attr 'unsafe-inline'", "font-src 'self'", "img-src 'self' data:", "connect-src 'self'", "frame-ancestors 'none'", "form-action 'none'", "base-uri 'none'"].join('; ');
const TYPES = { '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.json': 'application/json; charset=utf-8', '.woff2': 'font/woff2', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon' };
const MOCK = ['00-namespace.js', 'data.js', 'outputs.js', 'cli-spec.js', '00-util.js', '10-fixtures.js', '11-data-adapter.js', '20-clock.js', '30-model.js', '40-scripts.js', '50-sessions.js'];

/** The reference page's world: its modules in a vm, its sessions built (the oracle). */
export function oracle(dir = path.join(here, 'mock', 'js')) {
  const sb = { console, setTimeout, clearTimeout, setInterval, clearInterval, Date, Math, JSON };
  sb.window = sb; sb.globalThis = sb; vm.createContext(sb);
  for (const f of MOCK) vm.runInContext(fs.readFileSync(path.join(dir, f), 'utf8'), sb, { filename: f });
  sb.SL.sessions.init();
  return sb.SL;
}

/** epoch ms of a time of day (seconds since midnight) on 2026-01-02 in UTC: the page derives the same t0 from it when its time zone is
 *  UTC, as scripts/web-parity.mjs sets it (Emulation.setTimezoneOverride). */
const startedAt = t0 => Date.UTC(2026, 0, 2) + Math.round(t0 * 1000);

/** The server sends a task, a goal, a verdict, a queue and a gauge as the whole state each time (a field it omits is empty); the
 *  reference page's script sends what changed. Complete such an event from the reference model after it was reduced (w). */
function whole(e, w) {
  const T = e.k === 'task' && w.tasks[e.id];
  if (T) Object.assign(e, { title: T.title, owner: T.owner || undefined, deps: T.deps && T.deps.length ? T.deps.slice() : undefined, scope: T.scope || '-', s: T.st, closure: T.closure || undefined, failed: T.failed || undefined, attempts: T.attempts || undefined });
  if (e.k === 'goal') Object.assign(e, { s: w.goal.state, objective: w.goal.objective || undefined, turns: w.goal.turns || undefined, max: w.goal.max || undefined, paused: w.goal.paused || undefined, reason: w.goal.reason || undefined });
  if (e.k === 'verdict') Object.assign(e, { kind: w.verdictKind || undefined, left: w.left && w.left.length ? w.left.slice() : undefined });
  if (e.k === 'queue') Object.assign(e, { conflicts: w.conflicts || 0, bounced: w.bounced || 0 });
  if (e.k === 'gov') Object.assign(e, { inflight: w.inflight || undefined, queued: w.queued || undefined });
  if (e.k === 'warm' && !e.ttl && w.ttl) e.ttl = w.ttl;
}

/** A session of the reference page as a TabSnapshot: the whole scripted log, with the events the real server sends that the reference page computed itself. */
export function snapshotOf(SL, S, order) {
  const D = SL.D, R = SL.model, log = S.log.slice().sort((a, b) => a.t - b.t || (a.seq || 0) - (b.seq || 0));
  const keyframe = [{ t: 0, k: 'plan', seq: 0, steps: S.plan.slice(), st: S.plan.map(() => 'pending') }].concat(S.roster.map(r => ({ t: 0, k: 'layers', seq: 0, id: r.id, toks: D.layerToks(r.id) })));
  const W = R.newModel(S, { noChat: true }), events = []; let seq = 0;
  for (const e0 of log) {
    const e = JSON.parse(JSON.stringify(e0)); delete e.seq; R.reduce(W, e0); whole(e, W);
    e.seq = ++seq; events.push(e);
    if ((e.k === 'req' || e.k === 'use') && W.ag[e.id]) { const A = W.ag[e.id], c = SL.calc.agent(A); events.push({ t: e.t, k: 'use', seq: ++seq, id: e.id, rd: A.rd, un: A.un, out: A.out, wr: 0, cost: c.cost, saved: c.saved }); }
  }
  const m = S.meta, meta = { cwd: m.cwd, model: m.model, mode: m.mode, effort: m.effort, budget: m.budget, swarm: m.swarm, isolation: m.isolation, verify: m.verify, commit: m.commit, mailman: m.mailman, trustProject: m.trustProject, noMcp: m.noMcp, roleModels: m.roleModels, rules: m.rules, goalText: m.goalText, launch: m.launch, headless: !!m.headless, startedAt: startedAt(m.t0) };
  if (m.askTimeout) meta.askTimeout = m.askTimeout;
  const tab = { id: S.id, sid: S.sid, name: S.name, cwd: m.cwd, gen: 1, order, createdAt: startedAt(m.t0) }; if (m.headless) tab.headless = true;
  return { tab, gen: 1, seq, now: S.wt, metaV: 1, rosterV: 1, meta, roster: S.roster.map(r => ({ id: r.id, role: r.role, code: r.code, nth: r.nth, k: r.k, leg: r.leg, scope: r.scope, ro: !!r.ro, model: r.model, spawn: r.spawn || 0 })), keyframe, events, hist: [], questions: [] };
}

/** The catalogues of the pack in the API's shapes. */
export function catalogues(SL) {
  const D = SL.D, X = D.extra || {};
  return {
    cli: D.spec,
    slash: D.slash,
    models: { models: D.models.map(x => ({ ref: x.ref, provider: x.ref.split('/')[0], ctx: x.ctx, in: x.in, out: x.out, cached: x.cached, tools: x.tools, reasoning: x.reasoning, fav: !!x.fav, priceKnown: x.in != null })), favs: D.models.filter(x => x.fav).map(x => x.ref), roles: Object.keys(D.roles).map(n => ({ name: n, code: D.roles[n].code, ro: D.roles[n].ro, desc: D.roles[n].desc })), roleOrder: D.roleOrder, roleModels: D.roleModels, efforts: ['default', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'] },
    providers: { providers: (Array.isArray(X.providers) ? X.providers.map(p => ({ id: p.name, name: p.name.charAt(0).toUpperCase() + p.name.slice(1), base: p.baseUrl, key: p.signedIn ? 'signed in' : p.keyState === 'env' ? 'env' : p.keyState === 'stored' ? 'stored' : 'none', env: p.keyEnv, state: p.signedIn ? 'signed in' : (p.keyWhere || p.keyState), note: p.notes || '', recommended: !!p.recommended, keyWhere: p.keyWhere, dialect: p.dialect, signedIn: !!p.signedIn })) : D.providers.map(p => ({ id: p.id, name: p.name, base: p.base, key: p.key, env: p.env, state: p.state, note: p.note }))), providerNote: X.providerNote || '' },
    /* the pack's settings views have the API's shapes */
    permissions: X.permissions, trust: X.trust, mcp: X.mcp, config: X.config,
    skills: { skills: X.skills || [], skillsBudget: X.skillsBudget, commands: X.commands || [], hooks: X.hooks },
    schedule: X.schedule ? { jobs: X.schedule.jobs || [], daemon: X.schedule.daemon || {}, logs: X.schedule.logs || [] } : null,
    projects: { projects: [{ dir: '~/projects/shop', root: '~/projects/shop', name: 'shop', trust: 'trusted', files: 3, default: true }, { dir: '~/projects/orders-api', root: '~/projects/orders-api', name: 'orders-api', trust: 'trusted', files: 2 }, { dir: '~/scratch/untrusted-demo', root: '~/scratch/untrusted-demo', name: 'untrusted-demo', trust: 'not trusted', files: 4 }] },
    recorded: { recorded: SL.sessions.recorded, mb: SL.sessions.recordedMb() },
    defaults: { cwd: '~/projects/shop', model: 'anthropic/claude-sonnet-5-5', mode: 'default', swarm: 8, isolation: 'worktree', verify: 'go test {dirs}', budget: 5, trustProject: true, maxWorkers: 12 },
  };
}

/** Start the server. Resolves {server, url, close}. */
export function start(o = {}) {
  const SL = oracle(), ui = path.resolve(o.ui || path.join(REPO, 'internal/web/ui'));
  const snaps = {}; SL.sessions.list.forEach((S, i) => { snaps[S.id] = snapshotOf(SL, S, i); });
  const cat = catalogues(SL), streams = new Set(); let frameId = 0;
  const hello = () => ({ boot: 'mockapi', now: Date.now(), version: 'mock', streamAfter: 0, server: { addr: '127.0.0.1:6969', loopback: true, version: 'mock' }, limits: { maxBody: 65536, maxMessage: 262144, maxQuestions: 64 }, ui: { version: 'mock' }, active: 'shop', tabs: Object.values(snaps).map(s => s.tab), defaults: cat.defaults });
  const publish = (type, data) => { const msg = 'id: ' + (++frameId) + '\nevent: ' + type + '\ndata: ' + JSON.stringify(data) + '\n\n'; streams.forEach(r => r.write(msg)); };
  const json = (res, code, body) => { res.writeHead(code, { 'Content-Type': TYPES['.json'], 'Content-Security-Policy': CSP, 'Cache-Control': 'no-store' }); res.end(JSON.stringify(body)); };
  const server = http.createServer((req, res) => {
    const u = new URL(req.url, 'http://x'), p = u.pathname;
    if (p.startsWith('/api/')) {
      if (p === '/api/stream') { res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store', 'Content-Security-Policy': CSP }); res.write('retry: 2000\n\n'); streams.add(res); const hb = setInterval(() => res.write(': hb\n\n'), 15000); req.on('close', () => { clearInterval(hb); streams.delete(res); }); return; }
      if (req.method === 'GET') {
        if (p === '/api/hello') return json(res, 200, hello());
        let m = /^\/api\/sessions\/([^/]+)\/(snapshot|slash|permissions|trust|mcp|skills|config)$/.exec(p);
        if (m && snaps[m[1]]) { const v = m[2] === 'snapshot' ? snaps[m[1]] : m[2] === 'slash' ? { slash: cat.slash } : cat[m[2]]; if (v) return json(res, 200, v); }
        const g = { '/api/cli': cat.cli, '/api/models': cat.models, '/api/providers': cat.providers, '/api/projects': cat.projects, '/api/recorded': cat.recorded, '/api/schedule': cat.schedule, '/api/recorded/watching': { tabs: [] }, '/api/runs': { runs: [] }, '/api/doctor/endpoints': { endpoints: [] } }[p];
        if (g) return json(res, 200, g);
        if (/^\/api\/sessions\/[^/]+\/ws\/index$/.test(p)) return json(res, 200, { root: '/x', isolation: 'none', base: { id: 'base', label: 'before the session', time: '' }, cps: [], tree: [], version: 'v0' });   /* the Workspace's own parity runs on internal/web/uidev/test/packserver.mjs */
        if (/^\/api\/sessions\/[^/]+\/complete$/.test(p)) return json(res, 200, { paths: [] });
        return json(res, 404, { error: 'not served by this development server', code: 'not_found' });
      }
      let body = ''; req.on('data', d => { body += d; }); req.on('end', () => {
        const m = /^\/api\/questions\/([^/]+)\/answer$/.exec(p);
        if (m) { let a = {}; try { a = JSON.parse(body || '{}'); } catch { /* empty */ } const tab = Object.values(snaps).find(s => s.events.some(e => e.k === 'ask' && e.q.id === m[1])); if (!tab) return json(res, 404, { error: 'no such question', code: 'no_question' }); tab.seq++; publish('ev', { tab: tab.tab.id, ev: { t: 0, k: 'answer', seq: tab.seq, qid: m[1], choice: a.choice, note: a.note || '', by: 'you' } }); return json(res, 200, { ok: true }); }
        if (p === '/api/confirm') return json(res, 200, { id: 'mock', scope: '', expires_in: 60 });
        return json(res, 501, { error: 'this development server does not do that', code: 'not_implemented' });
      });
      return;
    }
    let rel = decodeURIComponent(p); if (rel.endsWith('/')) rel += 'index.html';
    const file = path.resolve(ui, '.' + rel);
    if (!file.startsWith(ui + path.sep) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) { res.writeHead(404, { 'Content-Security-Policy': CSP }); return res.end('not found\n'); }
    res.writeHead(200, { 'Content-Type': TYPES[path.extname(file)] || 'application/octet-stream', 'Content-Security-Policy': CSP, 'X-Content-Type-Options': 'nosniff', 'Cache-Control': 'no-store' }); res.end(fs.readFileSync(file));
  });
  return new Promise(resolve => server.listen(o.port || 0, '127.0.0.1', () => { const url = 'http://127.0.0.1:' + server.address().port + '/'; resolve({ server, url, close: () => { streams.forEach(r => r.end()); server.close(); } }); }));
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const a = process.argv.slice(2), opt = {};
  for (let i = 0; i < a.length; i++) { if (a[i] === '--port') opt.port = +a[++i]; else if (a[i] === '--ui') opt.ui = a[++i]; }
  const s = await start(opt);
  console.log(s.url);
  for (const sig of ['SIGINT', 'SIGTERM']) process.once(sig, () => { s.close(); process.exit(0); });
}
