#!/usr/bin/env node
// web-ui-dev.mjs: serve internal/web/ui on a loopback port with the Content-Security-Policy of `sleipnir web`, with no dependencies.
//
//   node scripts/web-ui-dev.mjs [--port 8686] [--host 127.0.0.1] [--dir internal/web/ui] [--quiet]
//
// The UI is plain files (no build step), so this is only a static file server: it lets the page be opened and screenshotted
// without the Go server (scripts/web-parity.mjs and scripts/look.sh style checks). It differs from the Go server in what it
// leaves out (no token, no cookie, no /api): the headers that decide whether the page works are the same, so a CSP violation
// shows up here exactly as it would there (the browser logs each violation to its console; scripts/web-parity.mjs fails a
// scene that produced one).
//
// Behaviour:
//   - GET and HEAD only; anything else is 405. `/` serves index.html. A path is resolved inside --dir; `..` and NUL are refused.
//   - Content types are exact and `X-Content-Type-Options: nosniff` is always sent (woff2 and svg need the right type).
//   - `/api/*` answers a JSON 404 (the live API belongs to the Go server).
//   - Cache-Control is no-store so an edit is seen on reload.
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

/** The policy of the Go server (contentSecurityPolicy in internal/web/envelope.go): keep the two in step. */
export const CSP = [
  "default-src 'none'",
  "script-src 'self'",
  "style-src 'self'",
  "style-src-attr 'unsafe-inline'",
  "font-src 'self'",
  "img-src 'self' data:",
  "connect-src 'self'",
  "frame-ancestors 'none'",
  "form-action 'none'",
  "base-uri 'none'",
].join('; ');

export const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.txt': 'text/plain; charset=utf-8',
  '.md': 'text/markdown; charset=utf-8',
};

const here = path.dirname(fileURLToPath(import.meta.url));

/**
 * Create the server for `root`. Resolves a request path to a file inside root and answers with the CSP and the content type.
 * @param {string} root directory to serve
 * @param {{quiet?: boolean}} [o]
 */
export function createServer(root, o = {}) {
  root = path.resolve(root);
  return http.createServer((req, res) => {
    const send = (code, type, body, extra = {}) => {
      res.writeHead(code, {
        'Content-Type': type,
        'Content-Security-Policy': CSP,
        'X-Content-Type-Options': 'nosniff',
        'Referrer-Policy': 'no-referrer',
        'Cache-Control': 'no-store',
        ...extra,
      });
      res.end(req.method === 'HEAD' ? undefined : body);
      if (!o.quiet) console.log(code, req.method, req.url);
    };
    if (req.method !== 'GET' && req.method !== 'HEAD') return send(405, 'text/plain; charset=utf-8', 'method not allowed\n', { Allow: 'GET, HEAD' });
    let rel;
    try { rel = decodeURIComponent(new URL(req.url, 'http://x').pathname); } catch { return send(400, 'text/plain; charset=utf-8', 'bad request\n'); }
    if (rel.includes('\0')) return send(400, 'text/plain; charset=utf-8', 'bad request\n');
    if (rel === '/api' || rel.startsWith('/api/')) return send(404, TYPES['.json'], '{"error":"not found"}\n');
    if (rel.endsWith('/')) rel += 'index.html';
    const file = path.resolve(root, '.' + rel);
    if (file !== root && !file.startsWith(root + path.sep)) return send(404, 'text/plain; charset=utf-8', 'not found\n');
    fs.stat(file, (err, st) => {
      if (err || !st.isFile()) return send(404, 'text/plain; charset=utf-8', 'not found\n');
      fs.readFile(file, (e2, buf) => {
        if (e2) return send(500, 'text/plain; charset=utf-8', 'error\n');
        send(200, TYPES[path.extname(file).toLowerCase()] || 'application/octet-stream', buf);
      });
    });
  });
}

/** Start listening; resolves {server, url, port}. Port 0 picks a free port. */
export function listen(root, { port = 8686, host = '127.0.0.1', quiet = false } = {}) {
  const server = createServer(root, { quiet });
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, host, () => {
      const a = server.address();
      resolve({ server, port: a.port, url: `http://${host}:${a.port}/` });
    });
  });
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const argv = process.argv.slice(2);
  const opt = { port: 8686, host: '127.0.0.1', dir: path.join(here, '..', 'internal/web/ui'), quiet: false };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--port') opt.port = +argv[++i];
    else if (a === '--host') opt.host = argv[++i];
    else if (a === '--dir') opt.dir = path.resolve(argv[++i]);
    else if (a === '--quiet') opt.quiet = true;
    else if (a === '-h' || a === '--help') { console.log('usage: node scripts/web-ui-dev.mjs [--port 8686] [--host 127.0.0.1] [--dir internal/web/ui] [--quiet]'); process.exit(0); }
    else { console.error('unknown argument ' + a); process.exit(2); }
  }
  if (!['127.0.0.1', 'localhost', '::1'].includes(opt.host)) { console.error('web-ui-dev: refusing a non-loopback host ' + opt.host); process.exit(2); }
  if (!fs.existsSync(path.join(opt.dir, 'index.html'))) { console.error('web-ui-dev: no index.html in ' + opt.dir); process.exit(1); }
  const { url } = await listen(opt.dir, opt);
  console.log(url);
  console.error('serving ' + opt.dir + '\nCSP: ' + CSP);
}
