package reward

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// Builders for hand-made episodes. Tests state only what a case depends on.

var t0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

type stepOpt func(*rl.Step)

func mkStep(id string, opts ...stepOpt) rl.Step {
	st := rl.Step{ID: id, Kind: rl.KindMain, Trainable: true}
	for _, o := range opts {
		o(&st)
	}
	return st
}

func withKind(k string) stepOpt { return func(s *rl.Step) { s.Kind = k } }
func withRole(r string) stepOpt { return func(s *rl.Step) { s.Role = r } }
func withSeg(seg, epoch int) stepOpt {
	return func(s *rl.Step) { s.Segment, s.Epoch = seg, epoch }
}
func withAt(d time.Duration) stepOpt { return func(s *rl.Step) { s.At = t0.Add(d) } }
func withLatency(ms int64) stepOpt   { return func(s *rl.Step) { s.LatencyMs = ms } }
func withPrompt(tokens int, shared string) stepOpt {
	return func(s *rl.Step) { s.Prompt.Tokens, s.Prompt.SharedPrefix = tokens, shared }
}
func withOut(n int) stepOpt { return func(s *rl.Step) { s.Usage.OutputTokens = n } }
func withUsage(in, read, w5, out int) stepOpt {
	return func(s *rl.Step) {
		s.Usage = core.Usage{InputTokens: in, CacheReadTokens: read, CacheWrite5mTokens: w5, OutputTokens: out}
	}
}
func withText(text string) stepOpt {
	return func(s *rl.Step) {
		s.Completion.Turn = core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{core.Text(text)}}
		s.Completion.Stop = core.StopEnd
	}
}
func withTurnID(id int64) stepOpt { return func(s *rl.Step) { s.Completion.Turn.ID = core.TurnID(id) } }
func withObs(obs ...rl.Observation) stepOpt {
	return func(s *rl.Step) { s.Observations = append(s.Observations, obs...) }
}
func withStop(r core.StopReason) stepOpt { return func(s *rl.Step) { s.Completion.Stop = r } }
func withInline(text string) stepOpt {
	return func(s *rl.Step) {
		s.Inline = &core.Prompt{Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text(text)}}}}
	}
}

func jsonRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func obs(name string, input any, output string) rl.Observation {
	return rl.Observation{ToolID: "c-" + name, Name: name, Input: jsonRaw(input), Output: output}
}

func obsErr(name string, input any, output string) rl.Observation {
	o := obs(name, input, output)
	o.IsError = true
	return o
}

func bashObs(cmd, output string) rl.Observation {
	return obs("bash", map[string]any{"command": cmd}, output)
}

func writeObs(path string) rl.Observation {
	return obs("write", map[string]any{"path": path, "content": "x"}, "wrote "+path)
}

func mkAgent(id, role string, steps ...rl.Step) rl.Agent {
	return rl.Agent{ID: id, Role: role, Steps: steps}
}

func mkEpisode(id string, agents ...rl.Agent) *rl.Episode {
	ep := &rl.Episode{Schema: rl.SchemaEpisode, ID: id, TaskID: strings.SplitN(id, "/", 2)[0], Agents: agents}
	ep.Outcome.Verifier = &rl.Verdict{Kind: "verifier", Pass: true, Score: 1}
	ep.Outcome.Claimed = "done"
	return ep
}

func passing(score float64) *rl.Verdict {
	return &rl.Verdict{Kind: "verifier", Pass: score >= 1, Score: score}
}

func failing() *rl.Verdict { return &rl.Verdict{Kind: "verifier", Pass: false, Score: 0} }

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > 1e-9*math.Max(1, math.Abs(want)) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func mustScore(t *testing.T, ep *rl.Episode, task *rl.Task, cfg Config, d DiffSource) {
	t.Helper()
	if err := Score(ep, task, cfg, d); err != nil {
		t.Fatalf("Score: %v", err)
	}
}

// diffOf assembles a git-style diff for one file from body lines.
func gitDiff(path string, body ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\nindex 1111111..2222222 100644\n--- a/%s\n+++ b/%s\n", path, path, path, path)
	b.WriteString(strings.Join(body, "\n"))
	b.WriteByte('\n')
	return b.String()
}

// hunk builds a hunk with correct counts from +/-/space prefixed lines.
func hunkOf(lines ...string) string {
	oldN, newN := 0, 0
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+"):
			newN++
		case strings.HasPrefix(l, "-"):
			oldN++
		default:
			oldN++
			newN++
		}
	}
	return fmt.Sprintf("@@ -1,%d +1,%d @@\n%s", oldN, newN, strings.Join(lines, "\n"))
}

func newFileDiff(path string, lines ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\nnew file mode 100644\nindex 0000000..2222222\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", path, path, path, len(lines))
	for _, l := range lines {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

func deletedFileDiff(path string, lines ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\ndeleted file mode 100644\nindex 1111111..0000000\n--- a/%s\n+++ /dev/null\n@@ -1,%d +0,0 @@\n", path, path, path, len(lines))
	for _, l := range lines {
		b.WriteString("-" + l + "\n")
	}
	return b.String()
}

// scoreDiff runs the hack detectors on a diff and returns the flags that fired.
func scoreDiff(t *testing.T, task *rl.Task, diff string, calls ...rl.Observation) *rl.Episode {
	t.Helper()
	ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(1000, ""), withOut(5), withObs(calls...))))
	src := DiffMap{}
	if diff != "" {
		h := core.HashString(diff)
		src[h] = diff
		ep.Outcome.Diff = h
	}
	if task == nil {
		task = &rl.Task{}
	}
	mustScore(t, ep, task, DefaultConfig(), src)
	return ep
}

func hasFlag(ep *rl.Episode, f string) bool { return ep.Has(f) }

func flagsOf(ep *rl.Episode) string { return strings.Join(ep.Flags, ",") }
