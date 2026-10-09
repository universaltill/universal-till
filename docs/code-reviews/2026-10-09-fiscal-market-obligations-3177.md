# Review — fiscal market-obligations table (ut-docs#3177, ADR-0129 slice 3)

## What shipped
- New `internal/fiscal/markets.go` adds `MarketObligations{HardGate,
  PerSaleDeviceReceipt, TaxRateSwitch}` and one `marketObligations` map.
  Each country line carries a same-line
  `// core-neutral:allow ADR-0050 D2 legal floor` marker.
- `RequiresHardGate` and `RequiresPerSaleDeviceReceipt` read the table.
  Their signatures and their case-sensitive matching are unchanged.
- New `fiscal.RequiresTaxRateSwitch` replaces the pages-package
  `taxRateSwitchMandatedCountries` var and keeps its `strings.ToUpper`
  matching. `missingTaxRateSwitcher` keeps its signature.
- The four `#2879` lines are removed from
  `scripts/ci/core-neutral-allowlist.txt`.
- `markets_test.go` adds table tests for the two lookups that had no
  direct test.

## Review
- **Author:** Sonnet subagent. **Reviewer:** Opus 5.5, fresh context in an
  isolated worktree.
- **No blockers or majors.** The reviewer checked behaviour equivalence for
  every caller and every casing.
- **Minor 1.** ADR-0068's 2026-10-08 note said the var still exists. Fixed
  with a dated note in the ut-docs PR, which also updates the
  uk/portugal-compliance references.
- **Minor 2.** The markets.go header claimed to be the ONLY place countries
  are listed. Reworded to name the remaining marked sites
  (AllowedTaxRateSetBP, ServiceChargeForbidden, menu tiles). Fixed.
- **Minor 3.** `AllowedTaxRateSetBP`'s marker reason pointed at the removed
  `#2879` entries. Now says "same shape as markets.go's table". Fixed.
- **Minor 4.** Legal reasoning was dropped from the old doc comments.
  Restored in markets.go: the TR fail-closed outcome
  (`BlockedNeverConfigured`), the turkey-compliance §1 citation, why DE is
  gated while tax-de is a skeleton, no new settings key per market,
  "HardGate alone doesn't prove a TR sale compliant", and why the blocking
  gates are case-sensitive. Fixed.
- **Nit.** No whole-table pin test. Accepted, because `TestRequiresHardGate`
  plus the new tests cover every column.

## Verified
- Existing `TestRequiresHardGate` and the tax-rate banner tests are
  unmodified and pass.
- TDD: the new tests failed to compile (`undefined: RequiresTaxRateSwitch`)
  before the implementation.
- Reviewer mutations: DE `PerSaleDeviceReceipt: true` made the new test
  fail, and dropping `ToUpper` made it fail on `"de"`.
- `guard-core-neutral.sh` passes. The reviewer confirmed it fails when a
  map line loses its marker, and when the marker has no reason.
- `go build ./...` and `go vet ./...` are clean, and so is every CI
  `build`-job guard except `guard-shellcheck-version`. `shellcheck` is not
  installed in this container, and no shell script changed.
- `guard-docs-shots` flagged the `internal/pages` edit. The edit is a Go
  lookup swap with identical output and no rendered pixel, so the surface
  hash was refreshed with `update-docs-shots-surface-hash.sh` in its own
  `Docs-Shots-Unchanged: true` commit. That guard and both of its
  self-tests then passed.
- `go test ./... -count=1` passed for every package except
  `internal/plugins`, which hit the default 10-minute timeout in this slow
  container. This change doesn't touch it, and CI runs it.
- Backend only. No UI or help surface changed, so no screenshots.

## Verdict
Safe to merge.
