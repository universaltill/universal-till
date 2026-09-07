# Catalog modifier groups/options: admin-sync + primary-only gate (ut-docs#1667)

## What shipped

`item_modifier_groups`/`item_modifier_options` (per-item modifier groups
like "Toppings" and their options like "Extra cheese") were catalog
structure that read shop-wide but sat unsynced with no primary-only gate —
the exact ut-docs#1546 shape (`tables`/`kitchen_stations`), flagged as a
genuinely open question by ut-docs#1586's schema-drift guard rather than
guessed into either list.

Two changes, `internal/data/sync_admin_repo.go`:
- Both tables moved from `nonAdminTables` into `adminTables`
  (`hasIsActive: true`, ordered after `items` — `item_modifier_groups` FKs
  onto `items(id)`, `item_modifier_options` FKs onto
  `item_modifier_groups(id)`, both `ON DELETE CASCADE`).
- `logSatelliteDivergencePrune`'s allowlist (previously hardcoded to
  `registers`/`stock_locations`, ut-docs#1590) generalized to a
  `divergencePruneTables map[string]string` naming table → originating
  card, now also covering both modifier tables: a shop with a
  satellite-created modifier from before this fix will have it silently
  pruned on its first post-upgrade pull, and that's worth a shop owner's
  attention the same way the registers/stock_locations case already was.

`internal/pages/catalog/handlers.go`: a `requirePrimary` closure (mirroring
`registers_page.go`'s) gates both `/api/catalog/modifier-group` and
`/api/catalog/modifier-option` POST handlers — refused via
`common.LocalizedError` (409, `catalog.error.replica_use_primary`) as the
first statement after the method check, before `ParseForm` or any DB read,
covering both the create and update branches of each handler (both branch
on the same `id`/`groupId`-presence check downstream).

i18n key added to all four locales (en/fa/ar/tr), phrased to match the
already-shipped UI panel title (`catalog.modifiers.title`, "Customization
options" / "خيارات التخصيص" / "گزینه‌های سفارشی‌سازی" / "Özelleştirme
seçenekleri") and the sentence template every sibling
`*.error.replica_use_primary` key already uses (registers/locations/
tables/kitchen-stations/fiscal-register). `web/help/{en,fa,ar,tr}/catalog.md`
gained a `## Good to know` bullet (the heading convention already used by
`tables.md`/`kitchen-stations.md`; `catalog.md` had none yet) — screenshots
regenerated via `make docs-shots` (100/100 Playwright specs; only 2 PNGs
differ, both 1-2 bytes of encoder noise, unrelated topics that regenerate
in lockstep with the hashed surface).

**Non-goals, deliberately out of scope:** `ModifierRepo.DeleteGroup`/
`DeleteOption` hard-delete but are wired to no handler today (grepped,
confirmed by the independent review too) — not gated, and the `adminTables`
comment now says so explicitly so a future PR wiring one up knows to gate
it. `vouchers`/`country_settings`/`price_history` are separate open
questions (#1668/#1669/#1671), untouched here.

## Independent review

Opus, isolated worktree (`isolation: "worktree"`, per the ut-docs#386
mitigation — the revert-then-restore TDD verification below never touched
this session's own checkout).

**Verdict: yes-with-fixes-required (2 should-fix, no blockers) — both
fixed, then the full gate re-run clean.**

Commands the reviewer actually ran (and this session re-ran after fixing
the findings): `gofmt -l .` (clean), `go build ./...`, `go vet ./...`,
`golangci-lint run ./...` (0 issues), the full `go test ./...` (all `ok`
— `internal/data` ~32s, `internal/pages` ~73s, `internal/pages/catalog`
~1.5-1.8s), `guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`,
`guard-data-access.sh`, `guard-compliance-claims.sh`, plus
`guard-kiosk-engine.sh`/`guard-page-http-error.sh`/
`guard-plugin-menu-read.sh`/`guard-htmx-loaded.sh`/
`guard-autofill-suppression.sh`/`check-brand-assets.sh` — all clean.
(`guard-deadcode-baseline.sh` couldn't run in the reviewer's environment —
`gtk+-3.0` missing from pkg-config, an environmental gap unrelated to this
diff; it fails identically on unmodified `main`.)

**FK ordering independently verified against `001_init.sql`, not taken on
the comment's word:** `item_modifier_groups.item_id → items(id) ON DELETE
CASCADE`, `item_modifier_options.group_id → item_modifier_groups(id) ON
DELETE CASCADE`, `foreign_keys` is on in production
(`internal/db/db.go`). Placement after `items`, groups-before-options, is
correct for `ApplyAdmin` phase 2 (forward upserts); phase 1 (reverse
deletes) therefore deletes options → groups before `items`, so a primary
deleting an item whose `items` delete is later FK-blocked by sale history
(retire-in-place) never leaves an orphaned modifier behind — the reviewer
traced this exact case. Nothing FKs *onto* either table
(`sale_line_modifiers.group_id`/`option_id` are bare `TEXT`, no
constraint), and receipts read `option_name_snapshot` directly rather than
joining, so pruning a modifier never damages a historical receipt.

**TDD claim independently re-verified, not just eyeballed:** the reviewer
reverted only the production code (both `adminTables` entries back to
`nonAdminTables`, the `requirePrimary` closure and its two call sites
removed) with both new tests left untouched, and confirmed
`TestAdminDumpApplyRoundTrip_ItemModifiers` and
`TestCatalogModifiersPanel_MutationsRefusedOnReplica` both failed with the
exact claimed, on-topic errors — then restored and confirmed both passed
again, atomically within one turn. Also independently confirmed
`TestSchemaTablesAreClassified` passes in both states (proving the
`nonAdminTables` removals are safe either way).

**Error-response shape checked and found correct, not a defect:**
`common.LocalizedError` → plain `http.Error(w, T(key), 409)`. HTMX won't
swap a non-2xx, but `catalog.html`'s `htmx:responseError` listener renders
`xhr.responseText` into the page's notice area — the same channel this
exact handler file's existing `catalog.error.invalid_request` 400s already
use, so this is consistent with established behavior in this file, not a
new pattern.

**Translations independently checked as real, not machine output:** all
three non-English strings are structurally identical to their
`registers.`/`tables.` siblings with only the noun swapped, and the
swapped noun is a byte-for-byte match for the already-shipped panel title
in each locale.

### Findings — both should-fix, fixed before merge

1. **should-fix — `logSatelliteDivergencePrune`'s allowlist wasn't
   extended to the two new tables.** A shop with a satellite-created
   modifier from before this fix (nothing stopped that pre-fix, and it
   genuinely worked — `ListGroupsForItem` reads locally, the picker
   renders it, `sale_line_modifiers` snapshots it at checkout) would have
   it silently hard-deleted on the first post-upgrade pull with zero
   signal anywhere — arguably worse than the registers/stock_locations
   precedent this pattern came from, since an unsynced `tables` row was
   largely unusable pre-fix while a satellite-local modifier was fully
   functional. **Fixed**: `divergencePruneTables` now maps table name →
   originating card (`registers`/`stock_locations` → ut-docs#1590,
   `item_modifier_groups`/`item_modifier_options` → ut-docs#1667), and a
   new regression test
   (`TestAdminApply_ItemModifierGroupHardDeletedPreExistingLogsWarning`,
   using the same `warnfContaining` helper ut-docs#1592 introduced) proves
   the warning actually fires. Confirmed only the hard-delete case is
   reachable for these two tables (nothing FKs onto them with a real
   constraint, so the retire-in-place branch never triggers) — matches
   `TestAdminApply_RegisterHardDeletedPreExistingLogsWarning`'s shape, not
   `TestAdminApply_RegisterRetiredInPlaceWhenFKBlockedBySatelliteShiftHistory`'s.
2. **should-fix — the replica-refusal test covered only the CREATE
   branch, not UPDATE, for either handler.** Both handlers branch on
   `id`/`optionID` presence at the exact same point `requirePrimary` gates
   ahead of, so the gate itself was never at risk — but the test would
   have stayed green through a future refactor that moved the check inside
   the create-only branch (an easy mistake: "editing an already-synced row
   is fine"), silently reopening exactly the bug this card exists to
   prevent, and on the *more* likely real replica action (editing an
   existing modifier, not creating a brand-new one). **Fixed**: extended
   `TestCatalogModifiersPanel_MutationsRefusedOnReplica` to also POST an
   update against a pre-existing group and a pre-existing option (mirroring
   `TestRegistersPage_MutationsRefusedOnReplica`'s rename/deactivate
   coverage of a pre-existing register), and added a response-body
   assertion for the localized message text on all four requests, not just
   the status code.

### Nits, addressed opportunistically (not required for merge)

- `sync_admin_repo.go`'s comment on the two new `adminTables` entries now
  says explicitly that `DeleteGroup`/`DeleteOption` hard-delete but are
  wired to no handler today, so a future PR wiring one up knows to gate it
  too (raised as a nit since leaving them ungated is itself correct today —
  grepped, confirmed unreachable from any handler/CLI/seed/plugin path).
- The new test's inline `settings` table now cites and matches this exact
  package's own precedent
  (`TestCatalogReplicaBannerNeverLinksAcrossDevices` in
  `handlers_test.go`) instead of a different package's.

### Nits/observations deliberately NOT addressed here

- The refusal message renders into `#item-form-msg` while the modifier
  forms live in a different card (`#catalog-variants`) — pre-existing for
  every error path in this handler file, not introduced by this diff; a
  UX follow-up, not a correctness gap.
- **Out of scope, filed as ut-docs#1689**: every other catalog mutation
  handler already in `adminTables` (`items`/`item_variants`/
  `item_barcodes`/`variant_barcodes`) has no `requirePrimary` gate at all
  — this diff doesn't make the catalog page any riskier than it already
  was, but the "sync is safe because mutation is gated" invariant this
  card's own comment states is not one the file actually holds elsewhere.
  Worth a dedicated card in the #1590 family rather than silently
  expanding this one's scope.

## Verified beyond automated tests

- `internal/db/migrations/001_init.sql` read directly (not inferred from
  comments) for both tables' real FK targets and `ON DELETE` behavior.
- Grepped every caller of `ModifierRepo.DeleteGroup`/`DeleteOption`
  (test-only) before treating "not wired to any handler" as a safe reason
  to leave them ungated.
- `make docs-shots` run for real (100/100 Playwright specs) rather than
  assumed unnecessary because "only text changed" — confirms the manual
  screenshots are genuinely fresh, not just that the guard was silenced.

## Safe to merge

Yes. No open findings; full gate green; TDD claims independently
re-verified by a different model in an isolated worktree.
