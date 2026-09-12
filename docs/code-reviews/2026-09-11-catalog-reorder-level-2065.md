# Code review: Catalog "Reorder level" field

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#2065 — "Inventory: reorder level has no UI
or import entry point — 'Reorder at'/'Low Stock' are dead on every shop"
**Complexity:** medium
**Reviewer:** fresh-context Opus subagent (independent, isolated working
directory)

## What shipped

`items.reorder_level` existed in the schema and was already read by
`GetLowStockItems` and the stock table's "Reorder at" column, but nothing
ever wrote it — every shop showed "—"/"No low stock items" forever. Fix
mirrors the existing `lead_time_days` field exactly, same shape end to end:

- `internal/data/catalog_repo.go`: `ItemReorderLevel` / `SetItemReorderLevel`.
- `internal/pages/catalog/handlers.go`: new `POST /api/catalog/item-reorder-level`
  (0..1,000,000 sanity bound, same rationale as `item-cost`'s ceiling,
  ut-docs#276); `ReorderLevel` wired into the item-variants panel data.
- `web/ui/partials/catalog_variants.html`: new field on the Catalog item's
  **Variants** tab, next to Lead time (days).
- `web/locales/{en,ar,fa,tr}.json`: `catalog.reorder_level` / `_hint`.
- `web/help/en/inventory.md`: "Reorder at"/"Low Stock" section updated to
  describe the new field instead of the previous "no screen to set this
  yet" gap.
- `internal/testsupport/sqlite_catalog.go`: the hand-maintained minimal
  test schema was missing `items.reorder_level` entirely (present in
  migration 001, absent here) — added to match.

**Explicit non-goals (scope discipline, BUILD-CYCLE.md):** CSV/import
column mapping for this field (the precedent field, `lead_time_days`, was
never import-mapped either — filed as a separate Backlog follow-up); `de`/
`es` plugin-pack translations for the 2 new keys (needs the self-hosted
NAS translation model, unreachable from this cloud session — filed
`blocked:env`, same shape as ut-docs#1922/#2040/#2042).

## What the independent review found

**Verdict: safe to merge after two small doc/i18n fixes (both applied).**
No code defects in the new path; the mirror of `lead_time_days` is
faithful.

1. **Real finding, filed as a follow-up rather than fixed here:**
   `internal/data/pos_repo.go`'s `GetLowStockItems` (`JOIN ... ON
   inv.item_id = i.id`) and `ListStockLevels` never match a variant-tracked
   item's inventory rows (`item_id IS NULL` for those,
   `internal/db/migrations/001_init.sql:183`) — such an item shows as
   permanently, unclearably "low" (qty 0 forever matched against any
   `reorder_level > 0`) and never gets a Reorder-at cell at all. Pre-existing
   SQL, not introduced by this change — but this change is what makes it
   reachable/likely, since the new field sits on the same Variants tab.
   Follow-up filed: ut-docs#2081.
2. **Fixed:** `web/help/en/inventory.md`'s new sentence overclaimed twice —
   a never-stocked item actually appears in Low Stock *immediately* at
   qty 0 (not only "once stock drops"), and per finding 1 a variant-tracked
   item's Reorder-at cell never appears at all. Reworded to state both
   accurately, including a one-line note on the variant gap.
3. **Fixed:** the `fa` hint referenced a column label ("سفارش مجدد در")
   that doesn't exist — the shop's actual `inventory.reorder_at` /
   `backoffice.reorder_at` string is «حد سفارش». Reworded
   `catalog.reorder_level` and its hint to use that established term.
   `ar`/`tr` were already consistent with their own established terms — no
   change needed there.

**TDD re-verified for real** (`git stash` cycle, twice, restored and
confirmed byte-identical after): reverting the new handler makes
`TestItemReorderLevel_ValidationAndClear`, `TestItemReorderLevel_RepoErrorIs500`
and `TestCatalogItemMutations_RefusedOnReplica` all fail with real
assertion errors (not skipped/no-ops); reverting only the `ReorderLevel`
panel-data wiring (handler left intact) fails the panel-render assertion
specifically — confirming that assertion is load-bearing, not incidental.

**Checked and clean:** `sqlite_catalog.go`'s new column matches migration
001 (`INTEGER NOT NULL DEFAULT 0`); clear-to-zero semantics correct and
asserted; i18n keys alphabetically placed, no duplicates; no raw SQL
outside `internal/data`; `SetItemReorderLevel` ignoring `RowsAffected` on a
bogus item id matches the existing `SetItemLeadTimeDays`/`SetItemCostPrice`
behavior (consistent, not a regression). Noted, not a defect: `reorder_level`
is `INTEGER`, so a weighed item can't have a fractional threshold — same
pre-existing column-type constraint the card didn't ask to change.

## Verified beyond automated tests

- Full `go build ./...`, `go vet ./...`, `gofmt -l .`, golangci-lint — clean.
- Full `go test ./...` — green, twice (Dev's initial pass and the
  Reviewer's independent re-run).
- `guard-i18n.sh`, `guard-data-access.sh`, `guard-page-http-error.sh`,
  `guard-help-drift.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh` — all
  green; the docs-shots manifest hashes are genuinely recomputed via a real
  headless-Chromium `make docs-shots` run (120 screenshots, 30 topics × 4
  locales), not hand-edited.
- **Real driven verification against the live app** (not just unit tests):
  ran the actual binary (`UT_AUTH=off`, throwaway sqlite DB, demo catalogue
  seeded via `e2e/seed_demo`), then via `curl` against the real running
  server: set a reorder level of 5 on a real seeded item through the new
  endpoint, confirmed it persists and re-renders (`value="5"`) in the panel
  HTML, confirmed the stock table's "Reorder at" cell changed from "—" to
  `5`, then dropped that item's stock to 2 and confirmed `GET
  /api/inventory/low-stock` now lists it (`Current 2.00` / `Reorder Level
  5`) and the low-stock badge updates to `1`. This closes the loop the unit
  tests alone couldn't: the write path (new), the read path (pre-existing,
  now exercised with real data) and the UI (real HTML from a real request).
- **Visual check:** not screenshotted separately — the field is a plain
  `<input type=number>` in the existing Catalog Variants panel, identical
  markup/CSS to the adjacent Lead-time field it was modeled on 1:1; no new
  layout risk. Not verified at the 1280×800/1024×600 pilot/kiosk viewports
  specifically or in a physical RTL rendering — flagged rather than assumed,
  same as this card's own AC anticipates for a cloud session.

## Safe to merge

Yes, once `ut-plugin-language-{de,es}` carry the 2 new keys (blocked:env
follow-up, needs NAS-model access this session doesn't have) — not merged
this cycle for that reason alone, same standing convention as
ut-docs#1918/universal-till#982.
