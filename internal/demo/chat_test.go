package demo

import (
	"encoding/json"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/provider/mock"
)

// The chat scenario is what the recording of the chat is a session of (cmd/sleipnir/chat_record.go runs it). What a recording shows of
// the project is its code, so the code is held to what the recording says of it: it is Go, it is formatted, and the bug is the one the
// model fixes.

func TestChatProjectIsFormattedGo(t *testing.T) {
	files := ChatFiles()
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		src := []byte(files[name])
		if _, err := parser.ParseFile(token.NewFileSet(), name, src, parser.AllErrors); err != nil {
			t.Errorf("%s does not parse: %v", name, err)
			continue
		}
		if got, err := format.Source(src); err != nil || string(got) != string(src) {
			t.Errorf("%s is not what gofmt makes of it (%v): a recording shows this code", name, err)
		}
	}
	for _, want := range []string{"go.mod", "README.md", "AGENTS.md", "cmd/orders-api/main.go", "internal/httpapi/handler.go", "orders/list.go", "orders/list_test.go"} {
		if _, ok := files[want]; !ok {
			t.Errorf("the project has no %s", want)
		}
	}
}

func reply(t *testing.T, c *ChatScenario, user string) mock.Reply {
	t.Helper()
	return c.respond(&mock.Call{Messages: []mock.Msg{{Role: "user", Content: user}}})
}

// The model of the scenario edits the file with the call a real model would make, and the edit is one the edit tool accepts: the text it
// replaces is in the file once, and what it puts there is not.
func TestChatScriptEditsTheBuggyLineAndOnlyThat(t *testing.T) {
	c := NewChatScenario()
	var calls []mock.ToolCall
	for range 7 {
		calls = append(calls, reply(t, c, "go on").ToolCalls...)
	}
	var names []string
	for _, tc := range calls {
		names = append(names, tc.Name)
	}
	if got, want := strings.Join(names, " "), "read bash read edit bash grep"; got != want {
		t.Fatalf("the model calls %s, want %s", got, want)
	}
	var edit struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	for _, tc := range calls {
		if tc.Name == "edit" {
			if err := json.Unmarshal([]byte(tc.Args), &edit); err != nil {
				t.Fatalf("%v: %s", err, tc.Args)
			}
		}
	}
	src := ChatFiles()[edit.Path]
	if src == "" {
		t.Fatalf("the edit is to %q, which the project does not have", edit.Path)
	}
	if n := strings.Count(src, edit.OldString); n != 1 {
		t.Fatalf("the text the edit replaces is in %s %d times, want once", edit.Path, n)
	}
	fixed := strings.Replace(src, edit.OldString, edit.NewString, 1)
	if strings.Contains(fixed, edit.OldString) || !strings.Contains(fixed, "(page - 1) * size") {
		t.Errorf("the edit does not fix the offset:\n%s", edit.NewString)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), edit.Path, fixed, parser.AllErrors); err != nil {
		t.Errorf("the file after the edit does not parse: %v", err)
	}
}

// The answer to the first goal has what the recording shows of markdown, and the answer to the second is long enough that the person
// has time to press Ctrl-C however fast the machine is: the endpoint goes quiet after ChatInterruptAfter pieces of it.
func TestChatScriptAnswersInMarkdownAndThenAtLength(t *testing.T) {
	c := NewChatScenario()
	var final, long mock.Reply
	for i := 1; i <= 8; i++ {
		r := reply(t, c, "go on")
		switch i {
		case 7:
			final = r
		case 8:
			long = r
		}
	}
	if len(final.ToolCalls) != 0 {
		t.Errorf("the answer of the seventh step has %d calls: it ends the turn", len(final.ToolCalls))
	}
	for _, want := range []string{"## ", "\n- ", "```go\n", "`"} {
		if !strings.Contains(final.Text, want) {
			t.Errorf("the final answer has no %q: it is the markdown the recording shows\n%s", want, final.Text)
		}
	}
	if len(long.ToolCalls) != 0 {
		t.Errorf("the second answer has calls: %+v", long.ToolCalls)
	}
	const piece = 24 // runes of text the endpoint sends at a time
	if n := len([]rune(long.Text)) / piece; n < ChatInterruptAfter+5 {
		t.Errorf("the second answer is %d pieces long and the person presses Ctrl-C after %d of them: the answer must outlast them by a good margin", n, ChatInterruptAfter)
	}
	if ChatDropBefore < 2 || ChatDropBefore > 7 {
		t.Errorf("the cache is dropped before request %d, and the first goal takes seven", ChatDropBefore)
	}
}
