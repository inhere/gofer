# SVG Logo Design System

This workflow is informed by the MIT-licensed [op7418/logo-generator-skill](https://github.com/op7418/logo-generator-skill), with API-dependent showcase generation replaced by deterministic local SVG tooling.

## Concept selection

Build three directions from different visual mechanisms:

1. **Geometric reduction** — reduce the product’s action or object to circles, rectangles, lines, and negative space.
2. **Motion or relationship** — express flow, exchange, hierarchy, connection, or transformation with direction and rhythm.
3. **Letterform transformation** — modify a relevant letter only when it becomes a distinctive symbol rather than a generic monogram.

Choose metaphors from product behavior, not category clichés. Describe each direction in one sentence. Reject a concept if a competitor could use it unchanged.

## Construction

- Start with `viewBox="0 0 512 512"` for marks; choose a natural horizontal viewBox for wordmarks.
- Build on an 8px or 16px grid. Use few stroke widths and repeat radii deliberately.
- Prefer filled silhouettes for 16–24px icons. Rounded strokes need `stroke-linecap="round"` and `stroke-linejoin="round"`.
- Keep important geometry away from the viewBox edge by at least 8%.
- Use `currentColor` for monochrome variants; use explicit hex values for locked brand masters.
- Include `role="img"`, `<title>`, and `<desc>`. Do not include scripts, remote links, external CSS, filters that fail at small sizes, or base64 raster images.

## Evaluation matrix

Score each concept from 1–5:

| Criterion | Test |
|---|---|
| Meaning | Does the form connect to a real product behavior? |
| Distinction | Is the silhouette recognizable without color or text? |
| Reduction | Does it remain legible at 16px and in one color? |
| Balance | Are visual weight, spacing, and optical center controlled? |
| Versatility | Does it work on light/dark backgrounds and in square/horizontal uses? |
| Reproducibility | Can it be built with clean SVG paths and few colors? |

Reject any concept scoring below 3 for distinction or reduction. Select by total score only after these gates pass.

## Required variants

- Master symbol SVG with transparent background.
- Monochrome symbol using `currentColor` or a single locked fill.
- Wordmark or combination mark when the brand name must appear.
- Simplified favicon SVG when the master loses detail below 32px.
- PNG exports at requested sizes; typical set is 1024, 512, 256, 128, 64, 48, 32, 16.
- ICO containing 16, 32, and 48px when a browser package is requested.

## Visual review

Inspect on light and dark surfaces. Check transparent corners, clipped strokes, uneven antialiasing, optical centering, exact word spelling, and accidental resemblance to common marks. At 16px, count recognizable masses rather than judging fine detail.
