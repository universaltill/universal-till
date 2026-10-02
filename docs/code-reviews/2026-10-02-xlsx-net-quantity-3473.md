# Review: XLSX catalog import now carries net quantity (ut-docs#3473)

PR: universaltill/universal-till (branch `fix/3473-xlsx-net-quantity`)

Follow-up to ut-docs#3403 (`docs/code-reviews/2026-10-02-csv-net-quantity-3403.md`),
filed from that review's deferred finding N3: `ParseXLSX` had no net-quantity
handling at all, so a merchant who opened this till's own CSV export in
Excel, saved as `.xlsx`, and re-uploaded it lost any configured net
quantity silently.

## What shipped

- `internal/catimport/xlsx.go`'s `ParseXLSX` now recognises the same
  `Net quantity`/`Net quantity unit` columns the CSV path (`Parse`) does,
  calling the existing shared `parseNetQuantity` helper from
  `catimport.go` — no duplication, same package.
- The net-quantity value is read through the function's existing RAW
  numeric pass (`getNum`), the same one price/stock already use, not the
  formatted-text pass (`get`) tax/takeaway-tax use. This matters because
  a spreadsheet's own display format can round or group a plain number
  (see Findings).
- `internal/catimport/xlsx_net_quantity_test.go` (new): valid g/ml/ea
  pairs, blank-means-none, an invalid pair (non-blocking,
  `NetQuantityIssue`/`Raw`), a workbook with no net-quantity columns, and
  a numeric-cell/display-format case (below).
- `internal/pages/import_net_quantity_test.go`: added
  `TestImport_XLSXUploadImportsNetQuantity` — a real HTTP request through
  the real `/api/import` handler and a real migrated DB, mirroring the
  existing CSV-path round-trip test in the same file.
- `web/help/{en,de,fa,ar,tr}/catalog.md` and
  `ut-docs/architecture/catalog-import.md`: updated to say the net
  quantity columns round-trip through CSV **or** `.xlsx` (previously
  explicitly said CSV-only / "not carried by .bkp/XLSX").

Scope deliberately narrow: no change to `catimport.go`, no change to
`internal/pages/import_page.go` (it already dispatches `.xlsx` uploads to
`ParseXLSX` and renders `NetQuantityIssue` generically — proven by the
pre-existing CSV-path pages tests).

## Review

Independent review by an Opus 5.5 subagent (different model from the
Sonnet author), per `MODEL-ROUTING.md`'s easy-complexity row, in an
isolated git worktree of the feature branch.

**Finding (real, fixed):** the first draft read the net-quantity value
through the formatted-text pass (`get`), matching the tax-column pattern
it was mirroring. That is wrong for this field specifically — tax keeps
the formatted text because a percent-formatted cell's raw value (`0.19`)
would parse as "0.19%", but a net quantity is a plain whole-number count,
where the formatted-text pass is exactly the money-rounding problem the
function's own doc comment already describes: a numeric cell holding
`250.4` under a `0` display format renders as `"250"` and would have been
silently *accepted*; a valid `1000` under `#,##0` renders as `"1,000"` and
would have been *rejected*. Reproduced directly (both failure modes,
before the fix) and fixed by switching to `getNum` and including
`net_quantity` in the condition that loads the raw-cell pass. New test
`TestParseXLSX_NetQuantityNumericCellsIgnoreDisplayFormat` pins all three
shapes (General, `#,##0`, `0.00`, and the rounding `0` format) and fails
on exactly those cases if `getNum` is reverted to `get` — reconfirmed by
me, independently, after the fix landed.

Verified independently (by me, not just read):

- `git show` of the actual diff, not a description of it.
- `gofmt -l`, `go build ./...`, `go vet ./...` clean.
- `go test ./internal/catimport/... ./internal/pages/...` (full packages,
  uncached) green.
- Full repo `go test ./...` green (every package, prior to the raw-pass
  fix landing — re-run of the two affected packages after).
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh` all pass (no SQL outside `internal/data`, no new
  user-facing strings needing a key, no help-topic drift from the prose
  edits — they add a sentence, not a new heading/step/bullet).
- Reverted just the `xlsx.go` diff and confirmed both of the original new
  tests fail with the claimed error (`NetQuantityValue:<nil>`, then
  `NetQuantityIssue/Raw = ""/""` where `"invalid"/"500 kg"` was wanted;
  the pages-layer test fails on a NULL DB column), then restored and
  confirmed green — same for the numeric-format test after the raw-pass
  fix.

Other things checked, no issue found:
- Header matching is the existing shared `headerIndex` (exact match), so
  there's no collision with `stock`/`Quantity` and no off-by-one.
- No SQL, no `money.Money`, no file write (so no `os.MkdirAll`/
  `paths.Data` question applies), no network/offline-sync path touched.
- Test data uses generic names only ("Ground Coffee", "Widget", "Coffee
  Beans").
- Not a UI surface (no template/CSS/page touched) —
  `reference/ux-guidelines.md`'s checklist doesn't apply.
- `web/help/en/catalog.md` already described CSV/XLSX import generally;
  it (and `ut-docs/architecture/catalog-import.md`) explicitly said net
  quantity was CSV-only — now stale given this fix, so both were updated
  in this same change, with the four non-English `catalog.md` locales
  given an equivalent new sentence (not a literal translation of the
  pre-existing English "Age restricted" clause, which was already
  untranslated before this PR and is out of this card's scope).

**Non-blocking / accepted as-is:**
- The two `buildXLSX` test helpers (`internal/catimport`'s and
  `internal/pages`'s) stay separate, duplicated copies — a pre-existing,
  documented split (test helpers aren't exported across packages), not
  something this diff introduced or should fix.

## Verified beyond the automated tests

Not a shop-owner-visible new feature surface in the UI sense (an existing
import flow now recognises columns it already recognised for CSV), so no
screenshot/driven-browser run — the real HTTP-handler + real-migrated-DB
test is the correct top layer for this change, and is the one actually
exercised.

## Verdict

**Safe to merge.** No blocking findings remain; the one real finding
(raw vs. formatted cell read) is fixed and covered by a new test,
independently re-verified. Pre-existing, out-of-scope items noted above
are left as-is.

## Deferred items

None filed — this closes ut-docs#3403's deferred N3 finding in full.
