# Review: camera overlay Close reachable on short landscape screens (ut-docs#5122)

- Date: 2026-10-28
- Branch: `fix/5122-camera-overlay-close-reachable`
- Lane: `lane:cloud-41` (complexity:easy)
- Reviewer: independent Sonnet 5.5 subagent (the author was Opus 5.5)

## What shipped

On 740x360 and 640x320 the 4:3 video in the sell-screen camera overlays
(`.ai-identify-panel`: barcode scan, plugin identify) filled the
height-capped, scrolling panel. Close sat below the visible area, and
`elementFromPoint` at its centre returned null.

- `web/public/app.css`:
  - The `.ai-identify-controls` row is `position: sticky` at the panel's
    bottom edge. `inset-block-end: -1rem` and `margin-block-end: -1rem` lay
    it over the panel's own bottom padding, and it uses the panel surface
    as its background. Close stays on screen at any content height.
  - The row wraps (`flex-wrap`).
  - Under `max-height: 500px` the video's *width* is capped at
    `35dvh * 4/3` (with a `vh` fallback) and centred. The preview keeps its
    4:3 frame and most of it stays in view.
- `e2e/tests/camera-overlay-close-reachable-5122.spec.ts`: the spec covers
  740x360, 640x320, 1024x600 and 360x740, in LTR and fa. Close must be the
  hit target at its centre, the panel must not be scrolled, and Close must
  be inside the viewport. The same checks then run again with overflowing
  content (a long status). A real tap at Close's centre must close the
  overlay.
- `web/help/img/manifest.json`: only `surface_sha256` was refreshed
  (`update-docs-shots-surface-hash.sh`). No docs screenshot shows a camera
  overlay (`grep -rln` over `e2e/tests-docs` finds none), so every rendered
  manual image is unchanged. `Docs-Shots-Unchanged: true`.

## TDD

Before the CSS change, the new spec failed at 740x360 and 640x320 in LTR
and fa. Close was at y=474..525 in a 360/320px viewport, and its centre
hit null. 1024x600 and 360x740 passed. After the change all 8 cases pass.
The reviewer reverted `app.css` separately and reproduced the same failure.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Capping only the video's block size let `object-fit: cover` crop the preview to about 4:1. | Fixed: cap the inline size instead, so the frame stays 4:3. |
| 2 | nit | `35dvh` had no `vh` fallback. | Fixed. |
| 3 | nit | The plugin-identify row (Capture, Retake, Close) does not wrap, so long labels at 320–360px could overflow. This predates the change. | Fixed: `flex-wrap: wrap`. That overlay isn't rendered on the default e2e till, so it isn't driven. |
| 4 | nit | When content overflows, the panel scrolls about 10px at 740x360. | Accepted. The content is still reachable, and Close stays on screen. |

The reviewer checked these and found no problem:
- The sticky row's bottom is exactly the panel's bottom at all five
  measured sizes.
- The panel stays above `.statusbar` (the #3960 spec passes).
- `--surface` is defined only on `:root`, so the row's background matches
  the panel in every theme.
- Only logical properties are used, and there is no modal.

## Verified

- These specs pass: `camera-overlay-close-reachable-5122`,
  `camera-overlay-clear-of-statusbar-3960` and
  `sale-screen-camera-barcode-scan-548` (25/25, after the fixes).
- These guards pass: `guard-rtl-logical-css.sh`, `run-docs-shots-guard.sh`,
  `guard-i18n.sh`, `guard-no-showmodal.sh` and `guard-no-inline-handlers.sh`.
- I checked screenshots at 740x360 (en), 640x320 (fa, overflowing) and
  1024x600 (en). Close is visible and the panel is clear of the status bar.

## Verdict

Safe to merge.
