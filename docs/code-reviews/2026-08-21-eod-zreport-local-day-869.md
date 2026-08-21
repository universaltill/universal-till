# Code review: EOD Z-report day boundary unified to local calendar day

**Issue:** ut-docs#869 · **Repo:** universal-till · **Complexity:** medium ·
**Built by:** Sonnet (inline) · **Reviewed by:** Opus (fresh-context subagent,
isolated worktree — per this pipeline's medium-tier routing, an independent instance
that never saw the dev reasoning). ADR: `ut-docs/adr/0057-eod-zreport-local-day-boundary.md`.

## What shipped

`internal/data/pos_repo.go`'s `DepartmentsForDay` and `dateRangeSummary` (backing
`EndOfDay`/`EndOfDayRange`, the archived/printed EOD Z-report used for German TSE/§146a
compliance reporting) matched bare UTC `date(created_at)`. But `eodSchedulerTick`
(`internal/pages/eod_api.go`) already computes the day it archives from Go's **local**
wall-clock `time.Now()` — so on any non-UTC host, the scheduled/archived Z-report silently
aggregated the wrong calendar day's transactions. Fixed by changing all four `date(...)`
fragments (one in `DepartmentsForDay`, three in `dateRangeSummary`: the totals `BETWEEN`,
the methods `BETWEEN`, and the per-till breakdown's `=`) to
`date(created_at, 'localtime')` — matching `DayTotal` and the already-shipped
`ListSalesJournal` fix (ut-docs#774/PR#417), deliberately **not** `SalesByDay`'s
business-day-start shift (a different semantic, out of scope — see ADR-0057).

## Independent review findings

Opus, fresh context, isolated worktree (revert/restore mutation testing is unsafe on a
shared checkout per ut-docs#386). First-round verdict: **not safe to merge as drafted** —
one blocker, four non-blocking findings.

1. **Blocker — the new regression tests encoded the OLD (buggy) semantics.** The first
   draft's `internal/data/eod_zreport_local_day_869_test.go` hardcoded both the seeded
   timestamps and the expected day boundary as fixed UTC literals (e.g.
   `"2026-08-15"`). Reviewer's revert/restore verification, extended across real
   timezones (not just `TZ=UTC`), found this ran **backwards**: both new tests passed
   against the pre-fix (buggy) code and *failed* against the fix itself under
   `TZ=Asia/Tokyo`. The reviewer also found the diff newly broke two pre-existing,
   previously-green tests off-UTC (`TestEndOfDay_AggregatesSalesReturnsAndMethods`,
   `TestEndOfDayRange_AggregatesAcrossMultipleDaysInclusive` in
   `pos_repo_lifecycle_test.go`), which also hardcoded UTC-literal seed data/day
   arguments. Root cause: this package already solved exactly this problem
   (`b8ExpectedDay`, added by ut-docs#559's own review finding) and the first draft
   didn't use it. **Fixed**: rewrote all four affected tests to anchor every seeded
   instant on the *host's own* local noon (`time.Now()`-derived, not a hardcoded
   literal — noon keeps a same-day instant inside its calendar day for any real IANA
   offset, -12..+14) and derive the "day" argument passed to the repo methods via
   SQLite's own `date(?, 'localtime')` control query (`b8ExpectedDay`), never a Go-side
   string. Re-verified manually passing under `TZ=UTC`, `TZ=Asia/Tokyo`,
   `TZ=America/New_York`, `TZ=Pacific/Kiritimati`, `TZ=Etc/GMT+12` (the IANA offset
   extremes) after the fix.
2. **N1 (fixed) — the payments/methods fragment had zero boundary coverage.** Neither
   original test seeded a `payments` row, so `dateRangeSummary`'s methods query (one of
   the four changed fragments) was never actually exercised by the boundary test. Fixed:
   `TestEndOfDay_LocalDayBoundary` now seeds payments on both the excluded (prior-day)
   and included sales and asserts `rep.Methods` excludes the prior day's cash payment.
3. **N2 (fixed) — mis-citation.** The code comment (and ADR-0057) cited "ADR-0040 §2" for
   `report_archive` row immutability; that section is the till-side *retention/pruning*
   policy, not an immutability guarantee. Fixed to cite the actual mechanism —
   `ArchiveReport`'s `INSERT ... ON CONFLICT (kind, period) DO NOTHING` (write-once row)
   — in both the code comment and a follow-up ADR correction
   (`ut-docs` PR#874).
4. **N3 (fixed) — RHS convention asymmetry.** `DepartmentsForDay`'s day-match already
   wrapped its RHS in `date(?)`; `dateRangeSummary`'s three fragments used a bare `?`/
   `? AND ?`. Harmless for canonical `YYYY-MM-DD` input (all production callers;
   `eodDateRe` enforces the shape on the range endpoint) but inconsistent, and the
   reviewer flagged one latent case: `department_report_test.go`'s pre-existing smoke
   call `DepartmentsForDay(ctx, "now")` now has a LHS-local/RHS-UTC mismatch since `date(?)`
   `= date('now')` resolves `'now'` as UTC. That test already tolerates zero rows by its
   own design (explicit comment), so it doesn't newly fail — not itself a regression, but
   worth closing the inconsistency anyway. Fixed: all four fragments now wrap their day
   argument(s) in `date(...)` uniformly.
5. **N5 (accepted, no action) — pre-existing perf note.** `idx_sales_created` is unusable
   by all four `date(...)`-wrapped predicates (same as before this diff, `'localtime'`
   makes it non-deterministic too) — flagged as a pre-existing characteristic, not a
   regression. No change made; sibling queries elsewhere in the file already use an
   index-friendly half-open range shape where it matters more (higher-cardinality report
   windows), and these four queries are bounded to a single archived-report generation,
   not a hot path.

## Findings deferred, not missed

- **N4 — product-facing transitional gap.** A pilot shop live across the deploy boundary
  gets a silent, undocumented one-time discontinuity in its Z-report series (old reports
  computed under bare-UTC, new ones under local time), with no in-product signal.
  Documented as a "Known gap" in ADR-0057 (release-note candidate) rather than built as an
  in-product notice — no real pilot shop is live across this specific deploy yet.
- **Pre-existing, out-of-scope test fragility found during verification, not this diff's
  bug**: `TestPOSRepo_ListSalesJournal_DayFilter` (ut-docs#774/PR#417) has the identical
  hardcoded-UTC-literal flaw and fails under `TZ=Asia/Tokyo` on *unmodified* `main` —
  confirmed via `git stash` before touching anything. Filed as ut-docs#875 rather than
  bundled into this card (out of #869's scope: that test belongs to `ListSalesJournal`,
  not `DepartmentsForDay`/`dateRangeSummary`).

## Verified

- TDD re-verified independently by the review subagent in an isolated worktree: reverted
  just the SQL changes, confirmed the (fixed) tests still pass under `TZ=UTC` (documented,
  accepted limitation — local IS UTC there) but genuinely distinguish pre-fix vs. post-fix
  behavior under real non-UTC offsets, then restored.
- `go build ./...`, `go vet ./...` clean; `gofmt -l` clean on every changed file.
- Full `go test ./...` — all 38 packages pass under `TZ=UTC` (CI's zone).
- Full `go test ./internal/data/...` re-run under `TZ=Asia/Tokyo`, `TZ=America/New_York`,
  `TZ=Pacific/Kiritimati`, `TZ=Etc/GMT+12` — every test this diff touches or added passes
  in all four; the one pre-existing failure across those zones
  (`TestPOSRepo_ListSalesJournal_DayFilter`) is unrelated to this diff (filed as
  ut-docs#875).
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh` — all
  pass.
- Blast radius: `DepartmentsForDay`/`dateRangeSummary` have no callers beyond
  `EndOfDay`/`EndOfDayRange`, which have no callers beyond `eod_api.go`'s two
  `generateEOD` call sites (the scheduler tick and the manual run-now handler) — both feed
  operator-local day strings, so the fix makes both strictly more correct, no surprised
  caller.
- Archived-report safety: the only touch to `ArchiveReport`/`report_archive` in the diff
  is a comment; the write path itself is untouched, confirmed by reading it directly —
  already-archived rows cannot be altered by this change.
- No file-write / `paths.Data(...)` concerns — the diff writes no files and does no path
  handling.
- No real client/shop name, no secret-shaped literal.
- No `web/help/` topic references this behavior (backend-only, no UI change) — confirmed
  by `guard-help-topics.sh` passing unchanged.
- No i18n keys involved (no user-facing strings).

## Known, accepted gap

CI runs `TZ=UTC`, where `date(x)` and `date(x, 'localtime')` are numerically identical —
this specific SQL change isn't observable in CI's own test runs, the same pre-existing,
already-documented limitation `DayTotal`/`SalesByDay`/`SalesByWeekday`/`SalesByHour`
already live with. Unlike the first draft, though, the regression tests here don't
*assume* UTC to pass — they're genuinely host-timezone-independent (verified manually
across the IANA offset extremes above), so a future change that regresses the `'localtime'`
modifier back to bare `date()` would be caught by anyone running the suite off-UTC, even
though CI itself can't catch it.

## Verdict

Safe to merge.
