# Code review: EOD close-to-close instant-window query family (ADR-0066 card 1/2)

- **Card**: ut-docs#1140
- **Branch**: `pipeline/1140-eod-instant-window-query-family`
- **Design**: `adr/0066-eod-zreport-close-to-close-period.md` (accepted, revised 2026-08-26)
- **Dev**: Fable-model subagent (card labelled `complexity:hard`)
- **Review**: Opus, independent subagent in an isolated git worktree (`/home/user/review-worktree-1140`, detached at the pre-review commit)

## What shipped

The first of two implementation slices for ADR-0066 — the "eod" Z-report
kind moving from calendar-day to close-to-close (`[previous close, this
close)`) trading periods. This card adds **only the data-layer query
family**; nothing is wired into report generation yet (that's ut-docs#1141,
which depends on this card).

New, in `internal/data`:

- `instantWindow(col, from, to)` — shared half-open `datetime(col) >=
  datetime(?) AND datetime(col) < datetime(?)` WHERE-fragment helper,
  omitting the lower bound entirely when `from` is the zero `time.Time`
  (till's first-ever close, ADR-0066 Decision 3).
- `dateRangeSummaryInstant`, `SalesForTaxBandsInstant`,
  `DepartmentsForInstantWindow`, `CashReconciliationForInstantWindow`,
  `VouchersIssuedRedeemedForInstantWindow` — instant-windowed siblings of
  the existing calendar-day functions. Genuinely parallel query bodies,
  not a refactor: `dateRangeSummary`, `SalesForTaxBands`,
  `DepartmentsForDay`, `CashReconciliationForLocalDay`,
  `VouchersIssuedRedeemedForRange`, and `EndOfDay`/`EndOfDayRange` are
  byte-for-byte untouched (verified — see below).
- `LatestArchivedAt(ctx, kind)` — `MAX(created_at)` per kind, `nil, nil`
  on an empty archive, UTC-parsed with a hard error (not a silent
  fallback) on a corrupt value, since this feeds a fiscal window
  boundary.
- `ArchiveReport` gains a trailing `closedAt time.Time` parameter.
  `closedAt.IsZero()` is fully backward compatible (byte-identical SQL
  and args to the pre-card statement). Non-zero: `created_at` is written
  explicitly from `closedAt` (the clock-skew fix so the next close's
  `from` is byte-identical to this close's `to`), and for `kind='eod'`
  specifically an atomic same-local-calendar-day double-close guard is
  folded into the existing `INSERT ... SELECT` via a `HAVING` predicate
  — still inside the single write-locked autocommit statement, no
  separate pre-check, no TOCTOU.

Mechanical: all 6 existing `ArchiveReport(...)` call sites (5 test files
+ the one real caller, `internal/pages/eod_api.go:418` inside
`generateEOD`) updated to pass a trailing `time.Time{}`.

New tests: `internal/data/eod_instant_window_test.go`.

## Independent review findings

All findings were **test-coverage gaps, not production-code defects** —
the Opus reviewer confirmed the shipped SQL/Go logic correct by running
it, not just reading it (gate + a throwaway HAVING-vs-WHERE probe +
bind-parameter readback + byte-identical diff of the untouched
functions + two atomic revert→run→restore TDD re-verifications). All
five were fixed before this review record was written:

1. **`instantWindow`'s two-bound fragment left unparenthesized**
   (`pos_repo.go`) — no bug today (every call site splices it into an
   all-`AND` WHERE), but a future caller splicing it after an `OR` would
   silently get wrong precedence. Fixed: wrapped in parens.
2. **`archiveTimestampFmt` and `reportWindowFmt` were two independent
   constants that happened to hold the identical literal** — the
   gaplessness guarantee (`from(n+1) == to(n)`) depends on them staying
   equal forever. Fixed: `archiveTimestampFmt` is now defined as `=
   reportWindowFmt`, making the dependency the compiler enforces instead
   of a comment asking a future editor to remember it.
3. **`TestArchiveReport_ClosedAtGuardIsEODOnlyAndOptIn` passed even with
   the guard deleted entirely** — it only proved "must not over-apply",
   never that the guard is actually armed. Fixed: added a positive
   control (two same-local-day `eod` closes; the second must be
   `created=false`).
4. **The first-close unbounded-lower-bound test never exercised its own
   upper bound** (no sale seeded at/after `to`) and **the cancellations
   window's boundary test never exercised its lower bound** (both voided
   seeds were at/after `from`). Fixed: added a sale at `to` (must be
   excluded) to the first-close test, and a voided sale before `from`
   (must be excluded) to the boundary test.
5. **The guard's `date(..., 'localtime')` had no regression test that
   could actually fail on a wrong implementation** — every existing
   same-day test kept all instants within minutes of local noon, so a
   bare `date(created_at)` (no `'localtime'`) would have passed every
   existing test too, on any TZ. This is exactly the class of mistake
   ADR-0066 Decision 5 calls out by name ("CI's TZ=UTC cannot catch a
   mistake here"). Fixed: added
   `TestArchiveReport_GuardComparesLocalDayNotUTCDay`, seeding two
   instants that land on the SAME local calendar day at UTC+14
   (`Pacific/Kiritimati`) but DIFFERENT UTC calendar days, and asserting
   the second close is blocked.

   **Sub-finding surfaced while building that fix**: `t.Setenv("TZ",
   ...)` mid-process does *not* reliably change what
   `modernc.org/sqlite`'s `'localtime'` modifier resolves to — it's
   cached the first time any query in the process evaluates it, so a
   test asserting on this via `t.Setenv` alone passes or fails based on
   **test execution order**, not on the guard's real correctness (it
   happened to fail once test suite ordering put it after other
   `'localtime'`-using tests, and passed in isolation). Fixed the same
   way this codebase's own `internal/logging.TestFatalfExitsProcess` and
   `internal/selfupdate`'s subprocess tests already handle
   process-environment-dependent behavior: the test re-execs itself as a
   genuine subprocess with `TZ` set in its environment from the start,
   guarded by `UT_TEST_GUARD_TZ=1`.

## Verified beyond the automated tests

- Bind-parameter order in `ArchiveReport`'s guarded `INSERT ... SELECT`
  (11 placeholders / 11 args) traced against a real column readback —
  no swap, no off-by-one.
- `HAVING` (not `WHERE`) genuinely produces zero rows, not an error and
  not a wrong-value row, when the guard condition is false — confirmed
  by direct query execution, not just SQLite documentation.
- `dateRangeSummary`, `SalesForTaxBands`, `DepartmentsForDay`,
  `CashReconciliationForLocalDay`, `VouchersIssuedRedeemedForRange`,
  `EndOfDay`, `EndOfDayRange`, `ArchivedReportsInRange`,
  `PruneReportArchiveOlderThan`, `ListArchivedReports`,
  `HasArchivedReport` byte-compared against `origin/main` — identical.
  `internal/pages/eod_api.go` has exactly the one intended changed line.
- `ArchiveReport`'s zero-`closedAt` SQL text and bind args byte-compared
  against the pre-card version — identical.
- Two atomic revert→run→restore TDD checks (in the isolated review
  worktree): deleting the `HAVING` guard makes
  `TestArchiveReport_ConcurrentSameLocalDayDoubleClose` fail with a real
  assertion (`want exactly ONE … got 10`); reverting `LatestArchivedAt`'s
  UTC parse to `ParseInLocation(..., time.Local)` makes
  `TestLatestArchivedAt` fail by exactly 5 hours.
- Boundary mutation testing (both directions on `>=`/`<`) confirmed the
  half-open `[from, to)` window is genuinely pinned down by the new
  tests, not just asserted.
- Full gate: `gofmt -l .` clean, `go build ./...` clean, `go vet ./...`
  clean, `bash scripts/ci/guard-data-access.sh` passing (all new SQL
  stays in `internal/data`), plus every other CI-blocking guard in
  `ci.yml`'s `build` job run and passing.
- `go test ./internal/data/... ./internal/pages/... -race -count=1`: all
  packages `ok`, run three times across this review (once by Dev, once
  by the independent Opus review, once after folding in the review
  fixes).

## One pre-existing, unrelated flake observed — not a regression

`TestAsyncPrintFailureIsRecordedWhenPrintCtxExpired`
(`internal/pages/print_api_test.go`) failed twice in three isolated
`-race` runs during this review. `git diff` against `origin/main`
confirms zero changes to that file or anything it depends on — already
tracked as **ut-docs#1018** ("flakes deterministically under `go test
-race`, passes unraced"). Not touched by, and not caused by, this card.

## Deferred to card 2 (ut-docs#1141), not this card's scope

- Wiring `generateEOD`/`eodSchedulerTick`/the manual-run handler to
  actually call these new functions.
- The CSV/retention export's period-filter fix (`ArchivedReportsInRange`
  currently filters on `period` text, which needs to move to
  `created_at` once `period` mixes calendar-date and RFC3339 forms).
- Print/UI period display.
- Two things the independent review flagged as worth card 2's attention
  (not defects here, since nothing routes through them yet):
  1. **Cutover-day behavior**: a new-style close is blocked if a legacy
     zero-`closedAt` `eod` row already exists on the same local day —
     correct per ADR-0066 Decision 4's wording, but worth the release
     note the ADR's own Consequences section already anticipates.
  2. **`rep.Day == ""` fallback trap, sharper than documented**: if card
     2 naively routes `dateRangeSummaryInstant`'s report through the
     existing `attachEODTaxBands`/`rep.Day == ""` branch instead of
     calling `SalesForTaxBandsInstant` directly as ADR-0066 Decision 6
     requires, the failure mode is worse than "silently degrades to
     calendar-day banding" — with `rep.Day` empty, `rep.From`/`rep.To`
     also empty (`dateRangeSummaryInstant` doesn't populate them,
     display is card 2's job), so `date('')` is SQL `NULL`, the query
     returns zero rows, and `computeEODTaxBandsFromSales` returns `nil`
     with **no error at all** — a printed Z-report with no VAT table.

## Verdict

**Safe to merge.** No blocking findings from independent review; all
five test-coverage gaps it found were closed before this commit. Full
gate green, including a repeat run after the fixes.
