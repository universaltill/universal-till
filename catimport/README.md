# catimport — the shared catalogue parser

`github.com/universaltill/universal-till/catimport` reads catalogue exports
(generic CSV/XLSX with header synonyms, plus the grandfathered Loyverse,
Square, SumUp and Shopify dialects) into neutral `ImportItem`s, and `ToRows`
turns a parsed file into ADR-0051 interchange rows. The till and ut-cloud
use this one copy (ADR-0164 §1, ut-docs#4525).

```go
res, err := catimport.Parse(r, currencyDecimals, barcode.Default().IDs(), false)
rows := catimport.ToRows(res) // []catimport.Row, money in minor units
```

- **Header claims:** `HeaderRow` reads a CSV's or workbook's header row
  (bounded; a ZIP is opened as a workbook only when the file is named
  `.xlsx`/`.xlsm`) and `HeadersClaim` matches it against a file-rows import
  plugin's declared `file_headers`, so the till and ut-cloud offer a file
  to the same plugin (ADR-0164 §2). `NeutralHeader` names the columns the
  standard importer reads in any file; a `file_headers` claim must include
  one that is not neutral.
- **Column mapping (ut-docs#4550):** `ParseMapped` / `ParseXLSXMapped`
  take a `Mapping` (neutral field → the file's header, `""` = don't read
  the field) for a **generic** file whose headers the synonyms don't know.
  `MappableFields` lists the fields; `ValidateMapping` checks a mapping
  against `HeaderRow`'s headers and answers a `*MappingError` with a
  stable `MappingIssue*` code. A mapped column feeds only its mapped
  field. A known export (Loyverse, Square, SumUp, Shopify) refuses a
  mapping (`not_generic`). A nil mapping is `Parse` / `ParseXLSX`.
- **Packages:** `catimport` (CSV/XLSX parsing, detection, `Row`/`ToRows`),
  `catimport/barcode` (the ADR-0059 symbology registry), and
  `catimport/catalogrules` (item types, the net-quantity rule).
- **Pure.** No database, no files, no network. It never imports a till
  package (`internal/...`): `scripts/ci/guard-catimport-module.sh`.
- **Few vendor formats.** A vendor's export is parsed by its
  `ut-plugin-import-<system>` plugin (ADR-0164 §2). The one built-in shape
  admitted since is Epos Now's products export (ADR-0168 §1), because a
  real export is in hand; a vendor with an API path or logic beyond a
  header map still goes through a plugin.
- **Smart import (ADR-0168, `smart.go`).** `DetectSource(headers)` names
  `eposnow` | `my-export` | `till-export` | a grandfathered format |
  `generic`; `DetectKind(headers)` says `products` | `stock` | `customers`,
  or refuses `mixed_kinds` / `unknown_kind` (naming the missing columns per
  kind), from EN + DE + TR aliases. `ParseProducts`, `ParseStock` and
  `ParseCustomers` refuse a file that is not their kind (`*KindError`,
  nothing partly imported) and return **row issues**
  `{row, code, severity, field, original, suggested?}` (ADR-0168 §3a): the
  suggestion is the default fix (unknown category -> create, duplicate
  barcode -> keep the first row only, missing SKU/barcode -> a generated
  value, bad number/boolean -> the parsed guess, unknown tax group -> the
  nearest known rate, over-long name -> shortened). The caller adds
  store-dependent alternatives. Epos Now customer and stock shapes are not
  built-in until a real header row exists; those files read through the
  generic aliases and the mapping.
- **The till's own importers stay in the till.** `.bkp` and gift-voucher
  balance import, and the placeholder icons, live in
  `internal/catimport`.

## Versions

- universal-till builds this tree through `replace … => ./catimport` in its
  `go.mod`. A `replace` is not transitive, so another module that imports
  both universal-till and catimport needs the same `replace`, or a tag.
- ut-cloud will require a tag, `catimport/vX.Y.Z`, with no `replace`,
  once it consumes the module (card #4528). Its `manifest-contract-guard`
  job will then run `scripts/ci/check-catimport-pin.sh`, which fails while
  `catimport/` has changed since the pinned tag. That job's checkout of
  universal-till needs `fetch-depth: 0` and `fetch-tags: true`: the check
  diffs the tag against `HEAD`, so a shallow or tagless clone can't run it.
- **After merging a change here:** tag the merge commit
  `catimport/vX.Y.Z` (patch for a fix, minor for a new field or function)
  and (once #4528 lands) bump ut-cloud's `require`.

Tests: `cd catimport && go test ./...` (the root `go test ./...` doesn't
reach a nested module; CI runs both).
