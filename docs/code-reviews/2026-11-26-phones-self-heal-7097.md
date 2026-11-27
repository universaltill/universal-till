# Phones: self-heal on resume (ut-docs#7097, ADR-0217 slice T8)

Status: **stub, the Reviewer completes it** (a different-model pass over the
diff is required; none has run on this code yet).

Scope: `internal/credheal` (`Resume`, one new engine method), `mobile/mobile.go`
(`Resume`), `internal/cloudsync/heal.go` (`ProbeCredential` sends nothing
without a credential), `ios/UniversalTill/{TillController,TillServer}.swift` and
`Lifecycle/ForegroundThrottle.swift`, `android/.../MainActivity.kt` and
`ForegroundThrottle.kt`, READMEs, `docs/phones-self-heal-device-tests.md`.

## To attack

1. A resume never rotates a working credential: from `ok` the engine only probes;
   only a 401 with an envelope code starts a round (`TestResumeLeavesAWorkingOrUnreachableCredentialAlone`).
2. A final state (`replaced`, `moved`, `removed`) is never probed or healed on resume.
3. A resume never restarts or stops a healthy server (a sale may be open): iOS
   notifies only after `serverIsUp`, Android only calls `Mobile.resume()`.
4. The throttle on both shells: a swallowed call does not move the window; a
   clock step backwards fires.
5. A till with no credential (restored phone, fresh install) sends no request on
   resume (`TestProbeCredentialWithoutACredentialSendsNothing`).
6. No Swift or Kotlin code reads or sends the cloud token (unchanged; confirm).

## Findings

(none yet)


## Combined review 2026-11-27 (ut-docs#7097 batch, one Reviewer: not the Dev, not the Tester)

Independent security pass by a different-model subagent (Fable) plus the Reviewer's own reading, against the binding review-adr.md conditions (B1-B6, H1-H2, M1-M9). Pre-flight: walked PREFLIGHT.md for the touched surfaces; the batch's combined Tester report (tester-report.md) is the test evidence; no real shop or person names in the added tests.

Findings and outcome (engine fixes merged in):

1. Tester item A, fixed. `credheal.Resume` was reached only from `mobile` (not a deadcode root). It is now also called when the operator asks for a check-in (`requestOperatorCheckin`), so it is reachable from the desktop roots without growing the baseline (`guard-deadcode-baseline.sh` passes).
2. Verified: shells never touch the credential; `Resume` is non-blocking, buffered, and from `ok` only probes, rotating only on a coded 401 from a real check-in; a waiting till with a far wake is not woken early.
3. Not run: Kotlin `ForegroundThrottleTest`, device matrix rows (phones); the persisted-pair-request dead end is a carded design item.

Verdict: SHIP after #2760.
