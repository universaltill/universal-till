# Review: guest_order directive, till side (ut-docs#4337, ADR-0159 task 2)

- **Date:** 2026-10-15
- **Branch:** `feat/4337-guest-order-directive`
- **Author:** pipeline lane `lane:cloud-54` (Opus 5.5 dev subagent)
- **Reviewer:** independent Fable subagent (different model from the author)

## What shipped

The till half of ADR-0159 (table self-order through the cloud):

- Migrations 076–078 (renumbered from 075–077 at merge: main took 075 in #1943): `tables.guest_token` (16 random bytes, lowercase hex,
  unique where set), `kiosk_counter_orders.guest_order_id` (nullable,
  unique where set), and `guest_order_results` (per-till, never synced).
  Checksums pinned.
- `guest_tables: [{token, label}]` in the main till's `/v1/stores/sync`
  device report. Only enabled tables, at most 500, labels clipped to 64
  bytes on a character boundary. Tokens are minted on table create and on
  first need for older tables. A replica never sends the field.
- `cloudsync` `case "guest_order"`, main till only, at most 10 per tick.
  The payload is untrusted and its bounds are re-checked: 1–50 lines,
  integer qty 1–99, at most 20 unique options per line, and a valid
  `guest_order_id`. A refusal posts `failed` with `unknown_table`,
  `item_unavailable` or `bad_order` and writes nothing. An internal error
  leaves the directive pending for the next tick.
- The result post gains an optional `data{display_no,total_minor,currency}`,
  sent only for `guest_order`. Every guest_order result is stored before
  it is posted. A re-delivery re-posts the stored result without applying
  again. An unacknowledged result is retried each tick.
- Hook `internal/pages/guest_order_directive.go`. It uses a detached basket
  from the ADR-0103 session factory (`SelfOrderSessions.NewDetached`),
  never `d.Engine` or `d.KioskEngine`, pinned by a source-scan test and a
  snapshot test. It binds the table and re-validates every item, variant,
  option and group min/max against the till's own catalogue. The till
  prices the order and ignores any price in the payload.
- `placeCounterOrder` is factored out of `completeCounterOrderCheckout`.
  Both the LAN checkout and the directive share it. On the directive path
  the fiscal order starts with `offline=false`, and the kitchen ticket
  prints asynchronously after the park. A guarded `HeldSalesRepo.SwapPayload`
  (compare-and-swap) marks the lines sent only if the parked payload is
  still unchanged, so a cashier's resume is never overwritten.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | `TestSettingScope_EveryUsedKeyIsClassified` red: `cloudsync.GuestTablesKey` read as an unclassified settings key | **Fixed**: exempted as a device-report field, next to `LANAddressKey` |
| 2 | major | The async ticket goroutine had no `logging.RecoverAndLog`, so a panic in printing (triggered by an anonymous guest order) would kill the till. `TestNoUnrecoveredGoroutines` also failed. | **Fixed**: recover is the first defer |
| 3 | minor | Retry treated only 404 as final. A permanent 4xx (such as a 400) was retried for 48 h, and because retries run oldest first and stop at the first failure, it starved every later result. | **Fixed**: any 4xx except 401/408/429 is final. TDD test `TestTick_GuestOrderResultPermanent4xxDoesNotStarveLaterResults` failed before the fix ("d1 posted 3 times") and passes after it. |
| 4 | minor | A crash between the counter-row `Create` and the park leaves a held counter row with no held sale. A re-delivery then answers "applied" for an order staff can't see. | **Mitigated**: logged at Error level with the order number. Full orphan recovery has to tell this case apart from a cashier mid-resume, so it is a follow-up card. |
| 5 | minor | The `apply()` `case "guest_order"` is not reached in production (Tick routes to `handleGuestOrder`). If it were reached, it would post a deferred internal error as `failed`. | Accepted for now, in the follow-up card |
| 6 | nit | `counterOrderPlaced.TicketDone` is read only by tests | Accepted |
| 7 | nit | A nil hook posts "guest_order is not supported on this till", which is not one of the three codes | Accepted: matches every other directive, and the cloud shows "order at the counter" for any failure |
| 8 | nit | qty accepts integer-valued floats ("1.0") | Accepted: it is an integer 1–99 |

## Verified beyond automated tests

- The reviewer compared the LAN counter checkout line by line, old against
  new. The single snapshot read, the offline flag taken from the form, the
  synchronous print before the park, the rollback unless refused, the
  session release and Reset order, the tracking QR and `PayFirst` are all
  unchanged.
- The reviewer ran mutation checks, and each one made its test fail: the
  SwapPayload guard removed, the main-till-only rule dropped, stored-result
  idempotency disabled, hook idempotency disabled, the enabled-table check
  removed, the per-tick cap removed, the replica `guest_tables` delete
  removed, and the gift-voucher/weighed filter removed.
- No file writes and no cwd-relative paths in the diff. The partial unique
  indexes are valid in SQLite. `tables.guest_token` travels in the
  primary-wins admin bundle, so a promoted till keeps the same tokens.

No UI surface was changed (the QR, the QR dialog and the help are task 3,
ut-docs#4338), so nothing was checked visually. The docs-shots surface hash
was refreshed, and the commit carries `Docs-Shots-Unchanged: true`.

## Gate

`gofmt`, `go build`, `go vet`, `golangci-lint` (0 issues), the full
`go test ./...` (internal/plugins with CI's 20m timeout), and the
`build`-job guards.

## Verdict

Safe to merge once findings 1–3 are fixed (done). Deferred: findings 4/5
→ a follow-up Backlog card.
