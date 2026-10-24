# Review — schedule ticker tests race the busy-key release (ut-docs#4903)

**Date:** 2026-10-24 · **Lane:** lane:cloud-41 · **Card:** ut-docs#4903

## What shipped

- `internal/plugins/wasm_schedules.go`: the tick goroutine's defers are
  reordered so `schedBusy.Delete(busyKey)` runs before
  `schedRunsInFlight.Add(-1)`. A zero in-flight count now means the next
  tick can be admitted. No other behaviour changes: both run after `fire`
  returns, and `wg.Done` is still last.
- `internal/plugins/wasm_schedules_test.go`:
  - The new `waitSchedRunsIdle` helper waits for the in-flight count to
    reach zero.
  - Tests call it before every advance to a tick they expect to fire, or
    expect to be stopped for a reason other than "busy": `FiresAtEveryPlusJitter`,
    `ZeroJitterIsExact` and `StopsAtNextTickWhenPermissionRevokedLive`.
  - The two inline wait loops in the skip-while-running tests now use the
    helper.
  - `recordingFire.tail` delays the seam's return. `ZeroJitterIsExact` uses
    100ms, which makes the CI race deterministic.

## Root cause

CI run on universal-till#2129 (`22f0729`, `test-heavy (internal/plugins)`):
`TestSchedule_ZeroJitterIsExact` failed with "expected a schedule tick to
fire". The log shows `wasm com.test.exact schedule com.test.exact.tick:
previous tick still running — tick skipped`.

The test does three things in a row:
1. It receives the tick from inside the fire seam.
2. It waits for the loop's next `After`.
3. It advances the clock.

All of that can happen before the run's goroutine has reached its deferred
`schedBusy.Delete`. The loop then skipped the next tick as busy. That is
production's skip-while-running rule working correctly; the test raced it.

## Verified (beyond CI)

- **Red first, on `origin/main`:** adding only `tail = 100ms` to
  `ZeroJitterIsExact` fails it the same way as CI, with "previous tick
  still running — tick skipped" and then "expected a schedule tick to fire".
- **Green after the fix:** the fixed test file on `origin/main` passes
  ×3. The branch passes ×30 under `-race`, across every fake-clock
  schedule test.
- **Full package:** `go test ./internal/plugins/` is ok (867s).
- **Gate:** `gofmt`, `go build ./...` and `golangci-lint ./internal/plugins/...`
  report 0 issues, and every `ci.yml` guard passes except two.
  - `guard-shellcheck-version` fails because this sandbox has no
    `shellcheck` binary. This diff touches no shell script.
  - `guard-deadcode-baseline` fails because `deadcode` can't load packages
    in this sandbox. This diff adds functions in test files only.

## Findings

An independent reviewer (Sonnet, a different model from the author) ran vet and the fake-clock schedule tests ×30 under `-race`, and reread the root cause. It also checked every reader of `schedRunsInFlight` and `schedBusy`: only `wasm_schedules.go` and the test helper read them, and Close drains through `wg`.

1. **Minor, accepted:** nothing fails if the defer order is reverted.
   - The idle-wait makes the *test-side* race deterministic. Under the old order, only a nanosecond window remains between the decrement and the delete.
   - Pinning that order would need a seam inside the goroutine's tail, which is more code than the risk is worth.
   - The reorder stays as defense in depth, with a comment saying why.
2. **Minor, accepted:** `recordingFire.tail` is an uninterruptible sleep.
   - At 100ms it only lengthens `Close` in that one test.
3. **Checked, no change:**
   - No other schedule test advances before the previous run is idle.
   - The added wait in `StopsAtNextTickWhenPermissionRevokedLive` cannot hide a regression. A busy skip would time out `waitSchedActive(0)`, and a missing permission check would fail `expectNone`.

## Verdict

Safe to merge. Production behaviour is unchanged apart from the defer order, and the tests no longer race the run's release. Deferred: nothing.
