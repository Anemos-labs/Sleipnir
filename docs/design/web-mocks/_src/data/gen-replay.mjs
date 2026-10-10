// Distils a recorded session (events.jsonl of `sleipnir demo`) into a compact timeline for the Replay view.
// Event types kept (REAL names in internal/events): agent.spawn agent.state board.op tool.call tool.result model.request
// model.response cache.anomaly compact.commit mail.send mail.deliver merge.queued merge.merged merge.verify_failed
// merge.rolled_back task.merge lease perm.decide agent.stuck swarm.integration session.start session.end.
import fs from 'node:fs';
const clip = (s, n = 80) => { s = String(s ?? '').replace(/\s+/g, ' ').trim(); return s.length > n ? s.slice(0, n - 1) + '…' : s; };
const SANI = (s) => String(s).replaceAll(process.env.SCR, '<scratch>');
function distil(file, { ms = false, summary = false } = {}) {
  const rows = fs.readFileSync(file, 'utf8').trim().split('\n').map((l) => JSON.parse(l));
  const t0 = Date.parse(rows.find((r) => r.type === 'session.start').ts);
  const T = (r) => { const d = (Date.parse(r.ts) - t0) / 1000; return ms ? Math.round(d * 1000) / 1000 : Math.round(d * 10) / 10; };
  const out = [], pendingTool = {}, reqSeen = {};
  let meta = null;
  for (const r of rows) {
    const d = r.data || {}, a = r.agent;
    switch (r.type) {
      case 'session.start': meta = { id: r.session, model: d.model, provider: d.provider, isolation: d.isolation, swarm: d.swarm, version: d.version, ttl_s: d.models?.[d.model]?.ttl_s, recon_tokens: d.recon_tokens, shared_hash: d.shared_hash, prices: d.models?.[d.model] }; break;
      case 'agent.spawn': out.push({ t: T(r), e: 'spawn', a: d.id, role: d.role, parent: d.parent || null, task: d.task || null }); break;
      case 'agent.state': out.push({ t: T(r), e: 'state', a: d.id, s: d.state, line: clip(d.line, 70), task: d.task || null }); break;
      case 'board.op': if (d.op !== 'agent') out.push({ t: T(r), e: 'task', op: d.op, id: d.task, status: d.status, owner: d.owner || null, ...(d.title ? { title: clip(d.title, 90) } : {}), ...(d.role ? { role: d.role } : {}), ...(d.deps?.length ? { deps: d.deps } : {}) }); break;
      case 'tool.call': pendingTool[d.id] = { t: T(r), a, tool: d.name, arg: clip(d.input?.path ?? d.input?.command ?? (d.input?.action ? d.input.action + ' ' + (d.input.id ?? d.input.title ?? d.input.task ?? '') : d.input?.to ? d.input.to + ': ' + d.input.text : JSON.stringify(d.input)), 90) }; break;
      case 'tool.result': { const p = pendingTool[d.id]; if (p) { out.push({ t: p.t, e: 'tool', a: p.a, tool: p.tool, arg: p.arg, ms: d.ms, ...(d.error ? { err: true } : {}) }); delete pendingTool[d.id]; } break; }
      case 'model.request': reqSeen[d.req] = { kind: d.kind, role: d.role, sections: d.sections?.map((s) => [s.name, s.tokens]) }; break;
      case 'model.response': { const u = d.usage || {}; const rq = reqSeen[d.req] || {}; const prompt = (u.input_tokens || 0) + (u.cache_read_tokens || 0); out.push({ t: T(r), e: 'req', a, n: Number(String(d.req).split('.').pop().replace(/\D/g, '')) || 0, ...(rq.kind && rq.kind !== 'main' ? { kind: rq.kind } : {}), in: u.input_tokens, read: u.cache_read_tokens, out: u.output_tokens, hit: Math.round((d.hit_ratio || 0) * 100) / 100, ...(d.anomaly ? { anomaly: true } : {}), ...(rq.sections ? { layers: rq.sections } : {}) }); break; }
      case 'cache.anomaly': out.push({ t: T(r), e: 'anomaly', a, req: d.req, kind: d.kind, expected: d.expected_read, actual: d.actual_read }); break;
      case 'compact.commit': out.push({ t: T(r), e: 'compact', a, removed: d.removed_tokens, retained: d.retained_tokens, turns: d.removed_turns, reason: clip(d.reason, 70) }); break;
      case 'mail.send': out.push({ t: T(r), e: 'mail', from: d.from, to: d.to, kind: d.kind, text: clip(d.text, 160) }); break;
      case 'merge.queued': out.push({ t: T(r), e: 'merge', a, outcome: 'queued', task: clip(d.task, 40), position: d.position }); break;
      case 'merge.merged': out.push({ t: T(r), e: 'merge', a, outcome: 'merged', task: clip(d.task, 40), files: d.files, verified: d.verified }); break;
      case 'merge.verify_failed': out.push({ t: T(r), e: 'merge', a, outcome: 'verify_failed', task: clip(d.task, 40), cmd: d.cmd, exit: d.exit_code, output: clip(d.output, 200) }); break;
      case 'merge.rolled_back': out.push({ t: T(r), e: 'merge', a, outcome: 'rolled_back', task: clip(d.task, 40) }); break;
      case 'lease': out.push({ t: T(r), e: 'lease', a: d.agent || a, action: d.action, ...(d.scope ? { scope: d.scope } : {}) }); break;
      case 'perm.decide': out.push({ t: T(r), e: 'perm', a, allow: d.allow, tool: d.tool, command: clip(d.command, 60), by: d.by, reason: clip(d.reason, 140) }); break;
      case 'agent.stuck': out.push({ t: T(r), e: 'stuck', a, phase: d.phase, note: clip(d.note, 160) }); break;
      case 'swarm.integration': out.push({ t: T(r), e: 'integrated', applied: d.applied, files: d.files }); break;
      case 'session.end': meta = { ...meta, end: { t: T(r), cost_usd: d.cost_usd, reason: d.reason } }; break;
    }
  }
  // layers only on an agent's first request; the per-agent summary replaces the rest
  const firstReq = {}, agents = {};
  for (const e of out) if (e.e === 'req') { const g = (agents[e.a] = agents[e.a] || { requests: 0, in: 0, read: 0, out: 0, usd: 0 }); g.requests++; g.in += e.in || 0; g.read += e.read || 0; g.out += e.out || 0; g.usd += e.usd || 0; if (firstReq[e.a]) delete e.layers; else firstReq[e.a] = true; }
  Object.values(agents).forEach((g) => { g.usd = Math.round(g.usd * 1e6) / 1e6; g.hit = Math.round(g.read / Math.max(1, g.in + g.read) * 1000) / 1000; });
  const o = { meta, agents, events: summary ? out.filter((e) => ['spawn', 'task', 'merge', 'compact', 'stuck', 'anomaly', 'mail', 'integrated'].includes(e.e)) : out };
  return JSON.parse(SANI(JSON.stringify(o)));
}
const shop = distil(process.argv[2]);
const handbook = distil(process.argv[3], { ms: true, summary: true });
const screens = {};
for (const v of ['cockpit', 'cache']) { const ls = fs.readFileSync(`real/replay-final-${v}.txt`, 'utf8').replace(/\n+$/, '').split('\n'); const keep = []; let blank = 0; for (const l of ls) { if (/^│ +│$/.test(l)) { if (++blank > 1) continue; } else blank = 0; keep.push(l); } screens[v] = keep.join('\n') + '\n'; }
const HBID = JSON.parse(fs.readFileSync('demo-handbook/session/events.jsonl', 'utf8').split('\n')[0]).session;
fs.writeFileSync('build/replay.json', JSON.stringify({ shop, handbook, finalScreens: screens }).replaceAll('20261009-215641-1be255', '20260101-171204-7c1e3a').replaceAll(HBID, '20260101-171310-d2f6b8').replaceAll('2026-10-09', '2026-01-01'));
const c = {}; shop.events.forEach((e) => (c[e.e] = (c[e.e] || 0) + 1));
console.log('replay.json', fs.statSync('build/replay.json').size, 'shop events', shop.events.length, JSON.stringify(c), 'handbook events', handbook.events.length);
