package export_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/rl/export"
	"github.com/reee344/sleipnir/internal/rl/redact"
)

func newRedactor() *redact.Redactor { return redact.New(redact.Config{Salt: "export-test"}) }

// leaky records a run whose secrets appear everywhere the exporter reads text:
// the user's first message, the model's tool arguments, the tool result, and a
// home-directory path.
func leaky(t testing.TB, tokens bool) (export.Source, []string) {
	secrets := []string{"hunter2hunter2", ghToken, awsKey, "/home/alice"}
	src := rollout(t, variant{
		plan: "reading /home/alice/api/a.go", reward: 1, pass: true, tokens: tokens,
		user:     "fix the bug; DB_PASSWORD=hunter2hunter2 is in the env",
		input:    `{"path":"/home/alice/api/a.go","token":"` + ghToken + `"}`,
		resultTx: "package api // key " + awsKey,
	})
	return src, secrets
}

func TestRedactionCoversEveryTextSurface(t *testing.T) {
	src, secrets := leaky(t, false)
	for _, f := range []export.Format{export.FormatSteps, export.FormatKTO, export.FormatSFT, export.FormatGroups, export.FormatDPO, export.FormatATIF, export.FormatCanonical} {
		for _, inline := range []bool{true, false} {
			var table strings.Builder
			o := export.Options{Format: f, KeepFlat: true, DropFlagged: true, Redactor: newRedactor(), Inline: inline, Table: &table, PackSegments: f == export.FormatSFT}
			out, st := run(t, []export.Source{src}, o)
			if out == "" && f != export.FormatDPO {
				t.Fatalf("%s: no output", f)
			}
			all := out + table.String()
			for _, s := range secrets {
				if strings.Contains(all, s) {
					t.Errorf("%s (inline=%v): %q survived redaction", f, inline, s)
				}
			}
			if f != export.FormatDPO && (st.Redactions["secret"] == 0 || st.Redactions["github"] == 0 || st.Redactions["aws"] == 0 || st.Redactions["path"] == 0) {
				t.Errorf("%s: redaction stats %v", f, st.Redactions)
			}
			if all != "" && !json.Valid([]byte(strings.SplitN(all, "\n", 2)[0])) {
				t.Errorf("%s: output is not JSONL after redaction", f)
			}
		}
	}
}

func TestRedactionIsConsistentAcrossStepsAndSurfaces(t *testing.T) {
	src, _ := leaky(t, false)
	out, _ := run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, Redactor: newRedactor()})
	recs := decode(t, out)
	if len(recs) != 2 {
		t.Fatalf("%d records", len(recs))
	}
	p1, p2 := recs[0]["prompt"].([]any), recs[1]["prompt"].([]any)
	// Prefix sharing survives: the second prompt starts with the first, message for message.
	for i := range p1 {
		a, _ := json.Marshal(p1[i])
		b, _ := json.Marshal(p2[i])
		if string(a) != string(b) {
			t.Fatalf("message %d differs between steps after redaction:\n%s\n%s", i, a, b)
		}
	}
	// The assistant turn is redacted identically as a completion and as history:
	// same token for the same GitHub token.
	comp, _ := json.Marshal(recs[0]["completion"])
	var hist string
	for _, m := range p2 {
		if mm := m.(map[string]any); mm["role"] == "assistant" {
			b, _ := json.Marshal(mm)
			hist = string(b)
		}
	}
	// completion is a one-element list; compare the message inside.
	var list []json.RawMessage
	_ = json.Unmarshal(comp, &list)
	if !sameTokens(string(list[0]), hist) {
		t.Fatalf("the same secret must get the same token everywhere:\n%s\n%s", list[0], hist)
	}
	// tool_calls arguments are still JSON objects with the token in place.
	var msg struct {
		ToolCalls []struct {
			Function struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	_ = json.Unmarshal(list[0], &msg)
	args := msg.ToolCalls[0].Function.Arguments
	if !strings.HasPrefix(args["token"].(string), "⟦redacted:github:") || args["path"] != "/home/user/api/a.go" {
		t.Fatalf("arguments: %v", args)
	}
}

func sameTokens(a, b string) bool {
	extract := func(s string) []string {
		var toks []string
		for {
			i := strings.Index(s, "⟦redacted:")
			if i < 0 {
				return toks
			}
			j := strings.Index(s[i:], "⟧")
			toks = append(toks, s[i:i+j+len("⟧")])
			s = s[i+j:]
		}
	}
	ta, tb := extract(a), extract(b)
	if len(ta) == 0 || len(ta) != len(tb) {
		return false
	}
	for i := range ta {
		if ta[i] != tb[i] {
			return false
		}
	}
	return true
}

func TestRedactionStatsAreThisExportsOwn(t *testing.T) {
	src, _ := leaky(t, false)
	red := newRedactor()
	o := export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, Redactor: red}
	_, first := run(t, []export.Source{src}, o)
	_, second := run(t, []export.Source{src}, o)
	if first.Redactions["github"] == 0 || first.Redactions["github"] != second.Redactions["github"] {
		t.Fatalf("stats must be per export, not cumulative: %v vs %v", first.Redactions, second.Redactions)
	}
	if red.Stats()["github"] != first.Redactions["github"]+second.Redactions["github"] {
		t.Fatal("the redactor's own counters keep accumulating")
	}
	// No redactor: text goes out as recorded.
	out, st := run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true})
	if !strings.Contains(out, "hunter2hunter2") || len(st.Redactions) != 0 {
		t.Fatalf("without a Redactor nothing is redacted: %v", st.Redactions)
	}
}

// TestRedactedStepsNeverKeepTokenIds: token ids encode the original text, so a
// step whose text redaction changes must not export its ids, and nothing may be
// re-tokenised in their place.
func TestRedactedStepsNeverKeepTokenIds(t *testing.T) {
	src, _ := leaky(t, true)
	// With redaction on, every step's prompt contains the secret: nothing is exported.
	out, st := run(t, []export.Source{src}, export.Options{Format: export.FormatTokens, KeepFlat: true, DropFlagged: true, Redactor: newRedactor(), PackSegments: true})
	if out != "" || st.Drops["step:redacted_tokens"] != 2 || st.PackFailed["redacted"] != 1 {
		t.Fatalf("out=%d drops=%v packFailed=%v", len(out), st.Drops, st.PackFailed)
	}
	// Without redaction the ids are exported.
	out, _ = run(t, []export.Source{src}, export.Options{Format: export.FormatTokens, KeepFlat: true, DropFlagged: true})
	if len(decode(t, out)) != 2 {
		t.Fatalf("plain export: %d records", len(decode(t, out)))
	}
	// A clean episode passes through a redactor untouched, ids included.
	clean := rollout(t, variant{plan: "A", reward: 1, pass: true, tokens: true})
	out, st = run(t, []export.Source{clean}, export.Options{Format: export.FormatTokens, KeepFlat: true, DropFlagged: true, Redactor: newRedactor()})
	if len(decode(t, out)) != 2 || len(st.Drops) != 0 || len(st.Redactions) != 0 {
		t.Fatalf("clean episode: %d records, %v %v", len(decode(t, out)), st.Drops, st.Redactions)
	}
	// Canonical drops the traces of changed steps and keeps the rest.
	out, st = run(t, []export.Source{src}, export.Options{Format: export.FormatCanonical, DropFlagged: true, Inline: true, Redactor: newRedactor()})
	if strings.Contains(out, `"prompt_ids"`) || st.Drops["step:redacted_tokens"] != 2 {
		t.Fatalf("canonical must not keep ids that encode a secret: %v", st.Drops)
	}
	out, _ = run(t, []export.Source{src}, export.Options{Format: export.FormatATIF, DropFlagged: true, Redactor: newRedactor()})
	if strings.Contains(out, "prompt_token_ids") || strings.Contains(out, "completion_token_ids") {
		t.Fatal("atif must not keep ids that encode a secret")
	}
	out, _ = run(t, []export.Source{src}, export.Options{Format: export.FormatATIF, DropFlagged: true})
	if !strings.Contains(out, "prompt_token_ids") {
		t.Fatal("atif keeps ids when nothing is redacted")
	}
}

func TestGroupsDropLogprobsOfRedactedText(t *testing.T) {
	src, _ := leaky(t, true)
	out, _ := run(t, []export.Source{src}, export.Options{Format: export.FormatGroups, KeepFlat: true, DropFlagged: true})
	if !strings.Contains(out, `"logprobs":{"content":[`) {
		t.Fatal("groups carry logprobs when the trace is usable")
	}
	out, _ = run(t, []export.Source{src}, export.Options{Format: export.FormatGroups, KeepFlat: true, DropFlagged: true, Redactor: newRedactor()})
	if strings.Contains(out, `"token_id:`) || !strings.Contains(out, `"logprobs":null`) {
		t.Fatal("logprobs of ids that encode a secret must be omitted")
	}
}
