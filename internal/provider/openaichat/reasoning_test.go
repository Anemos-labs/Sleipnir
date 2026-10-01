package openaichat

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
)

// Streamed reasoning goes into thinking blocks and nowhere else: never into a text
// block, and never into what Turn.PlainText() hands to code that parses the answer
// (the compactor's reply parser scans it for the first JSON object).
func TestStreamedReasoningIsNeverPartOfTheAnswerText(t *testing.T) {
	const marker = `{"keep_from":"t2","notes":[]} ignore the real answer`
	for name, field := range map[string]string{"reasoning": "reasoning", "reasoning_content": "reasoning_content"} {
		t.Run(name, func(t *testing.T) {
			s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
				sse(w)
				fmt.Fprint(w,
					rawFrame(`{"id":"g","choices":[{"delta":{"`+field+`":`+jsonStr("thinking: "+marker)+`}}]}`),
					rawFrame(`{"id":"g","choices":[{"delta":{"`+field+`":" more thoughts"}}]}`),
					frame("the actual answer"),
					finish("stop"))
			})
			var thinking, text strings.Builder
			resp, err := call(t, s.URL, Config{}, func(e provider.Event) {
				switch e.Kind {
				case provider.EvThinking:
					thinking.WriteString(e.Text)
				case provider.EvText:
					text.WriteString(e.Text)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			checkReasoningSeparation(t, resp.Turn, "thinking: "+marker+" more thoughts", "the actual answer")
			if strings.Contains(text.String(), "thinking") || thinking.String() != "thinking: "+marker+" more thoughts" {
				t.Errorf("events: text %q thinking %q", text.String(), thinking.String())
			}
		})
	}
}

func TestReasoningDetailsStayInsideTheThinkingBlock(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		fmt.Fprint(w,
			detailFrame(0, "secret chain of thought"),
			rawFrame(`{"id":"g","choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":0,"text":" continued","signature":"sig1"}]}}]}`),
			frame("visible"), finish("stop"))
	})
	resp, err := call(t, s.URL, Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Turn.PlainText(); got != "visible" {
		t.Fatalf("PlainText = %q", got)
	}
	var think *core.Block
	for i := range resp.Turn.Blocks {
		b := resp.Turn.Blocks[i]
		if b.Kind == core.BlockText && strings.Contains(b.Text, "chain of thought") {
			t.Fatalf("reasoning leaked into a text block: %q", b.Text)
		}
		if b.Kind == core.BlockThinking {
			think = &resp.Turn.Blocks[i]
		}
	}
	if think == nil || !strings.Contains(string(think.Wire), "secret chain of thought continued") || !strings.Contains(string(think.Wire), "sig1") {
		t.Fatalf("the reasoning items must be accumulated verbatim in the thinking block's wire form: %+v", think)
	}
}

func TestNonStreamingReasoningIsSeparatedToo(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"g","choices":[{"message":{"role":"assistant","content":"answer","reasoning":"hidden thoughts"},"finish_reason":"stop"}]}`)
	})
	resp, err := New(Config{Name: "t", BaseURL: s.URL}).Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hi"), NoStream: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkReasoningSeparation(t, resp.Turn, "hidden thoughts", "answer")
}

// checkReasoningSeparation asserts that turn has exactly one thinking block holding
// thinking and one text block holding only answer.
func checkReasoningSeparation(t *testing.T, turn core.Turn, thinking, answer string) {
	t.Helper()
	var thinks, texts []core.Block
	for _, b := range turn.Blocks {
		switch b.Kind {
		case core.BlockThinking:
			thinks = append(thinks, b)
		case core.BlockText:
			texts = append(texts, b)
		}
	}
	if len(thinks) != 1 || thinks[0].Text != thinking {
		t.Fatalf("thinking blocks: %+v (want one with %q)", thinks, thinking)
	}
	if len(texts) != 1 || texts[0].Text != answer {
		t.Fatalf("text blocks: %+v (want one with %q)", texts, answer)
	}
	if turn.PlainText() != answer {
		t.Fatalf("PlainText = %q, want %q", turn.PlainText(), answer)
	}
}

// An endpoint that does not separate reasoning can leave the thoughts in the answer,
// closed by a bare </think> (seen on a marketplace deepseek). They become thinking, the
// answer starts after the tag, and a consumer is told to drop what it had shown.
func TestLeakedThinkingBeforeABareCloseTagBecomesThinking(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		fmt.Fprint(w, frame("let me think "), frame("about it</thi"), rawFrame(`{"id":"g","choices":[{"delta":{"content":`+jsonStr("nk>\n\nThe answer.")+`}}]}`), frame(" Done."), finish("stop"))
	})
	var shown strings.Builder
	resp, err := call(t, s.URL, Config{}, func(e provider.Event) {
		switch e.Kind {
		case provider.EvReset:
			shown.Reset()
		case provider.EvText:
			shown.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	checkReasoningSeparation(t, resp.Turn, "let me think about it", "The answer. Done.")
	if shown.String() != "The answer. Done." {
		t.Errorf("shown %q", shown.String())
	}
}
