package kv_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/swarm"
	"github.com/reee344/sleipnir/internal/tools"
	"github.com/reee344/sleipnir/internal/tools/fs"
	"github.com/reee344/sleipnir/internal/tools/recall"
	"github.com/reee344/sleipnir/internal/tools/shell"
	"github.com/reee344/sleipnir/internal/tools/skilltool"
	"github.com/reee344/sleipnir/internal/tools/web"
)

// searchStub stands in for a configured search backend (a key in the environment), which
// is what makes a session register web_search.
type searchStub struct{}

func (searchStub) Search(context.Context, string, int) ([]web.SearchResult, error) { return nil, nil }

// sessionRegistry registers the tools of a session the way Session.build does
// (internal/session/session.go): the file, shell and web tools, recall, skill, and, in a
// swarm, the coordination tools. MCP servers, which a person configures, are not part
// of it. The registry's Specs is the exact list every agent of the session sends.
func sessionRegistry(t *testing.T, swarmTools, search bool) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	fs.Register(reg)
	mgr := shell.NewManager()
	t.Cleanup(mgr.Shutdown)
	shell.Register(reg, mgr)
	cfg := web.Config{}
	if search {
		cfg.Backend = searchStub{}
	}
	web.Register(reg, cfg)
	reg.Register(recall.New(kv.NewArchive(nil)))
	reg.Register(skilltool.New(nil))
	if swarmTools {
		sw := swarm.New(swarm.DefaultConfig(), swarm.Deps{}, nil)
		t.Cleanup(sw.Shutdown)
		for _, tl := range sw.Tools() {
			reg.Register(tl)
		}
	}
	return reg
}

func sessionSpecs(t *testing.T, swarmTools, search bool) []core.ToolSpec {
	t.Helper()
	specs, err := sessionRegistry(t, swarmTools, search).Specs()
	if err != nil {
		t.Fatal(err)
	}
	return specs
}

func names(specs []core.ToolSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// TestGoldenToolSpecs pins the tool list every agent sends: each tool's name, description
// and input schema, the order, and the hash of the serialised list (the tools hash a
// request's manifest records). The list is the same for every agent and every role, so the
// provider caches it once for the swarm; one changed byte in it is a new prefix for all of
// them. The expected bytes are in testdata/golden/tools.
func TestGoldenToolSpecs(t *testing.T) {
	sessions := []struct {
		name          string
		swarm, search bool
	}{
		{"solo", false, false},
		{"solo+web_search", false, true},
		{"swarm", true, false},
		{"swarm+web_search", true, true},
	}

	t.Run("list", func(t *testing.T) {
		specs := sessionSpecs(t, true, true) // every tool the harness can offer
		raw, err := core.MarshalStable(specs)
		if err != nil {
			t.Fatal(err)
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err != nil {
			t.Fatal(err)
		}
		pretty.WriteByte('\n')
		golden(t, "tools/tools.json", pretty.Bytes())
	})

	t.Run("hashes", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString("# The tools every agent sends: sha256 of each tool's serialised spec, and of the whole serialised list per kind of session\n")
		sb.WriteString("# (core.MarshalStable of the tools, what a request's manifest hashes as its tools blob). A session's list is the registry's Specs().\n")
		sb.WriteString("# columns: what, byte count, sha256, then for a session the tools in order\n")
		sb.WriteString("# regenerate: go test ./internal/kv -run TestGoldenToolSpecs -update   (read docs/BUILDING.md, \"Changing prompt bytes\", first)\n")
		for _, s := range sessionSpecs(t, true, true) {
			b, err := core.MarshalStable(s)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&sb, "tool %s\t%d\t%x\n", s.Name, len(b), sha256.Sum256(b))
		}
		for _, se := range sessions {
			specs := sessionSpecs(t, se.swarm, se.search)
			b, err := core.MarshalStable(specs)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&sb, "session %s\t%d\t%x\t%s\n", se.name, len(b), sha256.Sum256(b), strings.Join(names(specs), ","))
		}
		golden(t, "tools/hashes.txt", []byte(sb.String()))
	})

	t.Run("same_for_every_agent", func(t *testing.T) {
		// The list does not depend on when or in what order the tools were registered, nor on
		// Go's map order (Registry keeps its tools in a map): register the same tools in many
		// orders and ask again and again; every answer is the same bytes.
		base := sessionRegistry(t, true, true)
		want, err := base.Specs()
		if err != nil {
			t.Fatal(err)
		}
		wantBytes, _ := core.MarshalStable(want)
		if !sort.StringsAreSorted(names(want)) {
			t.Fatalf("the list is not sorted by name: %v", names(want))
		}
		for seed := int64(0); seed < 20; seed++ {
			order := names(want)
			rand.New(rand.NewSource(seed)).Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
			reg := tools.NewRegistry()
			for _, n := range order {
				tl, ok := base.Get(n)
				if !ok {
					t.Fatalf("tool %s vanished", n)
				}
				reg.Register(tl)
			}
			for i := 0; i < 3; i++ {
				got, err := reg.Specs()
				if err != nil {
					t.Fatal(err)
				}
				if gotBytes, _ := core.MarshalStable(got); !bytes.Equal(gotBytes, wantBytes) {
					t.Fatalf("seed %d, request %d: the tool list depends on registration order or map order", seed, i)
				}
			}
		}
		// kv.SortTools, which tests and the simulator use for the same job, agrees byte for byte.
		sorted, err := kv.SortTools(want)
		if err != nil {
			t.Fatal(err)
		}
		if sortedBytes, _ := core.MarshalStable(sorted); !bytes.Equal(sortedBytes, wantBytes) {
			t.Fatal("kv.SortTools and Registry.Specs disagree about the list")
		}
	})

	t.Run("schemas_are_canonical", func(t *testing.T) {
		// A schema that is not its own canonical form would be re-spelled by anything that
		// canonicalises it, and two agents that did and did not would send different bytes.
		seen := map[string]bool{}
		for _, s := range sessionSpecs(t, true, true) {
			if seen[s.Name] {
				t.Errorf("tool %s is listed twice", s.Name)
			}
			seen[s.Name] = true
			canon, err := core.Canonical(s.InputSchema)
			if err != nil || !bytes.Equal(canon, s.InputSchema) {
				t.Errorf("tool %s: the schema is not canonical JSON", s.Name)
			}
			if strings.TrimSpace(s.Description) == "" {
				t.Errorf("tool %s has no description", s.Name)
			}
		}
	})
}
