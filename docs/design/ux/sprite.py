#!/usr/bin/env python3
"""The terminal horse: the official mark (docs/media/make_logo.py: the same Bezier outlines) rasterised to a small pixel grid, with
eight legs spread evenly along the belly and four gait frames. Pure Python; supersampling, no dependencies.

    python3 sprite.py            # prints the frames as half-block text
    python3 sprite.py --go FILE  # writes the frames as Go source (internal/tui/widget/horsesprite_gen.go)
    python3 sprite.py --html FILE  # an upscaled preview sheet for a browser

Pixel keys: ' ' empty, 'b' body, 'm' mane and tail, 'e' eye, 'h' hoof, '0'..'7' the eight legs (role colours)."""
import math, re, sys, os

SS = 6                       # supersampling per pixel side
# sprite sizes: (columns = pixels across, pixel rows (two per terminal row), mark-to-pixel scale)
SIZES = {"large": (52, 34, 0.0795), "medium": (40, 26, 0.0612), "small": (30, 20, 0.0458)}
W, H, SC = SIZES["large"]
OX, OY = 140.0, 52.0         # mark coordinates -> sprite pixels

BODY = ("M296 226 C310 192 380 184 450 192 C500 197 530 188 556 198 C590 212 600 250 582 280 "
        "C560 312 500 316 440 310 C380 304 318 300 300 270 C290 254 290 240 296 226 Z")
NECK = "M548 200 C580 160 604 118 634 92 L672 112 C668 150 648 182 624 212 C606 236 594 256 582 280 Z"
HEAD = "M634 92 C640 66 660 62 668 76 C690 88 720 120 738 150 C746 166 740 182 724 180 C704 178 680 164 660 152 Z"
MANE = ["M566 196 C566 168 580 140 600 116", "M552 204 C552 176 566 150 586 128"]      # two strands, flowing back down the neck
TAIL = ["M300 228 C262 220 222 232 190 250 C180 256 172 262 166 270", "M304 240 C274 238 246 248 226 264 C218 270 212 276 206 284"]   # streams back, only a little drooping
HIPS = [318, 356, 394, 432, 470, 508, 546, 578]   # eight legs, evenly along the belly


def use(size):
    global W, H, SC
    W, H, SC = SIZES[size]


def cubic(p0, p1, p2, p3, n=28):
    out = []
    for i in range(1, n + 1):
        t = i / n
        a, b, c, d = (1 - t) ** 3, 3 * (1 - t) ** 2 * t, 3 * (1 - t) * t * t, t ** 3
        out.append((a * p0[0] + b * p1[0] + c * p2[0] + d * p3[0], a * p0[1] + b * p1[1] + c * p2[1] + d * p3[1]))
    return out


def parse(d):
    """Flatten an absolute M/L/C/Z path into one polyline."""
    tok = re.findall(r"[MLCZ]|-?\d+\.?\d*", d)
    pts, i, cur = [], 0, (0, 0)
    while i < len(tok):
        c = tok[i]
        i += 1
        if c == "M" or c == "L":
            cur = (float(tok[i]), float(tok[i + 1])); i += 2; pts.append(cur)
        elif c == "C":
            q = [(float(tok[i + 2 * k]), float(tok[i + 2 * k + 1])) for k in range(3)]; i += 6
            pts += cubic(cur, *q); cur = q[2]
        elif c == "Z":
            pass
    return pts


def to_px(pts):
    return [((x - OX) * SC, (y - OY) * SC) for x, y in pts]


class Canvas:
    def __init__(self):
        self.w, self.h = W * SS, H * SS
        self.layers = []

    def blank(self):
        return [bytearray(self.w) for _ in range(self.h)]

    def fill(self, pts, key, thr=0.5):
        m = self.blank()
        pp = [(x * SS, y * SS) for x, y in to_px(pts)]
        ys = [p[1] for p in pp]
        for y in range(max(0, int(min(ys))), min(self.h, int(max(ys)) + 1)):
            xs = []
            yc = y + 0.5
            for i in range(len(pp)):
                (x1, y1), (x2, y2) = pp[i], pp[(i + 1) % len(pp)]
                if (y1 <= yc < y2) or (y2 <= yc < y1):
                    xs.append(x1 + (yc - y1) * (x2 - x1) / (y2 - y1))
            xs.sort()
            for a, b in zip(xs[::2], xs[1::2]):
                for x in range(max(0, int(a + 0.5)), min(self.w, int(b + 0.5))):
                    m[y][x] = 1
        self.layers.append((key, thr, m))

    def stroke(self, pts, width, key, thr=0.4, taper=None):
        """A thick polyline (capsules); taper=(w_start, w_end) in sprite pixels overrides width."""
        m = self.blank()
        pp = [(x * SS, y * SS) for x, y in to_px(pts)]
        n = len(pp)
        for i in range(n - 1):
            (x1, y1), (x2, y2) = pp[i], pp[i + 1]
            w1, w2 = (taper if taper else (width, width))
            r1 = (w1 + (w2 - w1) * i / max(1, n - 1)) * SS / 2
            r2 = (w1 + (w2 - w1) * (i + 1) / max(1, n - 1)) * SS / 2
            rmax = max(r1, r2)
            dx, dy = x2 - x1, y2 - y1
            L2 = dx * dx + dy * dy or 1e-9
            for y in range(max(0, int(min(y1, y2) - rmax)), min(self.h, int(max(y1, y2) + rmax) + 1)):
                for x in range(max(0, int(min(x1, x2) - rmax)), min(self.w, int(max(x1, x2) + rmax) + 1)):
                    t = max(0.0, min(1.0, ((x + .5 - x1) * dx + (y + .5 - y1) * dy) / L2))
                    px, py = x1 + t * dx, y1 + t * dy
                    r = r1 + (r2 - r1) * t
                    if (x + .5 - px) ** 2 + (y + .5 - py) ** 2 <= r * r:
                        m[y][x] = 1
        self.layers.append((key, thr, m))

    def dot(self, x, y, key):
        """One whole pixel."""
        m = self.blank()
        for yy in range(int(y * SS), int((y + 1) * SS)):
            for xx in range(int(x * SS), int((x + 1) * SS)):
                if 0 <= yy < self.h and 0 <= xx < self.w:
                    m[yy][xx] = 1
        self.layers.append((key, 0.5, m))

    def pixels(self):
        grid = [[" "] * W for _ in range(H)]
        for key, thr, m in self.layers:
            for y in range(H):
                for x in range(W):
                    s = 0
                    for yy in range(y * SS, (y + 1) * SS):
                        row = m[yy]
                        s += sum(row[x * SS:(x + 1) * SS])
                    if s / (SS * SS) >= thr:
                        grid[y][x] = key
        return ["".join(r).rstrip() for r in grid]


def leg_points(k, phase):
    """Hip -> knee -> fetlock -> hoof of leg k at a gait phase (0..3). Hind legs lean back, front legs reach forward, the middle
    ones are closer to upright; every leg swings with its own offset and a lifted leg is shorter."""
    hx = HIPS[k]
    wave = phase * math.pi / 2 + k * math.pi / 4
    ang = (k - 3.5) * 0.15 + 0.24 * math.sin(wave)
    lift = max(0.0, math.sin(wave + math.pi / 2))
    up, lo = 62.0, 84.0 - 22.0 * lift
    hy = 294.0
    kx, ky = hx + up * math.sin(ang * 0.55), hy + up * math.cos(ang * 0.55)
    fx, fy = kx + lo * math.sin(ang * 1.35 - 0.22 * lift), ky + lo * math.cos(ang * 1.25)
    return [(hx, hy), (kx, ky), (fx, fy)]


def frame(phase, stand=False):
    c = Canvas()
    # far legs first (they sit behind the body): every other one, drawn in the same colour as their near neighbours
    order = [0, 2, 4, 6, 1, 3, 5, 7]
    def legs(ks):
        for k in ks:
            pts = leg_points(k, 0 if stand else phase)
            if stand:
                hx = HIPS[k]
                pts = [(hx, 294.0), (hx + (k - 3.5) * 1.5, 350.0), (hx + (k - 3.5) * 2.5, 424.0)]
            c.stroke(pts, 1.5, str(k), 0.34, taper=(max(1.3, 1.9 * SC / 0.0795), max(1.0, 1.15 * SC / 0.0795)))
            fx, fy = pts[-1]
            c.stroke([(fx - 6, fy + 2), (fx + 10, fy + 2)], 1.0, "h", 0.3)
    legs(order[:4])
    c.fill(parse(BODY), "b")
    c.fill(parse(NECK), "b")
    c.fill(parse(HEAD), "b")
    legs(order[4:])
    for s in MANE:
        c.stroke(parse(s), 1.35, "m", 0.3, taper=(max(1.2, 1.6 * SC / 0.0795), 1.0))
    for s in TAIL:
        c.stroke(parse(s), 1.3, "m", 0.3, taper=(max(1.2, 1.7 * SC / 0.0795), 0.9))
    ex, ey = (672 - OX) * SC, (104 - OY) * SC
    c.dot(int(ex), int(ey), "e")
    return c.pixels()


def halfblocks(rows):
    rows = rows + [""] * (len(rows) % 2)
    out = []
    for y in range(0, len(rows), 2):
        a, b = rows[y].ljust(W), rows[y + 1].ljust(W) if y + 1 < len(rows) else " " * W
        line = ""
        for x in range(W):
            t, u = a[x], b[x]
            line += " " if t == " " and u == " " else ("█" if t == u else "▀" if u == " " else "▄" if t == " " else "▀")
        out.append(line)
    return out


PAL = {"b": "#e9e7f5", "m": "#8f6bf0", "e": "#1a1b26", "h": "#3b3850", "0": "#bb9af7", "1": "#7aa2f7", "2": "#7aa2f7", "3": "#7dcfff",
       "4": "#7dcfff", "5": "#9ece6a", "6": "#e0af68", "7": "#ff9e64"}


def html(frames):
    cells = []
    for f in frames:
        px = []
        for row in f:
            for ch in row.ljust(W):
                col = PAL.get(ch)
                px.append("<i style='background:%s'></i>" % col if col else "<i></i>")
        cells.append("<div class=f style='grid-template-columns:repeat(%d,9px)'>%s</div>" % (W, "".join(px)))
    return ("<!doctype html><meta charset=utf-8><style>body{margin:0;background:#1a1b26}.s{display:flex;gap:28px;padding:24px}"
            ".f{display:grid;grid-auto-rows:9px;background:#16161e;padding:10px;border-radius:8px}"
            ".f i{display:block}</style><div class=s>%s</div>" % "".join(cells))


def go_source(sprites):
    def lit(rows, indent="\t\t"):
        return "{\n" + "".join("%s%s,\n" % (indent, repr_go(r)) for r in rows) + indent[:-1] + "}"
    body = "// Code generated by docs/design/ux/sprite.py; DO NOT EDIT.\n\npackage widget\n\n"
    body += "// horseSprite is the terminal horse at one size: four gait frames and a standing one. One string per pixel row (two pixel rows\n"
    body += "// make one terminal row). Keys: ' ' empty, 'b' body, 'm' mane and tail, 'e' eye, 'h' hoof, '0'..'7' the eight legs.\n"
    body += "type horseSprite struct {\n\tframes [4][]string\n\tstand  []string\n}\n\n"
    body += "var horseSprites = map[string]horseSprite{\n"
    for name, (frames, stand) in sprites.items():
        body += "\t%s: {\n\t\tframes: [4][]string{\n" % repr_go(name)
        for f in frames:
            body += "\t\t\t" + lit(f, "\t\t\t\t") + ",\n"
        body += "\t\t},\n\t\tstand: []string" + lit(stand, "\t\t\t") + ",\n\t},\n"
    body += "}\n"
    return body


def repr_go(s):
    return '"' + s + '"'


def build(size):
    use(size)
    return [frame(p) for p in range(4)], frame(0, stand=True)


if __name__ == "__main__":
    sprites = {n: build(n) for n in SIZES}
    if "--go" in sys.argv:
        open(sys.argv[sys.argv.index("--go") + 1], "w").write(go_source(sprites))
    elif "--html" in sys.argv:
        out = []
        for n, (frames, stand) in sprites.items():
            use(n)
            out.append(html([frames[0], frames[1], frames[2], stand]).split("<div class=s>")[1].rsplit("</div>", 1)[0])
        use("large")
        head = html([]).split("<div class=s>")[0]
        open(sys.argv[sys.argv.index("--html") + 1], "w").write(head + "<div style='display:flex;flex-direction:column'>" + "".join("<div class=s>%s</div>" % o for o in out) + "</div>")
    else:
        for n, (frames, stand) in sprites.items():
            use(n)
            print("==", n, W, "x", H)
            for r in halfblocks(frames[0]):
                print(r)
