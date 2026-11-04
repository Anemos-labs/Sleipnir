package session

import (
	"os"
	"path/filepath"
)

// WorktreesDir is the directory the worktrees of every isolated team live under: <state>/worktrees, <state> being $SLEIPNIR_HOME,
// else ~/.sleipnir under home (the current user's home directory when home is empty).
func WorktreesDir(home string) string { return filepath.Join(StateRoot(home), "worktrees") }

// legacyWorktreesDir is where the worktrees were kept before they moved into the data directory: <cache>/sleipnir/worktrees, <cache>
// being the platform's per-user cache directory for the real home directory ($XDG_CACHE_HOME or ~/.cache on Linux) and <home>/.cache
// for another. Trees found there still belong to their sessions: those sessions resume with them in place, and the storage cleanup
// releases them as it does the others. "" when it is the same directory as WorktreesDir.
func legacyWorktreesDir(home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	cache := filepath.Join(home, ".cache")
	if h, err := os.UserHomeDir(); err == nil && home != "" && samePathLoose(h, home) {
		if c, err := os.UserCacheDir(); err == nil && c != "" {
			cache = c
		}
	}
	dir := filepath.Join(cache, "sleipnir", "worktrees")
	if samePathLoose(dir, WorktreesDir(home)) {
		return ""
	}
	return dir
}
