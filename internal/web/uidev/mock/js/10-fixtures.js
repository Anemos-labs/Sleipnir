/* 10-fixtures.js: the built-in sample data (SL.FX). Everything here is SAMPLE unless a comment says REAL.
 * The core works with only this; 11-data-adapter.js lets window.SLDATA (the data pack) override pieces of it.
 * The roster table below is the ONE table every number in the app derives from. */
(function (SL) {
  'use strict';
  const { rng } = SL.u;

  /* ---- roles: short codes and colours come from the code (internal/swarm roles) ---- */
  const ROLES = {
    manager: { code: 'mgr', ro: false, desc: 'plans, spawns, merges; edits no file' },
    backend: { code: 'be', ro: false, desc: 'server code' },
    frontend: { code: 'fe', ro: false, desc: 'pages and client code' },
    fullstack: { code: 'fs', ro: false, desc: 'both sides of one feature' },
    tester: { code: 'ts', ro: false, desc: 'tests' },
    reviewer: { code: 'rv', ro: true, desc: 'read-only review' },
    scout: { code: 'sc', ro: true, desc: 'read-only survey' },
    docs: { code: 'dc', ro: false, desc: 'documentation' },
  };
  const ROLE_ORDER = ['scout', 'backend', 'frontend', 'tester', 'reviewer', 'docs', 'fullstack'];
  const PRICES = { mgr: { in: 3.0, cached: 0.3, out: 15.0 }, worker: { in: 1.0, cached: 0.1, out: 4.0 }, sample: true };

  /* ---- the roster at the snapshot t = 00:38 (start order = leg order; the manager has no leg) ---- */
  const ROSTER = [
    { id: 'mgr',  role: 'manager',  state: 'wait',  task: null, doing: 'waits for the team (T4 T5 T6 T7 T8)', scope: '-',                              prompt: 9400, read: 7332, out: 1200, spawn: 0 },
    { id: 'sc-1', role: 'scout',    state: 'done',  task: 'T1', doing: 'eleven endpoints; list shapes',       scope: '-',                              prompt: 6100, read: 5490, out: 500,  spawn: 4.6 },
    { id: 'sc-2', role: 'scout',    state: 'done',  task: 'T2', doing: '48 items with id, name, price',       scope: '-',                              prompt: 6400, read: 5760, out: 520,  spawn: 4.6 },
    { id: 'sc-3', role: 'scout',    state: 'done',  task: 'T3', doing: 'one-based paging; cursors only on /orders', scope: '-',                        prompt: 5800, read: 5278, out: 480,  spawn: 4.6 },
    { id: 'be-1', role: 'backend',  state: 'wait',  task: 'T4', doing: 'T4 submitted: harness runs `go test ./api/catalog/...`', scope: 'api/catalog/**, api/server.go', prompt: 8100, read: 7209, out: 2300, spawn: 16.9 },
    { id: 'be-2', role: 'backend',  state: 'edit',  task: 'T5', doing: 'editing api/cart/cart.go: Total() in cents', scope: 'api/cart/**',              prompt: 8600, read: 6622, out: 2100, spawn: 16.9 },
    { id: 'fe-1', role: 'frontend', state: 'ask',   task: 'T6', doing: 'wants to run `npm install --save-dev vitest`', scope: 'web/**',                prompt: 7200, read: 6480, out: 1500, spawn: 16.9 },
    { id: 'ts-1', role: 'tester',   state: 'think', task: 'T7', doing: 'table tests for the catalogue contract', scope: 'api/**/*_test.go',           prompt: 5900, read: 4897, out: 900,  spawn: 16.9 },
    { id: 'rv-1', role: 'reviewer', state: 'idle',  task: 'T8', doing: 'waits for T4 to merge (read-only)',    scope: '- (read-only)',                  prompt: 2400, read: 1776, out: 150,  spawn: 16.9 },
  ];
  /* per-request count at the snapshot: sets the length of each hit series and the size of a live request */
  const NREQ = { mgr: 5, 'sc-1': 3, 'sc-2': 3, 'sc-3': 3, 'be-1': 6, 'be-2': 7, 'fe-1': 5, 'ts-1': 3, 'rv-1': 2 };

  /* hit series: per-request ratios whose mean equals the cumulative hit (equal-sized requests), be-2's last request is the break (0) */
  function makeSeries(a) {
    const n = NREQ[a.id], h = a.read / a.prompt, rnd = rng(a.id.split('').reduce((s, c) => s + c.charCodeAt(0), 7));
    if (a.id === 'be-2') return [0.65, 0.95, 0.95, 0.95, 0.95, 0.94, 0];
    const c = Math.min(0.75, Math.max(0.55, n * h - (n - 1) * 0.96)), out = [Math.round(c * 100) / 100];
    for (let i = 1; i < n - 1; i++) out.push(Math.round((((n * h - c) / (n - 1)) + (rnd() - 0.5) * 0.03) * 100) / 100);
    out.push(Math.round((n * h - out.reduce((s, x) => s + x, 0)) * 100) / 100);
    return out;
  }
  const HITSERIES = {}; ROSTER.forEach(a => { HITSERIES[a.id] = makeSeries(a); });

  const LAYERS = [
    { id: 'G0', name: 'constitution', tok: 1900, note: 'the harness rules, shared by every agent', col: 'var(--mgr)' },
    { id: 'G1', name: 'shared pin', tok: 2200, note: 'project survey (recon), shared', col: 'var(--be)' },
    { id: 'G2', name: 'role pin', tok: 400, note: 'what this role does, shared by the workers of that role', col: 'var(--fe)' },
    { id: 'G3', name: 'notes', tok: 300, note: 'what the agent wrote down', col: 'var(--ts)' },
    { id: 'G4', name: 'spine', tok: 350, note: 'the folded history', col: 'var(--warm)' },
    { id: 'G5', name: 'thread', tok: 1000, note: 'verbatim recent turns, per agent', col: 'var(--rv)' },
  ];
  const G5TOK = { mgr: 1400, 'be-1': 900, 'be-2': 1000, 'fe-1': 900, 'sc-1': 600, 'sc-2': 600, 'sc-3': 600, 'ts-1': 800, 'rv-1': 500 };

  const GOAL = 'Build the shop: a catalogue with pagination, a cart with a running total, and the shop page that shows them. go test ./... must pass.';
  const PLAN = [
    'Survey the API, the seed data and the paging conventions',
    'Catalogue endpoint with pagination',
    'Cart with a running total, money in cents',
    'Shop page: item grid and pager',
    'Tests and review of the catalogue and the cart',
    'go test ./... passes on the merged result',
  ];
  const TASKS = {
    T1: { title: 'survey endpoints', owner: 'sc-1', deps: [], scope: '-' },
    T2: { title: 'survey seed data', owner: 'sc-2', deps: [], scope: '-' },
    T3: { title: 'survey paging patterns', owner: 'sc-3', deps: [], scope: '-' },
    T4: { title: 'catalogue: GET /items?page&size', owner: 'be-1', deps: ['T3'], scope: 'api/catalog/**, api/server.go' },
    T5: { title: 'cart: add, remove, total (cents)', owner: 'be-2', deps: ['T2'], scope: 'api/cart/**' },
    T6: { title: 'shop page: item grid and pager', owner: 'fe-1', deps: ['T1', 'T4'], scope: 'web/**' },
    T7: { title: 'tests: catalogue and cart', owner: 'ts-1', deps: ['T4', 'T5'], scope: 'api/**/*_test.go' },
    T8: { title: 'review: catalogue and cart', owner: 'rv-1', deps: ['T4', 'T5'], scope: '- (read-only)' },
  };
  const DEPS = [['T1', 'T6'], ['T2', 'T5'], ['T3', 'T4'], ['T4', 'T6'], ['T4', 'T7'], ['T5', 'T7'], ['T4', 'T8'], ['T5', 'T8']];
  const CKPTS = [
    { id: 'c07', ts: '03:04:38', files: 3, note: 'T4 catalogue handler', skipped: false },
    { id: 'c06', ts: '03:04:29', files: 1, note: 'T5 cart total in cents', skipped: false },
    { id: 'c05', ts: '03:04:21', files: 2, note: 'seed items + loader', skipped: false },
    { id: 'c04', ts: '03:04:12', files: 0, note: 'skipped: nothing to put back', skipped: true },
  ];

  /* ---- code (REAL: the text of the repository; items_test.go is sample) ---- */
  const CODE = {
    'api/catalog/items.go': { st: 'A', add: 31, del: 0, ag: 'be-1', task: 'T4', kind: 'file', text:
`package catalog

import "sort"

// MaxSize is the largest page a client may ask for.
const MaxSize = 48

// Item is one thing the shop sells. Prices are in cents.
type Item struct {
	ID         string \`json:"id"\`
	Name       string \`json:"name"\`
	PriceCents int64  \`json:"price_cents"\`
}

// Page is one page of the catalogue, counted from 1.
type Page struct {
	Items []Item \`json:"items"\`
	Page  int    \`json:"page"\`
	Pages int    \`json:"pages"\`
	Total int    \`json:"total"\`
}

// Store holds the catalogue in memory, sorted by ID.
type Store struct{ rows []Item }

// New returns a Store over rows, sorted by ID.
func New(rows []Item) *Store {
	s := &Store{rows: append([]Item(nil), rows...)}
	sort.Slice(s.rows, func(i, j int) bool { return s.rows[i].ID < s.rows[j].ID })
	return s
}

// List returns page number page (from 1) of at most size items. A page below 1 is page 1,
// a page past the end is the last page, and a size outside 1..MaxSize is clamped.
func (s *Store) List(page, size int) Page {
	size = min(max(size, 1), MaxSize)
	pages := (len(s.rows) + size - 1) / size
	page = min(max(page, 1), max(pages, 1))
	lo := (page - 1) * size
	hi := min(lo+size, len(s.rows))
	return Page{Items: s.rows[lo:hi], Page: page, Pages: pages, Total: len(s.rows)}
}` },
    'api/server.go': { st: 'M', add: 7, del: 0, ag: 'be-1', task: 'T4', kind: 'diff', text:
`@@ -18,6 +18,7 @@ func (s *Server) routes() http.Handler {
 	mux := http.NewServeMux()
 	mux.HandleFunc("GET /healthz", s.health)
+	mux.HandleFunc("GET /items", s.items)
 	mux.HandleFunc("GET /orders", s.orders)
 	return mux
 }
@@ -44,3 +45,10 @@ func (s *Server) health(w http.ResponseWriter, r *http.Request) {
 	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
 }
+
+// items serves one page of the catalogue: GET /items?page=1&size=12.
+func (s *Server) items(w http.ResponseWriter, r *http.Request) {
+	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
+	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
+	writeJSON(w, http.StatusOK, s.catalog.List(page, size))
+}` },
    'api/cart/cart.go': { st: 'M', add: 4, del: 4, ag: 'be-2', task: 'T5', kind: 'diff', live: true, text:
`@@ -23,10 +23,10 @@ func (c *Cart) Add(id string, price int64, qty int) {
 // Total is what the cart costs, in cents.
-func (c *Cart) Total() float64 {
-	var total float64
+func (c *Cart) Total() int64 {
+	var total int64
 	for _, l := range c.lines {
-		total += l.Price * float64(l.Qty)
+		total += l.PriceCents * int64(l.Qty)
 	}
 	return total
 }` },
    'web/shop.js': { st: 'A', add: 14, del: 0, ag: 'fe-1', task: 'T6', kind: 'file', note: 'uncommitted until the question is answered', text:
`const grid = document.querySelector('#items');
const pager = document.querySelector('#pager');
let page = 1;

async function load(n) {
  const res = await fetch(\`/items?page=\${n}&size=12\`);
  const data = await res.json();
  page = data.page;
  grid.replaceChildren(...data.items.map(card));
  pager.replaceChildren(...pagerButtons(data.page, data.pages));
}` },
    'api/catalog/items_test.go': { st: 'A', add: 0, del: 0, ag: 'ts-1', task: 'T7', kind: 'file', stream: 'ts-1', text:
`package catalog

import "testing"

func rows(n int) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = Item{ID: string(rune('a' + i%26)), Name: "item", PriceCents: 100}
	}
	return out
}

func TestList(t *testing.T) {
	s := New(rows(48))
	cases := []struct {
		name       string
		page, size int
		wantLen    int
		wantPage   int
	}{
		{"first page", 1, 12, 12, 1},
		{"page 0 is page 1", 0, 12, 12, 1},
		{"size clamped to MaxSize", 1, 500, 48, 1},
		{"size below 1 is 1", 1, -3, 1, 1},
		{"past the end is the last page", 99, 12, 12, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := s.List(c.page, c.size)
			if len(got.Items) != c.wantLen || got.Page != c.wantPage {
				t.Fatalf("List(%d,%d) = %d items on page %d; want %d on %d",
					c.page, c.size, len(got.Items), got.Page, c.wantLen, c.wantPage)
			}
		})
	}
}` },
  };
  const STREAM_TEST = 'func TestList(t *testing.T) {\n\tcases := []struct{ name string; page, size, want int }{\n\t\t{"first page", 1, 12, 12}, {"page 0 is page 1", 0, 12, 12},\n\t\t{"size clamped", 1, 500, 48}, {"past the end", 99, 12, 12},\n\t}';

  /* project trees: path, size, status, owner, protected */
  const TREE_SHOP = [
    ['AGENTS.md', 1.2, '-'], ['Makefile', .4, '-'], ['go.mod', .1, '-'], ['.gitignore', .1, '-'], ['.sleipnir/config.json', .5, '-'],
    ['.env', .1, '-', null, 'deny'], ['secrets/', 0, '-', null, 'deny'],
    ['api/server.go', 2.9, 'M', 'be-1', 'lease'], ['api/catalog/items.go', 1.4, 'A', 'be-1', 'lease'], ['api/catalog/items_test.go', 1.2, 'A', 'ts-1', 'lease'],
    ['api/cart/cart.go', 1.1, 'M', 'be-2', 'lease'], ['web/index.html', .9, '-', 'fe-1', 'lease'], ['web/shop.js', .6, 'A', 'fe-1', 'lease'], ['web/shop.css', .8, '-', 'fe-1', 'lease'],
    ['seed/items.json', 6.4, '-'],
  ];
  const TREE_ORDERS = [
    ['AGENTS.md', .8, '-'], ['go.mod', .1, '-'], ['orders/list.go', 1.3, 'M', 'mgr'], ['orders/list_test.go', 1.1, 'M', 'mgr'], ['orders/store.go', 1.9, '-'], ['cmd/orders/main.go', .7, '-'], ['.env', .1, '-', null, 'deny'],
  ];
  const ORDERS_CODE = {
    'orders/list.go': { st: 'M', add: 3, del: 3, ag: 'mgr', task: null, kind: 'diff', text:
`@@ -12,9 +12,9 @@ func (s *Store) List(page, size int) []Order {
 	if size <= 0 {
 		size = 20
 	}
-	lo := page * size
-	hi := lo + size
+	lo := (page - 1) * size
+	hi := min(lo+size, len(s.rows))
 	if lo >= len(s.rows) {
 		return nil
 	}
-	return s.rows[lo:min(hi, len(s.rows))]
+	return s.rows[lo:hi]` },
    'orders/list_test.go': { st: 'M', add: 9, del: 0, ag: 'mgr', task: null, kind: 'diff', text:
`@@ -20,3 +20,12 @@ func TestList(t *testing.T) {
 		t.Fatalf("page 1: got %d orders, want 20", len(got))
 	}
 }
+
+func TestListIsOneBased(t *testing.T) {
+	s := newStore(45)
+	if got := s.List(1, 20); got[0].ID != "o-001" {
+		t.Fatalf("page 1 starts at %s, want o-001", got[0].ID)
+	}
+	if got := s.List(3, 20); len(got) != 5 {
+		t.Fatalf("page 3 has %d orders, want 5", len(got))
+	}
+}` },
  };

  /* ---- sessions: three live fixtures + 12 recorded (REAL id format; everything else sample) ---- */
  const LIVE = [
    { id: 'shop', name: 'shop', kind: 'shop', sid: '20260102-030405-5eed01', cwd: '~/projects/shop', model: 'anthropic/claude-sonnet-5-5', mode: 'default', effort: 'default', budget: 5, swarm: 8, isolation: 'worktree', verify: 'go test {dirs}', commit: false, mailman: false, trustProject: true, noMcp: false, headless: false, t0: 3 * 3600 + 4 * 60 + 5, started: '03:04:05', launch: 'sleipnir chat --swarm 8 --isolation worktree --verify "go test {dirs}" --budget-usd 5' },
    { id: 'orders-api', name: 'orders-api', kind: 'orders', sid: '20260102-025110-c04a7d', cwd: '~/projects/orders-api', model: 'anthropic/claude-sonnet-5-5', mode: 'accept-edits', effort: 'default', budget: 2, swarm: 0, isolation: 'none', verify: '', commit: false, mailman: false, trustProject: true, noMcp: false, headless: false, t0: 2 * 3600 + 51 * 60 + 10, started: '02:51:10', launch: 'sleipnir chat --swarm 0 --mode accept-edits --budget-usd 2' },
    { id: 'docs-sweep', name: 'docs-sweep', kind: 'docs', sid: '20260102-030000-9ab1e2', cwd: '~/projects/shop', model: 'anthropic/claude-sonnet-5-5', mode: 'accept-edits', effort: 'default', budget: 1, swarm: 2, isolation: 'worktree', verify: '', commit: false, mailman: false, trustProject: true, noMcp: true, headless: true, askTimeout: '10m', t0: 3 * 3600, started: '03:00:00', launch: 'sleipnir run --swarm 2 --mode accept-edits --budget-usd 1 --ask-timeout 10m "Sweep docs/: fix stale command names and broken relative links"', schedule: 'nightly docs sweep (cron 0 3 * * *)' },
  ];
  const D = 86400;
  const RECORDED = [
    { id: '20260101-221530-a91c3e', first: 'Survey the repo and write AGENTS.md', model: 'anthropic/claude-sonnet-5-5', cost: 0.04, mb: 3.1, ageS: 0.2 * D, agents: 3, resumable: true, dur: 252 },
    { id: '20260101-181102-77bd20', first: 'Add GET /orders with cursor paging', model: 'anthropic/claude-sonnet-5-5', cost: 0.19, mb: 6.4, ageS: 0.38 * D, agents: 5, resumable: true, dur: 468 },
    { id: '20260101-094409-0c2e9b', first: 'Fix the cart rounding on 0.1 + 0.2', model: 'heimdall/demo-model', cost: 0.02, mb: 1.2, ageS: 0.75 * D, agents: 1, resumable: true, dur: 125, interrupted: true },
    { id: '20251231-151200-d44a10', first: 'Seed 48 items from the catalogue CSV', model: 'heimdall/demo-model', cost: 0.06, mb: 2.4, ageS: 1.5 * D, agents: 2, resumable: true, dur: 331 },
    { id: '20251229-113015-f3d210', first: 'Rename Page.Count to Page.Total', model: 'anthropic/claude-haiku-5-5', cost: 0.03, mb: 1.6, ageS: 3.5 * D, agents: 1, resumable: true, dur: 94 },
    { id: '20251224-090044-1be5a8', first: 'Write the order status SSE endpoint', model: 'anthropic/claude-sonnet-5-5', cost: 0.27, mb: 9.8, ageS: 8.9 * D, agents: 6, resumable: true, dur: 720 },
    { id: '20251218-163340-6c0f77', first: 'Profile the catalogue handler', model: 'anthropic/claude-sonnet-5-5', cost: 0.11, mb: 4.0, ageS: 14 * D, agents: 2, resumable: true, dur: 302 },
    { id: '20251212-110033-be7712', first: 'Spike: server-sent events for order status', model: 'anthropic/claude-sonnet-5-5', cost: 0.31, mb: 12.6, ageS: 21 * D, agents: 4, resumable: true, dur: 1082 },
    { id: '20251130-084418-90ac52', first: 'Port the cart to int64 cents', model: 'heimdall/demo-model', cost: 0.12, mb: 5.2, ageS: 33 * D, agents: 3, resumable: false, dur: 580 },
    { id: '20251121-195502-3a77d9', first: 'Add a Makefile target for the seed loader', model: 'heimdall/demo-model', cost: 0.02, mb: 0.9, ageS: 42 * D, agents: 1, resumable: false, dur: 77 },
    { id: '20251107-142210-e81c05', first: 'Migrate the web page to fetch()', model: 'anthropic/claude-haiku-5-5', cost: 0.08, mb: 3.3, ageS: 56 * D, agents: 2, resumable: false, dur: 244 },
    { id: '20251019-101900-52d6b4', first: 'First run: survey and AGENTS.md', model: 'anthropic/claude-sonnet-5-5', cost: 0.05, mb: 2.0, ageS: 81 * D, agents: 3, resumable: false, dur: 190 },
    /* older sessions: the 26 recorded sessions (+3 live) make `sessions prune --older-than 30d --keep 20` list nine of them */
    { id: '20251127-101544-b21f90', first: 'Add request logging middleware', model: 'heimdall/demo-model', cost: 0.04, mb: 1.8, ageS: 36 * D, agents: 1, resumable: false, dur: 110 },
    { id: '20251123-163920-4ac8e1', first: 'Why does the seed loader skip row 41?', model: 'anthropic/claude-haiku-5-5', cost: 0.02, mb: 0.7, ageS: 40 * D, agents: 1, resumable: false, dur: 64 },
    { id: '20251116-092251-d07b36', first: 'Split server.go into handlers and routes', model: 'anthropic/claude-sonnet-5-5', cost: 0.21, mb: 7.5, ageS: 47 * D, agents: 4, resumable: false, dur: 640 },
    { id: '20251111-141008-91e2af', first: 'Add a /healthz endpoint', model: 'heimdall/demo-model', cost: 0.01, mb: 0.6, ageS: 52 * D, agents: 1, resumable: false, dur: 48 },
    { id: '20251104-110642-7c35d2', first: 'Rewrite the catalogue filter in plain JS', model: 'anthropic/claude-sonnet-5-5', cost: 0.14, mb: 4.6, ageS: 59 * D, agents: 3, resumable: false, dur: 410 },
    { id: '20251029-195317-e8a014', first: 'Document the Makefile targets', model: 'anthropic/claude-haiku-5-5', cost: 0.03, mb: 1.1, ageS: 65 * D, agents: 1, resumable: false, dur: 85 },
    { id: '20251025-084730-2f6b9c', first: 'Cart totals: use integer cents', model: 'heimdall/demo-model', cost: 0.07, mb: 2.9, ageS: 69 * D, agents: 2, resumable: false, dur: 230 },
    { id: '20251016-153205-a03d77', first: 'List the dependencies we can drop', model: 'anthropic/claude-haiku-5-5', cost: 0.02, mb: 0.5, ageS: 78 * D, agents: 1, resumable: false, dur: 59 },
    { id: '20251009-120411-5be1c8', first: 'Add the orders table migration', model: 'anthropic/claude-sonnet-5-5', cost: 0.09, mb: 3.4, ageS: 85 * D, agents: 2, resumable: false, dur: 275 },
    { id: '20250930-101830-c64f02', first: 'Explain the checkpoint format', model: 'anthropic/claude-haiku-5-5', cost: 0.01, mb: 0.3, ageS: 94 * D, agents: 1, resumable: false, dur: 41 },
    { id: '20250921-175544-18d9ea', first: 'Prototype the cart page', model: 'heimdall/demo-model', cost: 0.1, mb: 5.8, ageS: 103 * D, agents: 3, resumable: false, dur: 330 },
    { id: '20250912-093316-b7a65d', first: 'Fix the flaky TestListItems', model: 'anthropic/claude-sonnet-5-5', cost: 0.05, mb: 1.5, ageS: 112 * D, agents: 1, resumable: false, dur: 120 },
    { id: '20250904-141902-0e3c91', first: 'Choose a router: net/http or chi?', model: 'anthropic/claude-opus-5-5', cost: 0.22, mb: 2.2, ageS: 120 * D, agents: 1, resumable: false, dur: 150 },
    { id: '20250830-160227-2d9e6f', first: 'Bootstrap the repo: go.mod, Makefile, AGENTS.md', model: 'anthropic/claude-sonnet-5-5', cost: 0.07, mb: 1.0, ageS: 125 * D, agents: 1, resumable: false, dur: 98 },
  ];

  /* ---- models (SAMPLE prices; real ids only for the four anthropic ones; in:null = price unknown) ---- */
  const M = (ref, ctx, i, o, c, tools, reasoning, o2) => Object.assign({ ref, ctx, in: i, out: o, cached: c, tools, reasoning, fav: false, sample: true }, o2 || {});
  const MODELS = [
    M('anthropic/claude-fable-5-1', 1000000, 8.0, 40.0, 0.8, true, true, { fav: true }),
    M('anthropic/claude-opus-5-5', 500000, 5.0, 25.0, 0.5, true, true),
    M('anthropic/claude-sonnet-5-5', 400000, 3.0, 15.0, 0.3, true, true, { fav: true }),
    M('anthropic/claude-haiku-5-5', 300000, 1.0, 5.0, 0.1, true, true, { fav: true }),
    M('heimdall/demo-model', 64000, 1.0, 4.0, 0.1, true, false),
    M('heimdall/demo-large', 128000, 2.0, 8.0, 0.2, true, true),
    M('heimdall/demo-small', 32000, 0.2, 0.8, 0.02, true, false),
    M('openrouter/sample-coder-70b', 128000, 0.6, 2.4, null, true, false),
    M('openrouter/sample-reasoner', 200000, 1.8, 7.2, null, true, true),
    M('openrouter/sample-mini', 64000, 0.1, 0.4, null, false, false),
    M('openrouter/sample-vision', 128000, null, null, null, true, false),
    M('openai/sample-gpt-a', 400000, 1.25, 10.0, 0.125, true, true),
    M('openai/sample-gpt-b-mini', 128000, 0.25, 2.0, 0.025, true, true),
    M('openai/sample-gpt-c-nano', 64000, 0.05, 0.4, 0.005, false, false),
    M('chatgpt/sample-plan-a', 200000, null, null, null, true, true),
    M('chatgpt/sample-plan-b', 128000, null, null, null, true, false),
    M('local/qwen-coder-32b-awq', 32768, null, null, null, true, false),
    M('local/sample-llama-70b', 16384, null, null, null, false, false),
  ];
  const PROVIDERS = [
    { id: 'heimdall', name: 'Heimdall', base: 'https://heimdall.sample/v1', key: 'env', env: 'HEIMDALL_API_KEY', state: 'key set in the environment', note: 'the default provider' },
    { id: 'anthropic', name: 'Anthropic', base: 'https://api.anthropic.com', key: 'env', env: 'ANTHROPIC_API_KEY', state: 'key set in the environment', note: 'prefix caching with cache_control breakpoints' },
    { id: 'openrouter', name: 'OpenRouter', base: 'https://openrouter.ai/api/v1', key: 'none', env: 'OPENROUTER_API_KEY', state: 'no key', note: 'prices and cache support vary per upstream' },
    { id: 'openai', name: 'OpenAI', base: 'https://api.openai.com/v1', key: 'none', env: 'OPENAI_API_KEY', state: 'no key', note: 'automatic prefix caching' },
    { id: 'chatgpt', name: 'ChatGPT plan', base: 'https://chatgpt.sample/backend', key: 'none', env: '', state: 'not signed in', note: 'sign in from the terminal' },
    { id: 'local', name: 'local server', base: 'http://127.0.0.1:8000/v1', key: 'none', env: '', state: 'a self-hosted vLLM / SGLang server (token ids captured)', note: 'price unknown; no key' },
  ];
  const ROLE_MODELS = { manager: 'anthropic/claude-sonnet-5-5', backend: 'heimdall/demo-model', frontend: 'heimdall/demo-model', fullstack: 'heimdall/demo-model', tester: 'heimdall/demo-model', reviewer: 'heimdall/demo-model', scout: 'heimdall/demo-model', docs: 'heimdall/demo-model', mailman: '' };

  /* ---- permissions: modes (REAL names) and the rules in force with their origin ---- */
  const MODES = [
    { id: 'default', desc: 'reads and read-only commands go through; everything else asks', danger: 0 },
    { id: 'accept-edits', desc: 'also edits inside the project and build and test commands; ask and deny rules still win', danger: 0 },
    { id: 'plan', desc: 'read-only: writes, network and commands that are not provably read-only are refused', danger: 0 },
    { id: 'bypass', desc: 'no questions, except about the very dangerous (sudo, deleting the workspace, a forced push, disk tools, shutdown); deny rules still apply', danger: 1 },
    { id: 'yolo', desc: 'asks nothing at all, the very dangerous included; deny rules and guarded paths still refuse. For sandboxes only', danger: 2 },
  ];
  const RULES = [
    { effect: 'deny', rule: './.env', origin: 'built-in protection' },
    { effect: 'deny', rule: './secrets/**', origin: 'project config' },
    { effect: 'ask', rule: 'Bash(git push:*)', origin: 'project config' },
    { effect: 'allow', rule: 'Read', origin: 'built-in protection' },
  ];
  const TESTS_PRESET = ['Bash(go test:*)', 'Bash(go vet:*)', 'Bash(make test:*)', 'Bash(npm test:*)', 'Bash(npx vitest:*)', 'Bash(pytest:*)', 'Bash(cargo test:*)'];

  const TRUST_FILES = [['AGENTS.md', '9f3c1a2e'], ['.sleipnir/config.json', '5be2d07c'], ['.sleipnir/skills/go-review/SKILL.md', 'a710c4f1']];
  const MCP = [
    { name: 'fs-docs', origin: 'user config', transport: 'stdio', state: 'running', tools: ['read_doc', 'list_docs', 'search_docs', 'outline', 'headings', 'links', 'anchors', 'toc', 'find_refs', 'lint', 'diff_doc', 'stat', 'tree', 'grep_docs'] },
    { name: 'issues', origin: 'project .mcp.json', transport: 'http', state: 'needs approval', tools: ['list_issues', 'get_issue', 'comment', 'close', 'label'] },
    { name: 'metrics', origin: 'user config', transport: 'http', state: 'failed: reconnect refused (connection reset)', tools: [] },
    { name: 'browser', origin: 'user config', transport: 'stdio', state: 'off', tools: ['open', 'screenshot'] },
  ];
  const SKILLS = [{ name: 'go-review', desc: 'review a Go diff for races and error handling', from: '.sleipnir/skills (trusted)' }, { name: 'release-notes', desc: 'draft release notes from merged tasks', from: '.sleipnir/skills (trusted)' }, { name: 'shop-seed', desc: 'regenerate seed/items.json', from: '~/.sleipnir/skills (you only)' }];

  const SLASH = [
    ['conversation', [['/goal', 'TEXT', 'work until it is met, judged on evidence (/goal: status)'], ['/new', '', 'start again, empty (same model and mode)'], ['/clear', '', 'the same as /new'], ['/resume', '[id]', 'pick an earlier session from a menu, and continue it'], ['/sessions', '', 'the newest sessions'], ['/compact', '[focus]', 'fold the older thread now; focus says what to keep in view'], ['/rewind', '[id]', 'list checkpoints, or restore files to before a turn'], ['/diff', '[id]', 'what changed in the newest checkpoint, or in <id>'], ['/exit', '', 'quit (Ctrl-D, or Ctrl-C twice at the prompt)']]],
    ['model and cost', [['/model', '[ref]', 'pick a model from a menu; a team starts again on it'], ['/effort', '[level]', 'show or change reasoning effort (closest supported level)'], ['/fav', '[ref]', 'star a model, or unstar it; starred ones lead in /model'], ['/login', '[provider]', 'add a key, or sign in with ChatGPT (the chat comes back)'], ['/budget', '[usd|off]', 'the dollar budget for the turns from now on'], ['/cost', '', 'tokens, cost and cache hit ratio so far'], ['/stats', '', 'the stats page (ctrl+t): cost, cache, savings, layers'], ['/context', '', 'what each layer of the prompt weighs'], ['/status', '', 'model, mode, session, budget and cost at a glance']]],
    ['permissions', [['/mode', '<m>', 'default | accept-edits | plan | bypass | yolo'], ['/plan', '[prompt]', 'switch to read-only mode; with a prompt, start planning it'], ['/allow', '<rule>', 'allow, this session, what would ask: tests, Bash(go test:*)'], ['/permissions', '', 'the mode and the rules in force'], ['/trust', '', "this project's own instructions and settings, and your yes"]]],
    ['a team, and the program', [['/roles', '[role=m]', 'which model each role runs on; change one (restarts)'], ['/swarm', '<n> [flags]', 'start again as a manager and n workers'], ['/restart', '[flags]', 'start again with other flags: --no-mcp, --cwd DIR, ...'], ['/agents', '', "the team's agents and tasks (ctrl+g: cockpit)"], ['/steer', 'TEXT', 'tell the running turn something, without stopping it'], ['/verbose', '[on|off]', 'notices and tool errors'], ['/anim', '[on|off]', 'motion'], ['/cwd', '', 'the directory this session works in']]],
    ['what the model knows', [['/recon', '', 'the project map in the shared layer'], ['/skills', '', 'the skills the model can load'], ['/mcp', '', 'tool servers: state and tools (/mcp reconnect NAME)'], ['/help', '', 'this text, and your custom commands and skills']]],
  ];
  const SHORTCUTS = [
    ['Enter', 'send'], ['\\ at line end · alt+enter · ctrl+j', 'newline'], ['↑ ↓ · ctrl+r', 'history, search history'], ['/', 'command palette (also ctrl+k)'], ['@', 'path completion'],
    ['shift+tab', 'permission mode: default → accept-edits → plan (never bypass, never yolo)'], ['ctrl+t · alt+t', 'stats page (Cache)'], ['ctrl+g · alt+g', 'team cockpit'], ['ctrl+o', 'expand collapsed tool output'],
    ['esc', 'release a hold, close an overlay, answer 3 to a question, then interrupt the turn'], ['ctrl+c', 'discard the line; twice quits'], ['o c m b r s', 'Cockpit, Cache, Mail, Board, Replay, Sessions'], ['space', 'over the chat: pin the hold; elsewhere: pause the view'],
    ['← → · + −', 'seek 10 s · speed (replay)'], ['1 2 3', 'answer a question (after the keyboard has been quiet for a moment)'], ['?', 'this list'],
  ];
  const CONFIG = [
    ['models.default', 'anthropic/claude-sonnet-5-5', '~/.sleipnir/config.json'], ['permissions.mode', 'default', 'defaults'], ['swarm.max_workers', '12', 'defaults'], ['swarm.isolation', 'worktree', '.sleipnir/config.json (trusted)'],
    ['swarm.verify', 'go test {dirs}', '.sleipnir/config.json (trusted)'], ['cache.warm_seconds', '300', 'defaults'], ['cache.breakpoints', '2', 'defaults'], ['budget.usd', '5.00', 'flag --budget-usd'], ['mcp.enabled', 'true', 'defaults'],
  ];
  const SCHEDULE = [
    { name: 'nightly docs sweep', cron: '0 3 * * *', goal: 'Sweep docs/: fix stale command names and broken relative links', cwd: '~/projects/shop', model: 'anthropic/claude-sonnet-5-5', mode: 'accept-edits', budget: 1, next: 'tomorrow 03:00', last: 'today 03:00 · running', status: 'running' },
    { name: 'weekly dependency audit', cron: '0 6 * * 1', goal: 'List outdated Go modules and open a plan', cwd: '~/projects/shop', model: 'heimdall/demo-model', mode: 'plan', budget: 0.5, next: 'Mon 06:00', last: 'Mon 06:00 · ok', status: 'ok' },
    { name: 'orders smoke', cron: '*/30 * * * *', goal: 'Run go test ./orders/... and report', cwd: '~/projects/orders-api', model: 'heimdall/demo-model', mode: 'default', budget: 0.2, next: '03:30', last: '03:00 · ok', status: 'ok' },
    { name: 'seed refresh', cron: '0 1 1 * *', goal: 'Regenerate seed/items.json from the CSV', cwd: '~/projects/shop', model: 'heimdall/demo-model', mode: 'accept-edits', budget: 0.3, next: 'Nov 1 01:00', last: 'Oct 1 01:00 · failed (verify)', status: 'failed' },
  ];

  /* ---- a minimal CLI spec and outputs, used only when the data pack is not loaded ---- */
  const F = (name, arg, def, desc, rep) => ({ name, arg: arg || 'bool', default: def == null ? null : def, repeatable: !!rep, desc });
  const SPEC = { generatedFrom: 'built-in (data pack not loaded)', commands: [
    { path: ['sessions'], usage: 'sleipnir sessions [flags]', summary: 'list recorded sessions', positional: [], flags: [F('dir', 'string', '<state>/sessions', 'the directory that holds the sessions'), F('limit', 'int', '20', 'how many to list')] },
    { path: ['sessions', 'prune'], usage: 'sleipnir sessions prune [flags]', summary: 'Deletes recorded sessions that are old: the event log, the blobs it points at and the checkpoints of each.', positional: [], flags: [F('older-than', 'string', '30d', 'delete sessions whose newest file is older than this (30d, 36h, 2w; 0 is any age)'), F('keep', 'int', '20', 'never delete the newest N sessions, whatever their age'), F('yes', 'bool', null, 'delete them (without it, the sessions that would go are listed and nothing is deleted)')] },
    { path: ['trust'], usage: 'sleipnir trust [add|forget|list] [DIR]', summary: 'say yes (or forget it) to the files a repository brings', positional: [{ name: 'ACTION', required: false }, { name: 'DIR', required: false }], flags: [F('user', 'bool', null, 'act on ~/.sleipnir instead of the project')] },
    { path: ['mcp'], usage: 'sleipnir mcp [list|approve|revoke|test] [NAME]', summary: 'tool servers: list, approve, revoke and test', positional: [{ name: 'ACTION', required: false }, { name: 'NAME', required: false }], flags: [F('json', 'bool', null, 'print as JSON')] },
    { path: ['doctor'], usage: 'sleipnir doctor [flags]', summary: 'probe an endpoint: streaming, tools, prefix-cache behaviour, warm-up needs', positional: [], flags: [F('model', 'string', null, 'model to probe: provider/model'), F('provider', 'string', null, 'heimdall | openrouter | openai | custom'), F('deep', 'bool', null, 'also measure cache granularity, minimum prefix and warm-up needs (more requests)'), F('json', 'bool', null, 'print the report as JSON'), F('base-url', 'string', null, 'API base URL (overrides the provider default)')] },
    { path: ['sim'], usage: 'sleipnir sim [flags]', summary: 'simulate cache policies: a model, not a benchmark', positional: [], flags: [F('mode', 'string', 'compare', 'compare | scenarios | pins | agents'), F('json', 'bool', null, 'print as JSON'), F('agents', 'int', '8', 'workers in the simulated team'), F('turns', 'int', '12', 'turns per agent')] },
    { path: ['models'], usage: 'sleipnir models [words...] [flags]', summary: 'list models and prices from a marketplace catalogue', positional: [{ name: 'WORDS', required: false }], flags: [F('tools', 'bool', null, 'only models that call tools'), F('reasoning', 'bool', null, 'only reasoning models'), F('max-price', 'string', null, 'maximum input $/M'), F('min-context', 'int', null, 'minimum context tokens'), F('all', 'bool', null, 'include non-chat models')] },
    { path: ['config'], usage: 'sleipnir config [flags]', summary: 'show the effective configuration and where each value came from', positional: [], flags: [F('json', 'bool', null, 'print as JSON'), F('trust-project', 'bool', null, 'apply the sensitive keys of the project config')] },
    { path: ['recon'], usage: 'sleipnir recon [flags]', summary: 'print the project survey that seeds the shared prompt layer', positional: [], flags: [F('cwd', 'string', '.', 'the directory to survey')] },
    { path: ['init'], usage: 'sleipnir init [flags]', summary: 'write a starter .sleipnir/config.json and AGENTS.md', positional: [], flags: [F('user', 'bool', null, 'write ~/.sleipnir/config.json instead'), F('model', 'string', null, 'models.default to write')] },
  ] };

  SL.FX = { ROLES, ROLE_ORDER, PRICES, ROSTER, NREQ, HITSERIES, LAYERS, G5TOK, GOAL, PLAN, TASKS, DEPS, CKPTS, CODE, STREAM_TEST, TREE_SHOP, TREE_ORDERS, ORDERS_CODE, LIVE, RECORDED, MODELS, PROVIDERS, ROLE_MODELS, MODES, RULES, TESTS_PRESET, TRUST_FILES, MCP, SKILLS, SLASH, SHORTCUTS, CONFIG, SCHEDULE, SPEC };
})(SL);
