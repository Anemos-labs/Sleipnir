package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func TestBlockingCompactionConfigurationReachesSoloAndSwarm(t *testing.T) {
	for _, team := range []bool{false, true} {
		name := "solo"
		if team {
			name = "swarm"
		}
		t.Run(name, func(t *testing.T) {
			var a *agent.Agent
			client, model := startMock(t, func(c *mock.Call) mock.Reply {
				if strings.Contains(c.LastUser(), "<compactor-task>") {
					return mock.Reply{Text: `{"keep_from":"t17","spine":[{"turns":"t1-t16","line":"Inspected implementation files and tests."}]}`}
				}
				if len(a.Thread().Snapshot().Turns) >= 20 {
					t.Error("configured blocking compaction did not commit before the request")
				}
				return mock.Reply{Text: "finished"}
			})
			o := opts(t, newRepo(t), client, model)
			o.Config = config.Defaults()
			o.Config.Cache.CompactionMode = "blocking"
			o.Config.Cache.CompactThresholdTokens = 1
			o.Swarm = team
			s, err := session.New(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			a = s.Agent
			if team {
				a, err = s.Swarm.StartIdleManager()
				if err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 10; i++ {
				a.Thread().Append(core.Turn{Role: core.RoleUser, Origin: core.OriginSystem, Blocks: []core.Block{core.Text("Inspect another file.")}})
				a.Thread().Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.Text(strings.Repeat("Existing implementation details and test results. ", 100))}})
			}
			if _, err := a.Run(context.Background(), ""); err != nil {
				t.Fatal(err)
			}
		})
	}
}
