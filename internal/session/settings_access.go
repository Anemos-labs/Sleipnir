package session

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/trust"
)

// SettingsInfo is what the settings pages of `sleipnir web` read of a running session: where it works, whether it uses the
// project's own files, the configuration it started with and the rules its flags added. It is a snapshot; nothing in it is shared
// with the session.
type SettingsInfo struct {
	// Root is the project root and Cwd the working directory; Home is the home directory whose ~/.sleipnir the session reads.
	Root, Cwd, Home string
	// TrustProject says the session uses the project's own files (instructions, settings, skills, tool servers).
	TrustProject bool
	// TrustHow says how that was decided: "flag", "remembered", "asked", "asked-and-remembered", "declined", "not-asked", or "" when
	// the project has nothing that trust would unlock.
	TrustHow string
	// Config and Report are the configuration the session loaded (Report is nil when the configuration was handed in).
	Config *config.Config
	Report *config.Report
	// FlagAllow are the allow rules the session was started with (--allow, presets expanded).
	FlagAllow []string
	// NoMCP says the session was started without tool servers.
	NoMCP bool
	// Mode is the permission mode in force now.
	Mode perm.Mode
}

// SettingsInfo returns the session's settings snapshot. It is safe to call while a turn runs.
func (s *Session) SettingsInfo() SettingsInfo {
	info := SettingsInfo{
		Root: s.opts.Root, Cwd: s.opts.Cwd, Home: s.opts.Home, TrustProject: s.opts.TrustProject,
		Config: s.cfg, Report: s.cfgRep, FlagAllow: slices.Clone(s.opts.Allow), NoMCP: s.opts.NoMCP,
	}
	if s.trust != nil {
		info.TrustHow = s.trust.How
	}
	if s.Perm != nil {
		info.Mode = s.Perm.Mode()
	}
	return info
}

// ProtectedConfigRules are the ask rules every session adds before the configuration's own (the places whose contents run as code
// or steer every later session: a write there asks in every mode, bypass included, and an unattended session refuses it). A copy.
func ProtectedConfigRules() []string { return slices.Clone(protectedConfigDirs) }

// ProjectFootprint reads what the project around dir would let the harness use if it were trusted: the files of the project root
// found from dir (config.FindRoot; dir itself when none is found), as `sleipnir trust` shows them.
func ProjectFootprint(home, dir string) (*trust.Footprint, error) {
	root, _ := config.FindRoot(dir)
	if root == "" {
		root = dir
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return trust.Scan(root, dir, home)
}

// Ledger states of a remembered directory, as TrustNow reports them.
const (
	// TrustUnchanged: the files are the ones the person said yes to.
	TrustUnchanged = "trusted"
	// TrustChanged: the files differ from the ones the person said yes to (or cannot be read).
	TrustChanged = "changed"
	// TrustGone: the directory is not there any more.
	TrustGone = "gone"
)

// TrustNow says what the ledger's entry e for dir is worth now, as `sleipnir trust list` says it: "unchanged", "directory is gone",
// "cannot be read: <why>" or "changed: <which files>", and the state word of the same (TrustUnchanged, TrustGone, TrustChanged).
func TrustNow(ledger *trust.Ledger, home, dir string, e trust.Entry) (now, state string) {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "directory is gone", TrustGone
	}
	fp, err := ProjectFootprint(home, dir)
	if err != nil {
		return "cannot be read: " + err.Error(), TrustChanged
	}
	if st, _ := ledger.Check(dir, fp); st != trust.Trusted {
		return "changed: " + trust.DescribeChanges(trust.Changes(e, fp)), TrustChanged
	}
	return "unchanged", TrustUnchanged
}
