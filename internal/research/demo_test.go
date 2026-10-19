package research

import (
	"fmt"
	"os"
	"strings"
	"testing"

	webtool "github.com/anemos-labs/sleipnir/internal/tools/web"
	"github.com/anemos-labs/sleipnir/internal/web"
)

// TestDemoWiki builds a small wiki from fixture sources (example domains, invented text) into $RESEARCH_DEMO_DIR, to look at:
//
//	RESEARCH_DEMO_DIR=/tmp/demo go test ./internal/research -run TestDemoWiki
func TestDemoWiki(t *testing.T) {
	dir := os.Getenv("RESEARCH_DEMO_DIR")
	if dir == "" {
		t.Skip("RESEARCH_DEMO_DIR is not set")
	}
	_ = os.RemoveAll(dir)
	st := NewStore(dir)
	r, err := st.Create("Which sync engine should a local-first note app use in 2026?", "quick", ModeKickoff, "a local-first notes app with realtime sync")
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeReader{pages: map[string]*webtool.Page{}}
	tool := NewTool(st, fr)
	tool.Fonts = web.Fonts()
	must(t, tool, "mgr-1", map[string]any{"action": "plan", "scope": "CRDT and server-authoritative sync engines usable from a TypeScript client; hosted services are out of scope",
		"perspectives": []map[string]string{
			{"name": "App developer", "concerns": "how much code the integration takes, and how offline edits merge"},
			{"name": "Maintainer", "concerns": "project health, license, release cadence"},
			{"name": "Skeptic", "concerns": "document growth, merge surprises, vendor lock-in"}},
		"tracks": []map[string]any{
			{"title": "Merge models", "questions": []string{"How do CRDTs merge concurrent edits?", "What does a server-authoritative model give up?", "How do they handle rich text?"}, "look": "papers, project docs"},
			{"title": "Engines in practice", "questions": []string{"Which engines are maintained?", "How large do documents grow?", "What do adopters report?"}, "look": "repositories, issue trackers, talks"},
			{"title": "Integration", "questions": []string{"What does a minimal client look like?", "How is storage persisted?", "How is auth done?"}, "look": "official examples, source code"}},
		"pages": []map[string]any{
			{"id": "index", "title": "Local-first sync engines", "summary": "Which engine to build a local-first note app on, and why."},
			{"id": "merge-models", "title": "Merge models", "summary": "CRDTs, operational transforms and server authority compared.", "category": "Concepts"},
			{"id": "engines", "title": "Engines compared", "summary": "Five maintained engines side by side.", "category": "Landscape"},
			{"id": "document-growth", "title": "Document growth", "summary": "Why history makes documents grow, and what compaction costs.", "category": "Concepts"},
			{"id": "context", "title": "Build context", "summary": "What the build should reuse, choose and avoid.", "category": "For the build"}}})
	type src struct{ url, title, kind, track, text string }
	srcs := []src{
		{"https://docs.yjs-example.dev/merging", "Merging concurrent edits", "docs", "T1", "Concurrent insertions at the same position are ordered by the client id of their authors, so every replica converges to the same document without a server."},
		{"https://papers.example.org/crdt-survey-2024", "A survey of sequence CRDTs", "paper", "T1", "Sequence CRDTs trade metadata for availability: every character keeps an identifier, and deleted characters are kept as tombstones until compaction."},
		{"https://blog.example.net/server-authority", "Why we chose server authority", "blog", "T1", "A server-authoritative model rebases pending local changes on the confirmed state, which keeps the document small but requires a server to decide the order of edits."},
		{"https://github.example.com/automerge-like/engine", "automerge-like/engine", "repo", "T2", "The engine stores the full change history in a compressed columnar format; a typical text document of 100,000 edits takes about 300 KB."},
		{"https://github.example.com/syncline/syncline", "syncline/syncline", "repo", "T2", "Releases follow a six-week cadence and every release since 1.0 has kept the wire format backward compatible."},
		{"https://forum.example.org/t/document-size", "Document size keeps growing", "forum", "T2", "After a year of daily use our largest notebook reached 40 MB, almost all of it deleted text kept for merging."},
		{"https://talks.example.com/local-first-2025", "Local-first in production (talk)", "talk", "T2", "We shipped offline editing to two million users; the hard part was not merging text but migrating the schema of documents that were offline for months."},
		{"https://docs.syncline-example.dev/quickstart", "Quickstart", "docs", "T3", "A client opens a document by id, subscribes to changes, and persists updates to IndexedDB with the provided storage adapter."},
		{"https://docs.syncline-example.dev/auth", "Authentication", "docs", "T3", "The sync server accepts a signed token per connection and checks document permissions on every update, not only when the socket opens."},
		{"https://raw.example.com/syncline/client/src/store.ts", "client/src/store.ts", "code", "T3", "// Persist one update atomically.\nexport async function persist(doc: Doc, db: IDBDatabase) {\n  const tx = db.transaction('updates', 'readwrite')\n  tx.objectStore('updates').add(encodeUpdate(doc))\n  await tx.done\n}"},
		{"https://spec.example.org/sync-protocol", "Sync protocol specification", "spec", "T3", "A peer first sends its state vector; the other peer answers with only the updates the vector does not cover, so reconnecting after a long offline period sends one message each way."},
		{"https://news.example.com/2026/sync-engines", "The state of sync engines", "news", "T2", "Of the twelve engines announced since 2021, five had a release in the last six months."},
		{"https://bench.example.dev/sync-2026", "Sync engine benchmarks 2026", "data", "T2", "Applying 10,000 concurrent text edits took 41 ms in the fastest engine and 2.3 s in the slowest, on the same laptop."},
		{"https://reference.example.org/wiki/Operational_transformation", "Operational transformation", "reference", "T1", "Operational transformation needs a central server or a complex transformation property to guarantee convergence for every pair of concurrent operations."},
		{"https://blog.example.net/rich-text-crdt", "Rich text on a CRDT", "blog", "T1", "Formatting spans are stored as marks anchored to characters, so a bold range keeps its meaning when text is inserted inside it concurrently."},
	}
	for _, s := range srcs {
		fr.pages[s.url] = &webtool.Page{Title: s.title, Text: s.text}
		must(t, tool, "sc-"+strings.ToLower(s.track), map[string]any{"action": "cite", "url": s.url, "title": s.title, "kind": s.kind, "track": s.track, "quotes": []string{firstLine(s.text)}})
	}
	findings := []struct {
		claim, track, conf string
		srcs               []string
	}{
		{"CRDT engines converge without a server by ordering concurrent insertions by author id", "T1", "high", []string{"S1", "S2"}},
		{"Sequence CRDTs keep deleted characters as tombstones until compaction, so documents grow with their history", "T1", "high", []string{"S2", "S6"}},
		{"A server-authoritative model keeps documents small but needs a server to order edits", "T1", "medium", []string{"S3", "S14"}},
		{"Rich text on a CRDT anchors formatting marks to characters so concurrent inserts keep formatting", "T1", "medium", []string{"S15"}},
		{"A columnar history format keeps a 100,000-edit text document near 300 KB", "T2", "medium", []string{"S4"}},
		{"Users report notebooks of 40 MB after a year, mostly deleted text kept for merging", "T2", "low", []string{"S6"}},
		{"Schema migration of long-offline documents was the hardest production problem in one large deployment", "T2", "medium", []string{"S7"}},
		{"Only five of twelve engines announced since 2021 had a release in the last six months", "T2", "medium", []string{"S12", "S5"}},
		{"The fastest engine applied 10,000 concurrent edits in 41 ms, the slowest in 2.3 s", "T2", "medium", []string{"S13"}},
		{"A minimal client opens a document, subscribes to changes and persists updates to IndexedDB", "T3", "high", []string{"S8", "S10"}},
		{"Permissions are checked on every update, not only at connection time", "T3", "high", []string{"S9"}},
		{"State vectors make a reconnect after a long offline period a single exchange", "T3", "high", []string{"S11"}},
	}
	for i := 0; len(r.Findings) < r.DepthInfo().MinFindings; i++ {
		f := findings[i%len(findings)]
		claim := f.claim
		if i >= len(findings) {
			claim = fmt.Sprintf("%s (confirmed again in round %d)", f.claim, i/len(findings)+1)
		}
		must(t, tool, "sc-"+strings.ToLower(f.track), map[string]any{"action": "note", "claim": claim, "track": f.track, "confidence": f.conf, "sources": f.srcs})
		r, _ = Load(dir, r.Slug)
	}
	must(t, tool, "sc-t3", map[string]any{"action": "snippet", "url": "https://raw.example.com/syncline/client/src/store.ts", "project": "syncline/syncline", "language": "ts",
		"license": "MIT", "shows": "persisting each update in one IndexedDB transaction, so a crash never stores half an update", "track": "T3",
		"code": "export async function persist(doc: Doc, db: IDBDatabase) {\n  const tx = db.transaction('updates', 'readwrite')\n  tx.objectStore('updates').add(encodeUpdate(doc))\n  await tx.done\n}"})
	para := func(s string) string { return s + "\n\n" }
	pages := map[string]string{
		"index": para("A local-first note app should start on a **CRDT engine with a compact history format** and a state-vector sync protocol, and plan for document compaction from the first release [S1] [S4] [S11]. Server-authoritative engines keep documents smaller but give up editing without a server, which is the point of a local-first app [S3].") +
			"## Key findings\n\n- Every maintained CRDT engine converges without a server; they differ in history size and speed [S2] [S13]. See [[merge-models]].\n- History is the cost: deleted text stays in the document until it is compacted [S2] [S6]. See [[document-growth]].\n- Few engines are maintained: five of twelve released in the last six months [S12]. See [[engines]].\n\n" +
			"> [!TIP]\n> The [[context|build context]] page lists what the build should reuse and avoid.\n",
		"merge-models": "## CRDTs\n\n" + para("A CRDT orders concurrent insertions deterministically, by the author's client id, so every replica reaches the same text without asking a server [S1]. The price is metadata: each character keeps an identifier and deleted ones stay as tombstones [S2].") +
			"## Operational transformation\n\n" + para("Operational transformation transforms concurrent operations against each other; guaranteeing convergence for every pair needs a central server or a hard property to prove [S14]. That is why most new engines chose CRDTs or plain server authority [S3].") +
			"## Server authority\n\n" + para("A server-authoritative engine rebases pending local edits on the confirmed state. Documents stay small, but a server decides the order of edits, so two offline devices cannot merge without it [S3].") +
			"## Rich text\n\n" + para("Formatting is stored as marks anchored to characters: a bold range keeps its meaning when someone types inside it concurrently [S15]. See [[engines]] for which engines implement marks, and [[document-growth]] for what the metadata costs."),
		"engines": "## Side by side\n\n| Engine | Model | History format | 10k edits |\n|---|---|---|---:|\n| engine A | CRDT | columnar, compressed | 41 ms |\n| engine B | CRDT | append log | 2.3 s |\n| syncline | CRDT | state vectors | n/a |\n\n" +
			para("The benchmark ran every engine on one laptop: the fastest applied 10,000 concurrent edits in 41 ms and the slowest in 2.3 s [S13]. A compressed columnar history keeps a 100,000-edit document near 300 KB [S4].") +
			"## Project health\n\n" + para("Of twelve engines announced since 2021, five had a release in the last six months [S12]. Syncline releases every six weeks and has kept its wire format compatible since 1.0 [S5]. Read [[merge-models]] for how they merge, and [[context]] for the choice."),
		"document-growth": "## Why documents grow\n\n" + para("A sequence CRDT keeps an identifier per character and keeps deleted characters as tombstones until compaction [S2]. In daily use that adds up: one user's notebook reached 40 MB after a year, almost all of it deleted text [S6].") +
			"## Keeping it small\n\n" + para("A compressed columnar history keeps a 100,000-edit document near 300 KB [S4]. Compaction has a cost of its own: a peer that was offline longer than the compaction horizon must fetch the whole document again, which the state-vector exchange makes one round trip [S11].") +
			para("The hardest production problem one large deployment reported was not merging but migrating the schema of documents offline for months [S7]. See [[merge-models]] and [[engines]]."),
		"context": "## Choose\n\n- A CRDT engine with a compressed history and a state-vector protocol, such as syncline: maintained, compatible since 1.0, MIT [S5] [S11].\n- Persist each update in its own IndexedDB transaction:\n\n{{C1}}\n\n" +
			"## Reuse\n\n" + para("Use the engine's storage adapter rather than writing one; the quickstart opens a document, subscribes and persists in a few lines [S8]. Check permissions on every update on the server, not only on connect [S9].") +
			"## Avoid\n\n" + para("Do not ship without compaction: notebooks grow to tens of megabytes of deleted text [S6] [S2]. Version the document schema from day one; migrating long-offline documents was the hardest problem in production [S7]. Background: [[document-growth]], [[engines]], [[merge-models]]."),
	}
	for _, id := range []string{"index", "merge-models", "engines", "document-growth", "context"} {
		must(t, tool, "dc-1", map[string]any{"action": "write_page", "page": id, "body": pages[id]})
		must(t, tool, "rv-1", map[string]any{"action": "review_page", "page": id, "ok": true})
	}
	must(t, tool, "mgr-1", map[string]any{"action": "gap", "text": "How do engines migrate document schemas without breaking offline peers?", "track": "T2"})
	out := must(t, tool, "mgr-1", map[string]any{"action": "build"})
	t.Log(out)
	t.Log(must(t, tool, "mgr-1", map[string]any{"action": "status"}))
}

// firstLine is the first line of s that is long enough to quote.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if len(l) >= minQuoteRunes {
			return l
		}
	}
	return s
}
