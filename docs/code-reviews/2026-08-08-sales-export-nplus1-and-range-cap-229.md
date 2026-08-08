# Code review: SalesForExport N+1 sub-queries and unbounded export range

**Ticket:** universaltill/ut-docs#229
**Date:** 2026-08-08
**Repo/branch:** `universal-till` / `fix/sales-export-nplus1-and-range-cap`
**Reviewer:** independent Opus subagent (complexity:medium tier), isolated worktree

## What shipped

`POSRepo.SalesForExport` (`internal/data/export_repo.go`) issued 1 query
for the matched sales list plus 2 extra queries **per matched sale** (tax
lines, payments) — a year-long range on a busy till could mean roughly
100k queries and a large in-memory/stdin payload on till-class (e.g. Pi)
hardware, with no cap on the requested date span at all.

- Replaced the per-sale `exportSaleTaxLines`/`exportSalePayments` loop
  with two range-scoped, joined batch queries
  (`exportSaleTaxLinesBatch`/`exportSalePaymentsBatch`) — one query each
  for the whole matched range, grouped in Go by sale ID. Same
  `status='completed' AND sale_type='sale'` + date-range WHERE semantics
  as before, now expressed via `JOIN sales s ON s.id = sl.sale_id` /
  `... p.sale_id` instead of a per-sale `WHERE sale_id = ?`.
- Added a 366-day max range cap on `POST /api/data/export`
  (`internal/pages/data_api.go`, `maxExportRangeDays`/`maxExportRange`),
  rejecting an over-long range with 400 *before* the repo layer (or the
  plugin call) is ever touched.
- Updated the `settings.data.export.help` locale string (all 4 locales)
  to mention the new range limit — a shop owner requesting an unbounded
  export now sees a rejection they never saw before, so the existing help
  text needed to say why.

### Tests (written test-first, TDD)

- `TestSalesForExport_ConstantQueryCount`
  (`internal/data/export_repo_querycount_test.go`) — opens a second
  connection to the same on-disk WAL SQLite file through a custom
  `driver.Connector`/`driver.Conn` wrapper that counts every `SELECT`
  prepared, seeds 2 then 50 sales on separate days/ID prefixes (so the
  two batches can't collide or leak into each other's range query), and
  asserts the query count stays flat and small (≤5) as sale count grows
  20x — confirmed failing against the pre-fix code (5 queries for 2
  sales, 101 for 50 — exactly the predicted `1+2n`) before the fix
  landed, passing after.
- `TestExportDispatch_DateRangeTooLarge` /
  `TestExportDispatch_DateRangeAtCap` (`internal/pages/export_dispatch_test.go`)
  — a >366-day range is rejected with the new message; exactly 366 days
  apart still succeeds (boundary inclusive).

## Independent review (round 1)

An independent Opus subagent, isolated in its own git worktree, reviewed
the diff without having seen any prior reasoning about it:

- Ran `go build`, `go vet`, `gofmt -l` on all four touched files,
  `go test ./internal/data/... -run TestSalesForExport -v`,
  `go test ./internal/pages/... -run TestExportDispatch -v`, the full
  `go test ./...` (one pre-existing unrelated failure only, see below),
  `guard-data-access.sh`, `guard-i18n.sh`, plus `guard-help-topics.sh` on
  its own initiative — all green. Also ran the query-count test at
  `-count=25` (no flakes) and `-race -count=5` on the export tests (no
  races).
- **Independently re-verified both TDD claims**: reverted just the
  production fix in `export_repo.go` (kept the new test) and reproduced
  the exact `5 → 101` growth; reverted just the range-cap check and
  reproduced the exact failure; restored both and confirmed green again.
- **Wrote and ran a differential test** (not kept — a review-time check,
  not part of the shipped diff): re-implemented the pre-fix per-sale code
  verbatim and asserted `reflect.DeepEqual` against the new batch
  implementation across 6 ranges over a deliberately awkward dataset
  (multi-band sale, repeated payment method with non-zero
  `change_given`, a sale with lines but no payments and vice versa, a
  voided sale and a return sale inside the range, boundary-day
  `23:59:59`, out-of-range either side, empty `from`/`to`, sale IDs
  sorting opposite of `created_at` order) — byte-identical output in
  every case. Confirmed no row duplication is possible (`sales.id` is
  `TEXT PRIMARY KEY`, so the join is strictly many-to-one) and confirmed
  zero-row sales produce the same nil slice both before and after.
- Confirmed money handling unchanged (the `money.FromMinor` conversions
  moved verbatim into the batch queries; no new arithmetic, no bare
  `int64` escaping the DB boundary) and i18n unaffected (the JSON
  `"message"` field class is explicitly out of `guard-i18n.sh`'s scope,
  consistent with every sibling message in this handler already being
  raw English).
- Confirmed N/A for the two recurring bug classes (missing
  `os.MkdirAll`, cwd-relative path instead of `paths.Data`) — this diff
  writes no files and adds no new paths outside a test's `t.TempDir()`.
- Confirmed no real client/shop name or secret-shaped literal in any new
  test data.

### Findings

1. **Fixed in this round**: the query-count test's lower bound was
   missing — `small != large` and `large > 5` both pass at
   `small == large == 0`, so a future change that let the counting
   wrapper's `QueryContext` bypass its own `Prepare`/`PrepareContext`
   interception (a plausible "let the optional fast path through"
   tidy-up) would make the harness silently stop counting and the test
   would keep passing even with the N+1 bug fully restored — the
   reviewer verified this concretely by making exactly that change and
   watching the test go green with the bug present. Added
   `if large < 3 { t.Fatalf(...) }` so the harness going quiet is itself
   a failure, not just the count growing.
2. **Fixed in this round**: `TestExportDispatch_DateRangeTooLarge` never
   subscribed an export handler, so removing the cap still 400s (a
   different failure — "export plugin did not respond" — happens to
   share the same status code), meaning the `rec.Code != http.StatusBadRequest`
   check alone couldn't distinguish the cap from that red herring; only
   the exact-message assertion made the test genuinely fail. Added
   `subscribeExportAsk` (matching `_DateRangeAtCap`'s own setup) so a
   missing cap now returns 200 and the status check is load-bearing too.
3. **Accepted as-is, not a bug**: the "366-day maximum" message is one
   day tighter in wording than what's actually enforced — the check is
   `toDate.Sub(fromDate) > 366*24h`, which accepts a 366-day *difference*
   (367 inclusive calendar days, verified against a leap year too). The
   production code's own comment already documents this as intentional
   slack ("366 covers a full calendar year ... plus one day of slack");
   the direction is safe (generous, never rejects a legitimate full-year
   export) so left as-is rather than tightened or renamed.
4. **Fixed in this round**: the new rejection is genuinely user-visible
   (`web/ui/pages/settings.html` renders the JSON `message` field
   verbatim into `#export-msg`), and the standing product-owner rule
   requires the manual to stay current with anything a shop owner sees.
   There was no existing `{{ helpLink }}`-backed topic for the export
   section to begin with (a pre-existing gap this change inherits, not
   one it created — confirmed via `guard-help-topics.sh`, which checks
   route coverage, not content freshness) — cheapest in-scope fix was to
   append the limit to the existing `settings.data.export.help` locale
   value in all 4 locales (en/ar/fa/tr) rather than open a new topic for
   a single sentence.
5. **Deferred, filed as a new Backlog card**: the cap bounds elapsed
   *time*, not row count or payload size — 366 days of a busy till's
   sales is still potentially six figures of rows loaded into one
   in-memory slice and marshalled whole into the WASM guest's stdin on
   Pi-class hardware. Out of ut-docs#229's stated scope (N+1 + a range
   cap specifically), so filed as a follow-up rather than expanded into
   this diff.

No blocker-class issue (money/tax, data loss, security) — no second
review round; both fixable findings were mechanical (test hardening,
locale copy) and applied directly rather than re-running the whole
review.

## Verification performed (this session, after applying the review's fixes)

- `go build ./...`, `go vet ./...` — clean.
- `gofmt -l` on the touched Go files — clean; all 4 touched locale JSON
  files parse.
- `go test ./internal/data/... -run TestSalesForExport -v` — 4/4 pass.
- `go test ./internal/pages/... -run TestExportDispatch -v` — 18/18 pass.
- `go test ./...` — every package passes except the pre-existing,
  unrelated `TestSaveCleansUpDirectoryOnWriteFailure`
  (`internal/issuereport`), which fails under this sandbox's root-run
  environment — tracked separately as ut-docs#415, and the independent
  reviewer separately confirmed it's the only failure in that package
  (13 others pass) on a clean worktree too.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh` — all green.

## Scope

`internal/data/export_repo.go` (repository layer, batch queries),
`internal/pages/data_api.go` (range-cap validation on
`POST /api/data/export`), plus regression tests in both packages and a
locale-copy update in all 4 `web/locales/*.json` files. No migrations, no
new page routes, no plugin-loading/verification path touched.

## Outcome

Independent review found no blocking issues; the query count is
genuinely constant (proved via differential testing against the original
per-sale implementation, not just output-shape inspection) and the range
cap's boundary arithmetic is correct and errs generous. Two findings
(vacuous-test lower bound, a same-status-code false-positive risk in the
range-cap test) fixed in this round; the help-text gap fixed alongside
them; one out-of-scope follow-up (row/payload-size cap) filed separately.

Safe to merge.
