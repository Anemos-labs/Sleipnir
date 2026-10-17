package session

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// A soft limit above the default hard one would never be reached: the hard limit forces the compaction first. So the hard limit
// follows it up, unless it was set too. (A benchmark of "compact later or never" with a soft limit of 150k compacted at 60k all the
// same, and the setting that was meant to compare when to compact compared nothing.)
func TestAHighSoftLimitRaisesTheHardLimitUnlessItWasSet(t *testing.T) {
	for _, c := range []struct {
		name       string
		cache      config.Cache
		soft, hard int
	}{
		{"the defaults", config.Cache{}, 20_000, 60_000},
		{"a soft limit below the hard one", config.Cache{ThreadSoftLimitTokens: 30_000}, 30_000, 60_000},
		{"a soft limit above the default hard one", config.Cache{ThreadSoftLimitTokens: 150_000}, 150_000, 150_000},
		{"both set: the hard one as it was set", config.Cache{ThreadSoftLimitTokens: 150_000, CompactThresholdTokens: 90_000}, 150_000, 90_000},
		{"only the hard one", config.Cache{CompactThresholdTokens: 40_000}, 20_000, 40_000},
	} {
		p := plannerFor(c.cache)
		if p.SoftThreadTokens != c.soft || p.HardThreadTokens != c.hard {
			t.Errorf("%s: soft %d hard %d, want %d and %d", c.name, p.SoftThreadTokens, p.HardThreadTokens, c.soft, c.hard)
		}
	}
}

// Compaction starts at a share of each model's window: a quarter by default, compact_at_percent when it is set. Token limits set
// without it keep applying to every model, as before the share existed.
func TestTheCompactionShareComesFromTheCacheSettings(t *testing.T) {
	for _, c := range []struct {
		name  string
		cache config.Cache
		share float64
	}{
		{"the default quarter", config.Cache{}, 0.25},
		{"a share that is set", config.Cache{CompactAtPercent: 40}, 0.40},
		{"token limits alone apply to every model", config.Cache{ThreadSoftLimitTokens: 30_000}, -1},
		{"a hard token limit alone too", config.Cache{CompactThresholdTokens: 90_000}, -1},
		{"a share wins over token limits for a model with a known window", config.Cache{CompactAtPercent: 30, ThreadSoftLimitTokens: 30_000}, 0.30},
	} {
		if got := plannerFor(c.cache).CompactAt; got != c.share {
			t.Errorf("%s: share %v, want %v", c.name, got, c.share)
		}
	}
}
