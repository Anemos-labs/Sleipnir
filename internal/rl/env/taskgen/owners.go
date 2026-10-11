package taskgen

import (
	"slices"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// FrontendRole is the role that owns user-interface paths in the team tasks the
// generators build (OwnUI).
const FrontendRole = "frontend"

// OwnUI gives a swarm task's user-interface work to the frontend role when the
// task's reference solution changes a UI file: files are the paths the change
// touches (Meta.Files), of which test files and test support (Classify) are not part
// of the solution and do not count. When one of the rest is UI work (rl.IsUIPath),
// team.owns["frontend"] becomes rl.UIGlobs(), so that no other role, the manager
// included, writes a UI file, and frontend joins team.roles when the task lists
// roles without it (an empty list already means every built-in role). It reports
// whether it changed the task; a task that is not a swarm team is left alone.
func OwnUI(t *rl.Task, files []string) bool {
	if t.Team.Mode != "swarm" || !touchesUI(files) {
		return false
	}
	owns := make(map[string][]string, len(t.Team.Owns)+1)
	for r, g := range t.Team.Owns {
		owns[r] = g
	}
	owns[FrontendRole] = rl.UIGlobs()
	t.Team.Owns = owns
	if len(t.Team.Roles) > 0 && !slices.Contains(t.Team.Roles, FrontendRole) {
		t.Team.Roles = append(append([]string(nil), t.Team.Roles...), FrontendRole)
	}
	return true
}

// touchesUI reports whether any file that is not a test or test support is UI work.
func touchesUI(files []string) bool {
	for _, f := range files {
		if _, kind := Classify(f); kind == Test || kind == TestSupport {
			continue
		}
		if rl.IsUIPath(f) {
			return true
		}
	}
	return false
}
