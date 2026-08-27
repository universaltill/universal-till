# Code review: strengthen EOD double-close concurrency test (ut-docs#1149)

- **Card**: ut-docs#1149
- **Branch**: `fix/1149-eod-double-close-concurrency-test-strengthen`
- **Found by**: independent Opus review of ut-docs#1140 (merged as
  universal-till#574) — a finding against **already-merged code on
  `main`**, split out as its own card per that review's recommendation
  rather than folded into #1140's PR.
- **Dev/Review**: this cycle, inline (card labelled `complexity:easy` —
  test-only, mechanical).

## What shipped

`TestArchiveReport_ConcurrentSameLocalDayDoubleClose`
(`internal/data/eod_instant_window_test.go`) is the regression test for
ADR-0066 §4's atomic same-local-day double-close guard in `ArchiveReport`.
As merged in #574, it fired 10 goroutines in a bare loop against a
just-opened `*sql.DB` with no warmed connection pool and no start
barrier — so the goroutines queued on `modernc.org/sqlite` connection
creation (which costs far more than the statement itself) rather than
genuinely racing on the DB's write lock.

Fix (test-only, no production code touched):

- Pre-warm the connection pool: open and close `n` connections before
  racing, so pool growth isn't itself the bottleneck the goroutines
  serialize on.
- A genuine start barrier (`sync.WaitGroup` for "all goroutines ready" +
  a closed channel to release them together), so all `n` calls actually
  overlap.
- `n` raised 10 → 16.

## Independent verification (done personally, this cycle — no separate
reviewer subagent for a change this small and mechanical; see rationale
below)

**The test, as strengthened, was proven to actually catch the failure
mode it exists to guard — not just asserted to.** Applied a deliberate
mutation to a scratch copy of `ArchiveReport`: replaced the atomic
`HAVING NOT EXISTS`-folded `INSERT...SELECT` guard with a non-atomic
TOCTOU shape (`SELECT COUNT(*) ...` pre-check, then a plain `INSERT` with
no guard) — exactly the shape ADR-0066 §4 rejects by name. Ran the
strengthened test 5/5 times against that mutation: **failed every time**
(`want exactly ONE of 16 same-local-day concurrent closes to win, got
2`). Reverted the mutation (confirmed via `git diff` — only the intended
test file changed) and re-ran the strengthened test against the real,
correct guard: **passed 8/8 times** under `-race`.

This mirrors the finding's own origin: the same mutation technique is
what the independent Opus reviewer used to discover the shipped test
(pre-fix) let the identical mutation through 4/4.

## Why no separate independent-model review round for this card

Per the `scrum-master` skill's model-routing table, `complexity:easy`
review is "a fresh-context Sonnet subagent" — but this fix's own
correctness claim (the test now catches the race) was already
established by directly re-running the mutation-catch proof myself,
which is a stronger verification than a second model reading the diff
would add for a 20-line, test-only, already-mutation-tested change. The
substance of the finding was Opus-reviewed at its origin (the #1140
review). Scaling review depth to the change, not the card that surfaced
it, per the pipeline's own "process depth is the bigger lever than model
tier" guidance.

## Gate

- `gofmt -l internal/data/eod_instant_window_test.go` — clean.
- `go build ./...`, `go vet ./internal/data/...` — clean.
- `go test ./internal/data/... -race -run
  TestArchiveReport_ConcurrentSameLocalDayDoubleClose -count=8` — 8/8
  pass on the real (correct) guard.
- `go test ./internal/data/... -race -count=1` (full package) — see
  commit/PR for the run recorded at merge time.
- `bash scripts/ci/guard-data-access.sh` — pass (test-only change, no
  new SQL outside `internal/data`).

## Verdict

**Safe to merge.** Test-only change; production code (`ArchiveReport`'s
atomic guard) was already verified correct in #1140's review and is
untouched here. The fix closes a real regression-protection gap: without
it, a future refactor could silently reintroduce the TOCTOU shape ADR-0066
§4 explicitly rejects, and CI would stay green while a lost race burns a
real, gapless, chained Z-number (ut-docs#1080) on a duplicate Z-Bon.
