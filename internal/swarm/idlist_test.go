package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestIdListTakesAListOrOneStringOfIds(t *testing.T) {
	for in, want := range map[string]string{
		`"T3"`: "T3", `"T3, T4"`: "T3 T4", `["T3","T4"]`: "T3 T4", `["T3, T4"]`: "T3 T4", `[]`: "",
	} {
		var l idList
		if err := json.Unmarshal([]byte(in), &l); err != nil || strings.Join(l, " ") != want {
			t.Errorf("idList from %s = %q, %v; want %q", in, l, err, want)
		}
	}
}

// An explicit empty list stays a list: task revise reads a nil list as "leave the dependencies as they are".
func TestAnEmptyIdListIsNotNil(t *testing.T) {
	var l idList
	if err := json.Unmarshal([]byte(`[]`), &l); err != nil || l == nil {
		t.Fatalf("idList from [] = %#v, %v; want an empty, non-nil list", l, err)
	}
}

// Through task revise: deps=[] clears a task's dependencies.
func TestTaskReviseWithAnEmptyDepsListClearsThem(t *testing.T) {
	r := newRVRig(t, Config{MaxWorkers: 2}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} })
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{
		{ID: "T1", Title: "api", Role: "backend", Status: StatusTodo},
		{ID: "T2", Title: "page", Role: "backend", Status: StatusTodo, Deps: []string{"T1"}},
	}})
	if res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": "T2", "deps": []string{}, "text": "T2 no longer needs T1"}); res.IsError {
		t.Fatalf("revise: %s", res.Text)
	}
	if got, _ := r.sw.Board.Snapshot().Task("T2"); len(got.Deps) != 0 {
		t.Fatalf("T2's deps after revise deps=[]: %v", got.Deps)
	}
}
