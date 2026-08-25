# Code review: UK tip allocation report (ut-docs#964)

**Branch:** `964-uk-tip-allocation-report`
**Reviewer:** independent Opus subagent, worktree-isolated (complexity:medium
→ Opus review, per scrum-master's model routing) · **Author:** Sonnet (this
pipeline cycle, inline)

## What shipped

The UK-specific remaining surface for the Employment (Allocation of Tips)
Act 2023, on top of ADR-0063's already-merged shared ledger
(`worker_allocations`, `InsertWorkerAllocation`, `WorkerAllocationsSummary`
— ut-docs#987):

- `data.ListWorkerAllocations`: row-level detail behind the aggregate
  summary, for the new export/detail table.
- Migration `066_worker_allocation_permission.sql`: `worker_allocation`
  permission action (manager/admin/super_admin, cashier denied — mirrors
  042/057).
- A new "tips" tab on `/reports`: received-vs-allocated totals (tip +
  service charge), a worker filter doubling as the Act's worker-request
  path, a record-a-payout form, CSV export.
- `web/locales/{en,ar,fa,tr}.json`: `reports.tab.tips` + 26
  `reports.tips.*` keys, real translations.
- `web/help/{en,ar,fa,tr}/reports.md`: new "Tips tab" section — capability
  wording only, no compliance-outcome claim (ADR-0040).

Complexity downgraded `hard` → `medium` at pick time: ADR-0063 already
resolved the architecturally hard parts (shared data model, cross-country
design, the ADR itself) before this card started; what remained was one
repo, standard existing patterns (a new `/ui/reports/tab/{name}` tab
following `payments`/`eod`'s shape, a new `permission_actions` row
following 042/057's shape, CSV export following `eod_api.go`'s shape).

## Independent review — first round: two blockers, fixed in this same round

Full independent pass (different model, isolated worktree so its
TDD-verification mutations never touched the shared checkout — ut-docs#386):
`gofmt -l`/`go build`/`go vet` clean, full `internal/pages`/`internal/data`/
`internal/db` suites green, all four relevant guards
(`guard-data-access`/`guard-i18n`/`guard-compliance-claims`/
`guard-help-topics`) green. Confirmed neither of this pipeline's two
recurring bug classes applies (no file-write path exists in this diff at
all — the CSV export streams straight to the response, never touches
disk). Money confirmed integer-minor-units end to end, SQL confirmed
parameterized/allow-listed, `cashier_id` confirmed validated against a real
user row, the route confirmed unreachable from the auth-exempt self-order
surface, RTL/design-token conventions confirmed followed, i18n translations
spot-checked genuine (not placeholders), compliance wording confirmed
capability-only.

**TDD re-verified independently**: broke (and confirmed a real assertion
failure, then restored) three things — the `worker_allocation` permission
gate on the POST, the future-date rejection, and the amount `<= 0` check.
All three regression tests discriminated correctly.

## Findings — both blockers fixed in this same round

- **Blocker 1 (real bug, data integrity)**: the record-payout POST validated
  and stamped the payout date in **UTC**, while every other clock this
  feature touches — `parseReportWindow`'s window, `reportNow()`,
  `generateEOD`'s own day boundary, and the read side's own
  `date(allocated_at, 'localtime')` — is **local**. Reproduced three
  distinct symptoms by running the branch's own tests under a non-UTC
  `TZ`: (a) a shop in Turkey (UTC+3) or UK BST (UTC+1) has its own real
  "today" rejected as "in the future" for the last 1-3 hours of every
  trading day — hits both markets this ledger exists for; (b) the same UTC
  mismatch the other way silently **accepts** a real tomorrow as valid for
  most of the day in any Americas shop, with no crafting needed — the
  form's own `max=` attribute actively offers it; (c) a payout recorded for
  "today" could be stored at an instant that reads back, through
  `date(...,'localtime')`, as **yesterday** — the detail table and the
  summary total would then disagree about which day the same payout fell
  on, on a record a worker can legally demand under the Act.
  **Fixed**: extracted the date-validation + instant-construction logic
  into a pure `workerAllocationRequestedAt(date, nowLocal)` function so the
  future-check and the stored instant are computed from the exact same
  local clock and can never disagree; both call sites (`"Today"`'s
  default/max and the POST handler) now use `time.Now()` (local), not
  `.UTC()`. Regression tests pin this against fixed `Europe/Istanbul`
  (UTC+3) and `America/Los_Angeles` (UTC-7) locations — deterministic
  regardless of host TZ or wall-clock time at test run, unlike the
  pre-existing `WorkerAllocationsSummary`/`ListWorkerAllocations` tests
  elsewhere in this area (those hardcode UTC timestamps against a
  local-day filter and are themselves TZ-fragile — inherited from `main`,
  not introduced here, tracked separately in ut-docs#1020 item 7).
- **Blocker 2 (real bug, authorization)**: `renderTipsTab`'s own doc
  comment claimed `CanRecord` (`worker_allocation`) gates "individual
  workers' payout records, not just an aggregate total" — but the
  `?cashier=` filter was applied to `WorkerAllocationsSummary` under
  `CanView` (`reports`) alone. A session holding only `reports` could read
  any named worker's received/allocated totals by picking them from the
  worker-picker dropdown the tab itself renders to that same session —
  two clicks in the normal UI, not a crafted request. Confirmed by the
  reviewer with a scratch reproduction and independently re-confirmed with
  a permission-matrix test (`authRepo.SetRolePermission(..., "cashier",
  "reports", true)`, mirroring the existing EOD gating test's own
  technique) before the fix and after.
  **Fixed**: the `?cashier=` filter is now only honored when `CanRecord` is
  true; a `CanView`-only session always gets the shop-wide total regardless
  of the query string, and the worker-picker dropdown itself moved behind
  `{{ if .CanRecord }}` in the template so it's not offered to a session
  it wouldn't do anything for. New regression test
  (`TestReportsPage_TipsTabCashierFilterIgnoredWithoutWorkerAllocationPermission`)
  pins the exact leak scenario the review reproduced.

## Non-blockers — deferred, tracked in ut-docs#1020

Export has no date-range cap (unlike its own `eod_api.go` precedent); CSV
formula injection via a manager-typed `note`/worker name opened in
Excel/Sheets; `renderTipsTab` swallows every repo-call error as "no data"
(matches the other tabs' existing style, but on a statutory record); the
new endpoints are a hard 403 rather than `eod_api.go`'s manager-PIN
elevation (may be intentional, undocumented); a `GetUser` DB error reports
as "bad input"; `workerAllocationDateRange` over-counts by a day under a
non-midnight business-day start. None reachable without `worker_allocation`
already granted, and none block real use of the feature today — filed as
ut-docs#1020 rather than expanding this round, per this pipeline's "a
second round is scoped to the fix, not a re-review of the whole diff" rule.

Also found, unrelated to this diff and filed separately (ut-docs#1018):
`TestAsyncPrintFailureIsRecordedWhenPrintCtxExpired`
(`internal/pages/print_api_test.go`) fails deterministically under
`go test -race` and passes reliably unraced — pre-existing, untouched by
this branch, confirmed via 3x repro on both.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`: clean.
- Full `go test ./...`: green (one pre-existing, unrelated `-race`-only
  flake noted above and filed separately, not present without `-race`).
- `go test ./internal/pages/... -run 'WorkerAllocation|ReportsPage|ReportsTab|TipsTab' -race -v`:
  green, including both new blocker-regression tests and the three
  `workerAllocationRequestedAt` unit tests.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`: all green.
- No real client/shop name used as demo/seed/test data; no secret-shaped
  literal anywhere in the diff.
- `permission_settings_page_test.go` confirmed NOT to need updating
  (no exhaustive action-catalog assertion) and confirmed
  `permissions.action.worker_allocation` exists in all four locales, so
  migration 066 doesn't reintroduce the missing-locale-key trap 057 once
  did.

## Safe-to-merge verdict

**Yes**, after the two blocker fixes above. Second review round not
warranted: both findings were data-integrity/authorization bugs in the
statutory-record path this card exists to build (exactly the "blocker-class
issue" bar this pipeline's process-depth rule sets for earning a second
round), fixed directly and scoped to what was flagged, each pinned by a
new regression test that fails on the pre-fix code and passes on the
post-fix code — not a re-review of the whole diff. Non-blockers deferred to
ut-docs#1020 rather than expanding this round further.
