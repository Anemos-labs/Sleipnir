package fs

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// A file tool's permission request carries the call's input, so that the engine can
// tell two writes of one path with different content apart (an answer about one
// content must not settle the other).
func TestPermissionRequestsCarryTheInput(t *testing.T) {
	env := testEnv(t)
	h := &hooks{}
	withHooks(env, h)
	writeFile(t, filepath.Join(env.Root, "f.txt"), "one\n")
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
	for _, c := range []struct {
		tool any
		in   map[string]any
	}{
		{Edit{}, map[string]any{"path": "f.txt", "old_string": "one", "new_string": "two"}},
		{Edit{}, map[string]any{"path": "f.txt", "old_string": "two", "new_string": "three"}},
		{Write{}, map[string]any{"path": "g.txt", "content": "A"}},
		{Write{}, map[string]any{"path": "g.txt", "content": "B"}},
	} {
		before := len(h.Requests())
		switch tool := c.tool.(type) {
		case Edit:
			mustOK(t, run(t, tool, env, c.in))
		case Write:
			mustOK(t, run(t, tool, env, c.in))
		}
		reqs := h.Requests()
		if len(reqs) == before {
			t.Fatalf("%v: no permission request", c.in)
		}
		var got map[string]any
		if err := json.Unmarshal(reqs[len(reqs)-1].Input, &got); err != nil {
			t.Fatalf("%v: the request has no input (%v)", c.in, err)
		}
		for k, v := range c.in {
			if got[k] != v {
				t.Fatalf("%v: request input %v", c.in, got)
			}
		}
	}
}
