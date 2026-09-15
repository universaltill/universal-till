# Code review — printed shelf labels show stale base_price instead of the current price_history price (ut-docs#2260)

- **Date:** 2026-09-15
- **Ticket:** ut-docs#2260 (`complexity:easy`)
- **Branch:** `fix/2260-printed-label-price-history`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (no visibility
  into the implementation reasoning), per this card's `complexity:easy`
  routing (the one tier where "different model" relaxes to "different,
  clean-context instance").
- **Verdict: SAFE TO MERGE.** No blockers found.

## The bug

`CatalogRepo.GetItemLabel` and `CatalogRepo.GetVariantLabel`
(`internal/data/catalog_repo.go`) read raw `base_price`/`item_variants.price`
— the item's/variant's **configured** price, not its current effective one.
These feed `POST /api/print/labels` (`internal/pages/print_api.go`), which
prints a **physical** shelf label via `print.RenderLabel`. When an active
`price_history` row overrides the configured price (a scheduled/promotional
price), the printed label disagreed with what the till actually charges —
the same root cause as ut-docs#2228 (variant picker) and ut-docs#2258
(sale-screen/kiosk tile), just for a printed label instead of a screen. A
printed label is arguably higher exposure than a screen tile: a screen
re-renders on the next load, but a label persists physically on the shelf
after the promotion starts or ends.

## What shipped

- `internal/data/catalog_repo.go`: `GetItemLabel` and `GetVariantLabel` now
  fold the same price_history-aware `COALESCE` correlated subquery directly
  into their existing single-row `SELECT`, modeled exactly on
  `ItemVariantsForSale`'s/`ItemCurrentPrices`' subquery (same
  `starts_at`/`ends_at` window, same `ORDER BY datetime(starts_at) DESC
  LIMIT 1` tie-break). No batching needed — label printing is already a
  per-item/variant operation (one call per `POST /api/print/labels`
  request), so the single-row shape stays.
- Tests (`internal/data/catalog_repo_single_item_test.go`):
  `TestGetItemLabel_UsesActivePriceHistoryRow`,
  `TestGetItemLabel_MatchesPOSRepoResolveCurrentPrice`,
  `TestGetVariantLabel_UsesActivePriceHistoryRow`,
  `TestGetVariantLabel_MatchesPOSRepoResolveCurrentPrice` — active/no-row/
  expired/future-dated cases for both surfaces, plus a differential parity
  check against `POSRepo.ResolveCurrentPrice` for each.

## What the independent review found

No blockers. Two informational notes, neither requiring a change:

- **N1:** `GetItemLabel`/`GetVariantLabel` were edited in place rather than
  given a `...ForSale`-style counterpart. Confirmed safe: every other call
  site (`internal/pages/catalog/handlers.go`,
  `internal/pages/common/barcode_conflict.go`,
  `internal/pages/pos_modifiers_api.go`, `internal/pages/cloudsync_wire.go`)
  only reads `.Name`/`.Code`/existence from these two methods, never
  `.PriceMinor` — so there is no "admin edit form now shows the promo price
  instead of the configured one" regression. The admin edit path reads the
  configured price from the separate, untouched `CatalogRepo.GetItem`.
- **N2 (pre-existing, out of scope):** the same-`starts_at` tie-break
  ambiguity and the missing `price_history(variant_id, starts_at)` index
  (ut-docs#2259) are inherited unchanged from `lookupPriceHistory`/
  `ItemVariantsForSale`/`ItemCurrentPrices` — not a new divergence.

### Independent TDD re-verification (empirical, not trusted)

Reverted only the production change in `internal/data/catalog_repo.go`
(kept the new tests), confirmed all four new tests fail with the exact
stale-price symptom (310 instead of the active override 250, for both item
and variant labels, both the direct assertion and the `ResolveCurrentPrice`
parity check) — restored, all green, full `internal/...`/`cmd/...` suite
(53 packages) green.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` — clean.
- `golangci-lint run ./internal/data/...` — 0 issues.
- Full repo suite (`go test ./internal/... ./cmd/...`) green.
- Guards green: `guard-data-access.sh` (no inline SQL outside
  `internal/data`), `guard-i18n.sh` (no new/missing keys — this diff adds
  no user-facing strings), `guard-price-history-sync.sh` (no new caller of
  `AppendPriceHistoryItem/Variant`).
- Money-correctness cross-check: both new subqueries verified byte-for-byte
  equivalent in semantics to `ItemVariantsForSale`/`ItemCurrentPrices`/
  `POSRepo.lookupPriceHistory`. The schema's own `CHECK` constraint (exactly
  one of `item_id`/`variant_id` set per `price_history` row) structurally
  prevents an item-level row leaking into a variant's price or vice versa.
- N+1 check: `GetItemLabel`/`GetVariantLabel` each called exactly once per
  `POST /api/print/labels` request — no loop over many items.
- Missed-call-site sweep: no other price-sensitive raw read of
  `base_price`/`item_variants.price` found outside what #2258 already fixed
  and what `pos_repo.go`'s scan-barcode-tier helpers already re-resolve
  through `resolvePrice`/`lookupPriceHistory` before use.
- No real client/shop name, no literal secrets, no money-as-float.

## Explicitly deferred (not this card)

- `price_history(variant_id, starts_at)` index and the same-`starts_at`
  tie-break hardening — ut-docs#2259 (separate ticket, already filed).
