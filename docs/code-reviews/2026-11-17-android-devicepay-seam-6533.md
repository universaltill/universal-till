# Review: Android Tap to Pay on the devicepay seam (2026-11-17)

Combined batch review (one independent Fable reviewer + one Tester for the whole Stripe Tap to Pay batch, owner order 2026-11-17). Full record: tester /tmp/stripe-tester.md, reviewer /tmp/stripe-review.md (findings below are the part for this repo).

Cards: ut-docs#6533, #3048

Pre-flight: android/README.md updated; web/help n/a (flag off, no UI, #6457); en.json: Android strings.xml only (guard-android-i18n baseline ok); deadcode baseline ok; ADR-0198 §3 cited; follow-ups deferred (#6455 caller for ResolveUnknown)

## Reviewer findings

A1. HIGH. A repeated key can be answered `declined` while its first tap may have been charged, or was paid.
`TapToPayEngine.collect` (TapToPayEngine.kt:201-213) checks `busy`, `not_foreground` and capability BEFORE the
paid-key lookup (219-223) and the same-key unresolved check (229-232). So:
  - key K is CONFIRMING, Go's deadline passes (Go answers pending), the plugin repeats K: `declined/busy`;
  - K ended `unknown` because the network dropped, the plugin repeats K while still offline: `declined/offline`;
  - K succeeded but the answer was lost, K is repeated while the Activity is stopped or the device is offline:
    `declined/not_foreground|offline` instead of the stored approval.
This breaks the rule the code states itself (#6439: unsure is pending, never declined) and the dev's own
"same key with an unresolved record => pending". Core's confirm (#6455) would catch an approval, but a `declined`
tells the cashier to take the money again. Fix: do steps 1 and "same key live/unresolved" first; same-key busy
must be pending. No test covers these orders.

A2. MEDIUM. The Go deadline and Go's Cancel are ignored while the flow is STARTING.
The deadline timer is armed only after the intent exists (TapToPayEngine.kt:238-242) and `cancel(key)` works only
in COLLECTING (TapToPayFlow.kt:79-83). If connect + session_create outlast Go's deadline, Go answers pending and
gives up, then the tap sheet opens anyway for up to 18 s and a customer can pay a payment nobody is waiting for
(persisted as done, so not lost, but the pay screen has moved on). Arm the deadline at collect start and make
cancel abort a STARTING flow.

A3. MEDIUM. No caller of `devicepay.ResolveUnknown` exists (dev notes say so). Until a manager-gated route calls
it, one unresolved record blocks every later tap on that device for good (`unknown_pending`). Must exist before
the flag is on. Related: the resolve is the operator's word only; asking the broker's `session_status` first
would make a wrong answer impossible for a succeeded PI.

A4. LOW. Unbounded per-process maps: `StripeTerminalReader.intents` keeps every succeeded PaymentIntent object
(removed only on cancel, ttpReal/StripeTerminalReader.kt:39, 217, 250), `TapToPayEngine.liveIntents` keeps every
declined/unknown one. Till binding rule: bounded caches.
A5. LOW. A deadline cancel is reported `cancelled/operator` (engine timer -> `cancel(key)` -> "operator_cancelled").
A6. LOW. `DevicepayBridgeCore.kt`, `DevicepayBridgeCoreTest.kt`, `DevicepayCodecTest.kt` are untracked (`??`), not
intent-to-add: easy to leave out of the commit, and guards that read the index do not see them.
A7. INFO. Not verifiable without a device (as the Dev and Tester state): SDK behaviour, bridge registration
across the Stripe tap Activity (onStop clears the bridge mid-payment; an in-flight Collect keeps its captured
bridge, so it should be fine), gomobile callbacks, prefs across process death. Help topic deferred to #6458 is
acceptable while the flag is off (nothing shop-visible ships).
A8. PROCESS. The 3048 review record still describes the WebMessage bridge; a new record is needed in
`docs/code-reviews/` before the PR.

### ut-docs (#6453 ADR-0198, PR #6537; readiness plan PR #6461)


## Fixes after review (Dev, re-tested)
## Fixes after review (Android section)
- A1 (HIGH) FIXED. TapToPayEngine.collect order is now: paid-key stored approval FIRST (even offline/backgrounded/busy), then
  same key still live => pending outcome_unknown (never busy), then busy/not_foreground/capability refusals which become pending
  outcome_unknown when this key has an unresolved record (gate()). Different keys unchanged (busy/offline/...). Tests (written
  first, 7 failed): sameKeyWhileItsFirstPaymentIsStillLiveIsPendingNotBusy, sameUnresolvedKeyWhileOfflineIsPendingNotOffline,
  sameUnresolvedKeyWhileBackgroundedIsPendingNotNotForeground, aPaidKeyAnswersTheStoredApprovalEvenOffline,
  aPaidKeyAnswersTheStoredApprovalWhileAnotherPaymentIsBusy.
- A2 (MEDIUM) FIXED. Deadline timer is armed at collect start (before f.start), cancelled on any delivery; TapToPayFlow.cancel()
  now aborts a STARTING flow (refuse "intent_canceled", documented start-error token) so Go's Cancel and the deadline stop a slow
  connect; an intent arriving after the abort is already cancelled by the existing guard. Tests: theCallersDeadlineAlsoCoversASlowStart...,
  goCancelAbortsAStartingFlow. Residual (unchanged, pre-existing): an IntentResult.Settled/Unresolved arriving after an abort in
  STARTING is not persisted (same as the hard-deadline path); only reachable when the key already had a server-side PaymentIntent.
- A3 (MEDIUM) DEFERRED, reason. #6455 host fns are not in this repo (nothing outside mobile/ references devicepay), and the only
  identity that can legitimately satisfy devicepay.Actor is the signed-in auth.User the host fn holds. A mobile.* method (Kotlin
  cannot prove who is a manager) or an HTTP route would be an unauthenticated "not charged" assertion = possible double charge, so
  none was added. Added TestResolveUnknownHasNoCallerYetAndNeverAnUnauthenticatedOne (internal/devicepay): fails if any non-test
  source calls devicepay.ResolveUnknown without being allow-listed. Still required before the flag is on: #6455 host fn calling it
  with auth.User, ideally after a broker session_status check. Card needed.
- A4 (LOW) deferred (bounded caches of intents/liveIntents; needs a card, flag off). A5 (LOW) deferred (deadline cancel still
  reports cancelled/operator for a COLLECTING flow). A7/A8 unchanged (device run; new 3048 review record is the Reviewer's).
- A6 FIXED. DevicepayBridgeCore.kt, DevicepayBridgeCoreTest.kt, DevicepayCodecTest.kt are now `git add`ed (staged, not committed).
- Verified: go test ./internal/devicepay ./internal/paybroker ./mobile ok; ./gradlew --offline testArm64DebugUnitTest BUILD SUCCESSFUL
  (all tests); -PtapToPay=true compileArm64DebugKotlin BUILD SUCCESSFUL. Flag default unchanged (OFF). Nothing committed.

## Verdict
Approve with the fixes above. Everything ships dark (flags default OFF, no platform fee, no plan gate). Items marked deferred are filed as Backlog cards or checklist lines on epic #126.
