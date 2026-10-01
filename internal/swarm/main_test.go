package swarm_test

import (
	"os"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/testutil"
)

// The suite ends by checking that no goroutine of the module is left running: a swarm that
// was closed has stopped its agents, its mailman and everything they started.
func TestMain(m *testing.M) { os.Exit(testutil.CheckLeaks(m)) }
