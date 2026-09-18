# 2026-09-18 — Hold-on-replica re-park created_at: tolerant comparison

Card: universaltill/ut-docs#2389 (p3, complexity:medium)

## What shipped

`TestHoldOnReplica_ParkLandsOnPrimaryAndResumeDeletesThere`
(`internal/pages/held_sale_sync_proxy_test.go`) failed once in CI with a
1-second mismatch between the primary's re-parked `created_at` and the
expected value, self-resolved on the very next run with no code change in
between (ut-docs#2389's own filing). Test-only change — no production code
touched.

## Root cause

`heldSaleWriteThrough` posts the first park to the PRIMARY first with
`created_at == ""` (no `HeldOrigin` yet), so the primary's own `Upsert`
COALESCEs it to *its own* `datetime('now')`. The primary's response
(`syncHeldSaleUpsertResult`) echoes back only `applied` + `updated_at`, not
`created_at`, so `mirrorHeldSaleFromPrimary` then writes the replica's local
row (what the test's `local.CreatedAt` captures) via a **second, independent**
`datetime('now')` read. These two clock reads normally land in the same
wall-clock second but can straddle a boundary under real CI scheduling
jitter between the two writes — primary is written first, so it reads as the
*older* of the two on a straddle, exactly the direction of the one observed
failure. Confirmed by reading `held_sale_sync_proxy.go` (`heldSaleWriteThrough`,
`mirrorHeldSaleFromPrimary`, `syncHeldSaleUpsertResult`) and `hold_api.go`
(`parkCurrentBasket`, `resumeHeldSale`, `heldSaleForResume`) directly, and
independently confirmed by the review below.

## Fix

Compare `again.CreatedAt` against `local.CreatedAt` with a tolerance
(parse both with the existing `heldSaleTimeLayout` constant, assert the
absolute `time.Duration` difference is ≤ 2s) instead of requiring
byte-exact string equality. `Label` stays an exact comparison (unrelated to
clock skew).

## Independent review

Opus, isolated worktree, never saw the implementation reasoning.

**Verdict: safe to merge** (after two non-blocking suggestions folded in
below).

- Verified: build clean, `go test -race -count=10` on the target test
  green, wider `internal/pages`+`internal/data` package tests green,
  `golangci-lint run ./internal/pages/...` 0 issues, `gofmt` clean.
- **Correction accepted**: my first-draft comment guessed a second
  candidate mechanism (the `Upsert` `ON CONFLICT DO UPDATE` branch racing
  a slow delete-write-through on resume). The reviewer read
  `heldSaleDeleteWriteThrough`/the test's own assertions and showed this
  cannot be the cause here (the delete is synchronous and the test
  confirms the primary row is gone before re-parking, so the re-park
  always hits INSERT) — the real mechanism is the two-independent-clock-
  reads one above. The comment in the test was corrected to match before
  this commit; the wrong hypothesis was never shipped.
  **No hidden production race was found** — nothing to file as a bug.
- **Not vacuous**: the byte-exact `created_at` guarantee is still covered
  elsewhere against a seeded clock —
  `internal/pages/hold_api_test.go`'s `created_at == "2026-09-09
  10:00:00"` assertions and `internal/data/small_repos_test.go`'s
  `Upsert` insert/update `created_at` semantics tests. This end-to-end
  test's own exact-match assertion was never the thing actually proving
  preservation correctness, so relaxing it to a tolerance loses no real
  regression coverage.
- **Tolerance widened 1s → 2s** (reviewer's suggestion, folded in): the
  skew window is one in-process `httptest` round trip, normally well
  under 1s, but a loaded CI runner occasionally exceeding it isn't
  impossible; 2s costs nothing given the exact coverage noted above.
- **Follow-up filed, not fixed here** (correctly out of scope for a p3,
  test-only card): the real root fix — echo `created_at` in
  `syncHeldSaleUpsertResult` and assign it before
  `mirrorHeldSaleFromPrimary`, mirroring what ut-docs#2271 already did
  for `updated_at` — would remove the two-clock-reads divergence at the
  source, a small genuine correctness gain (replica/primary `created_at`
  would then agree exactly, not just approximately) beyond test hygiene.
  Filed as universaltill/ut-docs#2394.

## Verified beyond automated tests

- Hand-checked the tolerance math against the real observed failure values
  (26s vs 27s → now within tolerance) and against a synthetic 5s-drift case
  (still fails) to confirm the change isn't a vacuous always-pass.
- Read the full call chain (`heldSaleWriteThrough`, `mirrorHeldSaleFromPrimary`,
  `syncHeldSaleUpsertResult`, `parkCurrentBasket`, `resumeHeldSale`,
  `heldSaleForResume`, `HeldSalesRepo.Upsert`) directly rather than trusting
  the card's own flake theory.

## Explicitly deferred

- universaltill/ut-docs#2394 (echo `created_at` from the primary's upsert
  response) — a real but small correctness/precision improvement, not a
  user-visible bug; left as a separate Backlog card rather than folded into
  this p3 test-hygiene fix.
