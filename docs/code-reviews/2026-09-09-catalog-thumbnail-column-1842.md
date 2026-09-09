# Code review: catalog list thumbnail column (ut-docs#1842)

**Date:** 2026-09-09
**Card:** ut-docs#1842 — "Catalog list's first column is blank on every row —
it renders a thumbnail placeholder it can never fill, and pushes the row
buttons under the side panel" (German pilot merchant, live feedback relayed
by the product owner)
**Branch:** `fix/1842-catalog-thumbnail-column`
**Diff:** `internal/data/catalog_repo.go` (`ItemThumbnails`,
`ItemThumbnailFor`, `HasAnyThumbnail`), `internal/data/catalog_repo_thumbnail_test.go`,
`internal/pages/catalog/row_oob.go` (`catalogRowVM.ImageURL`/`ShowThumbColumn`,
`buildCatalogRows`, `writeCatalogRowOOB`, `writeWholeTableOOB`, `emptyRowColspan`),
`internal/pages/catalog/handlers.go` (`/catalog` GET, `snapshotThumbColumn`,
every mutation endpoint), `internal/pages/catalog/thumbnail_column_test.go`
(new), `internal/pages/catalog/row_oob_test.go`,
`web/ui/partials/catalog_row.html`, `web/ui/partials/catalog_table.html`,
`web/help/{en,de,ar}/catalog.md`, `web/help/img/**` (regenerated)
**Reviewer:** independent (Opus, different model from the Sonnet that wrote
the code, own isolated git worktree, did not write the original diff)

## What shipped

The catalog list's first column guessed a thumbnail file path from the item
ID (`/public/assets/items/<id>/thumb.png`) and tested whether that file
existed on disk — it never asked the item what its real thumbnail actually
is. An item's thumbnail lives in `item_images` (role=`thumbnail`), including
the category placeholder icon `EnsureDefaultThumbnail` assigns at CSV import
time (`#1189`) — so an imported catalog with no source photos showed a
permanently blank column, wasting ~48px on every row and crowding the
Edit/✕ buttons toward the 360px-ish side panel.

Fix, following this file's own existing `ItemBarcodes`/`ItemBarcodesFor` /
`ItemVariants`/`ItemVariantsFor` pattern:

- `CatalogRepo.ItemThumbnails` (whole-catalog map) and `ItemThumbnailFor`
  (single item) read the real path from `item_images`; `catalogRowVM.ImageURL`
  carries it, and `catalog_row.html` renders it exactly the way every other
  `ImageURL` consumer in this codebase already does (`basket.html`,
  `buttons.html`, `buttons_admin.html`, `suggestions.html`:
  `{{ if .ImageURL }}<img src="{{ imgv .ImageURL }}">{{ end }}`) — no new
  rendering convention invented.
- `CatalogRepo.HasAnyThumbnail` answers "does any currently-active item have
  a thumbnail" — the column-collapse decision the card's AC2 asked for:
  when nothing in the listing has an image, the `<th>`/`<td>` are omitted
  entirely (not rendered empty at `width:0`), and `catalog_empty_row`'s
  `colspan` (`emptyRowColspan`) tracks whichever count is currently true.
- Button relocation, raised in the merchant's own follow-up comment on the
  card, was deliberately left out of scope — that's `#1830`'s UX pass, and
  this diff leaves `.btn-actions`/Edit/✕ untouched.

## What the independent review found

Two blockers, one should-fix, one nit — full findings below, all fixed and
re-verified in this same review pass (this review record already reflects
the fixed state; see "Fixed during review" for what changed).

### F1 — BLOCKER (fixed): row-level OOB fragments could disagree with the `<thead>`

The first draft re-derived `HasAnyThumbnail` fresh on every mutation and
trusted that to agree with "whatever the table currently shows" — but the
`<thead>` and every sibling row were rendered once, at page load (or the
last mutation), and a row-level OOB fragment can only ever touch the one
row it's about. The review reproduced it directly: an all-text catalog (7
`<th>`, 7 `<td>` per row) where a merchant uploads their first-ever item
photo — the exact flow this card exists to serve — got back an in-place row
replacement with **8** `<td>`s against a 7-column `<thead>`, misaligning
every field in that one row until a manual reload. The symmetric case
(deactivating the last imaged item) mis-spanned the empty-state placeholder
the same way (**F2**, same root cause).

**Fix:** every mutation handler now snapshots `HasAnyThumbnail` **before**
its own write (`snapshotThumbColumn`, called as the first action in each of
the four handlers that can plausibly flip it: item create with a
barcode-lookup auto-fill photo, item update with a reactivate/deactivate
`isActive` change, item deactivate, and item image upload — the other five
`writeRowOOB` call sites are variant/barcode mutations that never touch
`item_images` or an item's active state, so their "before" is safely taken
at the call site itself). `writeCatalogRowOOB` compares that snapshot
against the fresh post-mutation answer; when they disagree, it delegates to
the new `writeWholeTableOOB`, which re-renders the **entire** `#catalog-table`
(header and every row) as one `hx-swap-oob="true"` swap instead of a row
fragment — the one case a fragment genuinely cannot express. This is the
rare path (only the specific mutation that flips the global fact takes it);
every other mutation keeps the cheap per-row fragment `#1363` introduced.

Verified live in a real browser (not just Go tests): started an all-text
catalog (`<thead>` with 7 `<th>`), uploaded a photo to one item — the
`<thead>` gained the 8th `<th>` in place, **and** a second, untouched row
picked up its own thumbnail cell in the same response, proving the whole
table swapped rather than just the edited row.

### F2 — SHOULD-FIX (fixed): same root cause, empty-state colspan

Covered by the same F1 fix — `emptyRowColspan` is now only ever computed
from a value that's already been confirmed to match the live `<thead>` (or
the whole table is being re-rendered anyway, which recomputes it fresh and
correctly).

### F3 — BLOCKER, CI red (fixed): `guard-docs-shots.sh` failed

`make docs-shots` had been run before `web/help/en/catalog.md`'s prose fix
landed, so the manifest's `catalog`/`en` topic hash was stale against the
edited markdown. Re-ran `make docs-shots` after all doc edits were final;
`scripts/ci/guard-docs-shots.sh` is green.

### F4 — SHOULD-FIX (fixed): stale manual text in other locales

Only `en/catalog.md` had been corrected. `de/catalog.md` and `ar/catalog.md`
carried the same now-false claim ("the catalog list may still show a blank
tile until you add a real photo — only the till-facing screens get the
automatic icon") in German and Arabic — material for the Germany pilot
specifically. Both corrected to match the fixed behavior, and to describe
the column-collapse rule (AC2). `fa/catalog.md` and `tr/catalog.md` never
carried this paragraph at all (a pre-existing translation gap, not
introduced by this change) — left as-is, out of this card's scope.

### Nits (not fixed, noted for the record)

- `internal/pages/catalog/handlers.go`'s `/catalog` GET handler computes
  `hasThumbnails` with its own loop over `items` rather than calling
  `repo.HasAnyThumbnail` — the two agree today only because `ListItems`
  and `HasAnyThumbnail`'s join both filter `is_active = 1`. Harmless, but
  means `HasAnyThumbnail` is unit-tested without being exercised by the
  `/catalog` page path itself. Left as-is: the GET handler already has
  `items` in hand for `buildCatalogRows`, so the extra query
  `HasAnyThumbnail` would otherwise cost isn't free either.
- `CatalogRepo.HasAnyThumbnail` uses `SELECT 1 … LIMIT 1` +
  `sql.ErrNoRows`, while its closest sibling `HasActiveItems` uses
  `SELECT EXISTS(…)`. Functionally equivalent, stylistically off-pattern;
  not worth a follow-up on its own.

## Verified beyond automated tests

- Full gate: `go build ./...`, `go vet ./...`, `gofmt -l .` (clean),
  `golangci-lint run ./...` (0 issues), `go test ./...` (every package,
  not just `catalog`/`data`) — all green.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh` — all green.
- TDD re-verified personally (not taken on trust): reverted the repo-layer
  implementation and the handler-layer templates in turn, re-ran the
  specific new tests, confirmed each fails with the real, on-topic error
  (not a compile break), then restored and confirmed green again — for
  both the original fix and the F1/F2 whole-table-swap fix (new tests
  `TestItemImageUpload_FirstEverThumbnailSwapsWholeTable` and
  `TestItemDeactivate_LastImagedItemCollapsesWholeTable`).
- Real-browser e2e (pre-installed Chromium, not just `httptest`): existing
  suites `catalog-row-oob-1363.spec.ts`, `catalog-thumbnail-no-request.spec.ts`
  (`#319`'s own regression guard, unaffected), `catalog-image-to-till.spec.ts`
  all pass unmodified.
- Visual check, actually looked at (not just asserted on): screenshots at
  1280×800 (kiosk resolution) for (a) the seeded demo catalog with mixed
  real-photo/no-photo rows — buttons fully clear of the side panel, (b) a
  photo-less row's placeholder box, (c) RTL (`fa`) — thumbnail column
  mirrors correctly, actions stay reachable, no layout breakage. Did **not**
  separately re-screenshot dark theme — no CSS was touched by this diff
  (markup-only), so treated as low-risk and not re-verified visually.
- `docs-shots`' own regenerated screenshots: `catalog.png` is
  byte-identical across all 4 shipped locales (the demo/docs-shots catalog
  already seeds real thumbnails, so nothing visibly changed there); two
  unrelated files (`en/sell.png`, `fa/multitill.png` — one from each of
  the two full re-shoots this review triggered) picked up single-digit-byte
  diffs, decoded and confirmed as sub-pixel font-rasterization noise from a
  genuine re-shoot, not a real regression.

## Safe to merge

Yes. Both blockers (F1 CI-red, F3 the substantive OOB-consistency gap) and
both should-fix findings (F2, F4) are fixed and re-verified; the two nits
are cosmetic and don't block. No client/shop name used as test data; no
secret-shaped literal in the diff.
