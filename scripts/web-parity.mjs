#!/usr/bin/env node
// web-parity.mjs: numeric screenshot parity of two pages of the Sleipnir web UI, with no dependencies.
//
//   node scripts/web-parity.mjs [--a MOCK.html|URL] [--b DIR|URL] [--scenes scripts/web-parity-scenes.json] [--out dist/web-parity]
//                               [--only NAME,NAME] [--viewport 1440x900] [--threshold PCT] [--tolerance N] [--profile NAME]
//                               [--self] [--jobs N] [--images diff|all|none] [--json FILE] [--list]
//
// A is the approved single-file mock (default docs/design/web-mocks/v3/sleipnir-web.html), B is the page under test: a URL, or a
// directory that is served by scripts/web-ui-dev.mjs (default internal/web/ui). Every scene opens both pages in a fresh browser
// context, runs the same steps in each (clicks, keys, hovers, waits, JS), takes a screenshot of each, decodes the two PNGs itself
// and reports, per scene and viewport: the percentage of pixels that differ, the bounding boxes of the differing regions, and a
// diff image (the page dimmed, every differing pixel red, every region outlined). The exit status is 1 when a scene is above its
// threshold (default 0.5 %), when a page threw, logged a console error, or (for B) broke the Content-Security-Policy.
//
// DETERMINISM. The mock is a simulation: its clock is requestAnimationFrame. Both pages are brought to the same state by a virtual
// clock that is injected before any page script runs (Page.addScriptToEvaluateOnNewDocument) and replaces setTimeout, setInterval,
// requestAnimationFrame, performance.now, Date and Math.random. Time stands still until a step says `wait`: the driver then runs
// the page's own timers and animation frames in order, 1/60 s of virtual time per frame, as fast as the CPU allows. Both pages
// therefore see the same sequence of frame timestamps and the simulation reaches the same state at the same virtual second,
// whatever the machine load; the shipped mock needs no test hook for this. What the clock cannot reach is the browser's own
// animation timeline (CSS animations and transitions, Web Animations): before the screenshot every animation is finished (finite)
// or parked at its start (infinite), and the text caret is hidden, so the picture is a pure function of the DOM. Fonts are waited
// for. `--self` compares A with itself: any difference is nondeterminism of the method, and must be 0.
//
// NETWORK. The browser resolves no host except 127.0.0.1. The mock's Google Fonts link is answered from local files
// (--fonts, default docs/design/web-mocks/_src/fonts: the same woff2 files the packaged UI embeds) through the DevTools Fetch
// domain, so the mock renders with its real fonts; any other request fails and is reported.
//
// SCENES are JSON (scripts/web-parity-scenes.json, documented there). A step is one object:
//   {"wait": MS}                               advance the virtual clock
//   {"until": "JS expr", "max": MS}            advance in 100 ms slices until the expression is truthy (error at max)
//   {"click": "css", "text": "t", "nth": 0}    el.click() on the nth element matching css (containing the text); error if none
//   {"mouse": "css"}                           a real pointer move, press and release at the element's centre
//   {"hover": "css" | [x, y]}                  a real pointer move
//   {"key": "ctrl+k"}                          a key chord; {"keys": ["g", "e"]} presses several in turn
//   {"type": "text"}                           insert text into the focused element
//   {"eval": "JS"}                             run JS in the page
//   {"size": [w, h]}                           resize the viewport
//   {"expect": "css"}                          error unless an element matches (both pages)
//   {"sleep": MS}                              real time (for a page whose data arrives over the network)
//   {"sweep": N, "root": "css", "back": ["css", ...]}
//                                              click up to N distinct controls (buttons, tabs, links, checkboxes ...) under root, closing what
//                                              each opens (and clicking the `back` selectors to return when a click leaves the root), then type
//                                              into the text fields: exercises the page's handlers for CSP violations and script errors, and
//                                              compares what they leave behind
// any step may carry "when": "wide" | "narrow" (narrow: viewport width <= 760) to run on one kind of viewport only.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import zlib from 'node:zlib';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..');

/* ------------------------------------------------------------------------------------------------------------------ */
/* PNG: decode (inflate + filter reversal) and encode (stored filter 0 + deflate), RGBA 8 bit                         */
/* ------------------------------------------------------------------------------------------------------------------ */

const CRC = (() => { const t = new Uint32Array(256); for (let n = 0; n < 256; n++) { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; t[n] = c >>> 0; } return t; })();
/** CRC-32 of a buffer, as the PNG chunks want it. */
export function crc32(buf, c = 0xffffffff) { for (let i = 0; i < buf.length; i++) c = CRC[(c ^ buf[i]) & 255] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0; }

/** Decode a PNG into {width, height, data: Uint8Array RGBA}. Supports bit depth 8 (and 16, reduced to 8) of every colour type; no interlacing. */
export function decodePNG(buf) {
  const sig = [137, 80, 78, 71, 13, 10, 26, 10];
  for (let i = 0; i < 8; i++) if (buf[i] !== sig[i]) throw new Error('not a PNG');
  let pos = 8, width = 0, height = 0, depth = 0, ctype = 0, interlace = 0, palette = null, trns = null; const idat = [];
  while (pos < buf.length) {
    const len = buf.readUInt32BE(pos), type = buf.toString('latin1', pos + 4, pos + 8), body = buf.subarray(pos + 8, pos + 8 + len);
    pos += 12 + len;
    if (type === 'IHDR') { width = body.readUInt32BE(0); height = body.readUInt32BE(4); depth = body[8]; ctype = body[9]; interlace = body[12]; }
    else if (type === 'PLTE') palette = body;
    else if (type === 'tRNS') trns = body;
    else if (type === 'IDAT') idat.push(body);
    else if (type === 'IEND') break;
  }
  if (interlace) throw new Error('interlaced PNG is not supported');
  if (depth !== 8 && depth !== 16) throw new Error('PNG bit depth ' + depth + ' is not supported');
  const ch = { 0: 1, 2: 3, 3: 1, 4: 2, 6: 4 }[ctype];
  if (!ch) throw new Error('PNG colour type ' + ctype + ' is not supported');
  if (ctype === 3 && depth !== 8) throw new Error('palette PNG with depth ' + depth + ' is not supported');
  const bpp = ch * (depth / 8), stride = width * bpp, raw = zlib.inflateSync(Buffer.concat(idat));
  if (raw.length < (stride + 1) * height) throw new Error('PNG data is short');
  const cur = new Uint8Array(stride * height);
  for (let y = 0; y < height; y++) {
    const f = raw[y * (stride + 1)], src = y * (stride + 1) + 1, dst = y * stride, up = dst - stride;
    for (let x = 0; x < stride; x++) {
      const v = raw[src + x], a = x >= bpp ? cur[dst + x - bpp] : 0, b = y > 0 ? cur[up + x] : 0, c = x >= bpp && y > 0 ? cur[up + x - bpp] : 0;
      let r;
      switch (f) {
        case 0: r = v; break;
        case 1: r = v + a; break;
        case 2: r = v + b; break;
        case 3: r = v + ((a + b) >> 1); break;
        case 4: { const p = a + b - c, pa = Math.abs(p - a), pb = Math.abs(p - b), pc = Math.abs(p - c); r = v + (pa <= pb && pa <= pc ? a : pb <= pc ? b : c); break; }
        default: throw new Error('bad PNG filter ' + f);
      }
      cur[dst + x] = r & 255;
    }
  }
  const out = new Uint8Array(width * height * 4), step = depth / 8;
  for (let i = 0, n = width * height; i < n; i++) {
    const s = i * bpp, o = i * 4, g = k => cur[s + k * step];   // 16 bit: the high byte
    switch (ctype) {
      case 6: out[o] = g(0); out[o + 1] = g(1); out[o + 2] = g(2); out[o + 3] = g(3); break;
      case 2: out[o] = g(0); out[o + 1] = g(1); out[o + 2] = g(2); out[o + 3] = 255; break;
      case 0: out[o] = out[o + 1] = out[o + 2] = g(0); out[o + 3] = 255; break;
      case 4: out[o] = out[o + 1] = out[o + 2] = g(0); out[o + 3] = g(1); break;
      case 3: { const p = cur[s] * 3; out[o] = palette[p]; out[o + 1] = palette[p + 1]; out[o + 2] = palette[p + 2]; out[o + 3] = trns && cur[s] < trns.length ? trns[cur[s]] : 255; break; }
    }
  }
  return { width, height, data: out };
}

/** Encode {width, height, data RGBA} as a PNG. */
export function encodePNG({ width, height, data }) {
  const chunk = (type, body) => { const b = Buffer.alloc(12 + body.length); b.writeUInt32BE(body.length, 0); b.write(type, 4, 'latin1'); Buffer.from(body.buffer, body.byteOffset, body.length).copy(b, 8); b.writeUInt32BE(crc32(b.subarray(4, 8 + body.length)), 8 + body.length); return b; };
  const ihdr = Buffer.alloc(13); ihdr.writeUInt32BE(width, 0); ihdr.writeUInt32BE(height, 4); ihdr[8] = 8; ihdr[9] = 6;
  const raw = Buffer.alloc((width * 4 + 1) * height);
  for (let y = 0; y < height; y++) { raw[y * (width * 4 + 1)] = 0; Buffer.from(data.buffer, data.byteOffset + y * width * 4, width * 4).copy(raw, y * (width * 4 + 1) + 1); }
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', ihdr), chunk('IDAT', zlib.deflateSync(raw, { level: 6 })), chunk('IEND', Buffer.alloc(0))]);
}

/* ------------------------------------------------------------------------------------------------------------------ */
/* the comparison                                                                                                     */
/* ------------------------------------------------------------------------------------------------------------------ */

/** Paint solid rectangles [x, y, w, h] into an RGBA image (the mask colour). */
export function paintRects(img, rects, rgba = [255, 0, 255, 255]) {
  let n = 0;
  for (const [x0, y0, w, h] of rects) {
    for (let y = Math.max(0, y0); y < Math.min(img.height, y0 + h); y++) for (let x = Math.max(0, x0); x < Math.min(img.width, x0 + w); x++) { const o = (y * img.width + x) * 4; img.data[o] = rgba[0]; img.data[o + 1] = rgba[1]; img.data[o + 2] = rgba[2]; img.data[o + 3] = rgba[3]; n++; }
  }
  return n;
}

/**
 * Compare two RGBA images. A pixel differs when any channel differs by more than `tolerance`; pixels outside the smaller image
 * differ. Returns {diffPixels, total, pct, regions: [{x, y, w, h, pixels}], diff: image} where the diff image is `a` dimmed with the
 * differing pixels red and every region outlined.
 */
export function compare(a, b, { tolerance = 0, maxRegions = 12 } = {}) {
  const W = Math.max(a.width, b.width), H = Math.max(a.height, b.height), total = W * H;
  const mark = new Uint8Array(total), diff = { width: W, height: H, data: new Uint8Array(total * 4) };
  let n = 0;
  for (let y = 0; y < H; y++) {
    for (let x = 0; x < W; x++) {
      const i = y * W + x, inA = x < a.width && y < a.height, inB = x < b.width && y < b.height, o = i * 4;
      let d = false;
      if (inA && inB) {
        const pa = (y * a.width + x) * 4, pb = (y * b.width + x) * 4;
        d = Math.abs(a.data[pa] - b.data[pb]) > tolerance || Math.abs(a.data[pa + 1] - b.data[pb + 1]) > tolerance || Math.abs(a.data[pa + 2] - b.data[pb + 2]) > tolerance || Math.abs(a.data[pa + 3] - b.data[pb + 3]) > tolerance;
      } else d = true;
      if (d) { mark[i] = 1; n++; diff.data[o] = 255; diff.data[o + 1] = 0; diff.data[o + 2] = 0; diff.data[o + 3] = 255; }
      else if (inA) { const pa = (y * a.width + x) * 4, lum = (a.data[pa] * 0.3 + a.data[pa + 1] * 0.59 + a.data[pa + 2] * 0.11) | 0; const v = 40 + (lum >> 2); diff.data[o] = v; diff.data[o + 1] = v; diff.data[o + 2] = v; diff.data[o + 3] = 255; }
      else { diff.data[o] = 30; diff.data[o + 1] = 30; diff.data[o + 2] = 30; diff.data[o + 3] = 255; }
    }
  }
  const regions = n ? clusters(mark, W, H, maxRegions) : [];
  for (const r of regions) outline(diff, r);
  return { diffPixels: n, total, pct: total ? (100 * n) / total : 0, regions, diff };
}

/** Group differing pixels into regions: 8 px cells, cells within 2 cells of each other merge, each region gets its tight bounding box. */
function clusters(mark, W, H, max) {
  const C = 8, gw = Math.ceil(W / C), gh = Math.ceil(H / C), cell = new Uint8Array(gw * gh);
  for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) if (mark[y * W + x]) cell[((y / C) | 0) * gw + ((x / C) | 0)] = 1;
  const label = new Int32Array(gw * gh), R = 2; let nl = 0;
  for (let s = 0; s < cell.length; s++) {
    if (!cell[s] || label[s]) continue;
    label[s] = ++nl; const q = [s];
    while (q.length) {
      const c = q.pop(), cx = c % gw, cy = (c / gw) | 0;
      for (let dy = -R; dy <= R; dy++) for (let dx = -R; dx <= R; dx++) { const nx = cx + dx, ny = cy + dy; if (nx < 0 || ny < 0 || nx >= gw || ny >= gh) continue; const k = ny * gw + nx; if (cell[k] && !label[k]) { label[k] = nl; q.push(k); } }
    }
  }
  const box = new Map();
  for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) {
    if (!mark[y * W + x]) continue;
    const l = label[((y / C) | 0) * gw + ((x / C) | 0)]; let b = box.get(l); if (!b) box.set(l, b = { x0: x, y0: y, x1: x, y1: y, pixels: 0 });
    if (x < b.x0) b.x0 = x; if (y < b.y0) b.y0 = y; if (x > b.x1) b.x1 = x; if (y > b.y1) b.y1 = y; b.pixels++;
  }
  return Array.from(box.values()).sort((p, q) => q.pixels - p.pixels).slice(0, max).map(b => ({ x: b.x0, y: b.y0, w: b.x1 - b.x0 + 1, h: b.y1 - b.y0 + 1, pixels: b.pixels }));
}

function outline(img, r) {
  const put = (x, y) => { if (x < 0 || y < 0 || x >= img.width || y >= img.height) return; const o = (y * img.width + x) * 4; img.data[o] = 255; img.data[o + 1] = 220; img.data[o + 2] = 0; img.data[o + 3] = 255; };
  const x0 = r.x - 2, y0 = r.y - 2, x1 = r.x + r.w + 1, y1 = r.y + r.h + 1;
  for (let x = x0; x <= x1; x++) { put(x, y0); put(x, y1); }
  for (let y = y0; y <= y1; y++) { put(x0, y); put(x1, y); }
}

/* ------------------------------------------------------------------------------------------------------------------ */
/* the virtual clock, injected into every page before its own scripts                                                 */
/* ------------------------------------------------------------------------------------------------------------------ */

export const CLOCK = `(() => {
  if (window.__vt) return;
  const FRAME = 1000 / 60, EPOCH = Date.UTC(2026, 0, 2, 3, 4, 5), RealDate = Date;
  let now = 0, frameNo = 0, seq = 0, uid = 1000;
  const timers = new Map(), rafs = new Map(), realRaf = window.requestAnimationFrame.bind(window);
  const fail = e => queueMicrotask(() => { throw e; });
  const mk = repeat => function (fn, ms) {
    if (typeof fn !== 'function') return 0;
    const a = Array.prototype.slice.call(arguments, 2); ms = Math.max(0, +ms || 0);
    const id = ++uid; timers.set(id, { id, at: now + ms, seq: ++seq, fn, a, every: repeat ? Math.max(1, ms) : 0 }); return id;
  };
  window.setTimeout = mk(false); window.setInterval = mk(true);
  window.clearTimeout = window.clearInterval = id => { timers.delete(id); };
  window.requestAnimationFrame = fn => { const id = ++uid; rafs.set(id, fn); return id; };
  window.cancelAnimationFrame = id => { rafs.delete(id); };
  window.requestIdleCallback = fn => window.setTimeout(() => fn({ didTimeout: false, timeRemaining: () => 10 }), 1);
  window.cancelIdleCallback = window.clearTimeout;
  performance.now = () => now;
  function VDate() { if (!new.target) return new RealDate(EPOCH + now).toString(); return arguments.length ? new RealDate(...arguments) : new RealDate(EPOCH + now); }
  VDate.prototype = RealDate.prototype; Object.setPrototypeOf(VDate, RealDate); VDate.now = () => EPOCH + now;
  window.Date = VDate;
  let s = 0x5eed; Math.random = () => { s = (s + 0x6D2B79F5) >>> 0; let t = s; t = Math.imul(t ^ (t >>> 15), t | 1); t ^= t + Math.imul(t ^ (t >>> 7), t | 61); return ((t ^ (t >>> 14)) >>> 0) / 4294967296; };
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => false });
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
  window.__problems = [];
  document.addEventListener('securitypolicyviolation', e => window.__problems.push('csp: ' + e.violatedDirective + ' blocked ' + (e.blockedURI || 'inline') + (e.sourceFile ? ' at ' + e.sourceFile + ':' + e.lineNumber : '') + (e.sample ? ' [' + e.sample.slice(0, 60) + ']' : '')), true);
  function advance(ms) {
    const end = now + ms; let guard = 0;
    for (;;) {
      let next = null;
      for (const t of timers.values()) if (!next || t.at < next.at || (t.at === next.at && t.seq < next.seq)) next = t;
      const frameAt = (frameNo + 1) * FRAME;
      if (next && next.at <= frameAt && next.at <= end) {
        now = Math.max(now, next.at);
        if (next.every) { next.at += next.every; next.seq = ++seq; } else timers.delete(next.id);
        try { next.fn.apply(window, next.a); } catch (e) { fail(e); }
      } else if (frameAt <= end) {
        frameNo++; now = Math.max(now, frameAt);
        const cbs = Array.from(rafs.values()); rafs.clear();
        for (const fn of cbs) { try { fn(now); } catch (e) { fail(e); } }
      } else break;
      if (++guard > 5e6) throw new Error('virtual clock: runaway');
    }
    now = end; return now;
  }
  /** Resolves after two real (browser) frames: style, layout, ResizeObserver callbacks and paint have run for what changed. */
  const frame = () => new Promise(r => realRaf(() => realRaf(() => r(true))));
  window.__vt = { advance, frame, now: () => now, frames: () => frameNo, pending: () => ({ timers: timers.size, rafs: rafs.size }) };
})();`;

/** Run in the page before a screenshot: finish or park every animation, hide the caret. Returns the number of animations touched. */
const FREEZE = `(() => {
  let n = 0;
  for (const a of document.getAnimations()) {
    try {
      const t = a.effect && a.effect.getComputedTiming();
      a.playbackRate = 1;   // the mock slows every animation to 0 while the hold is on, and finish() refuses a stopped animation
      if (t && Number.isFinite(t.endTime)) a.finish(); else { a.pause(); a.currentTime = 0; }
      n++;
    } catch (e) { /* not ours to freeze */ }
  }
  if (!window.__frozenCss) { try { const sh = new CSSStyleSheet(); sh.replaceSync('*{caret-color:transparent!important}'); document.adoptedStyleSheets = [...document.adoptedStyleSheets, sh]; window.__frozenCss = true; } catch (e) { /* no constructable sheets */ } }
  return n;
})()`;

/**
 * Run in the page: click distinct controls one by one (virtual time advances after each), closing overlays between clicks. When a click
 * leaves the root (it opened another view), the `back` selectors are clicked in order to return, and the sweep goes on with what it has not clicked.
 */
const sweepJS = (max, root, back) => `(async () => {
  const rootSel = ${JSON.stringify(root || '')}, back = ${JSON.stringify(back || [])};
  const getRoot = () => rootSel ? document.querySelector(rootSel) : document;
  const sel = 'button, [role=button], [role=tab], [role=menuitem], [role=option], summary, a[href], input[type=checkbox], input[type=radio], select, [data-nav], .snb';
  const skip = '#input, .stx, [data-close-session]';
  const seen = new Set(); let n = 0, returns = 0;
  const sig = e => [e.tagName, e.id, typeof e.className === 'string' ? e.className : '', e.textContent.trim().slice(0, 40), JSON.stringify(e.dataset), e.getAttribute('aria-label') || '', e.closest('[data-view]') ? e.closest('[data-view]').dataset.view : ''].join('|');
  const open = () => document.querySelector('.sheet, .drawer, .sessmenu, .modemenu, .budpop, .ibx');
  const kick = async ms => { window.__vt.advance(ms); await window.__vt.frame(); };
  const click = e => { if (e.click) e.click(); else e.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window })); };
  const here = () => { const r = getRoot(); return r && (r === document || r.getClientRects().length > 0) ? r : null; };
  if (!here()) throw new Error('sweep: no root ' + rootSel);
  for (let guard = 0; guard < ${max} * 4 && n < ${max}; guard++) {
    let root = here();
    if (!root && returns < 40) { returns++; for (const b of back) { const e = document.querySelector(b); if (e) { click(e); await kick(100); } } root = here(); }
    if (!root) break;
    const el = Array.from(root.querySelectorAll(sel)).find(e => !e.matches(skip) && !e.disabled && e.getClientRects().length > 0 && !seen.has(sig(e)));
    if (!el) break;
    seen.add(sig(el)); n++;
    click(el); await kick(80);
    if (open()) { document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); await kick(60); }
  }
  const root = here();
  if (root) for (const f of Array.from(root.querySelectorAll('input[type=text], input[type=search], input[type=number], textarea')).filter(e => !e.matches(skip) && e.getClientRects().length > 0)) {
    f.value = 'a'; f.dispatchEvent(new Event('input', { bubbles: true })); f.dispatchEvent(new Event('change', { bubbles: true })); await kick(60);
  }
  return n;
})()`;

/* ------------------------------------------------------------------------------------------------------------------ */
/* a small DevTools client                                                                                            */
/* ------------------------------------------------------------------------------------------------------------------ */

const sleep = ms => new Promise(r => setTimeout(r, ms));

export function findChrome(explicit) {
  const cands = [explicit, process.env.CHROME_PATH].filter(Boolean);
  const cache = path.join(os.homedir(), '.cache/ms-playwright');
  try {
    for (const d of fs.readdirSync(cache).filter(x => /^chromium_headless_shell-/.test(x)).sort().reverse()) {
      for (const sub of fs.readdirSync(path.join(cache, d))) cands.push(path.join(cache, d, sub, 'chrome-headless-shell'));
    }
  } catch { /* no playwright cache */ }
  cands.push('/usr/bin/chromium', '/usr/bin/chromium-browser', '/usr/bin/google-chrome');
  const found = cands.find(c => { try { return fs.statSync(c).isFile(); } catch { return false; } });
  if (!found) throw new Error('no Chromium found: set CHROME_PATH or --chrome');
  return found;
}

/** One headless browser, one WebSocket; pages are attached with flattened sessions. */
export class Browser {
  static async launch(chromePath) {
    const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'web-parity-'));
    const args = ['--headless', '--no-sandbox', '--disable-gpu', '--hide-scrollbars', '--remote-debugging-port=0', '--user-data-dir=' + profile,
      '--force-device-scale-factor=1', '--force-color-profile=srgb', '--disable-lcd-text', '--disable-partial-raster', '--disable-checker-imaging', '--disable-threaded-animation', '--disable-threaded-scrolling', '--run-all-compositor-stages-before-draw', '--disable-background-timer-throttling', '--disable-renderer-backgrounding',
      '--disable-backgrounding-occluded-windows', '--no-first-run', '--no-default-browser-check', '--mute-audio',
      '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1', 'about:blank'];
    const proc = spawn(chromePath, args, { stdio: ['ignore', 'ignore', 'pipe'] });
    const b = new Browser(proc, profile);
    // however this process ends (an exception, a closed pipe, a signal), no browser is left running and no profile behind
    process.once('exit', () => { try { proc.kill('SIGKILL'); } catch { /* gone */ } try { fs.rmSync(profile, { recursive: true, force: true }); } catch { /* busy */ } });
    const wsURL = await new Promise((resolve, reject) => {
      let buf = ''; const t = setTimeout(() => reject(new Error('chrome did not start: ' + buf.slice(-300))), 20000);
      proc.stderr.on('data', d => { buf += d; const m = buf.match(/DevTools listening on (ws:\/\/\S+)/); if (m) { clearTimeout(t); resolve(m[1]); } });
      proc.on('exit', c => reject(new Error('chrome exited early (' + c + '): ' + buf.slice(-300))));
    });
    b.ws = new WebSocket(wsURL);
    await new Promise((r, j) => { b.ws.onopen = r; b.ws.onerror = () => j(new Error('cannot connect to ' + wsURL)); });
    b.ws.onmessage = ev => b.dispatch(JSON.parse(ev.data));
    return b;
  }
  constructor(proc, profile) { this.proc = proc; this.profile = profile; this.id = 0; this.pending = new Map(); this.handlers = new Map(); }
  dispatch(m) {
    if (m.id && this.pending.has(m.id)) { const { res, rej } = this.pending.get(m.id); this.pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result || {}); return; }
    const h = this.handlers.get(m.sessionId || ''); if (h && m.method) h(m.method, m.params || {});
  }
  send(method, params = {}, sessionId) {
    return new Promise((res, rej) => { const id = ++this.id; this.pending.set(id, { res, rej }); this.ws.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) })); });
  }
  async close() {
    try { this.ws.close(); } catch { /* closed */ }
    try { this.proc.kill('SIGKILL'); } catch { /* gone */ }
    await new Promise(r => { if (this.proc.exitCode !== null) r(); else { this.proc.once('exit', r); setTimeout(r, 2000); } });
    for (let i = 0; i < 5; i++) { try { fs.rmSync(this.profile, { recursive: true, force: true }); break; } catch { await sleep(200); } }
  }
}

const KEYS = { Escape: [27, 'Escape'], Enter: [13, 'Enter'], Tab: [9, 'Tab'], ArrowUp: [38, 'ArrowUp'], ArrowDown: [40, 'ArrowDown'], ArrowLeft: [37, 'ArrowLeft'], ArrowRight: [39, 'ArrowRight'], Backspace: [8, 'Backspace'], Space: [32, ' '], Home: [36, 'Home'], End: [35, 'End'] };

/** One page in its own browser context, with the virtual clock, the problem log and (for the mock) the local font responder. */
export class Page {
  static async open(browser, url, { width, height, mobile, fonts, mock, reducedMotion }) {
    const { browserContextId } = await browser.send('Target.createBrowserContext');
    const { targetId } = await browser.send('Target.createTarget', { url: 'about:blank', browserContextId });
    const { sessionId } = await browser.send('Target.attachToTarget', { targetId, flatten: true });
    const p = new Page(browser, sessionId, targetId, browserContextId);
    p.problems = []; p.notes = []; p.loaded = null; p.width = width; p.height = height; p.mobile = mobile; p.fonts = fonts; p.mock = mock;
    browser.handlers.set(sessionId, (m, a) => p.event(m, a));
    const s = (m, a) => p.send(m, a);
    await Promise.all([s('Page.enable'), s('Runtime.enable'), s('Log.enable'), s('Network.enable')]);
    await s('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile });
    await s('Emulation.setTimezoneOverride', { timezoneId: 'UTC' });
    await s('Emulation.setLocaleOverride', { locale: 'en-US' }).catch(() => {});
    await s('Emulation.setFocusEmulationEnabled', { enabled: true }).catch(() => {});
    if (reducedMotion) await s('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
    await s('Page.addScriptToEvaluateOnNewDocument', { source: CLOCK });
    await s('Fetch.enable', { patterns: [{ urlPattern: 'https://fonts.googleapis.com/*' }, { urlPattern: 'https://fonts.gstatic.com/*' }] });
    const loaded = new Promise(r => { p.loaded = r; });
    await s('Page.navigate', { url });
    await Promise.race([loaded, sleep(30000)]);
    await p.waitFor('window.__vt && document.readyState === "complete"', 15000);
    await p.settle(); await sleep(150); await p.settle();   // fonts, first layout and the observers' first callbacks happen in real time, before the simulation starts
    return p;
  }
  constructor(browser, sessionId, targetId, ctx) { this.b = browser; this.sessionId = sessionId; this.targetId = targetId; this.ctx = ctx; }
  send(method, params) { return this.b.send(method, params, this.sessionId); }
  event(method, a) {
    if (method === 'Page.loadEventFired') { if (this.loaded) this.loaded(); }
    else if (method === 'Runtime.exceptionThrown') { const d = a.exceptionDetails; this.problems.push('exception: ' + String((d.exception && d.exception.description) || d.text).split('\n')[0]); }
    else if (method === 'Runtime.consoleAPICalled' && (a.type === 'error' || a.type === 'assert')) this.problems.push('console.' + a.type + ': ' + a.args.map(x => x.value ?? x.description).join(' ').slice(0, 300));
    else if (method === 'Log.entryAdded' && a.entry.level === 'error') this.problems.push('log: ' + a.entry.text.slice(0, 300) + (a.entry.url ? ' ' + a.entry.url : ''));
    else if (method === 'Network.loadingFailed' && !a.canceled) this.problems.push('load failed: ' + a.errorText + (a.blockedReason ? ' (' + a.blockedReason + ')' : '') + ' ' + (this.urls && this.urls.get(a.requestId) || ''));
    else if (method === 'Network.requestWillBeSent') { (this.urls = this.urls || new Map()).set(a.requestId, a.request.url.slice(0, 120)); }
    else if (method === 'Fetch.requestPaused') this.answer(a).catch(e => this.problems.push('fetch: ' + e.message));
  }
  /**
   * The mock links Google Fonts: answer it from local files so that it renders with its real fonts. A page that is not the mock
   * must not ask for them (its CSP forbids it): the request is failed and reported. Every other external request fails anyway.
   */
  async answer(a) {
    const u = new URL(a.request.url), id = a.requestId;
    if (!this.mock) { this.problems.push('external request ' + a.request.url.slice(0, 120)); return this.send('Fetch.failRequest', { requestId: id, errorReason: 'BlockedByClient' }); }
    const cors = [{ name: 'Access-Control-Allow-Origin', value: '*' }, { name: 'Cache-Control', value: 'no-store' }];
    if (u.hostname === 'fonts.googleapis.com') {
      const css = fs.readFileSync(path.join(this.fonts, 'fonts.css'), 'utf8').replace(/url\(([^)]+\.woff2)\)/g, (m, f) => 'url(https://fonts.gstatic.com/local/' + f.replace(/['"]/g, '') + ')');
      return this.send('Fetch.fulfillRequest', { requestId: id, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'text/css; charset=utf-8' }, ...cors], body: Buffer.from(css).toString('base64') });
    }
    if (u.hostname === 'fonts.gstatic.com' && u.pathname.startsWith('/local/')) {
      const f = path.join(this.fonts, path.basename(u.pathname));
      if (fs.existsSync(f)) return this.send('Fetch.fulfillRequest', { requestId: id, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'font/woff2' }, ...cors], body: fs.readFileSync(f).toString('base64') });
    }
    this.problems.push('blocked request ' + a.request.url.slice(0, 120));
    return this.send('Fetch.failRequest', { requestId: id, errorReason: 'BlockedByClient' });
  }
  async eval(expr) {
    const r = await this.send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error('eval failed: ' + String((r.exceptionDetails.exception && r.exceptionDetails.exception.description) || r.exceptionDetails.text).split('\n')[0] + ' in ' + expr.slice(0, 120));
    return r.result ? r.result.value : undefined;
  }
  /** Let the browser run what is pending in real time: fonts, two frames (style, layout, observers, paint). */
  async settle() { await this.eval('document.fonts.ready.then(() => window.__vt.frame())'); }
  async waitFor(expr, ms) { const t0 = Date.now(); while (Date.now() - t0 < ms) { if (await this.eval(expr).catch(() => false)) return true; await sleep(40); } throw new Error('timed out waiting for ' + expr); }
  /** Advance the virtual clock by `ms`, in slices so that no single evaluation runs for long. */
  async wait(ms) { for (let left = ms; left > 0;) { const d = Math.min(left, 2000); await this.eval('window.__vt.advance(' + d + ')'); left -= d; } }
  async key(chord) {
    const parts = chord.split('+'), k = parts.pop(); let mod = 0;
    for (const m of parts) mod |= { alt: 1, ctrl: 2, meta: 4, cmd: 4, shift: 8 }[m.toLowerCase()] || 0;
    const named = KEYS[k], ch = k.length === 1 ? k : '', vk = named ? named[0] : k.toUpperCase().charCodeAt(0);
    const base = { modifiers: mod, key: named ? named[1] : k, code: named ? (k === 'Space' ? 'Space' : k) : /[a-z]/i.test(k) ? 'Key' + k.toUpperCase() : /^[0-9]$/.test(k) ? 'Digit' + k : '', windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk };
    await this.send('Input.dispatchKeyEvent', { type: 'keyDown', ...base, text: mod & 6 ? '' : ch || (k === 'Enter' ? '\r' : '') });
    await this.send('Input.dispatchKeyEvent', { type: 'keyUp', ...base });
  }
  async rectOf(sel, text, nth) {
    return this.eval(`(() => { const t = ${JSON.stringify(text || '')}; let l = Array.from(document.querySelectorAll(${JSON.stringify(sel)})); if (t) l = l.filter(e => e.textContent.includes(t)); const e = l[${nth | 0}]; if (!e) return null; e.scrollIntoView({ block: 'nearest', inline: 'nearest' }); const r = e.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2, w: r.width, h: r.height }; })()`);
  }
  async mouse(type, x, y, extra = {}) { await this.send('Input.dispatchMouseEvent', { type, x, y, ...extra }); }
  /** Run one scene step. */
  async step(st) {
    const narrow = this.width <= 760;
    if (st.when === 'wide' && narrow) return; if (st.when === 'narrow' && !narrow) return;
    if (st.wait != null) await this.wait(st.wait);
    else if (st.until != null) { const max = st.max || 20000; let t = 0; for (; t <= max; t += 100) { if (await this.eval('!!(' + st.until + ')')) break; await this.wait(100); await this.settle(); } if (t > max) throw new Error('until ' + st.until + ' not true after ' + max + ' ms'); }
    else if (st.click != null) {
      const ok = await this.eval(`(() => { const t = ${JSON.stringify(st.text || '')}; let l = Array.from(document.querySelectorAll(${JSON.stringify(st.click)})); if (t) l = l.filter(e => e.textContent.includes(t)); const e = l[${st.nth | 0}]; if (!e) return false; if (e.click) e.click(); else e.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window })); return true; })()`);
      if (!ok) { if (st.optional) return; throw new Error('click: no element matches ' + st.click + (st.text ? ' containing "' + st.text + '"' : '')); }
    } else if (st.mouse != null) {
      const r = await this.rectOf(st.mouse, st.text, st.nth); if (!r) throw new Error('mouse: no element matches ' + st.mouse);
      await this.mouse('mouseMoved', r.x, r.y); await this.mouse('mousePressed', r.x, r.y, { button: 'left', clickCount: 1 }); await this.mouse('mouseReleased', r.x, r.y, { button: 'left', clickCount: 1 });
    } else if (st.hover != null) {
      let x, y; if (Array.isArray(st.hover)) [x, y] = st.hover; else { const r = await this.rectOf(st.hover, st.text, st.nth); if (!r) throw new Error('hover: no element matches ' + st.hover); x = r.x; y = r.y; }
      await this.mouse('mouseMoved', x, y);
    } else if (st.key != null) await this.key(st.key);
    else if (st.keys != null) { for (const k of st.keys) { await this.key(k); await sleep(15); } }
    else if (st.type != null) await this.send('Input.insertText', { text: st.type });
    else if (st.eval != null) await this.eval(st.eval);
    else if (st.size != null) { this.width = st.size[0]; this.height = st.size[1]; await this.send('Emulation.setDeviceMetricsOverride', { width: st.size[0], height: st.size[1], deviceScaleFactor: 1, mobile: this.mobile }); }
    else if (st.sleep != null) await sleep(st.sleep);
    else if (st.sweep != null) this.notes.push('sweep clicked ' + (await this.eval(sweepJS(st.sweep, st.root, st.back))) + ' controls');
    else if (st.expect != null) { if (!(await this.eval(`!!document.querySelector(${JSON.stringify(st.expect)})`))) throw new Error('expect: no element matches ' + st.expect); }
    else throw new Error('unknown step ' + JSON.stringify(st));
    await this.settle();   // real time: let the browser deliver the event, run observers and lay out before the next step
  }
  /** Rectangles of the elements matching any selector, for masking. */
  async rects(selectors) {
    if (!selectors.length) return [];
    return this.eval(`(() => { const out = []; for (const s of ${JSON.stringify(selectors)}) for (const e of document.querySelectorAll(s)) { const r = e.getBoundingClientRect(); if (r.width > 0 && r.height > 0) out.push([Math.floor(r.left), Math.floor(r.top), Math.ceil(r.width) + 1, Math.ceil(r.height) + 1]); } return out; })()`);
  }
  /** Fonts loaded, animations finished or parked, caret hidden; then the PNG of the viewport. */
  async shoot() {
    for (let i = 0; i < 3; i++) {
      await this.eval('document.body.offsetHeight; document.fonts.ready.then(() => document.fonts.status)');
      const loading = await this.eval('Array.from(document.fonts).filter(f => f.status === "loading").length');
      if (!loading) break; await sleep(60);
    }
    for (let i = 0; i < 3; i++) { await this.eval(FREEZE); await sleep(50); }
    await this.eval(FREEZE);
    const { data } = await this.send('Page.captureScreenshot', { format: 'png', fromSurface: true });
    return Buffer.from(data, 'base64');
  }
  async pageProblems() { try { return (await this.eval('window.__problems || []')) || []; } catch { return []; } }
  async close() {
    this.b.handlers.delete(this.sessionId);
    try { await this.b.send('Target.closeTarget', { targetId: this.targetId }); } catch { /* gone */ }
    try { await this.b.send('Target.disposeBrowserContext', { browserContextId: this.ctx }); } catch { /* gone */ }
  }
}

/* ------------------------------------------------------------------------------------------------------------------ */
/* scenes                                                                                                             */
/* ------------------------------------------------------------------------------------------------------------------ */

/** Expand `each` (a scene repeated for several values of a variable, `${var}` substituted everywhere) and fill in the defaults. */
export function loadScenes(file, profile) {
  const doc = JSON.parse(fs.readFileSync(file, 'utf8')), def = doc.defaults || {}, out = [];
  const subst = (v, vars) => typeof v === 'string' ? v.replace(/\$\{(\w+)\}/g, (m, k) => (k in vars ? vars[k] : m)) : Array.isArray(v) ? v.map(x => subst(x, vars)) : v && typeof v === 'object' ? Object.fromEntries(Object.entries(v).map(([k, x]) => [k, subst(x, vars)])) : v;
  for (const sc of doc.scenes) {
    const sets = sc.each ? Object.entries(sc.each).reduce((acc, [k, vals]) => acc.flatMap(a => vals.map(v => ({ ...a, [k]: v }))), [{}]) : [{}];
    for (const vars of sets) {
      const s = subst({ ...sc, each: undefined }, vars);
      out.push({
        name: s.name, steps: s.steps || [], query: s.query || '', viewports: s.viewports || def.viewports || [[1440, 900], [390, 844]],
        threshold: s.threshold != null ? s.threshold : def.threshold != null ? def.threshold : 0.5,
        mask: [...(def.mask || []), ...(s.mask || []), ...(profile ? (doc.maskProfiles || {})[profile] || [] : [])], maskRects: s.maskRects || [], reducedMotion: !!s.reducedMotion,
      });
    }
  }
  return out;
}

function startServer(file) {
  const html = fs.readFileSync(file);
  return new Promise((resolve, reject) => {
    const srv = http.createServer((req, res) => { res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' }); res.end(req.method === 'HEAD' ? undefined : html); });
    srv.once('error', reject); srv.listen(0, '127.0.0.1', () => resolve({ srv, url: 'http://127.0.0.1:' + srv.address().port + '/' }));
  });
}

async function runPage(browser, url, scene, vp, opt, side) {
  const [width, height] = vp, res = { png: null, problems: [], rects: [], error: null, notes: [] };
  let page;
  try {
    const q = scene.query ? (url.includes('?') ? '&' : '?') + scene.query.replace(/^[?&]/, '') : '';
    page = await Page.open(browser, url + q, { width, height, mobile: opt.mobile === 'on' || (opt.mobile === 'auto' && width < 600), fonts: opt.fonts, mock: side === 'a' || opt.self, reducedMotion: scene.reducedMotion });
    for (const st of scene.steps) await page.step(st);
    res.notes = page.notes;
    res.rects = await page.rects(scene.mask);
    res.png = await page.shoot();
    res.problems = [...page.problems, ...(await page.pageProblems())];
  } catch (e) { res.error = e.message; if (page) res.problems = [...page.problems, ...(await page.pageProblems())]; }
  finally { if (page) await page.close(); }
  return res;
}

async function runOne(browser, urls, scene, vp, opt) {
  const t0 = Date.now(), name = scene.name + '@' + vp[0] + 'x' + vp[1], r = { scene: scene.name, viewport: vp[0] + 'x' + vp[1], threshold: opt.threshold != null ? opt.threshold : scene.threshold, error: null, problems: { a: [], b: [] } };
  const A = await runPage(browser, urls.a, scene, vp, opt, 'a'), B = await runPage(browser, urls.b, scene, vp, opt, 'b');
  r.problems = { a: A.problems, b: B.problems }; r.notes = [...new Set([...A.notes, ...B.notes])];
  if (A.error || B.error) { r.error = [A.error && 'A: ' + A.error, B.error && 'B: ' + B.error].filter(Boolean).join('; '); r.ms = Date.now() - t0; return r; }
  const ia = decodePNG(A.png), ib = decodePNG(B.png), rects = [...A.rects, ...B.rects, ...scene.maskRects];
  r.masked = paintRects(ia, rects); paintRects(ib, rects);
  const c = compare(ia, ib, { tolerance: opt.tolerance });
  r.size = [ia.width, ia.height, ib.width, ib.height]; r.diffPixels = c.diffPixels; r.total = c.total; r.pct = +c.pct.toFixed(4); r.regions = c.regions; r.ms = Date.now() - t0;
  if (opt.images !== 'none' && (opt.images === 'all' || c.diffPixels > 0)) {
    fs.mkdirSync(opt.out, { recursive: true });
    fs.writeFileSync(path.join(opt.out, name + '.a.png'), A.png); fs.writeFileSync(path.join(opt.out, name + '.b.png'), B.png);
    if (c.diffPixels > 0) fs.writeFileSync(path.join(opt.out, name + '.diff.png'), encodePNG(c.diff));
    r.files = { a: name + '.a.png', b: name + '.b.png', ...(c.diffPixels > 0 ? { diff: name + '.diff.png' } : {}) };
  }
  return r;
}

/* ------------------------------------------------------------------------------------------------------------------ */
/* main                                                                                                               */
/* ------------------------------------------------------------------------------------------------------------------ */

/** The codec and the comparison on synthetic pictures: round trip, CRC, tolerance, regions, size mismatch, masks. */
function selftest() {
  const assert = (c, m) => { if (!c) { console.error('selftest FAILED: ' + m); process.exit(1); } };
  assert(crc32(Buffer.from('123456789')) === 0xcbf43926, 'crc32 of the check string');
  const W = 70, H = 50, a = { width: W, height: H, data: new Uint8Array(W * H * 4) };
  for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) { const o = (y * W + x) * 4; a.data[o] = x * 3; a.data[o + 1] = y * 5; a.data[o + 2] = (x * y) & 255; a.data[o + 3] = 255; }
  const back = decodePNG(encodePNG(a));
  assert(back.width === W && back.height === H && Buffer.compare(Buffer.from(back.data), Buffer.from(a.data)) === 0, 'PNG round trip');
  assert(compare(a, back).diffPixels === 0, 'identical pictures');
  const b = { width: W, height: H, data: Uint8Array.from(a.data) };
  for (let y = 10; y < 14; y++) for (let x = 20; x < 30; x++) b.data[(y * W + x) * 4] ^= 0x10;
  for (let x = 60; x < 62; x++) b.data[(40 * W + x) * 4 + 2] ^= 0xff;
  let c = compare(a, b);
  assert(c.diffPixels === 42, 'diff pixel count ' + c.diffPixels);
  assert(c.regions.length === 2 && c.regions[0].x === 20 && c.regions[0].y === 10 && c.regions[0].w === 10 && c.regions[0].h === 4, 'regions ' + JSON.stringify(c.regions));
  assert(compare(a, b, { tolerance: 16 }).diffPixels === 2, 'tolerance hides a difference of 16 and keeps one of 255');
  assert(paintRects(b, [[18, 8, 14, 8], [58, 38, 6, 6]]) > 0 && paintRects(a, [[18, 8, 14, 8], [58, 38, 6, 6]]) > 0 && compare(a, b).diffPixels === 0, 'masks');
  assert(compare(a, { width: W - 10, height: H, data: new Uint8Array((W - 10) * H * 4) }).diffPixels > 0, 'a smaller picture differs');
  const d = decodePNG(encodePNG(compare(a, b).diff)); assert(d.width === W, 'diff picture encodes');
  console.log('selftest ok');
  return 0;
}

function usage(code) {
  console.log(`usage: node scripts/web-parity.mjs [options]
  --a FILE|URL        the reference page (default docs/design/web-mocks/v3/sleipnir-web.html)
  --b DIR|URL         the page under test; a directory is served like scripts/web-ui-dev.mjs (default internal/web/ui)
  --self              compare A with itself: any difference is nondeterminism of the method
  --scenes FILE       scenes (default scripts/web-parity-scenes.json)
  --only A,B          only scenes whose name contains one of these words
  --viewport WxH      only this viewport
  --threshold PCT     override every scene's threshold
  --tolerance N       a pixel differs when a channel differs by more than N (default 0)
  --profile NAME      add the selectors of maskProfiles.NAME to every scene's mask
  --jobs N            scene runs in parallel (default 2)
  --out DIR           where the PNGs go (default dist/web-parity)
  --images diff|all|none   which PNGs to write (default diff: only for scenes with a difference)
  --json FILE         write the report as JSON
  --fonts DIR         the mock's fonts for its Google Fonts link (default docs/design/web-mocks/_src/fonts)
  --chrome PATH       the Chromium (headless shell)
  --mobile auto|on|off   emulate a phone (meta viewport, overlay scrollbars) below 600 px wide (default auto)
  --list              list the scenes and exit
  --selftest          check the PNG codec and the comparison, then exit`);
  process.exit(code);
}

async function main() {
  process.stdout.on('error', () => process.exit(1));   // `| head` closed the pipe: stop, the exit handler removes the browser
  const argv = process.argv.slice(2), opt = {
    a: path.join(repo, 'docs/design/web-mocks/v3/sleipnir-web.html'), b: path.join(repo, 'internal/web/ui'), scenes: path.join(here, 'web-parity-scenes.json'), out: path.join(repo, 'dist/web-parity'),
    only: null, viewport: null, threshold: null, tolerance: 0, profile: null, self: false, jobs: 2, images: 'diff', json: null, fonts: path.join(repo, 'docs/design/web-mocks/_src/fonts'), chrome: null, list: false, mobile: 'auto',
  };
  for (let i = 0; i < argv.length; i++) {
    const k = argv[i], v = () => { if (i + 1 >= argv.length) { console.error('missing value for ' + k); process.exit(2); } return argv[++i]; };
    if (k === '--a') opt.a = v(); else if (k === '--b') opt.b = v(); else if (k === '--scenes') opt.scenes = v(); else if (k === '--out') opt.out = path.resolve(v()); else if (k === '--only') opt.only = v().split(',').filter(Boolean);
    else if (k === '--viewport') opt.viewport = v().split('x').map(Number); else if (k === '--threshold') opt.threshold = +v(); else if (k === '--tolerance') opt.tolerance = +v(); else if (k === '--profile') opt.profile = v();
    else if (k === '--self') opt.self = true; else if (k === '--jobs') opt.jobs = Math.max(1, +v() || 1); else if (k === '--images') opt.images = v(); else if (k === '--json') opt.json = v(); else if (k === '--fonts') opt.fonts = path.resolve(v());
    else if (k === '--chrome') opt.chrome = v(); else if (k === '--mobile') opt.mobile = v(); else if (k === '--list') opt.list = true; else if (k === '--selftest') return selftest(); else if (k === '-h' || k === '--help') usage(0); else { console.error('unknown argument ' + k); usage(2); }
  }
  let scenes = loadScenes(opt.scenes, opt.profile);
  if (opt.only) scenes = scenes.filter(s => opt.only.some(w => s.name.includes(w)));
  if (opt.list) { for (const s of scenes) console.log(s.name + '  ' + s.viewports.map(v => v.join('x')).join(' ') + '  threshold ' + s.threshold + '%  ' + s.steps.length + ' steps'); return 0; }
  const jobs = [];
  for (const s of scenes) for (const vp of s.viewports) if (!opt.viewport || (opt.viewport[0] === vp[0] && opt.viewport[1] === vp[1])) jobs.push({ s, vp });
  if (!jobs.length) { console.error('no scene matches'); return 2; }

  const cleanup = [], urls = { a: null, b: null };
  const asURL = async (x, label) => {
    if (/^https?:\/\//.test(x)) return x;
    const st = fs.statSync(x, { throwIfNoEntry: false });
    if (!st) throw new Error(label + ': ' + x + ' does not exist');
    if (st.isDirectory()) { const { listen } = await import(pathToFileURL(path.join(here, 'web-ui-dev.mjs')).href); const s = await listen(x, { port: 0, quiet: true }); cleanup.push(() => s.server.close()); return s.url; }
    const s = await startServer(x); cleanup.push(() => s.srv.close()); return s.url;
  };
  urls.a = await asURL(opt.a, 'A'); urls.b = opt.self ? urls.a : await asURL(opt.b, 'B');
  const browser = await Browser.launch(findChrome(opt.chrome));
  const bail = () => { browser.proc.kill('SIGKILL'); process.exit(130); }; process.once('SIGINT', bail); process.once('SIGTERM', bail);
  const results = new Array(jobs.length);
  console.log(`A ${opt.a}\nB ${opt.self ? 'A again (--self)' : opt.b}${opt.self || /^https?:/.test(opt.b) ? '' : ' (served at ' + urls.b + ')'}\n${jobs.length} scene runs, ${opt.jobs} at a time, threshold ${opt.threshold != null ? opt.threshold : 'per scene'}%, tolerance ${opt.tolerance}\n`);

  if (opt.images !== 'none') { fs.mkdirSync(opt.out, { recursive: true }); for (const f of fs.readdirSync(opt.out)) if (/\.(a|b|diff)\.png$/.test(f)) fs.rmSync(path.join(opt.out, f)); }
  let next = 0;
  const worker = async () => {
    for (;;) {
      const i = next++; if (i >= jobs.length) return;
      const { s, vp } = jobs[i];
      try { results[i] = await runOne(browser, urls, s, vp, opt); } catch (e) { results[i] = { scene: s.name, viewport: vp.join('x'), threshold: s.threshold, error: e.message, problems: { a: [], b: [] } }; }
      const r = results[i]; process.stderr.write(`  ${i + 1}/${jobs.length} ${r.scene}@${r.viewport}  ${r.error ? 'ERROR' : r.pct + '%'}\n`);
    }
  };
  try { await Promise.all(Array.from({ length: Math.min(opt.jobs, jobs.length) }, worker)); }
  finally { await browser.close(); for (const c of cleanup) { try { c(); } catch { /* closed */ } } }

  /* report */
  const W = Math.max(5, ...results.map(r => (r.scene + '@' + r.viewport).length));
  const failed = [];
  console.log('\n' + 'scene'.padEnd(W) + '  ' + 'diff %'.padStart(9) + '  ' + 'pixels'.padStart(8) + '  masked  regions  status');
  for (const r of results) {
    const csp = r.problems.b.filter(p => /^csp:/.test(p)), other = [...r.problems.a.map(p => 'A ' + p), ...r.problems.b.filter(p => !/^csp:/.test(p)).map(p => 'B ' + p)];
    let status = 'ok';
    if (r.error) status = 'ERROR'; else if (r.pct > r.threshold) status = 'FAIL'; else if (csp.length) status = 'CSP'; else if (other.length) status = 'LOG';
    r.status = status; if (status !== 'ok') failed.push(r);
    console.log((r.scene + '@' + r.viewport).padEnd(W) + '  ' + (r.error ? '-' : r.pct.toFixed(4)).padStart(9) + '  ' + String(r.diffPixels ?? '-').padStart(8) + '  ' + String(r.masked ?? '-').padStart(6) + '  ' + String(r.regions ? r.regions.length : '-').padStart(7) + '  ' + status + (r.error ? '  ' + r.error : ''));
    for (const g of (r.regions || []).slice(0, 5)) console.log('    region x=' + g.x + ' y=' + g.y + ' ' + g.w + 'x' + g.h + '  ' + g.pixels + ' px');
    for (const p of r.notes || []) console.log('    ' + p);
    for (const p of csp) console.log('    B ' + p);
    for (const p of [...new Set(other)].slice(0, 5)) console.log('    ' + p);
  }
  const ok = results.filter(r => r.status === 'ok').length, worst = results.filter(r => r.pct != null).reduce((m, r) => Math.max(m, r.pct), 0);
  console.log(`\n${ok}/${results.length} scene runs within their threshold; worst difference ${worst.toFixed(4)}%${opt.images !== 'none' ? '; images in ' + opt.out : ''}`);
  if (opt.json) { fs.mkdirSync(path.dirname(path.resolve(opt.json)), { recursive: true }); fs.writeFileSync(opt.json, JSON.stringify({ a: opt.a, b: opt.self ? opt.a : opt.b, tolerance: opt.tolerance, results }, null, 1)); }
  return failed.length ? 1 : 0;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then(c => process.exit(c), e => { console.error('web-parity: ' + (e && e.stack || e)); process.exit(2); });
}
