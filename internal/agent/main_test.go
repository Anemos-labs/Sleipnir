package agent_test

import (
	"os"
	"testing"

	"github.com/reee344/sleipnir/internal/testutil"
)

// The suite ends by checking that no goroutine of the module is left running: an agent that
// was closed has stopped everything it started.
func TestMain(m *testing.M) { os.Exit(testutil.CheckLeaks(m)) }
