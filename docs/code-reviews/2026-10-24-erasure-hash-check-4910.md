# Review: erasure-hash test false positive (ut-docs#4910)

**Date:** 2026-10-24 · **Lane:** lane:cloud-41 · **Card:** ut-docs#4910

## What shipped

This is a test-only change in `internal/customerimport/apply_test.go`. There is no production change.

- **New assertion:** `TestApply_ErasedCustomersAreNeverRecreated` now asserts that each `customer_erasure_hashes.hash` is a 64-char lowercase hex HMAC (`isHexHMAC`).
  - It replaced `strings.Contains(h, "example") || strings.Contains(h, "7700")`.
  - The old check failed whenever the random-salted HMAC contained `7700` by chance, which happens to about 0.1% of hashes.
  - It failed on universal-till#2129 at `ed1586d` with `433077005d02…`.
- **No vacuous pass:** the test now requires at least 3 suppression rows, and checks `Query`, `Scan` and `rs.Err()` errors.
- **Predicate test:** `TestIsHexHMAC` checks the predicate against the chance-`7700` hash from CI and against plain, E.164, national, digits-only and empty values.

## Verified

- **Red first:** the old predicate flags the CI hash, since `'7700' in '433077005d02…'` is True. The new one accepts it, as `TestIsHexHMAC` shows.
- **Tests pass:** `go test -count=5 ./internal/customerimport/` is ok.
- **Lint and vet:** `go vet` and `golangci-lint` report 0 issues.

## Findings

The independent review was done by Sonnet, a different model from the author.

- **Stored values:** every write to the table goes through `data.Hash`, which is hex HMAC-SHA256. The only callers are in `recordCustomerErasure`, for email and phone.
- **Regression still caught:** a plain value stored by mistake is still caught. A folded email has `@`, and the phone key forms are 11–13 characters, so neither can match 64 hex.
- **Same pattern elsewhere:** none. The other `strings.Contains` checks compare human-readable secrets with spaces or capitals, which a hex hash can never contain.
- **Low, fixed:** the row loop could pass vacuously if no row was written or the query failed. It now asserts at least 3 rows and checks errors.
- **Accepted:** an unsalted 64-hex digest would also pass. That is out of scope for this card.

## Verdict

Safe to merge. Deferred: nothing.
