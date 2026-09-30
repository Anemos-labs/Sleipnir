// Package core holds the provider-neutral vocabulary shared by every other
// Sleipnir package: messages, blocks, tools, usage and ids.
//
// The one rule that shapes this package: anything a provider hands us that we
// may later have to send back (thinking blocks and their signatures, encrypted
// reasoning items, compaction blocks) is kept byte-for-byte in Block.Wire.
// Rebuilding provider payloads from our own domain objects is the classic way
// to silently break prompt caches and preserved-thinking checks, so replay
// always prefers Wire over reconstruction.
package core

import (
	"encoding/json"
	"strings"
	"time"
)

// Role is the author of a turn.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleSystem is a mid-conversation operator message. Only some providers
	// accept it; adapters without support fold it into a user text block.
	RoleSystem Role = "system"
)

// BlockKind discriminates the payload of a Block.
type BlockKind string

const (
	BlockText             BlockKind = "text"
	BlockThinking         BlockKind = "thinking"
	BlockRedactedThinking BlockKind = "redacted_thinking"
	BlockToolUse          BlockKind = "tool_use"
	BlockToolResult       BlockKind = "tool_result"
	BlockImage            BlockKind = "image"
	BlockCompaction       BlockKind = "compaction"
)

// Block is one content element of a turn.
type Block struct {
	Kind BlockKind `json:"kind"`

	// Text is set for text blocks, and for thinking blocks when the provider
	// returned readable reasoning.
	Text string `json:"text,omitempty"`

	// Tool use.
	ToolID   string          `json:"tool_id,omitempty"`
	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`

	// Tool result. ToolID links back to the tool_use it answers.
	Result  []Block `json:"result,omitempty"`
	IsError bool    `json:"is_error,omitempty"`

	// Media (images) live in the blob store; MediaType is the MIME type.
	MediaType string `json:"media_type,omitempty"`
	MediaRef  string `json:"media_ref,omitempty"`

	// Wire is the provider-native JSON exactly as received. WireFormat names
	// the dialect ("anthropic", "openai-responses", "openai-chat") so a block is
	// only replayed verbatim to a provider that speaks that dialect.
	Wire       json.RawMessage `json:"wire,omitempty"`
	WireFormat string          `json:"wire_format,omitempty"`

	// Invalid explains why a tool_use's Input is not the JSON the model meant
	// (truncated by max_tokens, malformed arguments). The dispatcher reports it
	// back to the model instead of running the tool.
	Invalid string `json:"invalid,omitempty"`

	// Ephemeral blocks are rendered into one request and never persisted in the
	// transcript (the "hot" tail layer). Adapters use this to pick a
	// turn-scoped rendering where the provider offers one.
	Ephemeral bool `json:"ephemeral,omitempty"`
}

// Text returns a text block.
func Text(s string) Block { return Block{Kind: BlockText, Text: s} }

// ToolUse returns a tool_use block. Input must already be valid JSON.
func ToolUse(id, name string, input json.RawMessage) Block {
	return Block{Kind: BlockToolUse, ToolID: id, ToolName: name, Input: input}
}

// ToolResult returns a tool_result block answering tool call id.
func ToolResult(id string, isErr bool, content ...Block) Block {
	return Block{Kind: BlockToolResult, ToolID: id, IsError: isErr, Result: content}
}

// PlainText concatenates the text of a block's textual content. Tool results
// are flattened; non-textual blocks contribute nothing.
func (b Block) PlainText() string {
	switch b.Kind {
	case BlockText, BlockThinking:
		return b.Text
	case BlockToolResult:
		var sb strings.Builder
		for _, c := range b.Result {
			sb.WriteString(c.PlainText())
		}
		return sb.String()
	}
	return ""
}

// TurnID numbers the turns of one agent's thread. It never repeats within an
// agent, even after compaction removes turns, so one-line summaries and recall
// handles can refer to turns durably ("t41-t58").
type TurnID int64

// Origin records why a turn exists. It is bookkeeping for logs, training data
// and the compactor; it is never rendered to the model.
type Origin string

const (
	OriginUser   Origin = "user"   // typed by a human
	OriginModel  Origin = "model"  // produced by the LLM
	OriginTool   Origin = "tool"   // tool results returned to the model
	OriginMail   Origin = "mail"   // delivered mailbox message
	OriginSystem Origin = "system" // harness-generated notice
	OriginDigest Origin = "digest" // synthesized by compaction
	// OriginTask is work the harness hands to an agent: a swarm's kickoff or a reused
	// worker's next assignment. It says what to do, on the harness's authority, and it
	// is not the user's word: compaction never pins it as an instruction. The task
	// text is written from a board that models fill in, so it is data with a job, not
	// a person's instruction.
	OriginTask Origin = "task"
)

// Turn is one message in an agent's thread.
type Turn struct {
	ID     TurnID  `json:"id"`
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`

	Origin Origin    `json:"origin,omitempty"`
	At     time.Time `json:"at,omitempty"`

	// Assistant turns carry the usage of the request that produced them so the
	// compactor and cost reports can attribute spend to individual steps.
	Model string `json:"model,omitempty"`
	Usage *Usage `json:"usage,omitempty"`
}

// ToolCalls returns the tool_use blocks of a turn in order.
func (t Turn) ToolCalls() []Block {
	var out []Block
	for _, b := range t.Blocks {
		if b.Kind == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}

// PlainText concatenates the textual content of the turn: what the model said
// and what tools returned. Reasoning is not included: it is neither an answer nor
// safe to parse as one (a model may quote hostile text inside its reasoning, and
// a parser that scans for the first JSON object would take it), so callers that
// want it read the thinking blocks themselves.
func (t Turn) PlainText() string {
	var sb strings.Builder
	for _, b := range t.Blocks {
		if b.Kind == BlockThinking {
			continue
		}
		sb.WriteString(b.PlainText())
	}
	return sb.String()
}

// ToolSpec describes a tool offered to the model.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Strict      bool            `json:"strict,omitempty"`

	// Scheduling hints for the harness; never rendered to the model.
	ReadOnly bool `json:"-"`
}

// StopReason normalises provider stop reasons.
type StopReason string

const (
	StopEnd       StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopPause     StopReason = "pause_turn"
	StopRefusal   StopReason = "refusal"
	StopOther     StopReason = "other"
)
