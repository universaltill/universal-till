# Code review — short customer-facing order number (ut-docs#1817)

**Date:** 2026-09-08
**Branch:** `feat/1817-short-order-number`
**Reviewer:** independent review pass (Opus, different model from the Sonnet
build), worktree-isolated, did not write the implementation under review.
**Verdict:** **safe to merge** after 3 findings fixed below (all applied and
verified in this branch).

---

## What shipped

A short, customer-meaningful order number (`sales.display_no`), separate
from `receipt_no` — `receipt_no` is unchanged: still the permanent
fiscal/audit identity (ADR-0042), the scan-to-refund barcode target, and
the primary key every existing feature already addresses a sale by.

- **`internal/db/migrations/014_sale_display_no.sql`** — `ALTER TABLE
  sales ADD COLUMN display_no TEXT` on both `sales` and its archive twin
  `sales_archive` (see `internal/db/migrations/001_init.sql`'s own
  column-identical requirement). Nullable, no backfill: an old row falls
  back to its `receipt_no` everywhere via
  `COALESCE(NULLIF(display_no,''), receipt_no)`.
- **`internal/data/pos_repo.go`** — `NextDisplayNo(ctx, tx)`, mirroring
  `NextReceiptNo`'s existing shape (reads `sync.receipt_prefix` for
  multi-till collision safety, same `MAX(...)+1` allocation inside the
  caller's own transaction — offline, no network round-trip). Two
  schemes, via a new `sale.display_no_scheme` setting:
  - `trading_period_reset` (default) — resets after each till close, the
    SAME `report_archive`/`"eod"` boundary `generateEOD` itself uses
    (ADR-0066 Decision 6). Matches researched competitor precedent
    (Square ships exactly this; SumUp/ready2order/Lightspeed all show
    short per-day-or-shift numbers).
  - `lifetime_no_reset` — never resets. A genuine "rolling/wrapping"
    third scheme (the card's own example) was deliberately NOT shipped —
    see the doc comment on `DisplayNoSchemeLifetimeNoReset`: a
    `MAX(display_no)`-derived counter over an archive-only table
    (ADR-0042, rows never deleted) cannot correctly wrap, since the old
    high-water row keeps winning `MAX()` forever. A real wrapping scheme
    needs its own persistent counter row — different design, tracked as
    a follow-up rather than shipped broken.
- **`internal/pos/sales.go`** — `CompleteSale` allocates `display_no` in
  the exact same transaction as `receipt_no`; `pos.SaleInput.DisplayNo`
  threads a pre-given value straight through (mirrors `ReceiptNo`) so a
  synced/replayed journal (`internal/pages/sync_sales.go`) reuses the
  originating till's number rather than re-deriving an independent one.
- **Display surfaces** (all via the existing `GetSaleDetail`/
  `ListRecentOrders`/`ListRecentOrdersForStation`/
  `LookupOrderByTrackingToken` COALESCE, so every reader gets a
  ready-resolved value): self-order kiosk confirmation
  (`self_order_shop.go`), kitchen ticket (`kitchen_print.go`), `/orders`
  queue and the per-station kitchen-display fragment
  (`order_status.go`'s `orderRow`/`orderRowsFor`, `orders_list.html` —
  link target and element ids stay `receipt_no`, only the visible text
  changes), `/o/{token}` tracking page (`order_tracking.go`,
  `order_tracking.html`), and the printed customer receipt
  (`print_api.go` — an ADDITIONAL `"Order " + displayNo` line, only when
  it differs from `receipt_no`; `receipt_no` keeps printing too).
- **Cross-till proxy path** (`internal/pages/sync_orders.go`'s
  `syncOrderRow` wire struct, used when a replica renders the primary's
  live order board over HTTP) gets its own `display_no` field, with an
  explicit Go-side fallback-to-`ReceiptNo` in `fetchOrdersFromPrimary`
  for an older primary that omits it — this path bypasses the SQL-layer
  COALESCE entirely, so the fallback has to be re-stated in Go.
- **Settings** — new "Order numbers" card (`web/ui/pages/settings.html`),
  elevation-gated + audited like every other setting on that page
  (`POST /api/settings/order-no-scheme`, `settings_page.go`).
- **i18n** — 7 new keys (`settings.order_no.*` ×5, `orders.col.order_no`,
  `elevation.summary.order_no_scheme`) across all 4 core locales
  (en/ar/fa/tr), real translations, not English copy-paste.
- **Manual** — `web/help/{en,ar,de,fa,tr}/order-status.md` updated to
  describe the short number and the new setting; `make docs-shots`
  re-run (surface `76f424fe5a66…`).

## Independent review

Spawned via `Agent` (Opus, `isolation: "worktree"`) against a WIP commit
on this branch — genuinely independent read, didn't see the build
reasoning. Full report in the session transcript; summary here.

**Verdict as delivered: NOT SAFE TO MERGE — 2 blockers, later found a 3rd
via re-verification, all three fixed:**

1. **B1 (blocker) — the default scheme never actually reset.**
   `report_archive.created_at` is written in `archiveTimestampFmt`
   (`"2006-01-02 15:04:05"`, space-separated — the schema's own
   `datetime('now')` default and `ArchiveReport`'s explicit write both
   use this shape), while `sales.created_at` is `time.RFC3339`
   (`"...T...Z"`). `NextDisplayNo`'s original `since` bound compared
   these two shapes as raw TEXT: `'T'` (0x54) sorts after `' '` (0x20),
   so `created_at > since` read true for any same-calendar-day sale
   regardless of its actual time — the counter never returned to 1,
   silently degrading `trading_period_reset` into
   `lifetime_no_reset`'s own behaviour. Caught by the reviewer seeding
   `report_archive` in the real production format and observing the
   counter keep climbing across closes instead of resetting.
   **Fix:** read the raw archive timestamp via `exec(tx)` (never `r.db`
   — see finding 3 below), parse it with the SAME layout
   `LatestArchivedAt` itself uses (`archiveTimestampFmt`), then
   re-format to RFC3339 before binding it against `sales.created_at` —
   exactly what a safe-to-call `LatestArchivedAt` would have handed
   back. Verified: `TestPOSRepo_NextDisplayNo_Sequencing` now seeds
   `report_archive` in the real (space-separated) format, asserts the
   post-close count on the SAME calendar day as the close (the exact
   case the bug got wrong), and adds a second assertion that a sale
   created *after* the close is still counted (the reset isn't just
   excluding everything).
2. **B2 (blocker, root cause of why B1 shipped) — the original repo
   test seeded the wrong timestamp format**, matching `sales.created_at`
   instead of `report_archive.created_at`'s real shape — a false-positive
   test that "proved" the reset worked while production did the
   opposite. Fixed together with B1 (same test, corrected seed).
3. **B3 (blocker, CI-blocking) — `guard-docs-shots.sh` was red.**
   `make docs-shots` had been run before the `orderRow.DisplayNo`
   template-field fix (below) landed, so the committed screenshots were
   stale relative to the final `internal/pages`/`web/ui` surface.
   **Fix:** re-ran `make docs-shots` after all code changes settled;
   guard is green (`surface 76f424fe5a66…`, matches the reviewer's own
   independently-computed "current" hash).

**Should-fix, both applied:**

4. **S1 — no regression test, in the package that owns the bug, for the
   deadlock this diff's own `NextDisplayNo` doc comment describes**
   (`r.LatestArchivedAt` always queries via `r.db`, never `exec(tx)`;
   calling it from inside `CompleteSale`'s own transaction on a
   single-connection pool deadlocks — `r.db`'s second query blocks
   forever waiting for a connection the in-flight transaction will never
   free). The thing that actually caught this live was a 10-minute
   `internal/pages` test-suite timeout, in a different package, for a
   reason (`SetMaxOpenConns(1)`) that has nothing to do with
   `internal/data` owning the bug. **Fix:** added
   `TestPOSRepo_NextDisplayNo_DoesNotDeadlockInsideTxOnSingleConnectionPool`
   to `internal/data`, restricting the pool itself
   (`d.DB.SetMaxOpenConns(1)`) so the regression is caught here, in
   ~5s, with a message naming the actual failure mode. Verified
   TDD-style: reverted the fix (swapped back to `r.LatestArchivedAt`),
   confirmed this new test fails at the 5s timeout with the expected
   message, restored the fix, confirmed green again.
5. **S2 — `lifetime_no_reset` runs an unindexed `MAX(...)` scan of
   `sales` per checkout.** Not a correctness bug (the default scheme
   doesn't pay this cost — bounded by the trading-period `WHERE`), and
   this is an opt-in, not the default. Documented as an accepted
   trade-off on `DisplayNoSchemeLifetimeNoReset`'s own doc comment
   rather than adding an index pre-emptively for a scheme nobody may
   ever choose.

**Nitpicks, read and accepted, no change needed:**
- `orders.col.receipt` is now unused in `en/ar/fa/tr.json` (its only
  template consumer switched to `orders.col.order_no`) — cosmetic,
  `guard-i18n.sh` doesn't flag unused keys, left in place rather than
  risking an unrelated removal.
- `self_order_shop.go`'s kiosk-confirmation path now calls the heavier
  `GetSaleDetailByID` (header+lines+payments) instead of the narrower
  `SaleTotals`, just to read two strings — correct (only
  `GetSaleDetailByID` resolves `DisplayNo`), a bit more work than
  strictly needed on a low-frequency path. Not worth a second repo
  method for this diff's scope.
- `print_api.go`'s new `"Order " + displayNo` receipt line follows that
  file's existing, pre-dating hardcoded-English convention rather than
  fixing the printed receipt's broader (pre-existing, unrelated) i18n
  gap — a real, separate follow-up candidate, not a defect in this diff.

## Verified beyond automated tests

- Visual check (Playwright, throwaway spec, deleted after use): the new
  "Order numbers" settings card, in English and in `fa` (RTL) — clean
  layout in both, label/select/button aligned correctly, no overlap, no
  literal left/right leakage (`class="set-row"` already gives it logical
  properties for free, same as every neighboring card).
- `make docs-shots`'s own 104-screenshot run passed, so the light-theme,
  1024×600 kiosk viewport, and all 4 locales' rendered settings/orders/
  self-order-confirmation/tracking pages are captured and hash-pinned.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh`, `guard-page-http-error.sh`,
  `guard-plugin-menu-read.sh`, `guard-kiosk-engine.sh`,
  `guard-e2e-fixtures-import.sh` — all green.
- `go build ./...`, `go vet ./...`, `gofmt -l .` (zero output),
  `golangci-lint run ./...` (0 issues) — all clean, run twice (before
  and after the review-finding fixes).
- `go test ./internal/data/... ./internal/pos/... ./internal/pages/...`
  — all green, run to completion multiple times (`internal/pages` alone
  takes ~3 minutes; not skipped).
- `go test ./...` (whole repo) — green, 0 failures across every package.

## Explicitly deferred (new Backlog candidates, not this card)

- A genuine wrapping/rolling `#01`–`#99` display-number scheme, needing
  its own persistent counter row (see `DisplayNoSchemeLifetimeNoReset`'s
  doc comment).
- A per-order-type letter-prefix scheme (`A-12` kiosk vs. `C-12`
  counter) — the card's third example, needs its own design pass on how
  it stacks with the existing per-till `sync.receipt_prefix`.
- Localizing `print_api.go`'s `buildReceiptDoc` Meta lines (pre-existing
  gap, unrelated to this card).
- `lang-pack-drift` follow-up: the 7 new `web/locales/en.json` keys need
  the matching `ut-plugin-language-{de,es}` pack update (advisory on this
  PR, blocking on `main` push — standard sequencing, not a blocker here).
