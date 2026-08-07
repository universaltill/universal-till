# Till UI breaks at phone widths (ut-docs#413)

**Branch:** `feat/413-phone-width-layout` · **Reviewer model:** Opus — `complexity:medium` per the scrum-master skill's model-routing rubric (Sonnet builds, Opus reviews).

## What shipped

An external tester on a real ~360dp-wide Android phone found the till's
server-rendered web UI has no phone-width responsive layout — only one
breakpoint existed (`@media max-width: 900px`, a tablet floor) and nothing
below it, producing a cluster of symptoms (money-path modal apparently
off-screen, blank/unlabeled buttons, an obscured Subtotal row, a broken-
looking `/menu`, truncated text, a clipped nav item).

Dev drove a real Chromium browser at 360×640 (this sandbox ships a
pre-installed one at `/opt/pw-browsers` for exactly this purpose) instead
of guessing, and found the actual, mostly-shared root causes:

- `.nav` and `.kiosk-header`/`.kiosk-actions` both set `flex-wrap: nowrap`
  and need materially more width than 360px gives them; since
  `body.sale-screen` sets `overflow: hidden` (so the sale screen itself
  never scrolls), the excess content isn't a reachable scroll, it's simply
  invisible off-canvas — the mechanism behind "Lock clipped to Lo", blank-
  looking buttons (each button's own longest word became its minimum
  width and wrapped to 2 lines instead of shrinking cleanly), and `/menu`
  reading as broken (100% fallout from `.nav`'s own overflow — no
  menu-specific CSS was needed).
- `.pos-container`'s existing 900px-tier grid gives basket/tender/products
  three *equal* implicit rows with no flexible track — genuinely hides the
  basket's totals row under the tender panel at 360px. Fixed with a new,
  separate 480px-tier block giving `.tender` the only flexible track
  (mirrors the desktop grid's own existing `minmax(8rem,1fr) minmax(0,auto)`
  trick), deliberately not editing the existing 900px block (a 500-900px
  version of the same underlying bug is a real, separate, out-of-scope
  issue, noted for a future card).
- The reported "Deposit refund modal renders off-screen" is explained by
  the modal's *trigger* button being off-canvas (same `.kiosk-actions`
  overflow above) — the modal itself did not reproduce independently at
  360px in English at 1x scale; only a defensive `flex-wrap` was added to
  `.modifier-actions` for untested locale/scale combinations.
- Barcode input and statusbar text: added deliberate `text-overflow:
  ellipsis` (was native mid-character clipping).
- `.tender-actions` (the class named in the original ticket) turned out to
  be dead CSS, unused by any template; updated anyway for parity, but the
  live fix targets `.pay-grid`/`.tender-footer`/`.split-grid`/
  `.split-controls`.

New regression coverage: `e2e/tests/phone-width-layout-413.spec.ts`,
modeled on `basket-no-horizontal-scroll-391.spec.ts`'s per-file viewport-
override pattern (360×640), assertion-based (bounding boxes / computed
style / hit-testing), not screenshot diffing.

## Independent review — findings and resolution

Reviewed by a fresh-context Opus subagent, which read the full diff, ran
the new spec, and (this is the part worth recording) **reverted the CSS
fix and re-ran the tests against the pre-fix code** to prove each claim
empirically rather than take it on trust.

1. **BLOCKER (fixed) — `scripts/ci/guard-docs-shots.sh` failed with this
   diff, i.e. this would have shipped CI-red.** The guard's own documented
   fileset explicitly includes `web/public/**` ("a theme/app.css change is
   exactly as visible in a screenshot as a template change") — a CSS-only
   diff is not automatically exempt from the manual-screenshot-freshness
   requirement, contrary to the original assumption. **Fix:** ran
   `make docs-shots`'s underlying capture (56 screenshots across 14 topics
   × 4 locales, at the unaffected 1024×600 kiosk viewport — visually
   nil-to-cosmetic churn, since the new rules only apply below 480px) and
   committed the regenerated `web/help/img/manifest.json` + PNG set.
2. **MEDIUM (fixed) — the test for "every button keeps a visible label"
   was a false-pass against its own AC bullet.** It asserted only
   `text !== ''` (always true for server-rendered markup) and `width > 0`
   (true even for an element rendered entirely off-canvas). The
   reviewer's own pre-fix probe measured the real "Deposit refund" button
   at `left: 361` in a 360px viewport (wholly outside it) at 67px tall (a
   two-line wrap) — neither symptom was actually asserted. **Fix:**
   strengthened the test to check on-screen left/right bounds and a
   single-line height ceiling (60px, strictly between the ~46-51px
   single-line baseline and the ~66-67px wrapped-button measurement).
   Re-verified with the same revert-and-rerun method: now genuinely fails
   pre-fix, passes post-fix.
3. **MEDIUM (fixed) — no test existed for the "deliberate truncation, not
   mid-character clipping" AC bullet.** Added a test asserting the barcode
   input's computed `text-overflow`/`white-space` (and `overflow`,
   accepting Chromium's documented `hidden`→`clip` normalization for
   single-line text `<input>`s specifically — confirmed empirically, not
   assumed, when the first version of this assertion failed).
4. **LOW (fixed) — CSS cascade-order fragility.** The new `.statusbar`
   phone-tier block was placed *before* the base `.sb-update`/`.sb-enrol`
   rules it overrides; it only worked because neither base rule declared
   `overflow`/`text-overflow` yet, so equal-specificity source order
   happened to favor the later (base) rule not mattering today — a future
   edit to the base rule could have silently killed the override with
   nothing to warn about it. Moved the phone-tier block after the base
   rules, matching every other new block in this diff.
5. **LOW (accepted, not fixed) — `:214`'s basket/tender "never overlap"
   test doesn't fail pre-fix** (grid rows never geometrically overlap; the
   real symptom, content clipped inside `.basket`'s own scroll region, is
   what the separately-passing Subtotal-visibility test actually catches).
   Harmless — left as an additional geometric sanity check, not counted as
   its own proof of the overlap-class fix.
6. **LOW (accepted, not fixed) — `:300`'s tender-grid-collapse test
   asserts the computed value of the very declaration it tests**, which
   the reviewer flagged as tautological. Accepted per the test's own
   comment: measuring individual button widths would be fragile against
   locale string length, and the grid-column-count assertion is still a
   real, meaningful check that the phone-tier rule is actually active.
7. **Branch was 2 commits behind `origin/main`** (the just-merged #412) —
   moved forward to current main; the two intervening commits touch only
   `android/`/`.github/`/`docs/`/`scripts/ci/`, no overlap with this diff.

Three "didn't need a fix" claims from Dev, each independently confirmed by
the reviewer's own revert-and-rerun (not taken on trust): the pfand modal
itself (only its trigger button needed a fix), the bug-report panel (no
CSS change needed), and `/menu` (100% fallout from the `.nav` fix, proven
by the `/menu` overflow test failing pre-fix and passing post-fix with
zero menu-specific CSS in the diff).

## Verified beyond automated tests

- New spec: 11/11 pass (up from 10 after strengthening/adding two tests).
- Targeted regression sweep (391, ui-scale-basket, bugreport-panel,
  tender-panel-reachable, manual, plus the new spec): 59/59 pass.
- Full default-project suite: 98/98 pass (reviewer's independent re-run,
  matching Dev's own count).
- `go build ./...` clean; diff touches zero `.go` files.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh` (after regeneration) all pass.
- Zero physical CSS properties introduced — logical properties only
  (`margin-inline-*`, `max-inline-size`, etc.), confirmed by reading the
  full diff, not a grep alone.
- All 7 new `@media` blocks scoped to `max-width: 480px` only; the
  existing 900px tablet tier is untouched in place (131 insertions, 0
  deletions in `app.css` before the cascade-order fix moved lines around).

## Verdict

**Safe to merge.** The blocker (docs-shots freshness) is fixed and the
guard now passes; both medium test-coverage gaps are fixed and re-verified
with the same revert-and-rerun discipline the reviewer used; the low
cascade-order finding is fixed. No client/shop name or credential-shaped
literal in this diff.

## Explicitly deferred / out of scope

- The `.pos-container` equal-thirds row-stretch bug also affects the
  existing 500-900px tablet range — a real, separate, pre-existing issue,
  deliberately not touched here (out of this ticket's phone-width scope).
  Worth a follow-up Backlog card.
- `guard-docs-shots.sh`'s own failure message names `web/ui/**`/
  `internal/pages/**.go` but omits `web/public/**`, even though the
  script's actual hashed fileset includes it — the message contradicts the
  algorithm and likely contributed to Dev's original (wrong) assumption
  that a CSS-only diff needed no manual regeneration. Worth a small
  follow-up fixing the message, not scoped into this ticket.
