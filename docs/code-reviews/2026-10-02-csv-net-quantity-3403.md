# Review: CSV catalog import/export now carries net quantity (ut-docs#3403)

PR: universaltill/universal-till#1630 (`fix/3403-csv-net-quantity`)

## What shipped

`internal/catimport`'s CSV parser and `internal/pages/import_page.go`'s
`writeCatalogCSV` now round-trip an item's `net_quantity_value`/
`net_quantity_unit` (migration 060, ut-docs#3391) through two new columns,
`Net quantity`/`Net quantity unit`:

- `data.ExportRow` gains the two fields (`net_quantity_value`/
  `net_quantity_unit` JSON tags); `ExportRows`' SELECT/Scan carries them.
- `writeCatalogCSV` writes the pair only when both are set.
- `catimport.go` gets two new column synonyms and `parseNetQuantity`,
  which accepts a pair only if it passes `catalogtypes.ValidNetQuantity`
  (positive whole number, unit ∈ {g, ml, ea}); an invalid pair is dropped
  (never half-kept) and flagged via a new non-blocking `NetQuantityIssue`,
  the same pattern `TaxIssue` already uses.
- The import commit path passes the parsed pair into `CreateItemTx`.
- `export_contract_test.go`'s pinned `ExportRow` field list updated to
  match (required — it's a reflection-based schema guard on the plugin
  wire contract).

Scope deliberately CSV-only; `internal/catimport/xlsx.go` untouched.

## Review

Independent review by a Fable subagent (different model from the
Opus-5.5 author), per `MODEL-ROUTING.md`. Both the Fable reviewer and I
worked without a local clone this cycle — this sandbox's permission
classifier denied `git clone` of `universal-till` on this firing (a
read-only `get_file_contents`/`search_code` over the GitHub API worked
throughout, and is how every file in this PR was read and written).
**Compile/test claims below rest on reading the code plus the real CI
run on the PR, not a local `go build`/`go test`** — flagging that
explicitly rather than overstating verification.

Verified by reading (reviewer's independent pass, cross-checked by me):

- `CreateItemTx`'s parameter type (`catalogtypes.ItemInput`, aliased as
  `pos.ItemInput`) already carries `NetQuantityValue`/`NetQuantityUnit`;
  the call site's fields match exactly, and `insertItemRow` already runs
  `ValidNetQuantity` before writing — the new code can't bypass it.
- `ExportRows`' new SELECT columns and the `Scan()` arg list are in the
  same order, 13-for-13; no off-by-one.
- `parseNetQuantity`/`ValidNetQuantity` composition genuinely rejects
  every invalid shape the new unit test exercises (`kg`, `0`, `-5`,
  value-only, unit-only, `1.5`) and accepts `g`/`ml`/`ea` incl.
  case-insensitive `"ML"`.
- Header matching has no collision with the existing `stock`/`Quantity`
  column (exact-match only), and CSV column alignment (header vs. row
  `Write` calls) is 13-for-13.
- The composed warning string reaches the response through the same
  `htmlEscape` call every sibling issue-translate function's raw string
  does — no new injection surface.
- `internal/catimport/xlsx.go` is genuinely untouched (confirmed by
  reading the full file list and the file itself).
- Every test helper the new tests use (`newImportTestDeps`,
  `multipartCSV`, `initAuthTestI18n`, `data.DefaultEnabledBarcodeSymbologyIDs`,
  `catalogtypes.NetQuantityGrams/Millilitres/Each`) is real, confirmed via
  `search_code` against the actual repo, not assumed.
- `TestCatalogExport_NetQuantitySurvivesExportWipeReimport` is a faithful,
  real-HTTP, real-migrated-DB pin of the acceptance criterion (export →
  wipe → re-import preserves 227 g exactly; a plain item comes back NULL).

## Findings

**Blocking:** none.

**Required by repo process (fixed before this record, same cycle):**

- P1 — `reference/plugin-manifest.md`'s `export.requested.ask` `items`
  example/description didn't mention the two new fields (the plugin wire
  contract). **Fixed**: universaltill/ut-docs#3471.
- P2 — `architecture/catalog-import.md`'s CSV-column list didn't mention
  the new optional columns. **Fixed**: same PR, second commit.

**Non-blocking / accepted as-is:**

- N1 — the invalid-net-quantity warning reuses two existing i18n keys
  (`import.status.unknown_issue` + `catalog.net_quantity`) rather than a
  dedicated key, to avoid a 4-locale edit for one warning string. Reads
  correctly in every locale, if less precisely than a purpose-built
  message. Accepted; a dedicated key is a reasonable future polish, not
  filed separately (too small to warrant its own card).
- N2 — `ExportRows` sets `NetQuantityValue`/`NetQuantityUnit`
  independently rather than only-both-or-neither; every write path
  already enforces the pair, so this is unreachable in practice.
  Accepted as defensive-but-unnecessary.
- N3 — `.xlsx` re-import silently drops net quantity (no equivalent
  column handling in `ParseXLSX`, and no warning either, unlike the CSV
  path's invalid-input case). Real, filed as **ut-docs#3473**.
- N4 — a negative value could theoretical export unescaped; no DB
  `CHECK (> 0)` exists, only app-level validation. Practically
  unreachable (every write path validates) and `-5` isn't a formula
  trigger. No action.
- N5 — re-importing onto a live catalog never back-fills net quantity
  on an existing (SKU/barcode-matched) item, consistent with every other
  field's idempotent-skip behaviour and with the acceptance criterion's
  own export→wipe→reimport shape. Not a defect, noted for anyone reading
  this record later.

## Verified beyond the automated tests

- Read the real CI run on the PR (not assumed): `compile`, `authors`,
  `adr-taxonomy-guard`, `simulator` green; `build` (the `go test` job)
  and `playwright` were still running at review time — this record is
  not claiming a green full suite independently of that run.
- No real client/shop name or literal secret introduced (seed data is
  generic: "Coffee Beans", "Olive Oil", "Flour").
- Not UI-surface work (no template/CSS touched) — `reference/ux-guidelines.md`
  checklist doesn't apply.
- Not a shop-owner-visible new feature surface (an existing import/export
  flow gained two columns) — no `web/help/` topic update judged necessary;
  the existing catalog-import help topic's CSV-column list was not
  re-checked for staleness beyond this card's own two columns.

## Verdict

**Safe to merge** once CI (`build`, `playwright`) finishes green — no
blocking findings in the code itself; both repo-process doc requirements
are fixed in the same cycle (ut-docs#3471, this ut-docs branch's second
commit). One real deferred gap (N3) is filed as ut-docs#3473, not treated
as blocking this PR's own acceptance criterion (CSV round-trip only).

## Deferred items

- ut-docs#3473 — XLSX re-import path doesn't carry net quantity.
- A dedicated `import.status.net_quantity_invalid` i18n key (N1) — not
  filed as its own card; pick up opportunistically if touching this
  warning again.
