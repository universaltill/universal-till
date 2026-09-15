# Code review — sale-screen/kiosk tiles and suggestion chips show stale base_price instead of the current price_history price (ut-docs#2258)

- **Date:** 2026-09-15
- **Ticket:** ut-docs#2258 (`complexity:medium`)
- **Branch:** `fix/2258-sale-screen-price-history-tile`
- **Reviewer:** independent pass, different model (Opus) from the Sonnet
  implementation, per this card's `complexity:medium` routing. Two rounds:
  round 1 over the full diff, round 2 scoped to the round-1 blocker's fix
  only (per this pipeline's "second round earned by a blocker, scoped to
  the fix" rule).
- **Verdict: SAFE TO MERGE**, after the round-1 blocker was fixed and
  re-verified in round 2.

## The bug

`ui.ButtonStore.Load()` (cashier shortcut grid) read `b.Price` straight
from `data.ShortcutsRepo.LoadButtons` — the item's raw **configured**
`base_price`. `loadShopItems` (`internal/pages/self_order_shop.go`, kiosk
browse grid) was the same shape: `it.BasePrice` straight from
`CatalogRepo.ListItems`. Neither went through the price_history-aware
resolution path (`POSRepo.resolvePrice`/`ResolveCurrentPrice`) that
actually prices the line once tapped — so a scheduled/promotional price
change was invisible on the tile until the item was added to the basket,
which then charged a *different* price. Identical shape to ut-docs#2228
(the variant picker), just for the item tile rather than the variant
picker, and a follow-up ut-docs#2228 itself asked to be filed if true.

## What shipped

- `internal/data/catalog_repo.go`: new `CatalogRepo.ItemCurrentPrices(ctx,
  itemIDs []string) (map[string]int64, error)` — a single batched query
  (no N+1) keyed by item id, modeled directly on the neighboring
  `ItemIDsWithVariants` (batch-by-id-set via `inPlaceholders`) and
  `ItemVariantsForSale` (the correlated-subquery price_history window: an
  active, non-expired row overrides `base_price`, same
  starts_at/ends_at boundary and tie-break as `POSRepo.lookupPriceHistory`).
- `internal/ui/buttons.go`: `ButtonStore.Load()` calls it and uses the
  returned price, falling back to the existing raw `b.Price` on
  error/absence (same non-fatal-but-loud pattern as the neighboring
  `hasVariants` error handling).
- `internal/pages/self_order_shop.go`: `loadShopItems` — same pattern,
  falls back to `it.BasePrice`.
- `internal/pages/suggestions_api.go`: the "customers also buy" chip strip
  — same pattern (found by round 1's review, see below).
- Tests: `internal/data/catalog_repo_single_item_test.go` (repo-level:
  active/expired/future-dated/absent price_history rows, plus a
  differential test against `POSRepo.ResolveCurrentPrice`),
  `internal/ui/buttons_price_history_test.go` (cashier grid),
  `internal/pages/self_order_shop_test.go` (kiosk grid, Go-level +
  HTTP-rendered-HTML level), `internal/pages/suggestions_api_test.go`
  (suggestion chip).
- Filed as a separate follow-up, same root cause but a different surface:
  ut-docs#2260 (printed shelf labels via `GetItemLabel`/`GetVariantLabel`
  also read raw configured price — found by round 1's review, out of this
  ticket's scope since it's a print path, not a tile).

## What the independent review found

### Round 1 — full diff. Verdict: NOT SAFE TO MERGE, one blocker.

**BLOCKER — a third stale-price surface on the same screen was missed.**
The "customers also buy" suggestion strip (`internal/data/related_items_repo.go`'s
`SuggestForBasket`, rendered by `web/ui/partials/suggestions.html`) also
selected raw `base_price`, and its chip posts to the SAME `/api/pos/scan`
endpoint the fixed cashier tile posts to — so it carried the identical
quote-vs-charge hazard, one partial lower on the same screen. Verified
live: the reviewer reproduced a chip rendering £3.10 while the same
scan/add would charge £2.50.

**Fix:** `suggestions_api.go` now calls `ItemCurrentPrices` over the (at
most 4) suggestion item ids and overwrites each `PriceMinor`, reusing the
batched primitive this ticket already built rather than complicating
`SuggestForBasket`'s own query/`GROUP BY`. New regression test
`TestSuggestions_ChipShowsCurrentPriceHistoryPrice`.

Non-blocker findings from round 1, addressed inline (comment-only, no
logic change) or accepted as-is:
- **N1** (filed as ut-docs#2260): printed shelf labels have the same
  root-cause gap — genuinely out of scope for a tile-focused ticket.
- **N2** (addressed): `ItemCurrentPrices` deliberately carries no
  `is_active` filter, unlike `ResolveCurrentPrice`'s own item-price
  fallback — documented why in the method's doc comment (every current
  caller either already filters upstream or didn't filter the raw price
  it's replacing either).
- **N3** (addressed): test coverage gap — no future-dated (`starts_at` in
  the future) case, "literally the scheduled price change the ticket
  names." Added as case `i4` in `TestItemCurrentPrices_UsesActivePriceHistoryRow`.
- **N4/N5/N6** (noted, not fixed): a genuine £0.00 promo hides the tile
  price entirely under the existing `{{ if .Price }}` guard (pre-existing,
  cosmetic); the price_history same-`starts_at` tie-break is unconstrained
  (inherited from `lookupPriceHistory`/`ItemVariantsForSale`, no new
  divergence); SQLite bind-variable count on a full-catalog `IN (...)` is
  fine (existing `ItemIDsWithVariants`/`ItemIDsWithModifiers` have
  identical exposure).

### Round 2 — scoped to the blocker fix only. Verdict: SAFE TO MERGE.

Confirmed: right item ids used (suggestion ids, not basket ids); fallback
never zeroes a price; still a single batched call (≤4 ids); `SuggestForBasket`'s
own scoring/ordering untouched (only `PriceMinor` overwritten post-hoc);
the `is_active` doc-comment addition is genuinely comment-only (diff
filtered to non-comment lines: empty); a fresh completeness sweep found no
further missed call site beyond the two already filed/accepted.

### Independent TDD re-verification (both rounds, empirical, not trusted)

Round 1: reverted the three production files (`catalog_repo.go`,
`buttons.go`, `self_order_shop.go`) to `main`, confirmed
`TestButtonStoreLoad_ShowsCurrentPriceHistoryPrice`,
`TestLoadShopItems_ShowsCurrentPriceHistoryPrice` and
`TestSelfOrderShop_GridRendersCurrentPriceHistoryPrice` all fail with the
exact stale-price symptom (310 instead of 250, £3.10 instead of £2.50 in
the rendered HTML), and the two repo-level tests fail to *compile*
(method doesn't exist) — restored, all green.

Round 2: reverted only `suggestions_api.go` to its pre-blocker-fix state,
confirmed `TestSuggestions_ChipShowsCurrentPriceHistoryPrice` fails
showing £3.10 instead of £2.50 in the rendered chip HTML — restored,
green. Also empirically mutated the `starts_at` comparison direction in a
throwaway worktree and confirmed the new future-dated case (`i4`) does
independently catch it (returns the wrong 999 instead of 180) — reverted.

## Verified beyond automated tests

- `go build ./...` clean, `go vet ./...` clean, `gofmt -l .` silent,
  `golangci-lint run` — 0 issues, both rounds.
- Full repo suite (`go test ./internal/... ./cmd/...`) green after the
  final squashed commit.
- Guards green both rounds: `guard-data-access.sh` (no inline SQL outside
  `internal/data`), `guard-i18n.sh` (no new/missing keys — this diff adds
  no new user-facing strings, only changes which number flows into an
  already-existing `{{ money .Price }}`/`{{ money .PriceMinor }}`
  binding), `guard-kiosk-engine.sh`, `guard-price-history-sync.sh` (no new
  caller of `AppendPriceHistoryItem/Variant`).
- Money-correctness cross-check: `ItemCurrentPrices`' SQL window
  (`datetime(starts_at) <= CURRENT_TIMESTAMP AND (ends_at IS NULL OR
  datetime(ends_at) > CURRENT_TIMESTAMP)`, `ORDER BY datetime(starts_at)
  DESC LIMIT 1`) verified byte-for-byte equivalent in semantics to both
  `POSRepo.lookupPriceHistory` and `ItemVariantsForSale`'s own subquery.
  Confirmed an item-level `price_history` row (keyed on `item_id`) cannot
  leak into a variant's price or vice versa — enforced by the schema's own
  `CHECK` constraint (`001_init.sql`) that exactly one of
  `item_id`/`variant_id` is set per row.
- N+1 check: `ItemCurrentPrices` confirmed called exactly once per
  `Load()`/`loadShopItems()`/suggestions-request, outside every per-tile
  loop, via one `IN (...)` query.

## Explicitly deferred (not this card)

- Printed shelf labels' identical stale-price defect — ut-docs#2260.
