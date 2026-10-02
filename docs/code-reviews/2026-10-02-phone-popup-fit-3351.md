# Review: iOS popups open zoomed — phone popup-fit guard (ut-docs#3351)

**Date:** 2026-10-02 · **Branch:** `fix/3351-ios-popup-fit-viewport` ·
**Author model:** Opus 5.5 (Dev/Tester) · **Reviewer model:** Fable (independent)

## What shipped

`e2e/tests/phone-popup-fit-3351.spec.ts` only — no app change. At 390×844 and
440×956 (touch, mobile, motion on, effects level Full) it sweeps the 40 routes
of the #3297 phone sweep, opens every reachable popup (record dialogs,
`aria-haspopup` triggers, item/import/tax-code dialogs, Pay, bug report,
drawer, basket sheet, then every remaining `<dialog id>` via `.show()`) and,
once its finite animations end, asserts: box within the viewport; no
scale/skew transform left on the popup or any ancestor; no sideways page
scroll, `innerWidth` = viewport, `visualViewport.scale` = 1; no descendant past
an edge outside an on-screen scroll/clip box; every field in the popup ≥ 16px.
Vacuity floor: ≥ 28 distinct popups (29 today, 155 instances per width).

## Root cause

No popup geometry or motion defect on `main`: every popup rests on screen at
scale 1. The owner's "opens zoomed, pinch to fit" (v0.30.13/14) is iOS
focus-zoom: 8 popups focus a 13.9–14.4px field as they open
(`record-dialog.js` focuses the first field; `#table-add-label`, the item form
autofocus), and iOS zooms into any field under 16px on focus. Sibling
ut-docs#3350 (f7cc09d: fields ≥ 16px at ≤ 480px + WKWebView scale lock) fixes
it; it ships in **v0.30.16** (not yet tagged at review time). #3350's own spec
only measured fields rendered at page load — closed dialogs' fields have no
box — so popups were unguarded; this spec closes that gap.

## Verification

- Failing first: reverse-applying #3350's `app.css` hunk fails the spec with 13
  "fields iOS zooms into on focus" lines (12 dialogs + the basket), 8 tagged
  "focused on open" — re-run independently by the reviewer. Restored → passes.
- Mutations (Dev): `.modifier-modal { width: 28rem }` fails ("86px past the
  screen"); popup motion ending at `scale(1.1)` with `fill: forwards` fails
  ("still transformed").
- Spec green at both widths after review fixes (2 passed, 4.7m); combined run
  with popup-zoom-2944, phone-layout-sweep-3297, phone-input-no-zoom-3350,
  in-panel-dialog-transitions-2338, popup-scrim-2873,
  category-popup-fullscreen-3073, categories/locations record dialogs: 169/169.
- `gofmt`, `go build`, `go test ./internal/pages/...`, guard-e2e-fixtures-import,
  guard-e2e-no-browser, guard-docs-shots, guard-i18n, guard-no-showmodal,
  guard-help-*: clean. Only `e2e/tests/` changed → no docs-shots hash change.
- Looked at (390px, light, en): category record dialog, Add table, payment
  panel — all fit. **Not checked:** dark theme, RTL, German at phone width;
  real iPhone / iOS WebKit (Chromium emulation only). The owner should retest
  on a v0.30.16+ TestFlight build.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Server-filled popups (`#modifier-modal`, `#table-modal`) are measured empty — e2e seed has no modifier items, so a too-wide option grid would pass | Deferred → ut-docs#3411 |
| 2 | minor | ~4.5 min worker time, 600s timeout — suite's long pole | Accepted; split per route group if it ever flakes |
| 3 | minor | Silent skips; vacuity floor 25 vs 29 let four popups drop out unseen | Fixed: floor raised to 28 |
| 4 | minor | Fixed sleeps after goto/click can misattribute a slow htmx fill | Accepted (measured eventually; attribution only) |
| 5 | nit | `reachable()` waves through hidden/clip ancestors | Fixed: comment says it's deliberate |
| 6–7 | nit | No-op belt-and-braces resets; `/catalog` link triggers navigate | Accepted |

## Verdict

Safe to merge.
