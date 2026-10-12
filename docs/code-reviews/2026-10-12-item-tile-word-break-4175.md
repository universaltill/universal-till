# Review: phone sell screen item tile names break mid-word (ut-docs#4175)

## What shipped

- **Cause.** At 360px the phone layout puts item tiles three per row (the
  approved app-icon mock-up): ~88px wide. `.tile-name` is .9rem/600, and the
  root font is 17px on this viewport, so 15.3px. "Dishwashing" is 94px
  there, wider than the tile, so `overflow-wrap: anywhere` broke it as a last
  resort. (#3812 fixed category tiles by going to two columns; item tiles
  keep the mock-up's three.)
- **Measured first** (Chromium 360x740, demo catalogue, both browsing
  modes): at .9rem Dishwashing is 94.3px, All-Purpose and Conditioner
  85.8px, Toothbrush 83.8px. At .8rem Dishwashing is 83.8px and every other
  word is at most 76.3px, so all fit 88px. "Semi-Skimmed" is 91.4px even at
  .75rem; it can never fit, and it breaks at its hyphen ("Semi-" /
  "Skimmed"), which is a normal break.
- **Fix (`web/public/app.css`, phone block only):** the phone rule
  `.pos-container .btn-tile .tile-name` goes from .9rem to .8rem, with a
  comment stating the measured reason. This is option 1 of the card (a
  smaller name on phones); two per row stays an owner decision. 1024x600,
  desktop, kiosk and self-order keep their sizes.
- **Test:** `e2e/tests/item-tile-word-break-4175.spec.ts`, at 360x740 in
  `all_filter_chips` and `strip_overflow`, in en, de and fa (6 tests). Each
  test creates its own category and items, opens that category (filter chip
  / strip tab) and checks, for every unit of every visible tile name, that a
  DOM Range lies on one line. A unit is the whole word if it fits the tile at
  .8rem (measured at .8rem, not at the rendered size, which is what makes it
  fail on the old .9rem), else its hyphen-ended segments. It also asserts
  that the created tiles were measured (visible), three columns and no name
  past its tile's edge. Words under test: "Dishwashing" (en), "Waschpulver"
  (de, 84px at .8rem / 95px at .9rem), "مایع‌ظرفشویی" (fa, 84 / 90px).
- `web/help/img/manifest.json`: surface hash refresh only (`web/public/**`
  is part of the docs-shots surface). All docs shots are 1024x600 and the
  change is inside `@media (max-width: 480px)`, so no pixel changes.

## Review (independent, Opus 5.5; the code was written by Sonnet 5.5)

1. **major, fixed.** The first spec's `strip_overflow` half gave no
   protection: the created items are uncategorised and not on screen in that
   mode, and `allTextContents()` also returns hidden tiles, so the check
   "the created items are among the inspected tiles" passed vacuously. The
   three `strip_overflow` tests were green on the old CSS. Fixed: each test
   creates a category with its items and opens it (chip, or a click on the
   strip tab, which also covers a tab behind the "..." sheet); the spec waits
   for the created tile to be visible and asserts the created names were
   measured. Red on the old CSS was then re-checked for all six.
2. **minor, fixed.** On the old CSS the de and fa cases only failed because of
   the demo "Dishwashing" tile, not their own names. Words measured in
   the browser (fits at .8rem, not at .9rem) were added per language.
3. **found while fixing 1 (mine).** The first `.first()` wait picked a tile of
   a hidden panel, and the same item names in two modes collided (a
   deactivated item keeps its name, so the row count was 2). The wait now
   targets the created tile; names carry a per-mode tag.
4. **nit, accepted.** The price (.85rem/700) now reads slightly larger than
   the name (.8rem/600). Looked at in the screenshots: fine, and the guideline
   sets no minimum text size. Not changed here.
5. **nit, accepted.** The overflow assertion is nearly always true with
   `overflow-wrap: anywhere` + `overflow: hidden`; it only guards a future
   removal of `anywhere`. Kept.

The reviewer also confirmed the cascade (nothing later or more specific
overrides the changed rule; `themes/slate.css` is lower specificity; hidden
tiles in the Designer/Edit get the same size, which is right), the scope
(self-order has its own class; `catalog_row` is not inside `.pos-container`)
and that the docs-shots guard passes with the refreshed hash.

## Verified

- **Red -> green.** The spec on `main`'s `app.css`: 6/6 fail with "item names
  split mid-word" (Dishwashing / Waschpulver / مایع‌ظرفشویی). With the fix:
  6/6 pass.
- **Neighbouring specs (WSL, Chromium):** 243 passed across the phone-*,
  sell-tile-*, category-popup, category-image, sale-screen-213,
  sale-screen-category-* and sell-screen-browsing specs. One
  `phone-layout-sweep-3297` "/backoffice does not scroll sideways" failure in
  that run did not reproduce: it passes alone twice with this change, on
  `main`, and in order after this spec (92/92). Treated as a load flake; the
  cause was not proven.
- **Guards pass:** docs-shots, i18n, no-showmodal, e2e-fixtures-import,
  e2e-no-browser.
- **Screenshots looked at** (360x740, en and fa, `all_filter_chips`): three
  columns, names whole, the fa grid mirrored correctly.

## Not verified

- A real phone: Chromium at 360x740 with synthetic rendering only.
- `make docs-shots` was not re-run to compare PNGs byte for byte; the
  argument is structural (1024x600 only, change inside the 480px query).

**Verdict:** safe to merge.
