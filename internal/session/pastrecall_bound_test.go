package session

import (
	"errors"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Recall bounds what a handle reads back; reading an earlier session's blob through readThrough keeps that bound.
func TestReadThroughKeepsTheSizeLimitOfAnEarlierSession(t *testing.T) {
	past := events.NewMemBlobs()
	h, err := past.Put(make([]byte, 64))
	if err != nil {
		t.Fatal(err)
	}
	r := readThrough{mem: events.NewMemBlobs(), past: past}
	if _, err := r.GetMax(h, 16); !errors.Is(err, events.ErrBlobTooLarge) {
		t.Errorf("GetMax of a 64-byte blob with limit 16: %v; want ErrBlobTooLarge", err)
	}
	if b, err := r.GetMax(h, 64); err != nil || len(b) != 64 {
		t.Errorf("GetMax within the limit: %d bytes, %v", len(b), err)
	}
}
