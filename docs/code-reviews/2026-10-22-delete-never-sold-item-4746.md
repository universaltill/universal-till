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

## Independent review (Opus)

Reviewer: Claude Opus 5.5, separate session, on the PR head `700c7cd37`.
Read the whole delete path (`deleteUnusedItemTx`, `obsoleteItemsPredicate`,
the catalogue-pull tombstone, `cloudDeleteItem`), the shrinkage and
age-verification schemas (035, 056, 057, 063), the shrinkage reports and
`RestoreResetBatch`, and the cloudsync loop's kick handling.

Findings:

1. **High, fixed. A refused delete inside the caller's transaction left the
   voids detached.** `obsoleteItemsPredicate` also keeps an item with an
   `age_verifications` row (live or archived); the explicit checks did not.
   So for a never-sold age-restricted item with a void and an ID check, the
   voids were detached and the zero movements deleted, then the final
   `DELETE` refused. `DeleteUnusedItem` rolled that back, but
   `DeleteUnusedItemTx` runs in the catalogue pull's transaction, which
   deactivates the item and **commits**: the voids stayed detached from an
   item that still exists. Same path also showed the misnamed "it has stock
   history". Fix: an explicit, named `age_check` refusal ("its ID checks
   are on record — deactivate it instead") before any write, and every
   write now runs inside `SAVEPOINT delete_unused_item`, rolled back on any
   later refusal, so a future predicate clause cannot repeat this.
   Test: `TestDeleteUnusedItemTx_AgeCheckRefusesNamedAndLeavesVoidsAttached`
   (failed on the PR head with "attached voids = 0").
2. **Medium, fixed. An edit storm ran check-ins back to back.** Every
   catalogue edit changes the snapshot hash, so each kicked check-in
   rebuilt and pushed the whole catalogue; the capacity-1 channel only
   coalesces kicks that arrive during one check-in. A manager editing for
   half an hour would push the full catalogue every few seconds instead of
   every ~2 min. Fix: `catalogKicker` — the first edit kicks at once, later
   ones within `catalogKickGap` (10 s) share one trailing kick
   (`time.AfterFunc`, no goroutine of ours, nothing to leak on shutdown;
   send never blocks). Test:
   `TestCatalogKickWrap_StormIsThrottledWithATrailingKick` (failed first),
   run with `-race -count=3`.
3. **Low, fixed. The tombstone path dropped the void count from its
   audit.** A cloud tombstone deletes through the same rule but its audit
   row did not say "item deleted with N voids". Now it does
   (`TestPullTombstoneOfVoidOnlyItemAuditsTheVoids_4746`, failed first).
4. **Medium (CI blocker), fixed. `guard-help-drift` failed on the PR
   head**: de/tr got a new bullet while English appended a sentence to the
   cleanup bullet, so the de/tr baseline entries went stale. English now
   has the same bold-led bullet; the baseline records de/tr shifted in
   lockstep and ar/fa one bullet further behind. The writes' move into
   `deleteUnusedItemWrites` is recorded in `guard-price-history-sync.sh`'s
   allow-list (same writer, same callers).
5. **Low, fixed.** The archived-shrinkage refusal said "its voids" for
   archived comps and wastes too; now "its voids, comps or wastes".
   Help topics (en/de/tr) list the ID-check refusal.

Checked and fine:
- Atomicity / race: checks and writes share one transaction, and the DSN
  sets `_txlock=immediate` (`internal/db/db.go`), so the write lock is held
  from `BEGIN`: no sale can commit between the checks and the delete.
- `shrinkage_events.item_id` is nullable with no cascade (035); NULL
  satisfies the FK. The Shrinkage report groups by `reason_category` and
  `item_name`, so totals and the top-items list are unchanged. Archived
  rows with a NULL `item_id` restore fine (the FK accepts NULL); archived
  rows still naming the item keep refusing (`archived_shrinkage`), so
  `ErrArchiveReferencesRemoved` cannot be caused by this change.
  Z-reports do not read `shrinkage_events`.
- Variants: `shrinkage_events` has no `variant_id`; variant voids carry the
  parent `item_id` and are detached with it. Variant zero movements are
  deleted; variant sales/real movements refuse as before.
- Kick wrapper: GET/HEAD and unknown routes pass through untouched;
  WebSocket/SSE are GETs, so the missing `http.Flusher` on the status
  writer cannot bite (`Unwrap` is there for `ResponseController`).
  Checkout never waits on it.
- `go vet` clean on `internal/data`, `internal/pages`, `internal/catalogpull`.

Owner decision to confirm (not changed here): the owner wrote "a void or
shrinkage record on a never-sold item does not block". The PR keeps comps
and wastes blocking. Recommendation: **follow the owner's wording** —
detach comp/waste rows too and count each kind in the audit ("item deleted
with 1 void, 2 wastes"). They do not move stock (waste writes no
`stock_movements`), the reports read `item_name`, so nothing is lost; a
real stock loss still blocks through its non-zero movement. Kept as is
until the owner confirms, since it widens what a delete removes. ID checks
are now also a named refusal; same question applies if the owner wants a
refused-at-ID-check test item to be deletable.

Unverified: `golangci-lint` (local binary built with Go 1.26); no on-device
run of the kick latency.

Verdict: safe to merge once CI is green.
