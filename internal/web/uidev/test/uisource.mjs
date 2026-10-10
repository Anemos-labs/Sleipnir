// uisource.mjs: serve the page's own files (index.html, js/, css/) to a browser from this checkout instead of from the server's embedded
// copy, through the DevTools Fetch domain: a page check then runs the scripts as they are on disk (or as they were at a git revision)
// against a real server built once, without rebuilding it for each change. Everything else (the API, the stream, the sign-in) comes
// from the server as it is.
//
//   import { serveUi } from './uisource.mjs';
//   await serveUi(page, { rev: process.env.UI_REV });   // before the page is navigated; rev: a git revision, or none for the files on disk
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const UI = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'ui');
const REPO = path.resolve(UI, '..', '..', '..');
const TYPES = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8' };

/** The bytes of ui/<rel> on disk, or at git revision rev; null when there is no such file. */
function source(rel, rev) {
  if (!rev) { const f = path.join(UI, rel); return fs.existsSync(f) ? fs.readFileSync(f) : null; }
  try { return execFileSync('git', ['-C', REPO, 'show', rev + ':internal/web/ui/' + rel], { maxBuffer: 64 << 20 }); } catch (e) { return null; }
}

/** Answer the page's requests for index.html, js/* and css/* from the checkout (at rev, when given); keep the server's headers. */
export async function serveUi(page, opt = {}) {
  const rev = opt.rev || '';
  await page.send('Fetch.enable', { patterns: [{ urlPattern: '*', resourceType: 'Document', requestStage: 'Response' }, { urlPattern: '*/js/*', requestStage: 'Response' }, { urlPattern: '*/css/*', requestStage: 'Response' }] });
  page.on('Fetch.requestPaused', async p => {
    const u = new URL(p.request.url); let rel = u.pathname.replace(/^\//, '');
    const doc = p.resourceType === 'Document' && (p.responseStatusCode === 200) && /text\/html/.test((p.responseHeaders || []).map(h => h.name.toLowerCase() === 'content-type' ? h.value : '').join(''));
    if (doc) rel = 'index.html';
    const body = (doc || /^(js|css)\/[\w.-]+$/.test(rel)) && p.responseStatusCode ? source(rel, rev) : null;
    if (!body) { await page.send('Fetch.continueRequest', { requestId: p.requestId }).catch(() => {}); return; }
    const headers = (p.responseHeaders || []).filter(h => !/^(content-length|etag|content-type)$/i.test(h.name)).concat([{ name: 'Content-Type', value: TYPES[path.extname(rel)] || 'application/octet-stream' }]);
    await page.send('Fetch.fulfillRequest', { requestId: p.requestId, responseCode: 200, responseHeaders: headers, body: body.toString('base64') }).catch(() => {});
  });
}
