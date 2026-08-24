# Code review — catimport uses the shared barcode symbology registry (ut-docs#936)

- **Date**: 2026-08-24
- **Card**: universaltill/ut-docs#936 — "internal/catimport: run the same
  barcode parser/enabled-symbology decision as scan (folds in #302)"
- **ADR**: ADR-0059 (barcode symbology registry) — this is the fourth split
  card (catimport integration).
- **Author model**: Sonnet (inline). **Independent review**: Opus, fresh
  context, isolated git worktree (card is `complexity:medium`).
- **Branch**: `feat/936-catimport-barcode-registry`.

## What shipped

`internal/catimport`'s `normalizeBarcode` previously discarded short /
alphanumeric barcodes with its own ad hoc 6–14-digit-only rule, independent
of what the scan path and `CatalogRepo.AddBarcode` accept via the shared
`internal/barcode` registry (ADR-0059). Import and scan therefore disagreed
about what a valid code is: a 4-digit produce PLU or an alphanumeric
supplier code imported with no barcode attached, silently.

This change replaces that ad hoc rule with a call to the same
`barcode.Default().Match(enabledIDs, code)` function the scan path and
`AddBarcode` already use (shipped by #933/#934), so import and scan can
never disagree:

- `catimport.Parse` gains an `enabledSymbologyIDs []string` parameter; the
  pages layer reads the shop's enabled set via
  `data.SettingsRepo.EnabledBarcodeSymbologies` (catimport itself stays a
  pure parser with no DB access) and threads it through.
- `normalizeBarcode` now returns the matched `barcode.Decoded` (symbology
  id + decoded LookupKey) instead of applying a digit-length rule.
- `ImportItem` carries the decoded `BarcodeType`; the commit loop passes it
  to `AddBarcode` as an explicit `BarcodeType` so the code is decoded
  exactly once (see F1 below).
- The three old `BarcodeIssue{UnsupportedFormat,TooShort,TooLong}` reason
  codes (and their `import.status.barcode_{unsupported_format,too_short,
  too_long}` locale keys) collapse to one `BarcodeIssueNoSymbologyMatch` /
  `import.status.barcode_no_symbology_match`, reachable only once a shop
  narrows its enabled set away from the default permissive catch-alls
  (CODE128/INTERNAL_PLU) — under the default set, nothing is rejected, so
  every barcode that imported before still imports (compatibility
  preserved, ADR-0059 §2).

Under the default enabled set the behaviour change is purely additive: PLU
and alphanumeric codes that used to be dropped now attach.

## Acceptance criteria — verified

- 4-digit PLU + alphanumeric supplier code import with the barcode attached
  under the default set: `TestParseImportsShortAndAlphanumericBarcodes`,
  `TestImport_ShortPLUBarcodeImportsAttached`.
- Round-trip: import a code, then scan it on the sale screen and resolve
  the item: `TestImport_ShortPLUBarcodeImportsAttached` drives
  `POSRepo.ResolveScanLine` on the imported row.
- Row status reports the reason when a code is rejected by a narrowed
  enabled set: `TestParseReportsNoSymbologyMatchReason`,
  `TestImport_NoSymbologyMatchWarnsButStillImports`.

TDD reverification (revert-then-restore, done by the reviewer in an
isolated worktree and independently by the author): reverting
`normalizeBarcode` to the old digit-only rule fails the three
PLU/alphanumeric tests with real assertion errors; restoring makes them
pass.

## Independent review findings (Opus) and resolution

- **F1 (minor, latent — fixed).** catimport stores the decoded LookupKey in
  `ImportItem.Barcode`; the commit loop originally called `AddBarcode`
  without a `BarcodeType`, so `AddBarcode`'s inference path re-ran the
  registry match on the already-decoded key. For an embedded-data symbology
  that re-decodes to the wrong type (CODE128), and under a narrowed enabled
  set it fails the second match entirely and drops the barcode — exactly
  the import/scan disagreement this card removes. Not reachable under the
  default set (embedded symbologies default off, so LookupKey == code), but
  fixed properly: `ImportItem` now carries the decoded `BarcodeType`, passed
  as an explicit `BarcodeType` to `AddBarcode` so decoding happens once.
  Guarded by a new regression test,
  `TestImport_EmbeddedSymbologyImportDecodesOnce`, which fails (barcode
  dropped, "matches no enabled symbology") without the fix and passes with
  it — TDD-verified via revert-then-restore.
- **F2 (minor, reachable — fixed).** The trailing `.0` spreadsheet-artifact
  strip was unconditional; a legitimately-alphanumeric code ending `.0`
  ("ABC.0") would be truncated, storing a different string than the CSV and
  making a later scan of the literal code fail. Now the `.0` is stripped
  only when the remainder is all-digit. Covered by a `TestNormalizeBarcode`
  case.
- **F3 (minor — fixed).** The stale `ImportItem.BarcodeIssue` doc comment
  (still describing the old "discarded shape, e.g. a 4-digit PLU" behaviour
  and deferring to #295) is rewritten to the registry-based reality.
- **F4 (minor — fixed).** Re-added the `5.449E+12` scientific-notation case
  to `TestNormalizeBarcode` with its new expectation (stored verbatim via
  CODE128's catch-all under the default set — the documented ADR-0059 §2
  trade-off).
- **F6 (operational — coordinated).** en.json drops 3 keys and adds 1;
  `lang-pack-drift` is blocking on push to `main`. The
  `ut-plugin-language-{de,es}` packs are updated in the same cycle (remove
  the 3 orphaned keys, add `import.status.barcode_no_symbology_match`),
  following the ecosystem's established core-first drift-fixup order.
- **F5, F7, manual note (nitpick/backlog).** The round-trip test is not a
  tautology (mutation-tested); the enabled-set-ignored dimension is covered
  by `internal/data/pos_repo_scanline_test.go`. F7 (a stored `"null"`
  enabled-symbologies row disabling all barcodes) is pre-existing and shared
  with #934's scan path — noted for a backlog card before #935 ships a UI
  that can write the setting. No help topic states the old digit rule, so
  no manual prose went stale (both help guards pass).

## Verification run

`gofmt -l` clean; `go build ./...`, `go vet ./...` clean;
`go test ./...` green across the whole repo (non-`-race`, the CI-equivalent
invocation); `go test ./internal/catimport/... -race` green.
`guard-data-access.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
`guard-help-topics.sh`, `guard-docs-shots.sh`, `guard-android-i18n.sh`,
`guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-htmx-loaded.sh`
all pass. (`internal/pages -race` intermittently timed out under concurrent
load — the failing tail was an unrelated auto-update test; an isolated
`-race` re-run passed clean. CI does not use `-race`.)

No file writes in the diff (so neither recurring `os.MkdirAll` / cwd-path
bug class applies); no SQL outside `internal/data`; no money handling; no
real shop/client name introduced.

## Verdict

Safe to merge. Additive under the default enabled set; the latent
embedded-symbology correctness gap the review found is fixed and guarded;
lang-pack drift is handled in the same cycle.
