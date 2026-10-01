package demo

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

// shopScript plays the team of the shop scenario. Like the handbook's script it is the model and nothing else: the harness around
// it is real, and what it does with the script's calls (the gate, the merge queue, the mail router, the compaction planner, the
// repetition guard) is the harness's own doing. The script is written so that the session has what the cockpit and the cache view
// exist to show:
//
//	a fan-out:      three scouts read the same prefix at once
//	mail:           the backend tells the frontend what it will serve, the tester asks the cart's author a question
//	a cold cache:   one worker's build takes longer than the cache lifetime, so its next request is at a cold moment and the planner folds its thread
//	a stuck agent:  one worker runs the same failing check four times until the harness tells it it is repeating itself
//	a cache break:  the provider drops its cache in the middle of the run; the next requests find nothing of the prefix they were promised
//	a merge bounce: two workers each keep a rule in their own tree and break it together; the merge queue sends the second one back
//
// Who does what is decided by the agent's id and the number of requests it has made, which the script counts itself (the thread is
// folded by a compaction, so its length cannot say where an agent is). Cross-agent order is settled with milestones, never with sleeps
// alone, so that a slow machine changes how long the session takes and not what happens in it.
type shopScript struct {
	// scale stretches (>1) or squeezes (<1) the time the script takes; the cache lifetime is scaled with it.
	scale float64
	srv   *mock.Server

	mu    sync.Mutex
	step  map[string]int
	marks map[string]bool
}

func newShopScript(scale float64) *shopScript {
	if scale <= 0 {
		scale = 1
	}
	return &shopScript{scale: scale, step: map[string]int{}, marks: map[string]bool{}}
}

func (s *shopScript) sec(n float64) time.Duration {
	return time.Duration(n * s.scale * float64(time.Second))
}

func (s *shopScript) mark(name string) {
	s.mu.Lock()
	s.marks[name] = true
	s.mu.Unlock()
}

func (s *shopScript) marked(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.marks[name]
}

var (
	shopWho     = regexp.MustCompile(`you: (\S+) \((\w+)\)`)
	shopUnitRe  = regexp.MustCompile(`(?m)^\s+t(\d+)(?:-t(\d+))? · ([^·]*) · (.*)$`)
	shopTaskRef = regexp.MustCompile(`task (T\d+)`)
)

// who is the agent that is asking, as it says so in its prompt (and as a model would read it).
func shopWhoIs(c *mock.Call) (id, role string) {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if m := shopWho.FindStringSubmatch(c.Messages[i].Content); m != nil {
			return m[1], m[2]
		}
	}
	return "", ""
}

// respond is the model of every agent of the team.
func (s *shopScript) respond(c *mock.Call) mock.Reply {
	if strings.Contains(c.LastUser(), "<compactor-task>") {
		return s.compactor(c)
	}
	id, role := shopWhoIs(c)
	if id == "" {
		return mock.Reply{Text: "nothing to do"}
	}
	s.mu.Lock()
	n := s.step[id]
	s.step[id]++
	s.mu.Unlock()
	var r mock.Reply
	switch role {
	case "manager":
		r = s.manager(n)
	case "scout":
		r = s.scout(id, n)
	case "backend":
		if id == "be-1" {
			r = s.catalogue(n)
		} else {
			r = s.cart(id, n)
		}
	case "frontend":
		r = s.web(id, n)
	case "tester":
		r = s.tester(id, n)
	case "reviewer":
		r = s.reviewer(id, n)
	default:
		r = mock.Reply{Text: "nothing to do"}
	}
	return r
}

// again makes the next request of the agent the same step as this one: a wait, in tool time, for something another agent has to do first.
func (s *shopScript) again(id string) {
	s.mu.Lock()
	s.step[id]--
	s.mu.Unlock()
}

func jsonCall(id, name string, args any) mock.ToolCall {
	b, _ := json.Marshal(args)
	return mock.ToolCall{ID: id, Name: name, Args: string(b)}
}

func readCall(id, path string) mock.ToolCall {
	return jsonCall(id, "read", map[string]any{"path": path})
}
func bashCall(id, cmd string) mock.ToolCall {
	return jsonCall(id, "bash", map[string]any{"command": cmd})
}
func writeCall(id, path, body string) mock.ToolCall {
	return jsonCall(id, "write", map[string]any{"path": path, "content": body})
}
func mailCall(id, to, kind, text string) mock.ToolCall {
	return jsonCall(id, "mail", map[string]any{"to": to, "kind": kind, "text": text})
}
func doneCall(id, task, text string) mock.ToolCall {
	return jsonCall(id, "task", map[string]any{"action": "done", "id": task, "text": text})
}

// ---- the manager ----

func (s *shopScript) manager(n int) mock.Reply {
	switch n {
	case 0:
		var calls []mock.ToolCall
		titles := []string{
			"Survey the API: which endpoints the shop has and what each promises (docs/api.md)",
			"Survey the data: the items and the rules their fields keep (data/items.json, data/schema.md)",
			"Survey the design rules: paging, money and where each rule lives (docs/design.md)",
		}
		for i, t := range titles {
			calls = append(calls, jsonCall(fmt.Sprintf("t%d", i+1), "task", map[string]any{"action": "create", "title": t, "role": "scout"}))
		}
		for i := range titles {
			calls = append(calls, jsonCall(fmt.Sprintf("s%d", i+1), "spawn", map[string]any{"role": "scout", "task": fmt.Sprintf("T%d", i+1)}))
		}
		calls = append(calls, jsonCall("w1", "wait", map[string]any{"until": []string{"T1", "T2", "T3"}, "timeout_sec": 120}))
		return mock.Reply{Text: "The shop needs four pieces of work that hardly touch. Before I hand any of it out I want three surveys, read in parallel: the API, the data and the design rules.", ToolCalls: calls}
	case 1:
		var calls []mock.ToolCall
		for i := 1; i <= 3; i++ {
			calls = append(calls, jsonCall(fmt.Sprintf("a%d", i), "task", map[string]any{"action": "accept", "id": fmt.Sprintf("T%d", i)}))
		}
		ws := []struct {
			title, role string
			files       []string
		}{
			{"Catalogue: the items, paging by a stable cursor, and the one DefaultPort of the shop", "backend", []string{"shop/catalogue/**"}},
			{"Cart: lines, and a total in whole cents rounded once at the end", "backend", []string{"shop/cart/**"}},
			{"Web: the handlers for the catalogue and the cart, JSON with snake_case keys", "frontend", []string{"shop/web/**"}},
			{"Smoke tests: start the service, fill a cart, read the total, check out", "tester", []string{"tests/**"}},
		}
		for i, w := range ws {
			calls = append(calls, jsonCall(fmt.Sprintf("c%d", i+1), "task", map[string]any{"action": "create", "title": w.title, "role": w.role, "files": w.files}))
		}
		for i, w := range ws {
			calls = append(calls, jsonCall(fmt.Sprintf("sp%d", i+1), "spawn", map[string]any{"role": w.role, "task": fmt.Sprintf("T%d", 4+i)}))
		}
		calls = append(calls, jsonCall("w2", "wait", map[string]any{"until": []string{"T4", "T5", "T6", "T7"}, "timeout_sec": 180}))
		return mock.Reply{Text: "The surveys agree with each other. Four workers, each in a tree of its own: the catalogue, the cart, the web handlers and the smoke tests. The merge queue will say if their pieces disagree.", ToolCalls: calls}
	case 2:
		var calls []mock.ToolCall
		for i := 4; i <= 7; i++ {
			calls = append(calls, jsonCall(fmt.Sprintf("b%d", i), "task", map[string]any{"action": "accept", "id": fmt.Sprintf("T%d", i)}))
		}
		calls = append(calls, jsonCall("rv", "task", map[string]any{"action": "create", "title": "Review what was merged against AGENTS.md and run the shop's checks", "role": "reviewer"}))
		calls = append(calls, jsonCall("sr", "spawn", map[string]any{"role": "reviewer", "task": "T8"}))
		calls = append(calls, jsonCall("w3", "wait", map[string]any{"until": []string{"T8"}, "timeout_sec": 120}))
		return mock.Reply{Text: "All four pieces are merged and verified. One reviewer to read the result against the conventions.", ToolCalls: calls}
	case 3:
		return mock.Reply{Text: "Reviewed.", ToolCalls: []mock.ToolCall{jsonCall("ar", "task", map[string]any{"action": "accept", "id": "T8"})}}
	}
	return mock.Reply{Text: "Done. The shop has a catalogue with paging, a cart that totals in whole cents, the web handlers for both and smoke tests; every piece went through the merge queue and the checks pass on the result."}
}

// ---- the scouts ----

func (s *shopScript) scout(id string, n int) mock.Reply {
	reads := map[string][]string{"sc-1": {"docs/api.md"}, "sc-2": {"data/items.json", "data/schema.md"}, "sc-3": {"docs/design.md"}}
	report := map[string]string{
		"sc-1": "eleven endpoints; lists are paged by limit and after; errors are {code, message}; money is integer cents",
		"sc-2": "48 items with id, name, price_cents, stock and tags; ids are sku- and three digits; no price is below one cent",
		"sc-3": "cursor paging, one rounding at the end of a total, one DefaultPort defined in the catalogue, handlers with no rules of their own",
	}
	task := map[string]string{"sc-1": "T1", "sc-2": "T2", "sc-3": "T3"}
	switch n {
	case 0:
		var calls []mock.ToolCall
		for i, p := range reads[id] {
			calls = append(calls, readCall(fmt.Sprintf("r%s%d", id, i), p))
		}
		return mock.Reply{Text: "Reading the source the survey is about, all of it, before I say anything.", ToolCalls: calls}
	case 1:
		return mock.Reply{Text: "Reporting.", ToolCalls: []mock.ToolCall{doneCall("d"+id, task[id], report[id])}}
	}
	return mock.Reply{Text: "Done: " + report[id] + "."}
}

// ---- the catalogue (be-1): reads a lot, tells the web what it will serve, builds for longer than the cache stays warm ----

func (s *shopScript) catalogue(n int) mock.Reply {
	switch n {
	case 0:
		return mock.Reply{Text: "The catalogue is the piece the others lean on, so I read everything about it first: the API, the items and their rules.", ToolCalls: []mock.ToolCall{
			readCall("r1", "docs/api.md"), readCall("r2", "data/items.json"), readCall("r3", "data/schema.md")}}
	case 1:
		return mock.Reply{Text: "Telling the web what I will serve, then the types.", ToolCalls: []mock.ToolCall{
			mailCall("m1", "fe-1", "contract", "catalogue: GET /items answers {items:[{id,name,price_cents,stock,tags}], next}; the port is catalogue.DefaultPort, defined once in shop/catalogue, so please do not define your own."),
			writeCall("w1", "shop/catalogue/items.go", catalogueItemsGo)}}
	case 2:
		return mock.Reply{Text: "The store: a mutex, the list in id order, and the page cut at the cursor.", ToolCalls: []mock.ToolCall{
			writeCall("w2", "shop/catalogue/store.go", catalogueStoreGo)}}
	case 3:
		return mock.Reply{Text: "A full build and its checks take a while on this machine.", ToolCalls: []mock.ToolCall{
			bashCall("b1", fmt.Sprintf("sleep %.1f; echo built", s.sec(7).Seconds()))}}
	case 4:
		return mock.Reply{Text: "Built. The shop's own checks before I say it is done.", ToolCalls: []mock.ToolCall{bashCall("b2", "sh verify.sh")}}
	case 5:
		return mock.Reply{Text: "Done.", ToolCalls: []mock.ToolCall{doneCall("d1", "T4", "the catalogue with paging by a stable cursor, and the one DefaultPort")}}
	case 6:
		s.mark("T4 merged") // the done above went through the merge queue: the integration branch has DefaultPort now
	}
	return mock.Reply{Text: "The catalogue is merged: items, paging by a stable cursor, and the one DefaultPort of the shop."}
}

// ---- the cart (be-2): a check that fails until the harness says it is going round in circles ----

func (s *shopScript) cart(id string, n int) mock.Reply {
	switch n {
	case 0:
		return mock.Reply{Text: "I need the rules for totals before I write them.", ToolCalls: []mock.ToolCall{readCall("r1", "docs/design.md"), readCall("r2", "docs/api.md")}}
	case 1:
		return mock.Reply{Text: "A first version of the cart; the rounding is still to do.", ToolCalls: []mock.ToolCall{writeCall("w1", "shop/cart/cart.go", cartGoFirst)}}
	case 2, 3, 4:
		return mock.Reply{Text: "Running the cart's checks.", ToolCalls: []mock.ToolCall{bashCall("c"+strconv.Itoa(n), "sh checks.sh")}}
	case 5:
		// The provider's cache goes away for a few seconds: every request in that time finds nothing of the prefix it was promised, and
		// the harness says so (a cache anomaly per agent that had a warm cache), with what each miss cost.
		s.srv.CacheOutage(s.sec(2.5))
		return mock.Reply{Text: "Running the cart's checks again.", ToolCalls: []mock.ToolCall{bashCall("c5", "sh checks.sh")}}
	case 6:
		return mock.Reply{Text: "That is four times the same failure. The message says where: the total is not rounded. I will write the rounding instead of running the check again.", ToolCalls: []mock.ToolCall{writeCall("w2", "shop/cart/cart.go", cartGoFixed)}}
	case 7:
		return mock.Reply{Text: "Now the check.", ToolCalls: []mock.ToolCall{bashCall("c7", "sh checks.sh")}}
	case 8:
		return mock.Reply{Text: "Done.", ToolCalls: []mock.ToolCall{doneCall("d1", "T5", "the cart: lines, and a total in whole cents rounded once at the end")}}
	}
	return mock.Reply{Text: "The cart is merged: lines, and a total in whole cents rounded once at the end."}
}

// ---- the web (fe-1): keeps a rule in its own tree that it breaks together with the catalogue ----

func (s *shopScript) web(id string, n int) mock.Reply {
	switch n {
	case 0:
		return mock.Reply{Text: "The handlers follow the API document to the letter.", ToolCalls: []mock.ToolCall{readCall("r1", "docs/api.md")}}
	case 1:
		return mock.Reply{Text: "The handlers, and the port they listen on.", ToolCalls: []mock.ToolCall{writeCall("w1", "shop/web/handlers.go", webHandlersFirst)}}
	case 2:
		if !s.marked("T4 merged") {
			s.again(id)
			return mock.Reply{Text: "The catalogue is not merged yet; I cannot read what it serves. Waiting for it.", ToolCalls: []mock.ToolCall{bashCall(fmt.Sprintf("b%d", time.Now().UnixNano()%1000), fmt.Sprintf("sleep %.1f", s.sec(2).Seconds()))}}
		}
		return mock.Reply{Text: "The catalogue is merged. The checks in my tree.", ToolCalls: []mock.ToolCall{bashCall("v1", "sh verify.sh")}}
	case 3:
		return mock.Reply{Text: "Done.", ToolCalls: []mock.ToolCall{doneCall("d1", "T6", "the web handlers for the catalogue and the cart")}}
	case 4:
		return mock.Reply{Text: "The merge says the shop now has two DefaultPort definitions: mine, and the catalogue's, which was merged while I worked. The convention is one, in shop/catalogue. I take mine out and use the catalogue's.", ToolCalls: []mock.ToolCall{writeCall("w2", "shop/web/handlers.go", webHandlersFixed)}}
	case 5:
		return mock.Reply{Text: "Done again.", ToolCalls: []mock.ToolCall{doneCall("d2", "T6", "the web handlers, using the catalogue's DefaultPort")}}
	}
	return mock.Reply{Text: "The web handlers are merged; they listen on the catalogue's DefaultPort."}
}

// ---- the tester (ts-1): asks the cart's author a question ----

func (s *shopScript) tester(id string, n int) mock.Reply {
	switch n {
	case 0:
		return mock.Reply{Text: "The smoke tests need the API and the design rules in front of me.", ToolCalls: []mock.ToolCall{readCall("r1", "docs/api.md"), readCall("r2", "docs/design.md")}}
	case 1:
		return mock.Reply{Text: "One thing I cannot take from the documents, so I ask, and write the script meanwhile.", ToolCalls: []mock.ToolCall{
			mailCall("m1", "be-2", "request", "cart: for a line of 3 at 333 cents, is /cart/total 999 or 1000? I assume the sum is exact and rounding happens once at the end."),
			writeCall("w1", "tests/smoke.sh", smokeSh)}}
	case 2:
		return mock.Reply{Text: "The shop's checks on my tree.", ToolCalls: []mock.ToolCall{bashCall("v1", "sh verify.sh")}}
	case 3:
		return mock.Reply{Text: "Done.", ToolCalls: []mock.ToolCall{doneCall("d1", "T7", "a smoke script: start, add an item, fill a cart, read the total, check out")}}
	}
	return mock.Reply{Text: "The smoke script is merged under tests/."}
}

// ---- the reviewer ----

func (s *shopScript) reviewer(id string, n int) mock.Reply {
	switch n {
	case 0:
		return mock.Reply{Text: "I look at what was merged and run the shop's checks.", ToolCalls: []mock.ToolCall{bashCall("v1", "git branch --list 'sleipnir/*' | wc -l"), bashCall("v2", "sh verify.sh")}}
	case 1:
		return mock.Reply{Text: "Reporting.", ToolCalls: []mock.ToolCall{doneCall("d1", "T8", "the conventions hold: one DefaultPort, money in cents, every Go file starts with its package clause")}}
	}
	return mock.Reply{Text: "Reviewed: the conventions hold."}
}

// ---- the compactor ----

// compactor answers the fork request of an agent whose thread the planner wants folded: a patch that keeps the newest units and gives
// every older one a line in the spine, written from the list of foldable units that the request itself carries.
func (s *shopScript) compactor(c *mock.Call) mock.Reply {
	units := shopUnitRe.FindAllStringSubmatch(c.LastUser(), -1)
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
		spine = append(spine, entry{turns, "read and digested: " + line})
		last = to
	}
	patch := map[string]any{"keep_from": "t" + strconv.Itoa(last+1), "spine": spine, "mask": []string{},
		"notes":   []map[string]string{{"op": "add", "key": "facts", "text": "docs/api.md, data/items.json and docs/design.md were read; the paging cursor is the id of the last item and there is one DefaultPort"}},
		"promote": []string{}}
	b, _ := json.Marshal(patch)
	return mock.Reply{Text: string(b)}
}

// ---- the files the workers write ----

const catalogueItemsGo = `package catalogue

// DefaultPort is the port the whole shop listens on. It is defined here and nowhere else.
const DefaultPort = 8080

// Item is something the shop sells. Money is integer cents.
type Item struct {
	ID         string   ` + "`json:\"id\"`" + `
	Name       string   ` + "`json:\"name\"`" + `
	PriceCents int      ` + "`json:\"price_cents\"`" + `
	Stock      int      ` + "`json:\"stock\"`" + `
	Tags       []string ` + "`json:\"tags\"`" + `
}

// Page is one page of items and the cursor of the next, or nil at the end.
type Page struct {
	Items []Item  ` + "`json:\"items\"`" + `
	Next  *string ` + "`json:\"next\"`" + `
}
`

const catalogueStoreGo = `package catalogue

import (
	"sort"
	"sync"
)

// Store is the catalogue held in memory, in the order of the ids.
type Store struct {
	mu    sync.Mutex
	items []Item
}

// Add puts an item in its place in the id order.
func (s *Store) Add(it Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := sort.Search(len(s.items), func(i int) bool { return s.items[i].ID >= it.ID })
	s.items = append(s.items, Item{})
	copy(s.items[i+1:], s.items[i:])
	s.items[i] = it
}

// List returns at most limit items after the cursor. The cursor is the id of the last item of the previous page, so an item added or
// removed while a customer pages does not make a page repeat or skip one.
func (s *Store) List(after string, limit int) Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	i := sort.Search(len(s.items), func(i int) bool { return s.items[i].ID > after })
	end := min(i+limit, len(s.items))
	page := Page{Items: append([]Item(nil), s.items[i:end]...)}
	if end < len(s.items) {
		next := s.items[end-1].ID
		page.Next = &next
	}
	return page
}
`

const cartGoFirst = `package cart

// Line is an item in a cart and how many of it.
type Line struct {
	ID  string
	Qty int
}

// Total is what the lines cost in cents: price times quantity, summed.
func Total(lines []Line, price func(id string) int) int {
	total := 0
	for _, l := range lines {
		total += price(l.ID) * l.Qty // TODO(rounding): round once, at the end, half up
	}
	return total
}
`

const cartGoFixed = `package cart

// Line is an item in a cart and how many of it.
type Line struct {
	ID  string
	Qty int
}

// Total is what the lines cost in whole cents: the prices are in tenths of a cent, summed exactly, and rounded half up once, at the
// end, never per line.
func Total(lines []Line, priceTenths func(id string) int) int {
	tenths := 0
	for _, l := range lines {
		tenths += priceTenths(l.ID) * l.Qty
	}
	return (tenths + 5) / 10
}
`

const webHandlersFirst = `package web

import (
	"encoding/json"
	"net/http"
)

// DefaultPort is the port the service listens on.
const DefaultPort = 8080

// Health says the process is alive and nothing else.
func Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
`

const webHandlersFixed = `package web

import (
	"encoding/json"
	"net/http"

	"shop/catalogue"
)

// Addr is where the service listens: the catalogue's port, the only one the shop has.
func Addr() string { return ":" + itoa(catalogue.DefaultPort) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

// Health says the process is alive and nothing else.
func Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
`

const smokeSh = `#!/bin/sh
# Smoke test: start the service on its default port, fill a cart and check out. Needs sh and curl.
set -e
base="http://127.0.0.1:8080"
curl -fsS "$base/healthz" >/dev/null
curl -fsS -X POST "$base/cart/lines" -d '{"id":"sku-001","qty":3}' >/dev/null
total=$(curl -fsS "$base/cart/total")
echo "cart total: $total"
curl -fsS -X POST "$base/checkout" -d '{"address":"1 Main Street"}' >/dev/null
echo "ok: checked out"
`
