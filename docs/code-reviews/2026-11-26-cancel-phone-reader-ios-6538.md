# Code review: ut-docs#6538 (iOS: bounded cancel on the tap coordinator)

Date: 2026-11-26. Pre-flight: read-only; Tester: UniversalTillTapToPayTests 179 pass (9 new). Not run on a device.

## What shipped
TapToPayCoordinator.cancelGraceMs = 5000: requestCancel schedules cancelTimer; on expiry a live attempt finishes `.cancelled(reason)`. finish() invalidates the timer. FakeReader.holdRetrieve for tests. 9 new end-to-end cancel tests (ReaderCancelFlowTests.swift, new untracked file: must be `git add`ed).

## Findings
- none blocker.
- NIT-1 "never confirms after cancel": holds. requestCancel is reached only from phase .collecting (operator cancel, deadlinePassed); the .creating path sets cancelReason without the timer and is decided on read; .confirming is excluded in both callers, so the timer can never finish `cancelled` while a confirm is in flight. Every confirm path is guarded by `a.cancelReason == nil` (retrieved, maybeFinishCollect). `finished` guard makes a late collect/confirm callback a no-op.
- NIT-2 timer closure uses weak self/weak a; Attempt is retained by `current` so the timer fires, and finish() sets current=nil and invalidates. No retain cycle, no double completion (finish is idempotent).
- NIT-3 after the 5 s expiry with an SDK cancel still outstanding, a new attempt could start while Stripe's collect is still cancelling and fail "busy" (reported decline/read_failed, nothing charged). Cosmetic; Go side cancels the PI either way.
- NIT-4 untested on a real device: the real Apple sheet dismissal relies on Stripe cancelCollectPaymentMethod; Dev says so honestly.
- Housekeeping: commit must include ReaderCancelFlowTests.swift (listed in dev-ios-notes.md; `git diff` alone does not show it).

## Verdict
APPROVE. i18n and ADR-0198: no new strings on the phone; reader.cancel/reader.result frames unchanged (ADR-0198 6.5).

## Follow-up (orchestrator)
Reviewer's important finding (cancel lost when it coincides with an ack error/timeout) fixed in readerlink.go (non-blocking read of c.cancel on error; watchDone closed by defer); ut-cloud rejects unequal idempotency_key/attempt_key. Tester's 3 suite failures fixed (RecoverAndLog, fixture PI id, wasm broker body assertion).
