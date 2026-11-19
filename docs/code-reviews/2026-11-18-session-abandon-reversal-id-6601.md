# Review: table-session tender hand-off and stable reversal id (ut-docs#6601, slice A)

Date: 2026-11-18. Lane: `lane:cloud-41`. Built by Sonnet; Tester Opus 5.5; independent review Fable (money path, high risk).
Card: ut-docs#6601 (slice A). The durable restart half was split to ut-docs#6653. The plugin half is on ut-docs#6577.
Pre-flight: help topic n/a (no shop-visible screen or text changes; Problems lines are log text); en.json n/a (no new keys); reference/ updated (ut-docs `reference/payment-provider-contract.md`, Reversal section); ADR n/a (ADR-0136 D2 already routes reversals through `.refund` with `authorize_request_id`; ADR-0020 is unchanged); follow-ups → ut-docs#6653, ut-docs#6577.

## What shipped
- **Table-QR sessions get the abandon handler.** These are `pos.SessionBasketManager` sessions, one `pos.Service` each. The manager holds the #6602 `AbandonedTenderHandler` (`SetAbandonedTenderHandler`), pushes it to live sessions, and sets it on every new one through `build()`: Create, both BindTable mint paths and NewDetached. `wireAbandonedTenderReversal` wires it.
- **`Sweep` and `Remove` hand off a locked tender.** They hand it to the handler outside every lock through the new `Service.AbandonTender(reason)`, with reasons `AbandonSessionExpired` and `AbandonSessionEnded`. `AbandonTender` drops only the tender state; it is Reset's tender code moved into `dropTenderLocked`, so `handedOff` and the attempt-id clear behave exactly as for Reset. Evicting at the session cap skips a session with a pending tender (`HasPendingTender`).
- **Reversal refunds carry `reversal:<authorize_request_id>` as their event id** (`reversalRequestID`), the same on every re-send. Before, they sent `""` and got a fresh id each time. The refund page ignores a client `refund_attempt_id` that starts with `reversal:`, so the two id spaces cannot collide.

## Verification
- **Dev:**
  - `gofmt`, build, vet and golangci-lint (pos, pages) clean.
  - `go test ./internal/pos ./internal/pages` pass.
  - Kiosk-engine, data-access and fixed-date guards pass.
  - 12 mutations, each killed by its test.
- **Tester:**
  - `go test -race ./internal/pos` pass; targeted `-race` pages run pass.
  - Mutation checks: hand-off under `m.mu` fails the 2 s lock probe; the attempt-id clear, the refund prefix check and the sweep reason are each caught.
  - Deadcode guard passes (local Go 1.27 build of deadcode v0.48.0).
  - Backend only, so no driven UI run.
- **Reviewer:**
  - Re-ran build, vet, `-race` pos and the targeted pages tests.
  - Re-verified 6 TDD claims by mutation.
  - A probe of the #6602 M1 in-flight scenario through `AbandonTender` found no double reversal.

## Findings
- **L1 (fixed): a panic in the hand-off killed the sweep.** It skipped the remaining dropped sessions and ended the sweep goroutine. `abandonDropped` now recovers each hand-off and logs it. Test `TestSessionManager_SweepSurvivesHandlerPanic` was seen failing first.
- **L2 (accepted): `Remove` reverses on the guest's request path.** On the counter-order path (`self_order_shop.go`), the response waits one plugin timeout per captured leg when offline. That is bounded, and the order is already placed. The design note's claim that "Remove always follows a settled sale" is wrong for that caller.
- **L3 (fixed): the contract doc didn't cover re-sends or key limits.** It now gives the id length and the `:` character, says to hash rather than truncate, and says a provider that stores failures replays them on re-send. Core never re-sends a failed reversal today.
- **L4 (deferred → ut-docs#6653): two paths have no committed test.** `AbandonTender` with a tender still in flight was only probed during review, not committed. BindTable's under-lock fallback mint is not exercised.
- **L5 (fixed):** comment and log text now name the table-session case.
- **Pre-existing (→ ut-docs#6653):** a request that resolved a session's Service before `Remove` can still tender on the orphaned Service. The window is small, because checkout is serialised per token.

## Verdict
Approved after the L1/L3/L5 fixes.
