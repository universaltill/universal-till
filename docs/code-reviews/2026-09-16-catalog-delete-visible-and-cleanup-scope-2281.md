# Review — catalog "deleted but nothing deleted": hidden soft-deleted tiles, cleanup scope, honest 0-removed (ut-docs#2281)

**Date:** 2026-09-16 · **Lane:** local · **Card:** ut-docs#2281 (p1 bug, complexity:medium, 2026-09-16 design batch)
**Build:** Sonnet subagent, TDD · **Test:** driven run on a scratch till (tile hidden after soft-delete; preview with/without `include_active`) · **Review:** fresh-context Opus, read-only · **Fix triage:** local lane

## Report and diagnosis
Pilot tester (German shop) "deleted the catalog" from Settings → Data, was told it was deleted, saw everything still there; "reset" did nothing to the catalog. Three causes, all fixed:

- **A. Soft-deleted items kept their sell-screen tile.** Catalog "Delete item" = `is_active=0`; `ShortcutsRepo.LoadButtons` LEFT-JOINed `items` with no filter, so the tile stayed and tapping it silently did nothing (the resolver *did* filter). Now `JOIN … AND i.is_active = 1` — LoadButtons agrees with the resolver; the Designer hides the tile too; the orphan `shortcut_buttons` row is harmless (upsert on barcode, tile returns on reactivation).
- **B. Catalog cleanup could only remove *inactive* never-sold items** — an imported catalog is all active, so it removed 0 and said so in hardcoded English. Now an opt-in **"Also include active products that were never sold"** mode (`obsoleteItemsPredicate(includeActive)` threaded through Count/List/Cleanup, the two handlers, the elevation hidden field, a checkbox in Settings → Data), with new `held_sales`/`held_sales_archive` payload exclusions so a parked sale can never be orphaned; 0 removed renders as a warning with the reason, never as success.
- **C. Reset ("Clear transaction history") only archives transactions by design** — its response was hardcoded English; now `archived`/`batch_id` fields rendered through `settings.data.reset_done`. A "wipe the catalog for re-import" need is what B's new mode serves.

## Independent review (Opus) — findings and outcome
| # | severity | finding | outcome |
|---|---|---|---|
| 1 | should-fix (data safety) | `include_active` could delete an item sitting in the **live** cashier/kiosk basket (memory-only, invisible to the predicate) → checkout FK failure | fixed: `POSRepo.ObsoleteItemsAmong` + `cleanupInLiveBasket` refuse with 409 and an i18n'd message naming the basket, checked before the PIN prompt; mirrors `demoDataInLiveBasket`; test proves the refusal and that a non-candidate line does not block |
| 2 | should-fix (tests) | no test pinned the elevation prompt carrying `include_active` + the widened summary | fixed: `TestCleanupCatalog_ElevationPromptCarriesIncludeActiveAndWidenedSummary` |
| 3 | should-fix (CI) | `guard-docs-shots` red | `make docs-shots` run and committed |
| 4 | nit | `catalog_preview_none` said "inactive" even in the widened mode | reworded in all four locales |
| 5 | nit | translated help (`display.md` bullet 14) not updated | ar/fa/tr translated in-session (ut-docs#2291); `catalog.md`'s bullet has no translated counterpart yet — pre-existing drift under ut-docs#1973 |
| 6 | nit | help silent on modifier groups going with removed items | sentence added |
| 7 | nit | JS `fmt()` used string replacements (`$` patterns) | function replacements |

Clean: no other references (kiosk orders store no ids, promotions unlinked, `ReanchorGroupsBeforeBulkItemDelete` still runs on the widened set), i18n complete with real translations, no SQL outside `internal/data`, help topics accurate, tests not weakened.

## Gate
`go test ./internal/data/... ./internal/ui/...` (race, by the Dev) and `./internal/pages/...` (non-race; the package exceeds a local 20-min race budget — CI runs it separately) green except the pre-existing Go-1.27 image-fixture failures (ut-docs#2302, reproduced on untouched `main`). Guards: i18n, data-access, docs-shots, help-drift, help-topics green. TDD note from the Dev: the HTTP-layer tests for B and the reset test were written alongside the code rather than strictly red-first; the data-layer and tile tests were red-first and were re-verified red with the fix reverted by the reviewer.

## Follow-ups
- `ut-plugin-language-{de,es}`: 12 new keys → pack PRs this cycle (lang-pack-drift).
- ut-docs#453 (remaining inline-JS literals) is `blocked:dep` behind this card — release it at close-out.
