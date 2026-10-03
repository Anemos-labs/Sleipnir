package agent_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

func TestRecoveryMailIsInFirstRequestAfterFailure(t *testing.T) {
	for name, mode := range map[string]kv.HotMode{"inline": kv.HotInline, "persisted": kv.HotPersist, "turn scoped": kv.HotTurnScoped} {
		t.Run(name, func(t *testing.T) {
			p := &cxProv{prof: cxAnthropicProfile()}
			p.prof.TurnScopedSystem = mode == kv.HotTurnScoped
			calls := 0
			failed := errors.New("request failed")
			p.handle = func(p *cxProv, req *provider.Request) (*provider.Response, error) {
				calls++
				if calls == 1 {
					return &provider.Response{Turn: cxThinkingTurn(req.Prompt, "initial result"), Stop: core.StopEnd}, nil
				}
				if calls == 2 {
					return nil, failed
				}
				seen := false
				for _, message := range req.Prompt.Messages {
					for _, block := range message.Blocks {
						seen = seen || (message.Role == core.RoleUser && strings.Contains(block.Text, "recovery instructions"))
					}
				}
				if !seen {
					t.Error("first recovery request omitted the queued mail")
				}
				return &provider.Response{Turn: core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{core.Text("recovered")}}, Stop: core.StopEnd}, nil
			}
			a, _ := cxAgent(t, cxOpts{prov: p, noCompct: true, hotMode: mode,
				hot: func(string) []core.Block { return []core.Block{core.Text("board unchanged")} },
			})
			t.Cleanup(func() { _ = a.Close() })
			if _, err := a.RunTask(context.Background(), "initial task", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := a.RunTask(context.Background(), "task", ""); !errors.Is(err, failed) {
				t.Fatalf("initial run: %v", err)
			}
			before := a.Thread().Snapshot()
			if mode == kv.HotTurnScoped && before.Turns[len(before.Turns)-1].Role != core.RoleSystem {
				t.Fatal("fixture did not produce a turn-scoped board view")
			}
			a.Send("[mail h1 from harness] recovery instructions")
			if _, err := a.RunTask(context.Background(), "", ""); err != nil {
				t.Fatal(err)
			}
			if calls != 3 || p.rejects != 0 {
				t.Fatalf("got %d requests and %d thinking-binding rejections; want setup, failure, recovery", calls, p.rejects)
			}
			after := a.Thread().Snapshot()
			mail := after.Turns[len(before.Turns)]
			if mail.Origin != core.OriginMail || kv.IsSteer(mail.Blocks[0]) {
				t.Fatal("worker mail became user steering during recovery")
			}
			if !reflect.DeepEqual(before.Turns, after.Turns[:len(before.Turns)]) || before.Epoch != after.Epoch {
				t.Fatal("recovery rewrote existing conversation turns")
			}
			if a.PendingInbox() != 0 {
				t.Fatal("recovery mail remains unread")
			}
		})
	}
}
