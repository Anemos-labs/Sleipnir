package translate

import (
	"os"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/testutil"
)

// The suite runs in UTC, so that the times of day the events carry (a checkpoint's ts, the resumed row's date) are the same on every
// machine, and ends by checking that no goroutine of the module is left: a translator that was closed has stopped its goroutine and
// its follower.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(testutil.CheckLeaks(m))
}
