# Trading-date grouping of `eod_closes` in the plugin export (ut-docs#6313, slice S1)

Branch `fix/6313-trading-date-export`. Design: ADR-0066 amendment 2026-11-21 (ut-docs).

## Change
`POSRepo.EODClosesForExport` selects archived `eod` closes by trading date (local date of
`created_at` minus `reports.business_day_start`; legacy bare-date periods unshifted) instead of
calendar day of closing. `EODCloseExport` gains `closed_at` (RFC3339, local offset) and
`trading_date` (`YYYY-MM-DD`), not omitempty. A settings read error fails the export (500).
`ArchivedReportsInRange` and the Settings archive export are untouched. No migration.
Help topic `reports.md` gets one paragraph (English only).

## Tests
`internal/data/eod_close_export_test.go` (default boundary, shift, legacy, read error, TZ
re-exec under Asia/Tokyo and Europe/London incl. spring-forward), `internal/pages`
(`export_contract_test.go` pin, `export_dispatch_test.go`, `eod_api_test.go` archive-export
bytes unchanged under 04:00). Existing dispatch tests seeded a bare-date period; the helper now
seeds a new-style period (a bare date is a legacy close and is not tied to `created_at`).

## Release note
Shops with a later-than-midnight business day start: after-midnight Zs count for the previous
day in accounting/VAT exports (tax-uk VAT figures and MTD, tax-de DATEV).
