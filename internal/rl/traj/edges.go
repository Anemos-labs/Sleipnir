package traj

import (
	"fmt"
	"sort"

	"github.com/reee344/sleipnir/internal/rl"
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
//	compact  the compactor call that produced a compact.commit -> the first main step after it
//	         (Ref: "<agent>#<n>", n counting the agent's commits); mask-only and emergency
//	         commits have no compactor call and no edge
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

func (b *builder) compactEdges() {
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
		for _, st := range a.steps {
			if st.q.kind == rl.KindCompactor && st.q.seq < c.seq && !used[st.q.id] {
				from = st // the latest unused compactor call before the commit
			}
		}
		if from == nil {
			continue
		}
		used[from.q.id] = true
		b.edges = append(b.edges, edge{Edge: rl.Edge{Kind: rl.EdgeCompact, From: from.q.id, To: to.q.id, Ref: fmt.Sprintf("%s#%d", c.agent, nth[c.agent])}, seq: c.seq})
	}
}

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
