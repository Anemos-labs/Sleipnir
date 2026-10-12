// fakechrome.mjs: a stand-in for Chromium, for the tests of how scripts/web-parity.mjs's Browser.launch starts a browser (cdp-watchdog.test.mjs).
// Run as `node fakechrome.mjs MODE PIDFILE [the flags of Chromium, ignored]`: it appends its pid to PIDFILE, then
//   silent       prints nothing and waits (a browser stuck before it opens its DevTools port)
//   nohandshake  prints "DevTools listening on ws://..." and accepts connections there, but never answers the WebSocket upgrade
//                (a browser whose DevTools port is bound while the thread that would answer it is stuck)
//   noanswer     completes the WebSocket handshake and never answers a call
//   exit         writes a line to stderr and exits 1
//   answer       answers every DevTools call (a browser that works, as far as Browser.launch looks)
//   stall-once   the first run (PIDFILE was empty) is silent; every later run is answer
// It ignores SIGTERM, as a stuck browser may: only SIGKILL ends it.
import fs from 'node:fs';
import net from 'node:net';
import { createHash } from 'node:crypto';

const [mode0, pidFile] = process.argv.slice(2);
const first = !fs.existsSync(pidFile) || !fs.readFileSync(pidFile, 'utf8').trim();
fs.appendFileSync(pidFile, process.pid + '\n');
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

if (mode === 'exit') { process.stderr.write('[fake] no display to open\n'); process.exit(1); }
if (mode === 'silent') setInterval(() => {}, 1 << 30);
else {
  const held = [];
  const srv = net.createServer(c => {
    held.push(c);
    if (mode === 'nohandshake') return;
    let head = '', rest = null;
    c.on('data', d => {
      if (rest === null) {
        head += d.toString('latin1'); const m = head.match(/Sec-WebSocket-Key: (\S+)/i); if (!m || !head.includes('\r\n\r\n')) return;
        const accept = createHash('sha1').update(m[1] + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
        c.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' + accept + '\r\n\r\n');
        rest = Buffer.alloc(0); return;
      }
      if (mode === 'noanswer') return;
      let got; [got, rest] = frames(Buffer.concat([rest, d]));
      for (const f of got) {
        if (f.op === 8) { c.end(); return; }
        if (f.op === 1) { const m = JSON.parse(f.text); c.write(frame(JSON.stringify({ id: m.id, result: { product: 'FakeChrome/1' } }))); }
      }
    });
  }).listen(0, '127.0.0.1', () => {
    process.stderr.write('\nDevTools listening on ws://127.0.0.1:' + srv.address().port + '/devtools/browser/fake\n');
  });
}
