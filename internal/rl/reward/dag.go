package reward

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// roleClass maps a role name to the role tag a step trains. The harness names
// worker roles freely ("backend", "tests"): anything that is not manager,
// reviewer, compactor or mailman is a worker, so per-role baselines have
// enough samples and one weight set serves all of them.
func roleClass(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case rl.RoleManager, "orchestrator", "lead":
		return rl.RoleManager
	case rl.RoleReviewer:
		return rl.RoleReviewer
	case rl.RoleCompactor:
		return rl.RoleCompactor
	case rl.RoleMailman, "mail":
		return rl.RoleMailman
	}
	return rl.RoleWorker
}

// RoleClass is roleClass for other packages (the advantage estimator groups by
// it).
func RoleClass(role string) string { return roleClass(role) }

// StepRole is the role a step trains: its own Role tag when set, otherwise
// derived from its kind (a compactor call of a worker agent trains "compactor"),
// otherwise its agent's role.
func StepRole(a *rl.Agent, s *rl.Step) string { return stepRole(a, s) }

func stepRole(a *rl.Agent, s *rl.Step) string {
	if s.Role != "" {
		return roleClass(s.Role)
	}
	switch s.Kind {
	case rl.KindCompactor:
		return rl.RoleCompactor
	case rl.KindMailman:
		return rl.RoleMailman
	}
	return roleClass(a.Role)
}

// sig reads a signal, treating missing and non-finite values as absent and
// negative ones as zero (signals are counts).
func sig(ep *rl.Episode, name string) (float64, bool) {
	v, ok := ep.Signals[name]
	if !ok || !finite(v) {
		return 0, false
	}
	if v < 0 {
		v = 0
	}
	return v, true
}

func sigOr(ep *rl.Episode, name string, def float64) float64 {
	if v, ok := sig(ep, name); ok {
		return v
	}
	return def
}

// workSteps is the number of steps that do the team's work (everything except
// forks such as compactor and mailman calls).
func workSteps(ep *rl.Episode) (total, workers int) {
	for ai := range ep.Agents {
		a := &ep.Agents[ai]
		for si := range a.Steps {
			if isFork(&a.Steps[si]) {
				continue
			}
			total++
			if roleClass(a.Role) != rl.RoleManager {
				workers++
			}
		}
	}
	return total, workers
}

// criticalPath is the length, in steps, of the longest chain through the
// episode's causal DAG: each agent's steps in order plus the spawn and mail
// edges between agents. It is what a perfectly parallel team would still have
// to wait for. The bool is false when the recorded edges contained a cycle and
// only the longest single-agent chain could be used.
func criticalPath(ep *rl.Episode) (int, bool) {
	type key struct{ ai, si int }
	idx := map[key]int{}
	var nodes []key
	byStepID := map[string]int{}
	first := map[string]int{}
	last := map[string]int{}
	for ai := range ep.Agents {
		a := &ep.Agents[ai]
		for si := range a.Steps {
			if isFork(&a.Steps[si]) {
				continue
			}
			n := len(nodes)
			nodes = append(nodes, key{ai, si})
			idx[key{ai, si}] = n
			if _, ok := byStepID[a.Steps[si].ID]; !ok {
				byStepID[a.Steps[si].ID] = n
			}
			if _, ok := first[a.ID]; !ok {
				first[a.ID] = n
			}
			last[a.ID] = n
		}
	}
	if len(nodes) == 0 {
		return 0, true
	}
	succ := make([][]int, len(nodes))
	indeg := make([]int, len(nodes))
	link := func(from, to int) {
		if from == to {
			return
		}
		succ[from] = append(succ[from], to)
		indeg[to]++
	}
	// Sequential edges within an agent.
	prev := map[int]int{} // agent index -> previous node
	for n, k := range nodes {
		if p, ok := prev[k.ai]; ok {
			link(p, n)
		}
		prev[k.ai] = n
	}
	seqOnly := func() int {
		best, cur := 0, map[int]int{}
		for _, k := range nodes {
			cur[k.ai]++
			if cur[k.ai] > best {
				best = cur[k.ai]
			}
		}
		return best
	}
	resolve := func(id string, head bool) (int, bool) {
		if n, ok := byStepID[id]; ok {
			return n, true
		}
		m := last
		if head {
			m = first
		}
		n, ok := m[id]
		return n, ok
	}
	for _, e := range ep.Edges {
		if e.Kind != rl.EdgeSpawn && e.Kind != rl.EdgeMail {
			continue
		}
		f, ok1 := resolve(e.From, false)
		t, ok2 := resolve(e.To, true)
		if ok1 && ok2 {
			link(f, t)
		}
	}
	if d, ok := longestPath(succ, indeg); ok {
		return d, true
	}
	return seqOnly(), false
}

// longestPath returns the number of nodes on the longest path of a DAG, or false
// when the graph has a cycle. Kahn's algorithm: O(V+E).
func longestPath(succ [][]int, indeg []int) (int, bool) {
	n := len(succ)
	deg := append([]int(nil), indeg...)
	dist := make([]int, n)
	queue := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if deg[i] == 0 {
			dist[i] = 1
			queue = append(queue, i)
		}
	}
	seen, best := 0, 0
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		seen++
		if dist[u] > best {
			best = dist[u]
		}
		for _, v := range succ[u] {
			if dist[u]+1 > dist[v] {
				dist[v] = dist[u] + 1
			}
			deg[v]--
			if deg[v] == 0 {
				queue = append(queue, v)
			}
		}
	}
	return best, seen == n
}
