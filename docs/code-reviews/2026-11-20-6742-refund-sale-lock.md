# Per-sale refund lock (ut-docs#6742)

Branch `fix/6742-refund-sale-lock-fa` (the Dev lock branch merged with main, which
now includes the refund-leg-netting PR #2633). One combined Tester + Reviewer pass
(Opus 5.5, not the Dev agent). A Fable read-only reviewer was started too. Its report
did not arrive before this record was written, so this verdict rests on the Opus review
below.

## What shipped
- `internal/pages/refund_lock.go`: `keyedLocks`. Each sale id gets a channel that holds
  one value. The map entry has a reference count and is deleted at zero. A waiter stops
  waiting when its context is cancelled, and `unlock` is idempotent.
- `POST /api/refund` takes `refundSaleLocks.lock(r.Context(), detail.ID)` after the PIN
  check and the fiscal gate, before `loadRefundGuardState`, and releases it with
  `defer unlock()`. A waiter whose context ends gets a 503 `refund.error.server` (an
  existing key) and nothing is recorded.
- Tests: `refund_lock_test.go` covers the lock itself. `refund_concurrency_test.go` has
  seven handler races. The test seam is `refundHookGates` in the fake card plugin.

## Findings
| # | Finding | Severity | Result |
|---|---|---|---|
| 1 | The fiscal gate is decided before the wait. A refund that waited could run under a gate decision that is now stale (for example, the TSE failed while it waited). The window is short, the decision was already a snapshot taken before the provider call, and taking the gate inside the lock would hold the lock during a settings read | low | accepted, no change |
| 2 | `detail` (the sale) is loaded before the lock. Its fields are immutable for a completed sale. Every cap input (`loadRefundGuardState`, `RefundedByLeg`, returned quantities, charges) is read after the lock, so nothing stale feeds a cap | — | verified safe |
| 3 | The lock is held during the provider call, so a second refund of the same sale waits that long. The waiter's request context bounds the wait. Refunds of other sales are not delayed (`OtherSaleNotDelayed`) | — | intended |
| 4 | Lock lifecycle: every return path after the acquire runs `defer unlock()`, and a panic also unwinds through the defer. The cancelled-waiter path drops its reference. If the channel send and `ctx.Done` are both ready, `select` picks one: either the waiter holds the lock and unlocks normally, or it drops its reference. Neither case leaks a lock or a map entry (`LockReleasedAfterDeclineAndBadInput` checks that the map size returns to 0) | — | verified |
| 5 | Deadlock and lock order: only `POST /api/refund` uses `refundSaleLocks`, and nothing in the locked region calls back into it. The fiscal gate takes no lock, so there is no ordering cycle | — | verified |
| 6 | Other entry points: the only place that records a return, `InsertRefundLegRefunds` and `RefundedByLeg` is `POST /api/refund`. Preview and GET compute only. Sync replay (`sync_sales.go`) records a peer till's return without checking caps, because the money already moved (ADR-0011 flag), so a lock would change nothing. Cross-till races stay the ADR-0011 flag | — | exclusion confirmed safe |

No real defect found, so there is no fix commit.

## Verification
- `go build ./...` and `go vet ./internal/pages` are clean, and the new files pass gofmt.
  `guard-data-access.sh` and `guard-i18n.sh` pass.
- `go test ./internal/pages -run 'Refund|Fiscal|KeyedLock' -count=1`: ok (28 s).
- `go test -race ./internal/pages -run 'RefundConcurrency|KeyedLock' -count=3`: ok.
- Mutation (lock acquire replaced by a no-op, in a throwaway worktree that was then
  removed): 6 of the 7 concurrency tests fail. Two of them, split tender and leg cap,
  return 200 + 200, which is an over-refund. The other failures are "second legs = [120],
  want [30 90]", "lock map size = 1", and timeouts waiting for the waiter to park.
  `OtherSaleNotDelayed` stays green by design. The real tree is clean.
- The full `-race` package run is left to CI (it takes more than 25 minutes locally).

Pre-flight: till user-visible behaviour n/a (no UI or help change; the 409 and the error
copy are existing ones); `en.json` n/a (no new keys); `reference/` updated on the ut-docs
branch `docs/6742-refund-sale-lock` (the `architecture/refunds.md` bullet); ADR n/a (no
new decision; ADR-0011 is unchanged); review record: this file.

## Verdict
PASS. Deferred: none.
