# Code review — enroll Init data race (ut-docs#3021)

**Date:** 2026-09-27 · **Lane:** cloud-24 · **Built by:** Sonnet (Dev subagent) + orchestrator (regression test) · **Reviewed by:** Opus 5.5 (fresh context, independent)

## What shipped

- `internal/enroll/enroll.go`: `Init` computed `storeIDExplicit` / `tokenExplicit`
  and wrote the package globals *before* taking `mu`, while `liveToken`,
  `currentStoreAuth`, `Effective` and `rotate.go` read them under `mu.RLock`
  from other goroutines (a registration loop left running by an earlier
  `Init`). Both are now locals until `Init`'s existing `mu.Lock` block
  assigns them; the later startup-fill check reads the local. One comment
  on `mu` now says what it guards.
- Tests: new `initForTest` helper (context cancelled + background loop
  joined at `t.Cleanup`) replaces every `Init(context.Background(), …,
  &sync.WaitGroup{})` call — 16 sites across `enroll_test.go`,
  `replica_test.go`, `rotate_test.go`. Tests with their own ctx now also
  `t.Cleanup(cancel)` (and one joins its loop), so a `t.Fatal` can't leak a loop.
- New `TestInitWritesExplicitFlagsUnderMu`: a reader goroutine calls
  `Effective` (with a store id in `cur` and a token copy, so both flags are
  really read) while `Init` runs 20 times.
- CI (`ci.yml` `build` job): new step `go test -race -count=1 ./internal/enroll/`;
  the plain Test step excludes the package so it doesn't run twice.
  Broader `-race` coverage stays with ut-docs#3014.

## Verification

- Reproduced first: `go test -race ./internal/enroll/ -count=1` failed 2/4 and
  2/6 runs on `main` (race trace: write `enroll.go:247` in `Init` vs read
  `enroll.go:385` in `liveToken` from a loop started by
  `TestVouchForReplicaRefusals`).
- After: `-race -count=10` ok; 5 × `-race -count=1` ok; reviewer also ran
  `-race -count=15 -cpu 1,2,8` ok.
- Regression test TDD check: compiled against `main`'s `enroll.go` it fails
  **30/30** with `DATA RACE` (both flags: writes at L241/L247); with the fix it
  passes **30/30**.
- Gate: `gofmt -l .` clean, `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` 0 issues, full `go test ./... -count=1` green; CI
  guards run locally — all pass except two local-environment artefacts
  unrelated to this diff (`guard-deadcode-baseline.sh` skips
  `cmd/unitill-desktop` without GTK headers and so flags `internal/logging`
  funcs used there; `shellcheck` not installed — no shell script changed).

## Review findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | Regression test caught the old bug only ~29/40 runs (reader not yet running / `cur.StoreID` empty so `Effective` short-circuited) | Fixed: store id seeded in `cur`, `started` handshake — now 30/30 |
| 2 | minor | `resetState` comment still claimed tests leak `Init` loops | Fixed |
| 3 | minor | `TestInitRegistersDeviceWithTillName` returned with its loop running; three tests leak on a `t.Fatal` before `cancel()` | Fixed (cleanup joins / `t.Cleanup(cancel)`) |
| 4 | minor | `ci.yml` comment said the workflow never uses `-race` | Fixed |
| 5 | nit | `internal/enroll` ran twice in the `build` job | Fixed (excluded from the plain step) |
| 6 | nit | "Guarded by mu" comment repeated three times | Fixed (one comment on `mu`) |

Reviewer confirmed: every access to `cur`, `storeIDExplicit`, `tokenExplicit`,
`explicitConfigured`, `displayStoreID` is under `mu`; `envPinWarned` /
`unsavedToken` are atomics; cleanup LIFO order joins loops before their test
servers close; no SQL, no real names, no UI/i18n surface (UX/help not applicable).

**Verdict:** safe to merge. No deferred items (broader `-race` is ut-docs#3014).
