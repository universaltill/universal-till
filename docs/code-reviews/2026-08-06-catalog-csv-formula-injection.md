# 2026-08-06 — Catalog export: round-trip-safe CSV/formula-injection defusing

Card: [ut-docs#321](https://github.com/universaltill/ut-docs/issues/321) (p3, security)
Branch: `fix/321-catalog-csv-formula-injection`

## What shipped

`writeCatalogCSV` (`internal/pages/import_page.go`, hoisted from a private
closure to a package-level function so it's directly testable) now wraps
`Name`, `SKU`, `Barcode`, `Category` and `Description` — the free-typed,
attacker-reachable columns, reachable via an uploaded catalog CSV through
`POST /api/import` — through the existing `csvSafe()` helper
(`internal/pages/csv_export.go`, unchanged, shared with the invoice/audit
exports from ut-docs#195). A formula-shaped value like `=cmd|'/c calc'!A1`
now opens as inert text in Excel/LibreOffice, same defusing mechanism as
#195. `Price`/`Sold by weight`/`In stock`/`Active` are left untouched —
system-formatted, not free text; #195's review already found that
blanket-sanitizing corrupts legitimate negative amounts, and the same
reasoning applies to `Stock` here.

The harder part #195 explicitly scoped out: catalog CSV round-trips
through the till's own importer (`internal/catimport.Parse`) as a
documented migration path (export → import on a fresh till,
`TestExportCSVRoundTripsThroughImporter`), so `csvSafe`'s permanent
leading-apostrophe mitigation can't just be reused as-is — it would
silently pollute every export→import cycle. New `stripCSVDefuse()`
(`internal/catimport/catimport.go`) reverses it on import: it strips a
leading `'` only when the byte immediately after it is itself one of
`csvSafe`'s trigger chars (`=+-@`, tab, CR) — the exact two-byte shape
`csvSafe` emits, since it only ever adds `'` right before a trigger char.
A genuine value that merely starts with `'` and isn't followed by a
trigger char (e.g. `'Twas the night`) is left completely alone. Wired
into `Parse` for the same five columns, with defuse-stripping applied to
the raw barcode *before* `normalizeBarcode` so a defused value isn't
misreported as an unsupported barcode shape.

## Independent review (Opus subagent, complexity:medium routing)

Verdict: **yes, with follow-ups** — no blockers. The review independently
re-verified both TDD claims by mutation (reverting `stripCSVDefuse` to a
no-op, and dropping the `csvSafe` calls from `writeCatalogCSV`), confirmed
each mutation fails the new regression test with the expected assertion,
and confirmed the existing `TestExportCSVRoundTripsThroughImporter` stays
green under the second mutation (proving the two tests check genuinely
different things, not the same thing twice).

Findings:

1. **MINOR, fixed in this branch.** `csvSafe` is not injective for values
   that already start with `'` immediately followed by a trigger char —
   `csvSafe("'=X")` and `csvSafe("=X")` both produce `"'=X"`, so
   `stripCSVDefuse` can't tell a pre-existing `'=`-shaped value apart from
   its own defuse marker. A value already stored in that shape loses its
   leading apostrophe on one export→import cycle, then stays stable. The
   spreadsheet stays safe either way (every export re-defuses whatever is
   currently stored) — this is narrow data drift on an edge case
   vanishingly unlikely in real product data, not a reopened injection
   hole, and not a blocker. Original doc comment only described the safe
   (non-colliding) case in a way that read as covering everything. Fixed
   by expanding `stripCSVDefuse`'s doc comment to state the known lossy
   collision explicitly, and re-labelling the existing
   `'=cmd|'/c calc'!A1` case in `TestStripCSVDefuse` as deliberately
   pinning this accepted trade-off rather than only incidentally covering
   it. A fully lossless fix would need `csvSafe` itself (shared with
   invoice/audit) to double a pre-existing leading `'` before a trigger
   char — deferred to its own card rather than expanding this one's scope.
2. **MINOR, deferred to a follow-up Backlog card.** The `csvFormulaTriggers`
   char set is duplicated in `catimport.go` (can't import `pages` — cycle)
   with only a comment, not a test, keeping it in sync with `csvSafe`'s
   trigger set in `pages/csv_export.go`. A drift (e.g. someone adds a
   trigger char to `csvSafe` and forgets the mirror) would silently break
   round-tripping with zero test failures. Recommended fix: a table test
   in `internal/pages` (already imports `catimport`) that writes every
   char in the trigger set through `writeCatalogCSV` and asserts
   `catimport.Parse` returns it verbatim in both directions — this would
   also lock in `encoding/csv` quoting behaviour (embedded commas/quotes)
   on the catalog path, currently exercised only ad hoc, not in the test
   suite. Filed as ut-docs#356.
3. **NIT, not fixed.** `get()`'s `strings.TrimSpace` runs before
   `stripCSVDefuse`, so a `Name` of exactly a single tab character
   round-trips to a lone `'`. Reviewer's own characterization: "absurdly
   contrived." Not worth a card.
4–11. Non-issues, independently verified: foreign-CSV (Loyverse/Square)
   blast radius from `stripCSVDefuse` is acceptable (many source systems
   apply the same apostrophe convention, so stripping is often the
   correct restoration); defuse-strip-before-`normalizeBarcode` ordering
   is correct and matters; `Price`/`Stock`/flags exclusion is correct and
   safe (repeats #195's negative-amount lesson); no regression to
   invoice/audit (`csvSafe` itself untouched, their tests green); the two
   recurring bug classes this pipeline watches for
   (missing `os.MkdirAll`, cwd-relative paths instead of `paths.Data`)
   don't apply — no new disk writes; no i18n/manual/README update owed
   (pure Go CSV writer/parser change, no template/route/UI surface); no
   real client/shop name in test data.

## Verification

- `go build ./... && go vet ./...`, `gofmt -l` — clean on all 4 changed
  files.
- `bash scripts/ci/guard-data-access.sh` and `bash scripts/ci/guard-i18n.sh`
  — clean (no SQL, no new user-facing strings).
- `go test ./internal/pages/... ./internal/catimport/...` (including
  `-race`) — full pass, plus targeted `-v` runs confirming every
  CSV/catalog/invoice/audit test individually.
- `go test ./...` — one failure, `internal/issuereport.
  TestSaveCleansUpDirectoryOnWriteFailure`; confirmed environmental
  (reproduces identically on unmodified `main` — this container runs as
  root, which bypasses the read-only-directory permission check that test
  relies on) and unrelated to this diff, which touches only
  `internal/catimport` and `internal/pages`.
- TDD: both new tests (`TestStripCSVDefuse`, `TestExportCatalogCSV_
  FormulaShapedValuesDefusedAndRoundTrip`) written and confirmed failing
  against the pre-fix code before implementing; independently
  re-confirmed by the reviewing agent via targeted mutation (see above).

## Safe to merge

Yes. No blockers found or introduced. One follow-up card filed
(ut-docs#356, drift-guard test for the duplicated trigger-char set); the
docstring/test-labelling finding was fixed directly in this branch.
