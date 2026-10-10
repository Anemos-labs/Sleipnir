package web

import (
	"os"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/testutil"
)

// The hub and the server start goroutines (every stream is one); a test that leaves one behind fails the package.
func TestMain(m *testing.M) { os.Exit(testutil.CheckLeaks(m)) }
