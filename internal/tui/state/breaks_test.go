package state

import "testing"

// The rule both interfaces say cache breaks by: the prompt's breaks always, the endpoint's own when material or asked for, three of
// those, then one line in place of the rest.
func TestBreakNotices(t *testing.T) {
	cheap := Anomaly{Kind: "low_hit", MissUSD: 0.002, MissKnown: true}
	dear := Anomaly{Kind: "low_hit", MissUSD: 0.05, MissKnown: true}
	unknown := Anomaly{Kind: "low_hit"}
	layer := Anomaly{Kind: "low_hit", Layer: "notes"}
	drift := Anomaly{Kind: "drift"}
	var b BreakNotices
	for _, a := range []Anomaly{cheap, unknown, layer, drift} {
		show, capped := b.Say(a, false)
		if want := !a.EndpointOnly(); show != want || capped {
			t.Errorf("%+v: show %v capped %v", a, show, capped)
		}
	}
	for i, want := range []struct{ show, capped bool }{{true, false}, {true, false}, {true, false}, {false, true}, {false, false}} {
		if show, capped := b.Say(dear, false); show != want.show || capped != want.capped {
			t.Errorf("material break %d: show %v capped %v", i+1, show, capped)
		}
	}
	if show, _ := b.Say(drift, false); !show {
		t.Error("a drift after the cap is still said")
	}
	var v BreakNotices
	if show, _ := v.Say(cheap, true); !show {
		t.Error("with every notice asked for, a cheap miss is said")
	}
}
