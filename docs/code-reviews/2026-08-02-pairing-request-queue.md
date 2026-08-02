# Code review: pending-pairing-request queue + handlers (ADR-0033 part 2/3)

**Date:** 2026-08-02
**Scope:** `internal/db/migrations/027_pending_pairings.sql` (new),
`internal/data/pairing_repo.go` (new), `internal/pages/pairing_api.go`
(new), `internal/pages/sync_api.go`, `internal/pages/init.go`, plus
test-schema/upgrade-simulation fixes in `internal/pages/ui_smoke_test.go`,
`internal/db/{barcode_seed_test.go,dead_seed_test.go}`.
**Trigger:** universaltill/ut-docs#184 (part 2/3 of ADR-0033, LAN till
discovery + approve-to-pair; part 1 is `#183`, part 3 is `#185`).

## What shipped

The pending-request queue and API surface behind approve-to-pair: a
replica POSTs a pair request (unauthenticated by design, rate-limited)
carrying a device name and a SHA-256 `commitment` of a locally-generated
secret it never transmits in the clear. A manager (session-gated) lists
pending requests with a derived 6-digit verification code for the
visual match-and-compare ADR-0033 §4 describes. Approve/deny are
manager-PIN-gated, mirroring `refund_page.go`'s `AuthorizeManager`
pattern. Approve mints a one-time enrolment token via the *same*
`enrolTokens` store `/api/sync/enroll` (the QR flow) already uses — not a
second, disconnected token store — and the replica retrieves it via
`GET /api/sync/pair-requests/{id}?request_secret=...`, released only when
`SHA-256(request_secret) == commitment`.

- New migration `027_pending_pairings.sql`: `pending_pairings` table
  (id, device_name, commitment, token, requested_at, expires_at, status)
  + indexes on `status` and `expires_at`.
- `PairingRepo` (`internal/data`, all raw SQL stays here per
  `CLAUDE.md`): `CreatePendingRequest`/`ListPending`/`GetByID`/
  `Approve`/`Deny`, lazy-expiry filtering (expired rows excluded from
  reads, opportunistically deleted).
- 5 handlers in `pairing_api.go`, a small per-source sliding-window rate
  limiter, `derivedVerificationCode`.
- `registerSyncAPI` now returns the `*enrolTokens` store it creates
  (was previously func-local) so the pairing handlers can share it.
- No new i18n strings (JSON-only API surface; the UI is `#185`'s job).
- No ADR needed — this implements ADR-0033 as already accepted, no new
  architectural decision.

## New tests

15 tests across `internal/data/pairing_repo_test.go` and
`internal/pages/pairing_api_test.go`, including an end-to-end HTTP flow,
a real-migrated-schema variant (`TestPairingFlow_AgainstRealMigratedSchema`,
via `internal/db.Open` on a temp file — guards against the hand-rolled
`ui_smoke_test.go` fixture drifting from migration 027), and a dedicated
digit-only check for the verification code.

## Verification (self, before independent review)

- `go build ./... && go vet ./...`: clean.
- `go test ./...`: green except the pre-existing, already-filed
  `TestSaveCleansUpDirectoryOnWriteFailure` (ut-docs#258, fails under a
  root-run sandbox) — confirmed unrelated (fails identically on an
  unmodified `main`).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`:
  both green.
- Fail-then-pass, done personally: temporarily made `commitmentMatches`
  always return `true`, ran
  `TestPairingFlow_ApproveDeliversTokenOnlyToSecretHolder`, confirmed it
  fails with `expected 404 with a wrong secret, got 200` (a real
  possession-gate bypass, not a panic), restored, confirmed green.

## Independent review

Different-model subagent (Opus), fully independent re-verification
(re-ran build/vet/guards/tests from scratch, and independently reproduced
the fail-then-pass above by making the same edit itself). Findings:

- **Real, fixed (blocking-equivalent):**
  - Verification code was 6 **hex** characters
    (`hex.EncodeToString(sum[:])[:6]`), not 6 **decimal digits** as
    ADR-0033 §4 specifies — the replica side (`#183`/`#185`) would
    compute a different value and the two screens could never match.
    Fixed: `binary.BigEndian.Uint32(sum[:4]) % 1000000`, zero-padded.
    New regression test asserts every character is `0`-`9`, across 200
    distinct inputs.
  - Enrolment token was issued via `tokens.issue()` *before*
    `PairingRepo.Approve`'s compare-and-swap ran — a lost race
    (concurrent approve/deny/expiry) would leave a live, unburnt token
    sitting in `enrolTokens` for up to 10 minutes with nothing able to
    reclaim it. Fixed: on `Approve` failure the handler now calls
    `tokens.consume(tok)` to burn the orphaned token immediately.
  - A lost approve race (second approve, or approve racing deny) mapped
    to a bare `500` with the row id echoed in the body, when it's
    actually a `409`-class condition, not a server fault. Fixed: `Approve`
    returns a new `data.ErrNotPending` sentinel; the handler maps it to
    `409 Conflict` with a generic message. New test
    (`TestApprovePairRequest_SecondApproveReturns409AndFirstTokenStillWorks`)
    covers both the status code and that the *first* approve's token
    stays retrievable afterward.
  - Approving a request seconds from its original `expires_at` produced
    a `200 approved` for a pairing the replica could never actually
    retrieve (row expires before the next poll, indistinguishable from a
    denial). Fixed: `Approve` now extends `expires_at` by the same TTL
    from the moment of approval. New test
    (`TestPairingRepo_ApproveExtendsExpiry`) creates a request with a
    50ms TTL, approves it, waits 100ms (past the *original* deadline),
    and confirms it's still retrievable.
  - No audit trail for who approved a pairing — approving hands a new
    device the **entire shop database including `users`/`pin_hash`**,
    strictly more sensitive than a refund, which *does* get an audited
    actor via the same `AuthorizeManager` pattern this mirrors. Fixed:
    `authorizePairingManager` now returns the PIN-owner's id (or the
    session actor when `UT_AUTH=off`), and both approve and deny write a
    `till_pairing` audit row via the existing `POSRepo.InsertAudit`. New
    test asserts the row's `action`/`entity_type`/`entity_id`.
  - The possession-gated retrieval endpoint (`GET
    /api/sync/pair-requests/{id}`) was unauthenticated *and*
    unrate-limited — the exact brute-force surface the ADR's own
    security review was concerned about, left unmetered while the
    request endpoint was capped. Fixed: shares the same per-source rate
    limiter.
  - The rate-limiter's per-source hit list was only ever pruned when
    *that same source* made another request — a source that fires once
    and never returns leaked a map entry for the process lifetime.
    Fixed: once the map holds ≥256 distinct sources, `allow()` pays for
    one full sweep dropping anything now stale (self-healing, no
    background goroutine needed for what ADR-0033's own consequences
    section calls a not-general-purpose v1 facility).
- **Real, documented rather than fixed (needs #183 to actually close):**
  `derivedVerificationCode`'s `primaryTillID` input reuses
  `marketplace.DeviceIDFromConfig` as a stand-in, because LAN discovery
  (`#183`) — which was supposed to carry a real primary-till identifier
  over the mDNS TXT record — was found to have been closed "completed"
  on the tracker with **zero code actually implemented** (no dependency
  in `go.mod`, no discovery code anywhere under `internal/`, no commits
  referencing it). This means ADR-0033 §8's outbound (impersonation)
  mitigation is not actually in effect end-to-end yet — filed as a new
  Backlog card, **universaltill/ut-docs#264**, rather than silently
  left undocumented or expanding this card's scope to redo `#183`'s
  work.
- **False positive, verified and dismissed:** the review's first pass
  flagged the new migration as numbered `027` while believing the
  highest existing migration was `020`, which would make `027` a
  landmine for six future migration numbers. Independently checked
  against the actual repository state: `021`–`026` already exist and are
  already merged into `main` — `027` is genuinely the correct next
  number. (Root cause, caught and fixed separately during this review
  pass: the feature branch had accidentally been created from a stale
  local `main` ref instead of `origin/main`, missing that history; fixed
  by rebasing onto the real `origin/main` before this record was
  written — see below.)
- **Accepted as-is (nits):** a sub-microsecond timing difference between
  an unknown-id and an unapproved-id response on the retrieval endpoint
  (not exploitable — UUIDv4 ids, nothing enumerable); `ListPending`
  issuing a `DELETE` on every manager poll (a deliberate lazy-cleanup
  design choice per this card's own acceptance criteria, now backed by
  an index on `expires_at`).

## A real branching bug, found and fixed mid-review

While independently re-verifying the "migration numbering" finding
above, discovered the feature branch had been created from a stale
local `main` git ref (`a910205`) rather than the actual `origin/main`
(`835b082`) — six merged commits behind, including migrations 021–026.
Fixed by stashing the in-progress diff, resetting the branch onto the
real `origin/main`, and replaying the stash (one real merge conflict in
`ui_smoke_test.go`'s hand-rolled schema fixture, resolved by keeping
both the new `pending_pairings` table and the `sales.order_type`/
`service_charge_amount` columns from upstream). This also surfaced two
`internal/db` upgrade-simulation tests
(`TestSeedBarcodeChecksumsFixedOnUpgrade`,
`TestDeadTaxInclusiveSeedRemovedOnUpgrade`) that manually rewind
`schema_migrations` and replay migrations physically — both needed a
`DROP TABLE pending_pairings` rewind step added alongside their existing
per-migration `DROP COLUMN` steps (same class of fix already applied for
migrations 024/025/026, and the exact gap ut-docs#263 already tracks
generically).

## Verdict

**Safe to merge.** Independent review found six real issues — one
security-relevant (unrate-limited retrieval endpoint), several
correctness/robustness (verification-code format, token-orphan-on-race,
wrong status code on a lost race, expiry-during-approval, missing audit
trail) — all fixed, each backed by a new test, full gate re-run green
after every fix. One documented-not-fixed gap (`#264`, needs `#183` to
actually land) and one false positive (migration numbering, actually
correct once rebased onto the real branch base) were verified rather
than taken at face value in either direction.
