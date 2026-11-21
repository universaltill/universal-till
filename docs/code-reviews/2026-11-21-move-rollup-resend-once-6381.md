# Test: after a till move, each rollup day is re-sent once to the new shop (ut-docs#6381)

Branch `test/6381-move-rollup-resend-once`. Owner decision (delegated, 2026-11-18,
entry 26): option (a). Keep today's behaviour and add a pinning regression test only.
No production code changed.

## What shipped
`TestPushSalesAggregates_AfterMoveEachDayResentOnceToNewShop` in
`internal/cloudsync/sales_aggregates_test.go`:
- Seeds sales today, 1 day back, 3 days back (inside the 14-day window) and 20 days back (outside it).
- Two rounds under the old shop: each window day is posted once, then nothing.
- Then the move: `ResetSalesAggregateUploads` runs and the config switches to the new store id and token.
- Three rounds under the new shop: each window day is posted exactly once with the new
  `store_id` and `Bearer` token, the out-of-window day never, then nothing in either later round.

## Review (independent, Opus; author Sonnet)
Approve. Two nits, both fixed:
1. The doc comment credited the ledger reset with the re-send. The re-send actually comes
   from the store-salted fingerprint plus `store_id` in the payload hash. The comment now
   says so, and points at where the reset is pinned: `enroll/move_5127_test.go` (hook called
   once) and `data/move_history_5127_test.go` (rows deleted).
2. The token check used `strings.Contains`. It now checks equality with `"Bearer "+tok`.

The reviewer found no flakiness:
- A run that crosses midnight still passes. The hash gate stops the post, and the 3-days-back day stays in the window.
- It passes under `TZ=Pacific/Kiritimati` (UTC+14) and `TZ=Pacific/Pago_Pago` (UTC-11).

## Mutation checks
The author and the reviewer each ran their own mutations and restored the code afterwards:
- `RecordSalesAggregateUpload` as a no-op: FAIL at round 2 (the old shop re-posts).
- Every day posted twice: FAIL at round 1.
- Skip gate loosened plus a no-op reset: FAIL at round 3 (nothing re-sent to the new shop).
- A no-op reset alone: PASS. This is expected, because the store-keyed gate re-sends anyway. The reset is covered by the tests named above.

## Verification
- `gofmt` and `go vet` are clean.
- `go test ./internal/cloudsync/ -run SalesAggregate -count=3 -race` passes.
- `go test ./internal/cloudsync/ ./internal/enroll/ ./internal/data/` passes.
- `guard-fixed-dates.sh` passes.

Pre-flight: test-only change. Help topic: n/a (no user-visible behaviour change). i18n, ADR, README, migration: n/a.
