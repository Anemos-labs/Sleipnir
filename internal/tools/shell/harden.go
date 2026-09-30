package shell

// HardenProcess makes the harness process opaque to the commands it runs.
//
// Scrubbing the environment of a child does not protect the parent's own: on
// Linux any process of the same user can read /proc/<pid>/environ (the
// environment the harness was started with, provider API key included) and
// /proc/<pid>/mem, or ptrace it, and a model-run command is exactly such a
// process (`tr '\0' '\n' </proc/$PPID/environ`). Marking the process
// non-dumpable (prctl PR_SET_DUMPABLE 0) makes those files root-owned and the
// ptrace check fail for every other same-user process. Children are unaffected:
// the flag is reset by execve.
//
// Call it once, early in main, before or after loading secrets (it applies to
// the process from then on, whatever the environment already holds). Side
// effects to know about: no core dumps, debuggers cannot attach, and a
// non-root process can no longer open its own /proc/self/environ or list
// /proc/self/fd. It is a no-op on platforms without the mechanism (macOS would
// need PT_DENY_ATTACH, which is not exposed here), so it complements, and does
// not replace, keeping secrets out of the environment in the first place.
func HardenProcess() error { return hardenProcess() }
