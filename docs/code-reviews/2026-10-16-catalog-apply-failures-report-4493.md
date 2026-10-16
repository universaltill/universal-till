# Code review: the main till reports refused catalogue rows to the cloud (ut-docs#4493)

**Date:** 2026-10-16 · **Lane:** lane:cloud-54 · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent, one round)

## What shipped
- `data.CloudLinkRepo.ListFailures`: the till's `cloud_apply_failures` rows, newest first. No migration.
- `catalogpull.ReportFailures` + `NewHTTPReporter`. After a pull the feed served (not stopped, no error), the main till posts its whole current set of refused rows (at most 500, about 252 KiB) to `POST <endpoint>/v1/stores/catalog/apply-failures` (contract `cloud-catalog-feed.md` 1.1.0). It posts only when the set changed since the last accepted report. The fingerprint is `v2:` + sha256(store scope, set), kept in per-till `cloudsync.catalog.failures_reported`, which is never LAN-synced. An empty set after a non-empty one posts `[]`. A 5xx or transport error keeps the fingerprint and retries on the next tick. A 400 records the set as rejected (`cloudsync.catalog.failures_rejected`) and waits until the set changes. 401/403/409 stop quietly. The token is never put on an error or a log line.
- Entries are cleaned so they pass the cloud's validator exactly. Ids must be canonical UUIDs (never the nil UUID) and are lower-cased and de-duplicated. Reasons become valid UTF-8 (U+FFFD turns into `?`), lose control and Cf characters, and are capped at 500 runes. An entry whose reason ends up empty, or whose `at` is invalid, is dropped and logged locally.
- `cloudsync`: the report runs inside the main till's check-in tick and never fails the check-in. Nothing on the sale path.

## Findings (Fable)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The till's sanitiser and the cloud's validator differed (U+FFFD, Cf characters, the nil UUID). One bad entry would have refused every report with 400 forever. | **Fixed.** The test checks every sent reason against a copy of the cloud's cleaner. |
| 2 | major | The fingerprint had no store scope, so a till re-paired to another store never posted. | **Fixed:** scope is store id + endpoint, and the `v2:` prefix makes every till re-post once. Test added. |
| 3 | major | `TestTickDoesNotReportWhenThePullWasNotServed` was a false pass: the table was empty. | **Fixed:** it now seeds a failure. With the served-pull gate removed the test fails (`reports = 1`); with the gate restored it passes. |
| 4 | minor | Comments were misleading (trimmed entries; non-UUID ids). | Reworded. The dropped count is logged. |
| 5 | minor | A permanent 400 was retried on every tick. | **Fixed:** the rejected set waits until it changes. |
| 6 | minor | No tick-level test for a 5xx. | **Added:** `TestTickReportServerErrorKeepsFingerprint`. |
| 7 | nit | Undocumented 200-body discard; 409 is forward-compat only. | Comments added. |

## Verified beyond automated tests
- The false-pass fix was checked by removing the gate (the test fails) and restoring it.
- Gate: `gofmt -l .` empty, `go build ./...`, `go vet`, `go test -race` on catalogpull and data, `go test ./internal/cloudsync/`, `golangci-lint` 0 issues, guard-data-access. The full `go test ./...` ran in Dev; `internal/plugins` timed out under load there and passed when run alone (not touched by this change). CI runs the rest.
- No UI surface, so no screenshots. The my. screen is ut-docs#4466.

## Verdict
Safe to merge after universaltill/ut-docs#4555 (contract) and universaltill/ut-cloud#502 (endpoint). Before the cloud endpoint is deployed, a till's post gets a 404 → logged and retried on change, which is harmless.
