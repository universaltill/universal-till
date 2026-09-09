# Code review — per-item stock tracking (ut-docs#1850)

- **Branch:** `feat/1850-per-item-stock-tracking`
- **Date:** 2026-09-09
- **Lane:** `lane:local`
- **Reviewer:** independent subagent on a different model (Opus reviewing
  Sonnet's work), given the WIP commit `eff15132` and told to build, test,
  guard-run and mutation-test it itself.

## What shipped

`items.stock_untracked` (migration
`internal/db/migrations/013_items_stock_untracked.sql`) — a per-item "do not
track stock for this item at all" flag, independent of ut-docs#1843's
shop-wide `AllowNegativeInventory` switch. For an untracked item:

- `internal/pos/sales.go` — `CompleteSale` skips the insufficient-stock check
  and the `stock_movements` append, via a new batched
  `POSRepo.UntrackedByKey`/`StockTrackKey` lookup (`internal/data/pos_repo.go`).
- `internal/data/catalog_repo.go` — `CreateItem`/`CreateItemTx` skip the usual
  auto-created zero-quantity inventory row; `UpdateItem`/`GetItem`/`ListItems`
  carry the flag.
- `GetLowStockItems` / `ListStockLevels` gained `AND i.stock_untracked = 0`,
  which covers the Inventory screen, the reports page's low-stock chip
  (`reports_page.go:200`), the alerts digest (`alerts/alerts.go:86`),
  cloudsync and the `ask` API — all of which read through those two methods.
- `internal/pages/inventory_page.go` — the goods-in/adjustment item picker
  excludes untracked items (found by the Tester in a live driven run).
- `internal/pages/import_page.go` — the source's "Track inventory? No" answer
  now persists onto the created item, not just onto that row's one-time
  opening-stock decision.
- New catalog checkbox (`catalog.html`, `catalog_row.html`,
  `catalog/handlers.go`), key `catalog.stock_untracked` in all four shipped
  locales (+ de/es packs, PRs #202/#203, reviewed in their own repos), and a
  new "Stop tracking stock for one item" section in `web/help/*/inventory.md`
  across ar/de/en/fa/tr.

## Verified as correct (claims checked, not taken on trust)

- **The `continue` in `CompleteSale`'s movement loop is safely placed.** Read
  the whole loop body at `sales.go:896–979`: `lineRows`, `modifierRows` and
  `discountRows` are all appended *before* it, and the only statements after
  it build the `StockMovementInput`. An untracked line still gets its
  `sale_lines` row, its modifiers and its line discount — confirmed by
  `TestCompleteSale_UntrackedItem_NoStockMovementNoInventoryRow` asserting
  `COUNT(*) FROM sale_lines = 1` alongside `stock_movements = 0`.
- **A variant line resolves through its PARENT item.** The variant query is
  `SELECT iv.id, i.stock_untracked FROM item_variants iv JOIN items i ON
  i.id = iv.item_id` — the flag is read off the item, never the variant.
- **The unresolved-key default is fail-safe in the caller.** A key that
  matches nothing is simply absent from the map, so `isUntracked`'s
  `untrackedFlags[key]` reads the `false` zero value = tracked. Stock
  tracking can never be switched off by an empty lookup.
- **Migration is additive and checksum-safe.** `013_…` is the only file
  touched under `internal/db/`; `001_init.sql` is untouched;
  `ALTER TABLE items ADD COLUMN stock_untracked INTEGER NOT NULL DEFAULT 0`
  defaults to *tracked*, matching the Go zero value.
- **No mass-assignment clobber path.** `UpdateItem` has exactly one caller
  (`catalog/handlers.go:394`, via `parseItemInput`), and the checkbox
  correctly needs no hidden `=0` fallback because its default is unchecked
  (unlike `isActive`). `cloudCreateItem` and every other `ItemInput` literal
  leave the field at its safe zero value.
- **Multi-till sync needs no change.** `sync_admin_repo.go`'s items entry is
  schema-driven (`SELECT *` / `tableColumns`), so the new column propagates
  between tills automatically.
- **The catalog listing still shows untracked items** — correct, they are
  real, sellable items.
- **No money handling.** Nothing in the diff touches an amount; no raw
  float/int stands in for `internal/money.Money`.
- **Neither recurring bug class applies.** The diff writes no files, so there
  is no missing `os.MkdirAll` and no cwd-relative path where `paths.Data(…)`
  belongs (grepped the diff for `os.Create`/`WriteFile`/`MkdirAll`/
  `filepath.Join` — zero hits).
- **No real client or shop name in test/demo data.** Every literal added is
  generic (`Tracked Widget`, `Untracked Pick`, `Item A`, `Task Runner`).
- **Help prose is accurate to what was built**, not merely present. Read the
  English and German sections in full: "sells freely, is skipped by every
  stock check, and never shows up in Inventory or on a low-stock list" is
  exactly the shipped behaviour, and the German is a faithful rendering.

## Findings

### 1. `CreateVariant` created an inventory row on an untracked parent — FIXED (medium)

`catalog_repo.go:CreateVariant` called `ensureInventoryRow` unconditionally.
`CreateItem` correctly skipped the parent's own row, but every variant added
to that item afterwards re-created exactly the row the flag exists to
prevent, breaking the card's "no inventory row ever created for it".
Reproduced before fixing: one `inventory` row with `variant_id` set, and it
travelled out of `StockForExport` as a real on-hand figure.

Fixed by reading the parent's `stock_untracked` and skipping the row when set,
fail-safe in the same direction as `sales.go`'s `isUntracked` (an unreadable
or missing parent flag is treated as *tracked*). Regression test:
`TestCreateVariant_UntrackedParent_NoInventoryRow`, with a tracked-parent
control so the fix cannot be mistaken for having disabled variant stock.

### 2. `variantStockForExport` missing the exclusion — FIXED (medium)

`export_repo.go:variantStockForExport` is the export-payload sibling of
`ListStockLevels`, and got that method's `i.is_active` filter but not its new
`i.stock_untracked` one. A leftover variant row on an item switched untracked
therefore reached export/report plugins as genuine stock while the same
item's own row was filtered off every screen. Added
`AND i.stock_untracked = 0`. Regression test:
`TestStockForExport_ExcludesUntrackedParentVariants`.

### 3. `DeadStock` missing the exclusion — FIXED (low)

`pos_repo.go:DeadStock` reports "capital tied up in dead stock". An item
switched untracked keeps its existing inventory row (deliberately — the
quantity is real history and un-ticking the flag should bring it back), so
that leftover row resurfaced on the report for an item the shop has said it
does not count. Same one-line filter as its two siblings. Regression test:
`TestDeadStock_ExcludesUntrackedItems`.

### 4. `TrackedByKey` was named and documented as the inverse of what it returns — FIXED (low, bug-magnet)

The method returned `map[StockTrackKey]bool` whose value is the raw
`stock_untracked` column (true = **un**tracked), while its name said
"Tracked" and its doc comment said "reports whether each given item/variant
should be stock-tracked — false only when … stock_untracked is set", which is
the exact opposite. The caller was correct; the name and the contract were
not — and the doc's own explanation of the missing-key case was garbled
("deliberately ABSENT from neither map slot's zero value story"). This is the
same landmine the card's `StockUntracked` field naming exists to avoid, so it
was fixed the same way: renamed to `UntrackedByKey`, doc rewritten to state
the inverted sense and why, and `sales.go`'s local `trackedFlags` renamed to
`untrackedFlags`. Behaviour unchanged; test renamed accordingly.

### 5. Switching an item back to tracked left it invisible on Inventory — FIXED (low)

An item created untracked has no inventory row, and `UpdateItem` did not
create one when the flag was un-ticked. `ListStockLevels` JOINs `inventory`,
so the item was missing from the Inventory screen *entirely* — not even
listed at zero like every other tracked item — until someone happened to
record a goods-in for it. Added a best-effort `ensureInventoryRowExec` to
`updateItemExec` for the tracked case, mirroring `CreateItem`. Regression
test: `TestUpdateItem_SwitchBackToTracked_GetsInventoryRow`.

### 6. `ensureInventoryRowExec` was never actually idempotent — FIXED (medium, pre-existing)

Surfaced by finding 5's own test, which failed on the second `UpdateItem`
with *two* inventory rows. `ensureInventoryRowExec` used
`INSERT OR IGNORE` and relied on `ux_inventory_item`
(`UNIQUE(item_id, variant_id, location_id)`), but **SQLite treats NULLs as
distinct in a unique index**, and by the `inventory` table's own CHECK
constraint exactly one of `item_id`/`variant_id` is always NULL — so the
index never fires for a row this helper writes, and `OR IGNORE` ignored
nothing. Its doc comment's "if one doesn't already exist" was simply not
true; a duplicate row double-counts the item's quantity in every
`SUM(inv.quantity)` reader (`ListStockLevels`, `DeadStock`,
`StockForExport`, `DumpStock`). Latent only because the sole caller was
create-once; this card added a repeat caller.

Replaced with a single guarded statement —
`INSERT … SELECT … WHERE NOT EXISTS (… item_id IS ? AND variant_id IS ? …)` —
using `IS` for NULL-safe comparison, and one statement rather than
SELECT-then-INSERT so there is no check-then-act window.

### Accepted without change

- **`SyncStockRepo.DumpStock` and the seasonal-reorder on-hand query
  (`pos_repo.go:1877`) do not filter `stock_untracked`.** Correct as-is:
  `DumpStock` is the raw inventory truth replicated between tills, and
  filtering there would desynchronise replicas rather than hide anything.
  The seasonal query is an aggregate over rows that, for a properly created
  untracked item, do not exist.
- **`ListItems` and `ExportRows` still include untracked items.** Intended —
  they are real, sellable catalog entries; only their *stock* is untracked.
- **A direct `POST /api/inventory/receipt` against an untracked item is not
  server-side rejected.** The UI path is closed (the picker excludes it) and
  the movement would create a row; a validation layer there is defence in
  depth beyond this card's scope. Worth a follow-up if the endpoint is ever
  exposed beyond the Inventory screen.

## Test-quality verification (not taken on trust)

Each fix was reverted in place, the exact named test re-run, and a **real
assertion failure** (not a compile error) confirmed, then the fix restored
and the test re-run green.

| reverted | test | failure observed |
|---|---|---|
| `isUntracked(l) { continue }` in `sales.go`'s movement loop | `TestCompleteSale_UntrackedItem_NoStockMovementNoInventoryRow` | `expected 0 stock movements for an untracked item, got 1` |
| `if it.StockUntracked { continue }` in `inventory_page.go` | `TestInventoryPage_PickerExcludesStockUntrackedItems` | `stock_untracked item must NOT appear in the goods-in/adjustment picker` |
| `AND i.stock_untracked = 0` in `GetLowStockItems` | `TestGetLowStockItems_ExcludesUntrackedItems` | `got 2 items …, want 1 (untracked item must be excluded)` |
| the parent-flag check in `CreateVariant` (this review's fix 1) | `TestCreateVariant_UntrackedParent_NoInventoryRow` | `variant of a stock_untracked item must get no inventory row, got 1` |
| `AND i.stock_untracked = 0` in `variantStockForExport` (fix 2) | `TestStockForExport_ExcludesUntrackedParentVariants` | untracked parent's variant row present in the export payload |
| `AND i.stock_untracked = 0` in `DeadStock` (fix 3) | `TestDeadStock_ExcludesUntrackedItems` | `{Name:Item A Qty:7 StockValue:700}` still reported |

Finding 6 was not sought — it was caught by finding 5's own test asserting
`COUNT(*) = 1` after a re-save, which is the reason that assertion is in the
test rather than a simple "row exists" check.

## Gate

Run on the branch after the fixes, all clean:

- `go build ./...`, `go vet ./...`, `gofmt -l .` (no output)
- `go test ./...` — 0 failures
- `golangci-lint run ./...` — 0 issues
- `guard-data-access.sh`, `guard-i18n.sh` (1507 keys, all locales match
  en.json), `guard-help-topics.sh`, `guard-docs-shots.sh` (26 topics × 4
  locales, fresh), `guard-compliance-claims.sh`, `guard-page-http-error.sh`,
  `guard-plugin-menu-read.sh`, `guard-kiosk-engine.sh`

`guard-docs-shots.sh` passes without regenerating anything: the new catalog
checkbox sits inside the item form, which the catalog screenshot does not
capture, so the shot surface hash is unchanged.

## Verdict

**Safe to merge.** The core design is sound and better than the issue body
asked for — the inverted `stock_untracked` naming is the right call, the
`StockTrackKey`/`StockKey` split is correctly justified (stock tracking is
item-scoped, inventory is location-scoped), and the `continue` placement in
`CompleteSale` is genuinely safe. Six findings were fixed in this review, all
with regression tests: two of them (findings 1 and 6) were real correctness
bugs that would have produced wrong stock numbers in production.
