package env

// closuresOf sums the closure counts (rl.Outcome.Closures: "done:verified", "failed:superseded",
// "handed_off", ...) of the completed results, or returns nil when the swarm board recorded none.
// Counts add up over rollouts; a rate would hide how many tasks the board closed in each way.
func closuresOf(results []RolloutResult) map[string]int {
	var sum map[string]int
	for _, r := range results {
		if r.Status != StatusOK {
			continue
		}
		for k, n := range r.Closures {
			if sum == nil {
				sum = map[string]int{}
			}
			sum[k] += n
		}
	}
	return sum
}

// cloneClosures copies an episode's closure counts into a rollout result, so the result does not
// alias the episode.
func cloneClosures(c map[string]int) map[string]int {
	if len(c) == 0 {
		return nil
	}
	out := make(map[string]int, len(c))
	for k, n := range c {
		out[k] = n
	}
	return out
}
