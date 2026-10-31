# Review: cleared baskets in the ADR-0111 sales aggregate (ut-docs#4721)

**Date:** 2026-10-22 · **Branch:** `feat/4721-cleared-baskets-aggregate` · **Lane:** `lane:cloud-41`
**Built by:** Sonnet 5.5 · **Reviewed by:** Opus 5.5 (independent, different model) · **Complexity:** medium

## What shipped

The Z report has counted baskets cleared with New sale before payment
since #1122 (`cleared_baskets`, migration 086), and a replica's clears
have reached the main till since #4711. The ADR-0111 aggregate the main
till uploads to ut-cloud did not carry them, so my. reports could not
show them.

- `internal/data/cleared_basket_repo.go`: `clearedBasketTillKeyExpr` (the
  same till_id → register_id → selfTill rule as sales and no-sale opens)
  and `ClearedBasketsForTill` (count and Σ `total_minor` on one business
  date and till).
- `internal/data/sales_aggregate_repo.go`: `SalesAggregate` gains
  `ClearedBasketCount` / `ClearedBasketTotal` (`money.Money`).
  `SalesAggregateKeys` gains a cleared_baskets arm, so a day whose only
  activity is a cleared basket still gets a rollup, as with no-sale opens.
  `SalesAggregateFingerprints` gains arm `'k'` (count, Σ total, latest
  created_at), so a late-journaled clear rebuilds its day.
- `internal/cloudsync/sales_aggregates.go`: the wire DTO gains
  `cleared_basket_count` and `cleared_basket_total_minor`. The fields are
  additive, and ut-cloud's decoder ignores unknown fields, so the till and
  the cloud can ship in either order.
- `docs/data-model.md` updated. ADR-0111 has an "Additive fields" note in
  a separate ut-docs PR. The ut-cloud side (store, report totals, merchant
  export) is in ut-cloud.
- No migration, no UI, no locale keys.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | info | Every stored day's content hash changes once, because the DTO gained fields. The upload window is re-sent on the first round after upgrade. This is the same one-off cost the no-sale fields had (#2558). The build version is already in the fingerprint, so an upgrade rebuilds the window anyway. | Accepted |
| 2 | info | No per-cashier cleared-basket figure. Out of the card's scope. | Accepted |

No defects found.

## Verified

- Mutation check, run by the reviewer: with the `'k'` fingerprint arm
  disabled and the `SalesAggregateForTill` assignment dropped, the tests
  `TestClearedBasketsForTill_ScopedByDayAndTill`,
  `TestSalesAggregateKeys_IncludesClearedOnlyDayAndTill`,
  `TestSalesAggregateFingerprints_MoveOnlyWithSourceRows` and
  `TestPushSalesAggregates_ClearedBaskets` all fail. With the code restored
  they pass.
- Full gate: gofmt, build, vet, `go test ./...`, golangci-lint, and the
  data-access, card-data, core-neutral, deadcode and migration-collision
  guards.
- Not a UI change, so no driven run and no screenshots.

## Verdict

Safe to merge.

## Deferred

- ut-docs#4798: show the figures on my. and in the sales report's
  CSV/XLSX export and print.
