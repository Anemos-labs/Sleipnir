#!/usr/bin/env python3
"""Draws the design sketches of the terminal UI (docs/UX.md): HTML files next to this script, rendered to PNG by render.mjs.
   python3 make_sketches.py && node render.mjs chat.html swarm.html story.html"""
import os, sys
from sketchlib import *
import swarm_sketch, story_sketch

HERE = os.path.dirname(os.path.abspath(__file__))
INK = {"a": "#d6336c", "b": "#1c7ed6", "c": "#2b8a3e", "d": "#e8590c", "e": "#7048e8"}  # marker colours for the notes
P = PAL


def stack_bar(s, x, y, w, layers, cached, levels=None):
    """layers: [(name, tokens)], cached: tokens served from cache (a prefix). Draws one row and the label row under it."""
    total = sum(t for _, t in layers)
    share = [max(1, round(t / total * w)) for _, t in layers]
    while sum(share) > w:
        share[share.index(max(share))] -= 1
    while sum(share) < w:
        share[share.index(max(share))] += 1
    cx, used = x, 0
    cached_cells = round(cached / total * w)
    for (name, tok), n in zip(layers, share):
        col = LAYER[name.split()[0]]
        for i in range(n):
            pos = used + i
            if pos < cached_cells - 1:
                s.put(cx + i, y, "█", col)
            elif pos == cached_cells - 1:
                s.put(cx + i, y, "▓", col)
            else:
                s.put(cx + i, y, "░", col, dim=True)
        key = name.split()[0]
        if n >= len(key):
            s.put(cx + (n - len(key)) // 2, y + 1, key, col, bold=True)
        elif name.startswith("G6"):
            s.put(cx, y + 1, key, col, bold=True)
        cx += n
        used += n
    return x + w


def sparkline(s, x, y, values, marks=None):
    bars = "▁▂▃▄▅▆▇█"
    marks = marks or {}
    for i, v in enumerate(values):
        ch = bars[min(7, int(v * 7.99))]
        col = P["green"] if v > 0.85 else P["yellow"] if v > 0.5 else P["red"]
        s.put(x + i, y, ch, col)
        if i in marks:
            s.put(x + i, y - 1, marks[i][0], marks[i][1], bold=True)
    return x + len(values)


def chat():
    s = Screen(104, 37)
    s.runs(1, 0, [("◆ sleipnir", P["magenta"], None, "b"), (" 0.1.0  ", P["dim"]), ("heimdall/deepseek-v4-flash", P["cyan"]),
                  ("  ~/code/orders-api  ", P["dim"]), ("⎇ main ✓", P["green"]), ("  accept-edits", P["yellow"])])
    s.runs(1, 2, [("❯ ", P["magenta"], None, "b"), ("the pagination test in ./orders is failing, fix it", P["fg"], None, "b")])
    s.runs(1, 4, [("● ", P["fg"]), ("I'll read the test and the handler it exercises.", P["fg"])])
    for i, (tool, arg, res) in enumerate([("read", "orders/list_test.go", "84 lines"), ("grep", '"func List"', "3 matches in 2 files"),
                                          ("read", "orders/list.go", "212 lines")]):
        s.runs(3, 5 + i, [("⎿ ", P["dim"]), (tool.ljust(5), P["blue"]), (arg.ljust(28), P["fg"]), (res, P["dim"])])
    s.runs(1, 9, [("● ", P["fg"]), ("Edit ", P["blue"], None, "b"), ("orders/list.go", P["fg"])])
    diff = [(58, " ", "func (s *Store) List(ctx context.Context, page, size int) ([]Order, error) {"),
            (59, "-", "    offset := page * size"), (59, "+", "    offset := (page - 1) * size"),
            (60, " ", "    rows, err := s.db.QueryContext(ctx, listSQL, size, offset)")]
    for i, (n, sign, code) in enumerate(diff):
        bg = "#3b2030" if sign == "-" else "#20352b" if sign == "+" else None
        col = P["red"] if sign == "-" else P["green"] if sign == "+" else P["dim"]
        s.put(4, 10 + i, str(n).rjust(3) + " " + sign + " " + code.ljust(84), col if sign != " " else P["dim"], bg)
    s.runs(1, 15, [("● ", P["fg"]), ("Bash ", P["blue"], None, "b"), ("go test ./orders/...".ljust(74), P["fg"]), ("✓ 1.4s", P["green"])])
    s.runs(3, 16, [("⎿ ", P["dim"]), ("ok   example.com/orders   0.318s", P["dim"])])
    s.runs(1, 18, [("◆ compacted ", P["magenta"]), ("31.2k ", P["dim"]), ("▓▓▓▓▓▓▓▓▓▓▓▓", LAYER["G5"]), (" → ", P["dim"]), ("2.4k ", P["dim"]),
                   ("▒", LAYER["G4"]), ("   spine +1 resume · cache rewritten while cold: free", P["dim"])])
    s.runs(1, 20, [("● ", P["fg"]), ("Fixed. ", P["fg"], None, "b"), ("`List` computed the offset from a zero-based page; pages are one-based, so", P["fg"])])
    s.put(3, 21, "page 1 skipped the first `size` rows. The test passes. Want a test for page 0 too?", P["fg"])
    # live region
    s.runs(1, 23, [("⠹ ", P["magenta"], None, "b"), ("Thinking… ", P["fg"]), ("14s", P["dim"]), ("   ↓ 2.1k  ↑ 48.3k", P["dim"]), ("   $0.0021", P["fg"]),
                   ("   saved ≈ $0.31 (−93%) ", P["green"]), ("at list price", P["dim"]), ("   esc to interrupt", P["dim"])])
    s.put(1, 25, "prompt ", P["dim"])
    s.put(8, 25, "48.3k ", P["fg"])
    s.put(14, 25, "▕", P["dim"])
    layers = [("G0 const", 9100), ("G1 shared", 12400), ("G2 role", 6000), ("G3", 3200), ("G4", 1800), ("G5 thread", 14600), ("G6", 1200)]
    x = stack_bar(s, 15, 25, 50, layers, cached=44900)
    s.put(x, 25, "▏", P["dim"])
    s.runs(x + 2, 25, [("⛁ ", P["green"], None, "b"), ("93% cached", P["green"], None, "b"), ("    ◕ warm ", P["yellow"]), ("4:12 ", P["fg"]),
                       ("▰▰▰▰▰▰", P["yellow"]), ("▱▱", P["faint"])])
    s.put(1, 28, "cache  ", P["dim"])
    vals = [.2, .55, .8, .93, .96, .97, .97, .96, .95, .7, .42, .9, .97, .96, .97, .98, .95, .96, .97, .93]
    e = sparkline(s, 8, 28, vals, {10: ("⚠", P["red"]), 17: ("◆", P["magenta"])})
    s.put(e + 2, 28, "hit ratio per request · 14 req · 2.1k out", P["dim"])
    s.box(1, 30, 102, 3, color=P["faint"])
    s.runs(3, 31, [("❯ ", P["magenta"], None, "b"), ("now add a test for page 0 and a negative size", P["fg"]), ("█", P["fg"])])
    s.runs(1, 34, [("⏵⏵ accept edits", P["yellow"]), (" · ctrl+t stack · ctrl+o expand · / commands", P["dim"])])
    s.put(85, 34, "~/code/orders-api", P["dim"])
    s.put(1, 35, "   ⏎ queued: “and run the race detector”", P["dim"])

    WR = 40 + int(104 * CW + 28)             # right edge of the terminal window in the sheet
    items = [
        (12, 1, INK["b"], ["diffs, not “✓ edit”:", "what changed is always on screen"], 101),
        (18, 2, INK["e"], ["compaction is a little fold:", "the thread block shrinks into", "a one-line resume"], 101),
        (23, 3, INK["c"], ["saved ≈ $ is measured cache-read", "tokens × (input − read price); it", "counts up live. Labelled: at list price"], 101),
        (25, 4, INK["a"], ["THE PROMPT STACK: G0–G6 sized by tokens.", "Bright = served from cache, dim = paid", "in full. A cache break lights the layer", "that changed, with its cost."], 101),
        (28, 5, INK["d"], ["hit ratio per request; ⚠ a cache break,", "◆ a compaction: the cache's history"], 101),
        (31, 6, INK["b"], ["type ahead: what you type while the", "agent works is queued for the turn end"], 101),
    ]
    ov = [place_notes(WR, items)]
    return page(s, "Sleipnir chat: a turn in progress (design sketch)",
                "inline, not full-screen: scrollback above stays yours; the live region below is redrawn in place", "sleipnir — ~/code/orders-api — 104×37",
                "".join(ov), extra_w=400)


def write(name, body):
    with open(os.path.join(HERE, name + ".html"), "w") as f:
        f.write(body)
    print("wrote", name + ".html")


if __name__ == "__main__":
    want = sys.argv[1:] or ["chat"]
    if "chat" in want or "all" in want:
        write("chat", chat())
    if "story" in want or "all" in want:
        write("story", story_sketch.story())
    if "swarm" in want or "all" in want:
        write("swarm", swarm_sketch.swarm())
