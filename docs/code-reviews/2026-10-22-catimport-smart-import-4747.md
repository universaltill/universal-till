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
