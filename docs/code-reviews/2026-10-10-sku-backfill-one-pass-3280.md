# Code review — SKU backfill in one pass, read-only preview (ut-docs#3280)

## What shipped

- **`internal/data/item_sku_plan.go`** (new): `skuPlanner` applies
  `nextItemSKU`'s two rules to an in-memory copy of the codes. It reads
  item SKUs, variant SKUs and both barcode tables once. Rule 1's
  per-category maximum is computed lazily from a per-category list. Rule
  2's per-prefix maxima are built in one pass, splitting each SKU at its
  first `-` (prefixes never contain one). `take` records each assignment,
  so the next item sees it, the way the old per-item call saw the
  previous UPDATE.
- **`BackfillMissingItemSKUs`** still runs in one IMMEDIATE transaction
  (it serializes against concurrent inserts as before). It now plans in
  memory and stores each SKU with one prepared UPDATE.
- **`PlanMissingItemSKUs`** (the Catalog "Generate missing SKUs" preview)
  runs the same planner in `BeginTx(ctx, &sql.TxOptions{ReadOnly: true})`.
  With `ReadOnly`, modernc issues a plain deferred `begin` instead of the
  DSN's `_txlock=immediate`. Under WAL that gives a consistent snapshot
  and takes no write lock.
- The insert path (`CreateItem`/`CreateItemTx`/import preview) still
  calls `nextItemSKU` and is unchanged.

## TDD

These tests were written first and run against the unchanged code:
- `TestPlanMissingItemSKUs_TakesNoWriteLock` failed with
  `backfill item skus: begin: database is locked (5) (SQLITE_BUSY)` while
  another writer held the lock. It passes now (0.26s).
- `TestBackfillMissingItemSKUs_2000InOneCategoryIsFast` took **2.51s**
  (budget 1s). It now takes **~40ms** (numeric sequence) and **~50ms**
  (TOR-0001…TOR-2000 prefix sequence). Under `-race` the budget is 10s
  (measured 1.6s; CI runs `internal/data` without `-race`).
- `TestSKUPlan_MatchesPerItemGenerator` (an edge-case fixture with 19
  blank items) and `…Randomized` (5 seeds × 80 blank items) compare the
  planner with a copy of the old per-item loop. They pass on old and new
  code, which is AC 1: the assigned SKUs are identical.
- Mutation checks: disabling `take`'s numeric-max or prefix-max update
  keeps the equivalence tests green, because the taken-set probe still
  finds the same code. It does fail the 2,000-item test (falls back to
  KUC-… / "no free number after 0" at `maxSKUProbe`), so the prefix case
  was added to that test for this reason.

## Independent review (Fable)

The reviewer ran build, vet, the SKU tests and the `internal/pages/catalog`
tests. It also ran four probe tests by overlay: whitespace-padded
category ids, BLOB-typed SKUs, cross-category numeric collisions, and
50k items in 500 categories.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Minor | A BLOB-typed SKU/barcode counts as taken in the planner but not in `skuTaken`'s text `=`. Reproduced by binding `[]byte` directly; no app path writes one | Accepted (the planner is the safer side); documented on `taken` |
| 2 | Minor | Lazy scans were O(C·N)+O(P·N), plus a loop over cached prefixes per `take` (50k/500 categories: 248ms) | **Fixed**: per-category lists and one-pass prefix maxima, O(N) total, O(1) `take` |
| 3 | Nit | `take` keys the numeric cache by the untrimmed id while `next` trims. This is correct (it mirrors `category_id = ?` with a trimmed argument) but looks like a bug | **Fixed**: comment says it is deliberate |
| 4 | Nit | Timing budget headroom ~20×; low flake risk | Accepted |
| 5 | Nit | Review record missing | This file |

After the fixes: gofmt, vet, golangci-lint (0 issues), the SKU tests and
the mutation check were re-run, all green.

## Gate

`go build ./...`, `golangci-lint run ./...` (0 issues), `guard-data-access`,
`guard-i18n`, `guard-card-data-schema`, `guard-core-neutral`,
`guard-kiosk-engine` and `guard-pipefail-grep-q` all pass. `go test ./...`
passes except `internal/plugins`, which hit Go's default 600s package
timeout on this container. CI runs that package with `-timeout 20m`, and
it is untouched here. `guard-deadcode-baseline` reports two pre-existing
`internal/logging` functions because this container can't build
`cmd/unitill-desktop` (no GTK headers). Unrelated.

No user-visible change: the help topic doesn't describe locking. No
migration, no locale keys.

**Verdict:** safe to merge.
