# Review: Tap to Pay core seams and phone as card reader (ut-docs#6455, #6533, #6538)

PRs #2637 (K1a), #2638 (A1), #2639 (K1b), #2640 (R1a), #2641 (R1b), #2643 (A2), stacked. Two rounds of
independent security review (Fable 5.2; devs: Sonnet 5.5) on temp worktrees: round 1 at `2e8a529a7`, round 2
at `0abc5520a` (contains the heads of K1b `d39842d8a`, R1a `95c10c793`, R1b `c341d6db8`). Governing: ADR-0198
sections 3 and 6, ADR-0127.

Tester: combined round-1 and round-2 Tester (Sonnet 5.5, not the Dev). Dev: Sonnet 5.5. Record committed in #2639, the
first open PR of the stack (K1a #2637 and A1 #2638 were already merged); #2640, #2641 and #2643 refer to it.

## What shipped
Per-caller payment broker (native, host fn, core), `payment_provider_call` and `tap_to_pay_request` host
functions with two manager-granted permissions, `payment_attempts` (migration 111), core's own broker
confirmation of every tap, the pending screen with Check again and manager resolve, `Resolve(key, outcome)`
on the Android seam, card reader identity and pairing (`card_reader_credentials`, migrations 112 and 113,
route guard, per-IP bearer-failure limiter, approval poll with its own limits), the reader link hub and
remote collect, reader re-proof after a main key change (`next_pin`), and Android reader mode. All dark
(`card_readers.phone_pairing`, `-PtapToPay`, `-PreaderMode`).

## Findings
| # | Finding | Severity | Result |
|---|---|---|---|
| K-1 | Check again / resolve unlocked as "Not paid" when `session_cancel` answered `already_succeeded` | important | fixed (`tapCancelThenAssess`), mutation-verified |
| K-2 | Plugin `start_failed` or decline of a tap leg was final without a broker check | important | fixed (`tapVerifyFailure`), mutation-verified |
| A-1 | Reader mode never put the engine in the foreground | important | fixed |
| A-2 | One unknown outcome blocked the reader phone for good | important | fixed; see A-13 |
| A-3 | 30 s link-loss cancel reset by every reconnect | important | fixed |
| A-4 | Stripe SDK connection never dropped on revoke, loss, background | important | fixed |
| A-5 | Re-pair after revoke failed until force-stop | important | fixed |
| R-1/A-6 | 401 for lockout and lookup errors; phone wiped on any 401 | important | fixed (429 / 503 / 401; phone wipes on 401 only) |
| R-2/A-7 | No reader key rotation | important | fixed by re-proof; `next_pin` not announced in hello (accepted, noted for ADR-0198 6.2) |
| A-12 | A 404 on the unverified re-proof connection cleared the phone's pairing (`PinReProof.kt:61`, `ReaderNet.kt:158`) | important | fixed (`df78c1418`, tests `f7a058c0d`): `ReProof.Revoked` removed; anything but a verified proof keeps the pairing and backs off at the 30 s pace; only a 401 on the pinned link wipes. Re-read against the review text by the integrator (DevOps), not re-reviewed by Fable. `ReaderReProofTest` now asserts a 404 keeps the pairing |
| A-10 | Whole response body read before the 8192-character cut | nit | fixed in the same commit (`PairHttp.MAX_BODY`, read at most 8 KiB from the source) |
| K-8/A-13 | After a main restart the pending lock is gone and the phone releases its unknown record on the next key: second charge possible (`ReaderPayments.kt:92-99`) | important | open: blocks the flag, card to file |
| P-2 | Gate binding not limited to the plugin that owns the tap entry | pre_existing | deferred |
| K-6, K-7, R-3, R-4, R-8..R-11, A-8..A-11, A-14, S-2 | see `temp/phone-reader/review-batch.md` section 3 and `review-batch2.md` section 3 | nit | deferred |

## Verification
- Round 2, at `0abc5520a`: `go build ./...`; `go vet` and `go test` on pages (tap, reader, pair, poll, proof,
  bearer), readerlink, fleetlink, paybroker, pos, devicepay, data: green.
- Mutations (restored): cancel error ignored in `tapCancelThenAssess`: `TestTapCheck_CancelAlreadySucceededIsPaidNotNotPaid`,
  `TestTapCheck_CancelFailureStaysPending`, `TestTapResolve_NotChargedCancelAlreadySucceededIsRefused` fail.
  Verification skipped in `tapVerifyFailure`: `TestTapGate_FailureAfterAttemptRowIsVerifiedWithBroker` fails.
  TLS check removed from the reader proof: `TestReaderProofRefusals` fails. Round 1: `ConfirmTap`
  amount/currency compare off and the link check in `ReaderResult` off both fail their tests.
- Kotlin reader unit tests (default flags): 104 tests, 0 failures. Flag-ON build and device
  run: not done.
- Pre-flight: help topics `payments.md`, `multitill.md` updated; docs-shots manifest to regenerate in CI
  (reader cards changed); `en.json` + `en.meta.json` only (ADR-0160); `reference/plugin-host-functions.md`
  updated on ut-docs; ut-cloud manifest mirror for the two permissions and `tap`: deferred (S-2); README n/a
  (dark); ADR-0198 6.5 and 6.2 dated note for the frame and re-proof differences: deferred (R-7, list in
  `dev-r1b.md`).

## Verdict
K1a, A1, K1b: approve. R1a, R1b: approve with follow-ups. A2: changes required (A-12), then fixed and re-read
by the integrator: approve. #2639 was merged with main before landing; migrations 111/112/113 stay unique
against main. The flag stays off until K-8/A-13, the
`stripe_tap` plugin entry, the O-7 Stripe check and a two-device run are done.

## Deferred
Cards for every open row above; boot-time handling of `pending`/`unknown` attempt rows (K-8); device check
that an Android main serves pinned LAN TLS; "denied by a manager" answer for the phone.
