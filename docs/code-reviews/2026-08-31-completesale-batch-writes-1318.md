# CompleteSale: batch the per-line DB writes (ut-docs#1318)

**Date:** 2026-08-31
**Card:** [ut-docs#1318](https://github.com/universaltill/ut-docs/issues/1318) (p1, `complexity:hard`)
**Source:** Principal-engineer performance audit, 2026-08-30
(`universal-till/docs/code-reviews/2026-08-30-performance-audit.md`, section B).
**Dev:** Fable subagent. **Review:** independent Opus subagent, isolated
worktree.

## What shipped

`CompleteSale` (`internal/pos/sales.go`) used to issue up to 5 unbatched DB
statement executions **per basket line** inside its transaction: a
`CurrentQty` SELECT (stock check), `InsertSaleLine`, `InsertSaleLineModifiers`
(one exec per modifier), a conditional `InsertSaleDiscount`, and
`RecordStockMovement` (itself 3-4 statements). A 60-line basket issued
~300 individual statement executions.

Five new batch methods were added to `internal/data/pos_repo.go` — the
**existing single-row methods are untouched**, since they have other live
call sites (`sync_stock_repo.go`, `sync_sales.go`, `cloudsync_wire.go`,
`inventory_api.go`, `pos/inventory.go`):

- `CurrentQtyBatch` — one SELECT (OR-chained per-key predicate, not
  row-value `IN`) replacing the per-line stock-check loop.
- `InsertSaleLinesBatch` / `InsertSaleLineModifiersBatch` /
  `InsertSaleDiscountsBatch` — chunked multi-row INSERTs (chunk size
  computed from column count, ≲800 bound params/statement — safe headroom
  under SQLite's historic 999-variable default).
- `RecordStockMovementsBatch` — one chunked `stock_movements` INSERT (with
  the existing missing-`cost_price`-column fallback retry preserved);
  quantity deltas **aggregated per `StockKey`** across the whole basket (a
  repeated item on two lines lands as one combined delta, matching what N
  sequential `RecordStockMovement` calls would produce); one prepared
  inventory `UPDATE` executed once per distinct key (reusing
  `CurrentQtyBatch`'s existing-row knowledge, so no fresh existence probe);
  a chunked `inventory` INSERT for brand-new keys; one chunked `audit_log`
  INSERT **per movement** (audit trail stays per-movement, not per
  aggregated key).

`CompleteSale` was rewired to build all per-line rows up front, then land
them through the batch methods in FK-safe order: `sale_lines` first (its
FK children — `sale_line_modifiers`, `sale_discounts`, `stock_movements`
— all reference it), then modifiers/discounts/movements. No schema
change, no change to `CompleteSale`'s signature, return value, error
semantics, or the receipt-retry loop.

**A subtle semantic preserved deliberately, not fixed:** the old code
checked each line's stock delta independently against a quantity read
*before any writes happen* in the transaction — so two lines selling the
same item are each checked against the same starting quantity, not a
running total (a pre-existing quirk: `3 + 3` against a stock of `4`
succeeds, ending at `-2`). The batched check reproduces this exactly
(a missing map key reads as Go's zero value, same as `CurrentQty`'s
`found=false → 0`) rather than silently becoming stricter, which would
have failed sales that used to succeed — out of scope for a "batching
change, not a logic change" card.

## Verified

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `go test ./internal/pos/... ./internal/data/...` — all pass, including
  11 new tests (7 repo-level in `pos_repo_batch_test.go`, 4 integration —
  one with 4 subtests — in `sales_batch_test.go`) covering: multi-line
  repeated-item stock aggregation, the negative-inventory guard's exact
  quirk (including the 3+3-against-4 case), `AllowNegativeInventory`
  bypass, an item with no inventory row, zero-modifiers/zero-discounts
  no-ops, and returns restocking correctly through the batched path.
- `go test ./internal/pos/... -race` — clean, no data races.
- `go test ./internal/data/... -race` — **first attempt hit the default
  600s per-package timeout with no `DATA RACE` block in the dump** (park
  in `database/sql` `connectionOpener`, inside `internal/db.migrate`).
  Independently reproduced this same hang on a clean worktree of plain
  `main` (no diff at all) — confirmed pre-existing/environmental, not
  caused by this change. Also independently documented before this card,
  in `docs/code-reviews/2026-08-28-fiscal-sign-refund-return-dispatch.md`:
  *"A pre-existing full-package `-race` hang inside `internal/db.migrate`
  (random test each run) is unrelated to this diff."* `.github/workflows/
  ci.yml` never runs `-race`, so this isn't a CI gate. With
  `-timeout 45m`, `internal/data -race` passes clean in ~1800s.
- `bash scripts/ci/guard-data-access.sh` — pass (repository pattern
  intact: `internal/pos/sales.go` calls only repo methods, `internal/data/
  pos_repo.go` gained 436 insertions / 0 deletions, so every other
  single-row-method call site is provably untouched).
- Benchmark (`BenchmarkCompleteSaleLargeBasket`, new — 60 lines across 12
  distinct items, some with modifiers/discounts): **~2.0x faster on a
  60-line basket, 18.0ms → 8.8ms**, measured by reverting only `sales.go`
  and re-running the identical new benchmark against the old per-line
  code. 1-3 line benchmarks unchanged, as expected.

## TDD re-verified independently

Reverting both production files to their pre-diff state makes both new
test files fail to compile (`undefined: StockKey`, `CurrentQtyBatch
undefined`, etc.) — a genuine, on-topic red, confirmed in an isolated
worktree so the revert never touched the shared checkout. Restoring
returns both packages to green. Reviewer also isolated *just* `sales.go`
(keeping the new `pos_repo.go` methods): the integration test suite in
`sales_batch_test.go` passes identically against the OLD per-line
`CompleteSale`, which is the correct, honest characterization for a
parity/regression-preventing test on a pure refactor — not a red-first
TDD test in that specific file (the genuinely red-first tests are the
`internal/data` repo-method tests, which test code that didn't exist
before this diff).

## Adversarial correctness review (independent Opus pass)

- FK ordering confirmed correct against `foreign_keys` pragma being ON
  for every connection.
- The stock-check quirk's preservation confirmed by reading the code (not
  just the test name): a variant line normalizing both `ItemID`/
  `VariantID` before the transaction opens rules out a key-reconstruction
  mismatch between `CurrentQtyBatch`'s returned map and `CompleteSale`'s
  lookup.
- The `existing`-map handoff from `CurrentQtyBatch` to
  `RecordStockMovementsBatch` cannot go stale for this caller:
  `_txlock=immediate` takes the write lock at `BEGIN`, nothing between the
  two calls touches `inventory`, and there are no triggers.
- Per-key delta aggregation shown equivalent to N sequential
  `RecordStockMovement` calls, both for an existing and a brand-new
  inventory row.
- All seven chunk-size calculations checked by hand against their actual
  column counts (the easy-to-miss one: `audit_log`'s row placeholder has
  7 SQL columns but only 6 bound parameters, since `entity_type` is a
  literal `'inventory'` — `cols = 6` is correct).
- The `cost_price`-missing-column fallback retry cannot double-insert:
  the error fires at prepare time (zero rows attempted) and the retry is
  pinned to `start == 0`.
- No real client/shop name, no secret-shaped literal, in the new tests.

## Non-blocking, deferred as follow-up (not fixed in this PR)

1. Chunking is only actually exercised past a chunk boundary for
   `sale_lines` (120-row repo test, 60-line benchmark). The other six
   statements' chunk arithmetic was verified by hand but isn't exercised
   at chunk-crossing scale by any test.
2. `sales_batch_test.go`'s integration suite is a parity harness — it
   passes unchanged against the pre-batch `CompleteSale` — so it does not
   itself guard against a future regression back to the per-line loop. A
   statement-counting driver wrapper would be the test that actually pins
   the optimization.
3. `RecordStockMovementsBatch` trusts a non-nil `existing` map as
   authoritative (skips the UPDATE probe for an absent key). Sound for
   today's only caller (proven above), but `ux_inventory_item`'s
   `UNIQUE(item_id, variant_id, location_id)` can never fire on these
   NULL-bearing rows in SQLite, so a *future* caller passing an incomplete
   map could silently create a duplicate inventory row rather than
   erroring. Documented in the method's own doc comment; a future caller
   should pass `nil` (full-probe fallback) unless it can prove the map is
   complete, the same way `CompleteSale` does.
4. Two of three FK orderings (`sale_discounts.line_id`,
   `stock_movements.sale_line_id`) are correct by inspection and by the
   production schema, but the hand-written benchmark/test schemas don't
   declare those FKs, so only the `sale_line_modifiers` ordering is
   actually enforced by a failing test if it ever regresses.

Filed as a new Backlog card: see ut-docs#1347.

## Verdict

**Safe to merge.** No blocking findings from independent review; TDD
re-verified for real; correctness reasoning checked adversarially against
the money/stock-critical constraints this change touches.
