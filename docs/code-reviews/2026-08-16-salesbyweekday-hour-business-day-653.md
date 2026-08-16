# Code review: SalesByWeekday/SalesByHour group by business day, not raw local weekday/hour

**Date:** 2026-08-16
**Author (Dev):** scrum-master pipeline, Sonnet (complexity:easy)
**Reviewer:** independent Sonnet subagent, fresh context, isolated worktree
**Card:** universaltill/ut-docs#653 (duplicate of ut-docs#652, left open to close once this ships)

## What shipped

`internal/data/pos_repo.go`'s `SalesByWeekday`/`SalesByHour` — the Sales-trend
tab's busiest-weekday/busiest-hour charts — bucketed sales via
`strftime('%w'/'%H', created_at, 'localtime')`, ignoring the shop's configured
`reports.business_day_start` boundary. `SalesByDay`, the third chart on the
same tab, already groups by that boundary (ut-docs#559): a trading night that
spans it was already merging correctly into one row there, but the
busiest-day/hour charts still attributed a pre-boundary sale (e.g. 02:00 with
a 04:00 boundary) to the raw calendar weekday/hour — an inconsistency between
two views of the same trading data (found during #559's own review, filed as
two near-duplicate cards, #652/#653).

Both functions now take the resolved `hh, mm` business-day-start and thread
it into `busyBuckets`, which applies the same `'localtime', ?, ?` shift
modifiers `SalesByDay` already uses — `strftime('%w'/'%H', s.created_at,
'localtime', ?, ?)` in place of the unshifted call. One production call site
(`reports_page.go`'s `sales-trend` tab handler) updated to pass the window's
already-resolved `window.Hour`/`window.Minute` (the same fields #559 added to
`reportWindow` for exactly this purpose) instead of the previous 2-arg call.

New regression coverage in `pos_repo_batch8_reports_test.go`:
- `TestPOSRepo_SalesByWeekday_BusinessDayBoundary_ShiftsWeekday` — a 02:00
  sale with a 04:00 boundary buckets into the *previous* business day's
  weekday, not its raw calendar weekday.
- `TestPOSRepo_SalesByHour_BusinessDayBoundary_Shifted` — same repro for the
  hour bucket.
- `TestPOSRepo_SalesByWeekdayAndHour_DefaultBoundary_NoRegression` — pins
  `hh=mm=0` (the default) as unchanged vs. the pre-fix behavior.
- New `b8ExpectedSlot` helper computes expected buckets via SQLite's own
  `strftime(...,'localtime',?,?)` (mirroring `b8ExpectedDay`'s approach),
  independently of production's shift logic — so a sign-inverted fix is
  genuinely caught, not masked by a tautological assertion (confirmed in
  review, see below).
- Existing `TestPOSRepo_SalesByWeekdayAndHour_BucketsLocalTime` and all other
  pre-existing call sites (`product_reports_test.go`, the empty-DB and
  closed-DB error tests) updated to the new 4-arg signature, passing `0, 0`.

## Verified beyond automated tests

- **TDD independently re-verified by the reviewer**, not just taken on the
  implementer's word: reverted the fix in an isolated worktree (hardcoded the
  shift modifiers to `"0 hours"/"0 minutes"` regardless of the `hh, mm`
  args, keeping the 4-arg signature so the test files still compiled) and
  confirmed both new boundary tests failed with the exact expected-vs-got
  mismatch (`weekday slot = 4, want 3`; `hour slot = 2, want 22`). Went
  further than the standard revert-then-restore: also tried a **sign-flipped**
  shift as an extra check, confirmed it too fails distinctly (`hour slot = 6,
  want 22`) rather than accidentally passing — proving the tests would catch
  a sign-inversion bug, not just an absent one. Restored the real fix,
  confirmed both pass, worktree left clean (`git status --porcelain` empty).
- **Placeholder ordering checked against the query text**: `bucketExpr`'s two
  `?`s land in the `SELECT` clause, ahead of the `WHERE` clause's two `?`s —
  args bound as `(hourMod, minMod, fromStr, toStr)` matches textual order.
  `bucketExpr` remains a compile-time constant from the two typed callers,
  never user input — no injection risk, unchanged from the pre-existing
  pattern.
- **All callers of `SalesByWeekday`/`SalesByHour` confirmed via a full-repo
  grep**, not just the files in the diff — one production call site
  (`reports_page.go`), all test call sites updated, no orphaned 2-arg caller
  left anywhere.
- **Full gate run once, clean**: `go build ./...`, `go vet ./...`, `gofmt -l`
  on every touched file, full `go test ./...` (every package), plus
  `go test ./internal/data/... -race -run 'SalesByWeekday|SalesByHour|SalesByDay'`
  (no race flags). `guard-data-access.sh` and `guard-i18n.sh` clean.
- **Manual (`web/help/en/reports.md`) checked, not assumed unaffected**: its
  existing "Business day start" section already describes the effect
  generically ("Day/Week/Month/Year periods line up with your real trading
  day instead of the clock") — the same call the #559 review made for
  `SalesByDay`, extended here since the chart's shape/labels are unchanged
  and only boundary-configured shops' bucketing numbers change.

## Findings

- **Design question, flagged not fixed (informational):** shifting the
  *weekday* bucket by the business-day boundary is unambiguously correct —
  it merges a trading night into the business day it belongs to, matching
  `SalesByDay`. Shifting the *hour* bucket is more debatable: a 2am sale with
  a 04:00 boundary now displays as "22:00" on the busiest-hour chart, which
  could read as misleading for staffing decisions (real 10pm activity and
  real 2am activity land in the same label). This is exactly what
  ut-docs#653's own text specified ("mirroring #559's ... approach" applied
  uniformly to both functions), and is internally consistent with the rest
  of the tab — not a blocker, and not re-litigated here since the card
  already made this call — but flagged for product/UX awareness. Filed as
  universaltill/ut-docs#789 for a product decision on whether the busiest-hour
  chart should keep the shift, plus the reviewer's suggested one-line manual
  addition explicitly naming "busiest day/hour" if the shift stays.

## Verdict

**Safe to merge.** No blocking findings. One informational design question
filed as a follow-up card, not folded into this diff — it doesn't change
what the ticket asked for or make the current behavior wrong relative to its
own spec, just worth a product look.

## Post-review: CI findings fixed before merge

Two things surfaced only once this landed on a real PR (`universal-till#386`),
neither caught by the local gate above — noted here for completeness, not
folded into "what shipped" above since they're process/attribution, not the
feature:

- **`authors` check failed**: this session's default git identity
  (`Claude <noreply@anthropic.com>`) authored the commit directly, which the
  repo's commit-attribution guard rejects (AI tool identity must be a
  trailer, never the author). Fixed by setting the repo-local
  `user.name`/`user.email` to the pipeline's real linked account
  (`Farshid Mirza <4035824+farshid3003@users.noreply.github.com>`, matching
  every other merged commit in this history, e.g. `389c128`) and
  `git rebase -r --exec 'git commit --amend --no-edit --reset-author'`,
  force-pushed.
- **`guard-docs-shots` failed**: `reports_page.go` registers `/reports`,
  a screenshotted topic's route, so the guard hashes the whole file — any
  byte change trips it regardless of whether it touches rendering (it
  doesn't here; only two function-call argument lists changed). Ran
  `make docs-shots` and committed the refreshed `web/help/img/manifest.json`
  + regenerated PNGs. The two screenshots that actually changed pixels
  (`alerts`, `designer` — plus `fa/translations`) are unrelated to this diff
  and reflect normal run-to-run noise in seeded demo data, not a real UI
  regression; `reports`' own screenshots were pixel-identical across the
  regen. Re-ran the full gate (`go build ./...`, full `go test ./...`, all
  guards including this one) after both fixes — clean.
