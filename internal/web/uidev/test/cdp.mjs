// cdp.mjs: a tiny headless-Chromium driver with no dependencies: Node's global WebSocket and the DevTools protocol.
// The browser tests of this directory (*.browser.mjs) use it to open a page, evaluate scripts in it, send keys and clicks, and take
// screenshots; it is not part of `node --test` and not of CI.
//
//   const { open } = await import('./cdp.mjs');
//   const page = await open('http://127.0.0.1:PORT/', { w: 1440, h: 900 });   // a URL or a file path; scale, reduced, query are options
//   await page.eval('document.title'); await page.shot('out.png'); await page.close();
//
// Which Chromium runs: CHROME_PATH when it is set (any Chromium or headless shell binary); otherwise the newest Playwright headless
// shell in ~/.cache/ms-playwright (chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell); otherwise
// `chromium` on the PATH. The browser is started with its own temporary profile, which is removed with it, and is killed when the
// script exits, throws or is interrupted. The hosts of Google Fonts are mapped to an unreachable address, so a page that links them
// fails at once instead of waiting; those failures are collected in `page.fontMisses` and are not counted in `page.errors`.
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
export async function open(file, { w = 1440, h = 900, scale = 1, reduced = false, query = '' } = {}) {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'slcore-'));
  const chrome = spawn(CHROME, ['--headless', '--no-sandbox', '--disable-gpu', '--hide-scrollbars', '--js-flags=--expose-gc', '--host-resolver-rules=MAP fonts.googleapis.com 127.0.0.1, MAP fonts.gstatic.com 127.0.0.1', '--remote-debugging-port=0', `--user-data-dir=${profile}`, '--force-device-scale-factor=' + scale, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });
  process.once('exit', () => { try { chrome.kill('SIGKILL'); } catch (e) { /* gone */ } try { fs.rmSync(profile, { recursive: true, force: true }); } catch (e) { /* busy */ } });   // a script that throws or is killed never leaves a browser behind
  for (const sig of ['SIGINT', 'SIGTERM']) process.once(sig, () => { try { chrome.kill('SIGKILL'); } catch (e) { /* gone */ } process.exit(1); });
  let startTimer; const wsURL = await new Promise((resolve, reject) => { let buf = ''; chrome.stderr.on('data', d => { buf += d; const m = buf.match(/DevTools listening on (ws:\/\/\S+)/); if (m) resolve(m[1]); }); chrome.on('exit', c => reject(new Error('chrome exited ' + c))); startTimer = setTimeout(() => reject(new Error('chrome did not start')), 15000); });
  clearTimeout(startTimer);
  const port = new URL(wsURL).port, targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json(), tab = targets.find(t => t.type === 'page');
  const ws = new WebSocket(tab.webSocketDebuggerUrl); await new Promise((r, j) => { ws.onopen = r; ws.onerror = j; });
  let id = 0; const pending = new Map(), errors = [], logs = [], fontMisses = [], handlers = {};
  // The Google Fonts link cannot load offline (its host is mapped to a dead address so tests do not wait): recorded in fontMisses, not an app error.
  ws.onmessage = ev => { const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { const { res, rej } = pending.get(m.id); pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); return; }
    if (m.method && handlers[m.method]) handlers[m.method].forEach(f => { try { f(m.params); } catch (e) { /* a handler's failure is its own */ } });
    if (m.method === 'Runtime.exceptionThrown') { const d = m.params.exceptionDetails; errors.push('exception: ' + (d.exception?.description || d.text)); }
    if (m.method === 'Runtime.consoleAPICalled') { const t = m.params.type, s = m.params.args.map(a => a.value ?? a.description).join(' '); if (t === 'error' || t === 'assert') errors.push('console.' + t + ': ' + s); else if (t === 'log') logs.push(s); }
    if (m.method === 'Log.entryAdded' && m.params.entry.level === 'error') { const msg = 'log: ' + m.params.entry.text + ' ' + (m.params.entry.url || ''); if (/fonts\.g(oogleapis|static)\.com/.test(msg)) fontMisses.push(msg); else errors.push(msg); } };
  const send = (method, params = {}) => new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
  await send('Page.enable'); await send('Runtime.enable'); await send('Log.enable');
  const viewport = (W, H) => send('Emulation.setDeviceMetricsOverride', { width: W, height: H, deviceScaleFactor: scale, mobile: W < 600 });
  await viewport(w, h);
  if (reduced) await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
  const loaded = new Promise(r => { const hh = ws.onmessage; ws.onmessage = ev => { hh(ev); if (JSON.parse(ev.data).method === 'Page.loadEventFired') r(); }; });
  await send('Page.navigate', { url: (/^[a-z]+:/i.test(file) ? file : pathToFileURL(path.resolve(file)).href) + (query ? '?' + query : '') }); let loadTimer; await Promise.race([loaded, new Promise(r => { loadTimer = setTimeout(r, 20000); })]); clearTimeout(loadTimer); await new Promise(r => setTimeout(r, 600));
  const page = {
    errors, logs, fontMisses, send,
    /** Call fn(params) for every DevTools event `method` (e.g. Fetch.requestPaused after Fetch.enable). */
    on(method, fn) { (handlers[method] = handlers[method] || []).push(fn); },
    async eval(js) { const r = await send('Runtime.evaluate', { expression: js, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('eval: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text) + '\n' + js.slice(0, 200)); return r.result?.value; },
    async shot(file) { const { data } = await send('Page.captureScreenshot', { format: 'png' }); fs.mkdirSync(path.dirname(path.resolve(file)), { recursive: true }); fs.writeFileSync(file, Buffer.from(data, 'base64')); return file; },
    viewport, sleep: ms => new Promise(r => setTimeout(r, ms)),
    async key(k, mods = 0) { const named = { Escape: 27, Enter: 13, Tab: 9, ArrowUp: 38, ArrowDown: 40, ArrowLeft: 37, ArrowRight: 39, Space: 32 }; const vk = named[k] || k.toUpperCase().charCodeAt(0); const base = { modifiers: mods, key: k === 'Space' ? ' ' : k, code: named[k] ? k : (/[a-z]/i.test(k) ? 'Key' + k.toUpperCase() : 'Digit' + k), windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk }; await send('Input.dispatchKeyEvent', { type: 'keyDown', ...base, text: (mods & 6) ? '' : (k.length === 1 ? k : k === 'Enter' ? '\r' : k === 'Space' ? ' ' : '') }); await send('Input.dispatchKeyEvent', { type: 'keyUp', ...base }); },
    async move(x, y) { await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y }); },
    async click(x, y) { await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y }); await send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 }); await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', clickCount: 1 }); },
    async close() { try { ws.close(); } catch (e) { /* closed */ } chrome.kill(); await new Promise(r => { if (chrome.exitCode !== null) r(); else { chrome.on('exit', r); setTimeout(r, 2000); } }); try { fs.rmSync(profile, { recursive: true, force: true }); } catch (e) { /* busy */ } },
  };
  return page;
}
