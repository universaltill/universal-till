# Modifier-group link sync coverage — ut-docs#2236

**Date:** 2026-09-18
**Card:** universaltill/ut-docs#2236 — "Check whether a multi-till satellite can
read a stale synced-down copy of an item's modifier-group links"
**Complexity:** easy

## What was asked

Filed by an independent reviewer on ut-docs#2210 as an unconfirmed hypothesis:
a multi-till satellite might read a stale synced-down replica of
`item_modifier_group_links`/`item_modifier_group_opt_outs` (and, per a later
comment, `category_modifier_group_links` too, added by ut-docs#1915) instead
of the primary till's live data. Not a known bug — filed so it wasn't
silently lost.

## Investigation

Code inspection (`internal/data/sync_admin_repo.go`) confirmed all three
tables are already present in `adminTables` — composite PK, no `is_active`
(so a revoked link hard-deletes on prune, correct for a pure link row),
correctly FK-ordered after `items`/`categories`/`item_modifier_groups`. They
sync via the same generic `DumpAdmin`/`ApplyAdmin` mechanism as every other
catalog table, on the existing 30-second replica poll
(`StartSyncPull`/`syncPullTick`, `internal/pages/sync_admin.go`). No caching
layer sits between `ModifierRepo`'s reads
(`ResolveGroupsForItem`/`ListInheritedGroupsForItem`,
`internal/data/modifier_repo.go`) and the local DB.

So by code inspection, there is no staleness bug beyond the normal ~30s sync
bound every `adminTables` entry already carries.

**The real gap**: no regression test proved this. `TestAdminDumpApplyRoundTrip_
ItemModifiers` (existing, `internal/data/sync_admin_repo_test.go`) only
round-trips `item_modifier_groups`/`item_modifier_options` — never the three
link tables in question, and critically never the DELETE/unlink direction for
any of them (the actual "stale copy" risk: a satellite still offering a
modifier group the primary revoked). Grepped every `_test.go` referencing the
three table names — none do a `DumpAdmin`/`ApplyAdmin` round trip; the
migration tests (025/031) only check their own triggers/backfill.

## What shipped

One new test, `TestAdminDumpApplyRoundTrip_ModifierGroupLinks`
(`internal/data/sync_admin_repo_test.go`), sibling to the existing
`TestAdminDumpApplyRoundTrip_ItemModifiers`. No production code changed —
the mechanism was already correct.

The test covers, in three rounds, against a real fully-migrated primary +
replica DB pair:

1. **Add direction** for all three tables: a direct item link (`grp1` via
   `CreateGroup`'s auto-link), a category link (`LinkGroupToCategory`), and
   an opt-out (`OptOutItemFromGroup`) — dumped from primary, applied to
   replica, confirmed present row-for-row, and confirmed the replica's own
   `ModifierRepo.ResolveGroupsForItem` (never written to directly — only via
   `ApplyAdmin`) resolves identically to the primary.
2. **Delete direction, part 1**: `UnlinkGroupFromItem` + `OptInItemToGroup`
   (removes the opt-out) on the primary — confirms both rows are actually
   gone on the replica after the next sync, not ghosted, and that the
   now-un-opted-out category-inherited group correctly surfaces.
3. **Delete direction, part 2**: `UnlinkGroupFromCategory` — confirms the
   category link row is gone on the replica and the item resolves to no
   groups at all, matching the primary.

## Independent review

A fresh-context Sonnet subagent (complexity:easy → Sonnet dev, Sonnet review
per `scrum-master`'s model-routing table), run in an isolated worktree with
no prior context, independently:

- Re-ran the full gate: `go build ./...`, `go vet ./...`, `gofmt -l .`,
  `go test ./internal/data/...` (full package, not just the new test),
  `go test ./internal/pages/...`, `bash scripts/ci/guard-data-access.sh` —
  all green.
- **Independently verified the test is not a tautology**, via its own
  mutation (not reusing the dev-side mutation): skipped
  `item_modifier_group_links` in `ApplyAdmin`'s phase-1 delete loop
  (mirroring the existing `settings`/`plugin_settings` skip pattern),
  confirmed the new test failed with an informative error
  (`a direct item-group link removed on the primary is still on the
  satellite`), reverted the change byte-exact (`git diff` empty afterward),
  confirmed the test passed again.
- Read `modifier_repo.go`'s documented resolution semantics
  (`own ∪ (inherited \ opted-out)`) and confirmed the test's assertions match
  them exactly, and that `CreateGroup`'s auto-link and the three tables' PKs
  match what the test asserts.
- Confirmed no disk I/O outside `t.TempDir()`, no real client/shop names, no
  secret-shaped literals, and that the change is genuinely backend/test-only
  (no `internal/pages` template, no locale file touched) — so the UX/i18n/
  manual checklist items are correctly out of scope.

**Verdict: safe to merge as-is. No blocking findings.**

## Verified beyond automated tests

The dev-side mutation testing (separately, before handing off to review) also
confirmed the **add-direction** assertion is non-tautological: temporarily
removing `item_modifier_group_opt_outs` from `adminTables` made the test fail
on "must appear in the admin dump", then passed again after reverting.
Combined with the reviewer's independent delete-direction mutation, both
directions of the regression guard are proven real.

## Deferred / out of scope

Nothing deferred. The card's own "out of scope" note (ut-docs#1903, open
orders/held-sales multi-till sync) is a separate, unrelated code path and was
not touched here.

## Outcome

No bug found. The sync mechanism for all three modifier-group link tables was
already correct; it now has a real regression test covering both the add and
delete/unlink directions, including the delete direction that was the actual
risk the filing reviewer flagged.
