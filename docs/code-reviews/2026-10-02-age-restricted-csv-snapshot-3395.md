# Code review — items.age_restricted carried through CSV export/import and the cloud snapshot (ut-docs#3395)

- **Date:** 2026-10-02
- **Ticket:** ut-docs#3395 (`complexity:medium`), a follow-up found during
  review of ut-docs#3340 (age-restricted sales): `items.age_restricted`
  wasn't carried by the catalog CSV export/import, or by the cloud catalog
  snapshot `my.universaltill.com` reads.
- **Branch:** `fix/3395-age-restricted-csv-snapshot`
- **Author:** session model (Sonnet), inline. **Process deviation noted:**
  this card's `complexity:medium` maps to an Opus 5.5 build tier per
  `MODEL-ROUTING.md`; it was built at the session model instead. Flagged
  here rather than silently passed over; the code is still reviewed fully
  on its merits below.
- **Reviewer:** independent pass, fresh-context Fable subagent in an
  isolated worktree (own clone, never touched the shared checkout), per
  `MODEL-ROUTING.md`'s medium tier (reviewer differs from the author, and
  is never Opus for a medium card).
- **Verdict: SAFE TO MERGE**, after a hand-resolved rebase onto
  universal-till#1630 (merged mid-cycle) and one review-round fix.

## What shipped

- `internal/data/catalog_repo.go`: `ExportRow` gains `AgeRestricted bool`
  (`json:"age_restricted"`); `CatalogRepo.ExportRows`'s SQL/Scan select
  `i.age_restricted`, same position discipline as every other column here.
- `internal/pages/import_page.go`: `writeCatalogCSV` gains an "Age
  restricted" CSV column (Y/N, same shape as "Sold by weight"); the commit
  path's `pos.ItemInput{}` literal gains `AgeRestricted: it.AgeRestricted`.
- `internal/catimport/catimport.go` and `internal/catimport/xlsx.go` (two
  separate per-row parsing loops — CSV and Excel are genuinely different
  code paths in this package, confirmed by the independent reviewer): both
  gain an `"age_restricted"` column synonym and `isTruthy(get(rec,
  "age_restricted"))`.
- `internal/pages/export_contract_test.go`: the pinned cross-repo wire
  contract test (`TestExportItemRowSchema_PinnedFields`) updated — exists
  to keep `ut-docs`'s `reference/plugin-manifest.md` in sync with this Go
  struct; that doc updated in the same cycle, separate repo/branch
  (`ut-docs` `docs/3395-age-restricted-wire-contracts`).
- `internal/data/catalog_snapshot_repo.go`: `SnapshotItem` gains
  `AgeRestricted bool`; `CatalogSnapshotItems`'s SQL/Scan select
  `age_restricted`.
- `internal/cloudsync/cloudsync.go`: `snapshotItemRow` (the actual wire
  struct `pushSnapshotIfChanged` marshals) gains `AgeRestricted bool`
  (`json:"age_restricted"`), wired from the repo row.
- `ut-docs` `reference/manage-shop-catalog-api.md` §3.7's schema-2 field
  list updated to match (same branch as the plugin-manifest.md doc above).
- Tests: `TestParseAgeRestrictedColumn` / `TestParseNoAgeRestrictedColumnLeavesItFalse`
  (CSV parser-level); `TestParseXLSX_AgeRestrictedColumn` (Excel
  parser-level — this package's two readers can silently drift, same
  lesson as ut-docs#3403's own review); `TestExportCSVRoundTripsThroughImporter`
  extended (export writer → importer round-trip); `TestImport_AgeRestrictedColumnCommitsToDB`
  (full-handler-level, through the real `/api/import` mux against a real
  migrated SQLite DB — proves the `pos.ItemInput{}` wiring, since the
  parser-level tests never touch the commit path); `TestCatalogSnapshotItems`
  and `TestSnapshotSchema2Shape` extended for the snapshot side;
  `TestExportRows` extended (review-round fix, see below) for the CSV
  export's own SQL/Scan.

## Scoped out, and why (both checked by the reviewer, both confirmed correct)

1. **Re-import updating an already-existing item.** The original card text
   asked for this, but `import_page.go`'s commit loop only ever calls
   `CreateItemTx` — an existing SKU is always skipped
   (`data.ErrSKUExists` → `Skipped: true`), for every field, today. That is
   documented, intentional behaviour (`web/help/en/catalog.md`: "stay
   skipped — that is what keeps importing twice safe"), so "update on
   reimport" is a separate, much larger capability gap affecting every
   field, not specific to `age_restricted` — filed separately rather than
   invented here.
2. **The cloud (`ut-cloud`) actually ingesting/storing/displaying the new
   snapshot field**, and any `ut-my-shop` UI for it. This PR only gets the
   till *sending* the field — additive, forward-compatible, and an extra
   JSON key a lenient decoder doesn't recognise can't break today's
   ingest (reviewer checked `ut-cloud`'s ingest struct shape and
   `DisallowUnknownFields` usage via code search; see review notes).
   Actually consuming it cloud-side is separate repo work.

## Findings (fixed before merge)

1. **Pre-existing-at-review-time, became real once `main` moved:
   fresh conflict with universal-till#1630** (ut-docs#3403, CSV net
   quantity), merged to `main` while this branch was open. Both cards
   appended to the exact same tails: the `ExportRow` struct end, the
   `ExportRows` SELECT/Scan tail, the CSV header/row slices in
   `writeCatalogCSV`, the `pos.ItemInput{}` literal, and
   `TestExportItemRowSchema_PinnedFields`'s field list. **Fix:** rebased
   onto `main` and hand-resolved all three Go conflicts, keeping both
   cards' fields in declaration order (net quantity pair, then
   `age_restricted`) consistently across the struct, the SQL column list,
   the `Scan(...)` argument list, the CSV header, the CSV row, and the
   pinned-fields test — the reviewer's own worked example of what a
   careless resolution would do (age_restricted's 0/1 landing in a
   net-quantity slot, a NULL landing in the bool slot and erroring the
   whole export) was checked against by hand, column by column.
2. **Test gap on the export side of `ExportRows`** (medium, directly
   related to finding 1): nothing ran the real SQL and asserted
   `ExportRow.AgeRestricted` the way `TestCatalogSnapshotItems` already did
   for the snapshot side — `export_roundtrip_test.go` builds `ExportRow{}`
   literals by hand, `export_contract_test.go` only reflects on struct
   tags. **Fix:** extended `TestExportRows` (`catalog_repo_crud_test.go`)
   to set `age_restricted = 1` on one seeded item via real SQL and assert
   both rows' `AgeRestricted` come back correct from a real DB round trip
   — this is exactly the test that would have caught a Scan-order mistake
   in the rebase above.
3. **Non-English help text not updated** (low, accepted by precedent):
   only `web/help/en/catalog.md` gained the sentence; the sibling card
   ut-docs#3403 set the same precedent on the same paragraph (help-doc
   prose translation is separate follow-up work, unlike `web/locales/*.json`
   keys, which `guard-i18n.sh` gates per-PR and this card adds none of).

## Verified beyond automated tests

- Mutation-tested the handler-level DB-commit test twice: once by the
  author (comment out `AgeRestricted: it.AgeRestricted` in the
  `pos.ItemInput{}` literal, confirm `TestImport_AgeRestrictedColumnCommitsToDB`
  fails with the real assertion `age_restricted did not reach the DB`, not
  a build error, restore, confirm clean), once independently repeated by
  the reviewer in their own throwaway worktree with the identical result.
- `go build ./...`, `go vet ./...`, `gofmt -l .` (whole tree) clean.
- `golangci-lint run` on the four changed packages: 0 issues (reviewer).
- `go test ./internal/catimport/... ./internal/pages/... ./internal/data/... ./internal/cloudsync/...`
  (every package touched, full suite) green, before and after the rebase
  and the review-round fix.
- `scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh`,
  `scripts/ci/guard-help-topics.sh`, `scripts/ci/guard-help-drift.sh` all
  pass; no new help-doc structural drift (the added sentence sits inside
  an existing numbered item, no new heading/bullet/step).
- No UI surface touched (confirmed: no `.html`/template/`.js`/`.css` file
  in the diff) — `reference/ux-guidelines.md` doesn't apply, no screenshot
  regeneration needed.
- Checked for an unexpected surface this flag should also reach, now that
  it's more widely plumbed: the till-prompt gate (`pos_api.go`) and
  self-order kiosk block both read `items.age_restricted` live from the
  local DB, never from any payload touched here, so neither changes
  behaviour; satellite admin sync already carries the column via its
  whole-row pull (unchanged); the cloud→till `update_item_details`
  directive is a read-modify-write that never touches this field, so it
  can't clobber it.
