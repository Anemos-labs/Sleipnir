// Renders each HTML sheet given on the command line to a transparent-background PNG of the same name, 1x.
// logo.html -> logo.png, wordmark.html -> logo-wordmark.png, social.html -> social-preview.png, favicon-test.html -> favicon-test.png
import { createRequire } from 'module';
const require = createRequire('/opt/node22/lib/node_modules/');
const { chromium } = require('playwright');
import path from 'path';

const names = { 'logo.html': 'logo.png', 'wordmark.html': 'logo-wordmark.png', 'social.html': 'social-preview.png', 'favicon-test.html': 'favicon-test.png' };
const browser = await chromium.launch();
for (const f of process.argv.slice(2)) {
  const page = await browser.newPage({ deviceScaleFactor: 1, viewport: { width: 1600, height: 800 } });
  await page.goto('file://' + path.resolve(f));
  const el = await page.$('.s');
  const out = path.join(path.dirname(path.resolve(f)), names[path.basename(f)] || f.replace(/\.html$/, '.png'));
  await el.screenshot({ path: out, omitBackground: true });
  await page.close();
  console.log('rendered', out);
}
await browser.close();
