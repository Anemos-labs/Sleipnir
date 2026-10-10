#!/usr/bin/env node
// shot.mjs: drive a page in headless Chromium (no dependencies; Node 22's global WebSocket + the Chrome DevTools Protocol).
//
//   node shot.mjs PAGE [--w 1440] [--h 900] [--scale 1] [--dark|--light] [--step STEP]...
//
// PAGE is a file path or a URL. Steps run in order, each is KIND:ARG
//   shot:OUT.png        screenshot of the viewport          full:OUT.png   screenshot of the whole page
//   wait:MS             sleep                                click:CSS      el.click() on the first match
//   clickxy:X,Y         mouse click at viewport coordinates  hover:X,Y      move the mouse
//   key:ctrl+k          a key chord (ctrl|shift|alt|meta + a letter or Escape Enter Tab ArrowUp/Down/Left/Right Backspace Space)
//   type:TEXT           insert text into the focused element  eval:JS       run JS in the page (printed if it returns a value)
//   size:W,H            resize the viewport
// Console errors, uncaught exceptions and failed loads are printed at the end; exit status 1 if there were any.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

const CHROME = process.env.CHROME_PATH ||
  path.join(os.homedir(), '.cache/ms-playwright/chromium_headless_shell-1234/chrome-headless-shell-linux64/chrome-headless-shell');

const argv = process.argv.slice(2);
const opt = { w: 1440, h: 900, scale: 1, scheme: null, steps: [] };
let page = null;
for (let i = 0; i < argv.length; i++) {
  const a = argv[i];
  if (a === '--w') opt.w = +argv[++i];
  else if (a === '--h') opt.h = +argv[++i];
  else if (a === '--scale') opt.scale = +argv[++i];
  else if (a === '--dark') opt.scheme = 'dark';
  else if (a === '--light') opt.scheme = 'light';
  else if (a === '--step') opt.steps.push(argv[++i]);
  else page = a;
}
if (!page) { console.error('usage: node shot.mjs PAGE [--w N --h N --scale N --dark|--light] --step shot:out.png ...'); process.exit(2); }
const url = /^[a-z]+:/i.test(page) ? page : pathToFileURL(path.resolve(page)).href;

const here = path.dirname(new URL(import.meta.url).pathname);
fs.mkdirSync(path.join(here, '.profiles'), { recursive: true });
const profile = fs.mkdtempSync(path.join(here, '.profiles', 'p-'));
const chrome = spawn(CHROME, ['--headless', '--no-sandbox', '--disable-gpu', '--hide-scrollbars', '--remote-debugging-port=0',
  `--user-data-dir=${profile}`, '--force-device-scale-factor=' + opt.scale, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });

const wsURL = await new Promise((resolve, reject) => {
  let buf = '';
  chrome.stderr.on('data', d => { buf += d; const m = buf.match(/DevTools listening on (ws:\/\/\S+)/); if (m) resolve(m[1]); });
  chrome.on('exit', c => reject(new Error('chrome exited early (' + c + '): ' + buf.slice(-400))));
  setTimeout(() => reject(new Error('chrome did not start: ' + buf.slice(-400))), 15000);
});
const port = new URL(wsURL).port;
const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
const tab = targets.find(t => t.type === 'page');
const ws = new WebSocket(tab.webSocketDebuggerUrl);
await new Promise((r, j) => { ws.onopen = r; ws.onerror = j; });

let id = 0; const pending = new Map(); const problems = []; const logs = [];
ws.onmessage = ev => {
  const m = JSON.parse(ev.data);
  if (m.id && pending.has(m.id)) { const { res, rej } = pending.get(m.id); pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); return; }
  if (m.method === 'Runtime.exceptionThrown') { const d = m.params.exceptionDetails; problems.push('exception: ' + (d.exception?.description || d.text) + ` (line ${d.lineNumber + 1})`); }
  if (m.method === 'Runtime.consoleAPICalled' && ['error', 'assert'].includes(m.params.type)) problems.push('console.' + m.params.type + ': ' + m.params.args.map(a => a.value ?? a.description).join(' '));
  if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'log') logs.push(m.params.args.map(a => a.value ?? a.description).join(' '));
  if (m.method === 'Network.loadingFailed' && !m.params.canceled) problems.push('load failed: ' + m.params.errorText + ' ' + (m.params.blockedReason || ''));
  if (m.method === 'Log.entryAdded' && m.params.entry.level === 'error') problems.push('log: ' + m.params.entry.text + ' ' + (m.params.entry.url || ''));
};
const send = (method, params = {}) => new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
const sleep = ms => new Promise(r => setTimeout(r, ms));

await send('Page.enable'); await send('Runtime.enable'); await send('Network.enable'); await send('Log.enable');
const viewport = (w, h) => send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: opt.scale, mobile: w < 600 });
await viewport(opt.w, opt.h);
if (opt.scheme) await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: opt.scheme }] });
const loaded = new Promise(r => { const t = setInterval(() => {}, 1e9); const h = ws.onmessage; ws.onmessage = ev => { h(ev); if (JSON.parse(ev.data).method === 'Page.loadEventFired') { clearInterval(t); r(); } }; });
await send('Page.navigate', { url });
await Promise.race([loaded, sleep(20000)]);
await sleep(400);

const KEYS = { Escape: [27, 'Escape'], Enter: [13, 'Enter'], Tab: [9, 'Tab'], ArrowUp: [38, 'ArrowUp'], ArrowDown: [40, 'ArrowDown'], ArrowLeft: [37, 'ArrowLeft'], ArrowRight: [39, 'ArrowRight'], Backspace: [8, 'Backspace'], Space: [32, ' '] };
async function key(chord) {
  const parts = chord.split('+'); const k = parts.pop(); let mod = 0;
  for (const p of parts) mod |= { alt: 1, ctrl: 2, meta: 4, cmd: 4, shift: 8 }[p.toLowerCase()] || 0;
  const named = KEYS[k]; const ch = k.length === 1 ? k : '';
  const vk = named ? named[0] : k.toUpperCase().charCodeAt(0);
  const base = { modifiers: mod, key: named ? named[1] : k, code: named ? (k === 'Space' ? 'Space' : k) : (/[a-z]/i.test(k) ? 'Key' + k.toUpperCase() : 'Digit' + k), windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk };
  await send('Input.dispatchKeyEvent', { type: 'keyDown', ...base, text: mod & 6 ? '' : (ch || (k === 'Enter' ? '\r' : '')) });
  await send('Input.dispatchKeyEvent', { type: 'keyUp', ...base });
}
const evalJS = async js => { const r = await send('Runtime.evaluate', { expression: js, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) problems.push('eval: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text)); return r.result?.value; };

for (const step of opt.steps) {
  const c = step.indexOf(':'); const kind = step.slice(0, c); const arg = step.slice(c + 1);
  if (kind === 'wait') await sleep(+arg);
  else if (kind === 'size') { const [w, h] = arg.split(',').map(Number); await viewport(w, h); await sleep(250); }
  else if (kind === 'click') { const ok = await evalJS(`(()=>{const e=document.querySelector(${JSON.stringify(arg)});if(!e)return false;e.click();return true})()`); if (!ok) problems.push('click: no element matches ' + arg); await sleep(250); }
  else if (kind === 'clickxy') { const [x, y] = arg.split(',').map(Number); for (const t of ['mousePressed', 'mouseReleased']) await send('Input.dispatchMouseEvent', { type: t, x, y, button: 'left', clickCount: 1 }); await sleep(250); }
  else if (kind === 'hover') { const [x, y] = arg.split(',').map(Number); await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y }); await sleep(250); }
  else if (kind === 'key') { await key(arg); await sleep(250); }
  else if (kind === 'type') { await send('Input.insertText', { text: arg }); await sleep(200); }
  else if (kind === 'eval') { const v = await evalJS(arg); if (v !== undefined) console.log('eval →', JSON.stringify(v)); await sleep(150); }
  else if (kind === 'shot' || kind === 'full') {
    let clip;
    if (kind === 'full') { const m = await send('Page.getLayoutMetrics'); const s = m.cssContentSize || m.contentSize; clip = { x: 0, y: 0, width: s.width, height: s.height, scale: 1 }; }
    const { data } = await send('Page.captureScreenshot', { format: 'png', ...(clip ? { clip, captureBeyondViewport: true } : {}) });
    fs.mkdirSync(path.dirname(path.resolve(arg)), { recursive: true });
    fs.writeFileSync(arg, Buffer.from(data, 'base64')); console.log('wrote', arg);
  } else { console.error('unknown step', step); process.exitCode = 2; }
}
if (logs.length) console.log('console.log:\n  ' + logs.slice(-20).join('\n  '));
ws.close(); chrome.kill();
await new Promise(r => { if (chrome.exitCode !== null) r(); else { chrome.on('exit', r); setTimeout(r, 3000); } });
for (let n = 0; n < 5; n++) { try { fs.rmSync(profile, { recursive: true, force: true }); break; } catch { await sleep(300); } }
if (problems.length) { console.error('PROBLEMS:\n  ' + [...new Set(problems)].join('\n  ')); process.exitCode = 1; } else console.log('no console errors');
process.exit();
