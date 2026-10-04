package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

type blockingProvider struct {
	do func(context.Context, *provider.Request) (*provider.Response, error)
}

func (p blockingProvider) Profile() provider.Profile {
	return provider.Profile{Name: "test", Dialect: "openai-chat", Cache: cost.OpenAICacheModel()}
}

func (p blockingProvider) Do(ctx context.Context, req *provider.Request, _ func(provider.Event)) (*provider.Response, error) {
	return p.do(ctx, req)
}

func blockingReply(text string) *provider.Response {
	return &provider.Response{Turn: core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.Text(text)}}, Stop: core.StopEnd}
}

func newBlockingAgent(t *testing.T, p blockingProvider) (*Agent, *events.MemLog) {
	t.Helper()
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens, pl.HardThreadTokens = 1, 1
	log := events.NewMemLog()
	a, err := New(Config{
		ID: "worker", Provider: p, Model: cost.Model{ID: "test", ContextTokens: 100000, Cache: p.Profile().Cache},
		Tools: tools.NewRegistry(), Events: log, Planner: pl, BlockingCompaction: true,
		Notes: kv.NewLayer("notes", kv.KindNotes, 1, []kv.Segment{{Key: "assignment", Text: "Use the accepted shared HTTP contract."}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	for i := 0; i < 10; i++ {
		a.thread.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginSystem, Blocks: []core.Block{core.Text("Inspect another file.")}})
		a.thread.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.Text(strings.Repeat("Existing implementation details and test results. ", 100))}})
	}
	return a, log
}

func TestBlockingCompactionCommitsBeforeForegroundRequest(t *testing.T) {
	for _, response := range []string{"valid", "malformed", "error"} {
		t.Run(response, func(t *testing.T) {
			var a *Agent
			forks, foreground := 0, 0
			p := blockingProvider{do: func(_ context.Context, req *provider.Request) (*provider.Response, error) {
				if strings.Contains(req.Label, ".c") {
					forks++
					switch response {
					case "valid":
						return blockingReply(`{"keep_from":"t17","spine":[{"turns":"t1-t16","line":"Inspected implementation files and tests."}]}`), nil
					case "error":
						return nil, errors.New("compactor unavailable")
					default:
						return blockingReply("not a compaction patch"), nil
					}
				}
				foreground++
				if a.comp.count != 1 || a.comp.running || len(a.thread.Snapshot().Turns) >= 20 {
					t.Error("foreground request preceded the compaction commit")
				}
				if !strings.Contains(a.stack.Notes.Text(), "accepted shared HTTP contract") {
					t.Error("compaction lost the protected assignment")
				}
				return blockingReply("finished"), nil
			}}
			var log *events.MemLog
			a, log = newBlockingAgent(t, p)
			if _, err := a.Run(context.Background(), ""); err != nil {
				t.Fatal(err)
			}
			if forks != 1 || foreground != 1 || len(log.OfType(events.TypeCompactCommit)) != 1 {
				t.Fatalf("forks=%d foreground=%d commits=%d", forks, foreground, len(log.OfType(events.TypeCompactCommit)))
			}
			if response == "valid" && len(log.OfType(events.TypeCompactReject)) != 0 {
				t.Fatal("valid model patch fell back")
			}
		})
	}
}

func TestBlockingCompactionCancellationAndClosePreventForegroundRequest(t *testing.T) {
	for _, closeAgent := range []bool{false, true} {
		name := "cancel"
		if closeAgent {
			name = "close"
		}
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{})
			var foreground atomic.Int32
			p := blockingProvider{do: func(ctx context.Context, req *provider.Request) (*provider.Response, error) {
				if !strings.Contains(req.Label, ".c") {
					foreground.Add(1)
					return blockingReply("finished"), nil
				}
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			}}
			a, _ := newBlockingAgent(t, p)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := a.Run(ctx, ""); done <- err }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("compactor never started")
			}
			// A different agent can complete while this one's compactor is blocked.
			other, _ := newBlockingAgent(t, blockingProvider{do: func(context.Context, *provider.Request) (*provider.Response, error) {
				return blockingReply("independent work finished"), nil
			}})
			other.cfg.NoCompaction = true
			if _, err := other.Run(ctx, ""); err != nil {
				t.Fatal(err)
			}
			want := context.Canceled
			if closeAgent {
				want = ErrClosed
				if err := a.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("blocking compaction did not stop")
			}
			if foreground.Load() != 0 || a.comp.running {
				t.Fatalf("foreground=%d compactor still running=%v", foreground.Load(), a.comp.running)
			}
		})
	}
}

func TestBlockingCompactionExhaustsBudgetBeforeForegroundRequest(t *testing.T) {
	for _, charge := range []float64{0.5, 0.75} {
		t.Run(fmt.Sprint(charge), func(t *testing.T) {
			forks, foreground := 0, 0
			a, _ := newBlockingAgent(t, blockingProvider{do: func(_ context.Context, req *provider.Request) (*provider.Response, error) {
				if !strings.Contains(req.Label, ".c") {
					foreground++
					return blockingReply("finished"), nil
				}
				forks++
				resp := blockingReply(`{"keep_from":"t17","spine":[{"turns":"t1-t16","line":"Inspected implementation files and tests."}]}`)
				resp.CostUSD = &charge
				return resp, nil
			}})
			if err := a.SetBudget(0.5); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Run(context.Background(), ""); !errors.Is(err, ErrBudget) {
				t.Fatalf("got %v, want ErrBudget", err)
			}
			if _, spent := a.Usage(); spent != charge || forks != 1 || foreground != 0 {
				t.Fatalf("spent=%v forks=%d foreground=%d", spent, forks, foreground)
			}
		})
	}
}
