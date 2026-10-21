package session

import (
	"path/filepath"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/research"
)

// A research-first goal names its brief where every agent can read it: relative to the session's directory for an agent working in the
// checkout, by its absolute path in the checkout for a team in trees of its own (the trees are made before the brief is written).
func TestBriefSpecNamesAPathTheAgentsCanRead(t *testing.T) {
	root := filepath.FromSlash("/work/app")
	r := &research.Run{Slug: "booking-pages"}
	for _, c := range []struct {
		name string
		cwd  string
		iso  *isoPlan
		want string
	}{
		{"checkout", root, nil, ".sleipnir/research/booking-pages/BRIEF.md"},
		{"subdirectory", filepath.Join(root, "web"), nil, "/work/app/.sleipnir/research/booking-pages/BRIEF.md"},
		{"trees", root, &isoPlan{}, "/work/app/.sleipnir/research/booking-pages/BRIEF.md"},
	} {
		s := &Session{opts: Options{Root: root, Cwd: c.cwd}, iso: c.iso}
		if got := s.briefSpec(r); got != filepath.ToSlash(c.want) {
			t.Errorf("%s: brief named %q, want %q", c.name, got, c.want)
		}
	}
}
