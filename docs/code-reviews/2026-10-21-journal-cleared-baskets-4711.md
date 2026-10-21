# Review: journal a replica's cleared baskets to the main till (ut-docs#4711)

**Date:** 2026-10-21 · **Branch:** `fix/4711-journal-cleared-baskets` · **Lane:** `lane:cloud-41`
**Built by:** Sonnet 5.5 · **Reviewed by:** Opus 5.5 (independent, fresh context) · **Complexity:** medium

## What shipped

Since ut-docs#1122 the Z report prints CLEARED BEFORE PAYMENT from
`cleared_baskets` (migration 086), but a replica's rows stayed on the
replica, so on a multi-till shop the main till's report missed them
(#1122 review finding 1). This mirrors the no-sale journal (ut-docs#3562):

- `POST /api/sync/cleared-baskets` (new, `internal/pages/sync_cleared_baskets.go`):
  the primary accepts a replica's own clears over the LAN sync D3 bearer.
  Idempotent by id; `till_id` is the authenticated till's id, never read
  from the wire. Per-entry validation (id/register/actor ≤64 bytes, RFC3339
  `created_at`, `line_count` 0–10000, `total_minor` ≥0 — basket totals are
  floored at zero in `pos.computeTotals`); an invalid entry is rejected
  alone, a DB error 422s the batch. Audit `cleared_baskets_synced`.
- Replica: `syncPushTick` runs sales → no-sales → cleared baskets; composite
  cursor `sync.cleared_basket_push_cursor` (`<created_at>|<id>`), advanced
  only on 200; a push stamps `sync.last_push_at` / main-till contact.
- `internal/db/replica.go`: joining seeds the cursor to `<join time>|`, so
  the primary's own rows that arrive in the join snapshot are never pushed
  back. Promotion already clears every `sync.*` key.
- `data.LocalClearedBasketsSince`, `data.ApplyJournaledClearedBasket`;
  auth-exempt route, demo-mode deny entry, admin-bundle exclusion reason,
  `docs/data-model.md`, `web/help/en/reports.md` (one sentence).
- Contract: ut-docs `reference/contracts/pos-lan-sync-journal.md` 1.12.0
  (separate ut-docs PR). No migration, no UI, no locale keys.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A replica joined before this change has no cursor and pushes its whole history once, including rows inherited from the primary's snapshot; skipped by id unless the primary has reset its transaction history since, then re-counted under the replica. | Accepted, as #3562 finding 5; migration 086 is in no release tag yet, so no installed replica holds inherited rows. Documented in the contract changelog. |
| 2 | nit | String-compare cursor; a replica clock stepping back can leave rows behind it. | Accepted, same as `sync.push_cursor` and the no-sale cursor; all rows are second-precision UTC `Z`. |
| 3 | nit | A non-integer `line_count`/`total_minor` fails the whole decode (400). | Accepted, matches `/api/sync/no-sales` and `/api/sync/sales`; our replica always sends integers. |
| 4 | nit | `syncPushTick` doc comment overlong; "neither runs" with three steps. | Fixed. |

Reviewer verified with no finding: till_id only from the bearer (spoofed
field ignored), idempotency incl. in-batch duplicates, the primary never
pushes, push-tick order and early returns unchanged, version skew both
ways, re-join, reset/restore on the replica, no double count on the
replica's own Z report or the ADR-0111 aggregate (which doesn't read
cleared baskets), SQL only in `internal/data`, Money at the boundary.

## Verified beyond unit tests

- TDD re-verified by the reviewer in its own worktree: removing the
  `syncPushClearedBaskets` call fails
  `TestSyncPushTick_ClearedBasketReachesMainZReportOnce` ("main Z report
  cleared = 0 / 0, want 2 / 800"); removing the join seed fails
  `TestApplyReplicaIdentityStartsClearedBasketPushCursorAtJoin`; passing an
  empty TillID fails `TestSyncClearedBasketsAPI_AppliesSkipsRejectsAndPinsTill`
  (422).
- The end-to-end test drives `syncPushTick` against the primary's real mux
  and checks `EndOfDay` over two ticks (same-second rows, no sales queued).
- No visual surface changed (backend + one help sentence); no screenshots.

## Verdict

Safe to merge.

## Deferred

- ADR-0111 aggregate carrying cleared baskets to ut-cloud: ut-docs#4721.
