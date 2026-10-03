# Review: `logging.defaultLogger` becomes an `atomic.Pointer` (ut-docs#3513)

- **Date:** 2026-10-03
- **Branch:** `fix/ut-docs-3513-logging-atomic-default-logger`
- **Card:** universaltill/ut-docs#3513 (`complexity:medium`, lane:cloud-41)
- **Author:** Dev (Opus 5.5). **Tester:** independent re-run of the TDD claim
  and the gate. **Reviewer:** Fable 5.1 — a different model from the author,
  in its own detached worktree (`/home/user/review-3513`), not on the shared
  branch checkout.

## What shipped

`internal/logging/logging.go`'s package global `defaultLogger *Logger` is
now `atomic.Pointer[Logger]`. `Init()` builds the logger into a local,
`Store()`s it, and logs the "initialised" line through the local (not
through a re-read of the global). `L()` returns `defaultLogger.Load()`.

The three tests that swap the global for a capture logger
(`recover_test.go` ×2, `init_race_test.go`) use `Load()`/`Store()`. A new
test, `swap_race_test.go` `TestDefaultLoggerSwapWhileLRunningIsRaceFree`,
starts a goroutine that calls `L()` in a tight loop, waits until it is
running, then swaps `defaultLogger` 500 times from the test goroutine. The
reader is already running before the first write, so no goroutine-creation
happens-before edge can order the write after the reads — only the type of
the variable can make it safe.

`once sync.Once` is unchanged (Architect's scoping: out of this card).
No CHANGELOG line, matching the #3410 precedent for a test-visible-only
race fix with no shop-owner-visible behaviour change.

## Relationship to the issue as filed

ut-docs#3513 was filed against `internal/netreach`'s
`TestPanickingProbeStillClearsInFlight`: a read in `L()` (then
`logging.go:104-105`, the `defaultLogger == nil` fast path) racing the
write inside `once.Do` (`:94`). **That exact pair was fixed by ut-docs#3410**
(`docs/code-reviews/2026-10-02-logging-init-race-3410.md`: "That is the
same pair the card reported from `internal/netreach`"). The reviewer
confirmed independently that on pre-fix `main` today
`go test ./internal/netreach/... -race -count=5` and the full
`internal/logging` suite at `-race -count=3` are both already `ok`.

What #3410 left behind is the hazard this card closes: `defaultLogger` was
still a plain pointer, so any concurrent write to it that is *not* the
`once.Do` first-init — today, a test's capture swap; tomorrow, any
production re-init — races every goroutine inside `L()`. `RecoverAndLog`
reaches `L()` from every background goroutine, so the window is real, just
not exercised by any existing test. The new test exercises it. Closing
#3513 with this PR is correct: the reported symptom is gone (via #3410) and
the root-cause shape is now fixed, not merely avoided.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix (docs) | The handoff framed this PR as fixing the netreach race in the issue text; it does not — #3410 did, and this PR fixes the remaining swap hazard. A PR body or record that implied otherwise would mislead `git log` readers. | **Fixed**: stated plainly above and in the PR body. No code change. |
| 2 | nit, accepted | `L()`'s comment still says a `defaultLogger == nil` fast path "is a data race". With an atomic the detector would no longer flag such a path, but it would still be a logic hazard (a nil observed before `Store`). The warning stays correct in spirit. | Accepted as is — not worth a touch outside the diff's purpose. |
| 3 | residual, deferred | `init_race_test.go` still resets `once = sync.Once{}` by plain assignment. This is the same shape as the bug just fixed. It is **currently safe**: the package has no `t.Parallel()` (grep: none), the reset runs before the test's own goroutines start, and no earlier test leaves a goroutine calling `L()` running (empirically: full suite `-race -count=5` clean). It would become a real race the day a `t.Parallel()` test, or a test leaking a logging goroutine, is added to the package. | **Deferred** per Architect's scoping. Reviewer agrees it is reasonable to leave as a documented residual: fixing it needs a different mechanism (a resettable init guard, or running the first-init test in a subprocess as `file_test.go` does), not another atomic. Recommend a Backlog card — see "Deferred". |

Checks that came back clean:

- `Logger.level` and `Logger.log` are set only in the composite literal at
  construction (`Init()` and the three test swaps). No assignment to either
  field exists anywhere in the module (`grep '\.level\s*=\|\.log\s*='`:
  only unrelated hits in `selfupdate`, `tax-tr/okc/sim`, `data`). So the
  struct behind the pointer is immutable after publication and `*log.Logger`
  is goroutine-safe for `Printf`; publishing the pointer atomically is
  sufficient — there is no second unsynchronised access to hide behind it.
- Nothing outside `internal/logging` references `defaultLogger` or `once`.
- `go.mod` is `go 1.25.0`; `atomic.Pointer[T]` needs 1.19+.
- `t.Errorf` from the reader goroutine in the new test is legal
  (`Errorf` is goroutine-safe; only `FailNow`/`Fatal*` are not).
- The two recurring bug shapes (a file write without `os.MkdirAll`, a
  cwd-relative path where `paths.Data(...)` belongs) do not apply: the
  diff is pure in-memory synchronisation and touches no path or file I/O.
- No secrets, no real shop or client names in anything touched.
- CI already runs `internal/logging` and `internal/netreach` under `-race`
  (`ci.yml`, the #3021/#3410 step), so the new test is gated in CI.

## Verification (reviewer's own, beyond the automated gate)

- **TDD red/green re-run, independently, in the detached worktree**
  (revert → run → restore inside one command so nothing could land
  mid-revert):
  - Red: `logging.go`, `recover_test.go`, `init_race_test.go` reverted to
    `main`; `swap_race_test.go` mechanically rewritten to the plain-pointer
    form (`.Load()` → bare read, `.Store(x)` → `= x`). 5/5 runs of
    `go test ./internal/logging/ -race -run TestDefaultLoggerSwapWhileLRunningIsRaceFree`
    `FAIL` with two `WARNING: DATA RACE` reports each: writes at
    `swap_race_test.go:44` and `:46` against reads at `:24` and `:28`.
  - Green: worktree restored to the commit under review (`git status`
    empty); 5/5 runs `PASS`, package `ok`.
- Pre-fix `main` baseline: `netreach -race -count=5` ok, logging suite
  `-race -count=3` ok (see "Relationship to the issue as filed").
- Gate on the branch: `go test ./internal/logging/... -race -count=5` ok
  (37.8s); `go test ./internal/netreach/... -race -count=5` ok;
  `go build ./...` ok; `go vet ./internal/logging/... ./internal/netreach/...`
  clean; `gofmt -l internal/logging` empty.
- No UI surface touched: no UX pass, no help-topic or screenshot update,
  no i18n keys. Stated explicitly rather than skipped silently.

## Deferred

- **Backlog follow-up (recommended, not filed by the reviewer):** make
  `init_race_test.go`'s `once = sync.Once{}` reset race-proof so the
  first-init test cannot become a data race when a parallel or
  goroutine-leaking test joins the package. Candidate shapes: hide
  `once` + `defaultLogger` behind one small init-state value swapped
  atomically and reset through a test-only hook; or run the first-use
  test in a child process the way `file_test.go` already does. Finding 3
  above.

## Verdict

Safe to merge. No blockers; one documentation should-fix handled in this
record and the PR body; one residual deferred with agreement.
