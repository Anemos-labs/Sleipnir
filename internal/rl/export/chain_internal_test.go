package export

import (
	"encoding/json"
	"testing"
)

// TestSameTurnIgnoresReasoning compares a completion rendered with its reasoning
// against the same turn as a later prompt replays it (without reasoning), as a
// recorded swarm session had them: the turn must be the same, while a turn with
// another call or another id must not.
func TestSameTurnIgnoresReasoning(t *testing.T) {
	replayed := json.RawMessage(`{"role":"assistant","content":null,"tool_calls":[{"function":{"arguments":"{\"action\": \"get\", \"id\": \"T2\"}","name":"task"},"id":"chatcmpl-tool-b03","type":"function"}]}`)
	completion := json.RawMessage(`{"role":"assistant","content":null,"tool_calls":[{"function":{"arguments":"{\"action\": \"get\", \"id\": \"T2\"}","name":"task"},"id":"chatcmpl-tool-b03","type":"function"}],"reasoning_details":[{"type":"reasoning.text","text":"Let me read the task board.","index":0}]}`)
	if !sameTurn(replayed, completion) || !sameTurn(completion, replayed) {
		t.Fatal("a completion's reasoning kept it from matching the turn the prompt replays")
	}
	if sameJSON(replayed, completion) {
		t.Fatal("the fixture must differ byte for byte")
	}
	other := json.RawMessage(`{"role":"assistant","content":null,"tool_calls":[{"function":{"arguments":"{\"action\": \"get\", \"id\": \"T2\"}","name":"task"},"id":"chatcmpl-tool-907","type":"function"}]}`)
	if sameTurn(other, completion) {
		t.Fatal("another call with the same arguments is another turn")
	}
	content := json.RawMessage(`{"role":"assistant","content":"done","reasoning_content":"because"}`)
	if !sameTurn(content, json.RawMessage(`{"role":"assistant","content":"done"}`)) || sameTurn(content, json.RawMessage(`{"role":"assistant","content":"done!"}`)) {
		t.Fatal("content decides when there are no tool calls")
	}
}
