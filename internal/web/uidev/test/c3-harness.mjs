// c3-harness.mjs: loads the classic scripts of the Sessions, Runner, Settings and Tools pages into a Node vm context with the few browser
// globals and page modules they touch (stubs for the views registry, the sessions registry, the model calculations, the data layer and the
// toast and confirm of the overlays), so that their pure parts, their state machines and their API calls can be tested with `node --test`
// and no dependency. These modules render into a DOM: what is tested here is the markup they build (as strings), the requests they make
// and what they do with the answers; the DOM itself is covered by the browser suite (scripts/web-parity.mjs and its sweeps).
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';
import { webcrypto } from 'node:crypto';

export const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'ui');
export const JS = path.join(ROOT, 'js');

/** A fetch fake: routes(call) -> {status, body, headers} (or a promise of it); calls are recorded as {method, path, headers, body}. */
export function fakeFetch(routes) {
  const calls = [];
  const fn = async (p, init) => {
    init = init || {};
    const call = { method: init.method || 'GET', path: p, headers: Object.assign({}, init.headers || {}), body: init.body === undefined ? undefined : JSON.parse(init.body) };
    calls.push(call);
    const r = await routes(call);
    const headers = new Map(Object.entries(r.headers || {}).map(([k, v]) => [k.toLowerCase(), String(v)]));
    const text = r.body === undefined ? '' : JSON.stringify(r.body);
    return { ok: r.status >= 200 && r.status < 300, status: r.status, headers: { get: k => headers.has(k.toLowerCase()) ? headers.get(k.toLowerCase()) : null }, json: async () => JSON.parse(text) };
  };
  fn.calls = calls;
  return fn;
}

/**
 * A page: the context with SL loaded (namespace, util, api.js) and the stubs, then the named modules. Returns {SL, ctx, toasts, confirms,
 * views, fetch, bus}. `routes` answers the fake fetch; `modules` are the files of ui/js to run after the stubs, in order.
 */
export function page({ routes, modules, sessions } = {}) {
  const fetch = fakeFetch(routes || (() => ({ status: 404, body: { error: 'no route', code: 'not_found' } })));
  const store = new Map();
  const ctx = {
    console, setTimeout, clearTimeout, setInterval, clearInterval, Promise, TextEncoder, AbortController, URLSearchParams, URL, performance, fetch,
    crypto: webcrypto, location: { reload() {}, search: '', host: '127.0.0.1:6969' },
    localStorage: { getItem: k => store.has(k) ? store.get(k) : null, setItem: (k, v) => store.set(k, String(v)), removeItem: k => store.delete(k) },
    navigator: {}, document: { activeElement: null, createElement: () => ({}), querySelector: () => null },
  };
  ctx.window = ctx; ctx.globalThis = ctx; vm.createContext(ctx);
  const run = n => vm.runInContext(fs.readFileSync(path.isAbsolute(n) ? n : path.join(JS, n), 'utf8'), ctx, { filename: n });
  ['00-namespace.js', '00-util.js', 'api.js'].forEach(run);
  const SL = ctx.SL, toasts = [], confirms = [], views = {};
  SL.api.cfg.toast = (t, k) => toasts.push([t, k, 'api']);
  SL.api.cfg.reload = () => {};
  SL.views = { register: d => { views[d.name] = d; }, show() { return true; }, current: null };
  SL.loop = { dirty: false, frameNo: 0 };
  SL.settings = { hover: 'both', motion: 'auto', density: 'comfortable', cache: 'quiet', title: 'auto', ver: 0 };
  SL.G = { ver: 0, favs: new Set(), mcp: [], trust: [], trustDirs: [], providers: [], runs: [], sched: { jobs: [], daemon: {}, logs: [], n: 0 } };
  SL.D = { extra: { projects: [], doctor: { endpoints: [] } }, models: [], roles: { manager: { code: 'mgr' }, backend: { code: 'be' } }, roleOrder: ['backend'], roleModels: {}, efforts: [], modes: [{ id: 'default', desc: 'asks' }], shortcuts: [], spec: { commands: [], chatSlash: [] }, rules: [], testsPreset: [], model(ref) { return this.models.find(m => m.ref === ref) || null; } };
  SL.calc = { totals: () => ({ cost: 0.1234, pct: 93, prompt: 1000, out: 100, saved: 0.05 }), openQuestion: () => null, active: () => 2, waiting: () => 0, agent: () => ({ prompt: 10, pct: 50, out: 5, cost: 0.001 }) };
  SL.sessions = { list: sessions || [], active: (sessions || [])[0] || null, recorded: [], needs: () => [], recordedMb: () => 0, get(id) { return (this.list || []).find(s => s.id === id); }, prune: () => ({ list: [], mb: 0 }), parseAge: () => 0, pruneCandidates: () => [] };
  SL.act = {}; SL.live = { hello: { server: { addr: '127.0.0.1:6969' }, defaults: {}, ui: {} } };
  SL.ui = { toast: (t, k) => toasts.push([t, k]), confirm: spec => { confirms.push(spec); return spec; }, modal: spec => spec, IC: {} };
  SL.time = { noteKey() {}, T: { wall: 0 } };
  (modules || []).forEach(run);
  return { SL, ctx, toasts, confirms, views, fetch, bus: SL.bus };
}

/** A hostile string: every page must show it as text. */
export const EVIL = '<img src=x onerror="window.__pwn=1"><script>window.__pwn=2</script>"\'&';
/** True when s carries a tag of the hostile string as markup (an element the browser would create) instead of its escaped form. */
export const hasMarkup = s => /<img\s|<script/i.test(s);

/** The attributes of the markup that run script: event handlers (on...), a javascript: URL, srcdoc. Found by reading the tags and their quoted values, so that text inside a value never counts. */
export function scriptAttrs(html) {
  const out = [], tag = /<([a-zA-Z][^\s>\/]*)((?:\s+[^\s"'<>\/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'>]+))?)*)\s*\/?>/g, attr = /\s+([^\s"'<>\/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?/g;
  for (let m; (m = tag.exec(html));) { for (let a; (a = attr.exec(m[2]));) { const name = a[1].toLowerCase(), v = (a[2] || a[3] || a[4] || '').trim().toLowerCase(); if (/^on/.test(name) || name === 'srcdoc' || ((name === 'href' || name === 'src' || name === 'action') && /^javascript:/.test(v))) out.push(m[1] + '[' + name + ']'); } attr.lastIndex = 0; }
  return out;
}
export const tick = ms => new Promise(r => setTimeout(r, ms || 0));
