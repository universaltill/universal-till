# Code review: fix clock-origin mismatch in TestSupervisor_Shutdown_TimesOutLoudlyOnWedgedMonitor

**Card:** universaltill/ut-docs#507
**Date:** 2026-08-09
**Complexity:** easy — Dev inline (Sonnet), Review via a fresh-context Sonnet
subagent (isolated worktree). One review round; no findings, so no second
round was earned per this pipeline's process-depth rule.

## What shipped

`internal/plugins/shutdown_drain_test.go`'s
`TestSupervisor_Shutdown_TimesOutLoudlyOnWedgedMonitor` intermittently
failed under CPU load with `Shutdown returned after 9Xms, before its ctx
deadline`. Root cause: the test created its `context.WithTimeout(…,
100ms)` **before** capturing `start := time.Now()` — any scheduling gap
between those two lines meant the context's clock and the test's own
`elapsed` clock didn't share an origin, so `elapsed < 100ms` could fire
spuriously even though `Shutdown` genuinely waited the full deadline.

Fix: a pure 2-line reorder — `start := time.Now()` now comes immediately
before the `context.WithTimeout` call, so both clocks start from the same
instant. No change to the timeout value, the assertion bound, the
ERROR-log check, or the test's intent — a measurement-order fix only, per
the card's own acceptance criteria.

## Independent review (fresh-context Sonnet, isolated worktree)

Read the diff (`git show HEAD`) plus `Supervisor.Shutdown`
(`internal/plugins/supervisor.go`) to confirm the test's assertion still
matches real `Shutdown` behavior (it does: `Shutdown` blocks on
`s.wg.Wait()` via `select` against `ctx.Done()`, and logs the same
"still running when shutdown context expired" ERROR line the test checks
for). Ran `go build ./...`, `go vet ./...`, and the full
`internal/plugins` package — all clean.

Did its own revert-then-restore TDD verification under artificial CPU
load (8 `yes` processes on a 4-core box, load average 5.5–11.8):

- **Pre-fix** (manually reverted the reorder), `go test -race -count=50
  -run TestSupervisor_Shutdown_TimesOutLoudlyOnWedgedMonitor
  ./internal/plugins/...` under load: **3/50 failures**, e.g. `Shutdown
  returned after 80.35ms, before its ctx deadline` — exactly the
  described flake.
- **Post-fix**, same command run twice (100 total iterations) under
  equal-or-higher load (load average 11.03 at start): **0/100
  failures**.

### Findings

None. Diff is scoped to exactly the one test file (`git show --stat`
confirms 1 file, +1/-1 lines); no other files touched, no scope creep, no
loosened assertion.

## Verified beyond the automated suite

- Targeted flake repro (above) — reliably reproduced pre-fix, confirmed
  clean post-fix under load, independently by both Dev and the reviewer.
- Full `go build ./...`, `go vet ./...`, full `go test ./...` (every
  package, zero failures) — run by Dev before handoff.
- All 3 CLAUDE.md guards (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`) — clean; none are relevant to a test-only
  change but run per standing policy anyway.
- No raw SQL, no money, no user-facing strings, no route/template change —
  this is a test-file-only timing fix. No ADR needed; not an architectural
  decision. No manual/`web/help/` update needed — confirmed from
  `git diff --stat` that nothing shop-owner-visible changed.
- No real client/shop name anywhere in the diff; no secret-shaped literal.

## Safe-to-merge verdict

Yes, as-is — no changes needed from the review.
