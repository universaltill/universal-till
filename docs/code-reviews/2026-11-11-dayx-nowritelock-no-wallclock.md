# Review — TestDayX_NoWriteLock without wall-clock bounds (ut-docs#6026)

**Date:** 2026-11-11 · **Lane:** lane:cloud-41 · **Card:** universaltill/ut-docs#6026 (complexity:easy)
**Author model:** Opus 5.5 · **Reviewer:** independent Sonnet subagent (different model, fresh context, own worktree)
**Process:** `[no-subagent: one-file test-only fix]` for BA/Architect/Dev; the review stayed independent.

## What shipped

`internal/pages/eod_day_x_test.go` → `TestDayX_NoWriteLock`. The old test asserted
that the view finishes in under 300 ms while another writer held the lock, and that a
concurrent insert waits under 100 ms. A loaded shared runner can exceed both with no
lock involved: `pages-shuffle` failed with "a concurrent sale insert waited 135ms".

The new test has no timing threshold on the passing path:

1. **The view does not take the write lock.** `HoldWriteLock` stays held until
   `readEODDayX` has returned. `db.Open` sets `_txlock=immediate` and busy_timeout 5000,
   so a view that opened a tx would wait out busy_timeout and fail with SQLITE_BUSY.
   A 30 s timer only stops a hang.
2. **The converse.** An insert runs on a pooled `*sql.Conn` with `PRAGMA busy_timeout = 0`
   while views run back to back. If a view held the write lock, the insert would fail at
   once with SQLITE_BUSY instead of waiting. On defer, the connection gets busy_timeout
   5000 back before `Close` returns it to the pool.

The old insert ignored its error. The new one checks it.

## Verification

- `go test ./internal/pages -run TestDayX -count=20 -shuffle=on` passed (author, twice,
  and again after the fixes; reviewer separately).
- `go test -race ./internal/pages -run TestDayX_NoWriteLock -count=5` passed (reviewer, and
  the author after the fixes).
- `go test -timeout 20m -shuffle=on ./internal/pages` is the CI pages leg (CI keeps the
  package out of `-race`, ut-docs#1034).
- `gofmt`, `go vet`, `golangci-lint ./internal/pages/...` (0 issues),
  `guard-fixed-dates.sh` and `guard-data-access.sh` all OK.
- **Mutation (author and reviewer):** `readEODDayX` was changed to hold `BeginTx` for its
  whole run.
  - Part 1 failed after ~5.3 s with `database is locked (5) (SQLITE_BUSY)`.
  - With part 1 neutralised (lock released early), part 2 alone failed 15/15 with
    SQLITE_BUSY in ~0.3 s.
  - After restoring the code, the test passed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low (failure path) | If part 2's first view errored, `started` was never closed and the test blocked until go test's 10 min timeout | Fixed: `select` on `started` or the views' error |
| 2 | Info | Part 1's view-error branch skipped `release()` before `Fatalf` | Fixed |
| 3 | Info | Part 2's overlap is probabilistic: a view holding the lock very briefly could slip past it. Part 1 covers that case deterministically. | Accepted |

The reviewer looked for false-positive SQLITE_BUSY sources and found none. The fixture
has no other writer in part 2, WAL readers don't block writers, and an auto-checkpoint
can't make the committing writer return BUSY.

**Pre-flight:** review record — updated; help topic / docs-shots — n/a (test only, no
user-visible behaviour); reference docs / README — n/a; en.json / meta — n/a; ADR — n/a.

**Verdict:** approve after fixes 1–2. Nothing deferred.
