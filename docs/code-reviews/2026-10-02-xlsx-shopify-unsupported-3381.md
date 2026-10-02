# Code review — ParseXLSX rejects Shopify-shaped .xlsx (ut-docs#3381)

- **Date:** 2026-10-02
- **Ticket:** ut-docs#3381 (`complexity:easy`, `launch:uk` backlog fallback
  pick — Ready had no `launch:uk` cards this cycle; see the issue's claim
  comment for the full tier-2 picking trail).
- **Branch:** `feat/3381-xlsx-shopify-unsupported`
- **Author:** Sonnet (this cycle's build model, `complexity:easy`).
- **Reviewer:** independent pass, fresh-context Opus 5.5 subagent (no
  visibility into the implementation reasoning), per `MODEL-ROUTING.md`'s
  easy tier.
- **Verdict: SAFE TO MERGE**, after fixing one real test-quality finding.

## The gap

`internal/catimport/xlsx.go`'s `ParseXLSX` shares `DetectFormat`/`headerIndex`
with the CSV path (`Parse`), so it correctly detects a Shopify products
export re-saved as `.xlsx` as `Format == "shopify"` — but its own separate
per-row loop has none of `Parse`'s Shopify-specific carry-forward/
image-row-skip/variant-naming logic (ut-docs#3284, CSV-only). The file was
silently run through the generic loop anyway, misparsing every row while
still reporting a misleadingly-correct `"shopify"` format label — the exact
"guess instead of reject" shape `ErrXLSXMergedCells` already refuses for a
different unreadable layout.

Confirmed on current `main` before starting: #3284 (the PR that added
`"shopify"` detection) had merged, so the gap this card describes genuinely
exists to fix (it didn't exist yet on an earlier pass this same cycle, which
correctly bounced the card back to `blocked:dep` — see the issue history).

## What shipped

- `internal/catimport/xlsx.go`: new sentinel `ErrXLSXShopifyUnsupported`,
  same pattern as `ErrXLSXLegacyUnsupported`/`ErrXLSXMergedCells`. `ParseXLSX`
  returns it immediately after `DetectFormat`, before the name-column check
  and before `rejectMergedCells` — so it wins over both for a Shopify-shaped
  file, and nothing in the per-row loop ever runs against Shopify data.
  Chosen fix is the card's own acceptance-criteria option 2 (reject
  explicitly) rather than option 1 (port the full Shopify row logic into
  `ParseXLSX`) — out of scope per the card's "low urgency" framing: Shopify
  only exports CSV natively, a merchant must manually re-save as `.xlsx` to
  hit this at all.
- `internal/pages/import_page.go`: new `errors.Is(err,
  catimport.ErrXLSXShopifyUnsupported)` case in the parse-error switch,
  mapping to a new translated message, same shape as the adjacent
  `ErrXLSXMergedCells` case.
- `web/locales/{en,ar,fa,tr}.json`: new key
  `import.error.xlsx_shopify_unsupported`, alphabetically placed after
  `xlsx_merged_cells`. Translated by this cycle's own model (ar/fa/tr) per
  CLAUDE.md's "translate in the same change, yourself" rule — "Shopify" and
  `.csv` left untouched as a product name / file extension.
- `web/help/{en,de,ar,fa,tr}/catalog.md`: one clause added to the existing
  "Excel workbooks" bullet, next to the merged-cells/legacy-`.xls`
  documentation, in every locale that already documents that bullet.
- Tests: `internal/catimport/xlsx_test.go`
  (`TestParseXLSX_ShopifyRejected`, `TestParseXLSX_ShopifyRejectedBeforeMergeCheck`)
  and `internal/pages/import_xlsx_page_test.go`
  (`TestImport_ShopifyXLSXRejectedWithSpecificMessage`), mirroring the
  existing `ErrXLSXMergedCells`/`ErrXLSXLegacyUnsupported` test shapes at
  both the parser and handler layers.

## Review findings

1. **MEDIUM (fixed): `TestParseXLSX_ShopifyRejectedBeforeMergeCheck` didn't
   test what its name claimed.** The original fixture merged `A1:B1` —
   merging keeps only the top-left cell's value, so this blanked the
   "Title" header. With the shopify check removed (the TDD re-verification
   step), the test failed on `ErrNoNameColumn`, never reaching the merge
   check at all — it was accidentally testing "shopify wins over missing-
   name", not "shopify wins over merged-cells" as the name and comment
   claimed. **Fix:** merge `C1:D1` instead (inside the imported
   Handle/Title/Variant SKU/Variant Price column range, but leaving every
   header cell's own value intact), with a comment explaining why the exact
   cell range matters. Re-verified: with the shopify check removed, the
   test now genuinely fails with `ErrXLSXMergedCells`'s message; restored,
   it passes.
2. **LOW (fixed): stale doc comment.** The sentinel-error block comment in
   `xlsx.go` named only `ErrXLSXLegacyUnsupported`/`ErrXLSXMergedCells`;
   updated to include `ErrXLSXShopifyUnsupported`.

No other findings. Confirmed no Loyverse false positive (Loyverse's own
`handle` + `sold by weight` signature is checked first in `DetectFormat`,
before the shopify case), no SQL or money touched, no disk I/O introduced,
no real shop/client name anywhere.

## Verified beyond the automated tests

- TDD claim re-verified independently (reviewer, then re-confirmed by the
  author after the reviewer's fix): removing the `if res.Format ==
  "shopify"` check makes `TestParseXLSX_ShopifyRejected` fail with a real
  `err = <nil>, want ErrXLSXShopifyUnsupported`, and the handler test fails
  with HTTP 200 and a preview claiming "0 ready, 1 will be skipped" — the
  exact silent-misparse-reported-as-success shape the card exists to fix.
  Restoring the check makes both pass again.
- `go build ./...`, `go vet ./...`, `gofmt -l` (clean on every changed
  file), `go test ./internal/catimport/... ./internal/pages/...` (all
  green, `-count=1`) run independently by both author and reviewer.
- `guard-i18n.sh`, `guard-help-drift.sh`, `guard-help-topics.sh`,
  `guard-competitor-naming.sh`, `guard-compliance-claims.sh`,
  `guard-page-http-error.sh` all green.
- Full-repo `go test ./... -race` was run once by the author: every package
  up to and including `internal/data` passed cleanly (`internal/catimport`
  15.2s, `internal/pages` and subpackages all green) before the run hit a
  10-minute timeout inside `internal/db`'s `TestMigration051_FreshOpenSeedsPT`
  (a Portugal fiscal-pilot migration-replay test, unrelated to this
  change's scope — this sandbox's SQLite performance is visibly slow
  throughout the run, e.g. `internal/data` alone took 411s, `internal/cloudsync`
  507s). Nothing in the diff touches `internal/db`, migrations, or anything
  PT-related; this reads as an environment-speed artifact of this sandbox,
  not a regression, but it wasn't re-run to a clean finish locally — real
  CI (GitHub Actions) is the authority on this and will be checked at the
  DevOps step.
- No UI (HTML) surface changed — the operator-facing text is a flat
  `http.Error` response, same mechanism as the existing
  `ErrXLSXMergedCells`/`ErrXLSXLegacyUnsupported` errors; nothing to
  screenshot or drive through Playwright.

## Not a finding, logged for follow-through

- **Language-pack PRs owed.** `import.error.xlsx_shopify_unsupported` is a
  brand-new core key. Per `CLAUDE.md`/`reviewer`'s rule 4, core merges
  first (`main` goes red on `lang-pack-drift`, expected), and the
  `ut-plugin-language-de` / `ut-plugin-language-es` catch-up PRs land in
  this same cycle, before anything else — tracked as this cycle's own
  obligation, not a new card.
