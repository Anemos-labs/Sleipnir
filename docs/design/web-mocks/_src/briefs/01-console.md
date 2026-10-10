# 01 · CONSOLE — "tmux with a mouse"

Deliverable: `docs/design/web-mocks/01-console.html`

## The idea
The terminal program, kept honest and made better by the browser. Someone who loves the CLI opens this tab and
feels *nothing has been taken away*: same scrollback model, same live region at the bottom, same keys, same words.
The browser then adds what a terminal cannot: a mouse, real tabs, instant search of the scrollback, rich expansion of
tool output, copy buttons, hover explanations of every number, and pages (stats, cockpit) that are sharper than cells.

This is the **safe, familiar, power-user** option. Its craft is *restraint and precision*.

## Layout (single column, no sidebars)
* **Top bar like a tmux status bar** (one row, thin): left `[1:shop*] [2:orders-api] [3:+]` session tabs (each tab is a
  `sleipnir` session in this window; `*` = current; a tab shows a `●` when its turn is running and `?` when it has a
  question open); right: `● 127.0.0.1:6969 · loopback · token ✓`, a `mono` toggle (see below), the clock.
* **Scrollback** fills the page: the manager's conversation as the TUI draws it (left gutter markers `●` `◆` `⚠`,
  coloured tool names `Bash` `Edit` `Grep` with `✓ 1.2s`, collapsed tool output showing first and last lines with
  `ctrl+o` hint, diffs with line numbers and red/green rows and intra-line highlights, compaction row with the two
  little layer bars `compacted 1.4k ▇▇▇ → ▇▇ 625 -54%`, cache-break rows in pink with the explanation, a closing
  `── 51s · 23 steps · $0.11` rule). It is one continuous document in a monospace grid; it never reflows into cards.
* **Live region docked at the bottom** exactly like the TUI: status line (spinner, `manager waiting for the team`,
  elapsed, `↑9.4k ↓1.2k`, `$0.08`, `esc to interrupt`), the `⏎ queued:` line when something is typed ahead, the input
  box with rounded 1px border and `❯` prompt, the footer (`default · ctrl+t stats · / commands` left,
  `anthropic/claude-sonnet-5-5 · 20260102-030405-5eed01` right). The approval question is a box in the live region
  (command or diff, why it asks, `1 2 3`), with the quiet-period meter.
* **Pages** are full-height overlays that replace the scrollback the way the TUI's pages do, closed by the same key:
  `ctrl+t` **stats page** (token categories, hit ratio, saved est., the six prompt layers as a stacked bar with
  `/context` weights), `ctrl+g` **cockpit** (the pixel horse sprite on a `<canvas>` with its eight legs lit by state,
  the agents table, shared-prefix bar, swarm gantt, task board, merge queue, mail, governor: it is the `swarm.png`
  capture rebuilt in crisp HTML/CSS and made hoverable), plus `/sessions`, `/model` (searchable list with prices and
  `★` favourites), `/rewind` (checkpoint list with `/diff`), `/permissions`, `/mcp`, `/trust`, `/help`.

## What the browser adds (the reasons to prefer it over a terminal)
* click a tool call to expand its whole output; click a path to open its diff in an overlay; click an agent id
  (`be-2`) to open that agent's transcript in a side overlay; hover a number for a tooltip (`hit 84% = 56.9k cached-read
  / (11.2k uncached + 56.9k)`).
* a **scrollback search bar** (opened with `ctrl+f` or `/` at an empty prompt... choose the least surprising key and
  document it) that highlights matches and jumps between them;
* **copy** buttons that appear on hover for code blocks and tool output; selection works normally;
* session **tabs** and a **new-session** flow (`/new`, `/resume` menu);
* a **`mono` toggle** that switches the page to NO_COLOR mode (bold / dim / reverse carry the meaning), to prove the
  highlights stay readable without colour (the repo's UX rule); another toggle for reduced motion.
* a small **keys help** (`?` at an empty prompt) listing every binding.

## Look
* Tokyo Night on near-black (`#0b0e17`-ish, `#c0caf5` text, `#565f89` dim). Strict monospace grid: pick one font size,
  line-height an exact multiple (e.g. 14px / 21px), use `ch` for widths so box drawing and bars align to cells.
  Fonts: `JetBrains Mono` (Google) with `"DejaVu Sans Mono", ui-monospace, Menlo, Consolas` fallback.
* Borders are 1px lines in `#2a2e45`-ish, rounded 6px only on the input box and dialogs, like the TUI frames.
  No shadows, no gradients, no cards, no avatars, no bubbles, no icons beyond the TUI glyphs. Selection colour,
  a blinking block caret, and a subtle bell flash on the tab when a question opens (`SLEIPNIR_BELL` analogue) are the "delight".
* Dense but breathable: the TUI's own rhythm (blank line between turns).

## Signature moments to nail
1. The streaming conversation with live tool rows, collapsed output, the cache-break row.
2. The question box: arrow keys + `1 2 3`, the pause meter, `n`/`esc`.
3. `/` opens the palette as a TUI-style popup anchored above the input with descriptions, `tab` completes, fuzzy filter.
4. `ctrl+g` → the cockpit page appears; legs of the pixel horse step when their agent works; `ctrl+g` returns.
5. `ctrl+t` → the stats page.
6. `shift+tab` visibly cycles `default → accept-edits → plan` in the footer and never `bypass`.

## Avoid
Sidebars, cards, rounded "app" chrome, chat bubbles, gradients, glassmorphism, big headings, marketing copy.
If a screen could be mistaken for a SaaS dashboard, it is wrong.
