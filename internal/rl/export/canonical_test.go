package export_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/export"
)

func canonOpts(inline bool) export.Options {
	return export.Options{Format: export.FormatCanonical, DropFlagged: true, Inline: inline}
}

// expand runs Expand over dedup output; table may be nil (in-stream).
func expand(t testing.TB, dedup string, table *string) string {
	t.Helper()
	var out bytes.Buffer
	var tr *strings.Reader
	var n int
	var err error
	if table != nil {
		tr = strings.NewReader(*table)
		n, err = export.Expand(&out, strings.NewReader(dedup), tr)
	} else {
		n, err = export.Expand(&out, strings.NewReader(dedup), nil)
	}
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if n != len(lines(out.String())) {
		t.Fatalf("expand reported %d episodes for %d lines", n, len(lines(out.String())))
	}
	return out.String()
}

// TestDedupExpandRoundTripIsByteIdentical is the archive's contract: the
// deduplicated export, expanded, is byte for byte the inline export.
func TestDedupExpandRoundTripIsByteIdentical(t *testing.T) {
	fx, _ := fixture(t)
	srcs := map[string][]export.Source{
		"fixture": {fx},
		"swarm":   {swarmSource(t, true)},
		"group":   group(t, 4),
		"mixed":   append(group(t, 2), swarmSource(t, false)),
	}
	for name, s := range srcs {
		for _, red := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				o := canonOpts(true)
				oo := canonOpts(false)
				if red {
					o.Redactor, oo.Redactor = newRedactor(), newRedactor()
				}
				inline, _ := run(t, s, o)
				dedup, st := run(t, s, oo) // table in the stream
				if inline == "" || dedup == "" || st.Records != len(s) {
					t.Fatalf("records: %d", st.Records)
				}
				if got := expand(t, dedup, nil); got != inline {
					t.Fatalf("expanded dedup export differs from the inline export:\n%s", firstDiff(inline, got))
				}
				// A separate table writer gives the same result.
				var table strings.Builder
				oo.Table = &table
				dedup2, _ := run(t, s, oo)
				if strings.Contains(dedup2, `"kind":"message"`) {
					t.Fatal("with a table writer the stream holds episodes only")
				}
				tab := table.String()
				if got := expand(t, dedup2, &tab); got != inline {
					t.Fatalf("expand with a separate table differs:\n%s", firstDiff(inline, got))
				}
				// Expanding twice is a no-op on already inline data.
				if got := expand(t, inline, nil); got != inline {
					t.Fatal("expanding an inline export must leave it unchanged")
				}
			})
		}
	}
}

func TestDedupIsMuchSmallerThanInline(t *testing.T) {
	fx, _ := fixture(t)
	// Token traces are the same size either way and dominate the fixture; compare
	// the prompts.
	fx.Episode = withoutTokens(fx.Episode)
	inline, _ := run(t, []export.Source{fx}, canonOpts(true))
	dedup, _ := run(t, []export.Source{fx}, canonOpts(false))
	if len(dedup)*3 > len(inline) {
		t.Fatalf("dedup %d bytes vs inline %d: shared prefixes must be written once", len(dedup), len(inline))
	}
	// Many samples of one task share almost everything.
	srcs := group(t, 8)
	for i := range srcs {
		srcs[i].Episode = withoutTokens(srcs[i].Episode)
	}
	inline, _ = run(t, srcs, canonOpts(true))
	dedup, _ = run(t, srcs, canonOpts(false))
	if len(dedup) >= len(inline) {
		t.Fatalf("group: dedup %d vs inline %d", len(dedup), len(inline))
	}
}

// TestTableKeysAreTheRecordedBlobHashes: without redaction the segment table is a
// content-addressed subset of the run's blob store, so a table can be checked
// against (or rebuilt from) the blobs.
func TestTableKeysAreTheRecordedBlobHashes(t *testing.T) {
	fx, _ := fixture(t)
	var table strings.Builder
	o := canonOpts(false)
	o.Table = &table
	run(t, []export.Source{fx}, o)
	store, err := events.NewDirBlobs(filepath.Join(fixtureDir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	prev := ""
	for _, l := range lines(table.String()) {
		var e struct {
			Schema string          `json:"schema"`
			Hash   core.Hash       `json:"hash"`
			Kind   string          `json:"kind"`
			Body   json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatal(err)
		}
		if e.Schema != export.SchemaSegment || core.HashBytes(e.Body) != e.Hash {
			t.Fatalf("entry %s: schema %s, body hashes to %s", e.Hash.Short(), e.Schema, core.HashBytes(e.Body).Short())
		}
		if !store.Has(e.Hash) {
			t.Fatalf("%s entry %s is not a blob of the recorded run", e.Kind, e.Hash.Short())
		}
		if string(e.Hash) <= prev {
			t.Fatal("the table must be written in hash order")
		}
		prev = string(e.Hash)
		n++
	}
	if n < 40 {
		t.Fatalf("only %d table entries", n)
	}
}

func TestDedupStepReferences(t *testing.T) {
	src := chainRun(t, 4, nil)
	out, _ := run(t, []export.Source{src}, canonOpts(false))
	var ep map[string]any
	for _, l := range lines(out) {
		var m map[string]any
		_ = json.Unmarshal([]byte(l), &m)
		if m["schema"] == rl.SchemaEpisode {
			ep = m
		}
	}
	steps := ep["agents"].([]any)[0].(map[string]any)["steps"].([]any)
	first := steps[0].(map[string]any)["segments"].(map[string]any)
	second := steps[1].(map[string]any)["segments"].(map[string]any)
	if first["base"] != nil || first["tools"] == nil || first["model"] != "policy-1" || len(first["add"].([]any)) != 1 {
		t.Fatalf("first step references: %v", first)
	}
	if second["base"] != "solo.1" || second["keep"].(float64) != 1 || len(second["add"].([]any)) != 2 {
		t.Fatalf("a step is a delta against the previous step: %v", second)
	}
	if _, has := steps[0].(map[string]any)["inline"]; has {
		t.Fatal("dedup steps carry no inline prompt")
	}
	inl, _ := run(t, []export.Source{src}, canonOpts(true))
	if strings.Contains(inl, `"segments":{`) || !strings.Contains(inl, `"inline":{"model":"policy-1"`) {
		t.Fatal("inline steps embed the prompt and no references")
	}
}

func TestCanonicalKeepsEverythingAndIgnoresRoles(t *testing.T) {
	src := swarmSource(t, true)
	out, st := run(t, []export.Source{src}, export.Options{Format: export.FormatCanonical, DropFlagged: true, Inline: true, Roles: []string{"manager"}, Advantage: func(eps []*rl.Episode) error {
		eps[0].Agents[0].Steps[0].Advantage = 0.25
		return nil
	}})
	var back rl.Episode
	if err := json.Unmarshal([]byte(lines(out)[0]), &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Agents) != 3 || st.Records != 1 || back.Reward.Total != 0.9 || back.Reward.Components["cost"] != -0.1 || back.Agents[1].Reward.Total != 0.95 {
		t.Fatalf("canonical is the lossless archive: %d agents, reward %+v", len(back.Agents), back.Reward)
	}
	if back.Agents[0].Steps[0].Advantage != 0.25 {
		t.Fatalf("advantages from the hook are archived: %v", back.Agents[0].Steps[0].Advantage)
	}
	orig := src.Episode
	if len(back.Edges) != len(orig.Edges) || back.Signals[rl.SigSteps] != orig.Signals[rl.SigSteps] || back.Agents[1].Segments[1].Reason != "compact" {
		t.Fatalf("edges/signals/segments lost")
	}
	if back.Agents[1].Steps[0].Tokens == nil || len(back.Agents[1].Steps[0].Tokens.PromptIDs) == 0 {
		t.Fatal("token traces are archived")
	}
	if back.Agents[1].Steps[0].Inline == nil || len(back.Agents[1].Steps[0].Inline.Messages) == 0 {
		t.Fatal("inline prompt missing")
	}
	// The archive is not affected by the source episode being shared: the caller's
	// step still has no inline prompt.
	if src.Episode.Agents[0].Steps[0].Inline != nil {
		t.Fatal("export must not modify the caller's episode")
	}
}

func TestExpandRejectsDamagedInput(t *testing.T) {
	src := chainRun(t, 3, nil)
	dedup, _ := run(t, []export.Source{src}, canonOpts(false))
	ls := lines(dedup)
	var head, ep []string
	for _, l := range ls {
		if strings.Contains(l, `"schema":"`+export.SchemaSegment+`"`) {
			head = append(head, l)
		} else {
			ep = append(ep, l)
		}
	}
	join := func(v ...[]string) string {
		var all []string
		for _, x := range v {
			all = append(all, x...)
		}
		return strings.Join(all, "\n") + "\n"
	}
	cases := map[string]string{
		"missing segment":  join(head[1:], ep),
		"table after data": join(ep, head),
		"corrupt body":     join([]string{strings.Replace(head[0], `"kind":"message"`, `"kind":"message"`, 1)[:len(head[0])-12] + `"x":1}}`}, head[1:], ep),
		"bad episode":      join(head, []string{`{"schema":"sleipnir.rl/1","agents":"nope"}`}),
		"not json":         join(head, []string{"garbage"}),
		"unknown base":     join(head, []string{strings.Replace(ep[0], `"base":"solo.1"`, `"base":"ghost.1"`, 1)}),
		"keep too long":    join(head, []string{strings.Replace(ep[0], `"keep":1`, `"keep":99`, 1)}),
		"wrong kind":       join(head, []string{strings.Replace(ep[0], `"tools":"`, `"tools":"00`, 1)}),
	}
	for name, in := range cases {
		var out bytes.Buffer
		if _, err := export.Expand(&out, strings.NewReader(in), nil); err == nil {
			t.Errorf("%s: damaged input was accepted", name)
		}
	}
	// An empty stream expands to nothing.
	var out bytes.Buffer
	if n, err := export.Expand(&out, strings.NewReader(""), nil); err != nil || n != 0 || out.Len() != 0 {
		t.Errorf("empty input: %d %v", n, err)
	}
}

func TestCanonicalWithoutPromptsStaysLossless(t *testing.T) {
	src := chainRun(t, 3, nil)
	noRes := export.Source{Episode: src.Episode}
	out, st := run(t, []export.Source{noRes}, canonOpts(false))
	if st.Drops["step:no_prompt"] != 3 || len(st.Warnings) == 0 {
		t.Fatalf("stats: %+v", st)
	}
	var back rl.Episode
	if err := json.Unmarshal([]byte(lines(out)[0]), &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Agents[0].Steps) != 3 || back.Agents[0].Steps[0].Completion.Turn.Blocks == nil {
		t.Fatal("steps must survive without prompts; only their prompt references are missing")
	}
	if got := expand(t, out, nil); got != out {
		t.Fatal("expanding steps without references changes nothing")
	}
}

func TestCanonicalToFileAndBack(t *testing.T) {
	// The way the CLI uses it: main stream and table in separate files.
	srcs := group(t, 3)
	dir := t.TempDir()
	mf, _ := os.Create(filepath.Join(dir, "episodes.jsonl"))
	tf, _ := os.Create(filepath.Join(dir, "table.jsonl"))
	o := canonOpts(false)
	o.Table = tf
	if _, err := export.Export(mf, srcs, o); err != nil {
		t.Fatal(err)
	}
	mf.Close()
	tf.Close()
	m, _ := os.Open(filepath.Join(dir, "episodes.jsonl"))
	tb, _ := os.Open(filepath.Join(dir, "table.jsonl"))
	defer m.Close()
	defer tb.Close()
	var out bytes.Buffer
	if n, err := export.Expand(&out, m, tb); err != nil || n != 3 {
		t.Fatalf("expand: %d %v", n, err)
	}
	inline, _ := run(t, srcs, canonOpts(true))
	if out.String() != inline {
		t.Fatal("file round trip differs")
	}
}

// withoutTokens copies an episode with its token traces removed.
func withoutTokens(ep *rl.Episode) *rl.Episode {
	c := *ep
	c.Agents = append([]rl.Agent(nil), ep.Agents...)
	for i := range c.Agents {
		c.Agents[i].Steps = append([]rl.Step(nil), ep.Agents[i].Steps...)
		for j := range c.Agents[i].Steps {
			c.Agents[i].Steps[j].Tokens = nil
		}
	}
	return &c
}
