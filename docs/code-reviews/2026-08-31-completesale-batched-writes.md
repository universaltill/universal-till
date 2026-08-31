# CompleteSale batched writes (ut-docs#1318)

**Card:** [ut-docs#1318](https://github.com/universaltill/ut-docs/issues/1318) — `p1`, `complexity:hard`
**Branch:** `perf/1318-batch-completesale-writes`
**Dev:** Fable subagent, TDD-first. **Review:** Opus subagent, independent, isolated worktree.

## What shipped

`CompleteSale`'s tender transaction (`internal/pos/sales.go`) issued up to 5
unbatched statement executions PER BASKET LINE — a `CurrentQty` SELECT,
`InsertSaleLine`, `InsertSaleLineModifiers`, a conditional `InsertSaleDiscount`,
and `RecordStockMovement` (itself several raw statements). A 60-line basket
ran ~300 individual statement executions inside one transaction. This change:

- Adds `POSRepo.CurrentQtyBatch` — one SELECT for every line's stock level
  instead of a per-line SELECT (`internal/data/pos_repo.go`).
- Adds `POSRepo.InsertSaleLines`, `POSRepo.InsertSaleDiscounts`,
  `POSRepo.RecordStockMovements` (`internal/data/pos_repo.go`) and
  `POSRepo.InsertSaleLineModifiersBatch` (`internal/data/modifier_repo.go`) —
  each write type now runs through ONE prepared statement reused across every
  line in the basket, instead of a fresh statement per line per write type.
- Rewires `CompleteSale`'s per-line loop (`internal/pos/sales.go`) to build
  the per-line rows first, then issue the four batched calls.
- Adds `BenchmarkCompleteSaleLargeBasket` (`internal/pos/performance_test.go`)
  — a 55-line basket over 20 items (shared items, line discounts, modifiers)
  that none of the existing benchmarks (max 3 lines) exercised.
- The original single-row methods (`CurrentQty`, `InsertSaleLine`,
  `InsertSaleDiscount`, `RecordStockMovement`, `InsertSaleLineModifiers`) are
  untouched — every existing caller of them keeps working unchanged.

Net effect measured: ~300 statement executions for a 60-line basket → ~70
execs on ~7 prepared statements. Benchmark: batched code runs the 55-line
basket in ~7ms (comfortably under the 5000ms SC-001 threshold); a quick
before/after on the dev machine showed a ~8-10% wall-time improvement, with
the real win being the drop in statement-compilation count, which matters
more on the Pi-class target hardware than on this VM.

## Hard constraint: batching only, no logic change

The card explicitly required **no change to atomicity/correctness
guarantees**. In particular, the pre-existing stock-check quirk — every line
is checked against the SAME initial (pre-transaction) inventory quantity, so
two lines selling the same item do not see each other's depletion within one
sale — is a known, deliberate characteristic that predates this change and is
**preserved exactly**, not fixed, per the card's own instruction. It's pinned
by `TestCompleteSale_SharedItemStockCheckUsesInitialQty`.

## Independent review

Spawned as an Opus subagent (this card is `complexity:hard`, built by Fable —
model routing requires review at Opus, not Fable, per the `scrum-master`
skill's "Model routing by complexity"), in an isolated git worktree (the
verification below mutates tracked files).

**Gate:** `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean,
full `go test ./...` green, `guard-data-access.sh` /
`guard-i18n.sh` / `guard-kiosk-engine.sh` / `guard-help-topics.sh` /
`guard-compliance-claims.sh` all pass.

**Parity review:** every new batch method was read line-by-line against its
single-row counterpart — same validation order and error strings, same
`nullIfEmpty`/`nullInt64` handling, same `cost_price`-column fallback (now
checked at prepare time too, with the exec-time fallback retained as a
safety net), same `RowsAffected==0 → insert missing inventory row` branch,
same audit-log payload shape. `CurrentQtyBatch`'s cross-location/id-set query
was checked for over-matching and confirmed airtight — every scanned row is
re-gated against the exact requested key before being returned.

**Independent TDD re-verification (two deliberate sabotages in the isolated
worktree, not the working branch):**

1. Disabled the `aff==0` missing-inventory-row branch in
   `RecordStockMovements` → `TestRecordStockMovements_1318` failed for real
   (`sql: no rows in result set`). Restored, green again.
2. Deduplicated repeated `(location,item,variant)` keys in the same loop
   (simulating a "collapse the same item's movements" mistake) →
   `TestCompleteSale_MultiLineBasketWritesEverything` (5 movements expected,
   got 3) and `TestCompleteSale_SharedItemStockCheckUsesInitialQty` (wrong
   final quantity) both failed for real. Restored, green again.

**Verdict: safe to merge**, no blocking findings — no money/stock/atomicity
regression found.

## Non-blocking findings (accepted as follow-up, not fixed in this PR)

1. **`CurrentQtyBatch` vs `CurrentQty` diverge on a malformed key with BOTH
   `ItemID` and `VariantID` set** (`internal/data/pos_repo.go`).
   `CurrentQty`'s OR-shaped query still matches the item-level row for such a
   key; `CurrentQtyBatch`'s stricter per-branch matching does not. Reachable
   only via `internal/sync`'s replay path copying both fields from a remote
   peer's JSON — such a row already violates the `inventory` table's own
   CHECK constraint, so it can never be legitimate, and the only observable
   difference is which error message a doomed sale dies with (an earlier,
   less specific "insufficient stock" instead of the later "cannot specify
   both itemID and variantID"). Filed as **ut-docs#1353** — a validation
   guard on `SaleLineInput` belongs there, not a change to this batching PR.
2. **Test message clarity** in `pos_repo_batch_sale_writes_test.go` — a
   shared `qty` variable across three independent `Scan` checks could print a
   stale value if an earlier failure's error masked a later one. Fixed
   in this branch (separate variables per check) — cosmetic, no behavior
   change to what's being asserted.
3. `BenchmarkCompleteSaleLargeBasket` calls `getBenchmarkThreshold()`, marked
   `// Deprecated: prefer saleThresholds()` — consistent with the two
   existing sale benchmarks it sits next to; migrating all three to
   `saleThresholds()` is a tidy follow-up, not specific to this change. Not
   filed as its own card — low value on its own; whoever next touches these
   benchmarks should just do it inline.
4. No `internal/pos`-level (`CompleteSale`) test exercises a line whose item
   has NO existing inventory row — covered at the repo level
   (`TestRecordStockMovements_1318`), so overall coverage is adequate, but a
   sale-level case would harden the suite further. Left as a coverage note,
   not filed separately — low enough value to not warrant its own card.
5. Pre-existing, unrelated to this diff: `ux_inventory_item`'s unique index
   on `(item_id, variant_id, location_id)` doesn't prevent duplicate
   item-level inventory rows (SQLite treats NULLs as distinct in unique
   indexes). Not a regression here — noted for awareness only.

## Verification beyond automated tests

- Ran the new benchmark for real: `go test ./internal/pos/
  -bench=BenchmarkCompleteSaleLargeBasket -benchtime=20x -run=^$` — passes,
  ~7ms/op, well under the 5s SC-001 threshold.
- Manually re-derived the parity of every new repo method against its
  original (see above) rather than trusting the dev's self-report.
- Adversarially broke the insufficient-stock check myself (Tester step,
  outside the isolated-worktree review) and confirmed
  `TestCompleteSale_InsufficientStockRollsBackEverything` genuinely fails —
  not a tautology.

## Scope

Backend-only (`internal/pos`, `internal/data`, plus their test files) — no
`web/`, `web/locales/`, or manual-topic changes needed; no user-facing
behavior changed. No migration — no schema change.
