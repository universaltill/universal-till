# Code review — ut-docs#1342: precomputed local-date columns for report queries

- **Date:** 2026-09-06
- **Branch:** `fix/1342-report-query-local-date-column` (reviewed at WIP commit `5f06328`, diffed against `6ab0884`)
- **Reviewer:** independent review, Opus, fresh context, isolated worktree (implementer was a different, cheaper model)
- **Original verdict:** ❌ NOT safe to merge — 2 blocker-class findings, both empirically reproduced (F1, F2), plus one should-fix (F3) and two nits (F4, F5).
- **Final verdict, after fixes below: ✅ SAFE TO MERGE.**

---

## Post-review fix-up (same cycle, orchestrating session)

All five findings addressed, each independently TDD-verified (temporarily
reverted, confirmed the new/existing test fails with the exact reported
error, restored, confirmed green again) before being accepted as fixed:

- **F1 (archive round-trip drops local_date/voided_local_date)** — fixed in
  migration 007 itself (added `local_date`/`voided_local_date` to
  `sales_archive`, `local_date` to `payments_archive` and
  `worker_allocations_archive`, plus backfill UPDATEs for all three archive
  tables) and in `internal/data/reset_archive_repo.go`'s `resetArchiveTables`
  explicit column lists. New regression test
  `TestResetThenRestoreRoundTrip_LocalDateColumns` (`internal/data/reset_test.go`)
  pins the full archive→restore round-trip for all three tables AND asserts
  `EndOfDay` can find the restored sale again by date range — reverting just
  the column-list fix reproduces the reviewer's exact symptom (`local_date`
  reads back `""`). Also found and fixed the same gap in `ui_smoke_test.go`'s
  hand-rolled `sales_archive`/`payments_archive`/`worker_allocations_archive`
  fixtures (a third copy of the same schema, exercised by
  `internal/pages/data_api_test.go`'s reset/restore/purge handler tests,
  caught by the full-suite re-run after the first fix).
- **F2 (`SetSaleProvenance` leaves `local_date` stale on LAN-sync journal-in)**
  — fixed: it now also sets `local_date = COALESCE(date(?, 'localtime'), '')`
  from the same origin `createdAt` it stamps into `created_at`. New test
  `TestEndOfDay_FindsJournaledSaleOnOriginDayNotIngestDay`
  (`internal/data/pos_repo_lifecycle_test.go`) reproduces the reviewer's exact
  day-boundary scenario end-to-end (`EndOfDay` on the origin day finds the
  sale, ingest day does not); `TestSaleExistsAndSetSaleProvenance` extended to
  assert `local_date` directly. Reverting the fix reproduces both failures
  exactly as the reviewer's manual probe reported.
- **F3 (backfill/write aborts on a malformed timestamp)** — fixed: every
  backfill UPDATE in migration 007 (live and archive tables) and every
  write-path `date(?, 'localtime')` call (`InsertSale`, `InsertPayment`,
  `InsertWorkerAllocation`, `SetSaleProvenance`) now wraps in
  `COALESCE(..., '')`. `UpdateSaleStatus` was deliberately left unwrapped —
  its `voided_at`/`voided_local_date` input is always a Go-formatted
  `time.Now().UTC().Format(time.RFC3339)`, never external/malformed input.
  New tests `TestReportQueryLocalDate_BackfillToleratesMalformedTimestamp`
  and the corrected `TestReportQueryLocalDate_BackfillRecomputesFromSourceColumn`
  (`internal/db/report_query_local_date_test.go`) reproduce the exact
  `NOT NULL constraint failed: sales.local_date` error the reviewer found
  when the guard is removed.
- **F4 (duplicated `<expr>` assertion block)** — the second copy deleted
  from `internal/db/report_query_indexes_test.go`.
- **F5 (TZ-fragile backfill assertion)** — fixed: added a `ctrlDay` helper
  (mirrors this codebase's existing `b8ExpectedDay`/`eod1012Day` control-query
  idiom) and every assertion in `report_query_local_date_test.go` that checks
  an actual `local_date`/`voided_local_date` value now computes its
  expectation via `SELECT date(?, 'localtime')` against the real DB instead
  of a hardcoded literal, so it holds on any host timezone.

**Full gate re-run after all fixes**: `go build ./...`, `go vet ./...`,
`gofmt -l .` (clean), `golangci-lint run ./...` (0 issues), `go test ./...`
(full repo, 0 FAIL), `guard-data-access.sh`, `guard-migration-version-collision.sh`,
`guard-i18n.sh` — all green. See the original findings below, preserved
verbatim as the record of what was actually caught.

---

## What shipped

1. **New migration `internal/db/migrations/007_report_query_local_date_columns.sql`** — adds
   `sales.local_date` (NOT NULL DEFAULT `''`), `sales.voided_local_date` (nullable),
   `worker_allocations.local_date`, `payments.local_date`; backfills all four from
   `created_at`/`voided_at`/`allocated_at`/`paid_at`; adds 4 composite indexes
   (`idx_sales_status_local_date`, `idx_sales_status_voided_local_date`,
   `idx_worker_allocations_source_local_date`, `idx_payments_local_date`).
2. **5 read sites rewritten in `dateRangeSummary`** (`internal/data/pos_repo.go`) from
   `date(col,'localtime')` to plain column comparisons, including an OR-split of the
   cancellations query that previously used `COALESCE(voided_at, created_at)`.
3. **4 read sites rewritten in `internal/data/worker_allocation_repo.go`**
   (`WorkerAllocationsSummary` × 3, `ListWorkerAllocations`).
4. **Write paths populate the new columns** — `InsertSale`, `InsertPayment`,
   `InsertWorkerAllocation`, `UpdateSaleStatus` — via `date(?, 'localtime')` bound to
   the same timestamp parameter already being written.
5. **No SQL trigger** (design choice; see verification below).
6. Hand-rolled test schemas and raw INSERT fixtures across `internal/data`,
   `internal/pages`, `internal/pos`, plus `scripts/smoke_quickstart` updated.
7. New tests in `internal/db/report_query_local_date_test.go` (EXPLAIN QUERY PLAN
   sargability × 5 shapes, backfill, ordinary-writes).

---

## Automated checks — exact results

| Check | Result |
|---|---|
| `go build ./...` | exit **0**, no output |
| `go vet ./...` | exit **0**, no output |
| `gofmt -l .` | exit **0**, **no files listed** |
| `golangci-lint run ./...` | exit **0** — `0 issues.` |
| `go test ./...` (full suite) | exit **0** — 65 result lines, **0 `FAIL`**, `internal/data` ok 44.6s, `internal/db` ok 4.2s, `internal/pages` ok, `internal/pos` ok |
| `bash scripts/ci/guard-data-access.sh` | exit **0** — `✓ data-access guard: no inline SQL outside internal/data / internal/db` |
| `bash scripts/ci/guard-migration-version-collision.sh` | exit **0** — `✓ migration-version-collision guard: every internal/db/migrations/*.sql version number is unique` |
| `bash scripts/ci/guard-i18n.sh` | exit **0** — 1439 template keys resolve, all locales match |

**The green suite is exactly the problem.** Every finding below is invisible to
`go test ./...` because no existing test exercises the two code paths involved.

---

## Findings

### 🔴 F1 — BLOCKER. Reset → Restore silently erases `local_date`; EOD then under-reports revenue

`internal/data/reset_archive_repo.go`, `resetArchiveTables` (lines ~144–155).

The reset/restore archive round-trip copies **explicit column lists** between a live
table and its `*_archive` twin. Migration 007 adds four new columns to `sales`,
`payments` and `worker_allocations` but adds **nothing** to `sales_archive`,
`payments_archive`, `worker_allocations_archive`, and does **not** extend the three
`cols` strings in `resetArchiveTables`.

Consequence: `Settings → Data → Clear transaction history` followed by
`Restore` re-inserts every sale with `local_date` falling back to the column
DEFAULT `''` and `voided_local_date` NULL. The restore **succeeds silently** (NOT NULL
is satisfied by `''`), and every rewritten report query then drops those rows —
`local_date BETWEEN date(?) AND date(?)` never matches `''`.

Before this change the same round-trip was harmless, because the reports read
`date(created_at,'localtime')` and `created_at` **does** round-trip through the archive.
This is a straight regression, and it turns real takings into zero with no error.

**Reproduced** (temporary probe in `internal/data`, since deleted):

```
BEFORE reset/restore: SalesCount=1 Gross=1000
AFTER restore:        created_at="2026-03-10T12:00:00Z" local_date=""
AFTER reset/restore:  SalesCount=0 Gross=0
```

This is the third instance of a known bug class in this repo — `reset_test.go:280-283`
already documents it: *"must round-trip through sales_archive exactly like every earlier
ALTER … the reviewer finding that caught held_sales_archive missing table_id
(055_held_sales_archive_table_id.sql) applies here"*. Migrations 055 (`table_id`) and 056
(`tracking_token`) were both caught by review for exactly this. The precedent and the
pinning tests exist; this change did not follow them.

**Fix:** add `local_date`/`voided_local_date` to `sales_archive`, `local_date` to
`payments_archive` and `worker_allocations_archive` (in migration 007, since it is not
yet merged), extend the three `cols` strings in `resetArchiveTables`, and add a
round-trip regression test alongside the existing `table_id`/`tracking_token` ones in
`internal/data/reset_test.go`.

---

### 🔴 F2 — BLOCKER. LAN-sync journal-in leaves `local_date` stale; a synced sale is booked to the wrong business day

`internal/data/pos_repo.go:3615` `SetSaleProvenance`, called from
`internal/pages/sync_sales.go:349` (`applyJournal`).

```go
`UPDATE sales SET till_id = ?, created_at = ? WHERE id = ?`
```

The journal-in path is: `pos.CompleteSale` → `InsertSale` with `CreatedAt = now`
(the *receiving* till's clock — `internal/pos/sales.go:773`), which now also writes
`local_date = date(now,'localtime')`; then `SetSaleProvenance` **overwrites
`created_at` with the origin till's real timestamp and leaves `local_date` untouched**.
The file's own doc comments confirm both halves of this ("CompleteSale wrote 'now'";
"writes it VERBATIM over sales.created_at, clobbering the real timestamp CompleteSale
just wrote"; `pos_repo.go:5143` "stamped … along with the ORIGIN's created_at").

So a journaled-in sale now carries `created_at` = the real sale time but `local_date` =
the **ingest** day. Whenever ingest crosses a local-calendar-day boundary — a replica
that loses LAN connectivity in the evening and reconnects next morning, or a 23:58 sale
pushed at 00:01 — the sale is **removed from the correct day's EOD and added to the
wrong day's**. Two days are wrong at once, silently. This is the product's core
offline-first scenario, not an edge case.

**Reproduced** (temporary probe, since deleted):

```
journaled-in sale: created_at="2026-09-05T17:37:38Z" local_date="2026-09-06" (true local day "2026-09-05")
EndOfDay(2026-09-05).SalesCount = 0
```

**Fix:** `SetSaleProvenance` must also write
`local_date = date(?, 'localtime')` bound to the same `createdAt` parameter, with a
regression test asserting `EndOfDay(origin day)` still finds a journaled-in sale.

**Corollary (resolves once F1/F2 are fixed):** the change introduces a *mixed*
day-convention inside one report. `dateRangeSummary` now uses `local_date`, while the
same Z-report's `DepartmentsForDay` / `ArticleGroupsForDay` (pos_repo.go:896–1274, doc
comment: *"used by the EOD Z-report"*), `DayTotal` (:1679) and `ListSalesJournal`'s Day
filter (:6531) still use `date(created_at,'localtime')`. While F1/F2 stand, the Z-report
header total will not equal the sum of its own department lines for an affected sale,
and the sell-screen day total will disagree with the Z-report. Once `local_date` is
guaranteed to track `created_at` on every path, the two conventions agree again.

---

### 🟠 F3 — SHOULD-FIX. Backfill aborts the migration (till will not start) on a malformed timestamp

Migration 007 lines 58–61:

```sql
UPDATE sales SET local_date = date(created_at, 'localtime');
```

`date()` returns **NULL** for an unparseable input, and `local_date` is
`NOT NULL DEFAULT ''` — so a single bad row fails the statement.

**Reproduced** (temporary probe in `internal/db`, since deleted):

```
date('not-a-timestamp','localtime') -> valid=false value=""
backfill UPDATE against a malformed created_at -> err = constraint failed: NOT NULL constraint failed: sales.local_date (1299)
```

`applyMigration` is transactional, so the DB is not left half-migrated — but the error
propagates out of `Open()` and **the till will not start**. This is not hypothetical:
`internal/pages/sync_sales.go:52-72` documents that a missing/malformed `created_at`
from a peer's journal *"was silently corrupting the sale's actual creation time"* until
ut-docs#647 tightened it, so a till that ran a pre-#647 build can hold exactly such a row
today. Empty-string `created_at` behaves the same way.

**Fix:** wrap all four backfills in `COALESCE(date(<col>,'localtime'), '')`.

Same root cause on the write side, worth folding in: `InsertSale`'s
`validateRequired` only checks that `CreatedAt` is non-empty, so a malformed-but-non-empty
`CreatedAt` now makes the whole sale INSERT fail where it previously succeeded — a new
way for a checkout to hard-fail. Low practical risk (`CompleteSale` always formats a
real `time.Now()`, and `applyJournal` validates RFC3339 since #647), but it is a new
failure mode on the checkout path and cheap to defend against.

---

### 🟡 F4 — NIT. Duplicated assertion block

`internal/db/report_query_indexes_test.go:169-174` — the
`if strings.Contains(full, "<expr>")` guard is pasted **twice, verbatim**, back to back.
Harmless but dead; delete the second copy.

Related (not a defect, but worth recording): the same test's assertion was **weakened**
from `"idx_sales_status_created_dt (status=?)"` to just `"(status=?)"`, because migration
007 adds more `(status, …)` composite indexes and the planner's choice among
equally-unhelpful ones is now non-deterministic. The rationale in the comment is sound,
and I confirmed migration 076's own positive test
(`TestReportQueryIndexesMakeDateRangePredicatesSargable`) still passes — so no #1319
regression — but the test does prove strictly less than it did.

### 🟡 F5 — NIT. Timezone-fragile assertion in the backfill test

`TestReportQueryLocalDate_BackfillRecomputesFromSourceColumn` seeds
`created_at = '2024-06-01T09:00:00Z'` and asserts `local_date == "2024-06-01"`. That
holds only for host offsets in roughly (UTC-9, UTC+14]. Fine on UTC CI and in the
Turkey/UK/Germany target markets; it would fail for a developer in Hawaii. Either use
`12:00:00Z` for a wider margin, or compute the expectation from the DB
(`SELECT date(?, 'localtime')`) the way `b8ExpectedDay` already does in
`internal/data/pos_repo_batch8_reports_test.go`.

---

## What I verified beyond the automated tests

### Independent revert-then-restore TDD verification (index dependency)

I commented out exactly **one** index in the migration —
`CREATE INDEX idx_worker_allocations_source_local_date` — and re-ran only the
sargability test.

**With the index disabled** (real assertion failure, not a build error; the three
sibling subtests still passed, so the coupling is index-specific):

```
--- FAIL: TestReportQueryLocalDate_SalesAndWorkerAllocationRangesAreSargable
    report_query_local_date_test.go:105: query plan = "SEARCH worker_allocations USING INDEX idx_worker_allocations_source (source_type=?)",
        want it to use index idx_worker_allocations_source_local_date (sargable) not a full scan or a less-selective index
    --- PASS: .../sales_status+local_date_range
    --- PASS: .../sales_status+local_date_equality
    --- FAIL: .../worker_allocations_source_type+local_date_range
    --- PASS: .../payments_local_date_range
```

Note the fallback plan is `idx_worker_allocations_source (source_type=?)` — precisely the
non-sargable "equality-only search, date reduced to a residual filter" shape the card is
about, so the test measures the right thing.

**After restoring the index** (`git diff --stat` clean):

```
ok  github.com/universaltill/universal-till/internal/db  0.243s
```

The implementer's TDD claim is genuine, and the substring assertions cannot pass by
accident (`"idx_sales_status_local_date"` is not a substring of either
`"idx_sales_status"` or `"idx_sales_status_voided_local_date"`).

### OR-split equivalence for the cancellations query — traced by hand

Original: `date(COALESCE(voided_at, created_at),'localtime') BETWEEN date(?) AND date(?)`
New: `(voided_local_date IS NOT NULL AND voided_local_date BETWEEN …) OR (voided_local_date IS NULL AND local_date BETWEEN …)`

| Case | Original evaluates | New branch taken | Equivalent? |
|---|---|---|---|
| `voided_at` set (so `voided_local_date` set) | `date(voided_at,'localtime')` | branch 1 → `voided_local_date` | ✅ |
| `voided_at` NULL (so `voided_local_date` NULL) | `date(created_at,'localtime')` | branch 2 → `local_date` | ✅ |

The two branches are mutually exclusive and jointly exhaustive on
`voided_local_date IS NULL`, so **no double-count and no dropped row** — the split is
logically exact. It is correct *conditional on the invariant*
`voided_local_date IS NOT NULL ⟺ voided_at IS NOT NULL` and
`local_date = date(created_at,'localtime')`. **F1 and F2 are exactly the two places that
invariant is broken**, and note that in the F1 case the fallback branch fires and matches
`''` — i.e. the "fail visible" intent of the original COALESCE is defeated: the row
vanishes from every window rather than landing on some day. `internal/data/pos_repo_eod_1012_test.go`
does cover the both-NULL fallback case, which is good.

### Write-path placeholder/argument alignment — counted by hand

| Method | Columns | Value expressions | Go args | Aligned? |
|---|---|---|---|---|
| `InsertSale` | 28 | 28 (26 `?`, one literal `'completed'`, one literal `0` for `rounding`, one `date(?,'localtime')`) | 26 | ✅ `rounding` stays literal `0`; trailing args are `Note, CreatedAt, CreatedAt, CreatedAt` for `note/created_at/completed_at/local_date` |
| `InsertPayment` | 16 | 16 | 16, ending `paidAt, paidAt` | ✅ |
| `UpdateSaleStatus` | — | 6 placeholders | `status, status, now, status, now, saleID` | ✅ CASE WHEN mirrors the existing `voided_at` one exactly |

No off-by-one. `UpdateSaleStatus`'s `ELSE voided_local_date END` correctly preserves the
old value on a non-void status change, matching the existing `voided_at` semantics.

### The "no trigger" architectural judgment call — independently verified as correct

Read `internal/db/db.go:452-490`. `splitStatements` splits on **every `;` outside a
single-quoted literal**, with no awareness of `BEGIN … END`. A
`CREATE TRIGGER … BEGIN UPDATE …; END;` body would therefore be cut at the internal
semicolon, yielding an incomplete `CREATE TRIGGER … BEGIN UPDATE …;` — which is exactly
the `"incomplete input"` error the implementer reported. Its own doc comment says so:
*"No migration uses triggers or BEGIN…END blocks (checked 2026-09-02); if one ever does,
this splitter must learn them first."*

**The claim is accurate and the decision is right.** Teaching the splitter `BEGIN…END`
is shared-infrastructure surgery well outside this card. The write-path design stands —
what it needs is *complete* write-path coverage (F1, F2), not a different design.

### Test-fixture audit (repo-wide, structural, not "did tests pass")

Grepped every `CREATE TABLE sales|payments|worker_allocations` and every raw
`INSERT INTO sales|payments|worker_allocations` in the repo, then checked each against
whether it is exercised by an affected query or an affected write-path method.

Correctly updated: `internal/pages/ui_smoke_test.go`, `internal/pages/pos_status_test.go`,
`internal/pos/sales_test.go`, `internal/pos/offline_resilience_test.go`,
`internal/pos/performance_test.go`, `internal/data/pos_repo_batch8_reports_test.go`,
`internal/data/pos_repo_cash_recon_test.go`, `internal/data/pos_repo_eod_1012_test.go`,
`internal/data/pos_repo_lifecycle_test.go`, `internal/data/worker_allocation_repo_test.go`,
`internal/pages/eod_tax_bands_test.go`, `internal/pages/reports_page_test.go`,
`scripts/smoke_quickstart/main.go`.

Not updated, and **correctly so** (verified individually, not assumed):

- `internal/pos/shifts_test.go:652,676` — hand-rolled `sales`/`payments`; the file never
  calls `CompleteSale`/`InsertSale`/`InsertPayment`/`EndOfDay`, so the columns are unreachable.
- `internal/data/pos_repo_sync_test.go:20` — sync-queue-shaped `sales` fixture; only
  `ListQueuedSales`/`BumpSaleSyncAttempt` touch it.
- `internal/data/fiscal_chip_repo_test.go:22` — 3-column `sales`; only `LatestLocalSaleID`.
- `internal/testsupport/sqlite_catalog.go:31,169,188` — minimal catalog fixture, no
  report or write-path use.
- `internal/pages/reports_page_test.go:878`'s `INSERT INTO sales` — the tip received-side
  query joins sales only on `s.status`, never `s.local_date`, so it genuinely does not need it.
- The `*_archive` fixtures in `ui_smoke_test.go` — consistent with the production archive
  schema, which is itself the F1 gap.

**No missed fixture found.** The one real gap is production code (F1), not a test fixture.

### Other checks requested

- **Rolling upgrade / mixed-version LAN sync:** LAN sync replays *journal entries through
  `CompleteSale`*, it does not replicate SQLite rows between tills — each till owns its own
  DB file, so there is no "old-code till writes `local_date=''` into a new-code till's DB"
  scenario. The old-code-writes-a-blank-column risk does not exist. The *real* sync hazard
  is F2, which is present even when every till runs new code.
- **Money handling:** no `money.Money` arithmetic changed; all edits are SQL text and
  argument lists. No sign errors, no double-count (see the OR-split table above). The money
  risk in this diff is entirely *omission* of rows (F1, F2), not miscomputation.
- **Missing `os.MkdirAll` before a file write:** not applicable — the diff contains no
  `os.Create`/`os.WriteFile`/`MkdirAll`. Checked explicitly.
- **cwd-relative path where `paths.Data(...)` belongs:** not applicable — the only paths in
  the diff are `filepath.Join(t.TempDir(), …)` in tests. Checked explicitly.
- **Real client/shop names as demo/seed/test data:** none. Every added literal is generic
  (`s1`, `wa1`, `p1`, `R-1`, `c1`, `cash`, `tip`, `GBP`, `Worker One`, `2024-06-01`). Clean.
- **UX guidelines / help-manual rule:** **explicitly not applicable, and deliberately not
  checked.** This is a backend/data-layer-only diff — `web/` is untouched, no route is added
  or changed, no locale key is added, no screen changes. `reference/ux-guidelines.md` and the
  "manual ships with the feature" rule have no surface here. (`guard-i18n.sh` was run anyway
  and passes.)

---

## Scope: what was deliberately left unfixed

`SalesForTaxBands` (pos_repo.go ~3037/3051), the day-only report functions
(`ArticleGroupsForDay`, `DepartmentsForDay`, and siblings at ~896–1274), the
`busyBuckets` family (~2001–2013), `DayTotal` (~1679), the shift-close queries
(~2308–2387) and `ListSalesJournal`'s day filter (~6531) all still use
`date(col,'localtime')`.

**I agree this is legitimately out of scope and it should not block merge.** The card
named `dateRangeSummary` and `worker_allocation_repo.go`'s four queries, and this mirrors
the precedent set by PR #1319 / migration 076, which fixed some sites and filed the rest
(the `report_query_indexes_test.go` guards documenting "still NOT fixed" are that
precedent in code). The comment updates in this diff keep those guards internally
consistent.

**Recommend filing a follow-up card** covering `SalesForTaxBands`, the day-only report
functions, `busyBuckets`, `DayTotal` and `ListSalesJournal`'s day filter — with the note
that once F1/F2 are fixed the mixed convention is merely a performance gap, but until the
whole file converges on `local_date` the codebase carries two day-definitions that must be
kept in sync by hand.

---

## Verdict

❌ **Blocker-class issues found — do not merge as-is.**

The core design is right: the SQLite `'localtime'` non-determinism analysis is correct,
the trigger-avoidance judgment call is verified correct against `splitStatements`' actual
source, the OR-split is logically exact, the indexes are genuinely load-bearing (I proved
it by revert/restore), the argument lists line up, and the test-fixture sweep is complete.

What it is missing is **two write paths**. Moving a conversion from read time to write time
is only sound if *every* path that materialises or mutates the source column also
maintains the derived one. Two do not:

- **F1** — the reset/restore archive round-trip (drops the value entirely → zeroed EOD),
- **F2** — `SetSaleProvenance` on LAN journal-in (leaves the value stale → wrong business day).

Both are silent money-correctness bugs in a fiscal report, both are invisible to the
current green suite, and both were reproduced here with concrete numbers. **F3** should be
fixed in the same pass (it can prevent a till from starting after upgrade), and **F4/F5**
are cosmetic.

Re-review after F1–F3, with a regression test for each: an archive round-trip test beside
the existing `table_id`/`tracking_token` ones in `internal/data/reset_test.go`, and an
`EndOfDay`-after-`SetSaleProvenance` test in the sync/EOD tests.
