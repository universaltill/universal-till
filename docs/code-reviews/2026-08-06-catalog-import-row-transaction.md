# 2026-08-06 — Catalog import: one transaction per row

Card: [ut-docs#310](https://github.com/universaltill/ut-docs/issues/310)
Branch: `fix/310-catalog-import-row-transaction`

## What shipped

1. `CatalogRepo.CreateItemTx(ctx, tx, in)` (`internal/data/catalog_repo.go`)
   — additive alongside the existing `CreateItem`, which is untouched and
   keeps its own autocommit behavior for its other callers. Shares its
   item-insert + inventory-row logic with `CreateItem` via a new
   `ensureInventoryRowExec(ctx, ex execer, ...)` helper and a small local
   `execer` interface (satisfied by both `*sql.DB` and `*sql.Tx`) so the
   query text lives in exactly one place. The inventory-row step stays
   exactly as best-effort as `CreateItem`'s own (logged, never fails item
   creation) — a stockless deployment (no `stock_locations` table) must
   still be able to import a catalog.
2. `POSRepo.RecordStockMovementSavepoint(ctx, tx, in)`
   (`internal/data/pos_repo.go`) — wraps the existing
   `RecordStockMovement(ctx, tx, in)` (unchanged signature, still used
   elsewhere) in a `SAVEPOINT`/`ROLLBACK TO`/`RELEASE` so a failure on any
   of its four statements only discards that movement, never anything
   already written earlier in the caller's transaction.
3. `internal/pages/import_page.go`'s `/api/import` commit loop: begins a
   `*sql.Tx` per row, calls `CreateItemTx` then
   `RecordStockMovementSavepoint` against it, then `tx.Commit()`. Barcode
   attach stays the existing, unmodified `AddBarcode` call, deliberately
   run *after* the row's transaction commits — see "What stayed out of the
   transaction, on purpose" below.

## Why (and what changed observably)

Before this card, `CreateItem`, `AddBarcode`, and `RecordStockMovement`
each ran as independent autocommits. A crash or an unexpected DB-level
error between them could leave a real partial state (item created, no
stock; or worse, a half-applied stock movement) with no way to undo it.
The acceptance criteria required item + inventory row + opening-stock
movement to commit together, *without* changing any of the established
warn-and-continue outcomes (barcode conflict, unsupported shape, no
stock location, and — per the pre-existing
`TestImport_StockRecordingFailureWarnsAndDoesNotPublish` — a
stock-recording failure too, DB-level or not).

**Observably, nothing changes for any currently-tested case.** Every
warn-and-continue path still warns and the row still commits. What
changes is what happens on a genuinely new failure surface: `tx.Commit()`
itself failing (the item-insert error path already behaved this way
pre-#310, since nothing was ever created on failure either way).

## What stayed out of the transaction, on purpose

Barcode attach is not folded into the row's `*sql.Tx`. `AddBarcode` owns
its own `BEGIN IMMEDIATE` transaction on a dedicated connection
(ut-docs#304's race-protection fix for a concurrent check-then-insert on
barcode uniqueness); folding it into the row's tx would mean
re-implementing that protection here. It runs after the row's tx commits
so it can see the item at all (a separate connection can't see another
transaction's uncommitted writes). This means AC1's "barcode attach if
any" lands in its own already-atomic operation, not literally the same
Go transaction — documented in code, not silently dropped.

## Independent review (Opus, medium card) — two rounds

**Round 1** found two blocking issues:
- **B1**: `RecordStockMovement` called with a caller-supplied tx does not
  roll back on failure (by design — it only rolls back a tx it opened
  itself). Calling it directly against the row's tx meant a failure on
  its 2nd–4th statement left an orphaned partial write (e.g. a
  `stock_movements` row with no matching inventory change) that then rode
  along into `tx.Commit()` — reported to the operator as "stock not
  carried" when something had in fact landed.
- **B2**: the original regression test for the new rollback path
  (`TestImport_UnexpectedItemInsertFailureRollsBackWholeRow`) forced the
  item insert itself to fail — which already produced the same "failed:"
  outcome pre-#310 (nothing was ever created either way), so it didn't
  actually prove the transaction was load-bearing.

Fixed: `RecordStockMovementSavepoint` (item above) closes B1; the test's
doc comment was reworded to stop overclaiming, and a new test,
`TestImport_StockRecordingFailureLaterInTheMovementRollsBackJustTheMovement`,
forces the inventory *update* (statement #2, not #1) to fail and asserts
the movement is fully discarded with zero orphaned rows.

**Round 2** (scoped to the fix, earned by round 1 finding a blocker-class
issue — money/inventory-integrity) verified the savepoint semantics
empirically (a scratchpad copy of the repo, not the working tree) —
confirmed `ROLLBACK TO SAVEPOINT` leaves the savepoint open on the stack
and the code's `RELEASE` after it is load-bearing; confirmed an
unreleased savepoint does not block `tx.Commit()`; confirmed the fixed
savepoint name is safe for this call site (exactly one call per
transaction); confirmed the error text returned to the operator is
unaffected; TDD-re-verified the new test fails with an orphan count of 1
against the pre-fix code and passes after. **Verdict: approved.**

Three minor findings from round 2, one fixed here, two deferred:
- **M1 (fixed)**: the success-path `RELEASE SAVEPOINT` failing returned
  an error even though the movement had fully succeeded — same defect
  class as B1, just on a much narrower window (a `RELEASE` on a known-live
  savepoint essentially only fails on connection death). Now logged, not
  returned as a failure, matching the async nature of the equivalent
  rollback-path branch.
- **M2 (deferred, Backlog)**: `guard-data-access.sh`'s regex doesn't
  recognize `SAVEPOINT`/`ROLLBACK TO`/`RELEASE`/`BEGIN`/`COMMIT`, so its
  pass on this diff isn't actually evidence those statements stayed out
  of `internal/pages` — verified by hand (grep) instead. Real gap in the
  guard script itself, not specific to this change.
- **M3 (deferred, Backlog)**: `internal/pages/import_page_test.go` isn't
  gofmt-clean, but the drift is in `TestHTMLEscape`'s map literal —
  untouched by this diff, confirmed pre-existing on `HEAD`. `gofmt` isn't
  part of this repo's pre-commit checklist, so it'll keep drifting until
  something enforces it.

## Verified beyond automated tests

- Full three-package targeted suite (`internal/data`, `internal/pos`,
  `internal/pages`) green, including with `-race`, twice (once per
  review round).
- Full `go test ./... -race` green except the pre-existing, unrelated
  `TestSaveCleansUpDirectoryOnWriteFailure` (ut-docs#258, a root-run
  sandbox flake) — confirmed identical on `git stash` (pre-change code).
- `go vet`, `guard-data-access.sh`, `guard-i18n.sh` all clean.
- Grepped every other `RecordStockMovement(ctx, tx, ...)` call site in
  the codebase (round 2): only one other passes a caller-owned tx
  (`internal/pos/sales.go`'s `CompleteSale`), and it's structurally safe
  — it runs inside `db.WithTx`, which rolls back the *entire* surrounding
  transaction on any error, so no partial write can survive there either.
  No second instance of B1 exists elsewhere; no follow-up needed on that
  axis.

## Safe-to-merge verdict

Yes. All four acceptance criteria met (AC1's barcode-atomicity scope
documented as staying outside the row's Go transaction, for the reason
above); both review rounds' blocking findings closed and re-verified.

## Deferred

- Backlog card: extend `guard-data-access.sh` to also flag
  `SAVEPOINT`/`ROLLBACK TO`/`RELEASE`/`BEGIN`/`COMMIT` outside
  `internal/data`/`internal/db` (M2).
- Backlog card: add a `gofmt -l` check to CI/pre-commit so formatting
  drift like `TestHTMLEscape`'s map literal (pre-existing, unrelated to
  this change) doesn't keep accumulating (M3).
