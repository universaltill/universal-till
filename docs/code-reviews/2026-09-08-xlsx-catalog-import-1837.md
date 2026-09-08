# Code review: Excel (.xlsx) catalog import (ut-docs#1837)

**Date:** 2026-09-08
**Card:** ut-docs#1837 — "Excel (.xlsx) catalog import: the file picker will not
even let the merchant select the file" (German pilot merchant blocked from
migrating his catalog)
**Branch:** `fix/1837-xlsx-catalog-import`
**Diff:** `internal/catimport/xlsx.go` (new), `internal/catimport/xlsx_test.go`
(new), `internal/catimport/catimport.go` (`Result.SheetName`),
`internal/pages/import_page.go`, `internal/pages/import_xlsx_page_test.go`
(new), `internal/pages/import_page_test.go`, `web/ui/pages/import.html`,
`web/ui/pages/setup.html`, `web/locales/{en,ar,fa,tr}.json`,
`web/help/{en,de,ar,fa,tr}/catalog.md`, `go.mod`/`go.sum`
**Reviewer:** independent (different model, own worktree, did not write the code)

## What shipped

`catimport.ParseXLSX` reads an Office Open XML workbook into the same neutral
`ImportItem` list `Parse` (CSV) and `ParseBkp` produce, reusing
`DetectFormat`/`headerIndex`/`ParsePrice`/`ParseTaxRateBP`/`normalizeBarcode`
so a spreadsheet and a CSV of the same export behave identically (AC1). Only
the first worksheet is read and its name is reported to the operator through
the new `Result.SheetName` and `import.xlsx_sheet_used` notice (AC2). Merged
cells reject the whole file (`ErrXLSXMergedCells`) and a legacy binary `.xls`
gets its own "save as .xlsx or CSV" message (`ErrXLSXLegacyUnsupported`, AC6)
rather than the generic `invalid_file` one.

`internal/pages/import_page.go`'s format sniff went from two-way to three-way:
`.bkp` and `.xlsx` are both plain ZIPs with identical magic bytes, so
`LooksLikeXLSXZip` (does it open as a workbook with at least one sheet)
decides between them, and a non-ZIP upload is additionally sniffed for the
OLE2/CFBF signature. The `accept=` attribute on both `/import` and the setup
wizard's upload panel gained `.xlsx` plus its real MIME type — the actual
reported bug, since Android's picker filters strictly on that list.

Three blocking defects and one behavioural drift were found and fixed during
this review (all in this worktree, all with regression tests):

1. **A rounding display format silently changed the imported price.**
   `GetRows` APPLIES each cell's number format. Measured against excelize
   v2.11.0: a stored `1.4` under a `#,##0` or `0` format reads back as `"1"`,
   so a €1.40 item imported at €1.00 — and `0.89` under `"0"` imported at
   €1.00 — with no warning anywhere. The original code comment asserted the
   opposite ("a genuinely-numeric Excel cell always round-trips through
   GetRows as a plain dot-decimal string"); it does not — grouped
   (`"1,234.56"`), currency-suffixed (`" 1,234.56 € "`) and rounded (`"1"`)
   renderings all occur, and only the first two survive `ParsePrice` intact.
   Fixed by reading the **price and stock** columns from a second, raw
   (`excelize.Options{RawCellValue: true}`) pass, falling back to the
   formatted text when the raw cell isn't a plain number — which is exactly
   the text-stored German price (`"1.234,56"`) case, unchanged. The **tax**
   columns deliberately keep the formatted value: a percent-formatted cell
   stores `0.19`, and reading that raw would parse "successfully" as a 0.19%
   rate — silently wrong on a compliance-sensitive field (ut-docs#512).
   Comment corrected to match. `TestParseXLSX_RoundingDisplayFormatDoesNotChangePrice`,
   `TestParseXLSX_PercentFormattedTaxCell`.
2. **Decompression bomb / unbounded RAM on the new upload path.** Both
   `excelize.OpenReader` calls ran on excelize's defaults: `UnzipSizeLimit`
   is **16GB**, and `ReadZipReader` buffers every non-worksheet member whole
   in memory. Measured: a 210KB upload declaring a 200MB member is accepted
   and fully allocated — twice, once by the sniff and once by the parse.
   Nothing upstream bounds it: `POST /api/import` calls
   `ParseMultipartForm(20<<20)`, which is an in-memory threshold (the rest
   spools to disk), not a size cap, and unlike `POST /api/data/import` it has
   no `http.MaxBytesReader`. On the Pi-class hardware this ships on that is an
   OOM kill of the process that is also serving checkout, reachable by any
   manager and — via the wizard's first-boot preview exemption — by an
   anonymous LAN client on a not-yet-configured till. `ParseBkp` already
   guards this exact class (`bkpMaxMetaSize`/`bkpMaxDBSize`); the new path had
   no equivalent. Fixed with `xlsxMaxUnzipSize = 128 << 20` applied to both
   calls through one shared `xlsxOpenOptions()`.
   `TestParseXLSX_DecompressionBombRejected`.
3. **The German manual was left behind.** `web/help/de/catalog.md` still
   described import as CSV/`.bkp` only. The German manual landed two commits
   earlier (7b0eb61) and the merchant this card exists for reads it. en/ar/fa/tr
   were updated in the diff; `de` was not, and `guard-help-topics.sh` only
   enforces structure, not prose. Added the mirrored paragraph.
4. **(drift, non-blocking, fixed)** A blank separator row became a phantom
   problem row. `encoding/csv` drops a blank line so `Parse` never emits one;
   `GetRows` hands back an empty record, which produced a
   `missing_name_and_bad_price` row on the preview grid for a spreadsheet the
   CSV of the same export imported cleanly — a direct AC1 violation and an
   avoidable "what is wrong with row 3?" for the operator. `isBlankRow` skip
   added; `TestParseXLSX_BlankSeparatorRowSkipped` asserts xlsx and CSV of the
   same export agree row-for-row.

Two test-coverage gaps were also closed at the pages layer:
`TestImport_XLSXUnconfirmedCurrencyGatedAndReparses` (the currency-confirm
re-parse branch this card added a third case to had no coverage — the same gap
finding F10 closed for `.bkp` on ut-docs#970) and
`TestImport_BkpStillRoutesToBkpParserAfterXLSXSniff` (guards the `isBkp = the
upload is a ZIP` → `isBkp = a ZIP the xlsx sniff rejected` rewiring against a
too-eager sniff swallowing the pilot's own till backup).

## What was verified beyond automated tests

- **Format misclassification, probed both directions (brief item 1).** A
  synthetic `.bkp`-shaped ZIP (`backup.db` + `meta.inf` + `documents.zip`),
  a ZIP carrying an `xl/worksheets/sheet1.xml`-named member but no OOXML
  content types, and an empty-EOCD ZIP were all correctly rejected by
  `LooksLikeXLSXZip`; a real workbook was accepted. excelize requires
  `[Content_Types].xml` + a parseable `xl/workbook.xml` before it reports any
  sheet, which is strictly more than any `.bkp` carries. No misclassification
  found in either direction.
- **The currency-confirm re-parse Seek (brief item 4), settled by mutation.**
  Deleting the `file.Seek(0, io.SeekStart)` before the re-parse switch makes
  only the **CSV** test fail (`read header: EOF`); the `.bkp` and the new
  `.xlsx` tests still pass. Both take `io.ReaderAt` and read through
  `io.NewSectionReader`/`ReadAt`, which is independent of the file's current
  offset (`multipart.File` is `*os.File` or a `*io.SectionReader`; `ReadAt`
  moves neither). So the Seek is correct and, for the xlsx branch,
  provably not load-bearing — no silent-corruption risk on that path. The
  in-code comment claiming this is accurate. Mutation reverted.
- **Resource cost of the double open (brief item 3), measured.** On a
  generated 8,000-row / 6-column workbook (249KB on the wire): the sniff's own
  `OpenReader` costs 8.3ms and ~4MB of cumulative allocation, versus 871ms and
  ~223MB for the real parse — about **1%** overhead, not a doubling, because
  `OpenReader` parses only the workbook/content-type parts and never touches
  the worksheet rows. Both handles are `defer f.Close()`'d on every return
  path, including the sniff's early-error return; no leak. The raw second
  `GetRows` pass this review added takes that parse to 1.31s / ~329MB (+50%
  time, +48% allocation) — accepted as the price of not silently mispricing a
  catalog, on a one-time migration already behind the "Importing…" busy state.
- **The merged-cell rejection IS over-broad — confirmed, and deliberately
  left alone (brief item 2).** `GetMergeCells` returns every merge in the
  sheet, so a merged internal note 38 rows *below* otherwise-clean header and
  data rejects the whole workbook, as does a decorative merged title banner
  above the header. This is the documented "reject or report, never guess"
  tradeoff (AC4), it fails safe, and the message tells the operator exactly
  what to do, so it is not a blocker — but it will reject real merchant
  exports whose data is perfectly readable. A follow-up to scope the check to
  the header+data rectangle is recommended (see Deferred). Mutation-testing the
  check out also showed *why* it earns its place: with the merge across the
  header row, the preview silently priced the item at £0.00 rather than failing.
- **Number-format behaviour catalogued** (the source of finding 1): `general`
  → `"1234.56"`, `#,##0.00` → `"1,234.56"`, `#,##0.00\ "€"` → `"1,234.56 €"`,
  `[$-407]#,##0.00` → `"1,234.56"` (excelize does not emit German separators
  for a language-tagged format), accounting → `" 1,234.56 € "`, `#,##0`/`0` →
  `"1"`. Numeric barcode and SKU cells (Excel's habitual conversion of a
  pasted EAN into a number) round-trip fully — `5449000000996` stays
  `5449000000996` and matches EAN13 — excelize does not reproduce Excel's own
  scientific-notation *display*. A percent-formatted tax cell (`0.19` under
  `"0%"`) reads `"19%"` → 1900bp, correct.
- **TDD re-verified personally, four mutations, each reverted after.**
  (a) merged-cell rejection removed → `TestParseXLSX_MergedCells` and
  `TestImport_XLSXMergedCellsRejected` both fail (the latter with a 200 and a
  £0.00 price in the preview body); (b) the raw numeric pass bypassed →
  `TestParseXLSX_RoundingDisplayFormatDoesNotChangePrice` fails on all three
  rounding subtests with `PriceMinor=100` for both a stored 1.4 and a stored
  0.89, while the German text-price and percent-tax tests stay green (proving
  the raw pass doesn't regress them); (c) the blank-row skip removed →
  `TestParseXLSX_BlankSeparatorRowSkipped` fails with 3 items and a
  `missing_name_and_bad_price` phantom; (d) the unzip bound removed →
  `TestParseXLSX_DecompressionBombRejected` fails on both the sniff and the
  parse, and the test's own runtime jumps from 0.8s to 4.1s as the bomb is
  actually allocated. All restored; `git diff` against the backup copies is
  empty and the suite is green.
- **Line-by-line drift audit of the duplicated row loop (brief item 6)**:
  name/SKU/barcode/category/department/description/weighed, the
  `BarcodeIssueNoSymbologyMatch` condition, the Square variation-name special
  case, stock, tax and takeaway-tax issue tracking, the
  `IssueMissingNameAndBadPrice` combo switch and the
  `useItemNumbersAsBarcodes`/`seenForBarcode` first-occurrence-wins dedup are
  now field-for-field identical to `Parse`. The only remaining deliberate
  difference is `stripCSVDefuse`, correctly not applied (it reverses *this*
  app's own CSV export defusing, meaningless for a third-party workbook), and
  it is documented as such.
- **No new disk writes (brief item 9).** Nothing in the diff under `internal/`
  adds an `os.`/`paths.`/`filepath.` write, so neither recurring bug class (a
  missing `os.MkdirAll` before `paths.Data`, or a cwd-relative path) applies.
  One nuance for the package doc's "pure, disk-free parser" claim: excelize
  spills a worksheet or shared-strings member above its derived
  `UnzipXMLSizeLimit` (now min(128MB, 16MB) = 16MB) to `os.CreateTemp` and
  removes it on `f.Close()` — the same letter-only exception the package doc
  already records for `ParseBkp`, and both `Close`s here are deferred.
- **`accept=` parity (brief item 8)**: `grep -o 'accept="[^"]*"'` across
  `web/ui/pages/import.html` and `web/ui/pages/setup.html` returns exactly one
  distinct string, so the two cannot have drifted.
- **Test data and secrets (brief item 10)**: no real client or shop name;
  "Coca-Cola 330ml" matches the existing convention in `catimport_test.go` and
  `placeholder_test.go`, and "Cafe Example"/"Espressomaschine"/"Widget" are
  plainly synthetic. No secret-shaped literal; the only byte constants are the
  published ZIP and OLE2/CFBF format signatures.
- **New dependency reviewed — no ADR needed, agreeing with the developer.**
  `github.com/xuri/excelize/v2` v2.11.0 is pure Go: `go list -deps` shows no
  cgo files in it or in `richardlehane/mscfb`, `msoleps`, `xuri/efp`,
  `xuri/nfp`, `tiendc/go-deepcopy`, and its only "net" import is `net/url`
  (string parsing) — no `net/http`, no dialing, so nothing about ADR-0003's
  offline-first guarantee (a full sale completes with no network; assets
  vendored, no CDN) is touched: it is a build-time library compiled into the
  binary, not a runtime fetch. ADR-0007 reserves an ADR for
  "significant/architectural" choices; a file-format parsing library in the
  same category as the already-present `goldmark`/`go-qrcode`/`wazero` is not
  one, and it neither contradicts nor extends any accepted ADR (the plugin
  runtime, taxonomy, trust chain and HTMX decisions are all untouched). No
  superseding or new ADR required.
- **Full gate reproduced, not taken on trust**: `gofmt -l .` (no output),
  `go vet ./...`, `go build ./...`, `go test ./...` (whole repo, all green),
  `golangci-lint run ./...` (**0 issues**), `guard-data-access.sh`,
  `guard-i18n.sh` (1502 template keys resolve, all locales match en.json),
  `guard-help-topics.sh`, `guard-compliance-claims.sh` (287 files) and
  `guard-page-http-error.sh` — all pass after the fixes.
  `guard-deadcode-baseline.sh` cannot run in this container (it fails building
  `webview_go`/`cmd/unitill-desktop` for want of GTK/WebKit pkg-config headers,
  identically on `main` — an environment limitation, not a finding).

## Non-goals confirmed untouched

- `catimport.Parse` (CSV) and `ParseBkp` — not modified; the xlsx loop is a
  deliberate copy, and their tests are untouched and green.
- The `.bkp` routing, checksum and image-extraction behaviour — verified still
  intact by the new `TestImport_BkpStillRoutesToBkpParserAfterXLSXSniff` plus
  the existing `.bkp` currency-gate test.
- Offline-first: nothing on this path touches the network, and checkout is
  unaffected. The new unzip bound strictly reduces the chance of an
  import-triggered OOM taking the till down mid-trade.
- Money handling: prices continue to flow as integer minor units through
  `ParsePrice`; no float money is introduced (the raw pass is a *string*
  passed to the same `ParsePrice`).
- The repository pattern: `catimport` still carries no SQL.

## Deferred / out of scope

- **Merged-cell scope** — reject the file only when a merge intersects the
  header row or the data rectangle, instead of anywhere on the sheet. Worth a
  card: a merged title banner or a merged footer note is common in real German
  exports and today rejects a workbook whose data is perfectly readable.
- **A workbook excelize cannot open falls through to the `.bkp` message.** A
  password-protected/encrypted or corrupt `.xlsx` makes `LooksLikeXLSXZip`
  return false, so it is routed to `ParseBkp` and the operator is told "This
  doesn't look like a recognised backup file" — accurate for the parser that
  ran, confusing for someone who uploaded a workbook. Needs its own reason
  code plus four locale strings, so out of scope for a review fix.
- **Language-pack drift, and it points straight at this card's merchant.**
  `en.json` gained `import.xlsx_sheet_used`,
  `import.error.xlsx_legacy_unsupported` and `import.error.xlsx_merged_cells`
  and reworded `import.file`/`import.help`. Until
  `ut-plugin-language-{de,es}` are updated, a till running the **German**
  pack — the pilot merchant's own configuration — falls back to English for
  exactly the messages this feature adds. `lang-pack-drift` is advisory on the
  PR and blocking on push to `main`, so this must be picked up as its own
  follow-up rather than left to CI.
- **ar/fa/tr wording quality.** The three new keys and the two reworded ones
  were translated by the developer, not a native speaker (declared). Key
  coverage, `%s` placeholder presence and `guard-i18n.sh` all check out;
  linguistic quality is unverified and should go to the translation review
  queue. Nothing about the strings is RTL-hostile (no directional CSS, no
  embedded markup).
- **Dead branch in a test helper.** `buildXLSXSheet`'s `if sheet != "Sheet1"`
  path in `internal/catimport/xlsx_test.go` is never taken (the only caller
  passes "Sheet1") and would not do what its name suggests anyway, since
  `GetSheetList()[0]` stays "Sheet1" when a sheet is merely added. Cosmetic;
  left for the author to remove or use.
- **Binary-size impact of excelize** on the Pi image and the Android `.aar`
  was not measured here.

## Verdict

**Safe to merge**, with the four fixes made during this review included. The
feature does what the card asked, the sniff genuinely cannot confuse a `.bkp`
with a workbook in either direction, and the re-parse path is sound. Merging
the diff **as originally submitted** would not have been safe: it could
silently import a €1.40 item at €1.00 from a normally-formatted spreadsheet,
and it exposed an unbounded decompression path on a till whose main job is
taking money.
