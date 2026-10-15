# Review: LAN TLS re-proof on an unknown certificate (ut-docs#4410)

- **Date:** 2026-10-15
- **Branch:** `fix/4410-lan-tls-reproof`
- **Card:** universaltill/ut-docs#4410 (ADR-0114 §7, task 4; split from #2737)
- **Author:** Opus 5.5 (cloud lane `lane:cloud-54`)
- **Reviewer:** Fable, independent subagent with security focus, one round

## What shipped

A pinned replica that sees a certificate matching no held pin at its main
till's address re-proves that address. It no longer just fails.

- `internal/lantls/peer.go`: the new `SetPeerReproof` hook. When
  `peerTransport.RoundTrip` gets `ErrPinMismatch` (raised in the handshake,
  so no request byte, and no bearer, reached that server), it calls the
  hook. It then re-reads the pin and retries the request once, verified by
  the new pin. The retry needs a body that can be replayed (`GetBody`) and
  an unchanged main-till address. Otherwise the original mismatch stands.
- `internal/discovery/primary_watch.go`, `(*PrimaryWatch).Reproof`:
  - **Proof:** it challenges `sync.primary_url` over TLS that accepts the
    new certificate (`reproveVia`). The challenge carries no bearer and is
    never sent over plain HTTP. The proof is an HMAC keyed by the bearer
    hash and covering the pin the replica saw, so a MITM can't produce it.
  - **Serialised:** requests queued behind a successful re-proof just
    retry.
  - **Bounds:** the proof runs under its own 15 s timeout, not the
    caller's deadline. A failure starts a 1-minute cooldown. A refused
    proof logs an unkeyed WARN once; a transport failure logs INFO.
  - **Re-discovery:** `challenge` uses the same re-proof, so a candidate
    with a new certificate is adopted with its address and pin when it
    proves.
- `internal/app/app.go`: wires the hook to `deps.PrimaryWatch.Reproof` and
  restores it before the database closes.
- Docs: `ut-docs/architecture/lan-sync.md` (companion PR).

## Tests (TDD: each written first and seen failing)

- lantls: adopt and retry, body re-sent, failed re-proof keeps the
  mismatch, no retry when the pin is unchanged, an unrewindable body is not
  replayed.
- discovery:
  - re-discovery: new certificate re-proven; unproven certificate refused.
  - Reproof: restored main till adopted; MITM relaying the real proof over
    TLS refused, with the cooldown checked; MITM relaying an unpinned
    plain-HTTP proof refused; pin already changed skips the proof.
  - review fixes: an expired caller deadline doesn't fail the proof;
    success with the held pin is no refusal.
  - end to end through the real peer transport: the bearer only travels on
    the verified retry.
- pages: real sockets and the real `primary-proof` handler. The first pull
  re-proves, adopts the pin and applies the bundle, with no plain-HTTP
  request.
- Mutation checks: dropping the pin binding in `reproveVia`, the cooldown,
  and the own-timeout each fail the tests aimed at them. The reviewer
  independently reverted `reproveAndRetry` and `reproveVia` in a worktree:
  all 9 new tests at the time failed.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | major | Re-proof ran under the triggering request's context. 800 ms UI proxies (held sales, tables) could fail a genuine proof, start the cooldown and leave an unkeyed "did not prove" WARN in Problems for 24 h. | **Fixed:** own 15 s bound (`context.WithoutCancel`). WARN only for a refused proof (`proofRefusedError`); transport errors log INFO. Test added. |
| m1 | minor | A re-pairing during the proof could have its new pin overwritten. | **Fixed:** URL and pin are re-read after the proof; if either moved, nothing is stored. |
| m2 | minor | The retry could go to the old host if re-discovery moved `primary_url` meanwhile. Guaranteed mismatch, no leak. | **Fixed:** `samePeerHost` re-checked before the retry. |
| m3 | minor | A proof that succeeded with the held pin was treated as a refusal (cooldown + WARN). | **Fixed:** returns nil. Test added. |
| n1 | nit | `reproofMu` isn't ctx-aware, so waiters block for up to one proof. | Accepted: bounded by 15 s, background sync only, checkout unaffected. |
| n2 | nit | Server rate limit is 10/min per IP; worst case is about 2 proofs/min per replica. | Accepted. |

Security properties the reviewer confirmed:
- no bearer over an unverified connection;
- no MITM adoption (TLS relay, plain relay, Host rewrite);
- no plain fallback while a pin is held;
- no recursion into the peer transport;
- the hook's lifetime is correct in the app and in tests.

## Gate

- `go build ./...`, `go vet`, and `golangci-lint` (0 issues).
- `go test` passes for `internal/{discovery,lantls,pages,app}`.
- `go test ./...` passes except `internal/plugins`, which timed out after
  10 min in `TestHTTPLANEgress`. That test reaches a public endpoint from
  the sandbox, and the package is untouched; CI is authoritative.
- `guard-data-access`, `guard-core-neutral` and the other `ci.yml` guards
  pass. Three failures are environment only:
  - `guard-deadcode-baseline`: the local tool was built with go1.26;
  - `guard-shellcheck-version`: no shellcheck binary;
  - `retry-with-backoff.sh`: a helper, not a guard.
- No UI, i18n or manual change: the existing unreachable banner covers a
  refused re-proof. No e2e run, since no surface is touched.

## Verdict

Safe to merge. Deferred: key rotation / `next_pin` (#4409), and closing
plain-HTTP sync entirely (#4411).
