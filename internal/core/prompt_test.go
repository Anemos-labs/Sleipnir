package core_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
)

// TestWalkBlocksIsPrefixOrder: tools, then system blocks, then message blocks, each with
// the reference breakpoints use for it. Planning, drift detection and size accounting
// all rely on this being the one definition of "earlier".
func TestWalkBlocksIsPrefixOrder(t *testing.T) {
	type visit struct {
		ref  core.BlockRef
		what string
	}
	p := &core.Prompt{
		Tools:  []core.ToolSpec{{Name: "bash"}, {Name: "read"}},
		System: []core.Block{core.Text("s0"), core.Text("s1")},
		Messages: []core.Message{
			{Role: core.RoleUser, Blocks: []core.Block{core.Text("m0b0"), core.Text("m0b1")}},
			{Role: core.RoleAssistant},
			{Role: core.RoleUser, Blocks: []core.Block{core.Text("m2b0")}},
		},
	}
	var got []visit
	p.WalkBlocks(func(ref core.BlockRef, tool *core.ToolSpec, b *core.Block) {
		switch {
		case tool != nil && b == nil:
			got = append(got, visit{ref, "tool " + tool.Name})
		case b != nil && tool == nil:
			got = append(got, visit{ref, "block " + b.Text})
		default:
			t.Errorf("a visit must carry exactly one of tool and block: %v %v", tool, b)
		}
	})
	want := []visit{
		{core.BlockRef{Sys: true, Msg: -1, Blk: 0}, "tool bash"},
		{core.BlockRef{Sys: true, Msg: -1, Blk: 1}, "tool read"},
		{core.BlockRef{Sys: true, Msg: 0, Blk: 0}, "block s0"},
		{core.BlockRef{Sys: true, Msg: 0, Blk: 1}, "block s1"},
		{core.BlockRef{Msg: 0, Blk: 0}, "block m0b0"},
		{core.BlockRef{Msg: 0, Blk: 1}, "block m0b1"},
		{core.BlockRef{Msg: 2, Blk: 0}, "block m2b0"}, // a message without blocks is skipped, its index is not
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visited\n  %v\nwant\n  %v", got, want)
	}
}

// TestWalkBlocksHandsOutThePromptsOwnBlocks: the callback edits the prompt in place
// (planning marks blocks through the pointers), so they must not be copies.
func TestWalkBlocksHandsOutThePromptsOwnBlocks(t *testing.T) {
	p := &core.Prompt{
		Tools:    []core.ToolSpec{{Name: "bash"}},
		System:   []core.Block{core.Text("s")},
		Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("m")}}},
	}
	p.WalkBlocks(func(_ core.BlockRef, tool *core.ToolSpec, b *core.Block) {
		if tool != nil {
			tool.Description = "edited"
		}
		if b != nil {
			b.Text += "!"
		}
	})
	if p.Tools[0].Description != "edited" || p.System[0].Text != "s!" || p.Messages[0].Blocks[0].Text != "m!" {
		t.Fatalf("edits through the callback did not reach the prompt: %+v", p)
	}
}

func TestWalkBlocksEmptyPrompt(t *testing.T) {
	var p core.Prompt
	p.WalkBlocks(func(core.BlockRef, *core.ToolSpec, *core.Block) { t.Fatal("an empty prompt has nothing to visit") })
}

// TestWalkBlocksScales: a long prompt is visited once per block, in order, with no
// reference repeated.
func TestWalkBlocksScales(t *testing.T) {
	p := &core.Prompt{System: []core.Block{core.Text("s")}}
	for i := 0; i < 500; i++ {
		p.Messages = append(p.Messages, core.Message{Role: core.RoleUser, Blocks: []core.Block{
			core.Text(fmt.Sprint(i)), core.ToolUse("id", "n", json.RawMessage(`{}`)),
		}})
	}
	seen := map[core.BlockRef]bool{}
	n := 0
	p.WalkBlocks(func(ref core.BlockRef, _ *core.ToolSpec, _ *core.Block) {
		if seen[ref] {
			t.Fatalf("reference %+v visited twice", ref)
		}
		seen[ref] = true
		n++
	})
	if n != 1001 {
		t.Fatalf("visited %d blocks, want 1001", n)
	}
}
