# Code review: variant/item stock rows must use their own sell rate, not their parent's combined one (ut-docs#2089)

**Card:** universaltill/ut-docs#2089 — "Variant stock rows apply the whole
item's sell rate to just their own qty — inflates 'running out'/order
suggestions"
**Complexity:** medium (Dev: inline, session model Sonnet; Review: Opus
subagent, fresh context, isolated worktree)

## The bug

`internal/data.ItemDailySellRates` returns one sell rate per **item**,
folding in every sale of any of its variants. Since ut-docs#2082,
`ListStockLevels`/`GetLowStockItems` return a separate row per variant
(additive to the item's own row, ADR-0043 — "never folded"). But
`internal/pages/inventory_page.go`'s `stockLevelsForDisplay`,
`internal/alerts/alerts.go`'s `runningOutCount`, and
`internal/pages/reports_page.go`'s low-stock chip all did `rates[l.ItemID]`
for every row, including a variant row — applying the item's *combined*
rate against just that *one* variant's own quantity.

Worked example from the issue: an item selling 3/day total across
Small/Medium/Large variants (1/day each), each holding a healthy 20 units
(≈20 days' cover). Every variant row independently computed
`floor(20/3)=6 <= 7-day warn window` → misreported as running out, on all
three, and the reorder-suggestion quantity came out roughly 7x too high.

## What shipped

Two new sibling repository methods (`internal/data/pos_repo.go`), matching
this file's own established ADR-0043 "additive, never folded" pattern
(`ListStockLevels`/`variantStockLevels`, `GetLowStockItems`/
`variantLowStockItems`):

- **`VariantDailySellRates`** — a variant's own rate, keyed by variant id,
  computed from only the sale lines that recorded that variant.
- **`ItemDirectDailySellRates`** — an item's own rate from only its
  item-direct sales (no variant), keyed by item id.

`ItemDailySellRates` itself is **unchanged** — it still folds every
variant's sales into the parent item, because `product_reports_test.go`'s
dead-stock test explicitly relies on that folding for item-level aggregate
reporting ("a variant sale must give the parent item a sell rate").
Repurposing it would have silently broken that caller.

A new shared method, `LowStockItem.SellRate(itemRates, variantRates)`,
is the single place that decides which map/key a row reads: a
variant-scoped row (`VariantID` set) reads its own rate from
`variantRates`; an item-scoped row reads `itemRates`, which callers must
now pass as `ItemDirectDailySellRates`' result, never
`ItemDailySellRates`'. The three call sites now fetch both maps and go
through `SellRate` instead of indexing `rates[l.ItemID]` directly.

`web/help/en/inventory.md` updated (two sentences that said "each item's
last 28 days of sales" — now correct for a variant row too).

Tests: `internal/data/pos_repo_batch8_reports_test.go` gained
`TestPOSRepo_VariantDailySellRates_KeyedByVariantNotFolded` and
`TestPOSRepo_ItemScopedRowWithVariants_UsesItemDirectRateNotFoldedRate`;
`internal/pages/inventory_prediction_test.go` gained
`TestInventoryVariantSellRateIsPerVariantNotItemCombined`, a real
HTTP-handler-level integration test reproducing the issue's own T-Shirt
S/M/L example end to end.

## Independent review (Opus, fresh context, isolated worktree)

Spawned via `Agent` with `model: opus`, pointed at a detached worktree of
the fix's WIP commit (never touched the orchestrator's own shared
checkout). Ran the full gate itself (`go build`, `gofmt -l .`, `go vet`,
the affected package tests, `golangci-lint run`,
`scripts/ci/guard-data-access.sh`) — all clean — and independently
re-verified the TDD claim by reverting only the production files and
confirming the new tests failed for the right reason (all three variant
rows rendering `⚠ 6`, the issue's exact worked example) before restoring.
Mutation-tested `SellRate` by swapping its two map arguments and confirmed
both new tests catch it. Traced the sale/return path (`internal/pos`'s
`StockKey{ItemID, VariantID}`, the `sale_lines` CHECK constraint, the
`CompleteSale` choke point both sales and refunds go through) to confirm
`sl.item_id`/`sl.variant_id` really are mutually exclusive on a persisted
line, so the new SQL's `variant_id`/`item_id` filters are exhaustive, not
just believed to be. Swept every other `ListStockLevels`/`GetLowStockItems`
consumer and confirmed none of the rest compute a sell rate at all
(reorder-level/quantity-only), so none needed touching.

**Real finding, fixed**: the first version of this fix only handled the
variant-row side. The reviewer found and reproduced the mirror-image
case — an item that has **both** its own item-scoped inventory row and
variants (e.g. it kept item-level stock from before variants were added;
`GetLowStockItems`'s own doc comment names this as a real, reachable
case, and the receive dialog being item-scoped-only per
`web/ui/pages/inventory.html` makes it easy to land in). That item's
row's quantity never moves from a variant's sale, so the original fix's
`itemRates` argument — still `ItemDailySellRates`, which folds variant
sales into the item — reproduced the identical bug shape on the item
side: a healthy item-level row with zero item-direct sales but a busy
variant got misreported as running out purely from sales it never
actually supplied. Confirmed via a throwaway probe test (an item with 20
units of its own stock, 0 direct sales, one variant with 84 units sold in
the window) — the item row rendered `⚠ 6` and lit the header chip.

Fixed by adding `ItemDirectDailySellRates` and switching all three call
sites' `itemRates` argument to it, and adding a dedicated regression test
(`TestPOSRepo_ItemScopedRowWithVariants_UsesItemDirectRateNotFoldedRate`)
that would fail if `SellRate`'s item branch ever again reads the folded
map. `ItemDailySellRates` itself stays exactly as it was, for its own
existing callers.

**Real finding, fixed**: `web/help/en/inventory.md` said the prediction
comes from "each item's last 28 days of sales" — now inaccurate for a
variant row. Reworded both affected sentences to describe the per-row
(item-direct or variant-own) rate; `guard-help-topics.sh` and
`guard-help-drift.sh` both still pass (prose-only change, same
heading/step/bullet structure).

**Nitpicks, applied**: hoisted the repeated `l.SellRate(...)` call in
`inventory_page.go` to one local per row; removed a duplicate
`ItemDailySellRates` call in the data-layer test.

**Nitpicks, accepted as-is (not blocking)**: no dedicated unit test for
the variant-case path through `alerts.go`/`reports_page.go` specifically
(the shared `SellRate` method keeps the risk low, and the HTTP-level
integration test already exercises the same decision through
`inventory_page.go`); `VariantDailySellRates`/`ItemDirectDailySellRates`
each add a second/third full scan of `sale_lines ⨝ sales` per render —
pre-existing cost shape for this feature (mirrors the existing
`ItemDailySellRates` query's own cost), not a regression this fix
introduces, and no index exists on `sale_lines(variant_id)`/`(item_id)`
today; a combined single-scan query is a legitimate future optimization
but out of scope for a correctness fix.

## Verified beyond automated tests

- `gofmt -l .` empty; `go vet ./...` clean; `go build ./...` clean.
- `go test ./...` — full suite, green (twice: once after the initial
  variant-row fix, once after the item-row fix above).
- `golangci-lint run ./...` — 0 issues.
- `scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh` — all green.
- TDD re-verified personally (not just on the implementer's word): reverted
  the four production files, confirmed the new tests fail to compile
  (missing methods), then — the stronger check — reverted only
  `pos_repo.go` and left the three call sites pointed at the merge base,
  confirming `TestInventoryVariantSellRateIsPerVariantNotItemCombined`
  fails for the exact right reason (`⚠ 6` on all three variant rows, the
  issue's own worked example) before restoring and confirming green again.
- Not verified: a live curl against the running binary (`sqlite3` CLI
  unavailable in this sandbox to hand-seed a throwaway DB outside Go
  tests) — the HTTP-handler-level integration test
  (`TestInventoryVariantSellRateIsPerVariantNotItemCombined`) exercises
  the identical code path (real migrated SQLite DB, real `httptest`
  request through `registerInventoryPage`) and is the closest equivalent
  available here.

## Safe to merge

Yes. Both real findings from the independent review are fixed and
re-verified; nothing outstanding is blocking.
