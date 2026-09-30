"""A tiny library for drawing terminal-screen sketches: a cell grid rendered as HTML (every cell is its own fixed-width box, so
alignment does not depend on the font's glyph coverage), plus hand-drawn-looking annotation notes. Used by make_sketches.py;
the output is rendered to PNG by render.mjs (headless Chromium)."""
import html, math, random

CW, CH, FS = 9.6, 21, 16          # cell width, cell height, font size in px
PAL = dict(bg="#1a1b26", fg="#c0caf5", dim="#565f89", faint="#3b4261", green="#9ece6a", red="#f7768e", yellow="#e0af68",
           blue="#7aa2f7", cyan="#7dcfff", magenta="#bb9af7", orange="#ff9e64", panel="#16161e", sel="#283457")
# prompt layers, cold (stable) to hot (volatile)
LAYER = dict(G0="#4c6fd0", G1="#7aa2f7", G2="#2ac3de", G3="#9ece6a", G4="#e0af68", G5="#ff9e64", G6="#f7768e")


class Screen:
    def __init__(self, cols, rows):
        self.cols, self.rows = cols, rows
        self.cells = [[(" ", None, None, 0) for _ in range(cols)] for _ in range(rows)]

    def put(self, x, y, text, fg=None, bg=None, bold=False, dim=False):
        flags = (1 if bold else 0) | (2 if dim else 0)
        for i, ch in enumerate(text):
            if 0 <= y < self.rows and 0 <= x + i < self.cols:
                self.cells[y][x + i] = (ch, fg, bg, flags)
        return x + len(text)

    def runs(self, x, y, parts):
        """parts: list of (text, fg) or (text, fg, bg) or (text, fg, bg, 'b')"""
        for p in parts:
            t, fg = p[0], p[1]
            bg = p[2] if len(p) > 2 else None
            bold = len(p) > 3 and "b" in p[3]
            dim = len(p) > 3 and "d" in p[3]
            x = self.put(x, y, t, fg, bg, bold, dim)
        return x

    def fill(self, x, y, w, h, bg):
        for j in range(h):
            for i in range(w):
                if 0 <= y + j < self.rows and 0 <= x + i < self.cols:
                    ch, fg, _, fl = self.cells[y + j][x + i]
                    self.cells[y + j][x + i] = (ch, fg, bg, fl)

    def box(self, x, y, w, h, title="", color=None, rounded=True):
        color = color or PAL["faint"]
        tl, tr, bl, br = ("╭", "╮", "╰", "╯") if rounded else ("┌", "┐", "└", "┘")
        self.put(x, y, tl + "─" * (w - 2) + tr, color)
        self.put(x, y + h - 1, bl + "─" * (w - 2) + br, color)
        for j in range(1, h - 1):
            self.put(x, y + j, "│", color)
            self.put(x + w - 1, y + j, "│", color)
        if title:
            self.put(x + 2, y, " " + title + " ", PAL["fg"], None, True)

    def html(self):
        rows = []
        for line in self.cells:
            out = []
            for ch, fg, bg, fl in line:
                st = []
                if fg:
                    st.append("color:" + fg)
                if bg:
                    st.append("background:" + bg)
                if fl & 1:
                    st.append("font-weight:700")
                if fl & 2:
                    st.append("opacity:.55")
                c = html.escape(ch) if ch != " " else "&nbsp;"
                out.append('<i style="%s">%s</i>' % (";".join(st), c) if st else "<i>%s</i>" % c)
            rows.append("<div class='r'>" + "".join(out) + "</div>")
        return "\n".join(rows)


def wobble(points, amp=1.6, seed=1):
    rnd = random.Random(seed)
    return [(x + rnd.uniform(-amp, amp), y + rnd.uniform(-amp, amp)) for x, y in points]


def hand_path(points, seed=1):
    """A smooth, slightly shaky path through the points (Catmull-Rom converted to cubic Beziers)."""
    p = wobble(points, 1.4, seed)
    if len(p) < 3:
        return "M%.1f %.1f L%.1f %.1f" % (p[0][0], p[0][1], p[-1][0], p[-1][1])
    d = "M%.1f %.1f" % p[0]
    for i in range(len(p) - 1):
        p0 = p[i - 1] if i > 0 else p[i]
        p1, p2 = p[i], p[i + 1]
        p3 = p[i + 2] if i + 2 < len(p) else p[i + 1]
        c1 = (p1[0] + (p2[0] - p0[0]) / 6, p1[1] + (p2[1] - p0[1]) / 6)
        c2 = (p2[0] - (p3[0] - p1[0]) / 6, p2[1] - (p3[1] - p1[1]) / 6)
        d += " C%.1f %.1f %.1f %.1f %.1f %.1f" % (c1[0], c1[1], c2[0], c2[1], p2[0], p2[1])
    return d


def arrow(frm, to, color, seed=1, bend=0.18):
    """A hand-drawn arrow from frm to to (pixel coordinates), slightly curved, with a two-stroke head."""
    (x1, y1), (x2, y2) = frm, to
    mx, my = (x1 + x2) / 2, (y1 + y2) / 2
    nx, ny = -(y2 - y1), (x2 - x1)
    n = math.hypot(nx, ny) or 1
    mx, my = mx + nx / n * bend * n * 0.35, my + ny / n * bend * n * 0.35
    shaft = hand_path([(x1, y1), (mx, my), (x2, y2)], seed)
    ang = math.atan2(y2 - my, x2 - mx)
    hl = 11
    h1 = (x2 - hl * math.cos(ang - 0.45), y2 - hl * math.sin(ang - 0.45))
    h2 = (x2 - hl * math.cos(ang + 0.45), y2 - hl * math.sin(ang + 0.45))
    head = "M%.1f %.1f L%.1f %.1f L%.1f %.1f" % (h1[0], h1[1], x2, y2, h2[0], h2[1])
    return ("<path d='%s' /><path d='%s' />" % (shaft, head), color)


def note_svg(x, y, lines, color, size=15, rot=-1.5, width=None):
    """A handwritten-looking note (italic sans, slight rotation) with a wobbly underline under the first line."""
    tspans = "".join("<tspan x='%.1f' dy='%d'>%s</tspan>" % (x, size + 4 if i else 0, html.escape(t)) for i, t in enumerate(lines))
    w = width or max(len(t) for t in lines) * size * 0.52
    ul = hand_path([(x, y + 6), (x + w * 0.5, y + 8), (x + w, y + 5)], seed=int(x + y))
    return ("<g transform='rotate(%.1f %.1f %.1f)'><text x='%.1f' y='%.1f' font-size='%d' fill='%s' "
            "font-family=\"Liberation Sans, DejaVu Sans, sans-serif\" font-style='italic' font-weight='700'>%s</text>"
            "<path d='%s' stroke='%s' stroke-width='2' fill='none' stroke-linecap='round'/></g>") % (
        rot, x, y, x, y, size, color, tspans, ul, color)


PAGE = """<!doctype html><meta charset=utf-8><style>
body{margin:0;background:%(paper)s;font-family:'DejaVu Sans Mono',monospace}
.sheet{position:relative;padding:34px 40px 40px 40px;width:%(sw)dpx;background:%(paper)s}
.title{font:700 22px 'Liberation Sans',sans-serif;color:#33302a;margin:0 0 4px 2px}
.sub{font:italic 15px 'Liberation Sans',sans-serif;color:#6b6558;margin:0 0 18px 2px}
.win{background:%(bg)s;border-radius:12px;box-shadow:0 10px 30px rgba(40,30,10,.35);overflow:hidden;width:%(ww)dpx}
.bar{height:30px;background:#13141c;display:flex;align-items:center;padding-left:14px;gap:8px;color:#7d85a8;font:13px 'Liberation Sans',sans-serif}
.dot{width:12px;height:12px;border-radius:50%%;display:inline-block}
.scr{padding:10px 14px 14px 14px;font-size:%(fs)dpx;line-height:%(ch)dpx;color:%(fg)s;background:%(bg)s}
.r{height:%(ch)dpx;white-space:pre;display:flex}
.r i{font-style:normal;display:inline-block;width:%(cw)spx;text-align:center;overflow:visible;flex:none}
svg.ov{position:absolute;left:0;top:0;pointer-events:none}
</style>
<div class=sheet><div class=title>%(title)s</div><div class=sub>%(sub)s</div>
<div class=win><div class=bar><span class=dot style='background:#f7768e'></span><span class=dot style='background:#e0af68'></span><span class=dot style='background:#9ece6a'></span><span style='margin-left:12px'>%(wtitle)s</span></div>
<div class=scr>
%(body)s
</div></div>
<svg class=ov width=%(sw)d height=%(sh)d viewBox='0 0 %(sw)d %(sh)d' fill=none stroke-linecap=round stroke-linejoin=round>%(overlay)s</svg>
</div>"""


def page(screen, title, sub, wtitle, overlay="", paper="#f3eee3", extra_w=0):
    ww = int(screen.cols * CW + 28)
    sw = ww + 80 + extra_w
    sh = int(34 + 62 + 30 + screen.rows * CH + 24 + 40)
    return PAGE % dict(paper=paper, bg=PAL["bg"], fg=PAL["fg"], fs=FS, ch=CH, cw=CW, sw=sw, sh=sh, ww=ww, title=html.escape(title),
                       sub=html.escape(sub), wtitle=html.escape(wtitle), body=screen.html(), overlay=overlay)


def origin():
    """Pixel position of cell (0, 0) inside the sheet."""
    return 40 + 14, 34 + 62 + 30 + 10


def cell_xy(cx, cy, dx=0.0, dy=0.0):
    ox, oy = origin()
    return ox + cx * CW + dx, oy + cy * CH + dy


def badge(cx, cy, n, color, r=10):
    """A numbered marker on a cell; the note with the same number sits in the margin, level with the row."""
    x, y = cell_xy(cx, cy, CW / 2, CH / 2)
    return ("<g><circle cx='%.1f' cy='%.1f' r='%d' fill='%s' stroke='#fff' stroke-width='1.5'/>"
            "<text x='%.1f' y='%.1f' font-size='13' font-weight='700' text-anchor='middle' fill='#fff' "
            "font-family='Liberation Sans, sans-serif'>%d</text></g>") % (x, y, r, color, x, y + 4.5, n)


def margin_note(win_right_px, row, n, color, lines, nudge=0, size=15):
    """A note in the right margin, level with `row`, led by the same numbered badge; a dotted leader runs to the window."""
    _, y = cell_xy(0, row, 0, CH / 2)
    y += nudge
    x = win_right_px + 26
    out = ["<circle cx='%.1f' cy='%.1f' r='10' fill='%s'/>" % (x, y, color),
           "<text x='%.1f' y='%.1f' font-size='13' font-weight='700' text-anchor='middle' fill='#fff' font-family='Liberation Sans, sans-serif'>%d</text>" % (x, y + 4.5, n)]
    out.append(note_svg(x + 18, y + 4, lines, color, size=size, rot=-0.6))
    return "".join(out)


def place_notes(win_right_px, items, size=15, gap=10):
    """items: [(row, n, ink_colour, lines)]. Notes sit level with their rows unless that would overlap the one above;
    returns SVG for the notes, their badges and the dotted leaders from the badges in the window's gutter."""
    out, prev_bottom = [], -1e9
    for row, n, color, lines, gutter_col in sorted(items, key=lambda t: t[0]):
        _, ry = cell_xy(0, row, 0, CH / 2)
        h = len(lines) * (size + 4) + 4
        y = max(ry - 8, prev_bottom + gap)
        prev_bottom = y + h
        bx, by = cell_xy(gutter_col, row, CW / 2, CH / 2)
        nx = win_right_px + 26
        out.append("<path d='M%.1f %.1f L%.1f %.1f' stroke='%s' stroke-width='1.6' stroke-dasharray='2 5' opacity='.85'/>" % (bx + 12, by, nx - 12, y + 6, color))
        out.append(badge(gutter_col, row, n, color))
        out.append("<circle cx='%.1f' cy='%.1f' r='10' fill='%s'/>" % (nx, y + 6, color))
        out.append("<text x='%.1f' y='%.1f' font-size='13' font-weight='700' text-anchor='middle' fill='#fff' font-family='Liberation Sans, sans-serif'>%d</text>" % (nx, y + 10.5, n))
        out.append(note_svg(nx + 18, y + 10, lines, color, size=size, rot=-0.5))
    return "".join(out)
