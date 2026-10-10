#!/usr/bin/env node
// Assembles build/real.json from the captures in real/ (REAL output of bin/sleipnir on the sample projects), after the
// sanitising that makes them look like the sample user's machine: scratch paths become ~/projects/..., the recorded demo
// session gets a sample id and date. Nothing is invented here; text is only renamed and trimmed.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const here = path.dirname(fileURLToPath(import.meta.url));
const S = process.env.S;
const rd = (p) => fs.readFileSync(path.join(here, p), 'utf8');
const has = (p) => fs.existsSync(path.join(here, p));

const DEMO_ID = '20261009-215641-1be255', SAMPLE_DEMO_ID = '20260101-171204-7c1e3a';
const HB_ID = (() => { try { return JSON.parse(rd('demo-handbook/session/events.jsonl').split('\n')[0]).session; } catch { return ''; } })();
const SAMPLE_HB_ID = '20260101-171310-d2f6b8';
const san = (t) => {
  let s = String(t);
  s = s.replaceAll(`${S}/work/data/demo-shop/session`, `~/.sleipnir/sessions/${SAMPLE_DEMO_ID}`).replaceAll(`${S}/work/data/demo-shop/shop`, '/tmp/sleipnir-demo-3187402914/shop').replaceAll(`${S}/work/data/demo-shop`, '/tmp/sleipnir-demo-3187402914')
    .replaceAll(`${S}/work/data/demo-handbook/session`, `~/.sleipnir/sessions/${SAMPLE_HB_ID}`).replaceAll(`${S}/work/data/demo-handbook/handbook`, '/tmp/sleipnir-demo-3187402915/handbook').replaceAll(`${S}/work/data/demo-handbook`, '/tmp/sleipnir-demo-3187402915')
    .replaceAll(`${S}/work/data/home2`, '~').replaceAll(`${S}/work/data/home`, '~').replaceAll(`${S}/work/data/shop-final`, '~/projects/shop').replaceAll(`${S}/work/data/shop`, '~/projects/shop')
    .replaceAll('<scratch>/tmp/initproj', '~/projects/newapp').replaceAll(`${S}/work/data/rl/`, '').replaceAll(`${S}/work/data/rl`, '.').replaceAll(`${S}/work/data`, '<scratch>').replaceAll(S, '<scratch>');
  s = s.replaceAll(DEMO_ID, SAMPLE_DEMO_ID);
  if (HB_ID) s = s.replaceAll(HB_ID, SAMPLE_HB_ID);
  s = s.replaceAll('2026-10-09T21:56:41', '2026-01-01T17:12:04').replaceAll('2026-10-09T21:57:02', '2026-01-01T17:12:25');
  return s;
};
const parseCap = (name) => {
  const t = rd(`real/${name}.cap`);
  const blocks = [];
  for (const chunk of t.split(/^### /m).slice(1)) {
    const nl = chunk.indexOf('\n');
    let cmd = chunk.slice(0, nl).replace(/^\(in [^)]*\) /, '').replace(/^sleipnir ?/, '');
    let body = chunk.slice(nl + 1);
    const m = /\n?\[exit (\d+)\]\n?$/.exec(body);
    const exit = m ? +m[1] : 0;
    if (m) body = body.slice(0, m.index);
    blocks.push({ cmd: san(cmd), text: san(body.replace(/\n+$/, '')), exit });
  }
  return blocks;
};

// the sample world: the clock of the recording is moved to the sample day, the scratch ports to the usual ones
const finalize = (txt, extra = []) => {
  let t = txt.replaceAll('2026-10-09', '2026-01-01').replaceAll('20261009-215641-1be255', '20260101-171204-7c1e3a').replace(/127\.0\.0\.1:1809[0-9]/g, '127.0.0.1:8000');
  for (const [a, b] of extra) t = t.replaceAll(a, b);
  return t;
};
const R = {};
R.basic = parseCap('basic');
R.recon = parseCap('recon');
R.config = parseCap('config');
R.trust = parseCap('trust');
R.mcp = parseCap('mcp');
R.schedule = parseCap('schedule');
R.models = parseCap('models');
R.init = parseCap('init');
R.mock = san(rd('real/mock.cap')).trim();
// the trust ledger as the binary wrote it (the day is rewritten to the sample day)
{
  const j = JSON.parse(san(rd('real/trust.json')));
  const e = Object.values(j.projects)[0];
  R.trustLedger = { digest: e.digest, files: e.files, saved: '2026-01-01' };
  for (const b of R.trust) {
    b.text = b.text.replace(/\d{4}-\d{2}-\d{2}/g, (d) => (d === new Date().toISOString().slice(0, 10) ? '2026-01-01' : d));
    // the binary shortens a long directory with an ellipsis; the sample user's directory is short
    b.text = b.text.replace(/\/tmp\/[^\s:]*…/g, '~/projects/shop');
    if (/^DIRECTORY/.test(b.text)) { const rows = b.text.split('\n').map((l) => l.split(/ {2,}/)); const w = rows[0].map((_, i) => Math.max(...rows.map((r) => (r[i] || '').length))); b.text = rows.map((r) => r.map((c, i) => (i < r.length - 1 ? c.padEnd(w[i] + 2) : c)).join('')).join('\n'); }
  }
}
// config: the effective configuration at each layer (for the origin table), the files, and the JSON outputs
R.configLayers = { defaults: JSON.parse(rd('real/config-defaults.json')), user: JSON.parse(rd('real/config-user.json')), project: JSON.parse(rd('real/config-project.json')) };
R.configFiles = { user: rd('real/user-config.json'), project: rd('real/project-config.json'), projectMcp: rd('real/project-mcp.json'), agents: rd('real/project-agents.md') };
R.initFiles = { userConfig: rd('real/init-user-config.json'), projectConfig: rd('real/init-project-config.json'), agents: rd('real/init-agents.md') };

// demo reports (the recorded sessions are sample sessions of the sample user)
R.demo = { shop: san(rd('real/demo-shop.stdout')).trim(), handbook: san(rd('real/demo-handbook.stdout')).trim() };
// friction (text, json) and inspect, from the recorded demo session
R.friction = { json: JSON.parse(san(rd('real/friction.json')).replace(/data\/demo-shop\/session/g, `~/.sleipnir/sessions/${SAMPLE_DEMO_ID}`)) };
{
  const ins = JSON.parse(san(rd('real/inspect-shop.json')));
  const pts = ins.series.points.map((p) => [p.i, p.t, p.n, p.prompt, p.in, p.read, p.out, Math.round(p.hit * 1000) / 1000, p.usd, p.anom, p.commits]);
  ins.series = { fields: ['i', 't_ms', 'n_agents', 'prompt', 'in', 'read', 'out', 'hit', 'usd', 'anom', 'commits'], points: pts, group: ins.series.group, window: ins.series.window };
  delete ins.rl; delete ins.log.types; ins.log.types = '(38 event types: see DATA.md)';
  ins.session.root = 'shop'; ins.session.goal = ins.session.goal;
  R.inspect = ins;
}
// doctor: real reports against the local mock endpoint, and the real failure against a closed port
R.doctor = {
  mockDeep: JSON.parse(san(rd('real/doctor-mock-deep.json'))),
};
// rl: every captured block
const rlText = (f) => san(rd(`real/${f}`)).trim();
R.rl = {
  taskgenFixture: rlText('rl-taskgen-fixture.txt'),
  validate: rlText('rl-tasks-validate.txt'), stats: rlText('rl-tasks-stats.txt'), check: rlText('rl-tasks-check.txt'),
  rolloutV1: rlText('rl-rollout-v1.txt'), rolloutV2: rlText('rl-rollout-v2.txt'), rolloutErr: rlText('rl-rollout-v1.err').split('\n').slice(0, 3).join('\n'),
  report: rlText('rl-report.txt'), compare: rlText('rl-compare.txt'), show: rlText('rl-show.txt').split('\n').filter((l, i, a) => !/^(go|js|py|rs)-[a-z]+\/\d  /.test(l) || /\/0 /.test(l)).join('\n'), evalTaskgen: rlText('rl-eval-taskgen.txt'),
  listTargets: rlText('rl-reward-list-targets.txt'),
  compareJson: JSON.parse(rd('real/rl-compare.json')),
  reportR002: JSON.parse(rd('real/rl-report-r002.json')),
};
{ // per_task is in rl.json (runs[].perTask); the report keeps its headline, by_tag, by_role and identity
  const rr = R.rl.reportR002; for (const k of ['per_task', 'created', 'attempts', 'ite', 'wall_ms', 'steps']) delete rr[k];
}
fs.mkdirSync(path.join(here, 'build'), { recursive: true });
const HB = (() => { try { return JSON.parse(rd('demo-handbook/session/events.jsonl').split('\n')[0]).session; } catch { return ''; } })();
for (const k of ['schedule', 'models', 'init', 'mock']) delete R[k];   // unused by outputs.js: those outputs are computed from the sample state
fs.writeFileSync(path.join(here, 'build/real.json'), finalize(JSON.stringify(R), HB ? [[HB, SAMPLE_HB_ID]] : []));
const sz = {}; for (const [k, v] of Object.entries(R)) sz[k] = JSON.stringify(v).length;
console.log('real.json', fs.statSync(path.join(here, 'build/real.json')).size, JSON.stringify(sz));
