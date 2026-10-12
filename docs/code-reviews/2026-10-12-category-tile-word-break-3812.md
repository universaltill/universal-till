# Review: phone sell screen category tile names break mid-word (ut-docs#3812)

## What shipped

- **Cause.** At 360px the phone layout (`@media (max-width: 480px)`) forces
  every `.pos-container .products .grid` to three columns, for the approved
  app-icon item tiles. Category tiles share that grid, so each is ~88px wide
  with a ~70px content box. "Household" at .9rem/600 is ~75px, so the name
  could not fit on a line of its own. `overflow-wrap: anywhere` then broke
  it as a last resort ("Househol" / "d"). The overflow-wrap rule was not
  the bug.
- **Fix (`web/public/app.css`, phone block):**
  `.pos-container .products .grid:is(.category-tile-grid, .category-overflow-grid)`
  uses two columns. That is one of the card's own options. At 360px a
  category tile is now ~140px, which fits "Household", "Haushaltswaren" and
  the Persian test names whole. The tiles stay compact (ut-docs#2500); only
  their width grows. The "…" overflow sheet and the Move-to-category dialog
  share these tiles and get the same two columns: at 92vw, three columns
  there were ~93px, with the same problem.
- Specificity: (0,4,0), placed after the (0,3,0) three-column rule. It sits
  inside the phone media block, so 1024×600 and desktop are unchanged.
- **Test:** `e2e/tests/category-tile-word-break-3812.spec.ts`, run at
  360×740 in `category_tabs` mode, in en, de (`Haushaltswaren`, `Getränke`)
  and fa (`نوشیدنی‌ها`, `لوازم‌خانگی`, RTL). For every word of every tile name, a
  DOM Range must lie on one line. The spec also checks that every tile
  contributed words (no vacuous pass), that names stay inside their tile,
  and that there are two columns.
- **Follow-up filed:** ut-docs#4175. Item tile names split the same way at
  three per row ("Dishwashing", "Semi-Skimmed"). The item tiles keep the
  approved three-per-row mock-up, so that card needs its own decision.

## Review (independent, different model from the author)

1. **minor, fixed.** The first draft also changed `.category-tile-name` to
   `overflow-wrap: break-word; max-inline-size: 100%`, with a comment that
   blamed `anywhere`. The reviewer showed this was a no-op: `anywhere` and
   `break-word` only break a word that cannot fit on its own line. The draft
   also misdiagnosed the cause. Reverted to the original rule, and the
   comment now states the real cause.
2. **minor, fixed.** An `afterEach` `goto('/?lang=en')` claimed a locale
   reset that is not needed, because each test gets its own browser context.
   Removed.
3. **minor, fixed.** The created item's id is now recorded before anything
   else can throw, so a failed read-back cannot leak an active category onto
   the shared worker till's strip.
4. **nit, fixed.** The spec now asserts that every tile name had at least
   one inspected word.
5. **nit, accepted.** The two-column rule inherits the phone grid's
   `gap: 1rem .75rem`. That is unchanged from before.

## Verified

- **Red → green.** The spec was run against `origin/main`'s `app.css` and
  failed with "category names split mid-word" (en: Household; de:
  Haushaltswaren; fa: نوشیدنی‌ها). With the fix, all 3 pass.
- **The reviewer's own checks.** The reviewer independently confirmed, with
  a standalone Chromium script, that the Range detector flags a real split.
- **Neighbouring specs pass:** category-image-2500, category-popup-fullscreen-3073,
  sale-screen-category-strip-overflow-2307, sell-screen-browsing-mode-2499,
  sell-tile-move-category-2465 and popup-scrim-2873. Also passing:
  showmodal-statusbar-reachable-2097 and the phone-* sweep.
  `phone-sell-3059` "status row lives in the ☰ menu" fails identically on
  `main` in this container. Its "Storage almost full" chip comes from the
  container's disk, not this change.
- **Screenshots looked at.** Captured at 360×740 in en, de and fa: two
  columns, every name whole, and the fa tiles mirrored correctly.
- **Manual screenshots.** `make docs-shots` was run in this container on
  `main` and again on this branch (data dirs on tmpfs, to keep the disk-low
  chip out). All 120 PNGs were byte-identical; the manual's shots use no
  ≤480px viewport. So this commit carries only the surface-hash refresh
  (`Docs-Shots-Unchanged: true`). The help prose (`sell.md`,
  `categories.md`) makes no per-row claim.
- **Guards pass:** e2e-fixtures-import, e2e-no-browser, i18n, no-showmodal
  and docs-shots. `guard-deadcode-baseline` and `guard-shellcheck-version`
  cannot run in this container (deadcode tool built with an older Go; no
  shellcheck binary). The diff is CSS plus an e2e spec only.

## Not verified

- A real phone. The checks used Chromium at 360×740 with synthetic
  rendering only.

**Verdict:** safe to merge.
