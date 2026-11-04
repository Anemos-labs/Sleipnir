# Storage

Sleipnir keeps everything it writes for itself in one data directory: `$SLEIPNIR_HOME` when it is set, else `~/.sleipnir`
(`<state>` below). A project's own `.sleipnir/` folder (its configuration, research runs, scans, council ledger) belongs to the
project and is never touched by anything on this page.

## What is in it

| Path | What | Cleaned |
|---|---|---|
| `<state>/sessions/<id>/` | a recorded session: `events.jsonl`, the `blobs/` it points at, `checkpoints/` | yes: old sessions are deleted |
| `<state>/sessions/<id>/tmp/` | the session's scratch directory, the `$TMPDIR` of its commands (build caches, test output) | yes: emptied once the session is idle for a day |
| `<state>/worktrees/<id>/<agent>` | the git worktrees of an isolated team (`swarm.isolation = "worktree"`) | yes: with their session, or once their session is gone |
| `<state>/rl-work/` | RL episode workspaces (`sleipnir rl`) | no |
| `<state>/lore/`, `lessons.json`, `pins.json`, `schedule.json`, `mcp-approvals.json`, `run/`, `bin/` | memories, pinned sessions, scheduled goals, approvals, machine slots, plugin launchers | no |
| `~/.sleipnir/config.json`, `auth.json`, `chatgpt.json`, `MEMORY.md`, `teams/`, `plugins/`, `skills/` | your settings, credentials, notes and extensions | no |

The files of the last row are read from `~/.sleipnir` whatever `SLEIPNIR_HOME` says, so a scratch data directory (a test, a
fixture) runs with your own settings. With `SLEIPNIR_HOME` unset, all of it is the one directory `~/.sleipnir`.

Earlier versions kept the worktrees in the user's cache directory (`$XDG_CACHE_HOME/sleipnir/worktrees`, or
`~/.cache/sleipnir/worktrees`, and the platform's cache directory elsewhere). They are not moved: a git worktree records its
path in the repository, and a session resumes with its trees where they are. They drain as their sessions are deleted, and the
folder is removed once it is empty. Since that folder is shared by every data directory of the user, a folder of trees there
whose session is gone is released only by the cleanup of the default data directory (`SLEIPNIR_HOME` unset). `sleipnir storage`
lists them while any are left.

`sleipnir storage` prints the location and the room each area takes:

```text
data directory: /home/me/.sleipnir
  * worktrees         14.2 GB  isolated teams' worktrees
  * sessions           3.1 GB  recorded sessions: event logs, blobs, checkpoints
  * scratch            2.4 GB  the sessions' $TMPDIR
    other             41 MB   settings, credentials, memories, plugins and launchers
  total               19.7 GB
* cleaned by `sleipnir storage clean`; automatic cleanup: once a day in sleipnir web: sessions older than 30d, past the newest 20
```

## The cleanup

`sleipnir storage clean` (and, once a day, `sleipnir web` by itself) removes three things:

- **Old sessions.** A session whose newest file is older than `--older-than` (`storage.session_max_age`, 30 days) is deleted,
  except the newest `--keep` (`storage.keep_sessions`, 20), a pinned session, one written to in the last ten minutes and one a
  process holds. It is deleted as `sessions prune` deletes one, worktrees included: the session's lock is taken, the edits left
  in each tree are committed onto the tree's branch (`sleipnir/<id>/<agent>`), the trees are removed, and every branch that holds
  commits no other branch has is kept and named. Only the session's recovery reference goes.
- **Scratch directories.** The `tmp/` of a session that no process holds and that nothing has written to for a day is removed.
  It is the `$TMPDIR` of the session's commands, and nothing reads it once they are gone; a resumed session gets a new one.
- **Worktrees of sessions that are gone.** A folder of trees whose session is not recorded (deleted by hand, or recorded with
  `run --session-dir` elsewhere and older than the age) is released the same way: a tree whose owner may still be running leaves
  the whole folder alone, uncommitted edits are committed onto the tree's branch, and a branch with work of its own is kept. A
  folder whose repository cannot be found is left as it is and reported.

Without `--yes` the command removes nothing and lists what would go, with its size. `storage.clean = "off"` stops the automatic
cleanup; the command still works. The settings are read from your user file only: a repository cannot make the harness delete
your history.

The work a cleanup keeps is in ordinary Git branches of the project: `git branch --list 'sleipnir/*'` lists them, and `git log
sleipnir/<id>/<agent>` shows what each holds. Delete one with `git branch -D` once it is no longer needed.

## The permission engine and the data directory

Every write to the data directory asks, in every mode (`Edit(~/.sleipnir/**)`, and the same rule for `$SLEIPNIR_HOME`): what is
there steers later sessions. The trees of the session that is running are the exception, as they are its agents' workspace: a
write inside one is judged as a write in the project, with the project's own rules (`Edit(./.sleipnir/**)` and the others)
applied inside each tree. The trees of other sessions, and a link inside a tree that leads elsewhere in the data directory, are
still protected.
