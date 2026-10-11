package traj

import (
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// edge is an rl.Edge with the log position it was derived from, so edges can be
// emitted in log order.
type edge struct {
	rl.Edge
	seq uint64
}

// buildEdges derives the swarm DAG.
//
//	spawn    the parent's last main step before the agent.spawn event -> the new agent (Ref: task id)
//	mail     the sender's last main step before mail.send -> the recipient's first main step after
//	         mail.deliver (Ref: mail id); undelivered-in-time mail (no later step) has no edge
//	compact  the compactor call whose patch a compact.commit applied -> the first main step after
//	         it (Ref: "<agent>#<n>", n counting the agent's commits; Fallback: a mechanical patch
//	         replaced the call's); mask-only and emergency commits have no compactor call and no
//	         edge (see compactEdges for how the call is found)
//	promote  each `note` tool call between two shared-epoch commits -> "epoch:<n>" (Ref: the
//	         epoch's layer hash); facts promoted by compactors are not attributable from the
//	         log and have no edge
func (b *builder) buildEdges() {
	b.spawnEdges()
	b.mailEdges()
	b.compactEdges()
	b.promoteEdges()
	sort.SliceStable(b.edges, func(i, j int) bool { return b.edges[i].seq < b.edges[j].seq })
}

// lastMainBefore is the agent's last kept main step requested before seq.
func lastMainBefore(a *agentInfo, seq uint64) *stepInfo {
	if a == nil {
		return nil
	}
	i := sort.Search(len(a.main), func(i int) bool { return a.main[i].q.seq >= seq })
	if i == 0 {
		return nil
	}
	return a.main[i-1]
}

// firstMainAfter is the agent's first kept main step requested after seq.
func firstMainAfter(a *agentInfo, seq uint64) *stepInfo {
	if a == nil {
		return nil
	}
	i := sort.Search(len(a.main), func(i int) bool { return a.main[i].q.seq > seq })
	if i == len(a.main) {
		return nil
	}
	return a.main[i]
}

// spawnEdges links each child spawn to the parent's preceding main request, falling back to the
// parent agent when no request precedes it.
func (b *builder) spawnEdges() {
	for _, sp := range b.v.spawns {
		if sp.parent == "" {
			continue
		}
		from := sp.parent
		if st := lastMainBefore(b.agents[sp.parent], sp.seq); st != nil {
			from = st.q.id
		}
		b.edges = append(b.edges, edge{Edge: rl.Edge{Kind: rl.EdgeSpawn, From: from, To: sp.id, Ref: sp.task}, seq: sp.seq})
	}
}

func (b *builder) mailEdges() {
	deliveredAt := map[string]uint64{}
	for _, d := range b.v.delivers {
		if _, ok := deliveredAt[d.id]; !ok {
			deliveredAt[d.id] = d.seq
		}
	}
	for _, m := range b.v.mails {
		at, ok := deliveredAt[m.id]
		if !ok {
			at = m.seq
		}
		to := firstMainAfter(b.agents[m.to], at)
		if to == nil {
			continue
		}
		from := m.from
		if st := lastMainBefore(b.agents[m.from], m.seq); st != nil {
			from = st.q.id
		}
		b.edges = append(b.edges, edge{Edge: rl.Edge{Kind: rl.EdgeMail, From: from, To: to.q.id, Ref: m.id}, seq: m.seq})
	}
}

// compactEdges links each compact.commit to the compactor call it came from.
//
// A commit that names its request (req, the compactor's model.request id) is linked to exactly that
// call, provided it is a kept compactor step of the same agent requested before the commit. An empty
// req means no compactor was asked (a mask or emergency compaction), and such a commit has no edge; nor
// has one whose request never got a response, since that request is not a step.
//
// Logs written before commits named their request are linked by a heuristic: the latest compactor
// call of the agent before the commit that no earlier commit took and no commit names, except for a
// commit whose reason marks a mask or emergency compaction. It can pick the wrong call: when a commit's
// own compactor request got no response, it takes an earlier call whose patch was dropped or failed.
// It is kept only because those logs carry nothing better.
//
// The edge carries the commit's fallback: the call was made, but its answer was unusable and a
// mechanical patch was applied instead.
func (b *builder) compactEdges() {
	named := map[string]bool{}
	for _, c := range b.v.commits {
		if c.req != "" {
			named[c.req] = true
		}
	}
	used := map[string]bool{}
	nth := map[string]int{}
	for _, c := range b.v.commits {
		a := b.agents[c.agent]
		nth[c.agent]++
		if a == nil {
			continue
		}
		to := firstMainAfter(a, c.seq)
		if to == nil {
			continue
		}
		var from *stepInfo
		switch {
		case c.hasReq:
			if st := b.steps[c.req]; c.req != "" && st != nil && st.a == a && st.q.kind == rl.KindCompactor && st.q.seq < c.seq && !used[c.req] {
				from = st
			}
		case !noCompactor(c.reason):
			for _, st := range a.steps {
				if st.q.kind == rl.KindCompactor && st.q.seq < c.seq && !used[st.q.id] && !named[st.q.id] {
					from = st // the latest unused compactor call before the commit
				}
			}
		}
		if from == nil {
			continue
		}
		used[from.q.id] = true
		b.edges = append(b.edges, edge{Edge: rl.Edge{Kind: rl.EdgeCompact, From: from.q.id, To: to.q.id, Ref: fmt.Sprintf("%s#%d", c.agent, nth[c.agent]), Fallback: c.fallback}, seq: c.seq})
	}
}

// noCompactor reports whether a commit reason marks a compaction that asked no compactor: the agent
// prefixes the reason of a mask-only commit with "mask: " and that of an emergency one with "emergency: ".
func noCompactor(reason string) bool {
	return strings.HasPrefix(reason, "mask: ") || strings.HasPrefix(reason, "emergency: ")
}

// promoteEdges links note-producing requests between shared-epoch boundaries to the new epoch,
// emitting one edge per request per epoch.
func (b *builder) promoteEdges() {
	prev := uint64(0)
	n := 0
	for _, l := range b.v.layers {
		if l.scope != "shared-epoch" {
			continue
		}
		n++
		seen := map[string]bool{}
		for _, run := range b.runs {
			if run.name != "note" || run.seq <= prev || run.seq >= l.seq || seen[run.step.q.id] {
				continue
			}
			seen[run.step.q.id] = true
			b.edges = append(b.edges, edge{Edge: rl.Edge{Kind: rl.EdgePromote, From: run.step.q.id, To: fmt.Sprintf("epoch:%d", n), Ref: l.hash}, seq: l.seq})
		}
		prev = l.seq
	}
}
