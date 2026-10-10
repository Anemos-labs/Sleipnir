package web

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Drain lets each stream deliver what it holds, the last event published before it included, then ends the stream (with ErrClosed,
// which the event stream writes nothing for) and closes the hub; a subscriber that arrives meanwhile is refused.
func TestDrainDeliversWhatAStreamHoldsThenEndsIt(t *testing.T) {
	h := NewHub(HubConfig{})
	st, err := h.Subscribe("tabs", SubscribeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := h.Publish("tabs", Event{Type: "ev", Data: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Publish("tabs", Event{Type: "bye", Data: json.RawMessage(`{"reason":"stopping"}`), Critical: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	drained := make(chan struct{})
	go func() {
		h.Drain(ctx)
		close(drained)
	}()
	var types []string
	for {
		ev, err := st.Next(ctx)
		if err != nil {
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("the stream ended with %v", err)
			}
			break
		}
		types = append(types, ev.Type)
	}
	if _, err := h.Subscribe("tabs", SubscribeOpts{}); !errors.Is(err, ErrClosed) {
		t.Errorf("a subscriber during the drain: %v", err)
	}
	st.Close()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not return once the stream ended")
	}
	if len(types) != 6 || types[5] != "bye" {
		t.Errorf("the stream delivered %v, want five events and bye", types)
	}
}

// A stream that does not take what it holds is closed when the drain's time is up.
func TestDrainClosesAStreamThatDoesNotRead(t *testing.T) {
	h := NewHub(HubConfig{})
	st, err := h.Subscribe("tabs", SubscribeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := h.Publish("tabs", Event{Type: "bye", Data: json.RawMessage(`{}`), Critical: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	h.Drain(ctx)
	if _, err := st.Next(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("after the drain's time: %v", err)
	}
}
