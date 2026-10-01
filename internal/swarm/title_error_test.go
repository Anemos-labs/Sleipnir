package swarm

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A manager that put the title in a field of another name (`task`, `description`) was told "task needs a title" three times; the
// message names the field.
func TestTaskWithoutATitleSaysWhichFieldToUse(t *testing.T) {
	bd := NewBoard(events.Discard{})
	_, err := bd.CreateTask("mgr", TaskSpec{})
	if err == nil || !strings.Contains(err.Error(), `"title" field`) {
		t.Errorf("error %v", err)
	}
}
