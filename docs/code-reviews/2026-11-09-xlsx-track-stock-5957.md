# Code review — XLSX import reads the Track inventory column (ut-docs#5957)

- Card: universaltill/ut-docs#5957 (`complexity:easy`), follow-up from #2084.
- Branch: `fix/5957-xlsx-track-stock`.
- Pre-flight: card claimed by lane:cloud-41 from the filtered Backlog (Ready held nothing pickable); no open PR or branch for #5957; the defect was reproduced by a failing test before the fix.
- Process: `[no-subagent: one-function fix whose design is spelled out on the card]` for BA/Architect/Dev; Tester + Reviewer ran as one fresh subagent on a different model from the author.

## What changed

- `catimport/catimport.go`: the CSV parser's inline `track_stock` block moved
  verbatim into `applyTrackStock(item, raw)`. It still uses `parseYesNo`, so
  an explicit "No" and an unrecognised value stay distinct (#1843).
- `catimport/xlsx.go`: `ParseXLSXMapped`'s row step now calls
  `applyTrackStock`, next to `applyReorderLevel`. This is the same
  shared-helper shape #2084 used for the reorder level. Before this, a
  workbook's "Track inventory?" answer was silently dropped.
- `catimport/catimport_track_stock_test.go`:
  `TestParseXLSX_TrackInventoryMatchesCSV` compares the same rows parsed as
  CSV and as XLSX: Yes, No, an unrecognised value and blank. It also has two
  boolean-typed cells (Excel TRUE/FALSE).
- `catimport/VERSION` 0.6.0 → 0.6.1 (patch fix, ADR-0164).

## Verification

- Test first: the new test failed on `main` code (4 rows: XLSX
  `HasTracksStock=false` where CSV had `true`), then passed with the fix.
- Mutation check by the reviewer: removing the `applyTrackStock` line from
  `xlsx.go` makes the test fail again; restoring it gives back the identical diff.
- `catimport`: `go vet ./...`, `go test ./...` green; `gofmt -l` empty;
  `guard-catimport-version.sh` passes.
- Root module: `go build ./...` and `go test ./...` (see PR checks for CI).

## Review findings

None.

The reviewer confirmed:
- CSV behaviour is unchanged.
- Blank and unrecognised cells infer nothing.
- Boolean cells come through the formatted pass as "TRUE"/"FALSE", and `parseYesNo` accepts them.
- The mapped-header path works (`track_stock` is a `Mapping` field).
- `smart_products.go` and the Shopify path are unaffected.

Downstream, `rows.go:134` and `internal/pages/import_page.go` now give an
XLSX "No" the same "quantity not carried" handling that CSV already had.

## Docs

`web/help/en/inventory.md` describes the "Track inventory? No" behaviour
without naming a file format, so it is now true for workbooks as well. No
edit is needed.

## Cross-repo follow-up

ut-cloud requires `catimport/v0.6.0`. When CI tags `catimport/v0.6.1` on
merge, ut-cloud's pin bump will start filling `TracksStock` for XLSX uploads.
ut-cloud lists `track_stock` in `overlay.go`'s `noEffectFields`, so nothing
it writes changes. Only `check-catimport-pin.sh` needs the bump.
