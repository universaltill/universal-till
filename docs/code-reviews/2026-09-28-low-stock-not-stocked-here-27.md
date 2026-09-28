# Review: location reorder list shows items stocked only elsewhere (ut-docs#27)

**Date:** 2026-09-28 · Built by Opus 5.5 (lane:cloud-24) · Reviewed by Fable (independent)

## What shipped

Product owner's decision on ut-docs#27: on a location-filtered reorder list,
a tracked item with a reorder level and no stock row at that location, but
rows at other locations, is listed at qty 0, attributed to that location and
marked **not stocked here** — unless the item or the shop sells without
tracking stock.

- `internal/data/pos_repo.go`: new `LowStockItemsFor(ctx, locationID,
  shopStockUntracked)`; `GetLowStockItems` delegates with the shop tracking
  stock. The filtered item and variant queries now scope the inventory join
  to the location and add an `elsewhere` join; `LowStockItem.NotStockedHere`
  (`not_stocked_here` in JSON). Unfiltered queries are unchanged apart from
  a constant `0` flag column. Unknown location → no not-stocked-here rows.
  Items never stocked anywhere keep the old row (empty location, unflagged).
  The #2082 variant guard is kept: a variant-tracked item with no item row
  anywhere gets no phantom item row.
- `internal/pos/inventory.go`: `LowStockItemsFor` wrapper.
- `internal/pages/inventory_api.go`: `GET /api/inventory/low-stock?location_id=`
  passes `AllowNegativeInventory` ("Sell items without tracking stock") and
  adds the escaped `inventory.not_stocked_here` hint to the location cell.
- Locale key `inventory.not_stocked_here` in en/ar/fa/tr; de/es pack PRs follow.
- Three existing tests encoded the old behaviour (hidden) and were updated
  to the decision: Batch8, LocationFilterIncludesNeverStocked, VariantTrackedItem.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium (test) | `TestLowStockItemsFor_ShopUntracked` only asserted absence, and it passed against the old code | Fixed: it now first asserts the row is listed with the shop tracking stock |
| 2 | Low | `here.id` scanned without COALESCE; a dangling location FK would 500 the list | Fixed: `COALESCE(here.id, '')` in both queries (unreachable today: FKs on) |
| 3 | Low (product) | A retired location still gets not-stocked-here rows | Accepted: a retired location is still known, and its name is un-mangled |
| 4 | Info (perf) | `elsewhere` is a DISTINCT pass over inventory for each request, then indexed lookups | Accepted for till-sized DBs |
| 5 | Info | Pre-existing hardcoded English table headers in the handler | Not in scope; unchanged |
| 6 | Info | No shipped page passes `location_id`, so today the rows reach only API callers | Noted; the help page describes the unfiltered list, so no help change |

## Verification

- TDD: the new tests failed to compile against the old code. With the old
  queries plus a stub, they failed on assertions (the reviewer and I both
  checked). All pass with the fix.
- Full gate: `go test ./...` green, `go vet`, `gofmt`, `golangci-lint` 0
  issues, and every `ci.yml` build-job guard passes. The one exception is
  `guard-shellcheck-version.sh`: shellcheck isn't installed in the cloud
  container, and this change touches no shell scripts.
- Reviewer checked `? = 0` bool binding on modernc sqlite, HTML escaping,
  the plan via EXPLAIN, and the other callers (backoffice unfiltered;
  alerts/reports use `ListStockLevels`), which are unchanged.

No UI surface changed on a shipped page (the inventory page's Low Stock list
is unfiltered), so there is no screenshot or visual check.

**Verdict:** safe to merge.
