# Code review — demo catalog barcodes get valid EAN-13 check digits

- **Date:** 2026-07-31
- **Task:** ut-docs#17 (Demo catalog barcodes are not valid EAN-13)
- **Branch:** `fix/demo-barcode-ean13-checksums`
- **Author:** pipeline Dev step (Sonnet 5)
- **Independent reviewer:** general-purpose subagent on **Opus** (different model, per standing practice)

## What shipped

1. **`internal/db/migrations/001_init.sql`** — corrected the 13th (check)
   digit of all **50 `item_barcodes`** rows and, after review, all **12
   `variant_barcodes`** rows to a real EAN-13 mod-10 weighted checksum. Only
   the check digit changes on every row; the first 12 digits (and every
   other seed column) are untouched, so the demo numbering scheme and item
   associations are unaffected. No app code validates EAN-13 checksums
   today (confirmed by grep), so this is demo-data-only — the printed
   scanner-test sheet can now render as EAN13 instead of falling back to
   Code128, with no runtime behavior change.
2. **`internal/db/barcode_seed_test.go`** (new, package `db`) — two
   regression tests, following the existing `dead_seed_test.go` pattern of
   opening a freshly-migrated temp DB and asserting on the real seeded
   rows: `TestSeedItemBarcodesValidEAN13` and (added during review)
   `TestSeedVariantBarcodesValidEAN13`. Both assert the expected row count
   and that every barcode passes a private `isValidEAN13()` checksum
   helper, naming any bad barcode in the failure message.
3. **Five Playwright e2e specs** (`e2e/tests/{sale,rtl,ui-scale-basket,
   catalog-image-to-till,inventory-to-till}.spec.ts`) — updated the
   hardcoded old barcode literals (and adjacent comments) they type into
   the till's scan box to look up specific demo items by their now-changed
   primary barcode. Not caught by BA/Architect grooming (which only
   grepped `*.go`/`*.sql`); found by Tester grepping `e2e/` specifically
   and confirmed by actually running the suite.

## TDD evidence (independently re-verified, not just claimed)

Both new tests were written first and confirmed to fail against the
pre-fix seed data, listing every bad barcode in the failure message, then
passed after the corresponding migration fix. The reviewer **mutation-tested**
`TestSeedItemBarcodesValidEAN13` independently (reverted one corrected
barcode back to broken, confirmed the named failure, restored). The
pipeline mutation-tested the same way before review, and TDD'd
`TestSeedVariantBarcodesValidEAN13` the same way after the reviewer's
finding (stash-reverted the `variant_barcodes` fix, confirmed the test
failed naming all 12 bad barcodes, restored, confirmed pass).

Checksum computation was independently cross-validated **three times**
with differently-structured implementations (left-to-right weighting used
in the fix, right-to-left/GS1-textbook weighting used by Tester, and the
reviewer's own from-scratch script) — zero mismatches across all 62
corrected barcodes, and no `TEXT PRIMARY KEY` collisions in either table.

## Verified beyond automated tests

- Full `go build ./...`, `go vet ./...`, `go test ./...`, and both
  `scripts/ci/guard-data-access.sh` / `scripts/ci/guard-i18n.sh` — green,
  except `TestSaveCleansUpDirectoryOnWriteFailure` (`internal/issuereport`),
  confirmed **pre-existing and unrelated** by both Tester and the reviewer
  independently: it fails identically on unmodified `main` (a
  read-only-directory permission test that doesn't work when the test
  process runs as root, which this container does).
- Real Playwright e2e run (not skipped): 4/5 of the barcode-touching specs
  pass against a real booted till server; `catalog-image-to-till.spec.ts`
  fails on an unrelated pre-existing image-loading assertion that occurs
  before the barcode-scan step is ever reached — confirmed pre-existing
  against unmodified `main`.
- Repo-wide grep (reviewer) for every old barcode literal across the whole
  tree — zero stale references left anywhere outside the intentionally
  independent test fixtures in `internal/catimport/catimport_test.go`,
  `internal/cloudsync/cloudsync_test.go`, and `internal/print/escpos_test.go`
  (verified these genuinely don't read from the migration — one hand-builds
  its own in-memory schema, one is a bare CSV string, one is pure
  string/byte formatting).
- Checksum helper itself exercised (reviewer) against real-world valid
  EAN-13s (Coca-Cola, an ISBN-13, a GS1 example), invalid/malformed inputs
  (wrong length, non-digit, off-by-one check digit), and an exhaustive
  property check (for 200 different 12-digit prefixes exactly one of the
  10 possible check digits is accepted) — confirms it's a correct standard
  implementation, not just "happens to pass on these 62 values."
- No file-write path introduced (both recurring `os.MkdirAll` /
  `paths.Data(...)` bug classes checked and confirmed not applicable — the
  only path used is `t.TempDir()`, which already exists).
- No real client/shop name in the diff.

## Review findings

| # | Severity | Finding | Outcome |
|---|----------|---------|---------|
| 1 | should-fix | `variant_barcodes` (12 rows) also seeds `barcode_type='EAN13'` in the same migration/seed block with the identical fabricated-checksum defect — same defect class the ticket exists to eliminate, and scannable at the till via `POSRepo.resolveVariant` — but was missed by the original BA grep (only searched the `5xxx` item-barcode prefix, not the `6xxx` variant-barcode prefix) | **Fixed in-branch** — all 12 corrected, independently re-verified, new `TestSeedVariantBarcodesValidEAN13` guards it |
| 2 | note-only | `shortcut_buttons` (10 rows, prefix `2xxx`) has the same invalid-checksum pattern, but has no `barcode_type` column (never claims EAN13) and the `2` prefix is GS1's restricted in-store range — weaker case for "same defect" than variant_barcodes | **Deferred** — new Backlog card to be filed (distinct from the item/variant fix, out of this ticket's literal scope) |
| 3 | note-only | Runtime path `CatalogRepo.AddBarcode` defaults `BarcodeType = "EAN13"` for any user-entered barcode with no checksum/length validation beyond 6–14 digits, so the *runtime* can still create bogus-checksum EAN13-labelled rows even though the *seed* is now clean | **Deferred** — new Backlog card to be filed (genuinely separate: runtime validation feature vs. this ticket's demo-data scope) |
| 4 | nit | `len(barcodes) != 50` / `!= 12` hardcoded row-count assertions will need updating if the demo catalog ever grows | **Accepted as-is** — intentional guard that the query actually saw real seed data, not a vacuous pass |

## Verdict

**Safe to merge.** The one should-fix finding (variant_barcodes) was
fixed and re-verified in-branch with the same TDD/independent-checksum
rigor as the original item_barcodes fix. The two note-only findings are
genuinely separate scope (a different table with a weaker claim to the
same defect, and a runtime-validation feature) and are carded as
follow-ups rather than folded into this diff.
