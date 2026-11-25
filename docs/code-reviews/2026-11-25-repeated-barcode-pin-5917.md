# Review: pin the till importer's "first imported row keeps a repeated barcode" (ut-docs#5917)

Branch `fix/5917-repeated-barcode-pass-on`. Test-only: `internal/pages/import_page_test.go`
`TestImport_RepeatedBarcodeFirstImportedRowKeepsIt`. Paired with ut-cloud's fix (review record
`ut-cloud/docs/code-reviews/2026-11-25-repeated-barcode-pass-on-5917.md`).

Pre-flight: Any change: review record = this file; help/en.json/migrations/catimport = n/a (no production change,
no catimport VERSION bump); follow-ups → ut-docs#7037.

The BA found the till's own `/import` already does what #5917 asks: it attaches a barcode repeated in the file, at
commit, to the first row that actually imports (`import_page.go` ~1472-1486). Nothing pinned it. The test imports a
file whose first barcode holder has no name (not imported) and asserts row 3 gets the barcode, row 2 is not imported,
no warnings, row 3 `created`. It passes on main, as intended (a pin, not a fix).

Verification (Dev + Tester): `go test ./internal/pages/ -run TestImport` pass; `gofmt -l` clean;
`guard-fixed-dates.sh`, `guard-data-access.sh` pass. Independent review (Opus 5.5): pin test green, rules clean, no findings.
