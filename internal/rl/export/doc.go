// Package export writes canonical episodes (rl.Episode, built by package traj and
// scored by the reward package) as trainer-ready JSON lines.
//
//	o := export.DefaultOptions(export.FormatSteps) // DropFlagged on; a bool cannot default to true
//	o.Redactor = red
//	st, err := export.Export(w, []export.Source{{Episode: ep, Prompts: traj.Resolver{Run: run}}}, o)
//
// # Principles
//
// The prompt of a sample is what the model saw. Prompts are never re-rendered:
// they come from the resolver (the exact prompt, verified against its wire hash by
// traj) and are turned into chat messages with openaichat.Build, the same function
// that produced the bytes the endpoint received. A trainer that reads a steps
// record therefore trains on the identical messages and tools.
//
// Token ids and logprobs are exported only as the endpoint returned them. They
// are never re-tokenised, repaired or invented; a step without a consistent trace
// is skipped in the token formats and counted. Because ids encode the original
// text, a step whose text the redactor changes never exports its ids.
//
// Output is deterministic: episodes are ordered by (group, task, sample, id),
// every map is written in key order, and nothing depends on the clock or on the
// order in which the caller listed the episodes.
//
// # Pipeline
//
// Export applies, in this order:
//
//  1. drop episodes carrying a hard flag (rl.HardFlag: infra_error, truncated,
//     replay_mismatch, contaminated, hack:*), and for the training formats
//     weak_label episodes unless KeepWeak. token_mismatch counts only for the
//     tokens format. DropFlagged=false disables all of it.
//  2. the Advantage hook, on clones of the kept episodes (the caller's episodes
//     are never modified);
//  3. drop zero-variance groups (RL formats: steps, tokens, groups) unless KeepFlat:
//     a group whose rewards are equal for every role, with no non-zero advantage,
//     teaches nothing;
//  4. the licence allow-list (Licenses);
//  5. per-step filters: Roles, then the provider-terms rule (below), then
//     MaxPromptTokens;
//  6. redaction of every string (prompts, tools, completions, tool arguments,
//     observations);
//  7. split assignment (by repository) and the MaxSamples cap.
//
// Provider terms. A step is trainable only if its model is the policy under
// training. The RL formats export trainable steps only. sft, dpo and kto may also
// use a non-policy ("teacher") model's completions as targets, but only when the
// model is listed in TeacherOK; every other teacher step is skipped and counted in
// Stats.TeacherSkipped, and appears in Stats.TeacherUsed when it was allowed.
// canonical and atif are archives: they keep teacher steps, marked
// "trainable": false, and count them as archived in Stats.TeacherUsed.
//
// # Rewards and advantages
//
// A record's reward is the most specific one available: a non-zero step reward,
// else the agent's role reward when the reward package filled one, else the
// episode reward. "reward_components" are the components of that same source.
// "advantage" is present only when an Advantage hook ran or the step already
// carried one; it is never a fabricated zero.
//
// # Formats
//
// Every record has a "schema" and, when Options.Split is set, a "split".
// Message and tool shapes are those of an OpenAI-style chat endpoint.
//
// steps (schema sleipnir.rl.step/1), one record per step, Hugging Face
// conversational form:
//
//	id                  "<episode id>#<request id>"
//	task_id, group_id, sample, role, agent
//	step                index of the step within its agent
//	segment             segment index of the step
//	prompt              []message: the exact wire messages (system first), tool-call
//	                    arguments as JSON objects
//	completion          [one assistant message]; reasoning per Options.Reasoning:
//	                    drop (default), field (reasoning_content) or keep
//	                    (provider-native reasoning_details)
//	tools               the wire tools array
//	reward, advantage, reward_components
//	meta                policy, harness, kind, model, req, epoch, stop, usage, cache,
//	                    wire_hash, shared_prefix, shared_messages, trainable, flags,
//	                    license
//
// tokens (sleipnir.rl.tokens/1), one record per step, or per (agent, segment) with
// PackSegments:
//
//	id, task_id, group_id, sample, role, agent, segment, model, tokenizer, model_version
//	steps               request ids the record covers
//	packed              true for a packed segment
//	prompt_ids          ids of the first prompt
//	response_ids        the response; packed: C0 ++ obs0 ++ C1 ++ obs1 ... ++ Cn
//	response_mask       1 on completion tokens, 0 on observation tokens
//	old_logprobs        the sampled logprobs, 0 where masked; absent when the
//	                    endpoint returned none
//	reward, advantage
//
// A segment is packed only when every main step in it is exportable, carries a
// consistent trace, the traces chain (P[i+1] starts with P[i] ++ C[i] token for
// token; obs[i] is the rest of P[i+1]), tokenizer and model version agree, and one
// reward and advantage describe them all. Otherwise it falls back to per-step
// records and Stats.PackFailed counts the reason: not_trainable, no_trace, redacted,
// prefix_mismatch, logprobs_partial, tokenizer_changed, model_version_changed,
// credit_varies. Compactor calls are separate samples and are never part of a chain.
//
// groups (sleipnir.rl.group/1), one record per GRPO group in the ART / rLLM shape:
//
//	task_id, group
//	trajectories        one per (episode, agent, segment) and one per side call:
//	                    id, episode, sample, agent, role, segment, steps,
//	                    messages_and_choices, tools, reward, advantage, metrics, rank
//
// messages_and_choices holds wire messages, with each model turn replaced by a
// choice {finish_reason, index, message, logprobs}; logprobs.content lists
// {token: "token_id:<id>", logprob} from the step's trace, or is null.
//
// sft (sleipnir.rl.sft/1), OpenAI supervised fine-tuning shape:
//
//	messages            wire messages, tool-call arguments as JSON strings; assistant
//	                    messages carry "weight" (1 on trained turns, else 0)
//	tools, weights      weights aligns with messages
//	steps               request ids whose completions are trained in the record
//
// One record per step (only the completion trained) or, with PackSegments, per
// (agent, segment) with every selected model turn trained. Episodes are selected
// by verifier verdict (a failing verdict is out), reward >= MinReward and the best
// TopK per task by reward; with Select "best", the best TopK (default 1) verified
// episodes of each rollout group by the best-of-n ranking (rl.CompareRank).
//
// Ranks. sft records, groups trajectories and both sides of a dpo pair carry the
// episode's place in its rollout group (Episode.Group, else the task), among the
// episodes that reached the format writer: {position (1 is the best), group_size,
// key (rl.RankKey: verified, score, ite, waste, final_answer_chars)}.
//
// dpo (sleipnir.rl.dpo/1): {id, unit, task_id, role, prompt, chosen, rejected,
// tools, chosen_reward, rejected_reward, chosen_episode, rejected_episode,
// chosen_rank, rejected_rank, wire_hash}. unit "step": the same exact prompt (wire hash) with different
// completions in different episodes, best against worst by episode reward (the gap
// must be positive and chosen >= MinReward). unit "episode": per task, the best
// and worst episode whose root agent never rebased and started from the same
// prompt, comparing everything the root did after that prompt; a pair whose two
// continuations are equal is dropped (pair:same_continuation). With Pair
// "best-worst" both units compare within one rollout group and order by the
// best-of-n ranking instead of the reward: the chosen side is rank 1, which must
// be verified, and the rejected side the lowest-ranked one with a strictly worse
// rank key.
//
// kto (sleipnir.rl.kto/1): {prompt, completion, label, reward, ...}; label is the
// episode's verifier verdict, or reward >= MinReward without one.
//
// atif: one Agent Trajectory Interchange Format object per episode: the root
// agent's steps (user instruction, then agent steps with tool_calls, observation
// results, metrics with tokens, cached tokens and token ids), subagent_trajectories
// for every other agent (spawn calls reference them from their observation),
// context_management on the first step of each new segment, and final_metrics. It
// is a SUBSET of Harbor's ATIF as summarised in docs/TRAINING-DATA.md; field names and the schema_version string have not been validated
// against Harbor's RFC and must be before the output is published as ATIF.
// Per-step cost_usd is omitted (the canonical step does not carry it); the total
// is in final_metrics. No step is marked is_copied_context: Sleipnir agents do not
// inherit their parent's steps.
//
// canonical (sleipnir.rl/1): the lossless archive, one rl.Episode per line with
// rewards, signals, flags and every step (roles are not filtered). With Inline every
// step carries its full prompt in "inline". Otherwise the unique tools, system
// blocks and messages are written once as a segment table (sleipnir.rl.segment/1
// lines: hash, kind, body, hashes being the run's own blob hashes when nothing was
// redacted) and each step has "segments": {model, tools, system, base, keep, add},
// a delta against the previous step of its agent. The table leads the main stream,
// or goes to Options.Table. Expand turns a deduplicated export back into the inline
// form, byte for byte.
//
// # Redaction
//
// Options.Redactor runs over every string a record contains, before rendering, so
// the wire messages, the completion and the observation of one secret are all
// redacted the same way and prefix sharing between prompts survives. Tool-call
// arguments stay valid JSON.
package export
