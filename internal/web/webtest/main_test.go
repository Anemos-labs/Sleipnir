package webtest

import (
	"os"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/testutil"
)

// The fake server and the player start goroutines; a test that leaves one behind fails the package.
func TestMain(m *testing.M) { os.Exit(testutil.CheckLeaks(m)) }
