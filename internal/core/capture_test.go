package core

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

type memBlobs map[Hash][]byte

func (m memBlobs) put(b []byte) (Hash, error) {
	h := HashBytes(b)
	m[h] = append([]byte(nil), b...)
	return h, nil
}
func (m memBlobs) get(h Hash) ([]byte, error) {
	b, ok := m[h]
	if !ok {
		return nil, fmt.Errorf("missing")
	}
	return b, nil
}

func testPrompt(n int) *Prompt {
	p := &Prompt{
		Model:    "m",
		Tools:    []ToolSpec{{Name: "read", Description: "read <a & b>", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}},
		System:   []Block{Text("constitution")},
		Messages: []Message{{Role: RoleUser, Blocks: []Block{Text("pins <shared> & notes")}}},
	}
	for i := 0; i < n; i++ {
		p.Messages = append(p.Messages,
			Message{Role: RoleAssistant, Turn: TurnID(2*i + 1), Blocks: []Block{
				{Kind: BlockThinking, Text: "hm", Wire: json.RawMessage(`{ "sig" : "a b" }`), WireFormat: "x"},
				ToolUse(fmt.Sprintf("c%d", i), "read", json.RawMessage(`{"path":"a<b>.go"}`))}},
			Message{Role: RoleUser, Turn: TurnID(2*i + 2), Blocks: []Block{ToolResult(fmt.Sprintf("c%d", i), false, Text("é😀 ok"))}},
		)
	}
	return p
}

func TestManifestRoundTripAndDelta(t *testing.T) {
	store := memBlobs{}
	var st ManifestState
	states := map[string][]Hash{}
	var manifests []Manifest
	for i := 1; i <= 4; i++ {
		req := fmt.Sprintf("a.%d", i)
		m, next, err := BuildManifest(testPrompt(i), st, req, store.put)
		if err != nil {
			t.Fatal(err)
		}
		if i > 1 && (m.Base != fmt.Sprintf("a.%d", i-1) || len(m.Add) != 2) {
			t.Fatalf("request %d should extend the previous one by two messages: base=%q add=%d", i, m.Base, len(m.Add))
		}
		states[req] = next.Msgs
		manifests = append(manifests, m)
		st = next
	}
	// Expansion reproduces the model-visible prompt exactly, including raw wire
	// blocks and HTML-sensitive text.
	for i, m := range manifests {
		var base []Hash
		if m.Base != "" {
			base = states[m.Base]
		}
		got, _, err := m.Expand(base, store.get)
		if err != nil {
			t.Fatalf("expand %d: %v", i, err)
		}
		want := testPrompt(i + 1)
		for j := range want.Messages {
			want.Messages[j].Turn = 0
		}
		gb, _ := MarshalStable(got)
		wb, _ := MarshalStable(want)
		if string(gb) != string(wb) {
			t.Fatalf("request %d expanded differently:\n%s\n%s", i, gb, wb)
		}
	}
	// A rebase (changed early message) restarts the delta at the change.
	changed := testPrompt(4)
	changed.Messages[2].Blocks[0].Text = "masked"
	m, _, err := BuildManifest(changed, st, "a.5", store.put)
	if err != nil {
		t.Fatal(err)
	}
	if m.Keep != 2 || len(m.Add) != len(changed.Messages)-2 {
		t.Fatalf("delta after a rebase: keep=%d add=%d", m.Keep, len(m.Add))
	}
}

func TestManifestDetectsTampering(t *testing.T) {
	store := memBlobs{}
	m, st, _ := BuildManifest(testPrompt(2), ManifestState{}, "a.1", store.put)
	m.Add[0] = HashString("something else")
	if _, _, err := m.Expand(nil, store.get); err == nil {
		t.Fatal("a manifest whose parts do not match its wire hash must not expand")
	}
	m2, _, _ := BuildManifest(testPrompt(2), ManifestState{}, "a.1", store.put)
	for h := range store { // corrupt one blob
		store[h] = append(store[h], ' ')
		break
	}
	if _, _, err := m2.Expand(nil, store.get); err == nil {
		t.Fatal("a corrupt blob must be detected")
	}
	_ = st
}

func TestWireHashIgnoresRequestMechanics(t *testing.T) {
	a, b := testPrompt(2), testPrompt(2)
	b.CacheKey, b.Params.MaxTokens = "k", 99
	b.Breakpoints = []Breakpoint{{After: BlockRef{Msg: 0}}}
	store := memBlobs{}
	ma, _, _ := BuildManifest(a, ManifestState{}, "x", store.put)
	mb, _, _ := BuildManifest(b, ManifestState{}, "x", store.put)
	if ma.Wire != mb.Wire {
		t.Fatal("cache keys, breakpoints and params are not part of what the model sees")
	}
	c := testPrompt(2)
	c.Messages[1].Blocks[1].Input = json.RawMessage(`{"path":"other"}`)
	mc, _, _ := BuildManifest(c, ManifestState{}, "x", store.put)
	if mc.Wire == ma.Wire {
		t.Fatal("changed content must change the wire hash")
	}
}

func TestTokenTraceConsistency(t *testing.T) {
	ok := &TokenTrace{CompletionIDs: []int32{1, 2}, Logprobs: []float32{-1, -2}}
	if !ok.Consistent() {
		t.Fatal("aligned trace should be consistent")
	}
	if (&TokenTrace{CompletionIDs: []int32{1, 2}, Logprobs: []float32{-1}}).Consistent() {
		t.Fatal("misaligned logprobs must be inconsistent")
	}
	if (*TokenTrace)(nil).Consistent() || (&TokenTrace{}).Consistent() {
		t.Fatal("empty traces are not usable")
	}
	_ = reflect.DeepEqual
}
