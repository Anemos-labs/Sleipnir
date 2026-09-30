package export_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/export"
)

type failWriter struct{ after int }

func (w *failWriter) Write(p []byte) (int, error) {
	if w.after <= 0 {
		return 0, errors.New("disk full")
	}
	w.after -= len(p)
	return len(p), nil
}

func TestWriterErrorsAreReturned(t *testing.T) {
	srcs := group(t, 3)
	for _, f := range []export.Format{export.FormatSteps, export.FormatTokens, export.FormatCanonical} {
		_, err := export.Export(&failWriter{}, srcs, export.Options{Format: f, KeepFlat: true, DropFlagged: true, Inline: true})
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Errorf("%s: %v", f, err)
		}
	}
	// A table writer that fails is reported too.
	_, err := export.Export(io.Discard, srcs, export.Options{Format: export.FormatCanonical, KeepFlat: true, DropFlagged: true, Table: &failWriter{}})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("table writer: %v", err)
	}
}

func TestWireOptionsShapeThePrompt(t *testing.T) {
	src := rollout(t, variant{plan: "A", reward: 1, pass: true})
	out, _ := run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, Wire: openaichat.Options{SystemRole: "developer", MaxTokensField: "max_completion_tokens", ExtraBody: map[string]any{"foo": 1}}})
	recs := decode(t, out)
	first := recs[0]["prompt"].([]any)[0].(map[string]any)
	if first["role"] != "developer" {
		t.Fatalf("SystemRole must reach the rendering: %v", first)
	}
	if strings.Contains(out, `"foo"`) || strings.Contains(out, "max_completion_tokens") {
		t.Fatal("request parameters are not part of the training prompt")
	}
}

func TestEmptyAndDegenerateEpisodes(t *testing.T) {
	src := rollout(t, variant{plan: "A", reward: 1, pass: true})
	// An episode with no agents contributes nothing but is counted.
	empty := src
	ep := *src.Episode
	ep.Agents = nil
	empty.Episode = &ep
	for _, f := range export.Formats() {
		out, st := run(t, []export.Source{empty}, export.Options{Format: f, KeepFlat: true, DropFlagged: true, Inline: true})
		if st.Episodes != 1 {
			t.Errorf("%s: %+v", f, st)
		}
		if f != export.FormatCanonical && out != "" {
			t.Errorf("%s: an episode without steps produced %q", f, out)
		}
	}
	// A step with an empty completion is skipped, not exported as an empty target.
	src2 := rollout(t, variant{plan: "A", reward: 1, pass: true})
	src2.Episode.Agents[0].Steps[0].Completion.Turn.Blocks = nil
	out, st := run(t, []export.Source{src2}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true})
	if len(lines(out)) != 1 || st.Drops["step:empty_completion"] != 1 {
		t.Fatalf("drops: %v", st.Drops)
	}
	// A nil resolver result is an error, not a panic.
	src3 := rollout(t, variant{plan: "A", reward: 1, pass: true})
	src3.Prompts = nilResolver{}
	_, st = run(t, []export.Source{src3}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true})
	if st.Drops["step:no_prompt"] != 2 {
		t.Fatalf("drops: %v", st.Drops)
	}
}

type nilResolver struct{}

func (nilResolver) Prompt(_ *rl.Episode, _ *rl.Step) (*core.Prompt, error) { return nil, nil }
