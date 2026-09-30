// Renders each HTML file given on the command line to a PNG of the same name (2x), with the sandbox's Chromium.
import { createRequire } from 'module';
const require = createRequire('/opt/node22/lib/node_modules/');
const { chromium } = require('playwright');
import fs from 'fs';
import path from 'path';

const browser = await chromium.launch({ executablePath: process.env.CHROME || undefined });
for (const f of process.argv.slice(2)) {
  const page = await browser.newPage({ deviceScaleFactor: 2, viewport: { width: 1400, height: 900 } });
  await page.goto('file://' + path.resolve(f));
  const el = await page.$(".sheet");
  await el.screenshot({ path: f.replace(/\.html$/, '.png') });
  await page.close();
  console.log('rendered', f.replace(/\.html$/, '.png'));
}
await browser.close();
