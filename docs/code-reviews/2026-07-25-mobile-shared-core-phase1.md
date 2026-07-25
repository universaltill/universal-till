# 2026-07-25 — Android/iOS shared-core Phase 1 (ADR-0023, spec 013)

## Context
Farshid asked to raise Android till's priority and, in this session, to
start building it. Confirmed first (not assumed): no Android SDK, no
`gomobile`, no Xcode iOS SDK exist in this environment (`gomobile bind
-target=android` fails immediately with "could not locate Android SDK" —
run and observed directly). Scoped this session's work to exactly what's
buildable and testable with plain `go build`/`go test` regardless of
that: the Go-side groundwork `gomobile bind` will eventually wrap,
deliberately not the native Kotlin/Swift shells themselves (hand-written,
unbuildable, unverifiable native code would be worse than none).

## Design
- `internal/app.Run(ctx) error` — `main.go`'s full boot sequence
  (config → DB/migrations → plugin host → marketplace enrolment →
  pages/mux → background jobs → HTTP server), extracted verbatim so it's
  callable from more than one entry point. `main.go` now just calls it.
- `mobile/mobile.go` — the actual gomobile-bind surface: `Start(dataDir
  string) (string, error)`, `Stop()`, `IsRunning() bool`. Runs
  `internal/app.Run` in-process (mobile apps can't spawn a sibling
  binary the way `cmd/unitill-desktop` does), same "start, poll
  `/healthz`, then hand back an address" shape as the desktop shell.

## Independent review
Sonnet-model review, explicitly asked to verify the "behavior-preserving
refactor" claim line-by-line against `main.go`'s git history, not just
trust it, plus scrutinize `mobile.go`'s concurrency model.

**Confirmed correct**: all 8 `log.Fatalf` call sites in the old `main.go`
map 1:1 to `return err` in the same order in `internal/app/app.go`,
nothing dropped or reordered; the pre-existing silent-swallow on
`SaveRuntimeConfig`'s error is preserved exactly; `defer database.Close()`
fires correctly across every return path including the final blocking
`server.Start` call; env-var mutation via `os.Setenv` is safe against the
package's own concurrent callers (synchronous before the `go` statement,
serialized by the mutex); no incorrect comments.

**One real, deliberate behavior difference found, not a bug**: the
original inline `log.Fatalf` calls `os.Exit(1)`, skipping all deferred
functions — a fatal boot failure after `db.Open` succeeded used to leave
`database.Close()` never called. The extracted `Run` returns instead, so
that same deferred close now genuinely executes on every fatal path.
Checked every step for a dependency on the old abrupt-exit semantics —
found none. Strict improvement (clean SQLite shutdown even on a fatal
failure), documented rather than silently absorbed into an inaccurate
"behavior-preserving" claim.

**Four real bugs found in `mobile.go`'s first draft, all fixed:**

1. **Lock held across the ~10s ready-wait.** `Start` held `mu` through
   the whole blocking `waitUntilReady` call, so a concurrent `Stop()` or
   `IsRunning()` could block for the full timeout — a real risk for the
   exact scenario the code's own comment called out (iOS calling `Stop`
   from a termination callback with a short OS watchdog budget, racing a
   slow `Start`). Fixed: the lock now guards only brief state
   transitions (swapping the `inst` pointer), never the actual blocking
   start/stop work.
2. **Stale success after an unobserved crash.** If the server died on
   its own after a successful `Start` (e.g. a listener error, `Stop`
   never called), nothing ever read the abandoned error channel —
   `IsRunning()` and a subsequent `Start()` kept reporting the old,
   dead address indefinitely. Fixed: both now check the instance's
   `done` channel non-blockingly and correct the tracked state (and, for
   `Start`, actually restart) instead of lying.
3. **`Stop()` didn't actually drain.** It cancelled the context and
   returned immediately, before `app.Run`'s goroutine (and its deferred
   `database.Close()`) had actually finished — a native shell calling
   `Stop()` then quickly `Start()`-ing again against the *same* on-device
   data dir (backgrounded then rapidly foregrounded is a plausible real
   sequence) could race the old instance's in-flight teardown against
   the new instance's `db.Open()` on the same SQLite file. Fixed:
   `Stop()` now blocks on a `done` channel until the server has
   genuinely finished.
4. **Different-`dataDir` `Start` while running silently ignored the new
   value.** Minor, but a caller asking for a dataDir switch deserves an
   error, not a silent no-op serving the old one. Fixed: now an explicit
   error.

Two test-quality gaps also fixed: the post-`Stop` liveness check used to
poll and treat the first connection failure as conclusive (a real, if
low-probability, false-pass risk) — now `Stop()` genuinely drains before
returning, so a single deterministic check suffices. Added a test for
the fast-fail path (`app.Run`'s goroutine erroring before `/healthz` ever
answers, via a data dir that's a regular file instead of a directory)
and a test for the crash-detection scenario in finding #2, neither of
which existed before review.

## Verification
`go build ./...`, `go vet ./...`, `go test ./...` (full suite green,
`mobile` package included), `go test ./mobile/... -race -count=3`
(clean, no data races, zero flakes across 6 tests × 3 runs), both
`scripts/ci/guard-*.sh` — all green, before AND after the fixes.

Live-verified the refactored CLI entry point against a real built
binary: boots, `/healthz` and `/report-issue` both 200, identical to
pre-refactor. Also confirmed `cmd/unitill-desktop`'s separate `-tags
desktop` build (a different `main` package, unaffected by this refactor
in theory) still compiles.

## Explicitly not attempted
The actual native Android (Kotlin/Gradle) and iOS (Swift/Xcode) shells
(spec 013 Phases 2/3) — no SDK/toolchain in this environment, confirmed
by trying rather than assumed. Installing the Android SDK/NDK or Xcode
was deliberately not done unprompted (multi-GB, environment-modifying);
that's a decision for Farshid, not a default.
