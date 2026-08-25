# Code review: `main` red — `TestUnusualSales` date-dependent (ut-docs#969)

**Branch:** `fix/969-unusual-sales-date-determinism` · **PR:** universal-till#506
**Reviewer:** independent Opus subagent (complexity:medium → Opus review, per
scrum-master's model routing) · **Author:** Sonnet (this pipeline cycle)

## What shipped

`main` was red: `TestUnusualSales` in `internal/alerts` failed intermittently,
reported just after midnight on a Monday. Root cause: `data.POSRepo.DayTotal`
resolved "today" via SQLite's own `'now'` at query time; `unusualSales` calls
`DayTotal` five times in a row (yesterday + 4 baseline weeks), so if the real
clock ticked over a day boundary between two of those calls, each read could
resolve a *different* "today" — a race between two independent clock reads,
not a weekday-specific detector bug.

Fix: `DayTotal(ctx, daysAgo, ref time.Time)` and `unusualSales(ctx, db, ref)`
now take a caller-supplied reference instant instead of reading the clock
internally; every caller doing multi-day comparisons shares one Go-side
`ref`. New tests (`TestUnusualSales_EveryWeekdayIsDeterministic`,
`TestPOSRepo_DayTotal_EveryWeekdayIsDeterministic`) sweep all 7 weekdays with
fixed reference instants to prove the fix is calendar-day-agnostic rather
than "happens to pass today".

## Independent review — verdict: safe to merge

Full independent pass (different model, fresh context, isolated worktree):
correctness of the `date(?, 'localtime', ?)` rewrite verified empirically
across timezones (UTC, Europe/Berlin, Pacific/Kiritimati +14,
America/New_York) including a DST-transition window — no new edge case, the
day-boundary race is genuinely eliminated (one remaining clock read in
`Start()`'s own polling loop, which is correct — it's not part of the
5-reads-in-a-row comparison the bug was in).

**TDD re-verified independently, not taken on trust**: reverted all 5 touched
files to the parent commit and reproduced the *exact* reported failure
(`zero day on a selling weekday should be unusual (ratio=0 unusual=false)`)
deterministically via `TZ=America/New_York` / `TZ=Pacific/Kiritimati` (rather
than waiting for a real midnight race) — confirms the pre-fix code was
broken on more than just the reported midnight window. Restored and
confirmed green across 5 timezones. Separately mutation-tested the new sweep
tests themselves (patched `DayTotal` to ignore `ref` and use `'now'` again):
6 of 7 weekday subtests failed, with the 7th (today's real weekday)
coincidentally passing — a clean demonstration that the sweep catches
exactly the "happens to pass today" pathology it exists to prevent.

Guards: all 16 CI-blocking guards from `ci.yml`'s `build` job pass.
`go build`/`go vet`/`gofmt -l .` clean. Full `go test` for every touched
package green (`internal/alerts`, `internal/data`, `internal/pages` +
subpackages). Scoped `-race` runs (full-package `-race` on these SQLite-heavy
packages exceeds this container's default per-package timeout — documented,
pre-existing container-resource limit, not a CI-relevant regression; `ci.yml`
itself never runs `-race`) on the affected tests: no races reported.
`-count=20` flake sweep: zero failures. Backend-only change — no manual/UX
update needed (confirmed no shop-owner-visible surface changed);
`guard-help-topics.sh`/`guard-docs-shots.sh` both pass, screenshots
byte-identical.

## Findings — 4 nits, all fixed in this same review pass (none were blockers)

- **N1**: `alerts_test.go`'s `ref := time.Now()` (two occurrences) is local
  time, but a neighbouring comment asserted `createdAt` is "genuine UTC" —
  true only by SQLite normalizing an explicit offset before `'localtime'`,
  not because the code actually produces UTC. Fixed: `time.Now().UTC()`, so
  the comment's stated invariant now actually holds.
- **N2**: two comments said "UTC day boundary" where the boundary that
  actually matters (both sides go through `'localtime'`) is the *local* day
  boundary — accurate for the CI-observed flake (CI runs `TZ=UTC`) but
  misleading as the general statement it was phrased as. Fixed the doc
  comment in `pos_repo.go`; left the two "just after ... midnight" sweep-test
  comments corrected to say UTC (the sweep instants genuinely are
  `time.Date(..., time.UTC)`, so "UTC midnight" is the accurate description
  there, not "local").
- **N3**: an unparseable `ref` would make `date(?, ...)` return NULL and
  `DayTotal` silently return `(0, 0, nil)` rather than erroring — not
  reachable today (`ref.UTC().Format(time.RFC3339)` always parses for any
  real `time.Time`), so left as a documented non-issue rather than adding
  speculative defensive code with no live caller that needs it.
- **N4**: `backoffice_page.go` took a second independent `time.Now()` read
  (`weekNow`) right next to the newly-shared `dayRef` — harmless (the week
  window is a rolling 7×24h span, not calendar days, so a boundary tick
  can't misalign it) but inconsistent with the fix's own "one instant per
  render" principle. Fixed: `weekNow := dayRef.Add(time.Second)`.

**Deferred, not part of this fix** (noted for a future Backlog card, not
actioned here — out of scope for this bug's fix): the same `'now'`-in-SQL
pattern remains at `pos_repo.go`'s `seasonalWindowQuery`, `julianday('now')`,
and `AuditActionSummary`, and `reports_page.go` calls `reportNow()` three
separate times per handler. None of these do the multi-call comparison this
bug was in, so none are live bugs — just the same pattern this fix just
established as worth avoiding.

## Verified beyond automated tests

- Real TZ sweep (10 zones including half/quarter-hour offsets) against the
  fixed code: all green.
- Independent re-derivation of the SQL semantics (`date('now','localtime',?)`
  vs `date(?,'localtime',?)`) via direct SQLite probes, not just reading the
  diff.

## Safe-to-merge verdict

**Yes.** No blocker-class findings (no money/tax/data-loss/security issue).
Second review round not warranted — first round found nothing blocker-class,
just nits, all fixed here without a second full pass.
