# ut-docs#988 — Turkey yüzde usulü pool distribution (ADR-0063 step 2/2)

Date: 2026-09-20
Branch: `feat/988-yuzde-usulu-pool-distribution`
Review: one independent subagent, fresh context, Sonnet (`complexity:hard`
mapped Dev to Fable per `MODEL-ROUTING.md`; Fable was unavailable this
cycle — out-of-usage-credits, HTTP 429 — so Dev fell back to Opus per that
doc's own documented fallback. To keep review genuinely independent of
whichever model actually wrote the code, this review ran on a
fresh-context **Sonnet** subagent instead of the table's nominal
"hard → Opus", since Opus reviewing Opus's own tier would have defeated
the independence the routing table exists to protect).

## What shipped

A new "Yüzde Usulü" reports tab recording how a Turkey İş Kanunu 4857
art. 51 "yüzde usulü" percentage collection (no bill line — #962 already
forbids one) is distributed to staff, mirroring the already-shipped UK
"Tips" tab's pattern (#964) and reusing the already-shipped, already-
tested shared ledger from #987 (`worker_allocations`,
`InsertWorkerAllocation`/`WorkerAllocationsSummary`/`ListWorkerAllocations`
in `internal/data/worker_allocation_repo.go` — **zero changes needed
there**, it already generically supported `source_type =
"yuzde_usulu_pool"`). The one genuinely new piece of scope versus the UK
flow: a Turkey distribution splits one pool across **several** workers in
**one** event (sharing a batch `source_id`), reconciled server-side
against a manager-declared pool total to enforce "distributed in full" at
entry time — the UK flow has no such reconciliation since it's one worker
per submission.

- `internal/pages/reports_page.go`: `renderYuzdeTab`, `registerYuzdePoolAPI`
  (`POST /api/reports/worker-allocations/pool`,
  `GET /api/reports/worker-allocations/pool/export`).
- `web/ui/partials/reports_tab_yuzde.html` (new): single "Distributed" KPI
  (not a misleading received/allocated pair — see below), 4-column detail
  table, multi-row record form (`<template>`-cloned worker rows, running
  total, add/remove), export link.
- `web/ui/pages/reports.html`: one new tab button.
- `web/help/{en,tr,de,ar,fa}/reports.md`: a new manual section in all 5
  shipped locales (translated, not English-only), plus a deliberate,
  explained update to `scripts/ci/i18n-baseline/help-drift-baseline.json`
  recording the "reports" topic's new (larger, but proportionally
  unchanged) structural drift against English.
- `web/locales/{en,tr,ar,fa}.json`: 28 new i18n keys (see Finding 2 —
  originally 29, one dead key removed).
- `internal/pages/reports_page_test.go`: 9 new test functions (16
  sub-tests incl. table cases), TDD-first.

## Independent review — findings

One Sonnet subagent reviewed the diff in an isolated worktree (branched
from a WIP commit on this feature branch), ran the full gate itself, and
independently re-verified two TDD claims by neutering the specific
validation logic each test claims to cover, confirming a real failure,
then restoring and confirming green again.

**Finding 1 (should-fix, money/compliance correctness — fixed).** The
"pool total must exactly equal the sum of worker amounts" check
(`registerYuzdePoolAPI`'s validation loop) computed `sum += amountMinor`
then checked `sum > poolTotal` — correct in real arithmetic, but int64
addition wraps on overflow, and a wrapped (negative) sum can never be
`>` a positive `poolTotal`, so the bail-out goes silently blind past an
overflow. The reviewer's own hand-derived 3-row PoC turned out not to
survive the *pre-existing* incremental check when re-simulated (row 1
alone already exceeded the small `poolTotal` used, so it was rejected for
an unrelated reason) — verified this by simulating both the OLD and NEW
code paths in Python before writing anything. Constructing the actual
minimal bypass required a **4-row** sequence: two rows that overflow
`int64` on their sum (wrapping it negative, invisible to `sum >
poolTotal`), then two more rows climbing the wrapped sum back up to land
exactly on the declared `poolTotal` — verified end-to-end in Python
against both the vulnerable and fixed logic before touching Go code.
**Fixed** with a pre-addition overflow check
(`amountMinor > math.MaxInt64-sum`, rejected before the addition happens
at all, using `reports.yuzde.error.mismatch` — the same user-facing
message an honest mismatch gets, since from the operator's perspective
both are "this doesn't add up"). Regression test:
`TestReportsPage_RecordYuzdePool_IntegerOverflowInSumCheckIsRejected`,
using the exact verified 4-row construction
(`5e18, 5e18, 8446744073709551616, 5e18` against `poolTotal = 5e18`) —
independently re-verified via the standard revert/restore TDD discipline:
neutered the new guard (`if false && amountMinor > math.MaxInt64-sum`,
keeping the `math` import referenced so the neutering itself compiles),
confirmed the test fails for real (200 + the full re-rendered tab HTML,
not a compile error), restored the guard, confirmed the test passes
again.

**Finding 2 (nitpick, fixed).** `reports.yuzde.col_batch` was added to
all 4 shipped locales but referenced by no template or Go string — the
on-screen detail table deliberately omits a batch column (kept simple by
design; the CSV export's own `batch` header is a hardcoded literal, not
this key). Removed from all 4 locale files; `guard-i18n.sh` re-run clean
afterward (it only checks cross-locale key-set parity, not usage, so this
was silent until a human/review pass looked for it).

**Everything else the review checked came back clean, independently
verified, not just re-derived from this card's own framing:**

- Batch `source_id` placement: one `uuid.NewString()` per distribution
  event, outside the per-row loop; a fresh per-row id only for each row's
  own primary key. Confirmed via the existing
  `TestReportsPage_RecordYuzdePool_MultiWorkerDistributionPersistsOneBatch`.
- Permission split: `worker_allocation` gates both new routes;
  `reports` gates the read-only summary separately; `?cashier=` is only
  honored for a `CanRecord` session (the same #964-review-fixed leak
  class deliberately not reintroduced here). Confirmed via
  `TestReportsPage_YuzdeTabCashierFilterIgnoredWithoutWorkerAllocationPermission`
  and `TestReportsPage_YuzdePool_ForbiddenWithoutPermission`.
- Transaction/rollback: all validation happens strictly before
  `BeginTx`; `defer tx.Rollback()` is set immediately after. No code path
  writes before validation completes.
- CSV export: `csvSafe` applied to worker name and note (formula-
  injection guard); UK "tip" rows correctly excluded from the pool
  export (single-`source_type` filter); correct filename/`Content-Type`.
- Client-side `<template>`-clone JS: no id collisions across cloned
  rows, listeners correctly attached per clone (not relying on
  delegation), last-row Remove button correctly hidden.
- Manual docs (5 locales) accurately describe the actual shipped
  behavior (single "Distributed" figure, sum-must-match-total,
  no-future-date, worker filter, export) — read against the real
  template/Go code, not just checked for guard-script greenness.
- No compliance-outcome claim ("legal", "compliant") anywhere in any new
  copy — only descriptive "records"/"reports on" language.
- Turkish translations (the actual target market) read as natural,
  correct Turkish, consistent register with the pre-existing Tips-tab
  strings.
- Negative amounts, zero amounts, and empty notes are all handled
  correctly (rejected / accepted-as-legitimate-blank respectively);
  duplicate-worker detection is case-sensitive but operates on opaque
  `<select>`-sourced user-table ids, never user-typed text, so this is a
  non-issue in practice.

## Verified beyond the independent review

- A live driven run (this session, before the independent review):
  built and ran the real till binary against a real seeded DB
  (`bash e2e/run-till.sh`), drove the actual new tab with Playwright, and
  looked at the screenshots — 1024×600 kiosk floor (1 row and 3 rows),
  360px phone width, and RTL (`ar`) plus `tr` locale renders. Caught and
  fixed one real bug this way: clicking "+ Add worker" left keyboard
  focus on the button itself, so a Tab press skipped the newly-added row
  entirely and landed on the submit button — fixed by moving focus into
  the new row's own worker `<select>` on add; re-verified live
  (confirmed via a scripted keyboard-driven Playwright check, not just
  visual inspection) that Tab now lands correctly inside the new row.
- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean, both before and
  after the review's two fixes.
- `go test ./internal/pages/... ./internal/data/...` — full, green
  (before and after fixes).
- `go test ./internal/pages/ -run Yuzde -v` — all 9 test functions /
  16 subtests pass (before and after fixes).
- `bash scripts/ci/guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` — all green (before and
  after fixes; `guard-help-drift.sh`'s many "known drift" lines for
  unrelated topics are pre-existing and untouched by this change — only
  the "reports" topic's own entries moved, and were updated deliberately
  to reflect the new section added in all 5 locales with matching
  structure).
- TDD verified independently, twice: once by the review subagent (two
  pre-existing tests, mutation-based revert/restore), and again by this
  session for the overflow-fix regression test specifically (see
  Finding 1) — both directions (fails without the fix, passes with it).
- No real client/shop name used anywhere; no secret-shaped literal
  introduced.

## Explicitly deferred / out of scope

- `ut-plugin-tax-tr` (doesn't exist; ADR-0063's own explicit non-goal —
  whatever decides *when* a pool exists and its percentage is
  plugin-shaped country policy, deferred to its own future card).
- Real till hardware verification of the touch/keyboard interaction —
  covered by a driven run against the real binary in a real (if
  emulated-viewport) browser, not physical touch hardware; noted
  explicitly rather than silently assumed equivalent.

## Verdict

Safe to merge. One review round found one should-fix issue (Finding 1, a
real — if only remotely reachable, requiring a hand-crafted request from
someone who already holds the `worker_allocation` permission —
correctness gap in the one invariant this feature exists to enforce) and
one trivial nitpick (Finding 2), both fixed and independently
re-verified in the same round. No second round needed per this
pipeline's standing "second round only for a blocker, scoped to the fix"
rule — Finding 1 is judged should-fix rather than a true blocker (it
needs a deliberately hand-crafted request no real UI could ever produce),
but was fixed anyway given the feature's explicit legal-compliance
purpose.
