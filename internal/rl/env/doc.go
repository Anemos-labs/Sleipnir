// Package env is the environment side of Sleipnir's RL pipeline: tasks, isolated
// rollouts, clean-checkout verification, evaluation and a rollout server. The
// harness itself (agent loop, tools, swarm, sessions) is somebody else's; this
// package drives it through the small Harness interface and supplies everything
// around it that turns "an agent edited some files" into "a trustworthy reward".
//
// # The pieces
//
//	tasks.go      LoadTasks / WriteTasks / Filter / Split / Holdout / exclusion lists
//	workspace.go  Workspaces: cached setup snapshots, per-rollout working trees
//	verify.go     Verify / VerifyPatch / VerifyBaseline / CheckTask
//	runner.go     Runner.Rollout: G samples per task, resumable, cancellable
//	eval.go       Eval, BuildReport, Compare (paired bootstrap)
//	server.go     Serve: the rollout server for trainers
//	exec.go       Sandbox / Exec: how commands are run (the container hook)
//	taskgen/      FromGit, Mutate, Composite and the Recall hook
//
// # A rollout, end to end
//
//	Runner.Rollout(tasks, G)
//	  per (task, sample), at most Concurrency at a time:
//	    Workspaces.Prepare   copy-on-write clone of the cached snapshot of
//	                         (repo, commit, setup); setup ran once, with network
//	    Harness.Run          the agent, under the task budget; wall clock is a
//	                         context deadline; events.jsonl and blobs/ land in
//	                         <Out>/<task>/<sample>/
//	    Verify               diff of the workspace against the starting state,
//	                         applied MINUS protected paths to a separate clean
//	                         checkout, hidden files written there, verifier run
//	    outcome events       appended to events.jsonl; diff.patch, verifier.log
//	    Extract, Score       injected (traj and reward); episode.json written
//	    Workspace.Cleanup    processes killed, tree removed, no symlink followed
//
// Run directory layout (consumed by package traj):
//
//	<Out>/manifest.json      configuration, tasks, policy, versions, host warnings
//	<Out>/summary.json       pass rate, mean cost, per-task results, infra errors
//	<Out>/<task>/<sample>/   events.jsonl blobs/ task.json env.json diff.patch
//	                         verifier.log episode.json
//
// task.json is the rl.Task with inline hidden files replaced by their hashes:
// it sits beside a log that the agent's harness writes, and the policy must
// never be able to read verifier content there.
//
// # Infrastructure errors versus verdicts
//
// A failure of the machinery (a setup command that could not run, a git error on
// data the harness owns, a missing hidden blob, a sandbox that will not start,
// a provider outage) is an infrastructure error: the episode is flagged
// infra_error and exporters drop it, so noise is not learned. A failure the
// agent could have caused is a verdict with a score of zero and never an infra
// error: tests that fail or hang, a diff over the size limit, a workspace
// littered with nested repositories and FIFOs, a verifier script the agent
// deleted, a command that is no longer found. If a policy could provoke an
// "infra error" to avoid a bad reward it would learn to; every classification
// in this package follows that rule. Harness implementations follow it too: Run
// returns an error only for infrastructure faults.
//
// # Security model
//
// The threat is a policy that optimises reward and will therefore find any way
// to raise it that the environment allows: edit or delete the tests, plant a
// stub where a hidden test will appear, make the verifier command succeed
// trivially, read the answer from history or from a sibling rollout, or attack
// the harness through the files it leaves behind.
//
// What this package does about it:
//
//   - The verifier never runs in the agent's workspace. Verify computes the
//     agent's diff against the starting state, builds a separate clean checkout
//     from the cached snapshot (which no agent touches, and which is re-hashed
//     against its recorded baseline before every use), applies the diff with every
//     path matching Verifier.Protected removed, writes Hidden files there and runs
//     the command. Removed paths are reported in Result.ProtectedTouched and the
//     Runner turns them into the hack:protected_edit flag (and
//     hack:verifier_touched when a file the verifier command names, or a hidden
//     file, was touched).
//   - Hidden paths are implicitly protected, are written through os.Root after
//     replacing whatever the agent's diff planted there (a symlink at the path or
//     at a parent directory cannot redirect the write), and blobs are checked
//     against their content hash so an edited blob store cannot feed altered
//     tests to the verifier. Hidden content is never copied into the workspace,
//     the run directory or task.json.
//   - Protected globs are gitignore-style and matched case-insensitively, so a
//     diff touching "FOO_TEST.GO" cannot overwrite the protected "foo_test.go" on a
//     case-insensitive filesystem.
//   - The diff is computed without `git add`: files are enumerated with
//     `git ls-files` (which knows the ignore rules) and hashed byte for byte, so
//     no attribute filter, line-ending rule or nested repository can abort or
//     alter it. Paths that cannot be represented (special files, .git-like names,
//     symlinked parents, oversized files) are reported in Result.Skipped, not
//     silently dropped. Patches are generated without rename detection so that
//     every path stands alone and can be filtered.
//   - Workspaces are history-free by default: the commit is exported into a new
//     single-commit repository, so the fix of a task mined from history is not
//     one `git log --all` away. ModeClone opts into full history.
//   - git runs with system and user configuration disabled, the dangerous keys
//     (hooks, fsmonitor, diff and credential helpers, pagers, ext:: transports)
//     overridden on the command line, and never inside the agent's own .git
//     directory: the agent controls that directory's configuration.
//   - Commands run with an allowlisted environment, never the harness's: no
//     credentials, a private HOME and TMPDIR per run, a fixed locale and time
//     zone, a sanitised PATH, and proxies only for tasks that have network.
//     Tool caches (Go build and module cache, cargo, npm, pip) live under that
//     HOME, so a cache poisoned by one run cannot reach another run's verifier.
//   - Commands run in their own session and process group and are killed as a
//     group on timeout or cancellation, with output capped at head plus tail and
//     a hard kill for runaway output; stragglers that escaped the group are found
//     through a marker in their environment (Linux). Resource limits (file size,
//     core dumps, optionally open files and so on) are applied through prlimit.
//   - Network isolation: tasks without network run in a fresh network namespace
//     (unshare -n, loopback raised) where the host allows it; otherwise the fallback
//     is recorded in the manifest's warnings (or is an error with
//     WorkspaceOptions.RequireNetIsolation). The harness owns the agent's shell,
//     so it applies the prefix from RunSpec.NetPrefix.
//   - Cleanup never follows symlinks out of a workspace and makes read-only
//     directories (Go's module cache) writable first; it refuses to remove anything
//     outside the workspace area.
//   - The server requires a bearer token (constant-time comparison) unless bound to
//     loopback, refuses to bind elsewhere without one, limits body size and
//     concurrency, validates every identifier that reaches the filesystem, and can
//     confine inline tasks to a list of repository roots.
//
// What it does NOT do. This is directory-level isolation on the host, and it is
// not a security boundary against a hostile agent. An agent that runs as the same
// operating-system user can read and write everything that user can: other
// rollouts' workspaces and the cached snapshots (tampering with the latter is
// detected for tracked and non-ignored files only), a verification checkout while
// it exists, the run directory, the toolchain installation, the harness's own
// process environment through /proc. Code the agent wrote runs inside the
// verifier and can print a fake "score" line, exit 0 from a test binary, or read
// the hidden tests it is running against. Ignored files (node_modules, target
// directories) are not part of any diff or integrity check. Real containment needs
// containers or VMs, separate users and read-only mounts. The Sandbox hook exists
// for exactly that: implement it with a container runner, mount the working
// directory and the HOME and TMPDIR named in the environment, honour
// ExecPolicyFrom(ctx).Network, and setup and verification run inside it. Until
// then, treat every rollout and every task file as trusted-but-buggy code.
//
// Other known limits: the verifier's judgement is only as good as its command
// (see taskgen for how tasks are proven sound before use); submodules are not
// populated; absolute paths baked into setup output (virtualenvs, CMake caches)
// break when the tree is copied to a workspace; the cloned tool home is a full
// copy on filesystems without reflinks.
//
// # Requirements
//
// git 2.25 or newer and a POSIX sh. On Linux, unshare and ip (or ifconfig) enable
// network isolation and prlimit enables resource limits; on macOS neither is
// available and the manifest says so. Everything else is the standard library.
package env
