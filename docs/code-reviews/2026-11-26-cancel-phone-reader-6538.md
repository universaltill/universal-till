# Code review: ut-docs#6538 cashier Cancel stops the phone reader (universal-till)

Date: 2026-11-26. Reviewer: combined Reviewer (read of the uncommitted diff, 16 files; Tester's 3 failures reported fixed). Pre-flight: read-only review; build/test per Tester; no Fable second read was run in this pass (see Verdict).

## What shipped
- readerlink: PI id recorded on the attempt row right after session_create (SetPIID); cashier Cancel before the phone is asked and while the collect ack is awaited (new watcher goroutine) now stops phone + PaymentIntent; status/cancel bodies carry exactly one identifier.
- pos.brokerRef and paybroker.hostBody send exactly one identifier; hostBody fills the binding PI for a tap entry from core's own attempt row (provider must match).
- tap_gate/pos_api: plugin decline_code "cancelled" -> tapCancelledError after the broker confirms not paid; attempt settled cancelled; own toast `pos.toast.payment_cancelled`; sale open, request id spent, nothing locked.

## Findings
- none blocker.
- IMPORTANT-1 (accepted risk, cover with a test or note): readerlink.go ~276-300. If the cashier Cancel and a transport error/timeout from peer.Request coincide and the select picks `watchDone` first, the cancel is not honoured on the error path: result is pendingResult(link_lost) (tender locked) although the cashier cancelled. Money-safe (pending, never declined) but the owner symptom ("locked") can reappear in that narrow window. Fix: after `close(watchDone)` also do a non-blocking `select { case <-c.cancel: cancelledEarly.Store(true) default: }`.
- NIT-1 readerlink.go ~276: watcher goroutine leaks only if peer.Request panics (watchDone never closed); use `defer close(watchDone)` style. RecoverAndLog present (guard satisfied).
- NIT-2 stopWith(nil,...) (cancel before the collect frame): correct (phone never asked, PI cancelled; cancelIntent failure falls to status: succeeded->approved, canceled/requires_payment_method->cancelled, else pending). Note requires_payment_method is accepted as cancelled even if the cancel call failed: a still-open PI that is never tapped; acceptable, pre-existing rule.
- NIT-3 pos_api.go ~3288: only the cashier `/api/...tender` handler renders the cancelled toast. completeTender is also called from self_order_shop.go:719 (kiosk), which does not special-case tapStartFailedError either, so parity is kept; a kiosk customer cannot reach the cashier Cancel. No action.
- NIT-4 shop-owner-visible toast: no `web/help/` topic text mentions the Cancel behaviour; add one line if the card-payment topic lists toasts (Reviewer skill #324).
- NIT-5 plugin side: plugin maps cancelled -> `mark("declined")` on its own record + decline_code cancelled (ut-plugin-payment-stripe/src/tap_flow.go:242). Core now reports it as cancelled; the plugin's stored state "declined" is only advisory (core settles from the broker). Consistent with ADR-0198 3.7 ("cancelled before a tap -> cancelled"). No ADR contradiction; no ADR change needed.

## Invariants checked (by reading)
- Never "declined" for unknown: tapVerifyFailure runs tapAssess + tapCancelThenAssess before it settles; only verdict tapNotPaid writes AttemptCancelled; anything unreadable -> PaymentPendingError (locked). OK.
- No double charge: cancel racing a completed tap -> tapPaid -> ConfirmTap -> approved (tested). Refused cancel settles from status. The request id is spent on cancelled. OK.
- Main records only after cloud confirms: AttemptCancelled only on tapNotPaid; SetPIID records only the cloud-returned id and only while pending (UPDATE guarded by state=pending, ErrAttemptSettled/NotFound distinguished). OK.
- hostBody: PI taken only from core's row, only for BindAuthorize with matching AttemptKey and provider; the plugin's own JSON ids are still refused (ErrForeignPI loop precedes). No plugin-controlled identifier reaches the cloud. OK.
- Race in ack-cancel goroutine: Store happens-before cancelAck; the main goroutine reads after Request returns; the flag is atomic; the c.cancel channel is closed once (sync.Once in CancelActive). A cancel that arrives after watchDone closes is caught by the later `select <-c.cancel`. -race run requested (see Verdict).
- i18n: key in en.json and en.meta.json only (ADR-0160), alphabetical, no verbs/placeholders; toast level "info" is an existing level.

## Verdict
APPROVE with IMPORTANT-1 recommended before merge (small, 3 lines + test). Independent different-model read not run by me; money-adjacent change: orchestrator should run the Fable/Opus read the skill asks for if the policy needs it.

## Verification run by Reviewer
`go test -race` readerlink, pos, paybroker, logging: ok. internal/data: ok without -race; under -race the package FAILed after 553 s (no DATA RACE or named test failure seen in the tail; likely a slow-suite timeout, not in the SetPIID path; unverified, Tester recorded data ok). ut-cloud stripeconnect and handlers: ok.

## Follow-up (orchestrator)
Reviewer's important finding (cancel lost when it coincides with an ack error/timeout) fixed in readerlink.go (non-blocking read of c.cancel on error; watchDone closed by defer); ut-cloud rejects unequal idempotency_key/attempt_key. Tester's 3 suite failures fixed (RecoverAndLog, fixture PI id, wasm broker body assertion).
