#!/usr/bin/env node
// verify-real.mjs: builds a throw-away state directory that holds the 29 sample sessions (real event-log heads, the sample mtimes
// shifted onto the real clock, padded to the sample sizes), runs the REAL binary on it and compares its `sessions` and
// `sessions prune` output with the ports in outputs.js. Needs bin/sleipnir; touches nothing outside tmp/.
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
const here = path.dirname(fileURLToPath(import.meta.url));
const SL = process.env.SL, root = path.join(here, 'tmp/sessions-check/sessions');
const sb = {}; sb.window = sb; vm.createContext(sb);
vm.runInContext(fs.readFileSync(path.join(here, 'data.js'), 'utf8'), sb); vm.runInContext(fs.readFileSync(path.join(here, 'outputs.js'), 'utf8'), sb);
const S = sb.SLDATA;
fs.rmSync(path.join(here, 'tmp/sessions-check'), { recursive: true, force: true }); fs.mkdirSync(root, { recursive: true });
const realNow = Date.now(), sampleNow = Date.parse(S.meta.sampleNow);
const state = S.newState();
for (const s of state.sessions) {
  const d = path.join(root, s.id); fs.mkdirSync(d);
  const log = [{ seq: 1, type: 'session.start', data: { model: s.model } }, { seq: 2, type: 'user.input', data: { text: s.prompt } }, { seq: 3, type: 'session.end', data: { cost_usd: s.cost } }].map((e) => JSON.stringify(e)).join('\n') + '\n';
  fs.writeFileSync(path.join(d, 'events.jsonl'), log);
  fs.writeFileSync(path.join(d, 'pad.bin'), Buffer.alloc(Math.max(0, s.size - Buffer.byteLength(log))));
  const t = new Date(realNow - (sampleNow - Date.parse(s.lastWritten)));
  fs.utimesSync(path.join(d, 'events.jsonl'), t, t); fs.utimesSync(path.join(d, 'pad.bin'), t, t); fs.utimesSync(d, t, t);
}
const run = (...a) => { try { return execFileSync(SL, a, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }); } catch (e) { return e.stdout + e.stderr; } };
let fails = 0;
const cmp = (name, got, want) => { if (got === want) console.log('ok   ' + name); else { fails++; console.log('DIFF ' + name + '\n--- binary\n' + want + '\n--- port\n' + got); } };
// prune: dry run with the defaults, then --older-than 0 --keep 5
for (const [fl, args] of [[{}, []], [{ 'older-than': '60d', keep: 10 }, ['--older-than', '60d', '--keep', '10']], [{ 'older-than': '2w', keep: 0 }, ['--older-than', '2w', '--keep', '0']]]) {
  const want = run('sessions', 'prune', '--dir', root, ...args).trimEnd();
  const got = S.runOutput('sessions prune', fl, { state: S.newState() }).lines.map((l) => l.t).join('\n');
  cmp('sessions prune ' + args.join(' '), got, want);
}
// list: same ids and the same line up to the age column (the real clock moved a few seconds)
const want = run('sessions', '--dir', root, '-n', '29').trimEnd().split('\n');
const got = S.runOutput('sessions', { n: 29 }, { state: S.newState() }).lines.map((l) => l.t);
const norm = (l) => l.replace(/(\d+[smhd]|just now)( ago)?/, 'AGE').replace('↺', ' ');   // resumable needs snapshots in the log: the real one cannot know
let same = 0;
for (let i = 0; i < Math.max(want.length - 1, got.length - 1); i++) { const a = norm(want[i] || '').replace(/ +/g, ' '), b = norm(got[i] || '').replace(/ +/g, ' '); if (a === b) same++; else { fails++; console.log('DIFF line ' + i + '\n  binary: ' + want[i] + '\n  port  : ' + got[i]); } }
console.log(`sessions list: ${same} of ${got.length - 1} lines equal (age column and the ↺ mark aside)`);
// models: serve the sample catalogue (marketplace JSON) on loopback and let the REAL binary list it
import http from 'node:http';
const PRICE = (v) => (v === null ? '0' : String(v / 1e6));
const srv = http.createServer((req, res) => {
  const m = /^\/(heimdall|openrouter|openai)\/models$/.exec(req.url);
  if (!m) { res.writeHead(404); res.end(); return; }
  const data = S.models.filter((x) => x.provider === m[1]).map((x) => ({ id: x.id, context_length: x.context, architecture: { modality: x.chat ? 'text->text' : 'text->embedding' }, pricing: { prompt: PRICE(x.inPerM), completion: PRICE(x.outPerM), ...(x.cachedPerM !== null ? { input_cache_read: PRICE(x.cachedPerM) } : {}) }, supported_parameters: [...(x.tools ? ['tools'] : []), ...(x.reasoning ? ['reasoning', 'reasoning_effort'] : [])] }));
  res.writeHead(200, { 'content-type': 'application/json' }); res.end(JSON.stringify({ data }));
});
await new Promise((r) => srv.listen(18192, '127.0.0.1', r));
const home = path.join(here, 'tmp/models-home'); fs.rmSync(home, { recursive: true, force: true }); fs.mkdirSync(path.join(home, '.sleipnir'), { recursive: true });
fs.writeFileSync(path.join(home, '.sleipnir/config.json'), JSON.stringify({ models: { favorites: ['anthropic/claude-sonnet-5-5', 'anthropic/claude-haiku-5-5', 'heimdall/demo-model'] } }));
const env = { ...process.env, HOME: home, SLEIPNIR_HOME: home, HEIMDALL_BASE_URL: 'http://127.0.0.1:18192/heimdall', OPENROUTER_BASE_URL: 'http://127.0.0.1:18192/openrouter', OPENAI_BASE_URL: 'http://127.0.0.1:18192/openai', OPENROUTER_API_KEY: 'x', OPENAI_API_KEY: 'x', HEIMDALL_API_KEY: 'x' };
const runm = (...a) => new Promise((resolve) => { import('node:child_process').then(({ execFile }) => execFile(SL, ['models', ...a], { env, cwd: here }, (e, so, se) => resolve(so.trimEnd()))); });
const cases = [[{}, []], [{ tools: true, reasoning: true }, ['--tools', '--reasoning']], [{ 'max-price': 5, 'min-context': '200k' }, ['--max-price', '5', '--min-context', '200k']], [{ _: ['sample', 'coder'] }, ['sample', 'coder']], [{ fav: true }, ['--fav']], [{ all: true }, ['--all']]];
for (const [fl, args] of cases) {
  // the real binary lists heimdall, openrouter and openai only here (the other providers need a key or a route): compare on those
  const wantLines = (await runm(...args)).split('\n');
  const mine = S.runOutput('models', fl, { state: S.newState() }).lines.map((l) => l.t).filter((t, i) => i === 0 || /^\*? ?(heimdall|openrouter|openai)\//.test(t));
  // the real column widths follow only the rows it lists: re-run the tabwriter port on the same rows
  const rows = mine.map((t) => t.split(/\s{2,}/));
  const widths = F_tab(rows);
  cmp('models ' + args.join(' '), widths.join('\n'), wantLines.join('\n'));
}
function F_tab(rows) { return S.fmt.tabwriter(rows, 2); }
srv.close();
process.exit(fails ? 1 : 0);
