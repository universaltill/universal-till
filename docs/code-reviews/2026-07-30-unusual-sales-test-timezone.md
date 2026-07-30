# TestUnusualSales flaky on non-UTC machines — fixed 2026-07-30

## Background

`ut-docs/QUEUE.md` (2026-07-25) flagged `internal/alerts.TestUnusualSales`
as failing locally on at least one real machine/date ("zero day on a
selling weekday should be unusual (ratio=0 unusual=false)") while passing
in CI — confirmed pre-existing on a clean `main` checkout, not caused by
an unrelated change, but never root-caused or fixed.

## Root cause

**Test-only bug, no production defect.** Production always writes
`created_at` as genuine UTC (`internal/pos/sales.go:211`,
`time.Now().UTC().Format(time.RFC3339)`), and `internal/data/pos_repo.go`'s
`DayTotal` queries `WHERE date(created_at, 'localtime') = date('now',
'localtime', ?)` — a single `'localtime'` SQLite conversion, correct only
when `created_at` is real UTC.

`TestUnusualSales`'s `sale()` helper seeded `created_at` via SQL
`datetime('now','localtime',?)` — i.e. **already-local** wall-clock time.
Applying the query's single `'localtime'` conversion to an already-local
value double-applies the UTC offset, shifting the computed date across
midnight whenever the local offset (doubled) crosses a day boundary. CI
(UTC, offset 0) never hits this — doubling a zero offset is a no-op.

Reproduced directly: reverting the fix and running
`TZ=Asia/Dubai go test ./internal/alerts/... -run TestUnusualSales -v
-count=1` reproduces the exact flagged failure message. With the fix,
the same command passes.

## Fix

`internal/alerts/alerts_test.go`: seed `created_at` as a real UTC
timestamp computed in Go (matching production's own convention and the
established `b8At` helper in
`internal/data/pos_repo_batch8_reports_test.go`), passed as a bound SQL
parameter instead of built via a SQLite date-modifier string. Also
anchored the seeded time to **midday UTC** (`time.Now().UTC().Truncate(24
* time.Hour).Add(12 * time.Hour)`) rather than the wall-clock instant the
test happens to run at, per the independent review's finding below.
`fmt` import removed (no longer used in this file). No production code
changed.

## Independent review (different model, opus)

**Verdict: SAFE TO MERGE**, no blocking findings. Re-derived the root
cause from source independently (read `sales.go`, `pos_repo.go`,
`alerts.go` directly rather than trusting the diff's claim). Its worktree
happened to sit on pre-fix `main`, giving a real A/B: reproduced the exact
failure message under `TZ=Asia/Dubai`, confirmed pass post-fix, and swept
10 timezones post-fix (UTC, Dubai, Kolkata, Kiritimati +14, Midway −11,
New York, Tokyo, Sydney, Anchorage, Kathmandu) — all green, where the
pre-fix code failed in Dubai/Kolkata/Kathmandu/Midway. Independently ran
the full gate (build/vet/both guards/`internal/alerts`+`internal/data`
`-race`) itself rather than trusting reported output.

One **finding, addressed**: the offset-only framing ("≥3.5h") understates
the real condition — the review's own hourly sweep across the
2026-11-01 US DST transition found `TZ=America/New_York` still produces
mismatches for ~4 weeks after the transition, always at local midnight,
because the seed used `time.Now()`'s actual clock time rather than a
fixed time-of-day. Confirmed this is inherent to `DayTotal`'s own
production semantics (not a new bug), but trivially eliminated in the
test by anchoring to midday UTC — applied above. Re-verified post-fix:
the same 10-zone sweep still green, and `America/New_York` specifically
re-checked.

Also confirmed explicitly, per the review's checklist: diff is test-only
(`git diff main --name-only | grep -v _test.go` empty); no other file in
the repo has the `datetime('now','localtime'` seeding anti-pattern
(grepped clean); the `AddDate(0,0,-daysAgo)` arithmetic is equivalent to
the old SQL `-N days` modifier for UTC times (no DST in UTC); the two
recurring bug classes (missing `os.MkdirAll`, cwd-relative path instead
of `paths.Data(...)`) don't apply — this diff adds no file writes; no
real client/shop name or literal secret introduced.

## Verified beyond the specific regression test

- `go build ./...`, `go vet ./...` clean.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
  both green (no SQL/string changes outside the test file regardless).
- `go test ./internal/alerts/... ./internal/data/... -count=1 -race`
  green.
- Full `go test ./...` green except one confirmed pre-existing, unrelated
  failure (`internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`
  — a root-in-container permission-check artifact, reproduced identically
  on an unmodified `main` checkout via `git stash`, same as noted in the
  2026-07-30 coverage-batch-11 review).

## Accepted scope

Out of scope, per the original queue item: the larger forecasting/
alerts backlog (multi-year averaging, lunar-holiday awareness, per-item
lead times, seasonal-spike alerts) — untouched, still open in
`ut-docs/QUEUE.md`.
