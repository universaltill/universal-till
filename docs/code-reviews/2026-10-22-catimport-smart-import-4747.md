# Review: catimport smart import (ut-docs#4747, ADR-0168)

Scope: `catimport/` only (nested module): `smart.go` (kinds, sources, aliases,
row issue type, refusals), `smart_products.go` (Epos Now shape, generic
products via DE/TR automatic mapping, tax snapping, shared row checks),
`smart_people.go` (`ParseCustomers`, `ParseStock`), additive fields on
`ImportItem`/`Result`, `RowFor` carries `Active`. Review: self-review by the
dev lane; the parent runs the independent Opus review before merge.

## TDD
`smart_test.go` was written first against invented headers/rows (the Epos
Now header is the card's, with an invented 27-column CRLF `;` fixture:
duplicate barcode, duplicate name, empty category, empty barcode, zero price,
unknown tax rate, long name, bad boolean, bad price). First run failed to
compile (`undefined: RowIssue`, `Options`, ...), then the implementation
made it pass. All pre-existing catimport tests pass unchanged.

## Findings and decisions
- Row issues use `Suggested *string`: nil = no suggestion, pointer to "" =
  "clear the field" (duplicate barcode keeps the first row only).
- The parser never invents a SKU on the item (RowFor's rule, #1459): a
  `missing_sku` issue carries a generated, file-unique suggestion; the
  consumer generates the real one on create after matching.
- A rate that fits none of the store's rates, or two, is an issue and the
  item gets no tax; it is never guessed. Group id (`20VAT`) breaks ties and
  supplies the rate for a zero price.
- `ParsePrice` strips junk ("3.0x" parses), so "bad number" examples are
  negatives and non-numerics; the suggestion drops the sign.
- Mixed detection is by customer columns plus a price column; `Options.Force`
  lets the preview's manual kind choice bypass mixed/unknown but never the
  required-columns check.
- Customer issues carry `Original` in memory (needed for the preview);
  ADR-0168 §5's "no values stored/logged" is the job's duty and is noted in
  the README.
- Known limit: `my-export` reads the first of the `;`-joined barcodes only.
- Epos Now customer/stock shapes are NOT added (ADR-0168 §1): generic aliases.
- Core-neutral and catimport-module guards pass; `golangci-lint` 0 issues.

## Evidence
`cd catimport && go test ./...` green; `go build ./...` at the root green.
A local probe of the owner's real Epos Now file (counts only, never data):
50 rows, 9 categories, 0 items at price 0, 0 blocked rows, 50 with a rate,
2 duplicate-barcode issues, 3 missing barcodes.

## Independent review (Opus)

Reviewer: Opus 5.5 subagent, 2026-10-22, on `641db146e`. Ran `go vet`,
`go test ./...` (and `-race`) in `catimport`, `go build ./...` and
`go test ./internal/catimport/...` at the root, `golangci-lint` (0 issues),
the catimport-module, core-neutral, deadcode-baseline and baseline-growth
guards, plus throw-away probes (not committed) for detection, edge files and
timing. `go.mod`/`go.sum` untouched by the PR, `go mod verify` clean,
excelize stays pinned at v2.11.0. Every pre-existing test file is unchanged.

### TDD re-verified
Reverted `catimport.go`/`rows.go` to `origin/main` and removed the three
`smart*.go` files: `smart_test.go` fails to compile (`undefined: RowIssue`,
`Options`, `SourceEposNow`, ...). Restored with `git checkout HEAD --
catimport`; tree clean at `641db146e`; tests green. The four new tests
below were run against the unfixed code first and failed with the stated
errors.

### Findings
| # | Severity | Finding | Status |
|---|---|---|---|
| B1 | blocker | `withAliasMapping` looped forever when two columns matching one alias set were both named in the shop's mapping for other fields (e.g. `Bezeichnung`→description, `Artikelname`→sku): the skip list dropped the previous column, so the walk bounced between them. A hung worker goroutine at 100% CPU. | Fixed: the skip list only grows. `TestWithAliasMappingTerminatesWhenAliasColumnsAreMapped` (hung 3 s before). |
| B2 | high | A grandfathered Shopify export was refused `unknown_kind` by `DetectKind`/`ParseProducts` (its price is `Variant Price`, which no alias knew), although `ParseMapped` parses it. | Fixed: `Variant Price` counts as the price column for a Shopify-format header. `TestShopifyExportIsAProductsFile`. |
| B3 | high | Epos Now row with a blank `SalePriceIncTax`: `missing_price` was a *warning*, so the row imported at price 0 (a free item, against the card's "0 free items"). | Fixed: `missing_price` is an error; a variable-price item (`IsVariablePrice` yes, priced at sale) raises no issue. `TestParseProductsEposNowMissingPriceBlocksUnlessVariable`. |
| M1 | medium | `parseQuantity("1.234,5")` = 1.2345 (DE/TR count misread); `1.000.000` refused. | Fixed: with both marks the last is the decimal; a repeated mark is thousands. `TestParseQuantityBothSeparators`. |
| M2 | medium | `finishProducts` checked "row already has issue X" by scanning every issue: quadratic (40k rows 1.7 s; a 64 MiB file would run for hours). | Fixed: a `(row, code)` set; 200k rows now 0.36 s (probe). `hasIssue` removed and its deadcode-baseline entry burned down. |
| L1 | low (accepted) | A single `.` is always decimal: DE `1.000` reads as 1, and a single `,` with three digits as thousands: DE `2,500` (kg) reads as 2500. Inherently ambiguous; the existing test pins `1.000`; the preview shows the parsed quantity. | Accepted; documented in `parseQuantity`. |
| L2 | low (accepted, pre-existing) | `ParsePrice` strips junk, so a formula cell `=1+1` reads as 11.00. Pre-existing `ParseMapped` behaviour, not this PR's. | Accepted; candidate for a separate card. |
| L3 | low (accepted) | `ParseCustomers` with `Name` + `Surname` (no first-name column) keeps only `Name`. | Accepted; mapping step covers it. |
| N1 | note for card E | Memory: the smart parsers buffer up to 64 MiB and allocate a 600-byte `ImportItem` per record, so the job must keep ut-cloud's `boundCSV` row pre-count (20,000 items) before calling `ParseProducts`/`ParseCustomers`/`ParseStock`. | For #4750. |
| N2 | note for cards E/F | Row issues carry `Original` (customer e-mail etc.) and suggestions echo cell text (category, name). The job must mask in the preview, keep values out of `report.csv`/logs for customers (ADR-0168 §5) and CSV-defuse anything it writes back out. `KindError` and row errors carry no cell values. | For #4750/#4751. |

Also checked without finding a defect: BOM, CRLF, `;`/tab/comma sniffing;
empty file (`read header: EOF`), header-only (0 items), binary (refused
`unknown_kind`), ragged rows short and long (no panic); dotted/dotless
`İ/ı`, umlauts and `ß` in DE/TR headers; `Name` alone is `unknown_kind`;
products+email is `mixed_kinds`; Loyverse/Square/SumUp detect as products;
`Name;Price;Supplier Phone` stays products; Epos Now inc/ex, 4-decimal ex
price and `"1,200.00"`; duplicate barcode keeps the first row and flags each
repeat; generated SKU suggestions are file-unique.

### Verdict
Safe to merge after the fixes above, once CI is green. Deferred: N1, N2 to
the job cards; L2 as a possible card.
