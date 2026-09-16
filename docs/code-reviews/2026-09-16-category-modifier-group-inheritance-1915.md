# 2026-09-16 — Category-level modifier-group inheritance with per-item opt-out (ut-docs#1915)

**Design:** `ut-docs/adr/0094-category-modifier-group-inheritance-with-item-opt-out.md`
(extends ADR-0090). **Card:** ut-docs#1915, lane `lane:cloud-54`, part of the
2026-09-16 design-change batch (sequenced before ut-docs#2284, the category
editor UI that consumes this data layer).

## What shipped

- Migration `031_category_modifier_group_links.sql`: two additive link
  tables, `category_modifier_group_links` (category_id, group_id,
  sort_order) and `item_modifier_group_opt_outs` (item_id, group_id,
  presence-only), each with its three `sync_admin_version` triggers and one
  `group_id` index. No `ALTER`/`DROP` on any existing table.
- `internal/data/modifier_repo.go`: `ModifierGroup.OptedOut` field; a shared
  `attachOptions` helper factored out of the pre-existing option-loading
  logic (behaviour-preserving refactor); the sale-time resolver
  `ResolveGroupsForItem` (item's own active direct groups, then surviving
  category-inherited groups minus the item's opt-outs, deduped so a group
  both directly- and category-linked counts once, as the item's own copy);
  `ListInheritedGroupsForItem`/`inheritedGroupsForItem` (the admin-facing
  "inherited, greyed, can override" read); `ListGroupsForCategory`/
  `ListAllGroupsForCategory`; `LinkGroupToCategory`/`UnlinkGroupFromCategory`;
  `OptOutItemFromGroup`/`OptInItemToGroup`.
- `internal/data/sync_admin_repo.go`: two new `adminTables` entries so both
  link tables sync to satellite tills, ordered after their FK targets
  (`categories`, `items`, `item_modifier_groups`).
- Four sale-time call sites (`pos_modifiers_api.go` ×2, `self_order_shop.go`,
  `pos_api.go`) swapped from `ListGroupsForItem` to `ResolveGroupsForItem`.
  `ListGroupsForItem`/`ListAllGroupsForItem` themselves are unchanged and
  still serve the admin item-panel's narrower "direct links only" scope.
- `ItemIDsWithModifiers` (the tile-tap/scan-guard gate deciding whether the
  customization picker opens at all) extended to UNION in non-opted-out
  category-inherited groups — see "What the independent review found" below.
- 12 new tests in `internal/data/modifier_repo_category_test.go` plus a
  migration test (`TestMigration031_CreatesBothTablesAndIsReplaySafe`,
  `TestMigration031_IsOnDisk`).

**Out of scope, by design (ADR-0094 §5):** no template/handler/i18n change —
the category editor UI is ut-docs#2284, sequenced after this card. No change
to `UnlinkGroupFromItemUnlessLastLink`'s existing "≥1 item link" invariant
— a category is never a group's sole owner (ADR-0094 Decision 1).

## What the independent review found

Reviewed by a fresh Opus subagent (`isolation: "worktree"`, per the card's
`complexity:hard` routing — Fable built it, Opus reviewed it), briefed with
the full diff, ADR-0094, and this repo's `CLAUDE.md`. It built, vetted,
tested and lint/guard-checked the diff independently in its own worktree,
then did genuine TDD re-verification: reverted the opt-out filter and,
separately, the own-link dedup check in `ResolveGroupsForItem`, re-ran the
specific tests, confirmed both failed with real assertion mismatches (not
compile errors), and restored the file.

It found one real blocker: **`ItemIDsWithModifiers`** — the query behind
`HasModifiers`, which gates whether tapping a sale-screen tile (or a
barcode scan with no sellable variants) opens the customization picker at
all — still read only `item_modifier_group_links`, unchanged from before
this ADR. For an item with a category-inherited group and no direct link or
variant of its own, the tile would silently add straight to the basket at
base price; the inherited group — the entire point of this card — was never
offered. The reviewer reproduced this concretely with a throwaway
in-worktree test before reporting it, then removed the test.

**Fixed in this same commit**, not deferred: `ItemIDsWithModifiers` now
`UNION`s the existing direct-link query with a second clause reading
`category_modifier_group_links` joined through the item's `category_id`,
excluding any group the item has opted out of — the identical resolution
logic `ResolveGroupsForItem` already applies, so the tile-tap gate and the
sale-time resolver can no longer disagree. Added
`TestModifierRepo_ItemIDsWithModifiers_AgreesWithResolveGroupsForItem`
(confirmed it fails against the pre-fix query, then passes) and
`TestModifierRepo_ResolveGroupsForItem_DirectLinkSurvivesOptOutOfSameGroup`
(the ADR §2 edge case the reviewer flagged as untested: a group both
directly-linked and category-linked, opted out — the direct link must keep
it live). ADR-0094 amended with a new Consequences bullet naming this fifth
call site; `ut-docs/adr/README.md` also gained the missing ADR-0094 index
row (a separate reviewer nit).

Fixing this surfaced a second gap: three simplified, hand-rolled test
schemas elsewhere in the repo (`internal/ui/buttons_store_test.go`,
`internal/ui/resolver_querycount_test.go`,
`internal/testsupport/sqlite_catalog.go`) create `item_modifier_group_links`
directly rather than running real migrations, and the fixed
`ItemIDsWithModifiers` query now references the two new tables
unconditionally — `internal/ui`'s suite failed with "no such table:
category_modifier_group_links" until both empty tables were added to all
three fixtures. Fixed in the same commit; full `go test ./...` is green
with the fix in place.

## Verified beyond automated tests

- Full-repo `go test ./...` (not just the touched packages) — green, no
  regressions, before and after the `ItemIDsWithModifiers` fix.
- `gofmt -l .`, `go build ./...`, `go vet ./...` — clean.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
  `guard-migration-version-collision.sh` — all pass.
- `golangci-lint run` on every touched package — 0 issues.
- Backend-only change, no UI/template/visual surface touched — no
  screenshot/driven-run attestation applies (Tester skill's own carve-out).
- Scope discipline confirmed by the independent reviewer: exactly the 4
  (now 5, post-fix) intended call sites touched, no new i18n strings, no
  real client/shop name or credential-shaped literal anywhere in the diff.

## Deferred / follow-up

- `ut-docs#2284` — the category editor UI (colour, modifiers multi-select,
  kitchen-station routing), the sequenced consumer of this card's data
  layer.
- `ut-docs#2236` (`blocked:dep` on this card) — satellite staleness check
  needs to cover the two new link tables; label released at close-out.
- A group both directly-linked, category-linked, *and* opted-out was an
  untested interaction before this review — now covered (see above).

## Verdict

Safe to merge. One blocker found, fixed, and re-verified in this same
cycle; nothing deferred that changes the shipped behaviour's correctness.
