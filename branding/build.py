#!/usr/bin/env python3
"""Generate the mail-muncher brand assets.

Everything is authored on an exact pixel grid and emitted as SVG made of
`<rect>` elements with `shape-rendering="crispEdges"`. The SVGs under
`docs/assets/brand/` are the masters the site and the README consume; this
script regenerates them, plus the PNG rasters, from the grids below.

    python3 branding/build.py

Requires `rsvg-convert` (librsvg) for the PNGs and `fonttools` for converting
the Silkscreen wordmark to outlines so no SVG consumer needs the font file.

The mark carries its own warm palette, sampled from the reference drawing; the
site and the wordmark carry the UI palette. See BRAND.md for both.
"""

import os
import subprocess

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "docs", "assets", "brand")
FONT = os.path.join(ROOT, "branding", "fonts", "Silkscreen-Bold.ttf")

# ---------------------------------------------------------- mark palette ---
# Nine values, all but the accent sampled straight out of the reference. The
# mark is warmer than the interface on purpose: the UI palette is tuned for
# text contrast, and forcing the drawing onto it turned its creams grey.
PAL = {
    "o": "#573923",  # case keyline, and the envelope's edge inside the mouth
    "b": "#785331",  # screen bezel, mouth rim, the envelope's own keyline
    "a": "#A8835D",  # case shade: undersides, the neck, edges turned away
    "s": "#BC9F81",  # paper shade: the envelope's underside, the plinth
    "t": "#D7B893",  # case tan: the CRT body
    "m": "#E4CEB3",  # envelope paper - one step lighter than the case
    "l": "#EFDEC5",  # highlight cream: teeth, eye whites, lit top-left edges
    "k": "#000000",  # the screen, and the mouth's gape
    "r": "#E0533D",  # chevron, blush, envelope seams, power LED  (Accent light)
}
# The keyline is the only value that differs between the two variants. On light
# paper the tan case needs the dark line to hold its silhouette (tan on Paper is
# 1.78:1); on a dark page that brown sinks into the background (1.77:1), so the
# case's edge steps up to the shade value. The drawing is otherwise identical.
PAL_LIGHT = dict(PAL)
PAL_DARK = dict(PAL, o="#A8835D")

# ------------------------------------------------------------- UI values ---
FIELD = "#17130F"  # social card background                       (Paper dark)
PAPER = "#FBF8F5"  # opaque icon background                       (Paper light)
INK = "#1F1A17"    # wordmark on light surfaces                   (Ink light)
CREAM = "#F4EFEA"  # wordmark on dark surfaces                    (Ink dark)
MUTED_D = "#A79C93"
ACCENT = "#E0533D"


# ------------------------------------------------------------ grid tools ---
def grid(w, h, fill="."):
    return [[fill] * w for _ in range(h)]


def rect(g, x0, y0, x1, y1, c):
    for y in range(y0, y1 + 1):
        for x in range(x0, x1 + 1):
            if 0 <= y < len(g) and 0 <= x < len(g[0]):
                g[y][x] = c


def rows(g):
    return ["".join(r) for r in g]


# ------------------------------------------------------- the 60x60 mark ---
# A cell-for-cell transcription of the approved reference drawing.
#
# The reference is itself a 53 x 53 bitmap: sampling it on a 53-cell pitch
# reproduces it exactly, with not one impure cell, so the drawing's own grid is
# the grid used here - no shape was re-laid, resampled or re-proportioned. The
# 53 x 53 drawing is placed on a 60 x 60 canvas (offset 3, 3) purely for margin;
# 60 divides every raster this repo ships (480/60 = 8, 240/60 = 4, 180/60 = 3,
# 120/60 = 2) so every PNG lands on exact pixel boundaries.
#
# What was fixed is execution only. The reference dithers its keylines between
# the two browns, its shade bands between the two mid tans and its lit edges
# between the two creams; each of those runs is flattened to the single value
# the run is plainly meant to be. Nothing moved.
MARK = [
    "............................................................",
    "............................................................",
    "............................................................",
    "............................................................",
    "......................oooooooooooooooooooo..................",
    ".............oooooooooaaaaaaaaaaaaaaaaaaaaoooo..............",
    "........ooooollllllllllllllllllllllllllllllllloo............",
    "......oollllllttttttttttttttttttttttttttttttttllo...........",
    ".....oollltttttttttttttttttttttttttttttttttttttllo..........",
    "....ollltttttttttttttttttttttttttttttttttttttttttlo.........",
    "....ollttttttttbbbbbbbbbbbbbbbbbbbbbbbbbbbbbtttttlo.........",
    "...ollttttbbbbbkkkkkkkkkkkkkkkkkkkkkkkkkkkkkbbttttlo........",
    "...ollttttbkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkbtttlo........",
    "...olttttbkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkbttto........",
    "...olttttbkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkbttto........",
    "...oltttbkkkkkkkkkkklkkkkkkkkkkkkkkkkkkklkkkkkkbttto........",
    "...oltttbkkkkkkkkkkkkllkkkkkkkkkkkkkkkllkkkkkkkbttto........",
    "...oltttbkkkkkkkkkkkllllkkkkkkkkkkkkkllkkkkkkkkbttto........",
    "...oltttbkkkrrkkkkkkllkklkkkkkkkkkkkllkklkkkkkkbttto........",
    "...oltttbkkkkrrkkkkkllkkkkkkkkkkkkkkllkkkkkkkkkbttto........",
    "...oltttbkkkkkrrkkkkllkkkkkkkkkkkkkkllkkkkkkkkkbttto........",
    "...oltttbkkkkkkrrkkkkllkklkkkkkkkkkklllkkrrrkkkbttto........",
    "...oltttbkkkkkrrkkkkkkkkkkkkkkkkkkkkkkkkkrrrkkkbttto........",
    "...oltttbkkkkrrkkkkrrrkkkkkkkkkkkkkkkkkkkkkkkkkbttto........",
    "...oltttbkkkrrkkkkkrrrkkkkkkkkkkkkkkkkkkkkkkkkkbttlo........",
    "...oltttbkkkkkkkkkkkkkkkkkkkkbbbbbbbbbbbbkkbbbbbbbbbbbb.....",
    "...oltttbkkkkkkkkkkkkkkkblllllllllllllllloalllllllllllrb....",
    "...oltttbkkkkkkkkkkkkkkbblllklllklllklllbammlmmlmmmlmrtb....",
    "...oltttbkkkkkkkkkkkkkbtkklkkklkkklkorlabmmmmmmmmmmmrmmb....",
    "...oltttbkkkkkkkkkkkkkbbkkkkkkkkkkkoatrrlmmmmmmmmmmrmmmb....",
    "...oltttbkkkkkkkkkkkkblkkkkkkkkkkkotmmmrrmmmmmmmmrrmmmmb....",
    "...oltttbkkkkkkkkkkkkblkkkkkkkkkkkotmmmmrrmmmmmmrrtmmmmb....",
    "...oltttbkkkkkkkkkkkkblkkkkkkkkkkkotmmmmmrrrmmtrrmmmmmmb....",
    "...oltttbkkkkkkkkkkkkblkkkkkkkkkkkotmmmmrtrrrrrsrrmmmmmb....",
    "...oltttbkkkkkkkkkkkkktkkkkkkkkkkkotmmmrmmmasssmmmrmmmmb....",
    "...oltttbkkkkkkkkkkkkkbtkklkkklkkkotmmrmmmmmmmmmmmlrmmmb....",
    "...oltttbkkkkkkkkkkkkkkbbllklklkklotmrlmmmmmmmmmmmmlrmmb....",
    "...olttttbkkkkkkkkkkkkkkbllllllllllorlmmmmmmmmmmmmmmlrmb....",
    "...olttttbkkkkkkkkkkkkkkkbssssssssssoassssssssssssssstrb....",
    "...olttttbbkkkkkkkkkkkkkkkkkkkkkkkkkbbbbbbbbbbbbbbbbbbb.....",
    "...oltttttbbbkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkbbbbbbbo........",
    "...oltttttttbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbttttttoo........",
    "....ottttttttlllllllllllllllllllllllllllllllttttttao........",
    "....ootttttttttttttttttttttttttttttttttttttttttttao.........",
    ".....ottttttttttttttttttttttttttttttttttttrrrtttaao.........",
    "......oattttttttttttttttttttttttttttttttttorrttaoo..........",
    ".......oooaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaooo............",
    "..........ooooooooooooooooooooooooooooooooooo...............",
    "...................obaaaaaaaaaaaaabo........................",
    ".................ooobaaaaaaaaaaaaabooo......................",
    "...............obaaabatssssssssssabaaabo....................",
    "..............olllllssasssssssssassllllso...................",
    ".............olllllllllllllllllllllllllllo..................",
    "............ossmmmmmmmmmmmmmmmmmmmmmmmmmsso.................",
    "............ooooooooooooooooooooooooooooooo.................",
    "............................................................",
    "............................................................",
    "............................................................",
    "............................................................",
    "............................................................",
]


# ------------------------------------------------------- the 16x16 mark ---
def mark16():
    """Favicon mark: the same character at one device pixel per grid cell.

    The chevron, the envelope, the blush and the case shading are deliberately
    absent - at 16 device pixels they collapse into noise and take the
    silhouette with them. What survives is the chunky CRT, the brow step that
    carries the expression, a toothed mouth and one red LED in the same
    bottom-right corner the full mark puts it. Rendered at 16 and looked at, not
    assumed; an earlier version put the eyes and the mouth on adjacent rows and
    the white shapes fused into one blob.
    """
    g = grid(16, 16)
    rect(g, 2, 1, 13, 1, "o")
    rect(g, 1, 2, 14, 2, "o")
    rect(g, 2, 2, 13, 2, "t")
    for y in range(3, 10):
        g[y][1] = "o"
        g[y][2] = "t"
        rect(g, 3, y, 12, y, "k")
        g[y][13] = "t"
        g[y][14] = "o"
    rect(g, 1, 10, 14, 10, "o")
    rect(g, 2, 10, 13, 10, "t")
    rect(g, 2, 11, 13, 11, "o")
    rect(g, 6, 12, 9, 12, "a")               # neck
    g[12][6] = g[12][9] = "o"
    rect(g, 3, 13, 12, 13, "t")              # plinth
    g[13][3] = g[13][12] = "o"
    rect(g, 3, 14, 12, 14, "o")

    for x, y in ((4, 3), (4, 4), (5, 4), (4, 5), (5, 5)):
        g[y][x] = "l"                        # the loose top pixel is the brow
    for x, y in ((11, 3), (10, 4), (11, 4), (10, 5), (11, 5)):
        g[y][x] = "l"
    rect(g, 5, 7, 10, 8, "l")                # mouth, one dark row clear of the
    g[8][6] = g[8][9] = "k"                  # eyes, with two gaps for teeth
    rect(g, 10, 10, 11, 10, "r")             # power LED, same corner as the
    #                                          full mark puts it
    return rows(g)


# ------------------------------------------------------------------ SVG ---
def svg_rects(rws, dx=0, dy=0, pal=PAL):
    out = []
    w = len(rws[0])
    for y, row in enumerate(rws):
        x = 0
        while x < w:
            c = row[x]
            if c == ".":
                x += 1
                continue
            n = 1
            while x + n < w and row[x + n] == c:
                n += 1
            out.append(
                f'<rect x="{x + dx}" y="{y + dy}" width="{n}" height="1" '
                f'fill="{pal[c]}"/>'
            )
            x += n
    return out


def svg_doc(w, h, body, title, desc):
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" '
        f'width="{w}" height="{h}" role="img" aria-labelledby="t d" '
        f'shape-rendering="crispEdges">\n'
        f"<title id=\"t\">{title}</title>\n<desc id=\"d\">{desc}</desc>\n"
        + "\n".join(body)
        + "\n</svg>\n"
    )


DESC = ("A tan CRT monitor with an angry face on its dark screen, biting an "
        "envelope in half.")
DESC_SM = "A tan CRT monitor with two angry eyes on its dark screen."


# ------------------------------------------------------------- wordmark ---
class Wordmark:
    """Silkscreen Bold, converted to outlines so no consumer needs the font."""

    def __init__(self, path):
        from fontTools.ttLib import TTFont
        from fontTools.pens.svgPathPen import SVGPathPen

        self.font = TTFont(path)
        self.upem = self.font["head"].unitsPerEm
        self.cmap = self.font.getBestCmap()
        self.gs = self.font.getGlyphSet()
        self.SVGPathPen = SVGPathPen

    def advance(self, ch):
        name = self.cmap[ord(ch)]
        return self.font["hmtx"][name][0]

    def paths(self, text, size, x, y, fill):
        """`y` is the baseline. `size` must be a multiple of 8 to stay crisp."""
        out = []
        pen_x = 0.0
        k = size / self.upem
        for ch in text:
            name = self.cmap[ord(ch)]
            pen = self.SVGPathPen(self.gs)
            self.gs[name].draw(pen)
            d = pen.getCommands()
            if d:
                out.append(
                    f'<path d="{d}" fill="{fill}" '
                    f'transform="translate({x + pen_x * k:g} {y}) '
                    f'scale({k:g} {-k:g})"/>'
                )
            pen_x += self.advance(ch)
        return out, pen_x * k


# --------------------------------------------------------------- outputs ---
def write(name, text):
    p = os.path.join(OUT, name)
    with open(p, "w") as f:
        f.write(text)
    return p


def png(src, dst, w, h=None):
    subprocess.run(
        ["rsvg-convert", "-w", str(w), "-h", str(h or w),
         os.path.join(OUT, src), "-o", os.path.join(OUT, dst)],
        check=True,
    )


def png_padded(src, dst, target, multiple, grid_size, bg):
    """Render at an exact integer multiple, then pad to `target` on `bg`.

    Bitmap art only stays crisp at whole-number scales, so a 180px icon is a
    144px mark on a 180px field rather than a 3.75x smear.
    """
    inner = grid_size * multiple
    tmp = os.path.join(OUT, ".tmp.png")
    subprocess.run(
        ["rsvg-convert", "-w", str(inner), "-h", str(inner),
         os.path.join(OUT, src), "-o", tmp], check=True)
    subprocess.run(
        ["magick", tmp, "-background", bg, "-gravity", "center",
         "-extent", f"{target}x{target}", os.path.join(OUT, dst)], check=True)
    os.remove(tmp)


def main():
    os.makedirs(OUT, exist_ok=True)
    m60 = MARK
    m16 = mark16()

    # 1. the marks. Free-standing on a transparent background, in two keyline
    #    weights - see PAL_LIGHT / PAL_DARK above.
    for suffix, pal in (("", PAL_LIGHT), ("-dark", PAL_DARK)):
        write(f"mark{suffix}.svg",
              svg_doc(60, 60, svg_rects(m60, pal=pal), "mail-muncher", DESC))
        write(f"mark-small{suffix}.svg",
              svg_doc(16, 16, svg_rects(m16, pal=pal), "mail-muncher", DESC_SM))

    # 2. lockups: small mark + Silkscreen Bold wordmark ------------------------
    wm = Wordmark(FONT)
    for name, pal, fill in (("lockup.svg", PAL_LIGHT, INK),
                            ("lockup-dark.svg", PAL_DARK, CREAM)):
        body = svg_rects(m16, pal=pal)
        # font-size 8 units => one font pixel is one mark pixel
        paths, tw = wm.paths("mail-muncher", 8, 20, 12, fill)
        write(name, svg_doc(int(20 + tw + 1), 16, body + paths,
                            "mail-muncher", DESC_SM))

    # 3. rasters ---------------------------------------------------------------
    png("mark.svg", "mark-480.png", 480)                # 60 * 8
    png("mark-dark.svg", "mark-dark-480.png", 480)
    png("mark-small.svg", "favicon-16.png", 16)
    png("mark-small.svg", "favicon-32.png", 32)
    png("mark-small.svg", "favicon-48.png", 48)
    # iOS composites the icon onto its own background, so this one is opaque.
    png_padded("mark.svg", "apple-touch-icon.png", 180, 3, 60, PAPER)
    # 4x the 16-unit lockup grid; a PNG fallback for anywhere SVG is awkward.
    png("lockup.svg", "lockup-408.png", 408, 64)
    png("lockup-dark.svg", "lockup-dark-408.png", 408, 64)

    # 4. social preview --------------------------------------------------------
    body = [
        f'<rect width="1280" height="640" fill="{FIELD}"/>',
        '<g transform="translate(72 80) scale(8)">'
        + "".join(svg_rects(m60, pal=PAL_DARK)) + "</g>",
    ]
    body += wm.paths("mail-muncher", 64, 576, 288, CREAM)[0]
    body += wm.paths("an email client for AI agents", 24, 576, 344, MUTED_D)[0]
    body.append(f'<rect x="576" y="376" width="192" height="8" fill="{ACCENT}"/>')
    body += wm.paths("gmail.readonly — rules — files on disk",
                     16, 576, 432, MUTED_D)[0]
    write("social-preview.svg",
          svg_doc(1280, 640, body, "mail-muncher",
                  "mail-muncher — an email client for AI agents."))
    png("social-preview.svg", "social-preview.png", 1280, 640)

    print("wrote", OUT)


if __name__ == "__main__":
    main()
