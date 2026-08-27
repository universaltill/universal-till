# 2026-08-27 — Async print-failure test flake under `-race` (ut-docs#1018)

## What shipped

`internal/pages/print_api_test.go`'s `TestAsyncPrintFailureIsRecordedWhenPrintCtxExpired`
raised the test-local `printAsyncTimeout` from `50 * time.Millisecond` to
`400 * time.Millisecond`. Test-only change — no production code touched.

## Root cause

`printReceiptAsync`/`printKitchenAsync` each create **one** outer context
with `printAsyncTimeout` budget and spend it on a real DB-backed settings
read (`printerConfig` / `kitchenPrintingEnabled`, 7 `Settings.Get` calls,
`kitchenPrintingEnabled` also calls `ListKitchenStations`) **before**
attempting the print itself. Both helpers treat a failed/expired read as
"printing is off" and take the early "no attempt" return — silently,
without reaching the failure-recording code (`recordPrintFailureCtx()`,
a separate fresh 5s context), which is what the test is actually trying
to exercise.

At 50ms, `-race`'s scheduling/instrumentation overhead could let the
settings read alone burn the whole budget before the test's `hang`/
`hangKitchen` stubs ever ran, so the simulated failure was silently
skipped rather than recorded.

**Correction to the original card's hypothesis** (caught in independent
review): the card guessed the kitchen path loses because of its extra
`ListKitchenStations` call. The independent reviewer's repro under
sustained CPU load showed it's symmetric — 6/10 failures were
`receipt=true kitchen=false`, but 4/10 were `receipt=false kitchen=true`.
Whichever of the two goroutines loses the scheduler that run is the one
that reports empty; both paths share the identical defect (a shared
budget for the pre-flight read and the simulated hang), not just kitchen.

## Verification

- **Reproduced pre-fix**, independently, twice: this cycle (3/3 fail
  under `-race -count=5` on an idle machine, matching the exact assertion
  text and the `data.settings.get: context deadline exceeded` log lines
  from the card) and by the independent reviewer under sustained CPU load
  (10/10 fail, `-race -count=10`, both failure-message shapes seen).
- **Fix verified independently**, twice: this cycle (15/15 pass,
  `-race -count=15`, idle machine) and by the reviewer under load
  (15/15 pass at the same load that produced 10/10 pre-fix failures;
  10/10 pass at roughly double that load — ~8x measured margin).
- **Confirmed the fix doesn't weaken the test**: the reviewer temporarily
  reintroduced the original ut-docs#517a bug in the *production* code
  (bypassing `recordPrintFailureCtx()`'s fresh context) against the
  400ms test and confirmed it still fails 3/3 — the regression-detection
  strength is unchanged, only the false-negative window closed.
- `go build ./...` — clean.
- `gofmt -l .` — no output.
- `go vet ./internal/pages/...` — clean.
- `go test ./internal/pages/...` (full package, unraced) — pass (~80s).
- `scripts/ci/guard-data-access.sh` — pass (no SQL outside internal/data,
  unaffected by this diff regardless).
- `scripts/ci/guard-i18n.sh` — pass (1292 keys resolve; unaffected).
- **Not run**: a full `-race` pass of the whole `internal/pages` package.
  Attempted once; it exceeded a 10-minute background timeout, stuck in an
  unrelated test (`TestBuildKitchenTicket_IncludesTable`, a `db.Open`
  migration `Tx.Commit` under heavy parallel goroutine/DB-lock
  contention). Re-ran that specific test alone under `-race -count=3`:
  3/3 pass in under 30s — confirms the hang is a pre-existing
  environment/contention issue when *hundreds* of `-race`-instrumented
  tests run concurrently in this sandbox, not something this diff
  introduces (this diff touches one unrelated test file). Matches
  already-tracked flakes in this area (ut-docs#1151 SQLITE_BUSY,
  ut-docs#878 fsync hang, and ut-docs#1034/#1119's `test-race-pages`
  timeout bump from this morning). `-race` is not run in CI at all
  (`.github/workflows/ci.yml`), so this is a developer-tooling
  observation, not a merge blocker.

## Independent review (Opus subagent, worktree-isolated)

Verdict: **safe to merge, with notes** — see above for the root-cause
correction. Two follow-ups raised, deliberately not folded into this
fix:

1. **Magic-number concern, accepted**: 400ms is empirical, not derived,
   but sits well inside the repo's existing "generous margin over
   measured runtime" convention (ut-docs#643, ut-docs#1034/#1119) and the
   reviewer measured ~8x real headroom under load, not a marginal nudge.
   A strictly race-free fix (a test seam letting the stub cancel its own
   context, decoupling the pre-flight-read budget from the simulated-hang
   window) is a fair follow-up if this ever flakes again, not required
   now — the card's own text explicitly permits loosening the timing
   assumption.
2. **New Backlog card filed**, not fixed here (out of scope for a
   test-only flake fix): `printerConfig`/`kitchenPrintingEnabled` treat
   *any* settings-read failure (SQLite busy, disk error, an already-
   expired caller context) the same as "printing switched off," so a
   transient DB fault during the pre-flight read silently skips both the
   print attempt *and* the ut-docs#517a failure-recording path — no
   audit row, no `/orders` warning. This is the same silent-loss shape
   #517a was written to close, just triggered by a DB fault instead of a
   printer fault. Low likelihood in production (a fresh 15s budget, first
   thing the goroutine does) but real; tracked as a separate card so it
   isn't lost. See universaltill/ut-docs#1018's thread for the reviewer's
   full writeup.

## Safe-to-merge verdict

Yes. Test-only change, minimal diff, TDD-verified both directions by two
independent runs (mine and the reviewer's), no production behavior
change, all applicable gates green. The one gate not run to completion
(whole-package `-race`) was independently shown unrelated to this diff.
