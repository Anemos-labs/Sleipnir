# Deep research

The `research` plugin turns a question into a researched, cited wiki. A team plans the question from several points of
view, researches it on the web in parallel, keeps only quotes the harness found on their pages, writes pages in which
every factual paragraph cites its sources, fact-checks each page with an agent that did not write it (above quick depth), and publishes a
static site with cross-links, a search box, a page of sources, a page of evidence and a page that describes the method.

The same machinery can start a coding project: `/goal --research=DEPTH GOAL` researches how mature projects solve the
same problems before the team builds, and the research's build-context page becomes part of every agent's shared prompt.

## Starting a run

| Where | How |
|---|---|
| Terminal chat | `/research [quick\|standard\|deep] QUESTION` (default depth: standard) |
| Web page | the Research view (`g` then `w`): a question and a depth; or `/research ...` in the composer |
| Research before building | `/goal --research=DEPTH GOAL` in either interface, or *research first* beside the first goal in the web page's New session dialog, or *Research first* in its Goal sheet |

A run is a standing goal (it needs the `goal` plugin). Its spec is the run's brief, `.sleipnir/research/RUN/BRIEF.md`:
the protocol below, the source policy, the wiki's style and the milestones the judge rules on. The judge accepts a
milestone on the research tool's own reports (`research status`, `research check`), not on an agent's account of its
work.

`/research` alone lists the runs; `/research status [RUN]` reports one, `/research build [RUN]` renders its wiki,
`/research open [RUN]` gives the path of its `index.html`, and `/research export RUN DIR` renders the wiki into a
directory of the project. `sleipnir research [RUN] [--build] [--export DIR] [--json]` does the same from a shell
(`docs/CLI.md`).

## Depths

| Depth | Tracks | Gather rounds | Perspectives | Questions | Sources (sites) | Findings | Pages | Sources per page | Fact-checks per page |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| quick | 3 | 1 | 2 | 6 | 8 (4) | 12 | 2–4 | 3 | 0 |
| standard | 6 | 2 | 5 | 20 | 45 (18) | 70 | 10–16 | 6 | 1 |
| deep | 10 | 3 | 7 | 36 | 110 (40) | 170 | 19–33 | 8 | 2 |

The numbers are minimums the run must reach before its next phase opens (pages count the overview). A research-first run
also needs code snippets from mature projects: 2 from 2 projects at quick depth, 10 from 6 at standard, 20 from 10 at
deep. Pages aim at 300–700 words at quick depth, 700–1,500 at standard and 1,000–2,500 at deep.

Quick depth is meant to take minutes: it has no fact-check round. The harness still verifies every quote on its page and
every page's citations, and a review that finds issues still holds a page until it is fixed; nobody is required to review.
Research first from New session at quick depth gives the build its `context` page soon after the gather round.

## The protocol

1. **Plan.** An outline drafted from the question alone, perspectives (people who see the question differently and what
   each would ask), research tracks that do not overlap, and the wiki's pages: an `index` overview and topic pages, each
   with a summary and the tracks it draws on. A research-first run also plans a `context` page for the build team.
2. **Gather.** One researcher per track, all tracks at once. A researcher searches (`research search`; a query runs once
   per run), reads the pages behind the results, and cites each page it uses with passages copied word for word
   (`research cite`). It records each established point as a finding: one self-contained claim with its sources, a
   confidence, its track and the findings it contradicts (`research note`). A research-first run also records code worth
   learning from with its project, what it shows and its license (`research snippet`).
3. **Gap review.** Between two gather rounds, a reviewer reads the findings and records what is missing, unclear,
   contested or one-sided as gaps (`research review_round`). The next round researches them; each gap is closed with the
   findings that answer it, or as unanswerable with the reason.
4. **Write.** Writers write pages from the findings and the sources' quotes, in parallel; an editor then makes terms,
   tone and links consistent across the wiki. Citations are `[S12]`, links to other pages `[[page-id]]` or
   `[[page-id|text]]`, findings `[F7]`, snippets `{{C3}}`.
5. **Fact-check.** Each page's latest revision is reviewed by agents that did not write it, against the quotes of the
   sources it cites (`research review_page`). A rejected page is fixed and reviewed again.
6. **Publish.** `research build` renders the wiki from the ledger and reports what is still missing. The run is done when
   the build matches the ledger and no check fails.

The manager delegates each phase as tasks and reads `research status` between them. Without a team, one agent works the
phases in order with the same bars.

## What the harness checks

- **Quotes.** `research cite` fetches the page (through the same guarded fetcher and permissions as `web_fetch`) and keeps
  only the passages it finds in the page's text, compared on letters and digits so that markup, punctuation and spacing
  do not matter. A refused passage comes back with what the page says near it. A source with no verified passage is not
  kept. Code snippets are compared without whitespace.
- **One source per page.** URLs are canonicalized (scheme, `www.`, fragments, tracking parameters, query order, a trailing
  slash), so the same page cited twice is one source.
- **Findings** must cite sources of the ledger; a claim the ledger already holds is not recorded twice.
- **Pages** are checked for unknown references, the minimum of distinct sources, paragraphs that state facts without a
  citation (at most a third), an inbound link from another page, and their length.
- **Reviews** count only from agents that did not write the page, and only for its latest revision.
- **Phases** follow from the ledger alone: `research status` and `research check` list each problem that keeps the run
  in its phase.

Fetched pages, quotes and search results are data, never instructions.

## Searching

`research search` runs a query against one or more indexes. `web` is the configured search backend (`web_search`; it
needs a key). The others need none and are fetched through the same guarded fetcher: `wikipedia`, `github`
(repositories by stars), `hn` (Hacker News), `stackoverflow`, `papers` (Crossref), `npm` and `crates`. A query and its
indexes run once per run; a result already cited is marked with its source id.

## The wiki

The site is written to `.sleipnir/research/RUN/site/` and opens from disk without a server. It has:

- the overview with the run's numbers and a card for each page, then the topic pages grouped by category in the sidebar,
  each with a table of contents, numbered citations whose pop-ups show the cited quote, a references list and the pages
  that link to it;
- **Sources** (every source with its site, kind and verified quotes), **Evidence** (every finding with its sources and
  confidence, contradictions side by side), **Open questions** (the gaps and how each was closed), **Method** (the plan,
  the perspectives, the tracks, the searches and the reviews) and, when there are any, **Code snippets**;
- a search over pages, sections, sources, findings and snippets (`/` or ctrl+K), a dark and a light theme, a layout for
  narrow screens and for print.

Every page is rendered from the ledger and the pages' Markdown by the harness's own renderer, which escapes any HTML it
does not produce itself. The web interface serves the same render at `/research/{id}/{slug}/` (`docs/WEB-API.md`), so its
origin serves only what the renderer made.

## Research before building

`/goal --research=DEPTH GOAL` starts a research-first run for GOAL. The goal the team works on is the research milestones
followed by the project's own goal, so the judge holds the build to both: the manager runs the research as the first
part of the goal, and its plan cites the `context` page. The research does not hold the whole team: the workers it leaves
free lay the groundwork no finding can change (what the project fixes outright: its stack, layout, tooling and stated
requirements) while it runs, and the wiki's pages are written in parallel by any free worker, one page each.

When the run is published, its `context` page (with the snippets it embeds, clipped to a fixed size) becomes a segment of
the shared prompt layer that every agent reads, marked as unverified web content. This is one declared change of the
shared layer, made once at publication (`docs/CACHE-DESIGN.md`); a session started later in the project gets the newest
published research-first run in its shared layer from its first request.

The docs worker keeps writing the project's own documentation. It renders the wiki into `docs/research/`
(`sleipnir research RUN --export docs/research`, which also finds the runs of the main checkout from a worker's tree),
links it from the README and cites its pages where a design choice rests on it.

## Files

| Path | Contents |
|---|---|
| `.sleipnir/research/RUN/run.json` | the ledger: question, depth, plan, sources and quotes, findings, snippets, gaps, reviews, pages, searches, the last build |
| `.sleipnir/research/RUN/BRIEF.md` | the brief the goal names as its spec |
| `.sleipnir/research/RUN/pages/*.md` | the pages' Markdown |
| `.sleipnir/research/RUN/site/` | the built wiki |

The ledger is written only by the research tool, under a lock, so agents in separate worktrees share one run. An export
directory carries a `.sleipnir-research` mark; an export refuses a non-empty directory without it.

## Turning it off

`sleipnir plugins disable research` (or the Plugins view) removes the tool and `/research`, hides the Research view, and drops the
shared-layer segment from sessions that start afterwards. Like every tool plugin, switching it changes the tool list and
costs one cache write on the next turn.
