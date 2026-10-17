# Code review — LAN TLS key rotation (ut-docs#4409)

**Date:** 2026-10-15 · **Card:** universaltill/ut-docs#4409 (ADR-0114 §7, split from #2737) · **Lane:** lane:cloud-24
**Author model:** Opus 5.5 (dev subagent) · **Reviewer model:** Fable (independent, fresh context)

## What shipped

- `internal/lantls/rotation.go`: a main till whose LAN key is ≥ 4 years old creates a next P-256 key (`next-key.pem`/`next-cert.pem`, 0600, atomic writes) at load, records state in `rotation.json`, and keeps serving the current key. Every decision runs at load only, so the served pin is fixed per process and `ServedPin`, pairing codes and the primary proof stay correct. Switch at the next load once every paired, non-revoked pinning till has confirmed, or 14 days after the next key was made; the old key moves to `prev-*` (kept 14 days on disk, never served), then is deleted. Crash-safe switch order: `key.pem` always exists, and a half-finished switch is completed on the next load. A clock before 2020 (a Pi with no RTC) runs no time-based step.
- `fleetlink`: `Hello.tls_pin` is filled in (it used to be a placeholder). `Hello.next_pin` and `Report.next_pin` are new and clipped. The link client passes `OnHello` the pin **verified on that link's own TLS handshake** (`wsConn.VerifiedPin`, "" over plain HTTP).
- Replica (`sync_link_client.go`): takes `next_pin` only from a hello whose own connection was verified against the held pin, never from the self-reported `tls_pin`, and only a 64-hex value different from the held pin. It stores it as `sync.primary_cert_next_pin`. The peer transport accepts either pin, and the report carries it. When the verified pin equals the held next pin, the replica promotes it, so the retired key stops being trusted. Re-proof, re-discovery and pairing clear any next pin.
- Main till (`sync_link.go`): the hub hello announces both pins. `OnReport` records a confirmation (`ConfirmNext`) against the paired tills, leaving out those whose latest report says unpinned.
- Docs: `ut-docs/architecture/lan-sync.md` gains the rotation bullet.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | **Blocker (security)** | `next_pin` was gated on "a pin is held now", not on the connection the hello came over. On an unpinned replica's first hello, the once-per-process pin backfill pinned it mid-`OnHello`. A `next_pin` from a **plain-HTTP** hello was then trusted, so an on-path attacker could plant their own key as trusted. Reproduced end to end. | **Fixed.** The decision now uses the pin verified on the link's own handshake. Regression test `TestRotation_PlainLinkHelloNeverSetsANextPinAfterBackfill`: re-verified by the orchestrator, it fails on the old gate ("taken from a hello that came over plain HTTP") and passes with the fix. |
| 2 | Minor | `ConfirmNext` truncated more than 256 required tills, which could mark ready while uncounted tills had not confirmed. | **Fixed.** It is never ready above the cap, and falls back to the 14-day ceiling (`TestRotation_TooManyRequiredNeverReady`). |
| 3 | Nit | A power cut between the two renames of the switch left an orphan `next-cert.pem`. | **Fixed.** It is removed at load when no `next-key.pem` exists (`TestRotation_OrphanNextCertIsRemoved`). |
| 4 | Minor | Readiness is re-evaluated only on a new confirmation. A till revoked after the others confirmed, or a main till with no pinning tills, waits the 14-day ceiling. | **Accepted, documented** in lan-sync.md. Nobody is cut off; the switch is only slower. |
| 5 | Nit | `reportOf` closure reads `hub` after assignment. Unsynchronised in theory; the assignment happens-before serving. | Accepted. |
| 6 | Nit | A forward clock jump of 4 years or more mints a next key early; after the clock is corrected it switches only on full confirmation. | Accepted. Harmless. |

The reviewer checked and found correct: the retired key is never served; constant-time pin compares; crash safety of every switch step; `ConfirmNext` mutex and the transport bound to the (pin, next) pair; bounded memory; the re-proof path for a peer that missed the window; no SQL outside `internal/data`; `MkdirAll` before writes, with the dir from `paths.Data("tls")`.

## Verification

- TDD: every new test was seen failing first (dev). The reviewer independently broke `switchTo` and the promotion branch, and both guarding tests failed for the right reason. The orchestrator independently re-verified the blocker regression test.
- End to end over real sockets: a pinned replica learns, reports and confirms the next pin, and after the main till's switch it keeps syncing over pinned TLS with no plain-HTTP fallback and no manual step (`TestRotation_PinnedReplicaLearnsAndConfirmsTheNextPin`, `…PromotesTheNextPinAfterTheSwitch`).
- Gate: `gofmt`, `go build ./...`, `go vet ./internal/...`, `go test` on lantls/fleetlink/discovery/db/pages/server/app, `-race` on lantls/fleetlink/rotation tests, `golangci-lint` 0 issues, and every `ci.yml` guard. Two guards could not run locally (deadcode toolchain, shellcheck binary missing; no shell scripts changed) and are left to CI.
- No UI surface, no user-facing strings, no migration.

## Deferred

- ut-docs#4423: the manager "rotate now" action and rotation on restore to new hardware.

**Verdict:** safe to merge once CI is green.
