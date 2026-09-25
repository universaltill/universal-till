# Review: held sale atomic claim + tombstone (ut-docs#2712)

- **Branch:** `fix/2712-held-sale-atomic-claim`
- **Card:** universaltill/ut-docs#2712 (`complexity:hard`) — a held/parked
  sale could be tendered twice across a shop's till fleet.
- **ADR:** ADR-0093 Amendment B (ut-docs branch
  `docs/2712-adr-0093-amendment-b-held-sale-claim`, not yet merged).
- **Author:** Opus (Dev). **Reviewer:** Fable (independent, different
  model, from-scratch, in its own detached worktree).

## What shipped (Dev)

- Migration `044_held_sales_tombstones.sql`: `held_sales_tombstones` on the
  primary, one row per held sale it deleted, pruned past 24h on every write.
- `HeldSalesRepo.ClaimAndTombstone` — one `BEGIN IMMEDIATE` transaction:
  `DELETE … RETURNING` the row and write its tombstone; absent row → answer
  whether a fresh tombstone exists. `HeldSalesRepo.DeleteAndTombstone` for
  the existing `/delete`.
- `POST /api/sync/held-sales/claim` (bearer-authed, on the auth exempt list,
  pinned by `TestSyncPullPathsAreExempt`).
- `resumeHeldSale` now claims BEFORE `RestoreHeld` via
  `heldSaleClaimForResume`; a resume that fails after a successful claim
  hands the row back (`heldSaleGiveBack`). The primary till's own resume
  goes through the same local claim (a deliberate step past the ADR's
  literal text, closing the identical race on the primary itself).
- Dead code removed: `heldSaleDeleteWriteThrough` / `deleteHeldSaleOnPrimary`.
- No user-facing change: the refusal reuses the existing `hold.error.not_found`
  toast, no new locale key, no setting, no new page → **no help-topic update
  needed.**

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | **blocker (data loss / offline-first)** | A till's OWN tombstone refused its own newer copy. Everyday cycle: resume order X online (claim → tombstone X on the primary) → add an item → tap Hold; the re-park lands under the **same id** (ut-docs#1918, `hold_api.go:240`). If the primary was unreachable for just that re-park, the row is local-only (`primary_synced=0`). On the next online resume `/claim` finds no row but a fresh tombstone → `known=true` → the replica **deleted its only copy and refused the resume**. The order (already cooked, unpaid) was gone everywhere for up to 24h. The ADR's own argument ("ids are never reused across *unrelated* orders") missed that the *same* order is legitimately re-parked under its id. Reproduced with a new test that fails on the Dev tree: `TestResumeOnReplica_OfflineReparkAfterOnlineResumeStillResumes` → `a newer offline re-park of an order this till itself resumed must still resume, got 200:` (not-found toast rendered, order dropped). | **Fixed.** Tombstones now record **who** resolved the order: `held_sales_tombstones.till` (the caller's `tills.id` from `syncTill`; `""` for the primary till's own resume). `ClaimAndTombstone(ctx, id, till)` answers `known=true` only for a tombstone written by a *different* till. A caller's own tombstone can only be contradicted by that caller's own newer copy (its local copy is deleted at the end of the resume that wrote the tombstone); a stale mirror that outlived the resume is still dropped by Amendment A F2 (`primary_synced=1`), and a later resolution by another till overwrites the stamp. Wire shape `{claimed, known, row}` unchanged. Migration 044 is unmerged, so the column was added to it (not a 045) and its checksum re-pinned. New tests: `TestHeldSalesRepo_ClaimAndTombstone_OwnTombstoneIsNotKnown`, the regression test above (which also proves another till's later claim still refuses the same copy), and same-till/other-till branches in `TestSyncHeldSales_Claim` / `TestSyncHeldSales_DeleteLeavesTombstone` / `TestHeldSalesRepo_DeleteAndTombstone_LeavesTombstone`. |
| 2 | should-fix (test validity) | The cross-till tests' "till A" claimed with till B's own bearer (`claimOnPrimaryAs(…, "b-123", …)`), and the concurrent-claim tests raced four requests all from one enrolled till — so the fixtures never exercised two distinct tills. The ownership rule exposed this immediately. | **Fixed.** `newHeldSaleCrossTill` seeds a second till (`Till A`, bearer `a-456`); the endpoint tests seed one till per racer / a `Till 3`. |
| 3 | nit (docs) | ADR-0093 Amendment B's text (points 1–3) still describes `known` as "a fresh tombstone exists" and the tombstone as `(id, deleted_at)`. | **Open — orchestrator.** The docs branch must be updated before the amendment is accepted: tombstone `(id, deleted_at, till)`; `known` = "a fresh tombstone written by another till exists"; and the "ids are never reused" sentence replaced by the same-order re-park reasoning above. |
| 4 | accepted residual → Backlog card | `heldSaleGiveBack` is best-effort: a resume that claimed successfully, then failed (corrupt payload / auto-park failure / already-live), then could not reach the primary for the give-back, leaves the order only on the replica; if the replica's copy was a `primary_synced` mirror it is dropped by the next reconcile. A double fault (resume failure AND network failure inside one tap), narrower than the pre-change window it replaces, but pre-change "a failed resume cost nothing". | Not fixed here; note for a Backlog card ("give-back: clear `primary_synced` on local-only fallback, or retry once"). |
| 5 | note (coverage) | `TestResumeOnReplica_ClaimsOnPrimaryBeforeRestoring` asserts post-state only; no test pins the *ordering* claim-before-`RestoreHeld` as such. Acceptable: the atomic claim is the mechanism and the ordering is a two-line handler property, but a future refactor could move `RestoreHeld` above the claim unnoticed. | Noted. |
| 6 | info | `/claim` for an id that was never a held sale writes **no** tombstone (only a taken row does), so probing/replay cannot grow the table; growth is bounded to 24h of deletions and pruned on every claim/delete (including the absent-row branch). Standalone/primary tills write one tombstone per resume, pruned the same way — no side effect beyond a small table. `/claim` vs `/delete` racing the same id: both are `BEGIN IMMEDIATE` write transactions, so they serialize; either order leaves a tombstone. `heldSaleGiveBack` carries `TableID`, `CreatedAt`, label, payload, totals through `heldSaleToSyncRow` — nothing lost. `TestSyncPullPathsAreExempt` is a real pin (positive list asserts `exempt(p)`, negative list asserts `!exempt(p)`). Migration 044 follows 042/043's conventions (header comment with card ref, `IF NOT EXISTS`). No file writes (no `os.MkdirAll`/`paths.Data` concern), no money code, no new strings, no real client names or secret literals in fixtures. | — |

## Verified beyond automated tests

Commands run on the branch (Dev tree, then again after the fix):

- `gofmt -l .` → no output; `go build ./...` → OK; `go vet ./...` → OK;
  `golangci-lint run ./internal/...` → `0 issues`.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-migration-version-collision.sh`, `guard-kiosk-engine.sh` → all ✓.
- `go test -count=1 ./internal/db` → ok (21.1s; new 044 pin
  `00bdfb99…`). `go test -count=1 ./internal/data -run HeldSalesRepo` → 12/12
  pass. `go test -timeout 25m -count=1 ./internal/pages` (CI's own gate) →
  ok, 100.7s. Held-sale/resume/hold/open-orders subset: 91 tests pass.
- **`-race`:** `./internal/auth`, `./internal/db`, `./internal/data`,
  `./internal/pages/{catalog,common,itemsnav,settingsnav}` pass under
  `-race` (Dev tree; `data` 213s, `db` 542s). The full `./internal/pages`
  package exceeds `go test`'s default 10-minute timeout under `-race` on a
  4-CPU box (it needs `-timeout 20m` even without `-race` in CI; the stuck
  test at the 10m mark was an unrelated pairing test) — pre-existing, not
  this change. After the fix, re-run under `-race -timeout 40m -count=1`:
  `./internal/data` ok (197.6s), `./internal/auth` ok (11.0s), and the
  held-sale/resume/hold/open-orders subset of `./internal/pages` (`-run
  'HeldSale|SyncHeldSales|Resume|Hold|OpenOrders'`, 91 tests, including
  both concurrent-claim tests) ok (27.1s) — **no `DATA RACE` reports**.

**TDD re-verification, independently, in a detached scratch worktree
(never on the shared checkout):**

- Non-atomic claim (read via `r.db` *before* `BeginTx`, then `DELETE`
  without checking rows — the pre-fix Get-then-Delete shape):
  `TestHeldSalesRepo_ClaimAndTombstone_ExactlyOneConcurrentWinner` →
  `round 2: exactly one claim must win …, got 2 claimed / 2 refused`;
  `TestSyncHeldSales_ConcurrentClaimsExactlyOneWins` → `round 0: exactly one
  of 4 racing claims must win, got 2`. Restored → both pass. (A first
  attempt that left the read *inside* the `BEGIN IMMEDIATE` transaction did
  **not** fail the test — the DSN's `_txlock=immediate` serializes at
  `BEGIN` — which is worth knowing: the transaction, not `RETURNING`, is
  what makes the claim atomic.)
- Ignoring the tombstone (`if false && ok && known`):
  `TestResumeOnReplica_LostPushReplyCannotDoubleTender` → `till B's resume
  of an order till A already took must be refused with the not-found toast,
  got 200:` (order restored). Restored → passes.
- Finding 1's regression test: fails on the Dev tree (message above),
  passes after the fix.

## Verdict

**Not safe to merge as delivered** (Finding 1: a money-relevant order-loss
regression in the everyday resume → re-park cycle). **Safe to merge after
the review fix in this branch's `review fix (#2712)` commit**, provided the
ADR Amendment B text is updated on its docs branch (Finding 3) before that
amendment is accepted.

## Deferred

- Backlog card: give-back double-fault residual (Finding 4).
- ADR-0093 Amendment B wording (Finding 3) — docs branch.
