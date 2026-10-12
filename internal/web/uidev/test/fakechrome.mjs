// fakechrome.mjs: a stand-in for Chromium, for the tests of how scripts/web-parity.mjs's Browser.launch and cdp.mjs's open start a browser
// (cdp-watchdog.test.mjs).
// Run as `node fakechrome.mjs MODE PIDFILE [the flags of Chromium]`: it appends a line to PIDFILE with its pid and the directory of its
// --user-data-dir flag (separated by a tab), writes a file in that directory as a browser does, then
//   silent       prints nothing and waits (a browser stuck before it opens its DevTools port)
//   nolist       prints "DevTools listening on ws://..." and accepts connections there, but answers nothing: neither the list of targets
//                at /json/list nor a WebSocket upgrade (a browser whose DevTools port is bound while the thread that would answer it is stuck)
//   nohandshake  lists its one page at /json/list, but never answers a WebSocket upgrade
//   noanswer     completes the WebSocket handshake and never answers a call
//   answer-once  answers the first call and no later one (a browser that hangs after it started)
//   exit         writes a line to stderr and exits 1
//   answer       answers every DevTools call, and fires Page.loadEventFired after Page.navigate (a browser that works, as far as
//                Browser.launch and open look)
//   stall-once   the first run (PIDFILE was empty) is silent; every later run is answer
// It ignores SIGTERM, as a stuck browser may: only SIGKILL ends it.
import fs from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { createHash } from 'node:crypto';

const [mode0, pidFile] = process.argv.slice(2);
const profile = (process.argv.find(a => a.startsWith('--user-data-dir=')) || '').slice('--user-data-dir='.length);
const first = !fs.existsSync(pidFile) || !fs.readFileSync(pidFile, 'utf8').trim();
fs.appendFileSync(pidFile, process.pid + '\t' + profile + '\n');
if (profile) fs.writeFileSync(path.join(profile, 'Local State'), '{}');
const mode = mode0 === 'stall-once' ? (first ? 'silent' : 'answer') : mode0;
process.on('SIGTERM', () => {});

/** a text frame from the server: unmasked, as RFC 6455 has it */
function frame(text) {
  const p = Buffer.from(text), head = p.length < 126 ? Buffer.from([0x81, p.length]) : Buffer.from([0x81, 126, p.length >> 8, p.length & 255]);
  return Buffer.concat([head, p]);
}
/** the client frames complete in buf: [{op, text}], and what is left of buf */
function frames(buf) {
  const out = [];
  for (;;) {
    if (buf.length < 2) break;
    let len = buf[1] & 127, at = 2;
    if (len === 126) { if (buf.length < 4) break; len = buf.readUInt16BE(2); at = 4; } else if (len === 127) { if (buf.length < 10) break; len = Number(buf.readBigUInt64BE(2)); at = 10; }
    if (buf.length < at + 4 + len) break;
    const mask = buf.subarray(at, at + 4), p = Buffer.from(buf.subarray(at + 4, at + 4 + len));
    for (let i = 0; i < p.length; i++) p[i] ^= mask[i & 3];
    out.push({ op: buf[0] & 15, text: p.toString() }); buf = buf.subarray(at + 4 + len);
  }
  return [out, buf];
}
/** an HTTP/1.1 response that closes its connection */
const http = (status, json) => 'HTTP/1.1 ' + status + '\r\nContent-Type: application/json\r\nContent-Length: ' + Buffer.byteLength(json) + '\r\nConnection: close\r\n\r\n' + json;

if (mode === 'exit') { process.stderr.write('[fake] no display to open\n'); process.exit(1); }
if (mode === 'silent') setInterval(() => {}, 1 << 30);
else {
  const held = [];
  let answered = 0;
  const srv = net.createServer(c => {
    held.push(c);
    if (mode === 'nolist') return;
    let head = '', rest = null;
    c.on('data', d => {
      if (rest === null) {
        head += d.toString('latin1'); if (!head.includes('\r\n\r\n')) return;
        const key = head.match(/Sec-WebSocket-Key: (\S+)/i);
        if (!key) {   // a plain request: the list of targets is the only one a browser is asked here
          const page = { id: 'fake', type: 'page', url: 'about:blank', webSocketDebuggerUrl: 'ws://127.0.0.1:' + srv.address().port + '/devtools/page/fake' };
          c.end(/^GET \/json(\/list)?[ ?]/.test(head) ? http('200 OK', JSON.stringify([page])) : http('404 Not Found', '{}'));
          return;
        }
        if (mode === 'nohandshake') return;
        const accept = createHash('sha1').update(key[1] + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
        c.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' + accept + '\r\n\r\n');
        rest = Buffer.alloc(0); return;
      }
      if (mode === 'noanswer') return;
      let got; [got, rest] = frames(Buffer.concat([rest, d]));
      for (const f of got) {
        if (f.op === 8) { c.end(); return; }
        if (f.op !== 1 || (mode === 'answer-once' && answered)) continue;
        const m = JSON.parse(f.text); answered++;
        c.write(frame(JSON.stringify({ id: m.id, result: { product: 'FakeChrome/1' } })));
        if (m.method === 'Page.navigate') c.write(frame(JSON.stringify({ method: 'Page.loadEventFired', params: { timestamp: 0 } })));
      }
    });
  }).listen(0, '127.0.0.1', () => {
    process.stderr.write('\nDevTools listening on ws://127.0.0.1:' + srv.address().port + '/devtools/browser/fake\n');
  });
}
