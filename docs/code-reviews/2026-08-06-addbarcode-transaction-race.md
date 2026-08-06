# 2026-08-06 — AddBarcode SELECT-then-upsert race (silent barcode reassignment)

Card: [ut-docs#304](https://github.com/universaltill/ut-docs/issues/304) (p1)
Branch: `fix/304-addbarcode-race`

## What shipped

`CatalogRepo.AddBarcode` (`internal/data/catalog_repo.go`) did a SELECT-based
availability check (`ensureBarcodeAvailable`) and a separate
`INSERT ... ON CONFLICT(barcode) DO UPDATE` on `r.db`, with no transaction
around the pair. Two concurrent callers (two tills editing/importing at once,
a cloud-driven `AddBarcode` landing mid local-import, a retried import) could
both pass the check; the second `DO UPDATE` then silently reassigned the
barcode to a different item/variant — it then scans to the wrong product at
the till, charging the customer for the wrong item.

Fix, same public signature (`AddBarcode(ctx, in)` — ~10 existing call sites,
none changed):

1. `AddBarcode` now reserves a dedicated `*sql.Conn` and issues a raw
   `BEGIN IMMEDIATE` on it. `modernc.org/sqlite`'s driver does support a
   DSN-wide `_txlock=immediate` that would make every `BeginTx` in the app do
   this, but that was deliberately not used — the fix is scoped to this one
   connection rather than every write transaction in the app.
2. The check-then-insert body (both the item-barcode and variant-barcode
   branches) now runs entirely on that connection, via a new
   `addBarcodeInTx` helper; `ensureBarcodeAvailable`'s parameter changed from
   `*sql.DB` to `*sql.Conn`.
3. Both `ON CONFLICT(barcode) DO UPDATE` statements gained a
   `WHERE <table>.<owner_col> = excluded.<owner_col>` guard (no longer
   updating the owner column itself) plus a `RowsAffected() == 0` check
   returning an explicit "already assigned to a different item/variant"
   error — defense in depth on top of the transaction, matching the ticket's
   acceptance criteria literally ("an insert whose conflict behaviour cannot
   silently steal a barcode from another item").
4. New test `TestAddBarcodeConcurrentRace`
   (`internal/data/catalog_repo_concurrency_test.go`): two goroutines racing
   `AddBarcode` for the same barcode against two different items, 30 rounds,
   against a **real migrated file-backed DB** via `internal/db.Open` (not
   `testsupport.NewCatalogTestDB`'s `:memory:` DB — a `:memory:` DSN gives
   each pooled connection its own isolated database and cannot exercise
   real multi-connection locking at all). Each round asserts exactly one
   call wins, the loser's error says "already assigned", and the DB ends up
   with exactly one `item_barcodes` row, owned by the winner.

## TDD proof

Pre-fix (test run against unmodified code):
```
--- FAIL: TestAddBarcodeConcurrentRace (0.76s-0.87s across independent runs)
    round 1: both AddBarcode calls succeeded for barcode "RACE-001" — silent reassignment race
```
Post-fix: `PASS`, stable across `-race -count=3` and `-count=5` repeated runs.

## Scoped out of this card (with reasons, not silently dropped)

- **Import per-row atomicity** (the ticket's other acceptance-criteria
  bullet: "each import row commits atomically — item, barcode, and opening
  stock land together"). `internal/pages/import_page.go`'s commit loop was
  **not** touched. Wrapping `CreateItem` + `AddBarcode` +
  `RecordStockMovement` in one shared transaction per row would either (a)
  regress the deliberate warn-and-continue import UX from ut-docs#293 (an
  item is still created even when its barcode attach or stock movement
  fails — reviewed, intentional behavior, not a bug), or (b) require
  threading an external `*sql.Tx` through `AddBarcode`, which conflicts with
  `AddBarcode` needing its own dedicated `BEGIN IMMEDIATE` connection for
  the race fix above (nested transactions aren't free in SQLite). Filed as
  its own follow-up card: **ut-docs#311**.
- `items.sku`/`item_variants.sku` paths (`CreateItem`, `SKUExists`) — the
  independent review confirmed these are safe as-is: both columns are
  `UNIQUE` and `CreateItem`'s `INSERT` has no `ON CONFLICT`, so a race there
  surfaces as a normal constraint-violation error, never a silent
  reassignment. No change needed.
- `internal/db/db.go`'s DSN untouched (no `_txlock=immediate`) — see below.

## Independent review (Opus, fresh context) — one blocker found and fixed

**Blocker, fixed**: the transaction's cleanup (`ROLLBACK` on any error path,
and the final `COMMIT`) ran on the caller's own `ctx` — the same
cancellable, request-scoped context used for the actual work. Reproduced by
the reviewer: if that `ctx` is cancelled between finishing the work and
`ROLLBACK`/`COMMIT` (an `net/http` request context is cancelled on client
disconnect — a real scenario on a flaky till network or a user navigating
away mid-HTMX-request; `internal/pages/catalog/handlers.go`,
`internal/pages/import_page.go`, and `internal/pages/cloudsync_wire.go` all
pass such a context into `AddBarcode`), `ExecContext` never reaches SQLite,
the `ROLLBACK` is silently skipped, and `conn.Close()` returns the
connection to the pool *still inside `BEGIN IMMEDIATE`* — `modernc.org/
sqlite` implements neither `driver.SessionResetter` nor `driver.Validator`,
so `database/sql` has no way to detect or clean this. The reviewer measured
the consequence directly: an unrelated writer on a different connection then
gets `database is locked (SQLITE_BUSY)` after the full 5s busy_timeout and
**fails** — checkout blocked by a leaked lock, precisely what this product's
offline-first rule forbids.

Fix: `ROLLBACK` and `COMMIT` now run on `context.WithoutCancel(ctx)`, so
cleanup always reaches SQLite regardless of the caller's context state; if
`ROLLBACK` itself still errors (a *real* failure, not a skipped no-op), the
connection is discarded rather than pooled
(`conn.Raw(func(any) error { return driver.ErrBadConn })`, the documented
`database/sql` signal to close rather than reuse a connection). Re-verified:
full targeted + race gate green after the fix (below). A dedicated
black-box regression test for the exact cancellation timing window was
considered and not added — reliably hitting that window from outside
`AddBarcode` without adding test-only instrumentation hooks would itself be
flaky; the reviewer's own direct reproduction (shown above) together with
the fix being the documented, standard `context.WithoutCancel` pattern for
"cleanup must run regardless of caller cancellation" was judged sufficient.

**Medium, addressed**: the original code comment claimed
"`modernc.org/sqlite`'s `BeginTx` always issues a plain deferred `begin`" as
the reason a raw-connection `BEGIN IMMEDIATE` was needed — the reviewer
found this factually wrong (the driver does honor a DSN-wide `_txlock`
parameter) and the follow-on reasoning ("scoped to avoid blocking long
read-only report transactions") didn't hold either, since every existing
`BeginTx` call in this codebase is already a write transaction. Comment
corrected to state the real, narrower reason: scoping the change to the one
connection that needs it, not every write transaction in the app.

**Medium, filed as a follow-up, not fixed here**: independent of this fix,
the reviewer found that a `BEGIN IMMEDIATE` connection holding the RESERVED
lock makes a *concurrent, unrelated* deferred `BeginTx` writer fail
instantly with `SQLITE_BUSY` (~20µs, not honoring the 5s busy_timeout at
all — SQLite's deadlock-avoidance skips the busy handler specifically on a
SHARED→RESERVED lock promotion). This is structurally pre-existing across
every other write path in the codebase already (e.g. `ApplyAdmin` holds
RESERVED for longer than this fix's window), not a regression introduced
here, so not a blocker for this card — but it's a real gap. Filed as
**ut-docs#311**: move the DB to WAL mode + DSN-wide `_txlock=immediate`,
which would close this class for every writer at once and also let
`AddBarcode` drop its raw-connection dance in favor of a plain `BeginTx`.

**Nit, accepted as-is**: the `RowsAffected() == 0` defense-in-depth
branches in the upsert are unreachable in practice (`ensureBarcodeAvailable`
already catches a foreign owner earlier in the same transaction) and the
concurrency test's `strings.Contains(..., "already assigned")` assertion
can't distinguish which of the two layers actually fired. Left as cheap
insurance per the ticket's explicit ask; not worth a dedicated test for an
intentionally-defensive, currently-unreachable branch.

**Verified correct by the reviewer, no action needed**: the `ON CONFLICT
... WHERE` guard's SQLite semantics (a non-matching WHERE makes the whole
insert a no-op with `RowsAffected() == 0`, not an error — confirmed
empirically, not assumed); defer/rollback ordering; item/variant symmetry
in the guard column names; no other unsafe call site; repository-pattern
guard; no client/shop names or literal secrets in the diff.

## Verification beyond the automated suite

- Full gate re-run after the blocker fix: `go build ./...`,
  `go vet ./...`, `gofmt -l` (clean), `bash scripts/ci/guard-data-access.sh`
  (pass — all new SQL stays in `internal/data`).
- `go test ./internal/data/... -race -run 'AddBarcode|Barcode' -count=3`:
  all 8 barcode-related tests × 3 runs, all green, race-clean.
- `go test ./... -race`: every package passes except one pre-existing,
  unrelated failure — `internal/issuereport`'s
  `TestSaveCleansUpDirectoryOnWriteFailure` (already tracked as
  ut-docs#258). Confirmed unrelated by stashing this diff and re-running:
  identical failure on unmodified `main`, caused by this sandbox running as
  uid 0 (root bypasses a chmod'd read-only directory, which the test
  assumes will block a write).

## Safe-to-merge verdict

Yes, after the blocker fix above. The independent review's most valuable
finding was exactly the kind of thing a same-model self-review would have
missed: a correctness-under-cancellation bug in the *cleanup* path of a fix
whose main-path logic (the locking + upsert-guard design) was already
sound and TDD-proven.
