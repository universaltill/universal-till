# Code review — CompleteSale INSERT-count regression guard (ut-docs#2250)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2250 (`complexity:medium`, `p3`) — follow-up split out
  of ut-docs#1347 item 2.
- **Branch:** `fix/2250-completesale-insert-count-regression-guard`
- **Reviewer:** independent pass, Opus subagent (per this card's
  `complexity:medium` routing, `MODEL-ROUTING.md` — Dev on Sonnet, Review
  on Opus), isolated in its own git worktree (cleaned up on completion).
- **Verdict: SAFE TO MERGE** after one fix. Test-only change (zero
  production code touched); one medium finding (two of the five batched
  calls the card names were unexercised no-ops) fixed; three low/nit
  items folded in.

## The gap

ut-docs#1318 batched `CompleteSale`'s per-line repository writes
(`InsertSaleLinesBatch`, `InsertSaleLineModifiersBatch`,
`InsertSaleDiscountsBatch`, `RecordStockMovementsBatch`,
`CurrentQtyBatch`), replacing a one-exec-per-line loop. ut-docs#1347
added chunk-boundary correctness coverage for those batched methods
themselves, but explicitly deferred this item: `sales_batch_test.go`'s
integration suite only proves the *written data* is correct, and the
#1318 review confirmed the OLD unbatched code passes that same suite
identically — nothing would fail if a future edit quietly reintroduced
the per-line loop.

## What shipped

- `setupSaleDB` (`internal/pos/sales_test.go`) refactored to delegate to
  a new `setupSaleDBAtPath(t, path)` — a pure extraction exposing the
  on-disk path so a second connection can be opened against the same
  file. All 43 existing `setupSaleDB` call sites are unaffected.
- `internal/pos/sales_batch_insertcount_test.go` (new): a package-local
  copy of the `driver.Connector`-based statement-counting wrapper already
  used by `internal/data/export_repo_querycount_test.go` /
  `sync_admin_batch_test.go`'s `TestAdminApplyINSERTCountDoesNotGrowLinearlyWithRowCount`
  (ut-docs#1369 — this test's direct precedent, one repo layer up).
  `TestCompleteSale_INSERTCountDoesNotGrowLinearlyWithLineCount` seeds a
  migrated SQLite DB, closes that seed connection, opens a fresh
  INSERT-counting connection to the same file, and calls `CompleteSale`
  for a 2-line and a 50-line basket (each line carrying a modifier and a
  small line discount — see F1 below), asserting the statement-count
  growth between them stays small and flat rather than tracking line
  count 1:1.

## Independent review findings

**F1 — MEDIUM, fixed.** The first draft's basket lines carried no
modifiers and no line discount, so `InsertSaleLineModifiersBatch` and
`InsertSaleDiscountsBatch` both hit their `len(rows) == 0` early-return
on every call — two of the five batched methods the card names were
never actually exercised, so a regression in either specifically would
have been invisible to this guard. (The reviewer also noted
`CurrentQtyBatch` is a `SELECT`, outside this test's `"INSERT"` prefix
filter by design, and stays O(1) here regardless of `n` since every line
shares one item+location key — judged not worth chasing for this ticket,
since the card's own scope is the *write* batching.) Fixed: every line
now carries one modifier and a 10-minor-unit line discount, verified to
actually reach the DB (small-basket baseline INSERT count went from 7 in
the reviewer's own pre-fix measurement to 9 post-fix — the two new
per-line-once statements — with the small→large growth still flat at 1).

**F2 — low, fixed.** `countingSaleConn`'s doc comment claimed
case-insensitive prefix matching, but the constructor stored the caller's
`prefix` verbatim (only the *query* side was upper-cased) — a
lowercase-prefix caller would have silently matched nothing. Fixed by
upper-casing `prefix` in `openCountingSaleDB` itself.

**F3 — low, fixed.** The counting connection didn't replicate
`setupSaleDBAtPath`'s per-connection pragmas (`PRAGMA foreign_keys = ON`,
`SetMaxOpenConns(1)`/`SetMaxIdleConns(1)`) — SQLite pragmas are
per-connection, not persisted in the file, so the measured `CompleteSale`
call was running with FK enforcement silently OFF, unlike every other
test in this file. Fixed: `openCountingSaleDB` now sets the same pragma
and pool limits.

**F4 — low, fixed.** The seed connection's `Close()` was a bare call
after the four seed `Exec`s, not deferred — a `t.Fatalf` on any seed
statement would have left the connection open for the rest of the run.
Wrapped seeding in an immediately-invoked closure with `defer
setup.Close()`, keeping the invariant the reviewer verified matters here:
the seed connection is fully closed before the counting connection ever
opens (confirmed no window exists either way — `CompleteSale` does all
its work inside one `db.WithTx`, so even without this fix only one
connection was ever checked out at a time).

## Mutation verification (re-run personally after each fix, not taken on trust)

Two independent mutations, both re-run against the fixture as it stands
after the F1-F4 fixes above (numbers differ slightly from the reviewer's
own pre-fix measurement for this reason):

1. **`batchChunkSize` forced to `1`** (every batched multi-row INSERT
   degraded to one row per statement) — `internal/data/pos_repo.go`:
   growth jumped from 1 to **240** (small=14, large=254). Reverted;
   passes again (`git diff` on `pos_repo.go` empty).
2. **A genuinely faithful regression**, independently verified by the
   reviewer during their own pass (not re-run a third time here, since
   the reviewer's isolated-worktree run already confirmed it against the
   pre-F1 fixture and the mechanism is unchanged by F1-F4): replacing
   `InsertSaleLinesBatch`/`RecordStockMovementsBatch` with loops over the
   still-live singular `InsertSaleLine`/`RecordStockMovement` methods
   (the actual pre-#1318 shape) — growth 192 (small=11, large=203).

Both confirm the guard fires on the actual regression shape this card
exists to catch, not just on an artificial proxy.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/pos/...` and `./internal/data/...`, `-count=1` —
  green (108s for `internal/data`).
- `gofmt -l` — clean.
- `bash scripts/ci/guard-data-access.sh` — pass (the new file's SQL
  literals are seed statements in a `_test.go` file, which the guard
  exempts wholesale).
- `bash scripts/ci/guard-i18n.sh` — pass (no template/locale surface).
- No production code, UI surface, or user-facing behaviour touched —
  confirmed no i18n/help-topic/docs-shot update needed.
- No secrets, no real shop/client name in test fixtures (`itm1`, `Apple`,
  `Main`, `cash`, `reg1`, `user1`).

## Deferred / explicitly out of scope

`CurrentQtyBatch` (a `SELECT`, not counted by this test's `"INSERT"`
filter) staying O(1) here only because every line shares one stock key —
noted above, not chased further; this card's own scope (per its
description) is the *write* batching. A future card wanting SELECT-count
coverage for `CurrentQtyBatch` specifically would need a second counter
with a `"SELECT"` prefix and distinct items per line — not filed as a
separate Backlog card, since the reviewer judged it genuinely optional
against this ticket's actual acceptance bar, not a real gap.
