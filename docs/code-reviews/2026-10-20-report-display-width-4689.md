# Code review — Z-report / cloud report rows measured in display columns (ut-docs#4689)

**Date:** 2026-10-20 · **Branch:** `fix/4689-report-display-width` · **Lane:** `lane:cloud-41`
**Author model:** Sonnet (complexity:easy) · **Reviewer model:** Opus 5.5, fresh context

## What shipped

ut-docs#4685 made `internal/print` pad and clip receipt rows by display width
(a ZWNJ/ZWJ or combining mark takes no column). Report text built in
`internal/pages` still counted runes, so a fa Z-report row with a ZWNJ label
(`باقی‌مانده`) ended one column short of the 42-column rows.

- `internal/print`: exported `DisplayWidth` and `Clip` (wrappers over the
  existing helpers — one copy of the logic).
- `internal/pages/eod_api.go`: `footerRow`, `eodRow` and `rateTableRows`
  measure, pad and clip by display columns (new `padStart`/`padEnd`, fmt's
  `%*s` semantics but in columns); stale "clips at Width RUNES" comment fixed;
  the two tips rows go through `eodRow` instead of a raw `"%-20s %s"`.
- `internal/pages/cloudsync_print_report.go`: `printReportCols` pads with
  `padStart`.
- `eod_print_labels_4397_test.go`: its fit check measured runes; switched to
  `print.DisplayWidth` (same expectations — fa/ar rows are now exactly 42
  columns but 43–44 runes, and the renderer clips by columns).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Tips rows via `eodRow` change en output when a tips label overflows: the amount is kept and the label clipped, where the renderer used to cut the pence off. | Intended fix; pinned by `TestTipsRowOverflowKeepsAmount4689`. |
| 2 | minor | The `zwnj overlong clipped` subtest passed on the old code (its only ZWNJ was trailing). | Fixed: ZWNJ after every char, so `Clip` must skip zero-width runes inside the kept part. |
| 3 | minor | The rate-table test never ran the tight-collapse layout. | Fixed: `TestRateTableRowsTightLayoutDisplayWidth4689` (≈£1bn gross forces `w = tw`; verified the layout collapses to 42 columns). |
| 4 | nit | Zero-width characters typed literally in the test. | Fixed: `‌` / `́` escapes. |
| 5 | nit | A failure message still said "runes". | Fixed. |
| 6 | nit | `printReportCols` duplicated `padStart`. | Fixed. |
| 7 | nit | Pre-existing: a variance `" !!"` flag appended after `eodRow` can push an overflowing row past 42 columns, so the renderer clips the flag. | Out of scope; filed as ut-docs#4690. |

## Verified

- TDD re-verified by the reviewer: with the pre-fix bodies restored, the new
  tests fail (`DisplayWidth(...) = 41, want 42`, `width 25 != 26`, etc.);
  restored, they pass.
- en output byte-for-byte unchanged: the reviewer compared old vs new
  `footerRow`/`eodRow`/`rateTableRows` over 200,000 random label/amount
  pairs and 20,000 random rate tables (no zero-width runes) — all identical;
  the existing en golden lines (4330, 4397) still pass.
- Gate: gofmt, `go vet ./...`, `golangci-lint run ./...` (0 issues),
  `go test ./...`, build-job guards. `guard-deadcode-baseline.sh` could not
  run in the cloud sandbox (its `go run` deadcode build predates go1.27);
  CI runs it. The new exports have non-test callers in `internal/pages`.
- No UI surface: printed report text only, no new strings or locale keys.

## Verdict

Safe to merge.
