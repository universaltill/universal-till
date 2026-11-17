# Review: tree-pane zoom pins its transform-origin at measure time (ut-docs#3010)

Branch `fix/3010-pane-zoom-origin`. Follow-up to findings 1–2 of
`2026-09-27-pane-zoom-once-visible-2988.md`.

## What shipped
- `web/public/app.js` `zoomPane()`: the pane now scales about an inline px
  `transform-origin` at the viewport-clipped visible centre V, set once at
  measure time; translate is `O − V`. A pane that grows mid-zoom (/inventory's
  `hx-trigger="load"` low-stock list) no longer moves the anchor (was ~7.6px
  off the tapped row at t=0). The origin is cleared on `finish`/`cancel` —
  only by the latest zoom on that pane (`el.utZoomAnim` token) — and when
  `animate()` throws. Still transform+opacity only, `fill: none` (ADR-0122).
- `e2e/tests/tree-pane-zoom-2943.spec.ts`: a formula-independent check (pause
  at t=0, map the visible pane centre through the computed matrix about the
  computed `transformOrigin`, ≤1.5px from the tapped row centre, all five
  rail rows); `expectedZoom` updated to the new maths; the Full test asserts
  the inline origin is gone afterwards; a regression test for back-to-back
  swaps without a pointerdown.

## Review (independent, different model from the author)
1. **Medium — fixed.** An earlier zoom finished by `htmx:beforeSwap →
   UT.finishZooms()` dispatches `finish` a frame later, after the next
   `zoomPane()` set its origin, so its `clear()` wiped the new zoom's origin
   mid-animation (keyboard/switch activation, Enter-submit, JS click; with
   `defaultSettleDelay:0` all of beforeSwap→afterSettle is one task).
   Reproduced in Chromium (origin `""` while the second zoom ran); fixed with
   a per-element token; regression test "back-to-back pane swaps keep the new
   zoom pinned origin" added.
2. **Low — fixed by (1)'s test.** The t=0 test uses `link.click()` (sends
   pointerdown) so it could not catch (1).
3. **Low — pre-existing, deferred → new Backlog card.** "every rail row zooms
   exactly once" is flaky on `main` too (3/6 on HEAD~1, 4/6 on the branch:
   `Received 2` zooms or a null rail-row box). A quick wait-and-finish fix was
   tried and did not cure it; CI's 2 retries absorb it today.
4. Checked fine: matrix maths (`a + M·(P − a)` = T(o)·M·T(−o)); no other
   inline origin on the panes (only drag tiles, app.js:2864/2945); every early
   return precedes setting the origin; `guard-i18n.sh` clean.

## Verification
- TDD re-verified by the reviewer in a separate worktree: app.js from the
  base → new t=0 test fails `/inventory: y Expected < 1.5 Received 7.556`;
  restored → passes.
- After the review fix: tree-pane-zoom-2943 + popup-zoom-2944 +
  page-zoom-2942 → 27 passed, 1 failed (the pre-existing flake, finding 3).
- Tester (separate agent): 80/81 across `--repeat-each=3` (same flake); paused
  the /inventory zoom at t=0 at 1280×720, 1024×600 and `?lang=fa` (RTL) and
  read the screenshots — the shrunken pane sits on the tapped row, RTL
  mirrored; after the zoom the style attribute is empty and transform `none`.
  Not looked at: dark theme, German, phone widths, /catalog and /modifiers
  visually, the cancel path visually.
- ci.yml guards run locally: all pass except `guard-deadcode-baseline.sh`
  (Go packages, untouched here; environment) and `guard-shellcheck-version.sh`
  (no shellcheck binary in this container).

Pre-flight: help topic n/a (the help does not describe the zoom; no
behaviour a shop owner reads about changed); docs-shots n/a (static
screenshots, no animation); reference/ n/a; README n/a; en.json n/a (no
strings); review record updated; follow-up → Backlog card (finding 3).

Verdict: approve after fix (1).
