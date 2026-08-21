# Code review: journal truncation notice + local-day filter for ListSalesJournal

**Issue:** ut-docs#774 · **Repo:** universal-till · **Complexity:** easy ·
**Built by:** Sonnet (inline) · **Reviewed by:** Sonnet (fresh-context
subagent, isolated worktree — per this pipeline's easy-tier routing, a
clean-context instance that never saw the dev reasoning).

## What shipped

Two findings deferred from #550's independent review
(`docs/code-reviews/2026-08-16-cross-till-eod-order-list-550.md`), scoped
down at BA time from the card's literal "repo-wide day-boundary
consistency" framing to what's safely easy-tier — see Non-goals below.

1. **Truncation notice.** `/ui/journal?limit=full` caps at 100 rows with
   no indication when more exist for the active filter. `ListSalesJournal`
   (`internal/data/pos_repo.go`) now queries `limit+1` rows; if it gets
   back more than `limit`, it trims to `limit` and returns a new
   `truncated bool` (signature: `([]SaleJournalEntry, bool, error)`) —
   avoids a separate `COUNT(*)`. Threaded through
   `ui.JournalViewData.Truncated` → a new conditional notice in
   `web/ui/partials/journal.html`, gated inside the same
   `{{ if .ShowFilters }}` block as the existing replica notice, so the
   sale-screen OOB mini-widget never shows it. New i18n key
   `journal.truncated`, real translations in en/fa/tr/ar.
2. **Local-day filter.** `ListSalesJournal`'s `Day` filter matched bare
   UTC `date(s.created_at) = date(?)`, but `Day` comes from a browser
   `<input type=date>` in the operator's own local time. Changed to
   `date(s.created_at, 'localtime') = date(?)` — same convention
   `DayTotal` already uses (not `SalesByDay`'s business-day-start shift,
   which is a different semantic for trading-night merging).

## Non-goals (deliberately deferred, not missed)

`DepartmentsForDay`, `dateRangeSummary` (backs `EndOfDay`/`EndOfDayRange`,
the archived/printed EOD Z-report used for German TSE/§146a compliance
reporting), and the inline per-till breakdown inside `dateRangeSummary`
all still use bare UTC `date()`. BA/Architect decision: unifying date
semantics there risks altering historically-archived fiscal report
content and needs its own careful pass (possibly compliance/ADR-level),
not bundled into an easy-tier card. Filing a follow-up Backlog card.

## Independent review findings

Fresh-context Sonnet, isolated worktree (revert/restore mutation testing
is unsafe on a shared checkout per ut-docs#386). Verdict: **safe to
merge**, two non-blocking gaps, both fixed before commit:

1. **Missing CSS.** The new `journal-truncated-notice` class had no rule
   in `web/public/app.css` — its siblings (`journal-till-status`,
   `journal-replica-notice`) both do, so it would've rendered flush
   against neighboring elements. Fixed: added
   `margin-block-end: .5rem; font-size: .8rem;` matching
   `journal-till-status`'s pattern (plain informational text, not a
   warning tint — `journal-replica-notice`'s amber background is for an
   actual "can't do that" case, this isn't).
2. **Untested fencepost.** The truncation logic (`len(out) > limit`) was
   correct but the exact-boundary case (rows == limit) wasn't covered by
   a real test. Reviewer hand-verified it was correct, but the shipped
   suite only covered over/under, not exactly-at. Fixed: added
   `TestPOSRepo_ListSalesJournal_Truncated`'s boundary case (12 rows,
   limit 12 → `truncated=false`) and
   `TestJournalUIFilters_NoTruncatedNoticeAtExactCap` (100 seeded rows,
   the cap itself → no notice).

## Verified

- TDD re-verified independently, twice: (a) this session mutation-tested
  both new assertions (forced `truncated := false`, forced the template
  conditional to `false`) and confirmed each test fails with the expected
  error, then restored and confirmed green; (b) the independent review
  subagent repeated this itself in its own isolated worktree rather than
  trusting the claim, with the same result.
- `go build ./...`, `go vet ./...` clean.
- Full `go test ./...` — all 45+ packages pass, both before and after the
  two post-review fixes.
- `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-kiosk-engine.sh`,
  `bash scripts/ci/guard-plugin-menu-read.sh`,
  `bash scripts/ci/guard-compliance-claims.sh` — all pass.
- `gofmt -l` clean on every changed `.go` file.
- Real rendered HTML inspected directly (not just `strings.Contains`
  assertions): notice text/class/position correct, exactly 100 rows
  render when 101 exist in the DB.
- Signature-change blast radius: only 2 production callers of
  `ListSalesJournal` (`ListRecentSales`, the `/ui/journal` handler) —
  both confirmed correct; `ListRecentSales`'s only caller
  (`internal/pages/pos_api.go`'s sale-screen OOB mini-widget) still uses
  the unchanged 2-value wrapper and never leaks the extra row (already
  trimmed inside `ListSalesJournal` before either wrapper returns).
- Scope check: `DepartmentsForDay`/`dateRangeSummary` confirmed
  byte-identical to the pre-change commit — the deferred EOD Z-report
  date semantics were not touched.
- fa.json translation uses Persian numerals (۱۰۰), consistent with
  existing precedent elsewhere in that file.
- No real client/shop name used as test data; no secret-shaped literals.
- No `web/help/` topic exists for the Journal page, so nothing there went
  stale (confirmed by directory listing, not assumed).
- No ADR needed — straightforward bug fix + additive read-only UI notice,
  not a new architectural mechanism.

## Known, accepted gap

The local-time day-filter change is only observably different from the
old UTC behavior on a non-UTC host — CI runs `TZ=UTC`, where `'localtime'`
and bare UTC are behaviorally identical. This is a pre-existing,
documented limitation this codebase already lives with for `DayTotal`
(see `pos_repo_batch8_reports_test.go` ~line 590); not something this
card introduces or is expected to close.

## Verdict

Safe to merge.
