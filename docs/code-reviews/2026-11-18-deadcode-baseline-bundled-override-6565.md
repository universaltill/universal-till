# Review — deadcode baseline: bundled.OverrideAvailableForTest (ut-docs#6565)

Baseline-only PR required by `guard-deadcode-baseline-growth.sh` (ut-docs#3406):
PR universal-till#2587 (bundled Stripe/SumUp on the App Store build) adds
`bundled.OverrideAvailableForTest`, a test seam that other packages' tests call
(`internal/pages`, `internal/plugins`). It has no non-test caller by design, so
the whole-program deadcode pass reports it, like `buildinfo.OverrideChannelForTest`
(already baselined).

## Exposure
None at runtime: the function only swaps an in-memory flag. `scripts/ci/guard-appstore-no-code-fetch.sh`
fails a caller outside a `_test.go` file, so it cannot gain a production path.

## Verdict
One-line addition to `scripts/ci/deadcode-baseline.txt`, nothing else. Reviewed in
the combined store-batch-b review record of 2026-11-18 (see
`2026-11-18-store-batch-b-review.md` once PR #2587 merges).
