/* api.js: SL.api, the page's only door to the server (docs/WEB-API.md documents the routes, the confirmation protocol, the error body
 * and the stream). Every request goes to this origin under /api/ with the session cookie; a request that changes something carries
 * the X-Sleipnir-Web header (and, with a body, JSON). A call never throws: it resolves {ok: true, status, data} or
 * {ok: false, status, code, message, detail}, where message is the server's sentence (safe to show in a toast). Nothing here logs a
 * request body, a confirmation id or a response body.
 *
 * Confirmations. The server decides what raises privilege, on the settings that will take effect, and answers such a request
 * 428 confirm_required with the scope to confirm (X-Confirm-Scope, and detail {scope, reasons}). Any request can get that answer:
 * the page shows the person what is raised (cfg.askConfirm, set by the shell) and, on yes, sends the SAME request once more with a
 * single-use id for exactly that scope; on no it resolves the 428 with `declined: true` and nothing is toasted. The page never computes
 * those scopes itself. A caller that already asked the person (a typed mode name, a restore) passes its scope or id and is not asked
 * again; when that id is not good for what the server wants (403 confirm_invalid), the server names the scope and the question above
 * is asked once. */
(function (SL) {
  'use strict';
  const G = typeof window !== 'undefined' ? window : globalThis;
  const HDR = 'X-Sleipnir-Web';
  const CONFIRM = 'X-Confirm';
  const NET = 'the server is not reachable';
  /** Defaults a test may change (SL.api.cfg): request timeout, the longer one for snapshots and diffs, the 429 retry cap, the
   *  stream's reopen delay, and what a 401 does (the server answers the reloaded page with its sign-in page). */
  const cfg = {
    timeoutMs: 30000, longMs: 60000, retryCapMs: 5000, reopenMs: 2000,
    reload() { try { G.location.reload(); } catch (e) { /* no page to reload (tests) */ } },
    toast(text, kind) { if (SL.ui && SL.ui.toast) SL.ui.toast(text, kind); },
    /** askConfirm({scope, reasons, message, method, path}) -> Promise<boolean>: the person's answer to what a request raises. Set by
     *  the shell; without it a 428 is returned as it came. */
    askConfirm: null,
  };
  let reachable = true;
  let cidN = 0;
  const bus = SL.bus;

  /** Only paths under /api/ are ever requested. */
  const checkPath = p => typeof p === 'string' && p.indexOf('/api/') === 0 && p.indexOf('//') < 0;
  const slow = p => /\/snapshot$|\/ws\/(diff|index)\b/.test(p.split('?')[0]);
  const sleep = ms => new Promise(r => setTimeout(r, ms));

  /** The server became (un)reachable: live.js shows the disconnected state and comes back on its own. */
  const setReachable = on => {
    if (reachable === on) return;
    reachable = on;
    bus.emit(on ? 'api-online' : 'api-offline');
  };

  /** Turns an error response into the failure shape. The body is {error, code, detail?}; anything else gets a generic sentence. */
  const failure = async (res) => {
    let body = null;
    try { body = await res.json(); } catch (e) { body = null; }
    const out = { ok: false, status: res.status, code: (body && body.code) || 'http_' + res.status,
      message: (body && typeof body.error === 'string' && body.error) || 'the server answered ' + res.status, detail: body && body.detail };
    if (res.status === 428) out.scope = res.headers.get('X-Confirm-Scope') || '';
    if (res.status === 429) out.retryAfter = Number(res.headers.get('Retry-After')) || 1;
    return out;
  };

  /**
   * One request. opts: confirm (a scope the caller already asked the person about: the id is obtained right before the request, and
   * once more if the server says it is no longer valid), confirmId (an id the server issued already, e.g. a trust challenge), timeout
   * (ms), signal (an AbortSignal of the caller), noRetry (no 429 retry for a GET), noAsk (return a 428 without asking), exact (with
   * confirm: the person confirmed exactly that scope, for what the page showed; when the server wants another one, or no longer takes
   * a fresh id for it, the request returns {code: 'scope_changed', scope (the server's, when it named one), expected} and never asks:
   * the caller shows the thing again as it is now and asks again).
   */
  async function request(method, path, body, opts) {
    opts = opts || {};
    if (!checkPath(path)) return { ok: false, status: 0, code: 'bad_path', message: 'not an API path' };
    const headers = {};
    const init = { method, credentials: 'same-origin', cache: 'no-store', headers };
    if (method !== 'GET' && method !== 'HEAD') {
      headers[HDR] = '1';
      if (body !== undefined) { headers['Content-Type'] = 'application/json'; init.body = JSON.stringify(body); }
    }
    let confirmed = opts.confirmId || '', scope = opts.confirm || '', asked = false, renewed = false;
    const changed = (f, sc) => ({ ok: false, status: f.status, code: 'scope_changed', scope: sc, expected: opts.confirm || '', detail: f.detail,
      message: 'what this confirms has changed since it was shown: look at it again, then confirm again' });
    const noId = { ok: false, status: 0, code: 'confirm_failed', message: 'the server did not issue a confirmation' };
    if (opts.confirm && !confirmed) {
      confirmed = await confirm(opts.confirm);
      if (!confirmed) return noId;
    }
    for (let attempt = 0; attempt < 6; attempt++) {
      if (confirmed) headers[CONFIRM] = confirmed; else delete headers[CONFIRM];
      const ctl = typeof AbortController !== 'undefined' ? new AbortController() : null;
      let timedOut = false;
      const limit = opts.timeout || (slow(path) ? cfg.longMs : cfg.timeoutMs);
      const timer = ctl ? setTimeout(() => { timedOut = true; ctl.abort(); }, limit) : null;
      let unlink = null;
      if (ctl && opts.signal) {
        if (opts.signal.aborted) ctl.abort();
        else { const f = () => ctl.abort(); opts.signal.addEventListener('abort', f); unlink = () => opts.signal.removeEventListener('abort', f); }
      }
      if (ctl) init.signal = ctl.signal;
      let res;
      try {
        res = await G.fetch(path, init);
      } catch (e) {
        if (timer) clearTimeout(timer);
        if (unlink) unlink();
        if (opts.signal && opts.signal.aborted) return { ok: false, status: 0, code: 'aborted', message: 'canceled' };
        if (timedOut) return { ok: false, status: 0, code: 'timeout', message: 'the server did not answer in time' };
        setReachable(false);
        return { ok: false, status: 0, code: 'network', message: NET };
      }
      if (timer) clearTimeout(timer);
      if (unlink) unlink();
      setReachable(true);
      if (res.ok) {
        let data = null;
        if (res.status !== 204) { try { data = await res.json(); } catch (e) { data = null; } }
        return { ok: true, status: res.status, data };
      }
      const f = await failure(res);
      if (res.status === 401) { cfg.reload(); return f; }
      if (res.status === 403 && f.code === 'confirm_invalid' && confirmed) {
        // An id expires after a minute: one fresh id for the same scope; then (an id for something else) the server names its scope.
        if (scope && !renewed) { renewed = true; confirmed = await confirm(scope); if (confirmed) continue; return noId; }
        if (opts.exact) return changed(f, '');
        if (!asked) { confirmed = ''; scope = ''; continue; }
        return f;
      }
      if (res.status === 428 && f.code === 'confirm_required') {
        const sc = f.scope || (f.detail && typeof f.detail.scope === 'string' ? f.detail.scope : '');
        if (opts.exact) return sc && sc !== opts.confirm ? changed(f, sc) : f;
        if (asked || !sc || opts.noAsk || typeof cfg.askConfirm !== 'function') return f;
        asked = true;
        let yes = false;
        try { yes = await cfg.askConfirm({ scope: sc, reasons: (f.detail && Array.isArray(f.detail.reasons) ? f.detail.reasons : []).map(String), message: f.message, method, path }); } catch (e) { yes = false; }
        if (!yes) { f.declined = true; return f; }
        scope = sc; renewed = false; confirmed = await confirm(sc);
        if (!confirmed) return noId;
        continue;
      }
      if (res.status === 429 && method === 'GET' && !opts.noRetry && attempt === 0) {
        await sleep(Math.min(cfg.retryCapMs, f.retryAfter * 1000));
        continue;
      }
      return f;
    }
    return { ok: false, status: 0, code: 'gave_up', message: 'the request did not go through' };
  }

  /** A single-use confirmation id for scope, or null. */
  async function confirm(scope) {
    const r = await request('POST', '/api/confirm', { scope: String(scope) }, { noRetry: true });
    return r.ok && r.data && typeof r.data.id === 'string' ? r.data.id : null;
  }

  /** The canonical JSON the server digests: sorted object keys, no spaces, and Go's escaping of < > & U+2028 U+2029. */
  function canonical(v) {
    const walk = x => {
      if (Array.isArray(x)) return x.map(walk);
      if (x && typeof x === 'object') { const o = {}; Object.keys(x).sort().forEach(k => { if (x[k] !== undefined) o[k] = walk(x[k]); }); return o; }
      return x;
    };
    return JSON.stringify(walk(v)).replace(/[<>&\u2028\u2029]/g, c => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
  }

  /** d16(values): the first 16 hex characters of SHA-256 over the canonical JSON (the digest part of a confirmation scope). */
  async function d16(v) {
    const bytes = new TextEncoder().encode(canonical(v));
    const sum = await G.crypto.subtle.digest('SHA-256', bytes);
    return Array.from(new Uint8Array(sum)).map(b => b.toString(16).padStart(2, '0')).join('').slice(0, 16);
  }

  /** A client id for a request that creates something (a repeat within 60 s returns the first result). */
  const cid = () => 'c' + (++cidN);

  /**
   * The page's one stream: EventSource on /api/stream?after=N. handlers: frame(type, data, id) for every named
   * frame, state('open' | 'reconnecting' | 'closed'), and beforeReopen() (async, may resolve false to stay closed) which runs before
   * the stream is opened again after the browser gave up on it (a refused or broken response). The browser's own reconnects carry
   * Last-Event-ID; a reopen carries ?after= the last id seen. Returns {close(), lastId()}.
   */
  const FRAMES = ['ev', 'meta', 'roster', 'tab', 'reset', 'recorded', 'run', 'toast', 'ping', 'bye', 'gap', 'lagged', 'closed', 'hello'];
  function stream(after, handlers) {
    handlers = handlers || {};
    let es = null, last = Number(after) || 0, stopped = false, timer = null, state = '';
    const setState = s => { if (s !== state) { state = s; if (handlers.state) handlers.state(s); } };
    const open = () => {
      if (stopped) return;
      es = new G.EventSource('/api/stream?after=' + encodeURIComponent(String(last)));
      es.onopen = () => { setReachable(true); setState('open'); };
      es.onerror = () => {
        if (stopped) return;
        if (es && es.readyState === 2) { es = null; setState('reconnecting'); schedule(); }
        else setState('reconnecting');
      };
      FRAMES.forEach(type => es.addEventListener(type, ev => {
        const id = Number(ev.lastEventId);
        if (id > last) last = id;
        let data = null;
        try { data = ev.data ? JSON.parse(ev.data) : {}; } catch (e) { return; }
        if (handlers.frame) handlers.frame(type, data, id);
      }));
    };
    const schedule = () => {
      if (stopped || timer) return;
      timer = setTimeout(async () => {
        timer = null;
        if (stopped) return;
        let go = true;
        if (handlers.beforeReopen) { try { go = await handlers.beforeReopen(); } catch (e) { go = true; } }
        if (go === false) { setState('closed'); return; }
        if (go === 'later') { schedule(); return; }
        open();
      }, cfg.reopenMs);
    };
    open();
    return {
      close() { stopped = true; if (timer) clearTimeout(timer); timer = null; if (es) es.close(); es = null; setState('closed'); },
      lastId: () => last,
      state: () => state
    };
  }

  SL.api = {
    cfg,
    get: (path, opts) => request('GET', path, undefined, opts),
    post: (path, body, opts) => request('POST', path, body, opts),
    put: (path, body, opts) => request('PUT', path, body, opts),
    patch: (path, body, opts) => request('PATCH', path, body, opts),
    del: (path, body, opts) => request('DELETE', path, body, opts),
    request, confirm, d16, canonical, cid, stream,
    /** Whether the last request reached the server. */
    reachable: () => reachable,
    /** Encodes a path segment (ids are server-made, but every string is data). */
    seg: s => encodeURIComponent(String(s == null ? '' : s)),
    /** The tab's route prefix. */
    tab: id => '/api/sessions/' + encodeURIComponent(String(id))
  };
})(SL);
