# Training data and RL: the harness as an environment

Sleipnir is built so that a model can be **reinforcement-trained on Sleipnir's own way of working**, not just
fine-tuned on transcripts. "Our way of working" is more than code edits: it is the layered prompt (pins, notes, spine,
thread, hot tail), the task board, typed mail, `recall`, compaction patches, leases, verifier-gated `done`, and
swarm dispatch without briefings. A model only learns those by acting inside the real harness and being scored on
outcomes, so the harness is packaged as an RL environment:

```
 tasks.jsonl ──► rollout ──► capture ──► reward ──► export ──► trainer (verl / TRL / OpenRLHF / SkyRL / ...)
   (repo + goal     (real harness,    (exact prompt,   (verifier,       (steps / tokens /        │
    + verifier)      policy behind     completion,      cost, protocol)  groups / sft / dpo)      │
                     an OpenAI-style   token ids,                                                 ▼
                     endpoint)         logprobs)                                          new policy checkpoint
                        ▲                                                                         │
                        └─────────────── serve behind vLLM / SGLang, run `sleipnir rl eval` ◄─────┘
```

Every stage is a subcommand (`sleipnir rl ...`), every artefact is plain JSONL plus content-addressed blobs, and
every record is derivable from the event log, so data can also be produced from ordinary (non-RL) sessions.

## 1. Principles

1. **Train on what the model saw.** A sample's prompt is the exact rendered prompt of that call (layers, hot tail,
   tools), reconstructed byte-for-byte from the log and verified against a wire hash. No re-rendering "in the
   spirit of" the original. The policy is trained on the distribution it will be served on.
2. **Verifiable outcomes first.** Rewards come from executable checks (tests, builds, hidden tests, invariants)
   run in a clean environment the agent cannot tamper with. Model-graded signals are optional and never the only
   signal.
3. **Cost is part of the reward, priced like production.** Token, request and wall-clock cost are repriced from the
   recorded prompt recipes under a *target* cache/price model (`internal/cost`), so a policy trained against a local
   vLLM still learns cache-friendly, cheap behaviour for the provider it will be deployed on.
4. **One weight set, many roles.** Worker, manager, reviewer, compactor and mailman are role tags on one model.
   Per-role adapters would split the prefix cache at serving time. Role-specific rewards and per-role advantage
   normalisation make one model learn all of them.
5. **Segments, not just steps.** Between declared rebases (compaction commits, shared epochs) an agent's prompt is
   append-only, so a *segment* can be trained as one packed multi-turn sequence. Each rebase starts a new
   segment; the terminal reward is broadcast across an agent's segments so compaction is trained end to end.
6. **Never fabricate.** Token ids and logprobs are exported only when the endpoint returned them and a
   consistency check passed; otherwise the exporter emits text-level samples and says so.
7. **Governed by default.** Secrets are redacted deterministically, provider terms and repository licences are
   enforced by filters, verifier files are never visible to the policy, and hack detectors flag or zero
   episodes that game the check.

## 2. What is recorded (fidelity contract)

The event log already records every turn (`turn.append`), tool call and result, compaction decision, board, mail and
lease operation. RL adds these fields (all content-addressed blobs; unique text is stored once):

| Event | Added fields | Why |
|---|---|---|
| `model.request` | `agent`, `role`, `kind` (`main`, `compactor`, `mailman`, `recon`), `wire_hash`, `renderer` (version), `manifest` | exact prompt reconstruction; replay verification |
| `model.response` | `completion` (blob: the assistant turn incl. provider-native blocks), `tokens` (blob: `TokenTrace`, when captured), `retries`, `sampling` | the action, its token ids and logprobs |
| `agent.spawn` | `role`, `parent`, `model`, `task`, `scope` | lineage / who trained as what |
| `outcome` | `kind` (`verifier`, `review`, `human`, `protocol`), `score`, `pass`, `detail` (blob), `verifier_version` | reward inputs |

**Manifest.** A prompt is stored as a manifest of blob hashes: `tools` (one blob, identical for every agent), the
`system` blocks, and the messages. Because prompts are append-only between rebases, messages are delta-encoded
against the agent's previous request: `{base: "<prev req>", keep: n, add: ["<hash>", ...]}`, so a request costs a few
dozen bytes and a whole swarm's prompts cost about the size of its unique content. `traj.Prompt(req)` expands a
manifest to the exact `core.Prompt`; `wire_hash` is `sha256` of the canonical provider-neutral prompt JSON. The
replay test (`sleipnir rl verify DIR`) re-expands every request and compares hashes: a mismatch means the data would
not match what the model saw and the episode is rejected.

**Token trace.** With a self-hosted endpoint (vLLM, SGLang) the adapter asks for and stores
`{prompt_ids, completion_ids, logprobs, tokenizer, model_version}` for every call
(`--capture tokens`, default when the endpoint advertises support). It sits at the LLM boundary itself, so no proxy
is needed and no call can bypass it. Endpoints that cannot return ids are captured at text level.

## 3. The environment

### 3.1 Tasks (`tasks.jsonl`, one JSON object per line)

```json
{
 "id": "gorilla-mux-0187",
 "kind": "fix",                      // fix | feature | refactor | swarm | recall | compaction
 "repo": {"path": "/data/repos/mux", "commit": "a1b2c3d", "license": "BSD-3-Clause"},
 "setup": ["go mod download"],       // run once per task snapshot, result cached
 "prompt": "Requests with an encoded slash in the path 404. Fix it.",
 "team": {"mode": "single"},         // or {"mode":"swarm","agents":6,"roles":["backend","tests"]}
 "verifier": {
   "cmd": "go test ./... -run 'TestEncodedSlash|TestRoute' -count=1",
   "timeout_s": 300,
   "pass": "exit0",                  // exit0 | regex:<re> | json-score (last stdout line {"score":0..1})
   "hidden": {"mux_hidden_test.go": "blob:9f2c..."},   // written only at verification time
   "protected": ["*_test.go", "go.mod", ".github/**"]   // agent edits here are discarded and flagged
 },
 "budget": {"steps": 80, "requests": 200, "ite": 400000, "wall_s": 1800, "context_window": 200000},
 "tags": ["go", "http", "small"]
}
```

* **Isolation.** Each rollout gets a fresh worktree (or a copy when the repo is not git) and its own event log
  directory. The verifier runs in a *separate clean checkout* with the agent's diff applied minus protected paths,
  plus the hidden files, so tampering with tests or the verifier has no effect (and is flagged).
* **Budgets** stop runaway episodes; a stop is an outcome (`budget_exceeded`) and is penalised, not silently
  dropped. `context_window` may be set low (for example 32k) to force frequent compaction and so generate dense
  compaction data.
* **Infra errors** (setup failure, verifier crash, provider outage) mark the episode `infra_error`; exporters drop
  such episodes by default so noise is not learned.

### 3.2 Task generators (`sleipnir rl taskgen`)

Hand-written tasks do not scale, so tasks are mined from history:

* `taskgen git`: for commits that change source *and* tests, check out the parent, take the tests as the verifier
  (hidden; protected) and the commit message / linked issue as the prompt. Verified to fail before and pass after.
* `taskgen composite`: combine *k* independent commits that touch disjoint files into one **swarm task**: the
  natural structure for training the manager's dispatch and the workers' coordination (verifier = all tests pass,
  critical path = one commit's work if perfectly parallel).
* `taskgen recall`: run a normal task, then ask a question answerable only from an early detail (an error
  message, a file name, a constant found ten compactions ago); verifier = exact-match check. Trains `recall` use and
  faithful compaction.
* `taskgen mutate`: language-aware bug injection (flip a comparison, drop a nil check) with the project's own tests
  as verifier, for cheap volume.

Splits are by repository and commit date (`--holdout-repos`, `--holdout-after`); `rl eval` refuses to run on tasks
that appear in a training set.

### 3.3 Rollouts (`sleipnir rl rollout`)

```
sleipnir rl rollout --tasks tasks.jsonl --group 8 --concurrency 32 \
    --model my-policy --base-url http://vllm:8000/v1 --capture tokens \
    [--swarm 6] [--role-model manager=my-policy,worker=teacher-model] \
    --target-price anthropic-sonnet --rewards rewards.json --out runs/r001
```

* Runs `group` independent samples per task (the GRPO group) through the *real* harness: real tools, real KV
  layout, real swarm, headless, with permissions set by the task (`accept-edits` inside the worktree, no network
  unless the task allows it).
* One policy endpoint is enough; `--role-model` lets a run train one role against fixed others (for example train the
  manager while workers use a stronger model, or the reverse).
* Output: `runs/r001/<task>/<sample>/` with `events.jsonl`, `blobs/`, `episode.json` (canonical trajectory with
  rewards), `verifier.log`; plus `runs/r001/manifest.json` and `summary.json` (pass rate, cost, per-role stats).

A long-running **rollout server** (`sleipnir rl serve --addr :8787`) exposes the same thing to trainers:
`POST /v1/rollouts {task, policy:{base_url,model,sampling}, group, rewards}` returns episodes (streaming NDJSON),
so verl / OpenRLHF / SkyRL agent loops and online GRPO trainers can call the harness as a black-box environment, in
the same shape as rLLM / Agent Lightning / Polar gateways.

## 4. Canonical trajectory (`sleipnir.rl/1`)

`episode.json` (types in `internal/rl/types.go`):

```
Episode { id, task_id, group, sample, policy{model, endpoint, checkpoint, sampling}, harness{version, renderer,
          config_hash, roles, agents}, env{repo, commit, image, limits}, agents[], edges[], outcome, reward,
          flags[], cost{actual, repriced}, provenance{license, consent, teacher} }
Agent   { id, role, parent, steps[], reward }
Step    { id, kind, epoch, segment, prompt(manifest ref), completion, tokens?, usage, cache, latency_ms,
          observations[], wire_hash, reward?, advantage? }
Edge    { kind: spawn | mail | compact | promote | lease | board, from, to, ref }
```

* `segment` increments at every declared rebase; steps of one segment share an append-only prefix.
* `edges` make the swarm a DAG: a worker's first step points at the manager step that spawned it; a mail delivery
  points from sender step to recipient step; a compaction commit points from the compactor call to the first step
  of the new segment.
* Training units: **step** (one call: prompt to completion), **segment** (a packed multi-turn sequence), or
  **episode** (preference pairs). All are derived, never stored separately.

## 5. Rewards (`internal/rl/reward`, `--rewards rewards.json`)

Every reward is a sum of named, individually logged components, so a run can be re-scored with different weights
without re-running anything.

**Episode (all roles share the team outcome):**

| Component | Definition | Default weight |
|---|---|---:|
| `outcome` | verifier score in [0,1] in the clean checkout; 0 if a hack detector fired | 1.0 |
| `honest_done` | +0.1 if the agent's `done` was accepted and the verifier passed; −0.2 if it claimed done and the verifier failed | 0.1 / −0.2 |
| `cost` | −min(1, repriced ITE / budget.ite) under `--target-price` | 0.15 |
| `requests` | −min(1, requests / budget.requests) | 0.05 |
| `time` | −min(1, critical-path steps / budget.steps) | 0.05 |
| `protocol` | −(invalid tool calls, rejected patches, lease/scope violations, stale writes, budget breaches) / cap | 0.1 |

**Role-specific:**

* **Worker**: episode reward plus `evidence` (ran the checks it later cited), minus re-reads of ranges it had just
  compacted, minus edits outside its scope.
* **Manager**: episode reward plus `parallel_efficiency` (sum of worker steps / critical path, capped), minus
  `duplicate_work`, `conflicts`, `idle_workers`, `over_spawn` (workers whose work did not reach the result).
* **Compactor** (fork of the agent's request, returns a patch): `valid` (patch parses and applies without
  fallback), `size` (spine+retained tokens / thread tokens), `fidelity` (probe recall, below), `rebase_cost`
  (write cost of the commit under the target price model), plus the *downstream* episode reward broadcast to its
  segment chain so the compactor is rewarded for the agent still succeeding.
* **Mailman / mail use**: message `useful` (recipient's next action changed and the episode passed), minus
  unread/ignored, duplicate and low-value messages (hot-tail tokens spent).

**Fidelity probes.** `reward.Probes(archive)` generates deterministic questions from the archived thread (which files
did you modify, which test failed and with what message, what did the user require) and answers them from the
compacted prompt by exact/regex match on facts the compaction was supposed to keep. No model call is needed.

**Hack detectors** (`flag` and zero `outcome`): edits to protected paths; tests deleted, skipped or weakened
(`t.Skip`, `xfail`, assertion removal); verifier or CI config touched; hardcoded expected values from hidden
tests; network access to the upstream repository or solution hosts; writes outside the worktree; `exit 0`
shims. Flags are preserved so the data can also train detectors.

**Counterfactual pricing.** `reward.Reprice(episode, target)` replays the recorded requests through the real
`kv` planner's cost model under the target provider (explicit write premium, TTL, read weights) and returns ITE and
requests. The same episode can therefore be priced for Anthropic-like, OpenAI-like or marketplace-like deployment.

## 6. Advantages and groups (`internal/rl/adv`)

Exporters can attach advantages (or leave raw rewards to the trainer):

* `grpo`: (r − mean) / (std + ε) within the group (same task and policy snapshot), per **role** (a global baseline
  destabilises heterogeneous agents);
* `rloo`: leave-one-out baseline;
* `broadcast`: an agent's steps and segments inherit its (role) reward; compactor steps inherit the reward of the
  chain they served;
* `anchor` (opt-in): steps starting from the same (task, tree hash, failing-test set) are grouped across rollouts
  for step-level credit (GiGPO-style).

Groups whose rewards are all equal (no learning signal) and infra-error or budget-truncated episodes are dropped by
default; `--keep-flat` keeps them.

## 7. Export formats (`sleipnir rl export DIR --format ...`)

| Format | Unit | Shape | Use |
|---|---|---|---|
| `steps` | step | `{id, prompt:[messages], completion:[assistant message], tools, reward, advantage, group_id, role, meta}` in HF conversational form (arguments as JSON objects) | TRL GRPO/RLOO/offline RL, custom trainers |
| `tokens` | step or segment | `{prompt_ids, response_ids, response_mask, old_logprobs, reward, advantage, group_id, role}`; segments are packed only if the prefix property verifies | verl, OpenRLHF, SkyRL, Tinker-style |
| `groups` | task group | `{task, trajectories:[{messages_and_choices, tools, reward, metrics}]}` | ART / rLLM-style |
| `sft` | step or episode | `{messages, tools, weights}` (OpenAI SFT shape; assistant `weight` marks trained turns) of the top-*k* verified episodes per task | rejection-sampling fine-tuning, warm start before RL |
| `dpo` | step or episode | `{prompt, chosen, rejected}`: same prompt hash with different outcomes (compactor patches, anchor states) or same task with best vs worst episode | DPO / preference training |
| `kto` | step | `{prompt, completion, label}` | KTO / BCO |
| `atif` | episode | Harbor ATIF trajectories with `subagent_trajectories` and `context_management` | interchange |
| `canonical` | episode | `episode.json` records with a segment table (`--dedup`) or inline prompts (`--inline`) | lossless archive; everything above derives from it |

Common flags: `--role worker,manager,compactor`, `--min-reward`, `--top-k N`, `--advantage grpo|rloo|none`,
`--drop-flagged`, `--redact` (on by default), `--teacher-ok MODEL,...` (provider-terms filter), `--max-tokens`,
`--split train:0.9,val:0.1` (by repository), `--seed`.

`--inline` writes full prompts per sample (simple; large: shared layers repeat). `--dedup` writes a segment table once
and references it by hash; `sleipnir rl expand` and the Go/Python loaders expand it. Shared prefixes are also flagged
(`shared_prefix_id`) so trainers with prefix or tree packing compute the pinned layers once.

## 8. Governance and data quality

* **Redaction** (deterministic, so identical text redacts identically and prefix sharing survives): secret patterns
  (cloud keys, tokens, private keys, JWTs, high-entropy strings near `key|token|secret|password`), emails, IPs,
  home-directory paths. Runs at export time over prompts, completions and observations.
* **Provider terms.** Episodes record which model produced each completion and whether it is a teacher. Exports
  refuse teacher outputs unless the model is listed in `--teacher-ok`. Do not train competing models on outputs whose
  terms forbid it.
* **Licences and consent.** The repo licence and any consent flag travel with every record; `--licenses` filters.
* **Contamination.** Task splits by repo/date; hidden verifier files never appear in prompts; a scanner flags
  episodes whose transcript contains verifier content or benchmark strings.
* **Dedup and balance.** Exact dedup by prompt hash; per-task caps; `sleipnir rl inspect` prints pass rates, reward
  distributions, per-role step counts, flagged episodes, token totals, cache anomalies and group statistics.

## 9. What each role learns

| Role | Behaviours the data teaches | Signals |
|---|---|---|
| worker | reads the pinned map instead of exploring; edits within scope; runs the verifier before `done`; uses `recall` instead of re-reading | outcome, evidence, cost, protocol |
| manager | dispatches from the board without briefings; right-sized swarms; sleeps in `wait`; integrates and verifies | outcome, parallel efficiency, duplicate work, conflicts |
| compactor | dense, faithful one-line resumes; correct promotions; masks bulky results; minimal rewrites | validity, size, probe fidelity, rebase cost, downstream outcome |
| mailman / mail | few, typed, useful messages | recipient outcome, hot-tail tokens |
| reviewer | finds real defects; does not rubber-stamp | agreement with verifier, false-approve penalty |

## 9.1 Serving the trained model

Use one weight set with role tags (per-role LoRA ids enter vLLM's block hash and would split the shared-prefix
cache). Size KV memory for one shared prefix plus N private tails; keep the same `--cache-key` routing the harness uses
(`prompt_cache_key` / `X-Session-Id`). `sleipnir doctor` measures the served endpoint's cache behaviour like any other.

## 10. Command reference

```
sleipnir rl taskgen git|composite|recall|mutate ...   build tasks.jsonl from repositories
sleipnir rl rollout ...                               run G samples per task through the real harness
sleipnir rl serve --addr :8787                        rollout server for trainers
sleipnir rl reward RUN --rewards rewards.json         (re)score a run with different weights
sleipnir rl export RUN --format steps|tokens|groups|sft|dpo|kto|atif|canonical ...
sleipnir rl verify RUN                                replay check: re-expand prompts, compare wire hashes
sleipnir rl inspect RUN                               dataset statistics and flags
sleipnir rl eval --tasks holdout.jsonl ...            pass@1 / pass^k / cost / protocol metrics, optional --baseline RUN
sleipnir rl expand FILE                               expand a --dedup export to inline prompts
```

Data from ordinary interactive sessions can be exported the same way (`sleipnir rl export SESSION_DIR`): outcome
events, verifier runs the user triggered, accepted/rejected diffs and steering messages become weaker reward
signals, and exporters mark such episodes `weak_label` so they are opt-in for RL.
