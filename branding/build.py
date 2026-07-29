#!/usr/bin/env python3
"""Generate the mail-muncher brand assets.

Everything is authored on an exact pixel grid and emitted as SVG made of
`<rect>` elements with `shape-rendering="crispEdges"`. The SVGs under
`docs/assets/brand/` are the masters the site and the README consume; this
script regenerates them, plus the PNG rasters, from the grids below.

    python3 branding/build.py

Requires `rsvg-convert` (librsvg) for the PNGs and `fonttools` for converting
the Silkscreen wordmark to outlines so no SVG consumer needs the font file.

Every colour is one of the values in the brand palette; see BRAND.md.
"""

import os
import subprocess

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "docs", "assets", "brand")
FONT = os.path.join(ROOT, "branding", "fonts", "Silkscreen-Bold.ttf")

# --------------------------------------------------------------- palette ---
# Role -> hex. Only values from the brand palette table are permitted here.
PAL = {
    "o": "#6B625C",  # keyline round case, envelope and stand     (Muted light)
    "c": "#F1EBE4",  # case cream and envelope paper              (Surface light)
    "d": "#A79C93",  # shade: screen bezel, lit-away edges, neck  (Muted dark)
    "s": "#17130F",  # screen and the mouth's interior            (Paper dark)
    "t": "#FBF8F5",  # teeth, eye whites and lit edges            (Paper light)
    "r": "#E0533D",  # chevron, blush, envelope seams, power LED  (Accent light)
}
# The keyline is the only value that differs between the two variants: on light
# paper the cream case needs Ink to hold its silhouette (cream on Paper is
# 1.12:1); on a dark page Ink would be invisible and Muted draws the same edge
# the reference does.
PAL_LIGHT = dict(PAL, o="#1F1A17")   # Ink (light)  - for light surfaces
PAL_DARK = dict(PAL, o="#6B625C")    # Muted (light) - for dark surfaces
FIELD = "#17130F"  # social card background                       (Paper dark)
PAPER = "#FBF8F5"  # opaque icon background                        (Paper light)
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


def stamp(g, x0, y0, art, c):
    cells = []
    for i, row in enumerate(art):
        for j, ch in enumerate(row):
            if ch == "#":
                g[y0 + i][x0 + j] = c
                cells.append((x0 + j, y0 + i))
    return cells


def rows(g):
    return ["".join(r) for r in g]


# ------------------------------------------------------- the 60x60 mark ---
# The mark is a transcription of the approved reference drawing, cell for cell.
# The reference is drawn on two different pixel pitches - the case on roughly
# 19.5 device pixels, the face on roughly 15.3 - so the first job was to pick
# one. Re-laying the case on the coarser pitch would cost the eyes their brow
# step and the mouth its teeth; re-laying the face on the finer pitch costs
# nothing but grid cells. The finer pitch puts the whole drawing on 60 x 60.
# 480/60, 240/60, 180/60 and 120/60 are all whole numbers, so every raster below
# lands on exact pixel boundaries.

# Case silhouette: row -> (first column, last column). The top corners sweep far
# wider than the bottom ones. That is the CRT's bulge and it is in the reference.
CASE = {y: (3, 52) for y in range(4, 49)}
CASE.update({4: (18, 37), 5: (11, 44), 6: (7, 48), 7: (5, 50), 8: (4, 51),
             46: (4, 51), 47: (6, 49), 48: (9, 46)})

# The screen aperture, same treatment.
SCREEN = {y: (8, 47) for y in range(10, 42)}
SCREEN.update({10: (14, 41), 11: (11, 44), 12: (10, 45), 13: (9, 46),
               40: (10, 45), 41: (12, 43)})

# The prompt chevron: a two-pixel-thick ">" at the screen's left edge.
CHEVRON = ["##...",
           ".##..",
           "..##.",
           "...##",
           "..##.",
           ".##..",
           "##..."]

# The eyes. The stepped tail climbing off the top-outer corner of each pupil is
# the brow, and it is the whole expression. They are not mirror images of each
# other in the reference, so they are not mirrored here; the loose squares are
# glints, and the left eye has two while the right eye has one.
EYE_L = ["#.....",
         ".##...",
         "####..",
         "##..#.",
         "##....",
         "##....",
         ".##..#"]
EYE_R = ["....#.",
         "...#..",
         ".##...",
         "##..#.",
         "##....",
         "##....",
         ".##..."]

# The mouth: a rounded opening with one shade pixel on its outer edge and one
# white pixel inside that, carrying the teeth on the rim. The upper jaw runs on
# over the envelope, which is what makes the envelope read as half-swallowed
# rather than merely adjacent. 'd' shade, 't' white, '-' the dark gape, 'o' the
# keyline that stops the white teeth merging into the cream envelope behind
# them, ' ' leave whatever is underneath.
MOUTH = [
    "        ddddddddddddd   ",
    "  dtttttttttttttttttto  ",
    " dttttt-ttt-ttt-ttto    ",
    " dt--t---t---t--o       ",
    "dt-------------o        ",
    "dt-----------           ",
    "dt-----------           ",
    "dt-----------           ",
    "dt-----------           ",
    "dt-----------           ",
    " dt--t-------           ",
    " dtttt-t-t---           ",
    "  dtttttttt-t           ",
    "   dttttttttttto        ",
    "    ddddddddddddo       ",
]

# The envelope's seams, in envelope-local (row, columns). Read together they are
# an X: the flap's V from the two top corners down to a flat centre, and two
# more diagonals climbing from the bottom corners to meet it.
SEAMS = {
    1:  (33, 34),
    2:  (33,),
    3:  (16, 32),
    4:  (17, 18, 31),
    5:  (18, 19, 29, 30),
    6:  (19, 20, 28, 29),
    7:  (20, 21, 27, 28),
    8:  (19, 20, 22, 26, 27, 28, 29),
    9:  (18, 19, 23, 24, 25, 26, 29, 30),
    10: (17, 18, 30, 31),
    11: (16, 17, 31, 32),
    12: (14, 15, 16, 32, 33),
    13: (15, 33, 34),
    14: (34,),
}


def mark60():
    """A cream CRT with an angry face, chomping an envelope."""
    g = grid(60, 60)

    # ---- case ---------------------------------------------------------------
    for y, (l, r) in CASE.items():
        rect(g, l, y, r, y, "c")
    for y, (l, r) in CASE.items():          # lit on the top-left, shaded away
        g[y][l] = g[y][r] = "o"
        if l + 1 <= r - 1:
            g[y][l + 1] = "t"
            g[y][r - 1] = "d"
        if l + 2 <= r - 2:
            g[y][r - 2] = "d"
    top, bot = min(CASE), max(CASE)
    for x in range(CASE[top][0], CASE[top][1] + 1):
        g[top][x] = "o"
    for x in range(CASE[top + 1][0] + 1, CASE[top + 1][1]):
        g[top + 1][x] = "t"
    for x in range(CASE[bot][0], CASE[bot][1] + 1):
        g[bot][x] = "o"
    for x in range(CASE[bot - 1][0] + 1, CASE[bot - 1][1]):
        g[bot - 1][x] = "d"

    # ---- stand: a splayed neck under a bright plinth -------------------------
    for y, l, r in ((49, 19, 36), (50, 16, 38), (51, 14, 40), (52, 13, 41)):
        rect(g, l, y, r, y, "d")
        g[y][l] = g[y][r] = "o"
    for y, fill in ((53, "t"), (54, "c"), (55, "o")):
        rect(g, 11, y, 43, y, fill)
        g[y][11] = g[y][43] = "o"

    # ---- screen, recessed behind one pixel of bezel --------------------------
    for y, (l, r) in SCREEN.items():
        rect(g, l, y, r, y, "s")
    for y, (l, r) in SCREEN.items():
        for x in (l - 1, r + 1):
            if g[y][x] in ("c", "t", "d"):
                g[y][x] = "d"
    for y, ref in ((min(SCREEN) - 1, SCREEN[min(SCREEN)]),
                   (max(SCREEN) + 1, SCREEN[max(SCREEN)])):
        for x in range(ref[0] - 1, ref[1] + 2):
            if g[y][x] in ("c", "t"):
                g[y][x] = "d"
    for x in range(CASE[43][0] + 2, CASE[43][1] - 1):   # lit front panel edge
        if g[43][x] == "c":
            g[43][x] = "t"

    # ---- face ---------------------------------------------------------------
    stamp(g, 11, 18, CHEVRON, "r")
    stamp(g, 20, 15, EYE_L, "t")
    stamp(g, 36, 15, EYE_R, "t")
    rect(g, 19, 23, 21, 24, "r")            # blush: blocks, not underscores
    rect(g, 42, 21, 44, 22, "r")

    # ---- envelope, then the jaws closing over its left end -------------------
    EX, EY = 34, 25
    rect(g, EX, EY, EX + 22, EY + 14, "c")
    rect(g, EX, EY + 1, EX + 22, EY + 1, "t")          # lit top edge
    for x in range(EX, EX + 23):
        g[EY][x] = g[EY + 14][x] = "o"
    for y in range(EY, EY + 15):
        g[y][EX] = g[y][EX + 22] = "o"
    for m, cols in SEAMS.items():
        for k in cols:
            g[EY + m][EX + k - 13] = "r"
    for i, row in enumerate(MOUTH):
        for j, ch in enumerate(row):
            if ch != " ":
                g[25 + i][21 + j] = {"t": "t", "d": "d", "-": "s", "o": "o"}[ch]

    # ---- power LED on the lower bezel ---------------------------------------
    rect(g, 43, 45, 45, 46, "r")

    return rows(g)


# ------------------------------------------------------- the 16x16 mark ---
def mark16():
    """Favicon mark: the same character at one device pixel per grid cell.

    The chevron, the envelope, the blush and the case shading are deliberately
    absent - at 16 device pixels they collapse into noise and take the
    silhouette with them. What survives is the chunky CRT, the brow step that
    carries the expression, a toothed mouth and one red LED. Rendered at 16 and
    looked at, not assumed; an earlier version put the eyes and the mouth on
    adjacent rows and the white shapes fused into one blob.
    """
    g = grid(16, 16)
    rect(g, 2, 1, 13, 1, "o")
    rect(g, 1, 2, 14, 2, "o")
    rect(g, 2, 2, 13, 2, "c")
    for y in range(3, 10):
        g[y][1] = "o"
        g[y][2] = "c"
        rect(g, 3, y, 12, y, "s")
        g[y][13] = "c"
        g[y][14] = "o"
    rect(g, 1, 10, 14, 10, "o")
    rect(g, 2, 10, 13, 10, "c")
    rect(g, 2, 11, 13, 11, "o")
    rect(g, 6, 12, 9, 12, "d")               # neck
    g[12][6] = g[12][9] = "o"
    rect(g, 3, 13, 12, 13, "c")              # plinth
    g[13][3] = g[13][12] = "o"
    rect(g, 3, 14, 12, 14, "o")

    for x, y in ((4, 3), (4, 4), (5, 4), (4, 5), (5, 5)):
        g[y][x] = "t"                        # the loose top pixel is the brow
    for x, y in ((11, 3), (10, 4), (11, 4), (10, 5), (11, 5)):
        g[y][x] = "t"
    rect(g, 5, 7, 10, 8, "t")                # mouth, one dark row clear of the
    g[8][6] = g[8][9] = "s"                  # eyes, with two gaps for teeth
    rect(g, 3, 10, 4, 10, "r")                # power LED
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


DESC = ("A cream CRT monitor with an angry face on its dark screen, biting "
        "an envelope in half.")
DESC_SM = "A cream CRT monitor with two angry eyes on its dark screen."


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
    m60 = mark60()
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
