# Code review — ut-docs#1664: extend local_date sargability fix to remaining report queries

- **Date:** 2026-09-09
- **Branch:** `fix/1664-report-query-local-date-followups`
- **Reviewer:** independent review, Opus, fresh context, isolated worktree (implementer was Sonnet, this session's own model — `complexity:medium`).
- **Verdict: ✅ SAFE TO MERGE** (no blocker-class findings; 4 should-fix items addressed below before commit).

---

## What shipped

Follow-up to ut-docs#1342/PR#839/migration 007, whose own review deliberately
left several read sites unconverted. This converts:

1. Five day-only report functions in `internal/data/pos_repo.go`
   (`DepartmentsForDay`, `ArticleGroupsForDay`, `ArticleSalesForDay`,
   `OperatorSalesForDay`, `OrderTypeSalesForDay`):
   `date(s.created_at, 'localtime') = date(?)` → `s.local_date = date(?)`.
2. `SalesForTaxBands`'s three queries (sales header, sale_lines join,
   payments join): `date(created_at, 'localtime') BETWEEN date(?) AND date(?)`
   → `local_date BETWEEN date(?) AND date(?)` (aliased `s.local_date` on the
   two joined queries).
3. `DayTotal`: `date(created_at, 'localtime') = date(?, 'localtime', ?)` →
   `local_date = date(?, 'localtime', ?)` — confirmed this is plain
   calendar-midnight arithmetic applied to the `ref` literal (daysAgo whole
   days back), NOT the ADR-0057 business-day-start shift, so `local_date` is
   the correct, safe fit.
4. `ListSalesJournal`'s Day filter: `date(s.created_at, 'localtime') = date(?)`
   → `s.local_date = date(?)`, backed by a **new migration 021**
   (`idx_sales_local_date`, no `status` prefix — this is the one query in the
   group with no status predicate at all, so migration 007's
   `idx_sales_status_local_date (status, local_date)` composite can't serve
   it).

**Deliberately out of scope, left unconverted:** `busyBuckets`/`SalesByDay`/
`SalesByWeekday`/`SalesByHour` — these apply the ADR-0057 business-day-start
hh:mm shift **directly to `created_at`** inside their GROUP BY bucket
expression (`date/strftime(created_at, 'localtime', ?, ?)`), a materially
different, mutable-setting-dependent semantic from `local_date`'s fixed
calendar-midnight derivation. A correct fix needs its own design pass (a
business-day column that would need recomputing across history whenever the
shop's business-day-start setting changes) — this is a real design decision,
not a mechanical column swap, and stays out of this card per its own scope
note.

Also fixed: several test fixtures that raw-INSERT into `sales` without
setting `local_date` (defaults to `''` via migration 007's column default),
which would have silently broken once the queries they exercise switched
from computing the day from `created_at` to reading the precomputed column —
`seedJournalSale` (`pos_repo_batch8_sales_test.go`), two fixtures in
`pos_repo_eod_1012_test.go`, two in `journal_page_test.go`'s
`TestJournalUIFilters_TillAndDay`, and four `sale()` closures across
`alerts_test.go` feeding `DayTotal`-based unusual-sales detection.

---

## Independent review — verdict and process

Full independent Opus review in an isolated worktree: read the diff, read
the full surrounding production code (not just diff hunks), independently
re-derived every claim in the implementation brief, ran the complete gate,
and did two real revert-then-restore mutation tests against the new
sargability tests.

**No blocker-class issues.** In particular the reviewer:

- Enumerated every `INSERT INTO sales` site in the repo (245 hits) and
  traced each test that reaches one of the six converted query sites back to
  its seeding helper, confirming no fixture was missed beyond the ones
  already fixed in this diff.
- Independently verified `DayTotal`'s day-shift is calendar-midnight (safe),
  by reading the function's exact SQL and contrasting it with
  `SalesByDay`/`SalesByWeekday`/`SalesByHour`/`busyBuckets`, which apply the
  shift to `created_at` itself.
- Proved `idx_sales_local_date` is genuinely necessary by EXPLAIN-ing the
  real production query text with and without it (`SCAN … USING INDEX
  idx_sales_created` without it; `SEARCH … USING INDEX idx_sales_local_date
  (local_date=?)` with it).
- Confirmed migration 021's `IF NOT EXISTS` is required (not defensive
  paranoia) by reading `openAtPreMigrationSchema`
  (`fiscal_signing_keys_rename_test.go`), which rewinds the migration ledger
  past a version boundary and re-runs every migration above it against an
  already-migrated DB file — exposing any non-idempotent `CREATE INDEX`.
- Confirmed zero `money.Money` arithmetic changed and zero files under
  `web/` touched (diffed every top-level directory).
- Ran the full gate independently: `gofmt -l .` clean, `go build ./...`
  clean, `go vet ./...` clean, `go test ./...` full suite green,
  `golangci-lint run ./...` 0 issues, `guard-data-access.sh`,
  `guard-migration-version-collision.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh`, `guard-page-http-error.sh` all green.
- Ran two independent mutation tests, each restored and re-verified green
  afterward:
  - Commented out migration 021's `CREATE INDEX` → the new
    `ListSalesJournal` sargability test failed with a real query-plan
    assertion error (`SCAN s USING INDEX idx_sales_created`); the other
    three test families correctly stayed green (they don't depend on 021).
  - Removed migration 007's `idx_sales_status_local_date` → all three of
    the other new test families failed with real query-plan assertion
    errors; the journal test correctly stayed green.

## Post-review fix-up (same cycle)

Four should-fix findings, all addressed:

- **S1 — a smoke check in `department_report_test.go` had silently gone
  permanently dead.** Its `s1` fixture set neither `created_at` nor
  `local_date`, so `DepartmentsForDay(ctx, "now")`'s zero-row branch (which
  the test tolerates via `t.Log`, not a hard assertion) could never again be
  exercised on any host — before this change it matched via
  `date(created_at,'localtime') = date('now')` on a UTC host; after, `'' =
  date('now')` is false unconditionally. Fixed: the fixture now sets both
  `created_at` and `local_date` from the same `'now'` instant in one
  statement (so they can't disagree), and the comment was updated to
  reflect that the day match is now deterministic rather than a
  timezone-dependent smoke check.
- **S2 — this review record was missing from the diff.** Added (this file).
- **S3 — the new sargability tests only proved query-plan shape, never
  correctness, and seeded `local_date` via the same expression the
  production writer uses (tautological w.r.t. `created_at`).** Added
  `internal/data/report_query_local_date_followups_test.go`: seeds a real
  sale through the actual production writer path (`POSRepo.InsertSale` /
  `InsertSaleLine`, not raw SQL) and asserts all four converted query
  families (`DepartmentsForDay`, `SalesForTaxBands`, `DayTotal`,
  `ListSalesJournal`'s Day filter) read it back correctly for its day.
  TDD-verified by mutation: temporarily made `InsertSale`/`SetSaleProvenance`/
  `InsertPayment` write `local_date` one day off
  (`date(?, 'localtime', '+1 day')`); all four new subtests failed with real
  "sale not found" assertion errors; reverted (`diff` against the pre-mutation
  file confirmed byte-identical) and re-verified green.
- **S4 (accepted as a documented limitation, not fixed)** — the
  `internal/db` sargability tests EXPLAIN hand-written approximations of the
  production query text (e.g. the day-only case's test omits
  `deptRootsCTE` and its four joins). The reviewer independently confirmed
  the real production text plans identically, and S3's new
  `internal/data` test now covers the correctness gap this approximation
  left open (a wrong `local_date` value) — a future join/`ORDER BY` change
  regressing the *real* query's plan while these approximations stay green
  is a real but narrow residual gap, not worth the cost of exporting query
  constants for this card.

## Verification (after post-review fixes)

- `gofmt -l .` — clean
- `go build ./...`, `go vet ./...` — clean
- `golangci-lint run ./...` — 0 issues
- `go test ./...` — full suite green
- `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-migration-version-collision.sh` — both green
- New `TestReportQueryLocalDateFollowups_ConvertedQueriesFindRealWriterSeededSale`
  (4 subtests) and the 4 `internal/db` sargability tests: all pass; both
  TDD-verified by mutation as described above.

## What was NOT changed (accepted risk / explicit non-goal)

`busyBuckets`/`SalesByDay`/`SalesByWeekday`/`SalesByHour` still use
`date/strftime(created_at, 'localtime', ?, ?)` — non-sargable, but correct,
and a design decision (a business-day-shifted column) rather than a
mechanical fix. Left for a future card if/when it's worth the schema change.
