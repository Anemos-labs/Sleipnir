#!/usr/bin/env node
// Reads the scratch shop repository (mkshop.sh) and writes build/shop.json: the tree, the contents, the unified diffs per
// checkpoint (parsed into hunks), per-line authorship (git blame) and the file versions the time-travel scrubber needs.
// Everything here is computed from real git output on the sample files; nothing is typed by hand except the step table.
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const PROJECT = process.env.PROJECT || 'shop';
const P = path.join(here, PROJECT);
const git = (...a) => execFileSync('git', a, { cwd: P, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'], env: { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' }, maxBuffer: 64 << 20 });

// ---- the step table: checkpoint id -> time, label, what the swarm called it. c04 and older touched nothing.
const CFG = {
  shop: {
    steps: [
      { id: 'c05', tag: 'c05', time: '03:04:21', label: 'seed items + loader', live: true },
      { id: 'c06', tag: 'c06', time: '03:04:29', label: 'T5 cart total in cents', live: true },
      { id: 'c07', tag: 'c07', time: '03:04:38', label: 'T4 catalogue handler', live: true },
      { id: 'c08', tag: 'c08', time: '03:04:52', label: 'T7 tests: catalogue contract', live: false },
      { id: 'c09', tag: 'c09', time: '03:04:56', label: 'T5 cart Remove', live: false },
      { id: 'c10', tag: 'c10', time: '03:05:01', label: 'T6 item cards, pager and styles', live: false },
      { id: 'c11', tag: 'c11', time: '03:05:03', label: 'T7 tests: cart', live: false },
      { id: 'c12', tag: 'c12', time: '03:05:05', label: 'T4 document the last-page rule', live: false },
    ],
    snapTag: 'c07', finalTag: 'c12',
    project: { name: 'shop', root: '~/projects/shop', module: 'example.com/shop', branch: 'main', go: '1.25' },
    untracked: ['.env', 'secrets/signing.pem'],
    leaseGlobs: [
      { agent: 'be-1', task: 'T4', globs: ['api/catalog/**', 'api/server.go'] },
      { agent: 'be-2', task: 'T5', globs: ['api/cart/**'] },
      { agent: 'fe-1', task: 'T6', globs: ['web/**'] },
      { agent: 'ts-1', task: 'T7', globs: ['api/**/*_test.go'] },
    ],
    protectedRule: (p) => ({ rule: p === '.env' ? 'Read(./.env)' : 'Read(./secrets/**)', origin: 'project config', tier: 'deny', why: p === '.env' ? 'denied by the project and guarded by a built-in protection (.env files)' : 'denied by the project' }),
  },
  orders: {
    steps: [
      { id: 'c01', tag: 'c01', time: '02:54:49', label: 'turn 1: the pagination test in ./orders is failing, fix it', live: true },
      { id: 'c02', tag: 'c02', time: '03:03:21', label: 'turn 2: yes, add a test for page 0 and a negative size', live: true },
    ],
    snapTag: 'c02', finalTag: 'c02',
    project: { name: 'orders-api', root: '~/projects/orders-api', module: 'example.com/orders-api', branch: 'main', go: '1.25' },
    untracked: [],
    leaseGlobs: [],
    protectedRule: () => null,
  },
}[PROJECT];
export const STEPS = CFG.steps;
const order = ['base', ...STEPS.map((s) => s.tag)];

// ---- parse a git unified diff into files -> hunks -> lines
function parseDiff(text) {
  const files = [];
  let f = null, h = null;
  for (const raw of text.split('\n')) {
    if (raw.startsWith('diff --git ')) {
      const m = /^diff --git a\/(.+) b\/(.+)$/.exec(raw);
      f = { path: m[2], status: 'modified', oldPath: m[1], added: 0, removed: 0, hunks: [] };
      files.push(f); h = null; continue;
    }
    if (!f) continue;
    if (raw.startsWith('new file mode')) { f.status = 'added'; continue; }
    if (raw.startsWith('deleted file mode')) { f.status = 'deleted'; continue; }
    if (raw.startsWith('index ') || raw.startsWith('--- ') || raw.startsWith('+++ ')) continue;
    const hm = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$/.exec(raw);
    if (hm) { h = { oldStart: +hm[1], oldLines: hm[2] === undefined ? 1 : +hm[2], newStart: +hm[3], newLines: hm[4] === undefined ? 1 : +hm[4], section: hm[5], lines: [] }; f.hunks.push(h); continue; }
    if (!h) continue;
    if (raw === '') continue;
    const t = raw[0];
    if (t === '+') { h.lines.push({ t: '+', s: raw.slice(1) }); f.added++; }
    else if (t === '-') { h.lines.push({ t: '-', s: raw.slice(1) }); f.removed++; }
    else if (t === ' ') h.lines.push({ t: ' ', s: raw.slice(1) });
    else if (t === '\\') continue;
  }
  return files;
}

// ---- who wrote what, from commit authors
const commits = git('log', '--reverse', '--format=%H%x09%an%x09%s', CFG.finalTag).trim().split('\n').map((l) => { const [sha, who, subj] = l.split('\t'); return { sha, who, subj }; });
const tagOf = {};
for (const t of order) tagOf[git('rev-list', '-n1', t).trim()] = t;
let cur = 'base';
for (const c of commits) { cur = tagOf[c.sha] ? tagOf[c.sha] : cur; }
// a commit belongs to the first tag at or after it
const stepOfCommit = {};
{
  const tagsAfter = order.slice(1);
  let i = 0;
  const idx = {}; commits.forEach((c, k) => (idx[c.sha] = k));
  const tagIdx = tagsAfter.map((t) => idx[git('rev-list', '-n1', t).trim()]);
  commits.forEach((c, k) => {
    if (k === 0) { stepOfCommit[c.sha] = 'base'; return; }
    for (let j = 0; j < tagIdx.length; j++) if (k <= tagIdx[j]) { stepOfCommit[c.sha] = tagsAfter[j]; return; }
  });
}
const taskOf = (subj) => (/^(T\d):/.exec(subj) || [])[1] || '';

// ---- files
const trackedFinal = git('ls-tree', '-r', '--name-only', CFG.finalTag).trim().split('\n');
const trackedBase = git('ls-tree', '-r', '--name-only', 'base').trim().split('\n');
const untracked = CFG.untracked; // present on disk, ignored by git, denied to the agents
const show = (rev, p) => { try { return git('show', `${rev}:${p}`); } catch { return null; } };
const sizeOf = (s) => (s == null ? 0 : Buffer.byteLength(s));

const changedPaths = new Set();
const diffsByStep = {};
let prev = 'base';
for (const s of STEPS) {
  const files = parseDiff(git('diff', '--no-color', '-U3', '--no-renames', prev, s.tag));
  // authors per file for this step
  const range = git('log', '--format=%H', `${prev}..${s.tag}`).trim().split('\n').filter(Boolean);
  const authors = {};
  for (const sha of range) {
    for (const p of git('show', '--name-only', '--format=', sha).trim().split('\n').filter(Boolean)) {
      (authors[p] = authors[p] || new Set()).add(commits.find((c) => c.sha === sha).who);
    }
  }
  files.forEach((f) => { f.agents = [...(authors[f.path] || [])]; f.task = taskOf(commits.find((c) => range.includes(c.sha) && git('show', '--name-only', '--format=', c.sha).includes(f.path))?.subj || ''); changedPaths.add(f.path); });
  files.forEach((f) => { if (/seed\/items\.json$/.test(f.path)) f.hunks.forEach((h) => { const ch = h.lines.filter((l) => l.t !== ' '); if (ch.length > 16) { const keep = []; let nm = 0, np = 0; for (const l of h.lines) { if (l.t === '-' && nm < 4) { keep.push(l); nm++; } else if (l.t === '+' && np < 4) { keep.push(l); np++; } else if (l.t === ' ') keep.push(l); } h.lines = keep; h.lines.push({ t: '…', s: (f.removed - 4) + ' more removed and ' + (f.added - 4) + ' more added lines, the same change for the other items' }); h.truncated = true; } }); });
  diffsByStep[s.id] = files;
  prev = s.tag;
}
const snapshotDiff = parseDiff(git('diff', '--no-color', '-U3', '--no-renames', 'base', CFG.snapTag));
const finalDiff = parseDiff(git('diff', '--no-color', '-U3', '--no-renames', 'base', CFG.finalTag));

// ---- blame
function blame(rev, p) {
  const out = git('blame', '--line-porcelain', rev, '--', p).split('\n');
  const lines = [];
  let sha = '';
  for (const l of out) {
    const m = /^([0-9a-f]{40}) \d+ \d+/.exec(l);
    if (m) sha = m[1];
    if (l.startsWith('\t')) lines.push(sha);
  }
  const runs = [];
  lines.forEach((s, i) => {
    const c = commits.find((x) => x.sha === s);
    const step = stepOfCommit[s];
    const who = step === 'base' ? '-' : c.who, task = step === 'base' ? '' : taskOf(c.subj);
    const last = runs[runs.length - 1];
    if (last && last[2] === who && last[3] === (step === 'base' ? '' : step)) last[1]++;
    else runs.push([i + 1, 1, who, step === 'base' ? '' : step, task]);
  });
  return runs; // [firstLine, count, agent, checkpoint, task]; '-' = written before the session
}

// ---- tree
const leaseGlobs = CFG.leaseGlobs;
const globRe = (g) => new RegExp('^' + g.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*\*\//g, '(?:.*/)?').replace(/\*\*/g, '.*').replace(/\*/g, '[^/]*') + '$');
function leaseOf(p) {
  // the most specific glob wins (longest pattern): ts-1's test glob beats be-1's directory for *_test.go
  let best = null;
  for (const l of leaseGlobs) for (const g of l.globs) if (globRe(g).test(p) && (!best || g.length > best.g.length || (g.includes('_test') && !best.g.includes('_test')))) best = { l, g };
  return best ? { agent: best.l.agent, task: best.l.task, glob: best.g } : null;
}
const askPaths = (p) => p.startsWith('.sleipnir/') || p === '.mcp.json' && false;
const lastStepOf = (p) => { let r = null; for (const s of STEPS) if ((diffsByStep[s.id] || []).some((f) => f.path === p)) r = s; return r; };
const snapStatus = (p) => {
  const inSnap = snapshotDiff.find((f) => f.path === p);
  return inSnap ? (inSnap.status === 'added' ? 'A' : inSnap.status === 'deleted' ? 'D' : 'M') : '-';
};
const finalStatus = (p) => {
  const f = finalDiff.find((x) => x.path === p);
  return f ? (f.status === 'added' ? 'A' : f.status === 'deleted' ? 'D' : 'M') : '-';
};
const allPaths = [...new Set([...trackedFinal, ...untracked])].sort();
const writerUpTo = (p, lastTag) => {
  let w = null;
  for (const s of STEPS) {
    (diffsByStep[s.id] || []).filter((f) => f.path === p).forEach((f) => f.agents.forEach((a) => { w = { agent: a, task: f.task, cp: s.id }; }));
    if (s.tag === lastTag) break;
  }
  return w;
};
const tree = allPaths.map((p) => {
  const prot = untracked.includes(p);
  const ws = writerUpTo(p, CFG.snapTag), wf = writerUpTo(p, CFG.finalTag);
  const base = show('base', p), fin = show(CFG.finalTag, p), snap = show(CFG.snapTag, p);
  return {
    path: p,
    dir: path.dirname(p) === '.' ? '' : path.dirname(p),
    kind: /\.(go|js|html|css|json|md)$/.test(p) || p === 'Makefile' || p === '.gitignore' ? 'text' : 'other',
    sizeSnapshot: prot ? (p === '.env' ? 96 : 38) : sizeOf(snap == null ? base : snap),
    sizeFinal: prot ? (p === '.env' ? 96 : 38) : sizeOf(fin),
    status: snapStatus(p),          // at the snapshot: A added, M modified, D deleted, - unchanged
    finalStatus: finalStatus(p),    // after the run
    existsAtSnapshot: prot ? true : (snap != null || base != null),
    owner: ws ? ws.agent : null,    // agent that last wrote it up to the snapshot
    task: ws ? ws.task : null,
    checkpoint: ws ? ws.cp : null,
    ownerFinal: wf ? wf.agent : null, taskFinal: wf ? wf.task : null, checkpointFinal: wf ? wf.cp : null,
    lease: prot ? null : leaseOf(p),
    protected: prot ? CFG.protectedRule(p) : null,
    ask: askPaths(p) ? { rule: 'Edit(./.sleipnir/**)', why: 'built-in protection: a write into the config directory always asks, in every mode' } : null,
    ignoredByGit: prot,
  };
});

// ---- contents (snapshot = state at 03:04:43, final = after the run) for every tracked text file
const contents = {};
for (const p of trackedFinal) {
  const base = show('base', p), snap = show(CFG.snapTag, p), fin = show(CFG.finalTag, p);
  if (/seed\/items\.json$/.test(p)) {
    const head = (s) => (s == null ? null : s.split('\n').slice(0, 8).join('\n') + '\n  ...\n]\n');
    contents[p] = { base: head(base), snapshot: head(snap), final: head(fin), note: 'first 6 of 48 rows' };
    continue;
  }
  const o = { base, snapshot: snap, final: fin };
  // drop duplicates to save bytes: a field equal to the previous one points at it
  if (o.snapshot === o.base) o.snapshot = '=base';
  if (o.final === o.snapshot) o.final = '=snapshot';
  else if (o.final === o.base && o.snapshot !== '=base') o.final = '=base';
  contents[p] = o;
}
// the same file at every checkpoint where it changed (the scrubber): [[cp, content]] from base
const versions = {};
for (const p of changedPaths) {
  const vs = [['c04', show('base', p)]];
  let last = vs[0][1];
  for (const s of STEPS) { const c = show(s.tag, p); if (c !== last) { vs.push([s.id, c]); last = c; } }
  if (/seed\/items\.json$/.test(p)) { versions[p] = vs.map(([k, c]) => [k, c == null ? null : c.split('\n').slice(0, 8).join('\n') + '\n  ...\n]\n']); continue; }
  versions[p] = vs;
}

// ---- blame at the snapshot and at the end, only for files the session touched
const blameSnap = {}, blameFinal = {};
for (const p of changedPaths) {
  if (show(CFG.snapTag, p) != null) blameSnap[p] = blame(CFG.snapTag, p);
  if (show(CFG.finalTag, p) != null) blameFinal[p] = blame(CFG.finalTag, p);
}

// ---- go test of the final tree (recorded by the build script into build/gotest.txt)
// the stored seed excerpt is its first 8 lines: cut the blame runs to the same lines
for (const m of [blameSnap, blameFinal]) for (const p of Object.keys(m)) if (/seed\/items\.json$/.test(p)) { let left = 8; m[p] = m[p].map((r) => { const n = Math.min(r[1], Math.max(0, left - (r[0] - 1))); return n > 0 ? [r[0], n, r[2], r[3], r[4]] : null; }).filter(Boolean); }
const out = {
  project: CFG.project,
  snapshotCp: CFG.snapTag, finalCp: CFG.finalTag,
  steps: STEPS,
  commits: commits.map((c) => ({ sha: c.sha.slice(0, 7), agent: c.who, step: stepOfCommit[c.sha], subject: c.subj })),
  tree,
  contents: Object.fromEntries(Object.entries(contents).filter(([p]) => !changedPaths.has(p)).map(([p, v]) => [p, v.base != null ? v.base : (v.snapshot != null ? v.snapshot : v.final)])),
  diffs: Object.fromEntries(Object.entries(diffsByStep).map(([k, v]) => [k, v.map((f) => ({ path: f.path, status: f.status, added: f.added, removed: f.removed, agents: f.agents, task: f.task }))])),   // hunks: S.stepDiff() derives them from the versions (checked against git: gen-check)
  gitHunks: Object.fromEntries(Object.entries(diffsByStep).map(([k, v]) => [k, v.map((f) => [f.path, f.hunks.map((h) => [h.oldStart, h.oldLines, h.newStart, h.newLines])])])),
  stat: { snapshot: snapshotDiff.map((f) => ({ path: f.path, status: f.status, added: f.added, removed: f.removed })), final: finalDiff.map((f) => ({ path: f.path, status: f.status, added: f.added, removed: f.removed })) },
  versions,
  blameSnapshot: blameSnap,
  blameFinal: blameFinal,
  leases: leaseGlobs,
};
fs.mkdirSync(path.join(here, 'build'), { recursive: true });
fs.writeFileSync(path.join(here, `build/${PROJECT}.json`), JSON.stringify(out));
const stat = (a) => a.map((f) => `${f.path} (${f.status}, +${f.added} -${f.removed})`).join('; ');
console.log(PROJECT + '.json', fs.statSync(path.join(here, `build/${PROJECT}.json`)).size, 'bytes');
for (const s of STEPS) console.log(s.id, stat(diffsByStep[s.id]));
console.log('snapshot:', stat(snapshotDiff));
console.log('final:', stat(finalDiff));
