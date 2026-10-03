# Review: first-use L() concurrency test runs in a child process (ut-docs#3525)

- **Date:** 2026-10-03
- **Branch:** `fix/3525-init-race-test-subprocess`
- **Card:** universaltill/ut-docs#3525 (`complexity:easy`, lane:cloud-41b)
- **Author:** Dev (Sonnet). **Reviewer:** Opus 5.5 — a different model from
  the author, fresh context, in its own detached worktree.

## What shipped

`TestLConcurrentFirstUseIsRaceFree` (`internal/logging/init_race_test.go`)
used to retest first use by resetting the package-level `once` with a bare
`once = sync.Once{}` — the same unsynchronised-write shape #3513 fixed for
`defaultLogger`, harmless today only because nothing in the package uses
`t.Parallel()` or leaks goroutines. The Dev took the issue's shape (b): the
test re-execs its own binary (`os.Args[0]`, gated by
`UT_TEST_CONCURRENT_FIRST_USE`), and the child runs the same 16-goroutine
check against never-touched `once`/`defaultLogger` state, exiting 0/1. Same
pattern as `TestConsoleSinksAreRedacted` in `file_test.go`. No production
code changed.

Reviewer fixes on top (test file only):

1. The parent runs the child **5 times** (`firstUseRuns`).
2. The child prints a sentinel (`ut-first-use-ok`) after its checks pass;
   the parent requires it in the child's stdout.
3. `-test.run` is anchored at both ends (`^…$`).
4. The child gets `GORACE=<caller's GORACE> atexit_sleep_ms=0`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | The rewrite weakened the regression check (acceptance criterion 3). With `once.Do` removed from `Init()`, the WIP test failed only ~85% of runs (8/10, then 15–19/20 over five batches), not the claimed 10/10. The pre-change in-process test failed ~97% (19, 19, 20 of 20). A process has only one first use, so the child gets one chance to interleave. | Fixed: 5 fresh children per test run. Same mutation now fails 100/100 under `-race` and 50/50 without it. |
| 2 | blocker | Vacuous pass. A test binary whose `-test.run` matches nothing prints "no tests to run" and exits 0 (confirmed). If the test is renamed and the string literal isn't, the parent passes without checking anything. The precedent avoids this by asserting marker counts in the output. | Fixed: child prints a sentinel and the parent requires it. Confirmed by pointing the filter at a nonexistent name: fails with "exited 0 without running the check". |
| 3 | nit | Each `-race` child spent a flat ~1.0s in the race runtime's default `atexit_sleep_ms=1000` at exit. Mutated runs that exit 1 skip it and took 0.02s. | Fixed: `atexit_sleep_ms=0` added after the caller's GORACE, so their options are kept. A probe confirmed race reports and exit 66 still work with it set. Test now takes ~0.09s for 5 children, down from 1.02s for one. |
| 4 | nit | `-test.run=TestLConcurrentFirstUseIsRaceFree$` was not `^`-anchored, so a future `…XTestLConcurrentFirstUseIsRaceFree` would also run in the child. | Fixed: `^…$`. |
| 5 | info | The race detector never fires for the `once.Do`-removed mutation. `defaultLogger` is an `atomic.Pointer` and stdlib `log` is mutex-guarded, so only the identity assertion catches it. `-race` still covers any future non-atomic access. | Accepted. The comment says the child is the same race-instrumented binary and exits 66 on a race. |
| 6 | nit | No timeout or context on the child. If the parent hits `-test.timeout` and panics, a hung child would be orphaned. | Accepted: same as the precedent. The child only waits for 16 `L()` calls, and `cmd.Run()` waits for the child, so there is no zombie in the normal path. |
| 7 | nit | If a developer has `UT_TEST_CONCURRENT_FIRST_USE` exported, the parent runs in child mode. | Accepted: `go test` passes `-test.paniconexit0`, so the parent's `os.Exit(0)` panics and the run fails loudly, not silently. The child is started without that flag, so its `os.Exit(0)` is allowed. |

## Verified beyond the tests

- **TDD re-verified by the reviewer.** In a scratch copy (the shared
  checkout's `logging.go` was never edited), `once.Do(func(){…})` in `Init()`
  became an unguarded `func(){…}()`. Results:
  - WIP test: failed 85% of runs.
  - Original in-process test: failed 97%.
  - Fixed test: failed 100/100 with `-race` and 50/50 without.

  With the real `logging.go`, the fixed test passes.
- **Race exit propagates through re-exec.** A standalone probe (child does an
  unsynchronised `shared++` from 4 goroutines, then `os.Exit(0)`) exits 66 and
  prints a DATA RACE report, with and without `atexit_sleep_ms=0`. The race
  runtime's exit hook runs `racefini` on exit code 0, and `os.Args[0]` is the
  same instrumented binary.
- **Criterion 1:** no `once =` write remains in the package's tests. The
  other `defaultLogger` writes (`swap_race_test.go`, `recover_test.go`) are
  atomic `Store`s. No `t.Parallel()` in the package.
- **Original assertions kept:** non-nil logger, and every goroutine gets the
  same instance. The old `log.Writer()`/`log.Flags()` restore cleanup is gone
  and not needed, because the child's process-global changes end with it.
- **Gate:** `gofmt -l` (clean), `go build`, `go vet`, `golangci-lint`
  (0 issues), `go test ./internal/logging/... -race -count=20` (ok, 149s; the
  package's other tests take most of that), and the same without `-race`
  (ok).
- **Production code:** `git diff -- internal/logging/logging.go` is empty.
- CLAUDE.md rules (repository pattern, money, i18n, offline-first, plugin
  signing) don't apply to a test-only change. No UI or help topic touched.

## Verdict

Safe to merge with the reviewer fixes. The Dev's version met criteria 1 and 2
but not 3 (it caught the target regression less reliably than the test it
replaced, and could pass vacuously). Both are fixed here. Nothing deferred.
