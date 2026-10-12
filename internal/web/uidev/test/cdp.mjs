// cdp.mjs: a tiny headless-Chromium driver with no dependencies: Node's global WebSocket and the DevTools protocol.
// The browser tests of this directory (*.browser.mjs) use it to open a page, evaluate scripts in it, send keys and clicks, and take
// screenshots; they are not part of `node --test` and not of CI. How it starts a browser and how long it waits for one is tested by
// cdp-watchdog.test.mjs, on a stand-in (fakechrome.mjs).
//
//   const { open } = await import('./cdp.mjs');
//   const page = await open('http://127.0.0.1:PORT/', { w: 1440, h: 900 });   // a URL or a file path; scale, reduced, query are options
//   await page.eval('document.title'); await page.shot('out.png'); await page.close();
//
// Which Chromium runs: the `chrome` option; otherwise CHROME_PATH when it is set (any Chromium or headless shell binary); otherwise the
// newest Playwright headless shell in ~/.cache/ms-playwright (chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell);
// otherwise `chromium` on the PATH. The browser is started with its own temporary profile, which is removed with it, and is killed when
// the script exits, throws or is interrupted. A browser has 60 s (`deadline`) to start: to print its DevTools address, list its page at
// /json/list, accept the WebSocket of that page and answer a first call. One that does not is killed with its profile and another is
// started in its place, three at most (`attempts`); the error names the step the last one stopped at and quotes the end of its stderr.
// A DevTools call fails when it has no answer in 120 s (`callTimeout`), and at once when the browser exits or drops the connection.
// The hosts of Google Fonts are mapped to an unreachable address, so a page that links them fails at once instead of waiting; those
// failures are collected in `page.fontMisses` and are not counted in `page.errors`.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
/** The Chromium to run: CHROME_PATH, the newest Playwright headless shell, or `chromium` on the PATH. */
function chromeBinary() {
  if (process.env.CHROME_PATH) return process.env.CHROME_PATH;
  const cache = path.join(os.homedir(), '.cache/ms-playwright');
  try {
    for (const d of fs.readdirSync(cache).filter(n => n.startsWith('chromium_headless_shell-')).sort().reverse()) {
      const bin = path.join(cache, d, 'chrome-headless-shell-linux64/chrome-headless-shell');
      if (fs.existsSync(bin)) return bin;
    }
  } catch { /* no Playwright cache */ }
  return 'chromium';
}
const CHROME = chromeBinary();
process.setMaxListeners(0);
const sleep = ms => new Promise(r => setTimeout(r, ms));

/**
 * start runs one browser and connects to its page. Within `deadline` ms the browser must print its DevTools address, list its targets at
 * /json/list, accept the WebSocket of its page and answer Page.enable; a browser that misses the deadline, exits or refuses is killed and
 * its profile removed, and the error names the step it stopped at and ends with the last lines of its stderr. A start that failed leaves
 * no browser running and no listener on this process. It resolves to { send, quit }: send(method, params) is a DevTools call, which fails
 * when it has no answer in `callTimeout` ms and at once when the browser exits or drops the connection; quit kills the browser and removes
 * its profile. The page's events go to onEvent.
 */
async function start(bin, { scale, deadline, callTimeout, onEvent }) {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'slcore-'));
  const chrome = spawn(bin, ['--headless', '--no-sandbox', '--disable-gpu', '--hide-scrollbars', '--js-flags=--expose-gc', '--host-resolver-rules=MAP fonts.googleapis.com 127.0.0.1, MAP fonts.gstatic.com 127.0.0.1', '--remote-debugging-port=0', `--user-data-dir=${profile}`, '--force-device-scale-factor=' + scale, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });
  let ws = null, id = 0, dead = '';
  const pending = new Map();
  /** fail ends every call that waits, and every later one, with why */
  const fail = why => { if (dead) return; dead = why; for (const [i, { rej }] of [...pending]) { pending.delete(i); rej(new Error(why)); } };
  const send = (method, params = {}) => new Promise((res, rej) => {
    if (dead) return rej(new Error(dead));
    const i = ++id, t = setTimeout(() => { pending.delete(i); rej(new Error('no answer to ' + method + ' in ' + callTimeout / 1000 + ' s: the browser is stuck')); }, callTimeout);
    pending.set(i, { res: v => { clearTimeout(t); res(v); }, rej: e => { clearTimeout(t); rej(e); } });
    ws.send(JSON.stringify({ id: i, method, params }));
  });
  // however this process ends (an exception, a closed pipe, a signal), no browser is left running and no profile behind
  const leave = () => { try { chrome.kill('SIGKILL'); } catch (e) { /* gone */ } try { fs.rmSync(profile, { recursive: true, force: true }); } catch (e) { /* busy */ } };
  const interrupted = () => process.exit(1);   // whose exit listeners kill the browsers
  process.once('exit', leave); process.once('SIGINT', interrupted); process.once('SIGTERM', interrupted);
  const quit = async () => {
    fail('the browser was closed');
    process.removeListener('exit', leave); process.removeListener('SIGINT', interrupted); process.removeListener('SIGTERM', interrupted);
    try { ws?.close(); } catch (e) { /* closed */ }
    try { chrome.kill('SIGKILL'); } catch (e) { /* gone */ }
    // wait for the exit at most 2 s, and no longer than the exit takes: a timer left running would keep the script up for the rest of the 2 s
    await new Promise(r => { if (chrome.pid === undefined || chrome.exitCode !== null || chrome.signalCode !== null) return r(); const t = setTimeout(r, 2000); chrome.once('exit', () => { clearTimeout(t); r(); }); });
    for (let i = 0; i < 5; i++) { try { fs.rmSync(profile, { recursive: true, force: true }); break; } catch (e) { await sleep(200); } }
  };
  let step = 'the line "DevTools listening on" on its stderr', err = '', timer;
  chrome.stderr.setEncoding('utf8');   // a character split between two chunks is decoded whole, in the error's quote too
  const listening = new Promise(resolve => {
    chrome.stderr.on('data', d => { err = (err + d).slice(-4000); const m = err.match(/DevTools listening on (ws:\/\/\S+)/); if (m) resolve(m[1]); });
  });
  const failed = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error('nothing came in ' + deadline / 1000 + ' s')), deadline);
    chrome.on('error', e => reject(new Error('it could not be run: ' + e.message)));
    chrome.once('exit', (c, sig) => reject(new Error('it exited (' + (sig || c) + ')')));
  });
  failed.catch(() => { /* read by the races below; once the browser is up, only its exit rejects this, and nobody waits for it */ });
  const listing = new AbortController();
  try {
    const list = 'http://127.0.0.1:' + new URL(await Promise.race([listening, failed])).port + '/json/list';
    step = 'the list of its targets at ' + list;
    const targets = await Promise.race([fetch(list, { signal: listing.signal }).then(r => { if (!r.ok) throw new Error('it answered ' + r.status); return r.json(); }, e => { throw new Error('the request failed: ' + (e.cause?.message || e.message)); }), failed]);
    const tab = Array.isArray(targets) && targets.find(t => t.type === 'page');
    if (!tab) throw new Error('it listed no page');
    step = 'the WebSocket handshake of ' + tab.webSocketDebuggerUrl;
    ws = new WebSocket(tab.webSocketDebuggerUrl);
    await Promise.race([new Promise((r, j) => { ws.onopen = r; ws.onerror = () => j(new Error('the connection failed')); }), failed]);
    ws.onmessage = ev => { const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { const { res, rej } = pending.get(m.id); pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); return; }
      onEvent(m); };
    // a browser that dies or drops the connection ends every call that waits for it, with the reason, instead of leaving them to wait
    ws.onclose = () => fail('the DevTools connection closed');
    chrome.on('exit', (c, sig) => fail('chrome exited (' + (sig || c) + ')'));
    step = 'the answer to Page.enable';
    await Promise.race([send('Page.enable'), failed]);
    return { send, quit };
  } catch (e) {
    const running = chrome.pid !== undefined && chrome.exitCode === null && chrome.signalCode === null;
    listing.abort();
    await quit();
    const tail = err.trim().split('\n').slice(-5).join('\n');
    throw new Error('chrome did not start: ' + e.message + ' while waiting for ' + step + ' (' + bin + (running ? '; it was killed' : '') + ')' + (tail ? '; the end of its stderr:\n' + tail : '; it wrote nothing to stderr'));
  } finally { clearTimeout(timer); }
}

/**
 * open starts a browser (see start) and loads `file`, a URL or a file path, in its page. A browser that does not start has been killed
 * with its profile, and another is started in its place, up to `attempts` browsers; each failed attempt is noted on stderr, and the last
 * one's error is thrown. A failure after the start, such as a call that is never answered, kills the browser too.
 */
export async function open(file, { w = 1440, h = 900, scale = 1, reduced = false, query = '', chrome = CHROME, attempts = 3, deadline = 60000, callTimeout = 120000 } = {}) {
  const errors = [], logs = [], fontMisses = [], handlers = {};
  let loaded = () => {};
  // The Google Fonts link cannot load offline (its host is mapped to a dead address so tests do not wait): recorded in fontMisses, not an app error.
  const onEvent = m => {
    if (m.method && handlers[m.method]) handlers[m.method].forEach(f => { try { f(m.params); } catch (e) { /* a handler's failure is its own */ } });
    if (m.method === 'Page.loadEventFired') loaded();
    if (m.method === 'Runtime.exceptionThrown') { const d = m.params.exceptionDetails; errors.push('exception: ' + (d.exception?.description || d.text)); }
    if (m.method === 'Runtime.consoleAPICalled') { const t = m.params.type, s = m.params.args.map(a => a.value ?? a.description).join(' '); if (t === 'error' || t === 'assert') errors.push('console.' + t + ': ' + s); else if (t === 'log') logs.push(s); }
    if (m.method === 'Log.entryAdded' && m.params.entry.level === 'error') { const msg = 'log: ' + m.params.entry.text + ' ' + (m.params.entry.url || ''); if (/fonts\.g(oogleapis|static)\.com/.test(msg)) fontMisses.push(msg); else errors.push(msg); } };
  let b = null;
  for (let i = 1; !b; i++) {
    try { b = await start(chrome, { scale, deadline, callTimeout, onEvent }); } catch (e) {
      if (i >= attempts) throw e;
      process.stderr.write('chrome attempt ' + i + ' of ' + attempts + ' failed, starting another: ' + e.message.split('\n')[0] + '\n');
    }
  }
  const { send, quit } = b;
  const viewport = (W, H) => send('Emulation.setDeviceMetricsOverride', { width: W, height: H, deviceScaleFactor: scale, mobile: W < 600 });
  try {
    await send('Runtime.enable'); await send('Log.enable');
    await viewport(w, h);
    if (reduced) await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
    const load = new Promise(r => { loaded = r; });
    await send('Page.navigate', { url: (/^[a-z]+:/i.test(file) ? file : pathToFileURL(path.resolve(file)).href) + (query ? '?' + query : '') }); let loadTimer; await Promise.race([load, new Promise(r => { loadTimer = setTimeout(r, 20000); })]); clearTimeout(loadTimer); await sleep(600);
  } catch (e) { await quit(); throw e; }
  const page = {
    errors, logs, fontMisses, send,
    /** Call fn(params) for every DevTools event `method` (e.g. Fetch.requestPaused after Fetch.enable). */
    on(method, fn) { (handlers[method] = handlers[method] || []).push(fn); },
    async eval(js) { const r = await send('Runtime.evaluate', { expression: js, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('eval: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text) + '\n' + js.slice(0, 200)); return r.result?.value; },
    async shot(file) { const { data } = await send('Page.captureScreenshot', { format: 'png' }); fs.mkdirSync(path.dirname(path.resolve(file)), { recursive: true }); fs.writeFileSync(file, Buffer.from(data, 'base64')); return file; },
    viewport, sleep,
    async key(k, mods = 0) { const named = { Escape: 27, Enter: 13, Tab: 9, ArrowUp: 38, ArrowDown: 40, ArrowLeft: 37, ArrowRight: 39, Space: 32 }; const vk = named[k] || k.toUpperCase().charCodeAt(0); const base = { modifiers: mods, key: k === 'Space' ? ' ' : k, code: named[k] ? k : (/[a-z]/i.test(k) ? 'Key' + k.toUpperCase() : 'Digit' + k), windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk }; await send('Input.dispatchKeyEvent', { type: 'keyDown', ...base, text: (mods & 6) ? '' : (k.length === 1 ? k : k === 'Enter' ? '\r' : k === 'Space' ? ' ' : '') }); await send('Input.dispatchKeyEvent', { type: 'keyUp', ...base }); },
    async move(x, y) { await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y }); },
    async click(x, y) { await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y }); await send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 }); await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', clickCount: 1 }); },
    /** close kills the browser, which need not heed SIGTERM, and removes its profile. */
    close: quit,
  };
  return page;
}
