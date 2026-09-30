// Renders an SVG to a PNG with headless Chromium, for the stills of docs/media (the recordings themselves are SVG).
//
//   node scripts/svg2png.mjs IN.svg OUT.png [--scale 2] [--at SECONDS]
//
// An animated SVG is a CSS animation; --at freezes every animation at that many seconds into the loop, so a still can be taken of
// any moment of a recording (without it the still is the first frame). Playwright is found in the usual places: the project's
// node_modules, the global ones ($NODE_PATH, `npm root -g`) or $PLAYWRIGHT_NODE_MODULES. A Chromium is found by Playwright itself
// ($PLAYWRIGHT_BROWSERS_PATH), or given as $CHROMIUM_PATH.
import { createRequire } from 'module';
import { execFileSync } from 'child_process';
import fs from 'fs';
import path from 'path';

function loadPlaywright() {
  const roots = [process.cwd() + '/', process.env.PLAYWRIGHT_NODE_MODULES, ...(process.env.NODE_PATH || '').split(path.delimiter)];
  try { roots.push(execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim() + '/'); } catch { /* no npm: fine */ }
  roots.push('/opt/node22/lib/node_modules/');
  for (const r of roots.filter(Boolean)) {
    try { return createRequire(r.endsWith('/') ? r : r + '/')('playwright'); } catch { /* try the next */ }
  }
  console.error('svg2png: playwright is not installed (npm install -g playwright, or set PLAYWRIGHT_NODE_MODULES)');
  process.exit(2);
}

const args = process.argv.slice(2);
const opts = { scale: 1, at: null };
const files = [];
for (let i = 0; i < args.length; i++) {
  if (args[i] === '--scale') opts.scale = parseFloat(args[++i]);
  else if (args[i] === '--at') opts.at = parseFloat(args[++i]);
  else files.push(args[i]);
}
if (files.length !== 2 || !(opts.scale > 0)) {
  console.error('usage: node scripts/svg2png.mjs IN.svg OUT.png [--scale N] [--at SECONDS]');
  process.exit(2);
}
const [input, output] = files;
const svg = fs.readFileSync(input, 'utf8');
const m = svg.match(/viewBox="0 0 ([\d.]+) ([\d.]+)"/);
if (!m) { console.error('svg2png: ' + input + ' has no viewBox'); process.exit(2); }
const width = Math.ceil(parseFloat(m[1])), height = Math.ceil(parseFloat(m[2]));

const { chromium } = loadPlaywright();
const launch = process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {};
const browser = await chromium.launch(launch);
try {
  const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: opts.scale });
  await page.goto('file://' + path.resolve(input));
  if (opts.at !== null) {
    // every animation of the document has the same length and started at load: pausing them all at one time shows that moment
    await page.evaluate((ms) => { for (const a of document.getAnimations()) { a.pause(); a.currentTime = ms; } }, opts.at * 1000);
  }
  await page.screenshot({ path: output, omitBackground: true });
} finally {
  await browser.close();
}
console.log(`wrote ${output} (${width}x${height} at ${opts.scale}x${opts.at === null ? '' : ', frozen at ' + opts.at + 's'})`);
