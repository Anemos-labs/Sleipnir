// harness.mjs: loads the page's classic scripts into a Node vm context with the few browser globals they touch, so that the data
// layer (api.js, 30-model.js, 50-sessions.js, live.js) can be tested with `node --test` and no dependency. Not a DOM: modules that
// render are loaded only by the browser suite.
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';
import { webcrypto } from 'node:crypto';

export const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'ui');
/** The scripts under test: ui/js, or SL_UI_JS (another copy of them, e.g. an older revision to show that a test fails there). */
export const JS = process.env.SL_UI_JS || path.join(ROOT, 'js');

/** A fetch fake: routes is a function (method, path, init) -> {status, body, headers} or a Promise of it; calls are recorded. */
export function fakeFetch(routes) {
  const calls = [];
  const fn = async (p, init) => {
    init = init || {};
    const call = { method: init.method || 'GET', path: p, headers: { ...(init.headers || {}) }, body: init.body };
    calls.push(call);
    const r = await routes(call.method, p, init, call);
    if (r === 'network') throw new TypeError('Failed to fetch');
    if (r === 'hang') {
      return new Promise((resolve, reject) => {
        if (!init.signal) return;
        if (init.signal.aborted) reject(new DOMException('aborted', 'AbortError'));
        else init.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
      });
    }
    const headers = new Map(Object.entries(r.headers || {}).map(([k, v]) => [k.toLowerCase(), String(v)]));
    const text = r.body === undefined ? '' : typeof r.body === 'string' ? r.body : JSON.stringify(r.body);
    return {
      ok: r.status >= 200 && r.status < 300, status: r.status,
      headers: { get: k => headers.has(k.toLowerCase()) ? headers.get(k.toLowerCase()) : null },
      json: async () => JSON.parse(text)
    };
  };
  fn.calls = calls;
  return fn;
}

/** An EventSource fake: instances are kept in FakeES.all; a test pushes frames with emit(type, data, id) and errors with fail(). */
export class FakeES {
  constructor(url) { this.url = url; this.readyState = 0; this.listeners = {}; FakeES.all.push(this); }
  addEventListener(t, f) { (this.listeners[t] = this.listeners[t] || []).push(f); }
  open() { this.readyState = 1; if (this.onopen) this.onopen({}); }
  emit(type, data, id) { (this.listeners[type] || []).forEach(f => f({ data: JSON.stringify(data), lastEventId: String(id || '') })); }
  fail(closed) { this.readyState = closed ? 2 : 0; if (this.onerror) this.onerror({}); }
  close() { this.readyState = 2; this.closed = true; }
}
FakeES.all = [];

/** Builds a context: window === globalThis of the context, with fetch, EventSource, crypto, timers, a location whose reload is
 *  counted, and an empty localStorage. extra adds globals. */
export function context(extra) {
  const reloads = { n: 0 };
  const store = new Map();
  const ctx = {
    console, setTimeout, clearTimeout, setInterval, clearInterval, Promise, TextEncoder, AbortController, DOMException,
    URLSearchParams, URL, crypto: webcrypto, performance,
    location: { reload() { reloads.n++; }, search: '', href: 'http://127.0.0.1:6969/' },
    localStorage: { getItem: k => store.has(k) ? store.get(k) : null, setItem: (k, v) => store.set(k, String(v)), removeItem: k => store.delete(k) },
    EventSource: FakeES,
    ...(extra || {})
  };
  ctx.window = ctx;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  ctx.reloads = reloads;
  return ctx;
}

/** Runs the named modules of ui/js (or absolute paths) in ctx, in order. */
export function load(ctx, names) {
  for (const n of names) {
    const file = path.isAbsolute(n) ? n : path.join(JS, n);
    vm.runInContext(fs.readFileSync(file, 'utf8'), ctx, { filename: file });
  }
  return ctx;
}

export const tick = (ms) => new Promise(r => setTimeout(r, ms || 0));

/** A copy made of this realm's objects (values built inside the vm context have other prototypes). */
export const plain = v => v === undefined ? v : JSON.parse(JSON.stringify(v));
