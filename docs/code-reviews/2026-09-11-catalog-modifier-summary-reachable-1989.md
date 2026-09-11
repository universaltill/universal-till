# Catalog item form: modifier-groups summary reachability (ut-docs#1989)

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#1989 — "Catalog item form: content below the
variants/modifiers summary is barely reachable at 1024x600 (found while
building #1957)"
**Complexity:** easy

## What the card reported

At the 1024x600 kiosk-floor viewport, the item-edit dialog's Variants tab
trailing content — the customization-groups summary and its "Manage
customization groups" button (ut-docs#1957) — was reported as barely
scrollable into view, with a screenshot showing the dialog cutting off
around that area.

## What was actually found (BA verification before scoping)

Two other cards had already landed on `main` since the issue was filed:
ut-docs#1956 (full-screen catalog item form, PR #1021, merged
2026-09-10T09:17 UTC) and ut-docs#1957 (modifier-groups relocation, PR
#1017, merged 2026-09-10T19:25 UTC). Both predate this cycle and together
made `.catalog-form-body` a real flex/`overflow-y:auto` scroller
(`web/public/app.css:837`).

A manual Playwright probe against the live app confirmed the "Manage
customization groups" button is fully reachable — visible, hit-testable,
and clickable — after scrolling `.catalog-form-body` to its end at
1024x600. A screenshot of the scrolled state showed generous whitespace
below the button, not any cutoff. **The originally-reported defect does
not reproduce on current `main`.**

Per the BA step's "verify current state before scoping anything" rule,
this is not a bug to fix — it's a card whose premise was already resolved
as a side effect of #1956/#1957. Building a "fix" here would have been
solving a problem that no longer exists.

## What shipped instead

One new e2e regression test,
`e2e/tests/catalog-modifier-summary-reachable-1989.spec.ts`, locking in the
reachability guarantee the card cared about. No existing test covered this
specific case:
- ut-docs#1956's own test ("action bar stays visible ... scrolled to the
  bottom") only drives the **Details** tab.
- ut-docs#1967's test only covers the variant grid's **horizontal**
  scroller (Save Variant reachability).

Neither exercises scrolling `.catalog-form-body` all the way down on the
**Variants** tab specifically, which is exactly where #1989's concern
lived. The new test creates an item, adds four variants to push the panel
well past the 600px-tall viewport, scrolls `.catalog-form-body` to its
end, and asserts the "Manage customization groups" button is
`toBeInViewport({ratio:1})`, passes a real `elementFromPoint` hit-test,
and is genuinely clickable (the click must open `#modifier-groups-modal`).

## Independent review

A fresh-context Sonnet subagent (complexity:easy → Sonnet review, per
`scrum-master`'s model-routing table) reviewed the diff independently, in
an isolated worktree. Verdict: **PASS, safe to merge as-is.**

What it verified, beyond reading the diff:
- Ran the new spec for real (`npx playwright test
  tests/catalog-modifier-summary-reachable-1989.spec.ts --project=default`)
  — passed, 19.2s; repeated 3x back-to-back, no flakiness.
- Ran `scripts/ci/guard-e2e-fixtures-import.sh` — passed (106 specs
  checked, this one imports `test`/`expect` from `./fixtures` correctly).
- Cross-checked every selector/class the spec depends on
  (`#item-form-modal`, `#manage-modifiers-btn`,
  `.catalog-detail-modifiers-summary`, `.catalog-form-body`,
  `#modifier-groups-modal`) against the real markup/CSS.
- **Regression-discrimination check**: temporarily mutated
  `.catalog-form-body`'s CSS three ways and re-ran the spec each time
  (restoring after each). `flex: none` (a plausible pre-#1956 non-scrolling
  layout shape) made the test genuinely **fail**, with the expected
  message — confirming the test's own scrollability guard is real, not a
  tautology.
- One nit noted, not a blocker: the test's scroll step
  (`el.scrollTop = el.scrollHeight`, a JS-driven scroll) would not by
  itself catch a regression that specifically flips `overflow-y` from
  `auto` to `hidden` while leaving flex sizing intact. This is not unique
  to this file — `catalog-item-form-1956.spec.ts`'s own scroll-to-bottom
  test and `variant-grid-save-reachable-1967.spec.ts` use the identical
  pattern — so it's a shared, pre-existing characteristic of this test
  style across the suite, not something this diff introduces. Not fixed
  here; noted for anyone hardening the pattern suite-wide later.
- Confirmed no hardcoded waits/races, real geometry-based reachability
  assertions (not a bare `.toBeVisible()`), no real client/shop name used
  (`'Modifier Reach Probe ' + Date.now()`), no secret-shaped literals, and
  style/convention matching the two sibling spec files closely.

## Verified beyond automated tests

- Manually probed the live app (headless Chromium, 1024x600) before
  writing the test, confirming the button's real bounding box, a genuine
  `elementFromPoint` hit-test, and an actual successful click — the same
  rigor the new automated test now encodes permanently.
- Confirmed via `git log` that #1956 (PR #1021) and #1957 (PR #1017) are
  both already on `main` in this checkout, so the reachability being
  verified is current behavior, not a stale snapshot.

## Scope / non-goals

- Not re-litigating #1956 or #1967's own fixes.
- Not addressing the reviewer's noted `overflow-y:hidden` discrimination
  gap in the shared JS-scroll test pattern — that's a suite-wide
  characteristic across at least three files, not specific to this change,
  and out of scope for a `complexity:easy` card.
- No UI/CSS/Go changes — this is a test-only diff, so no `web/help/`
  manual topic update applies (no user-facing behaviour changed).

## Verdict

Safe to merge. CI green (see PR). Closes universaltill/ut-docs#1989 as
verified-already-fixed, with a new regression test guarding the specific
gap the card identified.
