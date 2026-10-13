# GitHub delivery

Each objective can independently enable **Auto-fix CI & reviews** and **Auto-merge**
in the chat composer. The terminal equivalents are `--auto-fix`, `--auto-merge`
and `/delivery fix|merge|both|off`. These choices reset for the next objective.
`gh` must be installed and authenticated for the repository.

PR discovery requires a working branch with an intended Git push remote and exactly
one push URL. Configure the branch's upstream/push remote (for example with
`git push --set-upstream origin HEAD`); the controller resolves that push URL with
`gh` and refuses unknown or ambiguous repository identity. A discovered PR must
match that head repository and the local checkout commit, not just the branch name.
The intended base is the target repository's default branch unless configured with
`git config branch.<name>.gh-merge-base <base-branch>` (replace the placeholders
with the working branch and desired base). This is also the setting used by
`gh pr create` for a nondefault base. Discovery refuses multiple open PRs matching
the branch rather than selecting one arbitrarily. The controller pins the PR URL,
head repository and base branch and rechecks the repository/base identity on later
snapshots. To change that binding, configure the intended push remote/base and
start delivery for a new objective; `/delivery resume` retains the saved binding.

The manager implements, verifies, commits, pushes and opens or reuses the PR.
Opening the PR is a direct manager operation. The controller polls GitHub without
model requests while checks are pending. Auto-fix returns failed checks, conflicts,
unresolved review threads and changed review summaries (including CodeRabbit) to
the manager. Findings are evidence to evaluate: valid findings are repaired;
inapplicable findings need an explanation before their threads are resolved.
The manager must explicitly clear review summaries; a blocked or missing verdict
prevents auto-merge even when GitHub reports no open review thread.
Repairs keep the existing task IDs and role pools. Conflicts are resolved by merging
the base into the working branch, without force-pushing.

Auto-merge uses a repository-supported merge method and the current head SHA. It
never uses administrator bypasses or changes branch protection. Pending checks,
requested changes, unresolved threads and merge conflicts prevent the merge request.
After merge, it waits for base-branch workflows and asks the manager to verify any
release, its tag commit and published artifacts. Repository requirements remain the
source of truth; GitHub can decline a merge after an earlier readiness snapshot.

Progress is checkpointed in the session's `delivery.json`. Closing a browser tab
does not stop the local server. Cancellation or a failed step pauses the workflow
and attempts to revoke any native auto-merge request it enabled. If GitHub is
unreachable, revocation cannot be guaranteed; the pending intent remains in the
checkpoint and the UI reports the error. `/delivery off` retries revocation.
A process restart does not silently resume publishing: `/delivery resume`, followed
by a message, continues the saved objective with its saved choices.

Explicit permission denials, session budgets and cancellation still apply. The
feature does not start a detached terminal daemon, enable paid services, bypass
required reviews, or enable CodeRabbit's autonomous implementation features.
