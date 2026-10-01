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
