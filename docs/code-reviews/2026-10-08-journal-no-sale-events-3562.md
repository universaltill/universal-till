# Review: journal replica no-sale opens to the main till (ut-docs#3562)

**Date:** 2026-10-08 · **Branch:** `fix/3562-journal-no-sale-events` · **Lane:** `lane:cloud-41`
**Built by:** Opus 5.5 · **Reviewed by:** Fable (independent, different model) · **Complexity:** medium

## What shipped

On a multi-till shop, "No sale" drawer opens (`no_sale_events`, migration
064) were only written on the till where the drawer opened. Only the main
till uploads the ADR-0111 sales aggregate, so `no_sale_count` and
`by_cashier.no_sale_opens` missed every open made on a secondary till.

- `POST /api/sync/no-sales` (new, `internal/pages/sync_no_sales.go`): the
  primary accepts a replica's own opens over the existing LAN sync D3
  bearer. Idempotent by id (`ON CONFLICT(id) DO NOTHING`). `till_id` is
  never read from the wire; it is the authenticated till's id, the same
  value `SetSaleProvenance` gives that replica's journaled sales, so the
  rollup counts the open under the same till key as its sales. Entries
  failing validation (id ≤64 bytes, RFC3339 `created_at`, reason ≤200
  runes, other ids ≤64 bytes) are rejected one by one, never the whole
  batch, so the replica's cursor can't wedge (ADR-0065 reasoning). One
  `no_sales_synced` audit row per batch that applied or rejected anything.
- Replica side: `syncPushTick` now runs the unchanged sales push, then
  `syncPushNoSales` with its own composite cursor
  `sync.no_sale_push_cursor = <created_at>|<id>`, so two opens in the same
  second are never lost. Advances only on 200; a successful push stamps
  `sync.last_push_at` / main-till contact like the sales push.
- `internal/db/replica.go`: joining a shop seeds the cursor to
  `<join time>|`, so the primary's own opens that came in the join
  snapshot are never pushed back as the replica's (found by Dev, not in
  the original design).
- Auth-middleware exemption, demo-mode deny entry, data-model doc,
  admin-bundle exclusion reason. Contract: ut-docs
  `reference/contracts/pos-lan-sync-journal.md` 1.10.0 (separate ut-docs PR).
- No migration, no UI, no locale keys.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Every non-200 logged at Error each 30 s; during the version-skew window (new replica, old primary → 401) that fills the Problems panel. | Fixed: `Warnf`, with the reason in a comment. |
| 2 | minor | Batch apply is per entry, not one transaction (same as `/api/sync/sales`): a DB error mid-batch → 422 after earlier entries committed; the retry then counts them as `skipped`, so the audit row under-reports `applied`. Data is correct. | Accepted, matches precedent. |
| 3 | minor | A no-sale-only push didn't stamp `sync.last_push_at` / main-till contact. | Fixed, test added (seen red first). |
| 4 | nit | No `http.MaxBytesReader`; 100-entry cap enforced after decode (same as `/api/sync/sales`). Bearer-authed LAN peer. | Accepted, matches precedent. |
| 5 | nit | Join seed uses the replica's clock; a primary clock ahead of it could have opens pushed back. The primary skips them by id unless it has since cleared history. | Accepted, same as `sync.push_cursor` one line above; documented in the contract changelog. |
| 6 | nit | Test hit counter incremented in the server goroutine without sync. | Fixed: `atomic.Int32`. |

Reviewer verified with no finding: composite cursor SQL and parsing;
the sales-push restructure is behaviour-identical (early returns, cursor,
`last_push_at`, `recordMainContact`); version skew both ways; peer-supplied
`till_id` ignored; a primary never pushes; promotion clears the new
`sync.*` cursor with the others; settings sync never carries `sync.*`;
reset-archive on either side; aggregate till key matches journaled sales.

## Verified beyond unit tests

- TDD re-verified by the reviewer in its own worktree: removing the
  `syncPushNoSales` call fails `TestSyncPushTick_NoSaleOpenReachesMainAggregateOnce`
  ("opens for the replica = {Total:0}, want 2"); removing the join seed fails
  `TestApplyReplicaIdentityStartsNoSalePushCursorAtJoin` (cursor `""`).
- The AC test is end to end: a replica DB with two same-second opens pushes
  through `syncPushTick` to an httptest server running the primary's real
  mux; the main till's `NoSaleOpensForTill` and `SalesAggregateForTill(...).NoSaleCount`
  are 2 under the replica's till id (0 under self/register keys) after
  each of two ticks.
- No UI surface touched — nothing to look at; no driven app run.
- `golangci-lint` could not run in the cloud container (binary built with
  go1.25 refuses the go1.27.1 module); CI runs it.

## Verdict

Safe to merge. Deferred: none (finding 2 and 4 match the sales journal and
would be fixed there together if ever).
