# Review: logging.L() init data race (ut-docs#3410)

- **Branch:** `fix/3410-logging-init-race`
- **Card:** universaltill/ut-docs#3410 (`complexity:easy`, lane:cloud-24)
- **Author:** Dev subagent (Sonnet). **Reviewer:** an independent Opus 5.5 subagent with fresh context in its own worktree. The orchestrator (Opus 5.5) triaged the findings.

## What shipped

- `internal/logging/logging.go`: `L()` used to check `defaultLogger == nil` outside `sync.Once`. That is a broken double-checked init. A goroutine reading the global raced the write inside `once.Do` from another goroutine on first use. Since 3ab5671, `RecoverAndLog` reaches `L()` from every background goroutine, so this was a real production race, not only a test artefact. `L()` now always calls `Init()`. `once.Do` is the synchronisation point, and after the first call it costs one atomic load per log line.
- `internal/logging/init_race_test.go` (new): `TestLConcurrentFirstUseIsRaceFree` resets the package to its uninitialised state. It then releases 16 goroutines that call `L()` together, and asserts they all get the same non-nil logger. It restores `defaultLogger` and the stdlib `log` writer and flags afterwards.
- `internal/logging/recover_test.go`: two tests saved `prev := defaultLogger` *before* calling `L()`. When they ran first, `prev` was nil, and cleanup left the package with a nil logger while `once` had already run. With `go test -shuffle=1`, `-shuffle=4` or `-shuffle=6` this was already failing on `main`. They now call `L()` first.
- `.github/workflows/ci.yml`: `internal/logging` and `internal/netreach` now run in the existing `-race` step next to `internal/enroll`. They are excluded from the plain Test step, so they don't run twice.

## TDD evidence

The new test was run against the parent's `logging.go` with
`go test -race -count=1 ./internal/logging/ -run TestLConcurrentFirstUseIsRaceFree`.
It fails with `WARNING: DATA RACE`: a read at `logging.go:104` (the nil check in `L`) against a write at `logging.go:93` (inside `once.Do`). That is the same pair the card reported from `internal/netreach`. With the fix, the test passes, including at `-count=20`. The Dev subagent confirmed this and the reviewer re-verified it independently.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit, pre-existing | `recover_test.go` saved `prev` before `L()`, which left the logger nil under `-shuffle` | Fixed: `-shuffle=1` and `-shuffle=4` pass now |
| 2 | nit | `internal/netreach` would run in both the plain and the `-race` CI steps | Fixed: excluded from the plain step, as `internal/enroll` already is |

No blockers or majors. The reviewer checked these points, and all are fine:
- Nothing outside the package writes `defaultLogger` or `once`.
- The test swaps of `defaultLogger` still can't be overwritten by `Init`.
- The new test leaks no state.
- `redact.go` is untouched, so ut-cloud's `logredact` mirror is unaffected.
- No other races in `internal/netreach`.
- The two recurring bug shapes (a file write without `MkdirAll`, a cwd-relative path) don't apply.

## Verified

- `go test -race -count=10 ./internal/logging/ ./internal/netreach/` passes. That is the card's "ten runs in a row".
- `go test -race -count=1 ./internal/enroll/` passes.
- `go build ./...`, `go vet`, `gofmt -l` and `golangci-lint run` (logging, netreach) are all clean.
- The CI YAML parses.
- Full `go test ./...` passes.
- Every guard in the `build` job passes, except `guard-shellcheck-version.sh`, which needs a `shellcheck` binary this container doesn't have. No shell script changed.
- No UI surface was touched, so there was no UX or visual check.

## Verdict

Safe to merge.
