"""A hand-drawn-style sketch of the Sleipnir mark: a galloping horse with eight legs, each leg in a role colour."""
import math, random

ROLE = ["#bb9af7", "#7aa2f7", "#7aa2f7", "#7dcfff", "#7dcfff", "#9ece6a", "#e0af68", "#ff9e64"]
INK = "#24232e"

HIND = [((330, 292), (290, 345), (240, 385), (195, 420), (178, 434)),
        ((355, 294), (330, 350), (285, 400), (250, 440), (232, 454)),
        ((380, 294), (382, 345), (356, 395), (344, 440), (336, 456)),
        ((405, 294), (412, 340), (434, 384), (446, 418), (456, 428))]
FRONT = [((500, 292), (496, 345), (480, 395), (470, 440), (462, 456)),
         ((525, 294), (545, 345), (580, 385), (606, 420), (620, 430)),
         ((550, 292), (590, 335), (640, 360), (690, 385), (708, 392)),
         ((575, 288), (620, 318), (660, 336), (690, 330), (704, 322))]


def jitter(pts, seed, amp=1.2):
    r = random.Random(seed)
    return [(x + r.uniform(-amp, amp), y + r.uniform(-amp, amp)) for x, y in pts]


def smooth(pts):
    if len(pts) < 3:
        return "M%.1f %.1f L%.1f %.1f" % (pts[0] + pts[-1])
    d = "M%.1f %.1f" % pts[0]
    for i in range(len(pts) - 1):
        p0 = pts[i - 1] if i else pts[i]
        p1, p2 = pts[i], pts[i + 1]
        p3 = pts[i + 2] if i + 2 < len(pts) else pts[i + 1]
        c1 = (p1[0] + (p2[0] - p0[0]) / 6, p1[1] + (p2[1] - p0[1]) / 6)
        c2 = (p2[0] - (p3[0] - p1[0]) / 6, p2[1] - (p3[1] - p1[1]) / 6)
        d += " C%.1f %.1f %.1f %.1f %.1f %.1f" % (c1 + c2 + p2)
    return d


def leg(pts, color, seed, far=False):
    """Two tapered segments: a thick upper leg (hip to knee) and a slim lower leg (knee to hoof) with a hoof."""
    pts = jitter(pts, seed)
    upper, lower = pts[:3], pts[2:]
    du, dl = smooth(upper), smooth(lower)
    op = 0.55 if far else 1.0
    hx, hy = pts[-1]
    return ("<g opacity='%.2f'><path d='%s' stroke='%s' stroke-width='25' fill='none'/><path d='%s' stroke='%s' stroke-width='15' fill='none'/>"
            "<path d='%s' stroke='%s' stroke-width='14' fill='none'/><path d='%s' stroke='%s' stroke-width='8' fill='none'/>"
            "<path d='M%.1f %.1f l-6 10 l16 0 z' fill='%s' stroke='%s' stroke-width='3'/></g>") % (
        op, du, INK, du, color, dl, INK, dl, color, hx - 2, hy - 4, INK, INK)


def svg():
    body = ("M296 226 C310 192 380 184 450 192 C500 197 530 188 556 198 C590 212 600 250 582 280 "
            "C560 312 500 316 440 310 C380 304 318 300 300 270 C290 254 290 240 296 226 Z")
    neck = "M548 200 C580 160 604 118 634 92 L672 112 C668 150 648 182 624 212 C606 236 594 256 582 280"
    head = "M634 92 C640 66 660 62 668 76 C690 88 720 120 738 150 C746 166 740 182 724 180 C704 178 680 164 660 152"
    mane = "M548 200 C550 168 568 138 590 112 M560 194 C564 160 582 132 606 104 M574 184 C580 152 598 124 622 96 M538 208 C538 178 550 152 568 128"
    tail = "M300 228 C250 222 196 246 160 300 C150 318 150 336 160 346 C170 322 190 300 214 290 C190 326 186 350 196 368 C214 340 236 318 262 308"
    eye = "<circle cx='672' cy='102' r='5' fill='%s'/>" % INK
    nostril = "<circle cx='731' cy='166' r='3.2' fill='%s'/>" % INK
    parts = []
    for i, (p, c) in enumerate(zip(HIND + FRONT, ROLE)):
        parts.append((i in (0, 1, 4, 5), leg(p, c, i + 3, far=i in (0, 1, 4, 5))))
    far_legs = "".join(x for f, x in parts if f)
    near_legs = "".join(x for f, x in parts if not f)
    return ("""<!doctype html><meta charset=utf-8><style>
body{margin:0;background:#f3eee3;font-family:'Liberation Sans',sans-serif}
.sheet{position:relative;width:980px;height:640px;background:#f3eee3;overflow:hidden}
h1{position:absolute;left:54px;top:26px;margin:0;font:800 54px 'Liberation Sans',sans-serif;color:#24232e;letter-spacing:1px}
p{position:absolute;left:58px;top:92px;margin:0;font:italic 19px 'Liberation Sans',sans-serif;color:#6b6558}
.cap{position:absolute;left:58px;bottom:26px;font:italic 15px 'Liberation Sans',sans-serif;color:#6b6558}
</style>
<div class=sheet><h1>Sleipnir</h1><p>one manager brain, many legs</p>
<svg width=980 height=640 viewBox='0 0 980 640' fill=none stroke-linecap=round stroke-linejoin=round>
<defs><filter id=rough><feTurbulence type=fractalNoise baseFrequency=0.035 numOctaves=2 seed=4 result=n /><feDisplacementMap in=SourceGraphic in2=n scale=3.2 /></filter></defs>
<g filter=url(#rough) transform='translate(40 60)'>
  <path d='M120 470 C360 492 600 492 830 466' stroke='#c9c1ae' stroke-width='3' stroke-dasharray='4 9'/>
  %s
  <path d='%s' fill='#fbf8f1' stroke='%s' stroke-width='5'/>
  <path d='%s' fill='#fbf8f1' stroke='%s' stroke-width='5'/>
  <path d='%s' fill='#fbf8f1' stroke='%s' stroke-width='5'/>
  <path d='%s' stroke='#7048e8' stroke-width='5'/>
  <path d='%s' stroke='#7048e8' stroke-width='5' fill='none'/>
  %s%s
  %s
  <g stroke='#c2185b' stroke-width='3'><path d='M380 210 C420 226 470 224 520 214' opacity='.0'/></g>
</g></svg><div class=cap>eight legs: one for each rider on the shared prefix (a design sketch of the mark)</div></div>"""
            % (far_legs, body, INK, neck, INK, head, INK, mane, tail, eye, nostril, near_legs))


if __name__ == "__main__":
    import os
    open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "logo.html"), "w").write(svg())
    print("wrote logo.html")
