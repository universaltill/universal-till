# Code review: don't hold s.mu through the restart-backoff sleep in monitorProcess

**Card:** universaltill/ut-docs#502
**Date:** 2026-08-09
**Complexity:** medium — Dev inline (Sonnet), Review via an independent Opus
subagent (isolated worktree). One review round: it found one test-strength
gap (not a defect in the production fix) and fixed it directly in its
worktree; nothing money/tax/data-loss/security class, so a second
independent round wasn't earned per this pipeline's process-depth rule.

## What shipped

Follow-up from ut-docs#380's own independent review (Finding 2, filed as
ut-docs#502). `Supervisor.monitorProcess` (`internal/plugins/supervisor.go`)
held `s.mu` across its entire restart path, including
`time.Sleep(backoff)` — up to `BackoffMax` (30s under `AutoStartPlugins`'
policy). `Supervisor.Shutdown` acquires `s.mu.Lock()` at the very start of
its own cancel loop, so a plugin mid-restart-backoff when `Shutdown` was
called blocked `Shutdown` on that lock for up to ~30s **before it ever
reached the ctx-bounded `wg.Wait()`** ut-docs#380 added — defeating that
fix's own bound, and reopening the exact `database.Close()`-races-a-live-
monitor class #380 set out to close.

Fix:

- **`internal/plugins/supervisor.go`**: `monitorProcess` now releases
  `s.mu` before the backoff sleep and re-acquires it after, instead of
  holding it via a single `defer` across the whole function. The sleep is
  `select`'d against `ctx.Done()` too (`ctx` is the plugin's own
  `procCtx`, cancelled by `Shutdown`/`StopPlugin` via `proc.cancel()`
  before either sets `proc.stopped`), so a deliberate shutdown wakes the
  goroutine immediately instead of wasting the rest of a long backoff.
  After re-acquiring the lock, `proc.stopped` is re-checked before
  proceeding to actually restart — `Shutdown`/`StopPlugin` may have run
  while this goroutine was asleep without the lock held.
- New test: `TestSupervisor_Shutdown_DoesNotBlockOnMonitorProcessRestartBackoff`
  (`internal/plugins/shutdown_drain_test.go`) — starts a crash-looping
  plugin with a 30s backoff, waits for the crash to be audited, then calls
  `Shutdown` with a 2s ctx timeout and asserts it returns promptly and has
  actually drained (not just given up loudly at the ctx deadline).

## Independent review (Opus, isolated worktree)

Ran the full gate itself (`go build`, `go vet`, the full
`internal/plugins` package under `-race`, all 3 CLAUDE.md guards, plus a
`-count=15` stress run under load), did a real revert-then-restore TDD
check, traced the lock discipline by hand across all six `monitorProcess`
return paths, and empirically probed the `ctx.Done()` wake path against
the real `AutoStartPlugins`/shutdown ordering.

**Verdict: safe to merge, production fix correct as written.** One
test-strength finding, fixed by the reviewer directly (pulled into this
branch verbatim — see below).

### Finding — MEDIUM, fixed

The regression test's own bound (`elapsed > 3*time.Second`) was wide
enough to pass via `Shutdown`'s *give-up* path (returns `nil` once its
2s ctx expires, having achieved nothing) as well as its *drain* path
(the intended behavior) — so it couldn't distinguish "drained cleanly"
from "gave up loudly after burning its whole budget while the monitor
was still asleep." The reviewer proved this by deleting just the
`ctx.Done()` case from the fix in a throwaway copy: the test still
**passed at 2.09s** with the monitor still live in its 30s backoff —
exactly the hazard ut-docs#380 exists to prevent, and the test's own
docstring claim ("proves Shutdown wakes it") would have been false.

Fixed by tightening the bound to `elapsed > time.Second` (the real drain
measures ~0.1s, ~10x headroom) and adding the same
`waitForNoMonitorGoroutines(t, 2*time.Second)` assertion the two sibling
drain tests already use, so the test also confirms the monitor goroutine
is actually gone, not merely that `Shutdown` returned. Re-verified in
both directions: fails (`"should wake the backoff sleep via ctx and
drain..."`) with the `ctx.Done()` case removed; passes `-count=15` under
CPU contention with the real fix.

### Deferred, not fixed (pre-existing, unchanged in kind by this diff)

- `RestartPlugin` reads `proc.Cmd.Path`/`proc.Cmd.Args` after releasing
  its `RLock`, while `monitorProcess` writes `proc.Cmd` under the write
  lock elsewhere — a genuine pre-existing data race, untouched by this
  commit and unexercised by any test (race detector stayed silent on it).
  Not filed as a new card — flagged here for visibility; worth a follow-up
  if anyone is in that function next.
- Each restart derives a new `context.WithCancel(ctx)` from the previous
  `procCtx` without cancelling the old one — a small, bounded (by
  `MaxRestarts`) context-node leak per restart chain. Pre-existing.
- A doomed restart (one that loses the race against a shutdown/stop
  signal) leaves the proc in `s.processes` with `stopped=false`, so
  `IsRunning()` can report a dead plugin as running. Pre-existing, same
  end state as before this fix.
- `time.After(backoff)` leaks its timer until it fires when `ctx.Done()`
  wins the select instead. Bounded (≤30s, one per plugin per crash);
  `time.NewTimer` + `defer Stop()` would be more idiomatic but wasn't
  worth churning the commit for.

## Verified beyond the automated suite

- **Lock discipline traced by hand** across all six `monitorProcess`
  return paths (early-stopped / policy-disabled / restart-limit-reached /
  backoff-then-stopped-recheck / restart-failed / successful restart):
  every path pairs exactly one `Lock` with exactly one `Unlock`, no
  double-lock, no leaked lock, `defer s.mu.Unlock()` registered only
  after the *second* `Lock()` (the self-deadlock bug class an earlier
  draft of this fix actually had during Dev — caught before commit, not
  by this review — is confirmed absent here).
- **`s.wg` accounting** re-verified: `defer s.wg.Done()` is the first
  statement, so all six return paths still pair correctly; `s.wg.Add(1)`
  before every respawn is unchanged and still executes before the
  spawning goroutine can return.
- **TOCTOU on `proc` fields after the re-acquire**: confirmed the
  `proc.stopped` recheck is sufficient — `RestartPolicy` is
  write-once at construction, `Cmd`/`cancel`/`RestartCount` are mutated
  only by the single live `monitorProcess` for that `proc` under the
  write lock, and `Shutdown` holding `s.mu` across its *entire* cancel
  loop (including clearing `s.processes`) means a re-acquiring monitor is
  strictly before or strictly after `Shutdown`'s critical section — no
  window orphans a live process.
- **`ctx.Done()` racing the reacquire, empirically**: probed against the
  real production ordering (root ctx cancels before `server.Start`'s
  shutdown goroutine reaches `supervisor.Shutdown`) — a monitor can wake
  via `ctx.Done()` with `proc.stopped` still `false`; confirmed the
  resulting doomed restart attempt fails fast (`cmd.Start()` returns
  `ctx.Err()` immediately), takes no `wg.Add`, spawns no process and no
  new monitor goroutine — strictly improved over the pre-fix behavior
  (same doomed restart, just after burning a full backoff sleep first),
  not a new hazard.
- **Revert-then-restore TDD, real, by the reviewer**: reverted
  `supervisor.go` to pre-fix, re-ran the new test alone — failed with
  `"Shutdown did not return — still blocked, likely on s.mu held through
  the restart backoff sleep"` after the full 10s wait, exactly the
  claimed symptom; restored, diff empty, test passes `-count=3` at
  ~0.09s each.
- Full `go build ./...`, `go vet ./...`, full `go test ./... -race`
  (all packages, zero failures, no data race reported), and all 3
  CLAUDE.md guards (`guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`) — run by Dev, by the reviewer in its isolated
  worktree, and once more here after pulling the reviewer's fix in.
- **The two recurring bug classes this pipeline keeps re-finding**
  (missing `os.MkdirAll` before a file write; a cwd-relative path where
  `paths.Data(...)`/`paths.Plugins()` belongs): confirmed not applicable
  — `supervisor.go` has zero file I/O; the one `os.WriteFile` in the diff
  is test-only, into `t.TempDir()`.
- No raw SQL added outside `internal/data`/`internal/db` (none touched);
  no user-facing strings (this is backend concurrency plumbing, no
  template/route/i18n-key change); no money involved; no ADR needed —
  this extends the existing lock/goroutine pattern `internal/plugins`
  already uses, not a new architectural decision.
- No real client/shop name anywhere in the diff or its tests (fixture IDs
  are `com.test.*`); no secret-shaped literal.

## Safe-to-merge verdict

Yes, with the reviewer's test-strengthening change folded in (applied
verbatim from the review worktree). No manual/`web/help/` update needed —
backend shutdown-lifecycle plumbing with no shop-owner-visible surface;
confirmed explicitly from the diff, not skipped.
