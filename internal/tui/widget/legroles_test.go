package widget_test

import (
	"slices"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// The horse's legs are the built-in roles in the swarm's order, the order the web page's legs and the web roster's Leg follow.
func TestLegRolesAreTheSwarmsRoleOrder(t *testing.T) {
	if !slices.Equal(widget.LegRoles, swarm.WorkerRoleOrder) {
		t.Errorf("LegRoles %v, swarm.WorkerRoleOrder %v", widget.LegRoles, swarm.WorkerRoleOrder)
	}
}
