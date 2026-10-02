# Review — diagnostics secret scan ignores the event timestamp (ut-docs#3432)

**Date:** 2026-10-02 · **Lane:** lane:cloud-54 · **Author model:** Sonnet (complexity:easy) · **Reviewer:** Opus 5.5, fresh context

## What shipped

Test-only change in `internal/diagnostics/events_test.go`:

- `TestAdversarial_NoSecretReachesAnyEventType` flaked when the `"at"`
  stamp's nanosecond digits spelled the fake PIN `4321`
  (`…56.66432154Z`, CI run 36981650635 on universal-till#1612).
- New helper `leakedSecret`: requires `"at"` to be a string in the exact
  canonical UTC RFC3339Nano form `marshal` writes, drops it, then
  substring-scans the rest of the event for every secret. A missing,
  non-string, unparsable or non-canonical `"at"` is an error, so the
  exclusion can't hide a secret. `assertNoSecret` now delegates to it.
- New deterministic `TestAssertNoSecret_IgnoresTimestampDigits`: an
  unlucky timestamp (guarded to really contain `4321`) is not a leak; the
  PIN in `TaxCodeID` with a timestamp free of those digits is; four
  tampered `"at"` values are errors.

No production code changed.

## TDD re-verified (orchestrator, inline)

Reverted `delete(m, "at")` → the new test fails with
`leakedSecret({"at":"2026-10-02T08:05:56.66432154Z",…}) = "pin"; want no leak`;
restored → passes.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | `time.Parse(RFC3339Nano)` accepts non-UTC offsets; pin to `marshal`'s exact form | **Fixed** — canonical-form check + `+01:00` case added |
| 2 | nit | With several secrets leaked, which one is named is map-order random | Accepted — message only |
| 3 | nit (outside diff) | `checkEventShape` (events.go) doesn't reject a field whose json name is `at`/`type`; `marshal` would silently overwrite it (data loss, not a leak) | Deferred → Backlog card |

Reviewer also confirmed: callers can't influence `"at"` (`marshal` writes it
after the struct fields); decode→re-encode keeps the same escaping so a
secret still matches; no other clock-dependent secret scan remains
(`internal/pages` uses `leakScanJSON`, which already strips `at`; ut-cloud
has no copy).

## Verified

`gofmt -l .` clean, `go build ./...`, `go test ./...` all ok,
`go test -race -count=30 ./internal/diagnostics/` ok (reviewer: `-count=50`),
`golangci-lint run ./internal/diagnostics/...` 0 issues, data-access / i18n /
core-neutral / pipefail / card-data / kiosk-engine / showmodal guards ok.
No UI surface touched — no screenshots, no help-topic change.

**Verdict:** safe to merge.
