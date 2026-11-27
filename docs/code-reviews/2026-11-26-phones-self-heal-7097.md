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
