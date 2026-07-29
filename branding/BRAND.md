# mail-muncher brand

mail-muncher is an email client for AI agents: it pulls Gmail with a read-only
scope, filters it with composable rules, and drops the matches on disk as files.
It is a small, sharp, terminal-native tool, and the identity should read that
way — retro-terminal, warm, precise. Not corporate SaaS, not neon cyberpunk.

Everything here is authored on an exact pixel grid. The masters are SVG built
from `<rect>` elements with `shape-rendering="crispEdges"`; they scale
losslessly, stay under 30 KB, and diff in git.

---

## Palette

Eleven values. This is the **interface** palette: the site, the wordmark, the
social card and every piece of chrome are drawn from it and from nothing else.

The mark is the one exception, and a deliberate one — it carries its own warmer
set, listed under [The mark's palette](#the-marks-palette). The two meet at
Accent `#E0533D`, which is the same value in both.

| Role | Light | Dark | Use |
| --- | --- | --- | --- |
| Accent | `#E0533D` | `#F2765F` | Links (dark only), logo mark, active nav, the one thing that pops |
| Accent deep | `#B33E2B` | `#E0533D` | Links (light only), hover/pressed, headings that need weight |
| Ink | `#1F1A17` | `#F4EFEA` | Body text |
| Paper | `#FBF8F5` | `#17130F` | Background — warm, never pure white or black |
| Muted | `#6B625C` | `#A79C93` | Secondary text, borders, code comments |
| Surface | `#F1EBE4` | `#221C18` | Code blocks, cards, table stripes, sidebar |

`#E0533D` was already the accent in `katra/config.yml`; this palette extends
existing project identity rather than replacing it.

### Contrast, measured

Every foreground/background pair that actually ships, computed with the WCAG 2.1
relative-luminance formula. AA needs **4.5:1** for body-size text, **3:1** for
large text (≥ 24px, or ≥ 19px bold) and for non-text UI components.

**Light scheme**

| Ratio | Pair | Where | Verdict |
| --- | --- | --- | --- |
| 16.29:1 | Ink `#1F1A17` on Paper `#FBF8F5` | body text | AAA |
| 14.56:1 | Ink on Surface `#F1EBE4` | code blocks, sidebar, table stripes | AAA |
| 5.63:1 | Muted `#6B625C` on Paper | secondary text, search previews | AA |
| 5.03:1 | Muted on Surface | secondary text on cards | AA |
| **5.45:1** | **Accent deep `#B33E2B` on Paper** | **link text** | **AA** |
| 4.87:1 | Accent deep on Surface | links inside code and sidebar | AA |
| 5.45:1 | Paper on Accent deep | primary button label | AA |
| **3.63:1** | **Accent `#E0533D` on Paper** | **never used for text** | **fails AA** |
| 3.24:1 | Accent on Surface | never used for text | fails AA |

The plain Accent on Paper is the trap. **3.63:1 passes for large text and for UI
component boundaries and fails AA for body-size text**, so on light backgrounds
the Accent is never a text colour — `$link-color` takes Accent deep instead. The
Accent still appears on light pages as a graphical element (the rule under the
social-card wordmark, the logo mark on its dark field), where 3:1 is the bar.

**Dark scheme**

| Ratio | Pair | Where | Verdict |
| --- | --- | --- | --- |
| 16.17:1 | Ink `#F4EFEA` on Paper `#17130F` | body text | AAA |
| 14.74:1 | Ink on Surface `#221C18` | code blocks, sidebar, tables | AAA |
| 6.88:1 | Muted `#A79C93` on Paper | secondary text | AA |
| 6.27:1 | Muted on Surface | secondary text on cards | AA |
| **6.63:1** | **Accent `#F2765F` on Paper** | **link text** | **AA** |
| 6.04:1 | Accent on Surface | links inside code and sidebar | AA |
| 6.63:1 | Paper on Accent | primary button label | AA |
| 4.82:1 | Accent deep `#E0533D` on Paper | hover and pressed states | AA |

The relationship inverts: on dark, the Accent is the readable one and Accent
deep is the emphasis. That is why `$link-color` differs between the two schemes.

The mark's own ratios are in [The mark's palette](#the-marks-palette) below.

---

## The mark's palette

**The mark is warmer than the interface, on purpose.** The UI palette is tuned
first for text contrast, and its neutrals are cool; running the drawing through
it turned the reference's tans and creams grey and cost the character its
complexion. So the mark keeps its own set, sampled straight out of the approved
reference, and the site keeps the table above. They are not in competition — the
mark is a picture, not a UI surface, and nothing in it is text.

Nine values. Eight are the reference's own, taken by sampling; the ninth is
Accent, brought over from the UI palette so the mark and the site agree on their
one red.

| Role | Value | Where it goes |
| --- | --- | --- |
| **Keyline** | `#573923` | the case's silhouette, and the envelope's edge where it enters the mouth |
| **Line** | `#785331` | the screen bezel, the mouth's rim, the envelope's own border |
| **Case shade** | `#A8835D` | undersides of the case, the stand's neck, edges turned away from the light |
| **Paper shade** | `#BC9F81` | the envelope's shaded underside, the plinth, the lower lip |
| **Case tan** | `#D7B893` | the CRT's body — the mark's dominant value |
| **Envelope paper** | `#E4CEB3` | the envelope's face, one step lighter than the case |
| **Highlight** | `#EFDEC5` | teeth, eye whites, the lit top and left edges |
| **Screen** | `#000000` | the screen, and the mouth's gape |
| **Accent** | `#E0533D` | chevron, blush, envelope seams, power LED |

Each earns its place:

- **Two browns, not one.** The reference draws the case's outline darker than
  the envelope's, and that difference is load-bearing — see below.
- **Two shades, not one.** `#A8835D` is a step down from the case tan and
  `#BC9F81` a step down from the envelope paper. Merging them would put the
  case's shadow value on the envelope's underside and pull the envelope back
  towards the machine.
- **Three lights.** Case tan, envelope paper and highlight are the three
  surfaces the drawing actually distinguishes: the machine, the mail, and what
  the light hits. Nothing is left over.

Everything the reference contained beyond these — four near-identical tans that
differ by two units per channel, and dithered runs mixing the pairs above — was
anti-aliasing, and is gone.

### The envelope has to separate from the case

This is the one thing the mark can fail at, and it did once. Some numbers:

| Ratio | Pair | Note |
| --- | --- | --- |
| **1.23:1** | envelope paper `#E4CEB3` on case tan `#D7B893` | the fill difference **alone is not enough** |
| **3.63:1** | envelope border `#785331` on case tan | what actually does the separating |
| 4.47:1 | envelope border on envelope paper | the border reads from the inside too |
| 5.54:1 | case keyline `#573923` on case tan | the machine's edge, a step darker still |
| 15.92:1 | teeth `#EFDEC5` on screen `#000000` | why the teeth crossing the envelope's corner survive |

The lighter fill on its own is a 1.23:1 whisper. The envelope reads as a
separate object because it is **outlined** — its own border, one value lighter
than the case's keyline so the two edges do not merge into a single blob — and
because the upper jaw's teeth are drawn **over** its top-left corner, in the
brightest value in the mark, so the pixels that say *biting* are the ones with
the most contrast rather than the least.

### The rest of the mark's ratios

| Ratio | Pair | Note |
| --- | --- | --- |
| 19.85:1 | screen `#000000` on light Paper `#FBF8F5` | the dark mass the face sits in |
| 9.85:1 | keyline `#573923` on light Paper | what holds the light-surface silhouette |
| 9.83:1 | case tan `#D7B893` on dark Paper `#17130F` | the dark-surface silhouette |
| 5.47:1 | Accent `#E0533D` on screen | chevron and blush |
| 5.34:1 | dark-variant keyline `#A8835D` on dark Paper | non-text boundary, 3:1 bar |
| 2.52:1 | Accent on envelope paper | the seams — a texture on an already-outlined shape, not a boundary |
| **1.78:1** | **case tan on light Paper** | **why the keyline changes weight** |
| **1.77:1** | **keyline `#573923` on dark Paper** | **the same reason, inverted** |

Those last two rows are the whole reason for the two variants below.

---

## The mark

A chunky tan CRT with an angry face on its dark screen — two brow-slanted eyes,
red blush blocks, a red `>` prompt chevron — with an envelope halfway into its
open, toothed mouth. The case stays the dominant shape; the envelope breaks past
its right edge by four cells and no more.

### It is a transcription, not an interpretation

The mark comes from an approved reference illustration. **The drawing is not
ours to change.** Proportions, placement, expression, shapes and relative sizes
are all taken across as drawn: the brows that step up and away from each pupil,
the loose glint squares (two on the left eye, one on the right — they are not a
mirrored pair in the reference and they are not mirrored here), the blush as
small blocks rather than lines, the rounded mouth with its teeth carried on the
rim, the upper jaw's teeth passing **over** the envelope's top-left corner, the
`>` at the screen's left edge at the size the reference draws it, and the X of
seams across the envelope's face.

The reference is machine-generated, so only its *execution* was fixed:

- **Dithered keylines.** The reference alternates cell by cell between its two
  browns along the case's top and bottom edges and around the envelope. Each run
  was flattened to the single value it is plainly meant to be.
- **Dithered shade and highlight bands**, likewise: the band under the case's
  top edge mixes the two mid tans, the lit front panel mixes the two creams.
- **Four near-identical tans** — `#D7B893`, `#D9BA95`, `#D9BA93`, `#D7BA93`,
  which differ by two units per channel — became one value.
- **Stray single cells** left over from anti-aliasing: a brown pixel inside the
  chevron, a tan pixel in the middle of the lower lip, and so on.

Nothing moved. Every outline the reference draws deliberately — the envelope's
border, the mouth's rim, the case's keyline, the screen's bezel — is still
there, at its own value.

The case's shading is part of the drawing, not part of the execution: the lit
top and left edges take the highlight, the edges turned away take the case
shade, and the screen sits behind a one-cell bezel line. Flattening that would
lose the CRT's volume.

### Grid: 60 × 60

**The reference is itself a bitmap.** Measured, its colour transitions fall on a
uniform 9.434-device-pixel pitch across a 500px image — a 53 × 53 grid. Sampling
it at that pitch reproduces it exactly: **2809 cells, not one of them impure.**
So there was no pitch to choose and no shape to re-lay; the drawing's own grid
is the grid used here.

53 is a poor canvas number, though — it divides none of the rasters this repo
ships. The 53 × 53 drawing is therefore placed on a **60 × 60** canvas at offset
(3, 3), which is a translation and nothing else: no scaling, no resampling, no
cell merged or split. The extra cells are margin.

60 divides cleanly into every raster: 480/60 = 8, 240/60 = 4, 180/60 = 3,
120/60 = 2. Every PNG lands on exact pixel boundaries with no padding and no
fractional scale.

The grid lives in `branding/build.py` as 60 literal strings, one character per
cell. That is the master; it is meant to be readable and diffable, and an edit
to it is an edit to the logo.

### Two marks

| | File | Grid | Use at |
| --- | --- | --- | --- |
| **Full mark** | `mark.svg` / `mark-dark.svg` | 60 × 60 | **48px and up** |
| **Small mark** | `mark-small.svg` / `mark-small-dark.svg` | 16 × 16 | **below 48px** |

The full mark at 16px is mush — the teeth, the envelope and the chevron all
collapse into noise and take the silhouette with them. This was rendered and
looked at, not assumed. The small mark is derived from the same drawing and
drops the chevron, the envelope, the blush and the case shading, keeping what
still reads at one device pixel per grid cell: the chunky CRT in case tan, the
brow step that carries the expression, a mouth with two tooth gaps, and one red
LED on the bottom-right bezel — the corner the full mark puts it in.

Dropping the envelope is the painful one, since the envelope is the point. At 16
cells the whole mouth is six cells wide; an envelope inside it is two or three,
which is not an envelope, it is a smudge that also eats the teeth. The small
mark keeps the *muncher* and lets the full mark carry the *mail*.

The small mark was rendered at 16, 32 and 48 and looked at too. An earlier
version put the eyes and the mouth on adjacent rows and the white shapes fused
into a single blob; the shipped version keeps one dark row between them.

Both are the same character. Do not scale the full mark down past 48px, and do
not scale the small mark up past 48px — at 64px and above its coarseness reads
as a mistake rather than as a decision.

### Two keyline weights, one drawing

The case is tan. On light Paper the tan *fill* is **1.78:1** — the silhouette
has to come from the edge, and the reference's dark keyline gives 9.85:1, so the
drawing works as drawn. On dark Paper the relationship inverts: the tan fill is
9.83:1 and carries the shape by itself, but the keyline drops to **1.77:1** and
sinks into the page, so the drawn line stops being a line.

Re-tinting the case was considered and rejected — that is exactly the mistake
that made the previous mark grey, and it changes the character's complexion. A
dark badge field was considered and rejected too: it reads as a container the
logo is trapped in rather than as the logo.

So the **keyline** changes, and only the keyline. On dark it steps up to the
case shade, `#A8835D`, which is 5.34:1 against Paper dark and still reads as an
edge against the tan fill. Everything else — the tans, the creams, the shading,
the screen, the face, the envelope and its border — is identical between the two
files, so they are unmistakably the same object.

| Surface | Case keyline | File |
| --- | --- | --- |
| Light | `#573923` | `mark.svg`, `mark-small.svg`, `lockup.svg` |
| Dark | `#A8835D` | `mark-dark.svg`, `mark-small-dark.svg`, `lockup-dark.svg` |

The envelope's border stays `#785331` in both. It is not the silhouette value —
its job is to separate the mail from the machine, and that job is the same on
either surface.

Both are transparent PNG/SVG — **there is no background plate**. Do not add one.
The one exception is `apple-touch-icon.png`, which iOS renders opaque, so it
ships on Paper.

---

## Wordmark and type

**Silkscreen Bold**, by Jason Kottke, under the SIL Open Font License 1.1.
`branding/fonts/OFL.txt` and `docs/assets/fonts/OFL.txt` carry the licence text
next to the font files it covers, as the OFL requires. The files are
redistributed unmodified; no derivative is made and no Reserved Font Name is
declared in the licence as shipped, so the naming clause is not engaged.

Silkscreen is a bitmap face drawn on an **8px grid**. Two consequences that are
not optional:

1. **Only set it at whole multiples of 8px.** 16, 24, 32, 48. At 18px or 1.125rem
   it goes soft, which defeats the point of pairing it with pixel art.
2. **It has no lowercase forms.** `mail-muncher` and `MAIL-MUNCHER` render
   identically. Every heading set in Silkscreen is capitals whether you type
   them that way or not.

Because of (2), a `text-transform` is never needed and code inside a heading is
given back the monospace stack — reference headings here are frequently a bare
config key or tool name, and those are case-sensitive identifiers that a
capitals-only face would misstate.

**Glyph coverage checked** (the font is 226 codepoints, so this matters): em
dash `—`, en dash `–`, curly quotes `‘ ’ “ ”`, middot `·`, bullet `•`, ellipsis
`…`, degree `°`, `© × ™ € & > ? / : \` -`, and the Latin-1 accented set are all
present. **Arrows (`→`, U+2192) are not**, so a heading containing one falls
through to the fallback stack mid-line. Avoid arrows in h1 and h2.

Where it may be used:

| Yes | No |
| --- | --- |
| the wordmark and lockups | body copy |
| the social card | `h3` and below |
| `h1`, `h2` | nav lists, tables, code blocks |
| the sidebar site title (via the lockup) | anything below 16px |

Body text keeps a normal system stack. Headings get letter-spacing (2px at 32px,
1px at 24px) and a whole-pixel line-height (40px and 32px) — pixel faces are set
solid by default and read as a wall without it.

---

## Files

Masters and derived rasters both live in `docs/assets/brand/`, because that is
the one directory the site can serve from. `branding/` holds this document, the
generator, and the font's build-time source.

| File | What |
| --- | --- |
| `docs/assets/brand/mark.svg` | full mark, 60 grid, `#573923` keyline, for light surfaces |
| `docs/assets/brand/mark-dark.svg` | full mark, `#A8835D` keyline, for dark surfaces |
| `docs/assets/brand/mark-small.svg` | small mark, 16 grid, `#573923` keyline |
| `docs/assets/brand/mark-small-dark.svg` | small mark, `#A8835D` keyline |
| `docs/assets/brand/lockup.svg` | small mark + wordmark, for light surfaces |
| `docs/assets/brand/lockup-dark.svg` | same, cream wordmark, for dark surfaces |
| `docs/assets/brand/lockup-408.png` | lockup at 4x (408 x 64), for anywhere SVG is awkward |
| `docs/assets/brand/lockup-dark-408.png` | same, dark-surface variant |
| `docs/assets/brand/mark-480.png` | 480px light mark (60 × 8), transparent |
| `docs/assets/brand/mark-dark-480.png` | 480px dark-surface mark, transparent |
| `docs/assets/brand/apple-touch-icon.png` | 180px (60 × 3) on Paper — iOS wants opaque |
| `docs/assets/brand/favicon-16/32/48.png` | small mark at 1×, 2×, 3× |
| `docs/assets/brand/social-preview.png` | 1280 × 640 GitHub / OG card |
| `docs/assets/fonts/Silkscreen-Bold.woff2` | the webfont the site serves |
| `branding/build.py` | regenerates every one of the above |

Rasters are always rendered at a **whole multiple** of the grid, and padded to
the target if the target is not a multiple. On the 60 grid nothing needs padding
— 480, 240, 180 and 120 are all whole multiples of 60.

To regenerate:

```bash
python3 branding/build.py     # needs rsvg-convert, ImageMagick, fonttools
```

---

## On the docs site

| File | What |
| --- | --- |
| `docs/_sass/color_schemes/mail-muncher.scss` | the light scheme, selected by `color_scheme: mail-muncher` |
| `docs/_sass/custom/custom.scss` | the display face, the logo sizing, and the whole dark scheme |
| `docs/_sass/custom/setup.scss` | retints the theme's colour ramp so callouts stay on palette |
| `docs/_includes/head_custom.html` | favicons, `theme-color`, Open Graph image metadata |

Both schemes ship in **one** stylesheet. just-the-docs compiles one scheme per
file and switches by swapping the `<link>`, which flashes; instead the dark
values are a `prefers-color-scheme` block in the same file, so the browser
resolves them before first paint. No JavaScript, no toggle, no flash.

The dark block is explicit overrides rather than a re-import of the theme's
modules. That is not a style preference: GitHub Pages' Jekyll runs Ruby Sass,
which refuses a nested `@import` of any file carrying a root-only directive, and
just-the-docs' `content.scss` opens with `@charset`. The override list was
produced by compiling the light stylesheet and enumerating every declaration
that carries a palette colour — 66 rules. **If the theme is ever unpinned from
just-the-docs v0.12.0, re-run that enumeration**, or a new rule will be
light-only in dark mode.

One deliberate exception to the palette rule: the theme derives a few hover and
pressed states with `darken($link-color, 2%)` and similar, which produces values
that are not in the table. They are derivations of a palette colour rather than
new colours, and removing them would mean forking the theme.

---

## What not to do

- **Do not put a background plate behind the mark.** It is drawn to stand free.
  Pick the variant that matches the surface instead.
- **Do not use the dark-surface variant on light paper.** Its `#A8835D` keyline
  is 3.27:1 against Paper's near-white, and against the case tan it is 1.84:1 —
  the edge dissolves into the fill.
- **Do not use Accent `#E0533D` as a text colour on a light background.** 3.63:1
  fails AA. Accent deep `#B33E2B` is the light-mode text accent.
- **Do not scale the full mark below 48px**, and do not scale the small mark
  above it.
- **Do not resample the PNGs.** Re-render from the SVG at a whole multiple.
- **Do not set Silkscreen at a fractional or non-multiple-of-8 size**, and never
  at body-copy size.
- **Do not modify the font files.** They are redistributed under the OFL as
  received.
- **Do not invent a colour.** Six UI roles, eleven values, and `rgba()` of one
  of them when a border needs to be quieter than Muted. The mark's nine are a
  closed set too — they are sampled from the reference, not chosen, so there is
  nothing to extend them with.
- **Do not put the mark's tans and browns on the site**, and do not put the UI
  neutrals in the mark. The two palettes meet at Accent and nowhere else.
- **Do not add a drop shadow, a gradient, or an anti-aliased edge to the mark.**
  It is pixel art on an exact grid; every one of those breaks the grid.
- **Do not flatten the envelope into the case.** Its border and its lighter fill
  are what make the mark read as mail being eaten rather than as a monitor with
  a rectangle next to it. Same for the teeth that cross its top-left corner.
- **Do not stretch the lockup.** Its aspect is fixed; scale it uniformly, and
  prefer whole multiples of its 16-unit height.

---

## Credits

- Silkscreen by Jason Kottke — <https://github.com/googlefonts/silkscreen> —
  SIL Open Font License 1.1.
- Syntax highlighting on the docs site uses
  [accessible-pygments](https://github.com/Quansight-Labs/accessible-pygments)
  (`github-light` / `github-dark`), which ships with just-the-docs. Its token
  colours are outside this palette by design — they are tuned for contrast
  against the code background, and reducing them to six hues would lose the
  distinctions that make highlighting worth having.
