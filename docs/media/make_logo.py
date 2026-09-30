#!/usr/bin/env python3
"""Draws the Sleipnir mark (an eight-legged horse, one leg per rider on the shared prefix) and its variants:

    python3 make_logo.py && node render.mjs logo.html wordmark.html social.html favicon-test.html

Outputs next to this script: logo.svg, logo-dark.svg, logo-wordmark.svg, logo-wordmark-dark.svg, favicon.svg; render.mjs turns the
HTML sheets into logo.png, logo-wordmark.png, social-preview.png and favicon-*.png. The hand-drawn wobble is an SVG filter
(feTurbulence + feDisplacementMap); the geometry is plain Bezier paths, so the files are small and scale to any size."""
import os, random, math

HERE = os.path.dirname(os.path.abspath(__file__))
ROLE = ["#bb9af7", "#7aa2f7", "#7aa2f7", "#7dcfff", "#7dcfff", "#9ece6a", "#e0af68", "#ff9e64"]

HIND = [((330, 292), (290, 345), (240, 385), (195, 420), (178, 434)),
        ((355, 294), (330, 350), (285, 400), (250, 440), (232, 454)),
        ((380, 294), (382, 345), (356, 395), (344, 440), (336, 456)),
        ((405, 294), (412, 340), (434, 384), (446, 418), (456, 428))]
FRONT = [((500, 292), (496, 345), (480, 395), (470, 440), (462, 456)),
         ((525, 294), (545, 345), (580, 385), (606, 420), (620, 430)),
         ((550, 292), (590, 335), (640, 360), (690, 385), (708, 392)),
         ((575, 288), (620, 318), (660, 336), (690, 330), (704, 322))]
BODY = ("M296 226 C310 192 380 184 450 192 C500 197 530 188 556 198 C590 212 600 250 582 280 "
        "C560 312 500 316 440 310 C380 304 318 300 300 270 C290 254 290 240 296 226 Z")
NECK = "M548 200 C580 160 604 118 634 92 L672 112 C668 150 648 182 624 212 C606 236 594 256 582 280"
HEAD = "M634 92 C640 66 660 62 668 76 C690 88 720 120 738 150 C746 166 740 182 724 180 C704 178 680 164 660 152"
MANE = "M548 200 C550 168 568 138 590 112 M560 194 C564 160 582 132 606 104 M574 184 C580 152 598 124 622 96 M538 208 C538 178 550 152 568 128"
TAIL = "M300 228 C250 222 196 246 160 300 C150 318 150 336 160 346 C170 322 190 300 214 290 C190 326 186 350 196 368 C214 340 236 318 262 308"


def jitter(pts, seed, amp=1.2):
    r = random.Random(seed)
    return [(x + r.uniform(-amp, amp), y + r.uniform(-amp, amp)) for x, y in pts]


def smooth(pts):
    d = "M%.1f %.1f" % pts[0]
    for i in range(len(pts) - 1):
        p0 = pts[i - 1] if i else pts[i]
        p1, p2 = pts[i], pts[i + 1]
        p3 = pts[i + 2] if i + 2 < len(pts) else pts[i + 1]
        c1 = (p1[0] + (p2[0] - p0[0]) / 6, p1[1] + (p2[1] - p0[1]) / 6)
        c2 = (p2[0] - (p3[0] - p1[0]) / 6, p2[1] - (p3[1] - p1[1]) / 6)
        d += " C%.1f %.1f %.1f %.1f %.1f %.1f" % (c1 + c2 + p2)
    return d


def leg(pts, color, seed, ink, far, thick=1.0):
    pts = jitter(pts, seed)
    du, dl = smooth(pts[:3]), smooth(pts[2:])
    hx, hy = pts[-1]
    op = 0.55 if far else 1.0
    return ("<g opacity='%.2f'><path d='%s' stroke='%s' stroke-width='%.1f'/><path d='%s' stroke='%s' stroke-width='%.1f'/>"
            "<path d='%s' stroke='%s' stroke-width='%.1f'/><path d='%s' stroke='%s' stroke-width='%.1f'/>"
            "<path d='M%.1f %.1f l-6 10 l16 0 z' fill='%s' stroke='%s' stroke-width='3'/></g>") % (
        op, du, ink, 25 * thick, du, color, 15 * thick, dl, ink, 14 * thick, dl, color, 8 * thick, hx - 2, hy - 4, ink, ink)


def mark(ink, fill, mane, rough=True, thick=1.0):
    far = [i in (0, 1, 4, 5) for i in range(8)]
    legs = [leg(p, c, i + 3, ink, far[i], thick) for i, (p, c) in enumerate(zip(HIND + FRONT, ROLE))]
    far_legs = "".join(l for l, f in zip(legs, far) if f)
    near_legs = "".join(l for l, f in zip(legs, far) if not f)
    flt = " filter='url(#rough)'" if rough else ""
    return ("<g%s fill='none' stroke-linecap='round' stroke-linejoin='round'>%s"
            "<path d='%s' fill='%s' stroke='%s' stroke-width='5'/><path d='%s' fill='%s' stroke='%s' stroke-width='5'/>"
            "<path d='%s' fill='%s' stroke='%s' stroke-width='5'/>"
            "<path d='%s' stroke='%s' stroke-width='5'/><path d='%s' stroke='%s' stroke-width='5'/>"
            "<circle cx='672' cy='102' r='5' fill='%s'/><circle cx='731' cy='166' r='3.2' fill='%s'/>%s</g>") % (
        flt, far_legs, BODY, fill, ink, NECK, fill, ink, HEAD, fill, ink, MANE, mane, TAIL, mane, ink, ink, near_legs)


DEFS = ("<defs><filter id='rough' x='-5%' y='-5%' width='110%' height='110%'><feTurbulence type='fractalNoise' baseFrequency='0.035' "
        "numOctaves='2' seed='4' result='n'/><feDisplacementMap in='SourceGraphic' in2='n' scale='3.2'/></filter></defs>")
LIGHT = dict(ink="#24232e", fill="#fbf8f1", mane="#7048e8")
DARK = dict(ink="#e9e7f5", fill="#2b2a3a", mane="#b79cf9")
FONT = "ui-sans-serif, system-ui, -apple-system, 'Segoe UI', 'Helvetica Neue', Helvetica, Arial, sans-serif"
VB_MARK = "110 30 700 480"


def svg_mark(pal, title="Sleipnir"):
    return ("<svg xmlns='http://www.w3.org/2000/svg' viewBox='%s' role='img' aria-label='%s'><title>%s</title>%s%s</svg>\n"
            % (VB_MARK, title, title, DEFS, mark(**pal)))


def svg_wordmark(pal, text_color, sub_color):
    # the mark at the left, the name and the tagline at the right
    return ("<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 1500 520' role='img' aria-label='Sleipnir: one manager brain, many legs'>"
            "<title>Sleipnir</title>%s<g transform='translate(-60 20) scale(1.0)'>%s</g>"
            "<text x='800' y='262' font-family=\"%s\" font-size='190' font-weight='800' fill='%s' letter-spacing='2'>Sleipnir</text>"
            "<text x='806' y='332' font-family=\"%s\" font-size='46' font-style='italic' fill='%s'>one manager brain, many legs</text></svg>\n"
            % (DEFS, mark(**pal), FONT, text_color, FONT, sub_color))


def svg_favicon():
    """A 64x64 icon that still reads at 16 px: a pale horse silhouette (body, neck, head, tail) over eight bold, evenly spaced leg
    bars in the role colours, on a rounded square. The detailed mark is for big sizes; this is its reduction."""
    legs = "".join("<rect x='%.1f' y='35' width='3.4' height='%d' rx='1.7' fill='%s'/>" % (13.5 + 4.1 * k, h, c)
                   for k, (c, h) in enumerate(zip(ROLE, (14, 16, 12, 16, 14, 16, 12, 15))))
    return ("<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64' role='img' aria-label='Sleipnir'><title>Sleipnir</title>"
            "<rect width='64' height='64' rx='14' fill='#24232e'/>%s"
            "<path d='M13 28 C11 23 14 20 20 21 L40 21 C45 21 47 19 48 14 L46 9 L51 9 L53 6 L55 10 L61 16 C62 19 60 22 57 21 L53 20 "
            "L50 26 C48 33 42 37 36 37 L22 37 C16 37 13 33 13 28 Z' fill='#e9e7f5'/>"
            "<path d='M13 26 C8 26 5 30 5 35 C8 32 10 31 13 32 Z' fill='#b79cf9'/>"
            "<path d='M44 13 L41 19 M47 12 L44 19' stroke='#b79cf9' stroke-width='2' stroke-linecap='round'/>"
            "<circle cx='54' cy='13' r='1.6' fill='#24232e'/></svg>\n" % legs)


def write(name, text):
    with open(os.path.join(HERE, name), "w") as f:
        f.write(text)
    print("wrote", name)


HTML = ("<!doctype html><meta charset=utf-8><style>html,body{margin:0;background:%s}.s{display:inline-block;width:%dpx;height:%dpx;%s}"
        ".s svg{width:100%%;height:100%%;display:block}</style><div class=s>%s</div>")


def sheet(name, inner, w, h, bg="transparent", extra=""):
    write(name, HTML % (bg, w, h, extra, inner))


if __name__ == "__main__":
    write("logo.svg", svg_mark(LIGHT))
    write("logo-dark.svg", svg_mark(DARK))
    write("logo-wordmark.svg", svg_wordmark(LIGHT, "#24232e", "#6b6558"))
    write("logo-wordmark-dark.svg", svg_wordmark(DARK, "#f0eef8", "#a7a3bd"))
    write("favicon.svg", svg_favicon())
    # sheets for the PNG exports
    sheet("logo.html", svg_mark(LIGHT), 1024, 702)
    sheet("wordmark.html", svg_wordmark(LIGHT, "#24232e", "#6b6558"), 1500, 520)
    social = ("<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 1280 640'><rect width='1280' height='640' fill='#f3eee3'/>"
              "<path d='M0 560 C320 590 960 590 1280 556' stroke='#d8d0bc' stroke-width='3' stroke-dasharray='5 11' fill='none'/>"
              "<g transform='translate(610 96) scale(0.74)'>%s</g>%s"
              "<text x='80' y='250' font-family=\"%s\" font-size='118' font-weight='800' fill='#24232e' letter-spacing='1'>Sleipnir</text>"
              "<text x='84' y='316' font-family=\"%s\" font-size='34' font-style='italic' fill='#6b6558'>one manager brain, many legs</text>"
              "<text x='84' y='420' font-family=\"%s\" font-size='26' fill='#3b3a4a'>A coding-agent harness whose prompt cache</text>"
              "<text x='84' y='458' font-family=\"%s\" font-size='26' fill='#3b3a4a'>is the shared memory of a team of agents.</text></svg>"
              % (mark(**LIGHT), DEFS, FONT, FONT, FONT, FONT))
    sheet("social.html", social, 1280, 640, bg="#f3eee3")
    sheet("favicon-test.html", "<div style='display:flex;gap:24px;padding:24px;background:#fff;align-items:end'>"
          + "".join("<div style='width:%dpx;height:%dpx'>%s</div>" % (s, s, svg_favicon()) for s in (128, 64, 32, 16)) + "</div>", 400, 180, bg="#fff")
