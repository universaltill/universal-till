# Review: guest_order orphan recovery and the dead apply() case (ut-docs#4392)

Date: 2026-10-15 · Lane: cloud-54 · Author: Opus 5.5 · Reviewer: Fable 5.1 (independent, separate worktree)

## What shipped

**Gap 1: orphaned counter row.** If the till dies between
`counterRepo.Create` and the held-sale park in `placeCounterOrder`, a "held"
`kiosk_counter_orders` row carrying the `guest_order_id` is left with no
`held_sales` row. The same happens when a park fails and its `DeleteHeld`
cleanup fails too. Until now, a re-delivered `guest_order` was answered
`applied` (total 0) for an order staff could not see.

- `internal/data/held_sales_repo.go` adds `HeldSalesRepo.HasTombstone`. It
  reads the existing ADR-0093 Amendment B tombstones, ignoring any older than
  the TTL. It also adds `HeldSaleTombstoneWindow` (24h), the Go form of
  `heldSaleTombstoneTTL`, and a test pins that the two agree.
- `internal/pages/guest_order_directive.go` (`guestOrderAlreadyPlaced`)
  decides as follows for a "held" counter row with no held sale:
  - **Tombstone present:** the order was resumed, paid or cancelled. It is
    answered as placed, as before, and never duplicated.
  - **No tombstone, row created by this process less than 30s ago:**
    another apply may still be mid-park. The directive is deferred
    (`errGuestOrderInFlight`, so it stays pending).
  - **No tombstone, row younger than 24h:** the order was never parked.
    The orphan row is removed (`DeleteHeld`) and the order is placed again,
    exactly once. `fiscal.order.start` fires again for the new id, the same
    as the existing failed-park retry.
  - **No tombstone, row 24h or older:** it can no longer be told apart from
    an order dealt with long ago. It is refused with `item_unavailable`
    (the guest is asked to order at the counter). It is never re-placed and
    never reported as parked.
- Why "no tombstone" proves "never parked": `guest_order` is main-till only.
  On the main till, every removal of a held sale goes through
  `ClaimAndTombstone` (resume and cancel via `heldSaleClaimForResume`) or
  `DeleteAndTombstone` (a replica's `/delete`). A resume can only follow
  the row's creation, so its tombstone outlives any row younger than 24h.

**Gap 2: dead code.** `cloudsync.apply()`'s `case "guest_order"` is removed.
Tick routes guest_order through `handleGuestOrder`, and that case would have
skipped the stored-result idempotency. `apply` now falls through to "unknown
directive type". `TestApply_NeverRunsTheGuestOrderHook` pins that the hook
is never run there, and the nil-hook contract test now calls
`applyGuestOrder`.

## Tests (TDD)

- `TestCloudGuestOrder_RedeliveryReplacesAnOrphanExactlyOnce`: the order is
  parked once, visible, and has the same total. A further re-delivery
  answers the same order.
- `TestCloudGuestOrder_RedeliveryDuringAResumeIsNotDuplicated`: the claim
  goes through `heldSaleClaimForResume`. This is a regression guard: it
  passes on old and new code.
- `TestCloudGuestOrder_RedeliveryOverAnOldOrphanIsRefused`
- `TestCloudGuestOrder_RedeliveryOverAnInFlightOrderIsDeferred`
- `TestCloudGuestOrder_RedeliveryAfterARestartReplacesAYoungOrphan`
- `TestHeldSalesRepo_HasTombstone`, `TestHeldSaleTombstoneWindowMatchesTTL`

Red/green was checked twice, once by the author and once by the reviewer in
its own worktree. With the old `guest_order_directive.go`, the orphan,
old-orphan and in-flight tests fail. The resume guard passes either way.

## Findings (independent review) and outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The 2-minute in-flight grace could push a restarted till's re-placement past the cloud's 10-minute delivery window (ut-cloud `claims.GuestOrderExpiry`). Tick is a single goroutine, so nothing from a previous process can be in flight. | **Fixed.** A row created before `guestOrderProcessStart` is never deferred. The grace for rows from this process is now 30s. The constant's comment documents the coupling, and a new restart test covers it. |
| 2 | minor | No doc update. | ADR-0159 §5's rule (idempotent on `guest_order_id`, never applied twice) still holds: an orphan was never placed. This crash-recovery detail lives in the code comment and this record. No reference doc describes the path. |
| 3 | minor | In practice the 24h-refuse branch is reachable only through clock skew or an unparsable `created_at`, and `item_unavailable` is an arbitrary code. | Accepted. A comment says the code is arbitrary: all three codes read "order at the counter". |
| 4 | minor | The admin data reset (`reset_archive_repo.go`) archives `held_sales` without a tombstone and keeps `kiosk_counter_orders`. A re-delivery within 10 minutes of a reset would re-place the order. | Accepted. It needs a reset within the 10-minute window plus a lost result. Re-placing an order the reset wiped, rather than one that was served, is defensible. |
| 5 | nit | The in-flight test accepted any error. | **Fixed.** It now asserts `errors.Is(err, errGuestOrderInFlight)`. It also pins the process start, because `created_at` has whole-second precision. |
| 6 | nit | A clock stepped backwards defers the directive until it catches up. | Accepted. The cloud's 10-minute window bounds it and it heals itself. |

Hunt results from the reviewer:
- Every `DELETE FROM held_sales` on the main till leaves a tombstone, except
  the reset archive (#4).
- `ReconcileWithPrimary` runs on replicas only.
- `guest_order` is main-till only: the `mainTillOnlyTypes` list plus
  `requirePrimaryDirective`.
- UTC handling is consistent.
- No double placement and no invisible `applied` in any concurrency branch,
  including the error-path re-check in `cloudGuestOrder`.

## Verified

`gofmt`, `go build ./...`, `go vet`, `golangci-lint run ./...` (0 issues),
`go test ./...`, and every guard in `ci.yml`'s build job. The work is
backend only, with no UI or help surface. Nothing beyond automated tests
applies: there is no device or UI path to drive, and the tests run against a
real migrated database.

## Verdict

Safe to merge.
