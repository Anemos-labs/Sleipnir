package webtest

import (
	"github.com/anemos-labs/sleipnir/internal/scan"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// ScanSlug names the canned scan.
const ScanSlug = "shop-ab12cd3"

// scanRun is the canned scan of the shop: a survey of its API and its cart screen with every shard mapped, most of them
// hunted, and four leads (two confirmed, one rejected, one still with its verifier). It is measured by the scan package
// itself, so the page shows the phase, counts and severities a real scan of this ledger would.
func scanRun() *scan.Run {
	cite := func(file string, line int, quote string) scan.Cite {
		return scan.Cite{File: file, Line: line, Quote: quote}
	}
	step := func(file string, line int, quote, note string) scan.Step {
		return scan.Step{Cite: cite(file, line, quote), Note: note}
	}
	verdict := func(by string, confirm bool, note string) scan.Verdict {
		v := scan.Verdict{By: by, Role: scan.RoleVerifier, Confirm: confirm, Note: note}
		if !confirm {
			v.Reason = "guarded"
		}
		return v
	}
	return &scan.Run{
		Slug: ScanSlug, Name: "shop", Commit: "ab12cd3e9f0471c2a6b8d5e3f1a09b7c6d4e2f10", Depth: "survey", Created: "2026-10-09T21:12:40Z", Version: 31,
		Inventory: scan.Inventory{Files: []scan.File{
			{Path: "api/catalog/items.go", Lang: "go", Lines: 212, Funcs: 9, Complexity: 31},
			{Path: "api/catalog/seed.go", Lang: "go", Lines: 88, Funcs: 3, Complexity: 9},
			{Path: "api/cart/cart.go", Lang: "go", Lines: 174, Funcs: 8, Complexity: 27},
			{Path: "api/server.go", Lang: "go", Lines: 96, Funcs: 4, Complexity: 11},
			{Path: "web/cart/Cart.tsx", Lang: "tsx", Lines: 143, Funcs: 6, Complexity: 14},
			{Path: "web/lib/api.ts", Lang: "ts", Lines: 71, Funcs: 5, Complexity: 8},
		}},
		Shards: []scan.Shard{
			{ID: "S1", Files: []string{"api/catalog/items.go", "api/catalog/seed.go"}, Funcs: 12, Lines: 300, Complexity: 40,
				Mapped: "The catalogue: list and get handlers over a seed file read at start; paging comes from the query string.", MappedBy: "sc-1",
				Hunted: "Followed page and size from the handler to the slice and the item id to the lookup.", HuntedBy: "be-1"},
			{ID: "S2", Files: []string{"api/cart/cart.go"}, Funcs: 8, Lines: 174, Complexity: 27,
				Mapped: "The cart: add, remove and read, keyed by a cart id the client sends.", MappedBy: "sc-1",
				Hunted: "Followed the cart id and the quantity from the handlers to the store.", HuntedBy: "be-1"},
			{ID: "S3", Files: []string{"api/server.go"}, Funcs: 4, Lines: 96, Complexity: 11,
				Mapped: "The server: routes, CORS and the error writer.", MappedBy: "sc-1", Hunted: "Read the CORS settings and the error path.", HuntedBy: "te-1"},
			{ID: "S4", Files: []string{"web/cart/Cart.tsx", "web/lib/api.ts"}, Funcs: 11, Lines: 214, Complexity: 22,
				Mapped: "The cart screen and its API client.", MappedBy: "sc-1", Claimed: "fe-1"},
		},
		Points: []scan.Point{
			{ID: "P1", Lane: scan.LaneEntry, Title: "GET /items", Symbol: "ListItems", Cite: cite("api/catalog/items.go", 41, "func ListItems(w http.ResponseWriter, r *http.Request) {"), Shard: "S1", By: "sc-1"},
			{ID: "P2", Lane: scan.LaneEntry, Title: "POST /cart/{id}/items", Symbol: "AddItem", Cite: cite("api/cart/cart.go", 58, "func AddItem(w http.ResponseWriter, r *http.Request) {"), Shard: "S2", By: "sc-1"},
			{ID: "P3", Lane: scan.LaneBoundary, Title: "page and size parsed from the query", Cite: cite("api/catalog/items.go", 47, "size, _ := strconv.Atoi(q.Get(\"size\"))"), Shard: "S1", By: "sc-1"},
			{ID: "P4", Lane: scan.LaneAccess, Title: "a cart is read by the id in the path", Cite: cite("api/cart/cart.go", 31, "c := store.Get(r.PathValue(\"id\"))"), Shard: "S2", By: "sc-1"},
			{ID: "P5", Lane: scan.LaneSink, Title: "the page of items is allocated", Cite: cite("api/catalog/items.go", 63, "out := make([]Item, 0, size)"), Shard: "S1", By: "sc-1"},
			{ID: "P6", Lane: scan.LaneConfig, Title: "CORS allows every origin", Cite: cite("api/server.go", 22, "w.Header().Set(\"Access-Control-Allow-Origin\", \"*\")"), Shard: "S3", By: "sc-1"},
		},
		Edges: []scan.Edge{
			{ID: "E1", From: "P1", To: "P3", Relation: "calls"},
			{ID: "E2", From: "P3", To: "P5", Relation: "writes"},
			{ID: "E3", From: "P2", To: "P4", Relation: "reads"},
		},
		Leads: []scan.Lead{
			{ID: "L1", Title: "The page size is not capped, so one request allocates what it asks for", Category: "dos",
				Claim: "size comes from the query string and reaches make() with no upper bound.",
				Trace: []scan.Step{
					step("api/catalog/items.go", 47, "size, _ := strconv.Atoi(q.Get(\"size\"))", "the size is read from the request"),
					step("api/catalog/items.go", 63, "out := make([]Item, 0, size)", "and is the capacity of the page"),
				},
				CVSS: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H", Reach: scan.ReachExternal, Exploit: scan.ExploitTrivial,
				Points: []string{"P1", "P3", "P5"}, Fix: "Cap size at 50, as the API's notes say, before the allocation.", Shard: "S1", By: "be-1",
				Verdicts: []scan.Verdict{verdict("te-1", true, "No bound between the query and make(); a size of 1e9 is accepted.")}},
			{ID: "L2", Title: "Any client can read or change a cart by guessing its id", Category: "idor",
				Claim: "The cart id in the path is the only thing that selects a cart; nothing ties it to the caller.",
				Trace: []scan.Step{
					step("api/cart/cart.go", 31, "c := store.Get(r.PathValue(\"id\"))", "the cart is looked up by the id in the path"),
					step("api/cart/cart.go", 58, "func AddItem(w http.ResponseWriter, r *http.Request) {", "and the same lookup serves the write"),
				},
				CVSS: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:L/A:N", Reach: scan.ReachExternal, Exploit: scan.ExploitModerate,
				Points: []string{"P2", "P4"}, Fix: "Issue the cart id as an unguessable token, or bind the cart to a session.", Shard: "S2", By: "be-1",
				Verdicts: []scan.Verdict{verdict("te-1", true, "Ids are sequential integers; no session or owner is checked.")}},
			{ID: "L3", Title: "CORS allows every origin on the API", Category: "config",
				Claim: "Any site can call the API from a visitor's browser.",
				Trace: []scan.Step{step("api/server.go", 22, "w.Header().Set(\"Access-Control-Allow-Origin\", \"*\")", "every origin is allowed")},
				CVSS:  "CVSS:3.1/AV:N/AC:L/PR:N/UI:R/S:U/C:L/I:N/A:N", Reach: scan.ReachExternal, Exploit: scan.ExploitModerate,
				Points: []string{"P6"}, Shard: "S3", By: "te-1",
				Verdicts: []scan.Verdict{verdict("be-1", false, "The API has no cookies or credentials, so another origin reads only what is public.")}},
			{ID: "L4", Title: "A negative quantity lowers a cart's total", Category: "logic",
				Claim: "AddItem accepts a quantity below zero and adds it to the line.",
				Trace: []scan.Step{step("api/cart/cart.go", 66, "line.Qty += body.Qty", "the quantity is added as sent")},
				CVSS:  "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:L/A:N", Reach: scan.ReachExternal, Exploit: scan.ExploitTrivial,
				Points: []string{"P2"}, Shard: "S2", By: "be-1"},
		},
	}
}

// ScanView is what the Deep scan page lists: the scans of the project, the depths a scan can have, and whether the plugin
// and a sandbox are there.
type ScanView struct {
	Runs    []wire.ScanRun   `json:"runs"`
	Depths  []wire.ScanDepth `json:"depths"`
	On      bool             `json:"on"`
	Sandbox bool             `json:"sandbox"`
}

// Scans is the Deep scan page of the canned tab: the one canned scan, measured.
func Scans() ScanView {
	r := scanRun()
	rep := scan.Check(r)
	c := rep.Counts
	return ScanView{On: true, Depths: session.ScanDepths(), Runs: []wire.ScanRun{{Slug: r.Slug, Commit: r.Commit, Depth: r.Depth, Phase: rep.Phase,
		Created: r.Created, Team: r.Team(), Workers: 4, Problems: rep.Blocking(), Files: c.Files, Funcs: c.Funcs, Lines: c.Lines, Shards: c.Shards,
		Mapped: c.Mapped, Hunted: c.Hunted, Points: c.Points, Edges: c.Edges, Leads: c.Leads, Open: c.Open, Confirmed: c.Confirmed,
		Unproven: c.Unproven, Rejected: c.Rejected, BySeverity: c.BySeverity, Active: true}}}
}

// ScanDetail is the canned scan in full, with the fields the session's own answer has.
func ScanDetail() map[string]any {
	r := scanRun()
	rep := scan.Check(r)
	type shard struct {
		ID         string `json:"id"`
		Files      int    `json:"files"`
		First      string `json:"first"`
		Funcs      int    `json:"funcs"`
		Lines      int    `json:"lines"`
		Complexity int    `json:"complexity"`
		Claimed    string `json:"claimed,omitempty"`
		Map        string `json:"map,omitempty"`
		MappedBy   string `json:"mappedBy,omitempty"`
		Hunt       string `json:"hunt,omitempty"`
		HuntedBy   string `json:"huntedBy,omitempty"`
	}
	shards := make([]shard, 0, len(r.Shards))
	for _, sh := range r.Shards {
		shards = append(shards, shard{ID: sh.ID, Files: len(sh.Files), First: sh.Files[0], Funcs: sh.Funcs, Lines: sh.Lines, Complexity: sh.Complexity,
			Claimed: sh.Claimed, Map: sh.Mapped, MappedBy: sh.MappedBy, Hunt: sh.Hunted, HuntedBy: sh.HuntedBy})
	}
	d := r.DepthInfo()
	findings := scan.Findings(r)
	sum := scan.Summarize(findings)
	return map[string]any{"slug": r.Slug, "commit": r.Commit, "depth": r.Depth, "focus": r.Focus, "created": r.Created, "team": r.Team(), "workers": 4,
		"verifiers": d.Verifiers, "defender": d.Defender, "proveFrom": d.ProveFrom, "sandbox": false,
		"phase": rep.Phase, "counts": rep.Counts, "problems": rep.Problems, "moreProblems": 0,
		"findings": findings, "summary": sum, "headline": sum.Headline(rep.Phase == scan.PhaseDone), "intake": r.Intake(), "shards": shards,
		"points": r.Points, "edges": scan.MapDoc(r)["edges"], "reportDir": Cwd + "/.sleipnir/scans/" + r.Slug, "previous": r.Previous, "base": r.Base}
}
