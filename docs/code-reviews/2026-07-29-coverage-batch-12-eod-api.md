# Test coverage batch 12: EOD API handlers

2026-07-29

`internal/pages/eod_api.go` — end-of-day report generation, print, and
schedule settings. `eodDue`/`buildEODDoc` already had coverage in a
pre-existing `eod_test.go`; checked first and only added what wasn't
covered (direct regex edge cases, and the zero-sales-day footer case)
rather than duplicating.

## What's covered

- `generateEOD`'s idempotency: `ArchiveReport`'s `UNIQUE(kind,period)`
  makes a second same-day call a genuine no-op (`created=false`, no
  error) — verified against production (`INSERT ... ON CONFLICT DO
  NOTHING`), not just asserted.
- `POST /api/reports/eod/run`: manager gating, generate-then-
  already-exists on a repeat same-day call.
- `POST /api/reports/eod/print/{period}`: manager gating, 404 for an
  unarchived period, clean 502 (not a hang or panic) when no printer is
  configured — relies on `printerConfig`'s real default (`Mode` defaults
  to `"off"` when unset), no mock/hardware needed.
- `POST /api/settings/eod`: HH:MM validation only enforced when enabled;
  disabling never requires a valid time.
- `seedForPages` fixture: added `report_archive` (verified to match
  `013_report_archive.sql` exactly).

## Independent review (opus)

Confirmed the de-duplication assessment against the pre-existing
`eod_test.go`, traced the idempotency path through `ArchiveReport`'s real
SQL, confirmed the no-printer 502 test exercises the intended code path
(not the not-found path), and confirmed the schema addition matches the
migration exactly. No findings.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.
