# 2026-09-10 — Modifier groups shareable across items (ut-docs#2013 / ADR-0090)

**Change:** `internal/db/migrations/025_modifier_group_links.sql` (new) +
`ModifierRepo` reworked to go through the new link table + three re-anchor
call sites (`POSRepo.CleanupObsoleteItems`, `DemoSeedRepo.RemoveDemoItem`,
`internal/data/seeddata/remove_demo.sql`/`remove_demo_relaxed.sql`) +
`sync_admin_repo.go`'s declarative sync-table list + test-fixture updates
across several packages. Design: ADR-0090
(`ut-docs/adr/0090-modifier-groups-shared-across-items-via-link-table.md`).

**Dev:** Fable subagent, isolated worktree, per this card's `complexity:hard`
routing. **Reviewer:** independent Opus subagent, fresh context, isolated
worktree.

## What shipped

- `item_modifier_group_links (item_id, group_id, sort_order)` — additive,
  mirrors `option_sets`/`item_option_sets` exactly. Backfilled 1:1 from
  every existing group's current `item_id`/`sort_order`, so no existing
  shop sees any visible change.
- `item_modifier_groups.item_id` and its `ON DELETE CASCADE` stay
  untouched in the schema (the documented parent-table-rebuild trap rules
  out removing it in a migration) — now a legacy "anchor" the app actively
  re-points to a surviving linked item before any of the three real
  `DELETE FROM items` hard-delete paths can fire its cascade on a still-
  shared group.
- `ModifierRepo.listGroupsForItem`/`listShopModifierGroups` read through
  the link table; `ItemIDsWithModifiers` too. New `LinkGroupToItem`/
  `UnlinkGroupFromItem` give the deliberately-deferred "attach existing
  group" UI card a ready data layer. `CreateGroup` writes the group + its
  first link atomically.
- `sync_admin_repo.go` gains one declarative entry (composite PK, no
  `hasIsActive`, same shape as `item_option_sets`), with its three
  `sync_admin_version` bump triggers shipped in the same migration.

## Independent review — findings

**0 blocking.** Every claim in the diff's own comments was independently
reproduced by the reviewer, not taken on faith:

- **The fan-out bug the diff fixes is real**: `listShopModifierGroups`'
  pre-change `map[string]*ModifierGroup` (keyed by group id alone) would
  silently attach a shared group's options to only the LAST item's copy of
  it. Reviewer reverted the `byGroupID map[string][]int` fan-out, re-ran
  `TestModifierRepo_ListShopModifierGroups_SharedGroupCarriesFullOptionsUnderEveryItem`,
  confirmed it fails with exactly the predicted "0 options, want 2" error,
  restored, confirmed it passes.
- **Migration 025's replay-safety is real**: reviewer ran
  `TestMigration025_*` and `TestFiscalSigningKeys*` (the replay-sensitive
  suite the migration's own comment cites) directly — all pass.
- **All three re-anchor paths are load-bearing, not decorative**: reviewer
  stripped each of the three (`POSRepo.CleanupObsoleteItems`'s Go call,
  `RemoveDemoItem`'s Go call, and the inlined `UPDATE` in both
  `remove_demo.sql`/`remove_demo_relaxed.sql`) one at a time and confirmed
  each corresponding new test fails with "shared group was
  destroyed/cascade-deleted" — then restored and confirmed all pass.
- **The satellite-sync path was checked too** (not in the original brief):
  reviewer wrote and ran a throwaway test simulating a primary re-anchor +
  delete followed by a satellite `ApplyAdmin` pull, confirmed the group/
  link/options converge correctly on the satellite (phase 2 upserts the
  full snapshot in FK order inside one transaction) — not a gap.
- Full gate independently re-run on a pristine worktree: `gofmt`/`go
  build`/`go vet`/`go test ./...` (full suite) / `golangci-lint` all clean;
  24 of 26 CI-blocking guards pass (the 2 failures are `shellcheck`-binary-
  missing-from-this-environment, unrelated to this diff — it touches no
  `.sh` file).

**1 non-blocking, fixed in this commit.** `UpdateGroup`'s `sortOrder`
parameter wrote `item_modifier_groups.sort_order` — a column no read path
consults any more (both list queries now read the link row's own
`sort_order`). Not a live bug: no template in `web/ui` submits a
`sortOrder` field to this call today, so it's currently a harmless no-op —
but it's a trap for the deferred "attach existing group" UI card, which
will want per-item ordering (exactly what `LinkGroupToItem`'s `ON CONFLICT
DO UPDATE SET sort_order` already provides). Fixed by documenting it on
`UpdateGroup` itself, pointing future callers at `LinkGroupToItem` instead
of widening this PR with a signature change.

**2 minor, one fixed.** `DeleteGroup`'s doc comment didn't carry ADR-0090
§2's warning that deleting a group now removes it from *every* linked item,
not just one — fixed, one sentence, also pointing at `UnlinkGroupFromItem`
as the narrower alternative. `ReanchorGroupsBeforeBulkItemDelete`'s
string-interpolated subquery parameter was flagged as an interface that
invites a `fmt.Sprintf` caller later, even though today's sole caller
passes a compile-time constant with no injection risk (same pattern as the
pre-existing `itemSet`/`variantSet` composition it sits next to) — accepted
as-is, no code change; noted here for whoever adds a second caller.

## What was verified beyond automated tests

Both the ADR (document-first, reviewed separately —
`ut-docs/code-reviews/2026-09-10-adr-0090-shared-modifier-groups.md`) and
this implementation were checked against the real, running codebase rather
than each other's prose: every `DELETE FROM items` call site was grepped
independently by both the ADR reviewer and this implementation's reviewer
(agreeing on exactly three), and the implementation reviewer additionally
proved — by deliberately breaking each mechanism and watching the
corresponding test fail — that the fan-out fix, the migration's replay
safety, and all three re-anchor call sites are genuinely load-bearing, not
just present.

## Explicitly deferred (per ADR-0090 §5, not a gap in this card)

The UI to actually attach an existing modifier group to a second item —
filed as a follow-up Backlog card at this card's close-out. Until that
UI exists, this change is invisible to every shop: the schema/data layer
supports sharing, but no create path offers it yet, and the migration
backfill guarantees zero behavior change for every existing group.

## Verdict

**Safe to merge.**
