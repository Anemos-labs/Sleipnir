package main

import (
	"context"
	"strings"
	"testing"
)

func TestEffortCommandShowsChangesAndResetsWithoutRestart(t *testing.T) {
	s := chatSession(t, false, nil)
	h := &sessionHost{s: s}
	for _, tc := range []struct{ command, want string }{
		{"/effort", "effort: default"},
		{"/effort MAX", "effort: max"},
		{"/effort medium", "effort: medium"},
		{"/effort unrecognized", "effort: default"},
		{"/effort low", "effort: low"},
		{"/effort default", "effort: default"},
	} {
		var out strings.Builder
		res := h.Command(context.Background(), tc.command, &out)
		if !strings.Contains(out.String(), tc.want) || res.Send != "" || len(res.Restart) > 0 || res.Quit {
			t.Fatalf("%s: output=%q result=%+v", tc.command, out.String(), res)
		}
	}
}
