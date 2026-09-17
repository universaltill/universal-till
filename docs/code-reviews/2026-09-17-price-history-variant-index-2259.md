# Code review — price_history has no index for variant_id lookups (ut-docs#2259)

- **Date:** 2026-09-17
- **Ticket:** ut-docs#2259 (`complexity:medium`)
- **Branch:** `fix/2259-price-history-variant-index`
- **Reviewer:** independent pass, Opus subagent working in an isolated
  worktree (per this card's `complexity:medium` routing — Sonnet built it,
  Opus reviewed it, per `MODEL-ROUTING.md`).
- **Verdict: SAFE TO MERGE.** One real, non-blocking gap found (a 7th
  occurrence of the tiebreak pattern missed by the original scope) and
  fixed before merge. TDD claim independently re-verified. Full gate green.

## The bug

`price_history`'s only pre-existing index, `idx_price_history_item
(item_id, variant_id)` (`001_init.sql`), is `item_id`-leading, so a
`WHERE variant_id = ?` predicate can't seek it. `EXPLAIN QUERY PLAN`
against the real migrated schema showed `SCAN price_history` / `SCAN ph`
+ `USE TEMP B-TREE FOR ORDER BY` for every variant-keyed price-resolution
query. This was already true of `POSRepo.lookupPriceHistory` before
ut-docs#2228; #2228's `CatalogRepo.ItemVariantsForSale` multiplied it into
one full scan **per variant**, on the touchscreen-interactive
picker-open path, against a table that is append-mostly and never pruned.

Related, same review that filed this card: the tie-break between two
`price_history` rows sharing an identical `starts_at` was unconstrained
(`ORDER BY datetime(starts_at) DESC LIMIT 1` alone) — every price-
resolution query in this repo shares this exact shape and depends on
agreeing with every other one on which row wins in that case, previously
true only because SQLite's planner happened to pick the same row at every
call site.

## What shipped

- `internal/db/migrations/032_price_history_variant_index.sql`: new
  `CREATE INDEX IF NOT EXISTS idx_price_history_variant ON price_history
  (variant_id, starts_at)` — `IF NOT EXISTS` for the same reason
  `021_sales_local_date_index.sql` uses it (the
  `fiscal_signing_keys_*_test.go` migration-replay tests rewind
  `schema_migrations` past a boundary below 32 and re-run migrations
  above it against a DB that already physically has the index).
- `internal/data/pos_repo.go`: `lookupPriceHistory`'s `ORDER BY` gained a
  `, rowid DESC` secondary key, with a comment explaining why.
- `internal/data/catalog_repo.go`: the identical `rowid DESC` tiebreak
  applied to **all 7** occurrences of this repo's `price_history`
  price-resolution query shape — `GetItemLabel`, `ItemVariantsForSale`,
  `ItemCurrentPrices`, `GetVariantLabel`, `VariantsForItem`, and
  `resolveCurrentPriceExec` (the 7th — see "What the review found"
  below).
- `internal/db/price_history_variant_index_test.go` (new):
  `TestPriceHistoryVariantIndexMakesVariantLookupsSargable` — runs
  `EXPLAIN QUERY PLAN` for the real `lookupPriceHistory` and
  `ItemVariantsForSale` subquery shapes against a fresh migrated DB,
  asserting the exact `SEARCH ... USING INDEX idx_price_history_variant`
  plan line appears (pinned per-case, not just "appears somewhere" — the
  correlated-subquery case's outer query has its own unrelated `SEARCH`
  that would otherwise make the assertion toothless) and that no
  `SCAN price_history`/`SCAN ph` full scan appears. Deliberately does
  **not** require the residual `USE TEMP B-TREE FOR ORDER BY` to
  disappear — the sort key is the expression `datetime(starts_at)`, which
  a plain (non-expression) index can't also satisfy, but that sort now
  runs over one variant's few rows instead of the whole table.

## What the review found

**Missed 7th occurrence** (`internal/data/catalog_repo.go`,
`resolveCurrentPriceExec`) — a byte-for-byte copy of
`POSRepo.lookupPriceHistory`'s query, documented in its own comment as
that function's "execer-based twin ... same resolution order," used as
the pre-update resolved-price probe guarding `price_history` appends
(`SetItemPrice`, `UpdateItem`, `UpdateVariant`). The original scope (this
query + the 5 `catalog_repo.go` copies = 6) missed it; leaving it out
would have preserved exactly the incidental-agreement dependency this
card set out to remove, between two functions whose own documentation
says they must agree. Fixed with the identical one-line change before
merge; full gate re-run green after.

Verified exhaustively (repo-wide grep for `FROM price_history` and
`price_history ph`) that these are the only 7 price-resolution reads;
`sync_admin_repo.go`/`demo_seed_repo.go` touch `price_history` only via
`UPDATE`/`DELETE`, no `ORDER BY` to fix.

## What was verified beyond automated tests

- **Planner behavior with no `ANALYZE`** (this product never calls it):
  confirmed empirically, not by reasoning — `idx_price_history_variant`
  is what the planner picks for `variant_id = ?`; the pre-existing
  `item_id = ?` path via `idx_price_history_item` was already fine and
  stays fine.
- **`rowid DESC` semantic safety**: worked through `AppendPriceHistoryItem`/
  `AppendPriceHistoryVariant`'s close-prior-open-row behavior and
  confirmed at most one row is active per (item|variant) per instant for
  any data path the app itself produces (seeds included) — the fix is
  inert for real data, not just "low risk." Refinement over the card's own
  analysis: the tie condition is ties on *normalized* `datetime(starts_at)`
  values, not byte-identical raw strings — the table genuinely mixes
  `datetime('now')`'s `'YYYY-MM-DD HH:MM:SS'` default with appends'
  RFC3339 timestamps, which normalize to the same instant. Also noted:
  `rowid` isn't an `INTEGER PRIMARY KEY` alias here (`id` is `TEXT PRIMARY
  KEY`), so it isn't a documented-stable ordering across `VACUUM` — in
  practice relative order survives (`VACUUM INTO` copies in rowid order)
  and it remains the best available "latest wins" proxy, but this is
  implementation behavior, not a guarantee. `price_history` is never
  synced (ADR-0099), so no cross-device divergence risk from this either
  way.
- **TDD claim re-verified independently**: moved migration 032 aside,
  re-ran the new test, confirmed it fails with exactly the claimed
  `SCAN price_history` / `SCAN ph` + `USE TEMP B-TREE FOR ORDER BY` plans;
  restored the file (byte-identical, clean `git status`), confirmed both
  subtests pass again.
- **`IF NOT EXISTS` justified, not assumed**: confirmed
  `openAtPreMigrationSchema`'s rewind boundaries
  (`fiscalSigningRenameMigrationVersion = 9`,
  `fiscalSigningSplitMigrationVersion = 11`) are both below 32, so
  migration 032 genuinely is replayed against a DB that already has the
  index during those tests.
- **Write-path cost**: acceptable — `price_history` goes from 1 to 2
  indexes; inserts happen only on an admin price change, never at
  checkout.
- Full gate: `gofmt -l .` clean, `go build ./...`, `go vet ./...`,
  `go test ./...` (full suite, all green), `golangci-lint run ./...` (0
  issues), `guard-data-access.sh`, `guard-price-history-sync.sh`,
  `guard-migration-version-collision.sh`, `guard-docs-shots.sh` — all
  green. Not a UI-surface change (no `internal/pages`/`web/` touched), so
  the UX checklist and help-manual update requirement don't apply —
  confirmed, not assumed, by checking the diff's file list.
- No hardcoded secrets; no real client/shop name in test data (`i1`/`v1`/
  `Item 1`/`V1`/`ph1`).

## Explicitly deferred (non-blocking, noted for future reference — no card filed, pure optimizations with no correctness impact)

- A partial index (`WHERE variant_id IS NOT NULL`) would drop the ~half
  of `price_history` rows that are item-keyed with a `NULL` `variant_id`.
- A covering index (`variant_id, starts_at, ends_at, price`) would make
  the subquery index-only (no row lookup).
- The item-side index (`idx_price_history_item`, `(item_id, variant_id)`)
  would sort better as `(item_id, starts_at)` for the same reason this
  card fixed the variant side — out of this card's stated scope.
