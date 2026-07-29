# Test coverage batch 5 (final for pos_repo.go): found a live "today's sales missing from reports" bug

2026-07-29

Fifth and final batch of the `pos_repo.go` backfill: `TopItems`,
`PeriodComparison`, `PaymentBreakdown` — the last three functions in the
file with zero coverage anywhere in the codebase (confirmed via coverage
combined across `internal/data`, `internal/pos`, and `internal/pages`).
After this batch, `pos_repo.go` — originally ~150 untested functions, the
single biggest gap in the codebase — has no remaining 0%-coverage
functions.

## Bug found: `PeriodComparison` silently dropped today's sales

Same bug class as batch 3's `lookupPriceHistory` fix, found the same
way — writing a real test against real intended behavior instead of just
what the code currently does.

`PeriodComparison` computes "current period" vs "same period a year ago"
totals for the sales-comparison report. Its query:

```sql
created_at >= datetime('now', ?) AND created_at < datetime('now', ?)
```

`sales.created_at` is stored as RFC3339 (`...THH:MM:SSZ`, confirmed via
`internal/pos/sales.go`'s `time.Now().UTC().Format(time.RFC3339)`).
SQLite's `datetime('now', ...)` produces its own format
(`YYYY-MM-DD HH:MM:SS`, space, no suffix). On the same calendar day these
compare as raw strings, and `'T'` (0x54) sorts lexically after `' '`
(0x20) — so `created_at < datetime('now', '+0 days')` was **false for
essentially any sale from earlier today**, regardless of actual time of
day. Verified directly via the sqlite3 CLI:
`SELECT '2026-07-29T01:00:00Z' < datetime('now','+0 days');` → `0`, even
when "now" is hours later the same day.

**Real-world impact**: any sales-comparison dashboard/report run during
the day would undercount today's sales in the "current period" bucket —
today is typically the most-recently-generated and often highest-signal
data in the comparison, silently missing.

**Fix**: wrap both sides in `datetime(...)`.

**Caught by**: a new case in `TestPeriodComparison` — a sale timestamped 1
minute before "now" (same calendar day) alongside a sale 2 days ago; both
must land in the current-period count. Confirmed the test fails against
the pre-fix query (`count=1` instead of `2`) and passes with the fix.

## Independent review (opus)

Independently reproduced the bug via the sqlite3 CLI (two ways: raw
comparison and an end-to-end mini-table matching the real query shape),
confirmed the fix introduces no regression (NULL handling unaffected —
`created_at` is `NOT NULL`; the year-ago window benefits equally; the only
cost is losing an index on `created_at` for this query, negligible at
single-shop POS scale). Also did the important scoping check: searched all
~15 other `datetime('now', ...)` comparisons in the file to confirm this
was the ONLY one producing genuinely wrong results — all the others are
`>=`-only "since N days ago" filters, where the same format mismatch is
merely *permissive* for same-day rows (harmless over-inclusion, arguably
even more intuitive), not exclusionary. Flagged one more `<` occurrence
(`SeasonalUpcoming`, a low-stakes year-ago restocking suggestion feature)
with the same pattern but negligible practical impact — fixed for
mechanical consistency since it was a one-line, zero-risk change.

## Verification

`go build ./...`, `go test ./...`, `go test ./internal/data/... -count=3
-shuffle=on`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.

## Coverage delta / pos_repo.go summary across all 5 batches

`internal/data/pos_repo.go` went from ~150 functions with zero coverage
anywhere in the codebase to zero remaining, across 5 risk-prioritized
sub-batches (catalog-adjacent lifecycle, shift/refund/EOD/audit, checkout
resolution chain, shift-close cash reconciliation, reporting). Found and
fixed 3 real production bugs along the way: the `ends_at` same-day
price-history expiry bug, the dropped variant display name at checkout,
and this `PeriodComparison` same-day exclusion bug.
