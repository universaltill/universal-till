# Review: CompleteSale batch-write chunk-boundary coverage (ut-docs#1347)

**Branch:** `test/1347-completesale-batch-chunk-coverage`
**Diff:** `internal/data/pos_repo_batch_test.go` (+3 tests), `internal/data/pos_repo.go` (doc comment only, no behavior change), `internal/pos/sales_test.go` (2 FK declarations added to a hand-written test-only schema)
**Complexity:** easy — Dev at Sonnet (inline), Review at fresh-context Sonnet subagent
**Verdict: safe to merge, no blocking findings.**

## What shipped

Follow-up from independent review of ut-docs#1318 (batched `CompleteSale`
writes). Only `InsertSaleLinesBatch` was tested past its own chunk boundary;
this closes that gap for the other three batched repo methods, plus a
schema-hardening item from the same follow-up:

1. `TestPOSRepo_CurrentQtyBatch_ChunksPastParamCap` — 300 distinct keys
   against `batchChunkSize(3) = 266`, boundary-checked at index 265/266.
2. `TestPOSRepo_InsertSaleLineModifiersAndDiscountsBatch_ChunksPastParamCap`
   — 120 rows against `batchChunkSize(7) = 114` for both functions,
   boundary-checked at index 113/114.
3. `TestPOSRepo_RecordStockMovementsBatch_ChunksPastParamCap` — 150
   movements, all for brand-new inventory keys (so every one routes through
   the chunked inventory-INSERT branch, not the per-key prepared UPDATE,
   which isn't a multi-row statement and has no param-cap boundary).
   Crosses `batchChunkSize(9) = 88` (stock_movements insert) and
   `batchChunkSize(6) = 133` (inventory insert / audit_log insert),
   boundary-checked at 87/88 and 132/133.
4. **FK-declaration fix**: `internal/pos/sales_test.go`'s hand-written test
   schema (shared by 5 test files via `setupSaleDB`) was missing
   `sale_discounts.line_id → sale_lines(id)` and
   `stock_movements.sale_line_id → sale_lines(id)` — both present in the
   real migrated schema (`internal/db/migrations/001_init.sql`) but absent
   here. Added to match.
5. A documentation-only addition to `RecordStockMovementsBatch`'s doc
   comment recording the item-3 decision (leave the `existing`-map trust
   contract as-is rather than always probing via UPDATE) so it isn't
   forgotten, per the card's own framing that either choice was legitimate.

Item 2 from the card (a statement-counting regression guard for the
batching itself) was deliberately split out to a new Backlog card
(ut-docs#2250) rather than grown into this diff — a different kind of test
(a driver/repo-call-count spy) than the boundary coverage this diff adds,
and non-trivial enough to deserve its own scoped review.

## Why the FK addition is safe

`PRAGMA foreign_keys = ON` is set in this test schema, so this is a real,
enforced change, not cosmetic. Checked before adding it: none of the 5
files sharing this schema (`sales_test.go`, `sales_batch_test.go`,
`sales_line_order_type_test.go`, `sales_stock_untracked_test.go`,
`service_charge_tax_test.go`) construct a `sale_discounts`/`stock_movements`
row directly — `grep -rn "SaleDiscountRow{\|StockMovementInput{\|InsertSaleDiscount(\|RecordStockMovement("`
across all five returns zero matches. They only exercise these tables
through `CompleteSale`, which inserts sale_lines before any discount/
modifier/movement row that references one. Full suite green confirms this.

## Independent review — what was actually run, not just read

A fresh-context Sonnet subagent, isolated via `Agent(isolation: "worktree")`:
- Ran `go build`/`go vet`/`go test` for both affected packages — all clean.
- **Non-vacuity check, the important one for pure coverage-add work**:
  first tried widening `batchChunkSize` by one row per chunk — all 3 new
  tests still passed, because SQLite's 800-vs-999 param headroom absorbs a
  one-row shift for every column count here, so that particular mutation is
  a poor discriminator (not evidence of a vacuous test). Then mutated
  `InsertSaleDiscountsBatch`'s chunk loop to silently drop one row per
  chunk boundary (`end := start + chunk - 1`) — the new
  modifiers/discounts test **failed** (`sale_discounts count = 119, want
  120`), while the other two (unmutated) tests still passed. Reverted;
  all three pass clean again. This is real evidence the tests catch an
  actual boundary-dropped-row bug, not just a total-count check that a
  reordering could satisfy vacuously.
- Verified the FK-safety claim above independently via the same grep.
- Checked the new doc-comment paragraph line-by-line against the code and
  the real call site (`sales.go`) — found one wording overclaim: it said
  `CompleteSale` passes `CurrentQtyBatch`'s result "for the exact same
  keys" as `RecordStockMovementsBatch` uses, when `CurrentQtyBatch` is
  actually called for every line while `RecordStockMovementsBatch`'s own
  key set excludes untracked-item lines (a subset) — the map is a superset
  of what's needed, not identical. Doesn't weaken the trust argument (every
  key looked up is still guaranteed present) but was imprecise. **Fixed**
  before this commit — see "Findings — fixed" below.
- Confirmed no file-write/`os.MkdirAll`/`paths.Data` bug class applies (no
  file I/O in this diff) and no real client/shop name was introduced,
  rather than skipping either check.
- Restored its worktree to the exact pre-review committed state before
  finishing (`git status --porcelain` empty).

Verdict: **safe to merge**, no blocking issues.

## Findings — fixed

One non-blocking wording nitpick from the independent review, applied
before this commit: reworded the `RecordStockMovementsBatch` doc-comment
paragraph to say the caller's map is a superset of the keys this function
looks up (not "the exact same keys"), matching `CurrentQtyBatch`'s actual
per-line call site vs. this function's untracked-item-excluding key set.

## Verified beyond automated tests

- Full `internal/data` and `internal/pos` suites run together after the
  fix (not just the three new tests in isolation) — no interference.
- The non-vacuity mutation test above is itself verification beyond what
  an automated CI run alone would show — it proves the tests are load-
  bearing, not just green.

## Explicitly deferred / out of scope

- ut-docs#1347 item 2 (a statement-counting regression guard for the
  batching itself, so a future edit reintroducing the old per-line loop
  would fail a test) — split out to ut-docs#2250, a materially different
  kind of test (a driver/repo-call-count spy) than the boundary coverage
  this diff adds.

## Merge

No demo/shop-name data introduced, no secret-shaped literals. Not a UI
surface change (backend test-only diff, plus one doc comment) — UX
guidelines and manual/help-topic checklists don't apply. Merged with
`merge_method: "merge"` (never squash/rebase), per this repo's standing
git-identity/attribution rule.
