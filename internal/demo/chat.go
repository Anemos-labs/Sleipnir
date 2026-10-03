package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

// The chat scenario is the session behind the chat recording of the README (docs/media/chat.svg): one person at the chat program, a
// scripted model, and everything between them real. The person asks for a failing test to be fixed; the model reads the code, runs the
// tests with the real go command, reads the test, edits the file (the person is asked, presses y, which answers nothing, and then 1),
// runs the tests again and looks for other callers; then the person asks for one more thing, and presses Ctrl-C before the model has
// answered. Two things happen to the cache that the chat exists to show: the thread is folded into a resume on the way (the compaction
// limit is set low in the user's configuration, so that it happens in a minute and not in an hour), and the provider drops its cache
// once, before the sixth request, which the harness notices and says what it cost.
//
// This file is the model and the project; cmd/sleipnir/chat_record.go is the person, the real session and the recorder. Like the other
// scripts of this package it is the model and nothing else: it says what a model would say to this project, and the tools, the
// permission engine, the cache planner and the accounting do what they do with it.

// The two things the person asks for.
const (
	ChatGoal1 = "the pagination test in ./orders is failing, fix it"
	ChatGoal2 = "yes, add a test for page 0 and a negative size"
)

// ChatDropBefore is the main request that finds the provider's cache gone: the endpoint drops it just before that request arrives.
const ChatDropBefore = 6

// ChatSessionID is the id the recorded session is given: an id of the form the harness makes (a date, a time and six hex digits), but
// fixed, so that the footer of the recording does not depend on the day it was made.
const ChatSessionID = "20260102-030405-5eed01"

// ChatEpoch is the time of the start of the recorded session, which its clock and its log go by.
var ChatEpoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// ChatFiles are the files of the project the person works in, by path.
func ChatFiles() map[string]string {
	return map[string]string{
		"go.mod":    "module example.com/orders-api\n\ngo 1.21\n",
		"README.md": "# orders-api\n\nA small HTTP service that lists orders, a page at a time.\n\nRun the tests with `go test ./...`.\n",
		"AGENTS.md": chatAgents,
		"cmd/orders-api/main.go": `// Command orders-api serves the orders on :8080.
package main

import (
	"log"
	"net/http"

	"example.com/orders-api/internal/httpapi"
	"example.com/orders-api/orders"
)

func main() {
	store := orders.NewStore(nil)
	http.Handle("/orders", httpapi.Handler{Store: store})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
`,
		"internal/httpapi/handler.go": chatHandler,
		"orders/list.go":              chatList,
		"orders/list_test.go":         chatListTest,
	}
}

const chatAgents = `# Conventions

- Go 1.21 and the standard library only.
- Pages count from 1, and size is the number of orders on a page.
- Money is integer cents, never a float.
- Tests sit next to the code and run with ` + "`go test ./...`" + `: run them before you say a change is done.
- Keep functions short, and give every exported name a doc comment.
`

const chatHandler = `// Package httpapi serves the orders over HTTP.
package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"example.com/orders-api/orders"
)

// Handler serves GET /orders?page=N&size=M.
type Handler struct {
	Store *orders.Store
}

// ServeHTTP lists one page of orders as JSON. The page counts from 1 and defaults to 1; the size defaults to 20.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	page := intParam(r, "page", 1)
	size := intParam(r, "size", 20)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"page":   page,
		"pages":  h.Store.Pages(size),
		"orders": h.Store.List(page, size),
	})
}

func intParam(r *http.Request, name string, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	return n
}
`

// chatList is the file with the bug: List takes its offset from the page number, and pages count from 1.
const chatList = `// Package orders lists orders a page at a time.
package orders

import (
	"errors"
	"sort"
)

// ErrNotFound is returned by Get when no order has the id.
var ErrNotFound = errors.New("orders: no such order")

// Order is one row of the orders table.
type Order struct {
	ID       int
	Customer string
	Total    int // cents
	Paid     bool
}

// Store holds the orders in id order.
type Store struct {
	rows []Order
}

// NewStore returns a store that holds rows, sorted by id.
func NewStore(rows []Order) *Store {
	sorted := append([]Order(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	return &Store{rows: sorted}
}

// Len is the number of orders in the store.
func (s *Store) Len() int { return len(s.rows) }

// Get returns the order with the id, or ErrNotFound.
func (s *Store) Get(id int) (Order, error) {
	i := sort.Search(len(s.rows), func(i int) bool { return s.rows[i].ID >= id })
	if i == len(s.rows) || s.rows[i].ID != id {
		return Order{}, ErrNotFound
	}
	return s.rows[i], nil
}

// Pages is how many pages of size orders the store has.
func (s *Store) Pages(size int) int {
	if size < 1 {
		return 0
	}
	return (len(s.rows) + size - 1) / size
}

// List returns the orders on one page. Pages count from 1; a page past the
// end, or a page or size below 1, is empty.
func (s *Store) List(page, size int) []Order {
	if page < 1 || size < 1 {
		return nil
	}
	offset := page * size
	if offset >= len(s.rows) {
		return nil
	}
	end := offset + size
	if end > len(s.rows) {
		end = len(s.rows)
	}
	return s.rows[offset:end]
}

// Unpaid returns the orders that have not been paid, in id order.
func (s *Store) Unpaid() []Order {
	var out []Order
	for _, o := range s.rows {
		if !o.Paid {
			out = append(out, o)
		}
	}
	return out
}

// Revenue is the sum of the paid orders, in cents.
func (s *Store) Revenue() int {
	total := 0
	for _, o := range s.rows {
		if o.Paid {
			total += o.Total
		}
	}
	return total
}
`

const chatListTest = `package orders

import "testing"

var sample = []Order{
	{1, "ada", 1200, true}, {2, "grace", 250, false}, {3, "edsger", 7500, true},
	{4, "barbara", 900, true}, {5, "donald", 4000, false},
}

func ids(os []Order) []int {
	var out []int
	for _, o := range os {
		out = append(out, o.ID)
	}
	return out
}

func TestGet(t *testing.T) {
	s := NewStore(sample)
	o, err := s.Get(3)
	if err != nil || o.Customer != "edsger" {
		t.Fatalf("Get(3) = %v, %v", o, err)
	}
	if _, err := s.Get(9); err != ErrNotFound {
		t.Fatalf("Get(9) error = %v, want ErrNotFound", err)
	}
}

func TestListFirstPage(t *testing.T) {
	got := NewStore(sample).List(1, 2)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Errorf("List(1, 2) holds orders %v, want 1 and 2", ids(got))
	}
}

func TestListLastPage(t *testing.T) {
	got := NewStore(sample).List(3, 2)
	if len(got) != 1 || got[0].ID != 5 {
		t.Errorf("List(3, 2) holds orders %v, want 5", ids(got))
	}
}

func TestPages(t *testing.T) {
	if n := NewStore(sample).Pages(2); n != 3 {
		t.Errorf("Pages(2) = %d, want 3", n)
	}
}

func TestRevenue(t *testing.T) {
	if got := NewStore(sample).Revenue(); got != 9600 {
		t.Errorf("Revenue() = %d, want 9600", got)
	}
}
`

// ChatInterruptAfter is how many pieces of the second answer the person sees before they press Ctrl-C: the endpoint goes quiet after
// that many, which is what makes the Ctrl-C the only way the turn can end, however fast or slow the machine is.
const ChatInterruptAfter = 20

// ChatScenario is the model of the recorded chat and the endpoint it is served from. The endpoint has no latency of its own: how long
// a model of this speed would take is the recorder's to say (cmd/sleipnir/chat_pace.go), so that a recording does not depend on how
// busy the machine that made it was.
type ChatScenario struct {
	mu    sync.Mutex
	step  int // main requests answered so far
	nmain int // main requests that have arrived (the endpoint counts them to drop its cache before the right one)
	srv   *mock.Server
}

// NewChatScenario makes the scenario.
func NewChatScenario() *ChatScenario { return &ChatScenario{} }

// Start serves the model on a local endpoint and returns the base URL a provider is configured with (OpenAI-style, ending in /v1) and
// the function that stops it. The endpoint is the repository's mock, with its automatic prefix cache: the cache hits the harness sees
// are the ones that cache gives. Twice it does something that a person's endpoint does: it drops its cache just before the request
// it is told to (ChatDropBefore), as a provider that restarted would, and it goes quiet in the middle of the answer to the second
// goal (ChatInterruptAfter), as a model that takes its time does.
func (c *ChatScenario) Start() (url string, stop func()) {
	c.srv = mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, c.respond)
	inner := c.srv.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if !bytes.Contains(body, []byte("<compactor-task>")) {
				c.mu.Lock()
				c.nmain++
				drop := c.nmain == ChatDropBefore
				c.mu.Unlock()
				if drop {
					c.srv.FlushCaches()
				}
				if bytes.Contains(body, []byte(ChatGoal2)) {
					w = &quietAfter{ResponseWriter: w, ctx: r.Context(), left: 1 + ChatInterruptAfter}
				}
			}
		}
		inner.ServeHTTP(w, r)
	}))
	return ts.URL + "/v1", ts.Close
}

// quietAfter is a response that stops after some pieces: the pieces the mock has flushed reach the client, and the next flush waits
// until the client has gone away (the request's context is done), so the answer never ends of its own accord.
type quietAfter struct {
	http.ResponseWriter
	ctx  context.Context
	left int // flushes that still reach the client
}

// Flush forwards a bounded number of HTTP flushes, then waits for cancellation to simulate a
// stalled response.
func (q *quietAfter) Flush() {
	if q.left <= 0 {
		<-q.ctx.Done()
		return
	}
	q.left--
	if f, ok := q.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// UserConfig is the user's configuration of the recorded session, as the JSON of ~/.sleipnir/config.json: the endpoint, the model, and
// the thread size at which compaction is forced, which is a setting of the user's and is low here so that one happens in a minute.
func (c *ChatScenario) UserConfig(url string) map[string]any {
	return map[string]any{
		"providers": map[string]any{"mock": map[string]any{"base_url": url}},
		"models":    map[string]any{"default": "mock/mock-1"},
		"cache":     map[string]any{"thread_soft_limit_tokens": 600, "compact_threshold_tokens": 1100},
	}
}

// chatCall builds a mock tool invocation with JSON-encoded arguments for the chat script.
func chatCall(id, name string, args map[string]any) mock.ToolCall { return jsonCall(id, name, args) }

var chatUnitRe = regexp.MustCompile(`(?m)^\s+t(\d+)(?:-t(\d+))? · ([^·]*) · (.*)$`)

// respond is the model. It answers by how many requests of the main thread it has answered, never by the length of the thread, which
// a compaction shortens.
func (c *ChatScenario) respond(call *mock.Call) mock.Reply {
	if strings.Contains(call.LastUser(), "<compactor-task>") {
		return chatCompactor(call)
	}
	c.mu.Lock()
	c.step++
	n := c.step
	c.mu.Unlock()
	switch n {
	case 1:
		return mock.Reply{Text: "I'll start with the code the test exercises.",
			ToolCalls: []mock.ToolCall{chatCall("call_1", "read", map[string]any{"path": "orders/list.go"})}}
	case 2:
		return mock.Reply{Text: "Now the tests, to see how they fail.",
			ToolCalls: []mock.ToolCall{chatCall("call_2", "bash", map[string]any{"command": "go test ./orders/..."})}}
	case 3:
		return mock.Reply{Text: "Two pagination tests fail. Let me read what they expect.",
			ToolCalls: []mock.ToolCall{chatCall("call_3", "read", map[string]any{"path": "orders/list_test.go"})}}
	case 4:
		return mock.Reply{Text: "The offset is the bug: pages count from 1, so page 1 starts at row 0, not at row size. It should be (page - 1) * size.",
			ToolCalls: []mock.ToolCall{chatCall("call_4", "edit", map[string]any{"path": "orders/list.go",
				"old_string": "offset := page * size", "new_string": "offset := (page - 1) * size"})}}
	case 5:
		return mock.Reply{Text: "Running the tests again.",
			ToolCalls: []mock.ToolCall{chatCall("call_5", "bash", map[string]any{"command": "go test ./orders/..."})}}
	case 6:
		return mock.Reply{Text: "They pass. One more look, for callers that worked around the old offset.",
			ToolCalls: []mock.ToolCall{chatCall("call_6", "grep", map[string]any{"pattern": `\.List\(`, "glob": "*.go"})}}
	case 7:
		return mock.Reply{Text: "## Fixed\n\n" +
			"`List` took its offset from the page number, but pages count from 1, so page 1 skipped the first `size` rows.\n\n" +
			"- the offset is `(page - 1) * size` now\n" +
			"- `go test ./orders/...` passes, and the HTTP handler already sends one-based pages\n\n" +
			"```go\noffset := (page - 1) * size\n```\n\n" +
			"Want a test for page 0 and a negative size?"}
	}
	// The second goal: a long answer, of which the person sees the beginning.
	return mock.Reply{Text: "I'll add table-driven cases for page 0, a negative page and a negative size, next to the tests that are there and in " +
		"the same style: one store of five orders, one call each, and a check of what comes back. Before I write them I want to see how the " +
		"existing tests build their store, so that the new cases use the same sample and the same helper for reading the ids. Then I'll run " +
		"the whole package once more, and go vet on it, since the new test declares a table type that nothing else in the package uses. " +
		"If any of the new cases fails I will say which, and why, before I touch the code: a failing case here would mean that List has a " +
		"second bug, and you should hear about that before I change anything else."}
}

// chatCompactor answers the fork request of the agent whose thread the planner wants folded: a patch that keeps the newest units and
// gives each folded run a line of the spine, and one fact for the notes. The units are the ones the brief lists.
func chatCompactor(call *mock.Call) mock.Reply {
	units := chatUnitRe.FindAllStringSubmatch(call.LastUser(), -1)
	if len(units) == 0 {
		return mock.Reply{Text: `{"keep_from":"t1","spine":[],"mask":[],"notes":[],"promote":[]}`}
	}
	type entry struct {
		Turns string `json:"turns"`
		Line  string `json:"line"`
	}
	var spine []entry
	last := 0
	for _, u := range units {
		from, _ := strconv.Atoi(u[1])
		to := from
		if u[2] != "" {
			to, _ = strconv.Atoi(u[2])
		}
		turns := "t" + u[1]
		if to != from {
			turns += "-t" + strconv.Itoa(to)
		}
		line := strings.TrimSpace(u[4])
		if i := strings.Index(line, " → "); i > 0 {
			line = line[:i]
		}
		if len(line) > 150 {
			line = line[:150]
		}
		spine = append(spine, entry{turns, "folded: " + line})
		last = to
	}
	patch := map[string]any{"keep_from": "t" + strconv.Itoa(last+1), "spine": spine, "mask": []string{},
		"notes":   []map[string]string{{"op": "add", "key": "facts", "text": "orders/list.go: Store.List takes a page that counts from 1; its tests are in orders/list_test.go"}},
		"promote": []string{}}
	b, _ := json.Marshal(patch)
	return mock.Reply{Text: string(b)}
}
