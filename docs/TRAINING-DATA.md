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

Hand-written tasks do not scale, so tasks are mined from history. Every generator validates what it makes in the
environment rollouts use: the verifier must fail on the start state and pass with the reference solution, and hidden
verifier files and reference solutions go to a blob store (`--blobs`, `blobs/` next to the output) that travels with
the tasks file and never enters a workspace.

* `sleipnir rl taskgen git --repo R`: for commits that change source *and* tests, check out the parent, take the
  tests as the verifier (hidden; protected) and the commit message (or a linked issue, via a resolver) as the prompt.
* `sleipnir rl taskgen mutate --repo R`: language-aware bug injection (flip a comparison, drop a nil check, off by
  one) with the project's own tests as the verifier; only mutations the tests catch, whose reversal makes them pass,
  are kept. Cheap volume, and the source of many independent tasks in one repository state.
* `sleipnir rl taskgen composite TASKS -k 3`: combine *k* independent tasks of one repository (disjoint files) into one
  **swarm task**: the natural structure for training the manager's dispatch and the workers' coordination (the
  verifier passes only when every component passes; its score is the fraction that do).
* `sleipnir rl taskgen recall --repo R`: memory tasks. The agent reads a distinctive constant in one file, reads
  a dozen other files under a small context window (so its early history is compacted), and must then state the
  constant exactly. The verdict is an exact match on the final message, no command and no model. It trains
  faithful compaction and the choice between re-reading and `recall`; the cost components decide which pays.
* `sleipnir rl tasks validate|stats|filter|split|check FILE`: hygiene. `split` assigns whole repositories to
  train/val/test (deterministic), `check` re-proves every task sound, and `rl eval --exclude TRAIN` refuses to
  evaluate on anything in a training list.

### 3.3 Rollouts (`sleipnir rl rollout`)

```
sleipnir rl rollout --tasks tasks.jsonl --group 8 --concurrency 32 \
    --model my-policy --base-url http://vllm:8000/v1 --capture \
    [--swarm 6] [--role-model worker=teacher-model] \
    --target-price anthropic-sonnet --rewards rewards.json --out runs/r001
```

* Runs `group` independent samples per task (the GRPO group) through the *real* harness: real tools, real KV
  layout, real swarm, headless, with permissions set by the task (`accept-edits` inside the worktree, no network
  unless the task allows it).
* One policy endpoint is enough; `--role-model role=model` (repeatable) lets a run train one role against fixed
  others (for example train the manager while workers use a stronger model, or the reverse). Compaction is not a role
  of its own: a compactor is a fork of the agent's request, so it always runs on that agent's model, and the compactor
  data (`compact.*` events, the patch and its fidelity probe) belongs to the policy that was being trained. The policy is named by
  `--model` (a configured provider, or any id with `--base-url`), and its API key is read from the variable named by
  `--api-key-env` in the harness process only: the agent's shell never sees it.
* A swarm rollout is a batch run: the manager is held (a stop guard, bounded to three vetoes) until the board is settled, so
  an episode is not cut short by a manager that answers while workers still run; when the bound is reached the run ends
  and its result says what was left. With `swarm.mailman` on, the mailman's work is data too: `mail.route` (parcels in),
  `mail.batch`, `mail.digest` (digest out, naming every original sender) and `mail.direct` (a delivery that bypassed it).
* The agent's commands run in a scrubbed environment (private HOME and TMPDIR, no credentials, no network when the
  host can isolate it); permission prompts are refused, as an unattended session would refuse them, and the rules let
  the agent edit inside its workspace and run the usual build and test tooling. Steps and requests are hard budgets
  (a stop is an outcome: `budget`), wall-clock is enforced by the runner, and infrastructure failures are retried and
  never become episodes.
* Output: `runs/r001/<task>/<sample>/` with `events.jsonl`, `blobs/`, `episode.json` (canonical trajectory with
  rewards), `diff.patch`, `verifier.log`; plus `runs/r001/manifest.json` and `summary.json` (pass rate, cost,
  per-role stats). Rerunning into the same directory resumes: finished rollouts are skipped.

A long-running **rollout server** (`sleipnir rl serve --addr 127.0.0.1:8090 --runs DIR --tasks registry.jsonl`) exposes the same thing to trainers:
`POST /v1/rollouts {task, policy:{base_url,model,sampling}, group, rewards}` returns episodes (streaming NDJSON),
so verl / OpenRLHF / SkyRL agent loops and online GRPO trainers can call the harness as a black-box environment, in
the same shape as rLLM / Agent Lightning / Polar gateways.

A request names the policy endpoint and, optionally, the environment variable that holds its key
(`policy.api_key_env`). The server sends a key only where its operator said it may go: `--policy-host HOST[:PORT],…`
lists the hosts a request's `base_url` may name (loopback is always allowed) and `--policy-key-env NAME,…` the variables
its `api_key_env` may name (default: none, so a client cannot choose which of the server's credentials to use or
where to send it; a refused request gets a 403 that names the flag). A key to a plain-`http` host that is not this
machine also needs `--allow-insecure-http`, on `rl serve` as on `rl rollout` and `rl eval`.

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

**Hack detectors** (`flag` and zero `outcome`): edits to protected paths (judged by the final diff; a *new* file under a
protected glob, such as a scratch `debug_test.go`, is not an edit of anything and is not flagged, because the verifier
discards it); tests deleted, skipped or weakened
(`t.Skip`, `xfail`, assertion removal); verifier or CI config touched; hardcoded expected values from hidden
tests; network access to the upstream repository or solution hosts; writes outside the worktree; `exit 0`
shims. Flags are preserved so the data can also train detectors.

**`rewards.json`.** A file lists only what it changes; everything else keeps the documented defaults (a weight of 0
switches a component off). All keys are optional:

```json
{
  "weights": { "outcome": 1.0, "cost": 0.3, "requests": 0.0 },
  "caps": { "protocol": 10, "idle": 120000 },
  "target": "anthropic-sonnet",
  "clip": [-1.5, 1.5],
  "probes": true,
  "detectors": { "network": false },
  "workspace_roots": ["/work/rollouts"],
  "reprice": { "engines": 3, "capacity_tokens": 4000000 }
}
```

`weights` and `caps` are keyed by the component and cap names in `internal/rl/reward/config.go` (`outcome`,
`honest_done`, `false_done`, `cost`, `requests`, `time`, `protocol`, `evidence`, `reread`, `scope`,
`parallel_efficiency`, `duplicate_work`, `conflicts`, `idle`, `over_spawn`, `valid`, `size`, `fidelity`, `rebase_cost`,
`downstream`, `mail_useful`, `mail_spam`). `target` is a preset (`sleipnir rl reward -list-targets`: `anthropic-haiku`,
`anthropic-opus`, `anthropic-sonnet`, `marketplace`, `no-cache`, `openai`) or a model id from the price tables. `clip`
bounds every scalar reward (components stay raw). `detectors` switches individual hack detectors off (`protected`,
`tests`, `verifier`, `hardcoded`, `network`, `outside`, `shim`; all run by default). `workspace_roots` tells the
outside-the-worktree detector which directories an agent may write under. `reprice` tunes the counterfactual cache model
(engines, capacity, time-to-first-byte model). Re-score a finished run without re-running anything:
`sleipnir rl reward RUN_DIR --rewards rewards.json [--target-price marketplace] [--dry-run]`.

Things the scorer will not do quietly: an episode whose verifier diff cannot be read is an error (`ErrDiffUnavailable`),
not a pass; a protected pattern that is over 1 KiB or has more than 64 alternatives is refused with the task's field
named, because silently dropped protection is worse than an error; fidelity probes need the prompt text (from the run's
blobs) and are skipped and noted when it is not available, never scored 0. The hack detectors are heuristics: they miss
values built at run time or encoded, expected values edited to match new output, and weakening done through helpers the
diff does not show, which is why flags are kept and the verifier's clean checkout remains the source of truth.

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
| `canonical` | episode | `episode.json` records with a segment table (default) or inline prompts (`--inline`) | lossless archive; everything above derives from it |

Common flags: `--roles worker,manager,compactor`, `--min-reward`, `--top-k N`, `--advantage grpo|rloo|broadcast|anchor|none`
(default: grpo for steps, tokens and groups), `--group-by task|task+policy|group`, `--keep-flagged` (flagged episodes are
dropped by default), `--keep-flat`, `--keep-weak`, `--no-redact` (redaction is on by default), `--redact-salt`,
`--teacher MODEL,...` (provider-terms filter), `--licenses`, `--max-prompt-tokens`, `--max-samples`,
`--split train:0.9,val:0.1` (by repository), `--seed`, `--pack` (pack a segment into one token sequence when its traces
chain), `--reasoning drop|field|keep`, `--stats FILE`. Output files are 0600. Start from these defaults: an exporter that
keeps everything exports infra failures, truncated runs and reward-hacked episodes.

`--inline` writes full prompts per sample (simple; large: shared layers repeat). The default writes a segment table once
and references it by hash (`--table FILE` puts the table in its own file); `sleipnir rl expand EXPORT [--table FILE]`
expands it byte for byte to the inline form. Shared prefixes are also flagged (`shared_prefix`, `shared_messages`) so
trainers with prefix or tree packing compute the pinned layers once.

## 8. Governance and data quality

* **Raw logs are private.** The event log and blobs hold everything the agent saw, including any secret a tool
  printed (`cat .env`). They are written 0600 in 0700 directories and are never the shareable artefact: redaction
  happens at export, not at write time, because redacting a blob would change its content hash and break exact-prompt
  replay. Share exports, not run directories.
* **Redaction** (deterministic, so identical text redacts identically and prefix sharing survives): secret patterns
  (cloud keys, tokens, private keys, JWTs, high-entropy strings near `key|token|secret|password`), emails, IPs,
  home-directory paths. Runs at export time over prompts, completions and observations.
* **Provider terms.** Episodes record which model produced each completion and whether it is a teacher. Exports
  refuse teacher outputs unless the model is listed in `--teacher`. Do not train competing models on outputs whose
  terms forbid it.
* **Licences and consent.** The repo licence and any consent flag travel with every record; `--licenses` filters.
* **Contamination.** Task splits by repo/date; hidden verifier files never appear in prompts; a scanner flags
  episodes whose transcript contains verifier content or benchmark strings.
* **Dedup and balance.** Exact dedup by prompt hash; per-task caps; `sleipnir rl show RUN [TASK/SAMPLE]` prints pass rates, reward
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
sleipnir rl taskgen git|mutate|composite|recall ...   build tasks.jsonl (+ blobs/) from repositories
sleipnir rl tasks validate|stats|filter|split|check   task file hygiene and soundness proofs
sleipnir rl rollout --tasks T --model M --out RUN     run G samples per task through the real harness
sleipnir rl eval --tasks holdout.jsonl --model M      pass@1 / pass^k / cost / protocol metrics, optional --baseline report.json
sleipnir rl serve --addr 127.0.0.1:8090 --runs DIR    rollout server for trainers
sleipnir rl reward RUN --rewards rewards.json         (re)score a run with different weights or target prices
sleipnir rl export RUN --format steps|tokens|groups|sft|dpo|kto|atif|canonical ...
sleipnir rl verify RUN                                replay check: re-expand prompts, compare wire hashes
sleipnir rl show RUN [TASK/SAMPLE]                    run summary; one episode's agents, steps, rewards and flags
sleipnir rl expand EXPORT [--table T]                 expand a deduplicated canonical export to inline prompts
sleipnir inspect SESSION_DIR                          the cache inspector (any recorded session, also each rollout's)
```

Data from ordinary interactive sessions can be exported the same way (`sleipnir rl export SESSION_DIR`): outcome
events, verifier runs the user triggered, accepted/rejected diffs and steering messages become weaker reward
signals, and exporters mark such episodes `weak_label` so they are opt-in for RL.
