# Review — dedupe bpToPercent / bpToPercentString (ut-docs#533)

## Summary

`bpToPercent` (`internal/pages/import_page.go`) and `bpToPercentString`
(`internal/data/catalog_repo.go`) were byte-identical implementations of
basis-points-to-percent-string formatting (`1900` → `"19"`, `1950` →
`"19.5"`), found during independent review of ut-docs#512
(universaltill/universal-till#285). Neither package could import the
other's helper directly without a cycle (`internal/pages` imports both
`internal/data` and `internal/catimport`; `internal/catimport` already
imports `internal/data`), and `internal/data` isn't a natural home for a
pure-formatting helper either.

## Changed here

- New package `internal/taxrate` (`taxrate.go` + `taxrate_test.go`),
  mirroring `internal/money`'s file layout. One exported function,
  `FormatPercent(bp int) string` — the merged implementation, zero
  first-party imports so `internal/data`, `internal/pages` and
  `internal/catimport` can all depend on it without a cycle.
- Both call sites (`catalog_repo.go`'s tax-code naming, `import_page.go`'s
  CSV export) updated to `taxrate.FormatPercent`; both old private
  functions deleted. Pure refactor — no behavior change.

## Independent review (Opus, fresh context, isolated worktree)

Ran `go build ./...`, `go vet ./...`, the targeted package tests plus
`internal/catimport`, and every relevant CI guard
(`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-i18n.sh`, `guard-help-topics.sh`) — all green. Independently
verified, not just re-read:

- **Behavior preservation**: hashed all three function bodies (old
  `bpToPercentString`, old `bpToPercent`, new `FormatPercent`) — identical
  MD5, confirming no rounding/sign/trim change. Also wrote and ran a
  throwaway exhaustive round-trip test (`FormatPercent` → `ParseTaxRateBP`
  for every `bp` in `0..10000`, plus a negative-range trim-format check)
  before discarding it.
- **No leftover references**: repo-wide grep for the old names returns
  only a historical mention in a prior review record, not code.
- **Import-cycle claim**: `go list` confirms `internal/taxrate` imports
  only `fmt`/`strings`, and confirms the cycle `internal/data` would hit
  without this package.
- **Existing call-site coverage**: `catalog_taxcode_repo_test.go` and
  `import_page_test.go`'s `TestCatalogExport_RoundTripsTaxColumns` already
  pin the exact formatted strings at both call sites, including the
  fractional/trim path (`1950` → `"19.5"`) — both pass unchanged.
- **Scope**: 4 files touched, no drive-by edits.

Two non-blocking polish findings, both applied before merge:

- The package doc's "would otherwise import-cycle" framing overstated the
  necessity (only `internal/data`→`internal/catimport` would actually
  cycle; `internal/pages` already imports both). Reworded to lead with
  the real reason — neither existing package is a natural home for the
  helper — rather than implying a cycle where none exists.
- Test cases didn't pin the `%02d` zero-padding on a leading-zero
  fractional digit, a sub-1% value, or the `ParseTaxRateBP`-permitted
  upper bound. Added `1905→"19.05"`, `50→"0.5"`, `5→"0.05"`,
  `10000→"100"`.

One finding explicitly deferred, not fixed: `FormatPercent` overflows on
`bp == math.MinInt` (the `-bp` negation). Pre-existing in both original
functions (byte-identical), unreachable via any current caller
(`ParseTaxRateBP` clamps to `0..100`, DB rates are non-negative) — noted
for whoever next adds a caller without that guarantee, not a regression
introduced here.

## Verified beyond automated tests

Full `go build ./... && go vet ./... && go test ./...` gate green (ran
separately by the implementing session before this review), plus the
independent review's own from-scratch verification above.

## Verdict

Safe to merge. No behavior change; new package is a clean, minimal,
well-tested home for the shared helper.
