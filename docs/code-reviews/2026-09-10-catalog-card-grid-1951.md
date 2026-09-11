# Code review: catalog list becomes a card grid (ut-docs#1951)

**Date:** 2026-09-10
**Card:** ut-docs#1951 — "Catalog default view should be SumUp Library-style
item cards with an edit popup, not the old list" (later narrowed by the
product owner to the list/card layout only — the item editor's own redesign
split to ut-docs#1956)
**Branch:** `feat/1951-catalog-card-grid`
**Diff:** `web/ui/pages/catalog.html`, `web/ui/partials/catalog_table.html`,
`web/ui/partials/catalog_row.html`, `internal/pages/catalog/handlers.go`,
`internal/pages/catalog/row_oob.go`, `internal/data/catalog_repo.go`,
`internal/httpx/httpx.go`, `internal/httpx/tplcache.go`, `web/public/app.css`,
`web/locales/{en,ar,fa,tr}.json`, `web/help/{en,ar,fa,tr}/catalog.md` +
regenerated screenshots/manifest, plus test updates across
`internal/pages/catalog/*_test.go` and `internal/data/catalog_repo_thumbnail_test.go`,
and `e2e/tests/*.spec.ts`.
**Reviewer:** independent (Opus, different model from the Sonnet that wrote
the code; ran in the same shared checkout rather than an isolated worktree —
see "Process note" below — but did not write the original diff).

## What shipped

The `/catalog` admin page's item list moved from a `<table>` to a card grid
reusing the sale-screen's own `.btn-tile`/`.tile-colored`/`.tile-name`/
`.tile-price`/`.thumb` classes (`buttons.html`, `app.css`) — "SumUp
Library-style item cards" is exactly what that pattern already renders, so
no new CSS component was invented. Each card shows a photo-or-color tile,
name, a barcodes/variants caption, and price; SKU/Tax/Unit/Category and the
old per-card Edit/✕ buttons are gone — selecting a card opens the existing
item-edit `<dialog>` (`#item-form-modal`, shipped by ut-docs#1901), which
gained a new Deactivate button since the card no longer has one.

The old table had a "thumbnail column exists only if some item has a photo"
mechanism (`ShowThumbColumn`/`HasThumbnails`/`OOBSwap`/`emptyRowColspan`/
`writeWholeTableOOB`/`HasAnyThumbnail`, plus the whole-table-carrier
`<table hidden>` htmx-parsing workaround) that forced a whole-table
re-render whenever that flipped. A card grid has no shared `<thead>`
column — every card always independently renders its own
thumbnail-or-color-or-blank slot — so this entire mechanism was deleted as
dead weight, along with the now-unreachable `taxCodeName`/`categoryName`
per-row display (and the `taxCodeNameFunc`/`lookupNameFunc` helpers, and
their default no-op entries in `httpx.baseFuncs`) since cards don't show
Tax/Category at all.

Locale files gained `catalog.deactivate`, `catalog.item_deactivate_confirm`,
`catalog.empty` (replacing an incorrect `basket.empty` reuse) in all four
core locales — hand-translated by the implementing session, since the
self-hosted NAS translation service is unreachable from this cloud sandbox
(confirmed via a direct connection timeout). `catalog.col.tax`/
`catalog.col.category` were removed as now-orphaned. `web/help/*/catalog.md`
prose was corrected where it described now-removed behavior, and the
topic's screenshot regenerated in all four locales.

Complexity was downgraded from `hard` (grooming's estimate against the
original, editor-inclusive scope) to `medium` at the Architect pass, once
the item-editor redesign was confirmed already split out to ut-docs#1956 —
Dev ran at this session's own model (Sonnet), Review at Opus.

## What the independent review found

**Verdict: safe to merge.** No blocker survived verification.

Re-verified independently (not taken on trust):
- **The htmx protocol simplification is correct.** Read
  `web/public/vendor/htmx.min.js` (v1.9.12) directly rather than trusting
  the removed code's own comments: `makeFragment` only special-cases
  `thead|tbody|tfoot|colgroup|caption|col|tr|td|th|script|style`, so a
  `<button>`/`<div>` first tag falls through to a plain body-context parse
  regardless of what precedes it in the same response — the exact hazard
  the old `<table hidden>` carrier existed to work around is gone once
  fragments are `<button>`/`<div>` instead of `<tr>`/`<tbody>`.
- **The `.btn-tile[hidden]` CSS fix** (found by the implementing session
  running the real e2e suite against a real browser, not just Go template
  assertions — `.btn-tile`'s own `display: flex` was beating the UA
  `[hidden]{display:none}` rule, silently defeating the catalog grid's
  client-side search) reproduces exactly as claimed: reverting just that
  one rule made the two named e2e tests fail with the exact reported
  symptom; restoring it made them pass again.
- Keyboard path measured in a real browser (Tab focus-visible outline,
  Enter and Space both open the dialog), `--tile-color` survives
  html/template's CSS auto-escaping context, SKU search still matches,
  a failed deactivate still surfaces an error — all measured, not assumed.
- Full e2e suite (381 tests) green before and after the review's own fixes;
  `go build`/`vet`/`test`, `gofmt`, `golangci-lint`, and all CI guards clean.

Three fixes applied directly (accepted as-is into this branch):
1. **Contrast**: the barcodes/variants `.muted` caption inside a
   `.tile-colored` card measured 1.06:1 against WCAG AA's 4.5:1 (mid-grey
   on a colored tile) — added to the existing white-foreground override
   alongside `.tile-name`/`.tile-price`.
2. **Layout overflow**: adding the Deactivate button made
   `.catalog-form-head` overflow its non-modal, Escape-less dialog at phone
   width in every locale (measured 383–432px content vs. 307px container,
   Close button's hit-target landing outside the viewport) — `flex-wrap` +
   `min-width: 0` on the title, matching the till/small-till/tablet
   viewports' unchanged 0px-overflow measurement.
3. A test (`item_color_test.go`) that had gone vacuous after the table's
   `.color-dot` markup was deleted — replaced with the live
   `.tile-colored`/`--tile-color` equivalent.

## Fixed after the review (this pass)

- Two locale strings in `fa`/`tr` `catalog.md` still claimed the catalog
  list "may still show a blank/plain tile" for built-in and auto-assigned
  icons — false (and inconsistent with `en`/`ar`, which this same card had
  already corrected). Aligned all four locales.
- **Price alignment**: a card with a longer Barcodes/Variants caption
  pushed its price below its row-mates' (visible in the review's own
  screenshot). `margin-block-start: auto` on `.catalog-row .tile-price`
  (a `.btn-tile` flex child, column direction) pins every card's price to
  the bottom of its grid-stretched row, scoped to the catalog grid only —
  the sale-screen's own tiles have no such variable-height content.
- A test comment/assertion mismatch (`thumbnail_column_test.go`) — added
  the actual "exactly one thumbnail `<img>`" count the comment claimed.
- **Stale variants panel**: deactivating from the dialog removed the card
  and closed the dialog but left the always-visible variants/barcodes
  panel below the grid showing the now-gone item (new to this flow — the
  old per-row ✕ never opened that panel). Reset to its empty-selection
  state on a successful deactivate.
- Dead code the diff had left behind: `httpx.baseFuncs`'s now-unreferenced
  `taxCodeName`/`categoryName`/`brandName` default entries, and two stale
  comments (`catalog_repo.go`, `tplcache.go`) pointing at the now-deleted
  `taxCodeNameFunc`.
- Two now-orphaned locale keys (`catalog.col.tax`/`catalog.col.category`,
  zero remaining template call sites) removed from all four locale files.
- `card_grid_test.go`'s "no `<table>`" assertion scoped to the grid
  container rather than the whole page.
- Reverted unrelated `web/help/img/{ar,en}/sell.png` byte churn the
  review's own broader `docs-shots` re-run picked up — confirmed
  (`manifest.json`'s per-topic entries hash topic markdown text, not PNG
  pixels, and its one global `surface_sha256` doesn't require every PNG to
  be regenerated in lockstep) that this was pure text-rasterization
  nondeterminism unrelated to this diff, not a required update.

## Process note

The review agent was launched with `isolation: "worktree"`, but that
isolation covers this session's own primary repo (`ut-docs`) — the
manually-cloned `universal-till` checkout at a fixed path has no such
per-agent isolation, so the review's file-edits (the three fixes above)
landed directly on the shared working tree rather than a separate copy.
No corruption resulted (verified via `git diff` against the WIP commit
before accepting them), but this is the same class of risk ut-docs#386
documents for a shared checkout — noted here so a future cycle knows this
gap exists for a repo attached via `add_repo` rather than the session's own
tracked worktree system.

## Verified beyond automated tests

- Full e2e suite (381 tests) green, run twice (before and after the
  review's fixes) plus a third full targeted re-run (64 tests) after this
  pass's own fixes.
- Screenshot regenerated and visually inspected at the 1024×600 kiosk
  viewport in all four locales after every CSS change in this cycle
  (three regenerations total, tracking the `.btn-tile[hidden]` fix, the
  review's contrast/overflow fixes, and this pass's price-alignment fix).
- Physical-device confirmation (1280×800 pilot tablet, real touch
  hardware) stays `blocked:env` in this cloud sandbox, per the issue's own
  acceptance criteria — not silently skipped.

## Explicitly deferred (not this card)

- A follow-up Backlog card (ut-docs#1963) covers extending the ADR-0088
  UI-slot registry to a catalog-list-presentation slot — this card
  deliberately ships the card grid as the new hardcoded default rather
  than inventing an ad hoc plugin seam.
- Naming drift (`catalog_table.html`, `withTable`, `assertNoFullTable` etc.
  still reading "table") — the review's own nitpick; the `#catalog-table`/
  `#catalog-tbody` **ids** are a deliberate keep (existing OOB targets, JS,
  and e2e selectors all address the grid by these ids), but the Go/template
  identifier names are cosmetic leftovers, low-value to chase in this pass.
- The `.grid` class's 7rem sale-screen column floor vs. a catalog card's
  denser content — worth a thought, not a defect; `#buttons-grid-admin`
  is the in-file precedent for widening an admin grid if this is revisited.
- `catalog.col.tax`/`catalog.col.category` may still exist in the external
  `ut-plugin-language-{de,es}` packs; removing them from core does not
  itself break those packs' own CI, but their next unrelated PR's
  `lang-pack-drift` guard may flag them as orphans — not chased here since
  it's a separate repos' cleanup with no card of its own yet.
