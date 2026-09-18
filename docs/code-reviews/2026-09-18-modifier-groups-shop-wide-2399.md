# 2026-09-18 — Modifier groups become shop-wide entities (ut-docs#2399, ADR-0101)

**Card:** universaltill/ut-docs#2399 — product-owner report, verbatim: *"a
modifire can obly create with assosiate with a catalog. they should be
dedicated modifires and define generally in modifires page, then we can
assign them to the categories or catalogs by selecting them."*
**Design:** ADR-0101 (ut-docs PR #2400) — supersedes ADR-0090 Decision 2.
**Branch:** `feat/2399-modifier-groups-shop-wide`. **Complexity:** hard —
Dev at Fable, independent review at Opus, per the model-routing rule.

## What shipped

- **Migration `034_modifier_groups_drop_anchor.sql`** rebuilds
  `item_modifier_groups` without `item_id`, keeping the table name. Order:
  new parent + copy → each of the four leaf children
  (`item_modifier_options`, `item_modifier_group_links`,
  `category_modifier_group_links`, `item_modifier_group_opt_outs`) rebuilt
  pointing at the replacement (018's pattern) → drop the now-childless old
  parent → rename. Every dropped index and all 15 `sync_admin_version`
  triggers recreated. Checksum pinned in `shipped_migrations_test.go`.
- **`internal/db` test seam:** `Open` = `openRaw` + `migrate()`;
  `migrateUpTo(N)` lets a test build a *genuine* version-N database.
  `openAtPreMigrationSchema` (fiscal 009/011, 025 tests) and the two
  ADR-0100 population tests in `migration_drift_test.go` now build the
  real pre-N shape instead of rewinding a fully-migrated file — required,
  because 025's frozen backfill reads `item_id` and cannot replay after
  034.
- **`ModifierRepo`:** `CreateGroup` takes no item; new
  `ListAllModifierGroupsWithAssignments` (4 queries, joined in Go) and
  `NextGroupSortOrderForCategory`; removed `UnlinkGroupFromItemUnlessLastLink`,
  `GroupLinkCount`, both `Reanchor*`, the per-link `ListShopModifierGroups`
  fan-out, and `ItemID`/`ItemName` from `ModifierGroup`. Re-anchor steps
  gone from `CleanupObsoleteItems`, `RemoveDemoItem`, `remove_demo*.sql`.
- **`/modifiers`:** create card without an item picker; one card per group
  (rename/rules/active, option CRUD, **Assigned to**: category checkbox
  pills posting `attach-category`/`detach-category`, item chips with
  remove, datalist "Add item" → `attach`; unassigned hint), **Delete
  group** in a card footer with a confirm. `modifier_group_admin.html` is
  now the item-editor's attach-only partial. New handlers `attach-category`,
  `detach-category`, `delete` — all `requireCatalogManagement` +
  `requirePrimary`, answering through `renderModifierMutationResult`.
  Detach always allowed.
- **Cloud directive** `upsert_modifier_group`: `item_id` optional (blank →
  standalone group, shop-wide name-keyed idempotency; present → create +
  link, rollback on failure).
- **i18n:** 10 new `modifiers.*` keys + reworded `modifiers.subtitle` and
  `modifiers.empty` in en/fa/ar/tr; two dead keys removed. Pack follow-ups:
  `ut-plugin-language-de` v1.1.91, `-es` v1.1.82 (also fix `modifiers.empty`,
  which still told merchants to create a group from an item's catalog entry).
- **Help:** `web/help/{en,de,ar,fa,tr}/catalog.md` Modifiers paragraph;
  docs-shots regenerated.
- **Tests:** `migration_034_…_test.go` (real 001..033 DB with `item_id`,
  row survival, FK targets, cascade, replay, triggers), repo/handler tests
  (`modifiers_shop_wide_2399_test.go`, replica refusal on all new
  endpoints), cloudsync standalone-create test, e2e
  `modifiers-shop-wide-2399.spec.ts`.

## Independent review (Opus, isolated worktree at the WIP snapshot)

Ran build/vet, the affected packages' tests, four guards, and **two
mutation experiments on the migration**: (1) dropping the parent before
re-pointing the children → the 034 test fails on *silent data loss*
(`options = [], want [{o1…}{o2…}]`); (2) dropping the parent right after
the copy → `no such table` on all three 034 tests + the checksum pin.
Verified the `db.go` seam leaves production `Open` unchanged (DSN,
pragmas, lineage, `acceptedPriorChecksums`, `ErrDatabasePredatesReset`),
XSS-safety of `ItemsJSON` (`json.Marshal` escapes `<`), htmx re-execution
of the trailing inline script on every `outerHTML` swap, gate parity on
the new endpoints, and that no schema object outside the five tables
referenced the old parent.

Verdict: **safe to merge** — no blocking findings. Triage:

| # | Finding | Outcome |
|---|---|---|
| M1 | "Add item" with a name that resolves to no item posted an empty `itemId`; the handler answered `text/plain` 400, which htmx never swaps in → silent no-op | **Fixed**: client-side `setCustomValidity` (localized via `data-pick-msg`) + the handler routes the case through the Notice re-render with `modifiers.pick_item` |
| M2 | Standalone cloud create dedupes shop-wide by name — a different group with the same name is swallowed as "already exists" | **Recorded** in ADR-0101 §4 (accepted at-least-once trade, same as `upsert_category`); portal side tracked on #2401 |
| L1 | `catalog.modifiers.detach_last_error` / `catalog.modifiers.none` dead in all locales | **Fixed** (core + de/es packs) |
| L2 | Stale `sync_admin_repo.go` comment (FK onto items; `DeleteGroup` unwired) | **Fixed** |
| L3 | Test name still claimed `CreateGroup` writes a link row | **Renamed** `TestModifierRepo_CreateThenLink_WritesLinkRow` |
| L4 | ADR-0100 population tests rewound a fully-migrated file, so they replayed 034 rather than the real v0.18/v0.19 upgrade | **Fixed** — both now build through `openMigratedTo(t, path, 32)` |
| L5 | Item clean-up now leaves single-use groups as unassigned cards; no bulk delete | **Filed** universaltill/ut-docs#2406 (Backlog) |
| L6 | `delete` of an unknown id is a 200 no-op | Accepted — idempotent, harmless |
| L7 | `ORDER BY g.name` binary collation | **Fixed** — `COLLATE NOCASE` |
| L8 | Phantom `--accent-tint` token | **Fixed** — `--success-tint` |

## Verified beyond automated tests (Tester, local session)

- Full `go test ./...` (60 packages) green twice — before and after the
  review fixes; `go build`/`go vet`/`gofmt` clean; guards `i18n`,
  `data-access`, `help-topics`, `help-drift`, `docs-shots`,
  `migration-version-collision`, `htmx-loaded`, `page-http-error` pass
  (`guard-deadcode-baseline` fails identically on untouched `main` on this
  Mac — `cmd/unitill-desktop` build tags, unrelated).
- Playwright: `modifiers-shop-wide-2399`, `catalog-modifier-summary-
  reachable-1989`, `catalog-item-editor-attach-only-2330` — 4 passed.
  **CI then caught what the local subset missed:** the two OSK-decimal
  tests in `osk-decimal-sale-catalog-fields-1284.spec.ts` still drove the
  old mandatory item picker on `/modifiers`; rewritten to create the group
  standalone and attach the probe item from the card (commit `ff7b6a42`),
  then every spec touching `/modifiers` (8 files, 37 tests) re-run green.
- **Driven run** on a throwaway till (`:8098`, demo catalogue, manager):
  created "Sauces test" with **no item** from the real form → card
  appeared with the unassigned hint → ticked the *Cleaning* category
  checkbox → hint gone, box tinted → `GET /ui/pos/modifiers?item=itm036`
  (a Cleaning item) renders the group in the checkout picker; `itm001`
  (another category) does not.
- **Visual check, looked at:** `/modifiers` at 1024×600 (en, light) —
  create card and group cards share one column; Save row no longer wraps;
  Delete group sits in its own footer; category pills wrap cleanly. Same
  page in **fa (RTL)** at 1024×600 — mirrored correctly, pills and chips
  reversed, no overflow. **360px** — stacks, no horizontal scroll. Two
  Tester-found layout defects fixed before review: the create card was
  full-width while group cards were 44rem, and Delete group orphaned on a
  wrapped second row. **Not looked at:** dark theme plugin, the kiosk
  (`body.kiosk`) variant on a real Pi/tablet, ar/tr rendering.

## Deferred / follow-ups

- ut-docs#2401 — ut-cloud editor: `item_id` optional in
  `upsert_modifier_group` (claims validator + form).
- ut-docs#2406 — bulk-delete unassigned groups.
- ut-docs#2379 (nested-dialog path to `/modifiers`) unchanged, still open.
- Mixed-version satellites: a not-yet-upgraded satellite refuses the
  primary's bundle until it upgrades (ADR-0101 §5) — same window as every
  prior schema change, recorded, not fixed.
