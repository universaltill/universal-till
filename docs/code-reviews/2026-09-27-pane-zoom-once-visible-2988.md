# Code review: tree-pane zoom runs once, from the visible part of the pane (ut-docs#2988)

- **Date:** 2026-09-27
- **Branch:** `fix/2988-pane-zoom-once-visible`
- **Card:** universaltill/ut-docs#2988 (complexity:easy). Built by Sonnet, reviewed independently by Opus 5.5 in a detached worktree.

## What shipped

On /items, tapping Catalog or Inventory in the rail showed no zoom. There were two causes, both in `web/public/app.js`:

1. **Every rail tap zoomed twice.** Each rail row's response carries an out-of-band swap of `#items-rail`. htmx 1.9.12 fires `htmx:afterSettle` on every settled element and passes them all the same shared `detail`, so `zoomPane()` ran twice. The first run used the tapped row as the origin. The second run found that origin already used, so it zoomed from the bottom centre at scale 0.1, and that zoom was the one visible. Now only the pane's own settle event (`evt.target === t`) starts a zoom. The duplicate from the out-of-band swap still returns before the generic ease.
2. **Tall panes zoomed about a centre that was off screen.** `zoomPane()` now measures the pane clipped to the viewport. It solves `d = O − C − s(V − C)` so that the centre of the visible part lands on the tapped row. Scale stays uniform and the zoom still uses only transform and opacity, with no inline `transform-origin`. When the pane is fully on screen, the result is the same as the old formula.

**Tests:**
- `e2e/tests/tree-pane-zoom-2943.spec.ts`: a new test taps all five rail rows and requires exactly one zoom per tap, starting from the visible centre. The "Full" test's centre check was updated to match.
- `internal/pages/transitions_test.go`: pins the pane-branch gate and the viewport clip.
- `web/help/img/manifest.json`: only `surface_sha256` was refreshed (motion only, no pixel changes; `Docs-Shots-Unchanged`).

## Verification

- **Fails before the fix, passes after (reproduced by the author, then independently by the reviewer).** With the old `app.js`:
  - `/catalog: exactly one zoom per tap` expected 1 and got 2.
  - The Full test's dy was 63.78px off.
  - The Go test reported "the pane branch must gate zoomPane() on evt.target === t".
  - All three pass with the fix.
- **Reviewer's independent check, not using the production formula.** The reviewer paused the real animation at t=0 and mapped the clipped pane centre through the browser's computed transform matrix. On /catalog and /categories it lands on the tapped row within 0.01px.
- **Test runs:**
  - `go test ./...` passes.
  - `tree-pane-zoom-2943` (8), `in-panel-dialog-transitions-2338`, `page-transitions-2223`, `page-zoom-2942` and `popup-zoom-2944` all pass.
- **Guards:** docs-shots and i18n guards pass. `golangci-lint` on `internal/pages` reports 0 issues.
- **Not verified:** the product owner's tablet at Full effects. That check happens after the release, as the card asks.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The pane grows after it is measured (the `#low-stock-list` load swap on /inventory), and the live 50% `transform-origin` drifts, so the start point is about 7.6px off. | Deferred: ut-docs#3010 |
| 2 | minor | The e2e geometry check re-implements the production formula (`expectedZoom`), so it only proves the code matches itself. The test that catches the bug is the exactly-one-zoom count. | Deferred: ut-docs#3010 (add a `DOMMatrix` check that does not use the formula) |
| 3 | nit | A spec comment said the Categories pane is fully on screen. It is not: it is 968px tall against a 720px viewport. | Fixed |
| 4 | nit | The clip is against the window only, not against a scrolling ancestor. | Fixed: code comment added (none of the three panes sits in a scroller) |

## Verdict

Safe to merge.
