package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/provider/mock"
)

// TestLogRecordsExactPromptsCompletionsAndTokens is the fidelity contract that
// RL training data rests on: from the event log and blob store alone, every
// model call's prompt can be rebuilt byte for byte (verified against its wire
// hash), its completion is stored with provider-native blocks, and token ids and
// logprobs are kept when the endpoint returned them.
func TestLogRecordsExactPromptsCompletionsAndTokens(t *testing.T) {
	blobs := events.NewMemBlobs()
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens = 6500
	pl.MinThreadTokens = 2000
	r := newRig(t, rigOpts{planner: pl, blobs: blobs, capture: true,
		mock: mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}},
		scriptedWork(22, func(c *mock.Call) string { return compactorPatch(6)(c) }))
	if _, err := r.agent.Run(context.Background(), "please build the whole thing"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	type req struct {
		Req      string    `json:"req"`
		Agent    string    `json:"agent"`
		Role     string    `json:"role"`
		Kind     string    `json:"kind"`
		Renderer string    `json:"renderer"`
		Wire     core.Hash `json:"wire_hash"`
		Manifest core.Manifest
	}
	msgsOf := map[string][]core.Hash{}
	var mains, forks, deltas, rebases int
	prevLen := 0
	for _, e := range r.log.OfType(events.TypeModelRequest) {
		var q req
		if err := json.Unmarshal(e.Data, &q); err != nil {
			t.Fatal(err)
		}
		var raw struct {
			Manifest core.Manifest `json:"manifest"`
		}
		json.Unmarshal(e.Data, &raw)
		q.Manifest = raw.Manifest
		if q.Renderer != kv.RendererVersion || q.Agent != "be-1" || q.Wire == "" || q.Manifest.Wire != q.Wire {
			t.Fatalf("request %s is missing lineage or hashes: %+v", q.Req, q)
		}
		base := msgsOf[q.Manifest.Base]
		if q.Manifest.Base != "" && base == nil {
			t.Fatalf("request %s deltas against unknown base %q", q.Req, q.Manifest.Base)
		}
		p, msgs, err := q.Manifest.Expand(base, blobs.Get)
		if err != nil {
			t.Fatalf("request %s does not expand: %v", q.Req, err)
		}
		if len(p.Tools) == 0 || len(p.System) == 0 || len(p.Messages) == 0 {
			t.Fatalf("request %s expanded to an incomplete prompt", q.Req)
		}
		switch q.Kind {
		case "main":
			mains++
			msgsOf[q.Req] = msgs // only main requests chain
			if q.Manifest.Keep > 0 {
				deltas++
			}
			if len(msgs) < prevLen {
				rebases++ // a commit shortened the thread
			}
			prevLen = len(msgs)
			if q.Role != "backend" {
				t.Fatalf("main request role %q", q.Role)
			}
		case "compactor":
			forks++
			if q.Role != "compactor" {
				t.Fatalf("fork trained role %q", q.Role)
			}
		default:
			t.Fatalf("unknown kind %q", q.Kind)
		}
	}
	// Every rebase rewrites message 0 (notes and spine live in the preamble), so
	// the only non-delta requests are the first one and the first after each
	// commit; everything else is "previous messages plus two".
	commits := len(r.log.OfType(events.TypeCompactCommit))
	if mains < 20 || mains-deltas > 1+commits {
		t.Fatalf("steady state should be delta-encoded: %d main requests, %d deltas, %d commits", mains, deltas, commits)
	}
	if forks == 0 || rebases == 0 {
		t.Fatalf("expected a compaction fork and a rebase in the log: forks=%d rebases=%d", forks, rebases)
	}

	responses := 0
	for _, e := range r.log.OfType(events.TypeModelResponse) {
		var resp struct {
			Req        string    `json:"req"`
			Completion core.Hash `json:"completion"`
			Tokens     core.Hash `json:"tokens"`
		}
		json.Unmarshal(e.Data, &resp)
		if resp.Completion == "" {
			t.Fatalf("response %s has no completion blob", resp.Req)
		}
		b, err := blobs.Get(resp.Completion)
		if err != nil {
			t.Fatal(err)
		}
		var turn core.Turn
		if err := json.Unmarshal(b, &turn); err != nil || turn.Role != core.RoleAssistant || len(turn.Blocks) == 0 {
			t.Fatalf("completion blob of %s is not an assistant turn: %v %s", resp.Req, err, b)
		}
		tb, err := blobs.Get(resp.Tokens)
		if err != nil {
			t.Fatalf("response %s has no token trace: %v", resp.Req, err)
		}
		var tr core.TokenTrace
		if err := json.Unmarshal(tb, &tr); err != nil || !tr.Consistent() || len(tr.PromptIDs) == 0 {
			t.Fatalf("token trace of %s is unusable: %v %+v", resp.Req, err, tr)
		}
		responses++
	}
	if responses != mains+forks {
		t.Fatalf("every call needs a response record: %d responses for %d calls", responses, mains+forks)
	}

	// SLEIPNIR_FIXTURE_DIR=path go test -run TestLogRecords ./internal/agent
	// writes this run as a run directory (events.jsonl + blobs/) so the RL
	// pipeline's tests can use a real recording.
	if dir := os.Getenv("SLEIPNIR_FIXTURE_DIR"); dir != "" {
		writeRunDir(t, dir, r.log.All(), blobs)
	}
}

func writeRunDir(t *testing.T, dir string, evs []events.Event, blobs *events.MemBlobs) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, e := range evs {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		out = append(append(out, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), out, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := events.NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blobs.All() {
		if _, err := store.Put(b); err != nil {
			t.Fatal(err)
		}
	}
}
