package state

import "testing"

// A log that counts a checkpoint's files (count, and the one path recorded last) instead of
// listing them reads the same as one that lists them.
func TestCheckpointEventsWithACount(t *testing.T) {
	b := newB()
	st := fold(t,
		b.Emit("", TypeCheckpoint, map[string]any{"id": "cp_0001", "label": "turn 1", "count": 0, "agents": []string{}}),
		b.Emit("", TypeCheckpoint, map[string]any{"id": "cp_0001", "label": "turn 1", "count": 3000, "file": "gen/f2999.txt", "agents": []string{"main"}}),
	)
	cs := st.Snapshot().Checkpoints
	if len(cs) != 1 || cs[0].Files != 3000 || len(cs[0].Agents) != 1 {
		t.Fatalf("checkpoints: %s", js(cs))
	}
}
