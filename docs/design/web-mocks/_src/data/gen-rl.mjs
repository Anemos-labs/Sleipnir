#!/usr/bin/env node
// build/rl.json: the RL fixture set, from the REAL run directories in rl/ (tasks.jsonl, runs/r001, r002, e001).
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const here = path.dirname(fileURLToPath(import.meta.url));
const rl = path.join(here, 'rl');
const J = (p) => JSON.parse(fs.readFileSync(path.join(rl, p), 'utf8'));
const lines = (p) => fs.readFileSync(path.join(rl, p), 'utf8').trim().split('\n').map((l) => JSON.parse(l));
const FIX = '/home/thanos/Documents/Sleipnir/.claude/worktrees/sleipnir-web-interface-cc5a2f/bench/fixtures';
const clip = (s, n) => (s.length > n ? s.slice(0, n - 1).replace(/\s+\S*$/, '') + '…' : s);
const tasks = lines('tasks.jsonl').map((t) => {
  const f = JSON.parse(fs.readFileSync(path.join(FIX, t.id, 'task.json'), 'utf8'));
  return { id: t.id, kind: t.kind, language: f.lang, difficulty: f.difficulty, prompt: clip(t.prompt, 330), promptChars: t.prompt.length, verify: t.verifier.cmd, timeoutS: t.verifier.timeout_s, pass: t.verifier.pass || 'exit0', protected: t.verifier.protected || [], team: t.team || { mode: 'single' }, budget: t.budget || null, tags: t.tags, repo: t.repo.path, commit: t.repo.commit.slice(0, 7), requires: t.requires || [] };
});
const check = J('check.json');
const runDir = (name) => {
  const sum = J(`runs/${name}/summary.json`), man = J(`runs/${name}/manifest.json`);
  const samples = [];
  for (const t of sum.per_task) for (let n = 0; n < sum.group; n++) {
    const p = `runs/${name}/${t.id}/${n}/episode.json`;
    if (!fs.existsSync(path.join(rl, p))) continue;
    const e = J(p);
    samples.push([t.id, n, e.outcome.verifier.pass ? 1 : 0, Math.round(e.reward.total * 1000) / 1000, Math.round(e.cost.usd * 1e6) / 1e6, e.cost.requests, e.signals.steps, e.cost.wall_ms, Math.round(e.cost.usage.cache_read_tokens / Math.max(1, e.cost.usage.input_tokens + e.cost.usage.cache_read_tokens) * 1000) / 1000]);
  }
  return { id: sum.run_id, model: man.policy.model, endpoint: man.policy.endpoint.replace(/:\d+$/, ':8000') , group: sum.group, seed: man.config.seed, targetPrice: man.config.target_price, started: sum.started, durationMs: sum.duration_ms,
    summary: Object.fromEntries(['tasks', 'rollouts', 'completed', 'infra', 'spent_usd', 'pass_rate', 'mean_score', 'mean_reward', 'mean_cost_usd', 'mean_ite', 'mean_requests', 'mean_steps', 'mean_wall_ms', 'hack_rate', 'budget_rate'].map((k) => [k, sum[k]])),
    perTask: sum.per_task.map((t) => [t.id, t.samples, t.passed, Math.round(t.mean_reward * 1000) / 1000, Math.round(t.mean_cost_usd * 1e6) / 1e6, Math.round(t.mean_ite)]),
    sampleFields: ['task', 'sample', 'pass', 'reward', 'usd', 'requests', 'steps', 'wall_ms', 'hit'], samples };
};
const runs = [runDir('r001'), runDir('r002')];
// the eval run
const ev = J('runs/e001/report.json');
// one episode, reduced
const ep = J('runs/r002/go-lru/0/episode.json');
const episode = { id: ep.id, task: ep.task_id, group: ep.group, policy: ep.policy.model, verifier: { pass: ep.outcome.verifier.pass, score: ep.outcome.verifier.score, ms: ep.outcome.verifier.ms }, claimed: ep.outcome.claimed, reward: ep.reward, signals: ep.signals, cost: { ...ep.cost }, agents: ep.agents.map((a) => ({ id: a.id, role: a.role, status: a.status, steps: a.steps.length, segments: a.segments.length })), env: { repo: ep.env.repo, limits: ep.env.limits } };
const mut = lines('mutate.jsonl').map((t) => ({ id: t.id, kind: t.kind, prompt: t.prompt, verify: t.verifier.cmd, tags: t.tags, repo: t.repo.path.replace(/.*\/shop$/, '~/projects/shop') }));
const stepRec = JSON.parse(fs.readFileSync(path.join(rl, 'export-steps.jsonl'), 'utf8').split('\n')[0]);
const exportSample = { schema: stepRec.schema, id: stepRec.id, task_id: stepRec.task_id, group_id: stepRec.group_id, sample: stepRec.sample, role: stepRec.role, step: stepRec.step, segment: stepRec.segment,
  prompt: `[${stepRec.prompt.length} messages: system ${stepRec.prompt[0].content.length} chars, user, ...]`, completion: stepRec.completion, reward: stepRec.reward, advantage: stepRec.advantage, reward_components: stepRec.reward_components };
const out = {
  tasks, check: check.map((c) => ({ id: c.id, ok: c.ok, baseline: c.baseline_score })),
  split: { spec: 'train:0.75,test:0.25', seed: 3, train: lines('split.train.jsonl').map((t) => t.id), test: lines('split.test.jsonl').map((t) => t.id) },
  runs, eval: { id: 'e001', model: 'sample-policy-v2', tasks: ev.tasks, samples: ev.samples, pass_at_1: ev.pass_at_1, mean_reward: ev.mean_reward, usd_total: ev.usd_total, split: 'test' },
  episode, mutate: mut, exportStats: J('export-steps.stats.json'), exportSample,
  rewardComponents: ['outcome', 'honest_done', 'protocol', 'requests', 'time', 'cost', 'role/worker', 'role/compactor'],
  targets: fs.readFileSync(path.join(here, 'real/rl-reward-list-targets.txt'), 'utf8').trim().split('\n'),
};
fs.writeFileSync(path.join(here, 'build/rl.json'), JSON.stringify(out).replaceAll('2026-10-09', '2026-01-01').replace(/127\.0\.0\.1:1809[0-9]/g, '127.0.0.1:8000'));
console.log('rl.json', fs.statSync(path.join(here, 'build/rl.json')).size);
