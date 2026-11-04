# Review: move test fixtures expire relative to now — ut-docs#5468

Date: 2026-11-04 · Lane: `lane:cloud-41` · Branch: `fix/5468-move-test-expiry`

## What shipped

`main` went red at about 2026-11-04 09:30 UTC, in `test-heavy (race - enroll…)`, the pages jobs, `pages-shuffle` and `build`.
The cause is in the ut-docs#5127 move tests. They hard-coded `expires_at = "2026-11-03T09:30:00Z"`, and `enroll.PendingMoves` drops any move older than its expiry plus 24 h of grace.

Fix (tests only):
- `moveExp` (`internal/enroll/move_5127_test.go`), `mvExp` (`internal/pages/move_approval_5127_test.go`) and the new `pmExp` (`internal/cloudsync/pending_moves_5127_test.go`) are now `time.Now().UTC().Add(72h)`, truncated to whole seconds and in RFC 3339 format.
- In `TestPendingMoveRecordIsImmutablePerID`, the swapped record's expiry is now now+96h. Before, it was a fixed `2026-11-09` date that would have expired the same way next week.

## Process

Dev: Sonnet subagent. Review: independent Opus subagent.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The same expression is repeated in three test packages | accepted (separate packages) |

The reviewer checked every code path for now+72h:
- parse: 40-char cap, RFC 3339
- no upper bound on expiry
- `approvalkey.go` signs `ExpiresRaw` verbatim, and the tests build their expected signature from the same var
- no test asserts the rendered expiry text
- the immutability test still compares two different values

The other near-future literals (for example `usable_from`, and the display-only entitlement `KeyExpiresAt` `2026-11-14`) are never compared to the clock.

## Verification

- TDD was re-verified in a scratch worktree. On `origin/main`, `go test ./internal/enroll/` gives `FAIL` (9 move tests). On this branch it gives `ok`.
- These runs were `ok`:
  - `go test -race ./internal/enroll/`
  - `go test ./internal/pages/` (full package, 336 s)
  - `go test ./internal/cloudsync/`
  - `go test -race -run 'Move|Pending' ./internal/cloudsync/`
- The full `-race` cloudsync run is too slow for this box. CI runs it.
- `gofmt -l` and `go vet` are clean.

Pre-flight:
- help topic: n/a (tests only, no behaviour change)
- reference/: n/a
- README: n/a
- en.json: n/a
- follow-up guard against fixed future timestamps in clock-filtered fixtures: deferred → Backlog card filed with this cycle's close-out

## Verdict

Approve.
