# Review: delete a never-sold item that only has voids (ut-docs#4746)

Scope: `internal/data/item_delete_repo.go`, `internal/pages/cloudsync_catalog_wire.go`,
`internal/pages/catalog_kick.go` (+ `init.go` wiring), help topic `catalog` (en/de/tr).
Companion PRs: ut-cloud (closes a failed delete_item once the item is gone),
ut-my-shop (Deactivate on a refused delete in the changes list).

Owner decision (card comment, 2026-10-22): an item that was never sold is
deletable even if only a void is recorded; real history still refuses with a
named reason; archived-batch restore stays safe.

## What changed
- `DeleteUnusedItem[Tx]`: live `shrinkage_events` rows with reason `void` are
  detached (`item_id = NULL`, `item_name` kept, so the Shrinkage report keeps
  its totals); live zero-quantity stock movements are removed; the result
  carries `Voids`. The cloud hook writes the audit row `cloud_item_deleted`
  with `voids` and the note `item deleted with N voids`.
- Still refused, each with its own reason: sold (unchanged), a non-zero or any
  archived stock movement (`stock_history`), a live comp/waste
  (`shrinkage`, "it was comped or wasted N times"), any
  `shrinkage_events_archive` row (`archived_shrinkage`: a restorable reset
  needs the item, ErrArchiveReferencesRemoved), quick button, parked sale,
  open vouchers. Refusals run before any write, so a refusal changes nothing.
- Till-side promptness: `catalogKickWrap` kicks the cloud sync loop
  (`CloudSyncNow`, the cloud link's own capacity-1 coalescing channel) after
  any successful request that `catalogsync.Routes` lists, so a catalogue edit
  on the till is uploaded in seconds instead of at the next 2-minute tick.

## Findings and decisions
- Detach vs remove for voids: detach keeps the Shrinkage report whole and is
  the smaller loss of audit data. `item_id` is nullable (migration 035), no
  migration needed.
- Comp/waste are NOT detached. The owner's wording was "void or shrinkage";
  only void is clearly "never sold". A comp gives goods away and a waste is a
  stock loss, both real records. Flagged on the PR for the owner.
- Catalog cleanup (`obsoleteItemsPredicate`, bulk) is deliberately unchanged:
  it still keeps voided items. The owner's decision is about deleting one item.
- The cloud catalogue pull tombstone (`DeleteUnusedItemTx`) now also purges
  voids for a never-sold item; the same rule, one code path.
- The kick wrapper only reads method + path (`Resolve` with no form), never
  the body. A satellite till's channel is never drained; the send is
  non-blocking so that is harmless.

## TDD
`item_delete_voids_test.go` (void-only deletes, named refusals, sold+void
refuses and detaches nothing), `TestCloudDeleteItem_VoidOnly...` and
`catalog_kick_test.go` were written first and failed (build errors on the new
symbols, then the old reason in `shrinkage_reset_test.go`, updated to the new
archived reason). All pass.

## Not verified here
`golangci-lint` could not run locally (binary built with Go 1.26, repo
targets 1.27.1); CI runs it. Independent Opus review is run by the parent.
