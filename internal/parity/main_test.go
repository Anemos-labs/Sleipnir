package parity

import (
	"os"
	"testing"
	"time"
)

// The suite runs in UTC, so that the times of day the page's events carry (a checkpoint's ts) are the same on every machine: the
// page stream of the team recording is checked in and compared byte for byte.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}
