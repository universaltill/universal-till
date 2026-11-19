# Code review: Android Tap to Pay, Stripe Terminal, dark behind -PtapToPay (ut-docs#3048)

- Repo: universal-till, branch `feat/3048-android-tap-to-pay`, commits b2b95a28a (feature) and 294385ce0 (fix round) on origin/main; the review adds one more commit. Not pushed.
- Reviewer: Opus-class review pass run by the Sonnet-5.5 session in the worktree `/private/tmp/ut-3048-android` (the code was written by Sonnet; an earlier independent Fable payments review is the input to commit 2). Binding ADR: ADR-0125.
- Date: 2026-11-16
- Pre-flight: platform/capability claim: `android/README.md` updated, `universal-till/README.md` and `ut-website` n/a (flag off, nothing is claimed publicly); `web/help` n/a (nothing user-visible while dark); `en.json`/`en.meta.json` n/a (strings live in Android `strings.xml`, English only; Weblate for the shell strings is deferred to the card that turns the flag on); ADR amended: deferred (ADR-0125 Amendment 1 must be accepted before merge, see open items); review record: this file.

## What shipped
An arm64-only, compile-time flag (`-PtapToPay=true`, default off) that adds the Stripe Terminal Tap to Pay adapter, a process-scoped engine/flow with keyed pending and paid records, an origin-locked WebMessage bridge, and `internal/devicepay` (allowlisted cloud calls, bearer stays in Go). Shipped builds get a no-op stub; CI guards and release.yml built-APK checks keep the SDK out of every shipped flavor.

## Re-verification of the Fable findings (done personally)
Each fix was mutated, the tap suite run (`testArm64DebugUnitTest --tests com.universaltill.pos.tap.*`, 118 tests), the failing tests recorded, and the original restored (`git status` clean after the loop).

| Fix | Mutation | Result |
|---|---|---|
| Paid key answers the stored result | paid branch disabled | 3 fail (repeat-succeeded, rebuilt-engine, different-amount) |
| Paid key with other amount refused | check removed | 1 fails |
| Other key refused while unresolved | check removed | 3 fail |
| Same key, other amount, never collects stale intent | check removed | 1 fails |
| Unresolved record of this key locks it | check removed | 1 fails |
| CANCELLING deadline | deadline timer removed | 1 fails (the test hangs first; the run was killed) |
| Late result touches only its own key | `remove(key)` replaced by remove-all | 1 fails |
| Paid-store fallback on `result` | lookup disabled | 2 fail |

Two first-attempt mutants did not compile (smart-cast) and were redone as `&& System.nanoTime() < 0` forms. The dev-noted gap (persist-before-remove ordering) is not observable with the fakes; I confirmed it by reading `TapToPayFlow.finish` and `TapToPayEngine.sink.result`: the paid store is written inside `sink.result`, the pending record is removed after it returns. Accepted.

Also read and found correct: process scope (`TapToPayProcess`, ui/out cleared on Activity destroy, no destroyed Activity reachable), the BridgeCore origin re-check on every message plus main-frame and exact `http://<host>` rule, `removeWebMessageListener` before re-adding, flag gate (`TapToPayGate`), per-flavor `TAP_TO_PAY_ENABLED` (universal and armv7 hard-coded false), Kotlin 2.3 plugin bump only on a flag-on build, the manifest overlay, `devicepay` (https only, no redirects, allowlist, body caps, error text carries no URL or bearer, 409 maps to `connect_incomplete`), and the flag guard (scans workflows, actions, scripts, every gradle.properties; the release.yml built-APK check is the backstop no spelling bypasses).

## Findings
| # | Severity | Where | Finding | Status |
|---|---|---|---|---|
| R1 | Medium (money lock) | `TapToPayFlow.intentReady` | A same-key retry of an `unknown` payment reused the live intent and overwrote the `unknown` record with `created`. `created` does not lock, so a process death during the retry freed every other basket while the first tap might be charged. | **Fixed.** The unresolved record of the same intent is kept. New test `retryOfAnUnknownKeyKeepsTheUnresolvedRecordWhileItCollects` was red before the fix. |
| R2 | Low | `TapPrefs.pendingWrite` | The comment said unresolved records are never evicted by the 16-record bound; the code was a plain `takeLast`, so 16 orphaned `created` records (process killed mid-collect) could push out the oldest `unknown`/`confirming` one. | **Fixed.** New pure `PendingBound.keep` (resolved first, oldest unresolved only when nothing else is left) with 3 JVM tests. |
| R3 | Low | `TapToPayEngine` `infoOf.clear()` at 64 | The map is cleared while a late result of an older request can still arrive; that late success would then skip the paid-store write. Needs 64+ payments in one process plus a late arrival. | Accepted, open item. |
| R4 | Low | `StripeTerminalReader.ensureConnected` / `FlightGate` | No timeout on the one in-flight connect. If the SDK never ends discovery, the gate stays joined for the life of the process: `start` still ends `offline` at 24 s, but no later connect can ever begin. | Accepted while dark; needs the device run (D1-D6), then a bounded gate. Open item. |
| R5 | Medium (policy) | bridge `resolve_unknown` | The native side cannot know the role. Any script on the till origin can clear an unresolved payment, which is a cashier-reachable money lever (cashier is sale-only, hide AND server-refuse). The page and the plugin must refuse it for cashiers; nothing here can. | Accepted while dark, open item: the page card must gate it server-side. |
| R6 | Info | `apiPrefix` `core-neutral:allow` | A reviewed marker, a documented exception for a ut-cloud path. | Accepted; moves with the devicepay chain card. |
| R7 | Info | amount/currency from the page | ADR-0125 3c says amount, currency and key come from the in-flight event, not the plugin or page. The WebMessage bridge takes them from the page. Safe only because the result is advisory and the plugin verifies server-side; #6454 moves creation to the cloud. Needs Amendment 1 accepted. | Open item (blocking merge, not blocking the code). |

No real shop or client name in tests or seeds, no literal secret (token and bearer are runtime only), no card data field in any result (brand/last4 are always null in the real adapter), no `addJavascriptInterface`.

## Verification (run by reviewer)
- `./gradlew --offline testArm64DebugUnitTest` (flag off), after the fixes: 186 tests, 0 failures.
- `go test ./internal/devicepay ./mobile`: ok.
- Not re-run by the reviewer (relied on the dev and tester notes, no code changed there): flag-on build and tests, universal and armv7 builds, guard shell tests, full `go test ./...`, e2e, any device.

## Verdict
Approve to merge once the preconditions below hold. Safe while dark: the flag is off in every shipped flavor and the built-APK checks enforce it. NOT ready to turn on: no device run, so the real Stripe adapter (`StripeTerminalReader`), the API 24-32 `overrideLibrary` safety, sideload attestation (D12) and the origin lock on a real WebView (D13) are unproven.

## Open items
1. Accept ADR-0125 Amendment 1 (page-supplied amount; no plan gate) before merge.
2. Deadcode-baseline PR (6 `internal/devicepay/cloudcall.go` lines) must land first (#3406).
3. Device checklist D1-D19 on an NFC Android 13+ phone, owner-supplied; D12 and D13 are owner-critical.
4. R5: gate `resolve_unknown` to manager/admin in the page and plugin; ship it with the card that adds the page entry.
5. R3 and R4 follow-ups (bounded `infoOf`, connect gate timeout), cards or epic checklist lines.
6. Legal pass on `tap_edu_body` before the flag is ever on; Weblate for the shell strings.
7. The plugin-side server verification (PaymentIntent lookup endpoint) and #6454 (cloud-created PaymentIntent) remain before any live use.
