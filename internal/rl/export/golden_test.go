package export_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/export"
)

// trimmed keeps the first n steps of the fixture's agent without token traces: a
// real recorded episode small enough to keep a full-episode golden file (atif,
// canonical) in the repository.
func trimmed(ep *rl.Episode, n int) *rl.Episode {
	c := withoutTokens(ep)
	a := c.Agents[0]
	a.Steps = a.Steps[:n]
	a.Segments = []rl.Segment{{Index: 0, From: 0, To: n - 1, Epoch: 0, Reason: "start"}}
	c.Agents = []rl.Agent{a}
	c.Edges = nil
	c.Signals = map[string]float64{rl.SigSteps: float64(n), rl.SigRequests: float64(n)}
	c.Reward = rl.Reward{Total: 0.5, Components: map[string]float64{"outcome": 1, "cost": -0.5}}
	return c
}

// TestGolden pins the exact bytes of every format. Regenerate deliberately with
// go test -run TestGolden -update.
func TestGolden(t *testing.T) {
	fx, _ := fixture(t)
	fxTrim := export.Source{Episode: trimmed(fx.Episode, 3), Prompts: fx.Prompts}
	sw := swarmSource(t, true)
	grp := group(t, 4)
	packedChain := chainRun(t, 4, nil)

	base := func(f export.Format) export.Options {
		return export.Options{Format: f, KeepFlat: true, KeepWeak: true, DropFlagged: true}
	}
	with := func(o export.Options, edit func(*export.Options)) export.Options { edit(&o); return o }

	cases := []struct {
		name string
		srcs []export.Source
		o    export.Options
	}{
		// The real recorded run, bounded to a few records per format.
		{"fixture_steps", []export.Source{fx}, with(base(export.FormatSteps), func(o *export.Options) { o.MaxSamples = 2 })},
		{"fixture_kto", []export.Source{fx}, with(base(export.FormatKTO), func(o *export.Options) { o.MaxSamples = 2 })},
		{"fixture_tokens", []export.Source{fx}, with(base(export.FormatTokens), func(o *export.Options) { o.MaxSamples = 2 })},
		{"fixture_groups_compactor", []export.Source{fx}, with(base(export.FormatGroups), func(o *export.Options) { o.Roles = []string{"compactor"}; o.MaxSamples = 1 })},
		{"fixture_sft_compactor", []export.Source{fx}, with(base(export.FormatSFT), func(o *export.Options) { o.Roles = []string{"compactor"}; o.MaxSamples = 1 })},
		{"fixture_atif_first3", []export.Source{fxTrim}, base(export.FormatATIF)},
		{"fixture_canonical_first3_inline", []export.Source{fxTrim}, with(base(export.FormatCanonical), func(o *export.Options) { o.Inline = true })},
		{"fixture_canonical_first3_dedup", []export.Source{fxTrim}, base(export.FormatCanonical)},
		// A swarm: manager, worker with compaction and hot tail, teacher reviewer.
		{"swarm_steps", []export.Source{sw}, base(export.FormatSteps)},
		{"swarm_tokens", []export.Source{sw}, base(export.FormatTokens)},
		{"swarm_tokens_packed", []export.Source{sw}, with(base(export.FormatTokens), func(o *export.Options) { o.PackSegments = true })},
		{"swarm_groups", []export.Source{sw}, base(export.FormatGroups)},
		{"swarm_sft", []export.Source{sw}, with(base(export.FormatSFT), func(o *export.Options) { o.TeacherOK = []string{"teacher-x"} })},
		{"swarm_sft_packed", []export.Source{sw}, with(base(export.FormatSFT), func(o *export.Options) { o.PackSegments = true })},
		{"swarm_kto", []export.Source{sw}, base(export.FormatKTO)},
		{"swarm_atif", []export.Source{sw}, base(export.FormatATIF)},
		{"swarm_canonical_inline", []export.Source{sw}, with(base(export.FormatCanonical), func(o *export.Options) { o.Inline = true })},
		{"swarm_canonical_dedup", []export.Source{sw}, base(export.FormatCanonical)},
		// A GRPO group of one task.
		{"group_steps", grp, base(export.FormatSteps)},
		{"group_groups", grp, base(export.FormatGroups)},
		{"group_dpo", grp, base(export.FormatDPO)},
		{"group_sft_top1", grp, with(base(export.FormatSFT), func(o *export.Options) { o.TopK = 1 })},
		// A chain that packs.
		{"chain_tokens_packed", []export.Source{packedChain}, with(base(export.FormatTokens), func(o *export.Options) { o.PackSegments = true })},
		// Redaction and splits.
		{"leaky_steps_redacted", leakySources(t), with(base(export.FormatSteps), func(o *export.Options) { o.Redactor = newRedactor() })},
		{"group_steps_split", grp, with(base(export.FormatSteps), func(o *export.Options) { o.Split = "train:0.5,val:0.5"; o.Seed = 3 })},
	}
	stats := map[string]export.Stats{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := run(t, c.srcs, c.o)
			if out == "" {
				t.Fatal("golden case produced no output")
			}
			stats[c.name] = st
			golden(t, c.name+".jsonl", out)
		})
	}
	// Stats of every case in one file.
	if !t.Failed() {
		names := make([]string, 0, len(stats))
		for n := range stats {
			names = append(names, n)
		}
		sort.Strings(names)
		var sb strings.Builder
		for _, n := range names {
			b, _ := json.Marshal(stats[n])
			fmt.Fprintf(&sb, "%s %s\n", n, b)
		}
		golden(t, "stats.txt", sb.String())
	}
}

func leakySources(t testing.TB) []export.Source {
	src, _ := leaky(t, false)
	return []export.Source{src}
}

// TestGoldenFixtureDigests covers every format of the whole recorded run, with
// tokens and packing where they apply, by digest: the full outputs are too large
// to keep as text, and any change to them shows here.
func TestGoldenFixtureDigests(t *testing.T) {
	fx, _ := fixture(t)
	var sb strings.Builder
	for _, f := range export.Formats() {
		o := export.Options{Format: f, KeepFlat: true, KeepWeak: true, DropFlagged: true, PackSegments: true, Inline: true}
		out, st := run(t, []export.Source{fx}, o)
		sum := sha256.Sum256([]byte(out))
		fmt.Fprintf(&sb, "%-9s records=%-3d bytes=%-8d sha256=%s\n", f, st.Records, len(out), hex.EncodeToString(sum[:]))
	}
	o := export.Options{Format: export.FormatCanonical, KeepFlat: true, KeepWeak: true, DropFlagged: true}
	out, st := run(t, []export.Source{fx}, o)
	sum := sha256.Sum256([]byte(out))
	fmt.Fprintf(&sb, "%-9s records=%-3d bytes=%-8d sha256=%s (dedup, table in stream)\n", "canonical", st.Records, len(out), hex.EncodeToString(sum[:]))
	golden(t, "fixture_digests.txt", sb.String())
}
