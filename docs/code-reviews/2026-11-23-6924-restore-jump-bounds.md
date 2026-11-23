# Review: restore number jump stays inside the bound; ArchiveReport ignores out-of-bound Z (ut-docs#6924)

Branch `fix/6924-restore-jump-bounds`. Built by Sonnet (Dev). Tester: TODO. Reviewer: TODO.

## What shipped

- New leaf package `internal/restorenum` with `Max` (10^15) and `ParseFloor` (base-10 int64 in `[0, Max]`). `data.RestoreNumberMax`, `db.restoreNumberMax` and `db.parseRestoreFloor` now delegate to it, and `data.restoreFloor` and the jump read floors through it, so there is one parser. `fmt.Sscan` and the SQL `CAST(value AS INTEGER)` are gone.
- `JumpNumbersAfterRestore` believes only inputs in `[0, restoreInputMax]` (`Max` minus the 10^14 jump cap minus the 1000 rounding): the receipt scan (`maxReceiptSeqUpTo`), the stored counter, the receipt floor, the per-kind Z max (`BETWEEN 0 AND`) and the Z floor. A skipped value logs a warning. `days` and `restoreJumpSize` are capped at `restoreJumpMax` (10^14) and cannot overflow. The jump's output therefore never exceeds `Max`, so every reader accepts it.
- `ArchiveReport` (both variants) ignores `z_number` outside `[0, RestoreNumberMax]` through `CASE` inside the aggregate and a bounded `prev_closed_at` subquery. It reads the floor in Go through `ParseFloor`, so a huge, garbage (`12abc`, `1e18`) or negative floor counts as absent. No error is returned and the close is never blocked.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| - | - | TODO (reviewer) | TODO |

## Verification

- TDD: each new test was run red first (failure lines in the dev note), then green after the fix.
- `gofmt`, `go build ./...`, `go vet`, `golangci-lint run ./internal/...`, `guard-data-access`, `guard-fixed-dates` and `guard-core-neutral` pass; `go test ./internal/data/... ./internal/db/... ./internal/restorenum/...` passes.

Pre-flight: help topic n/a (internal numbering safety, no screen or step a shop owner sees); reference docs n/a (no reference page describes the bound); README n/a; en.json n/a (no user-facing strings, the warning is an operator log line); migrations n/a; ADR n/a (hardens ADR-less #5068/#6906 mechanism, no new decision).

**Verdict:** TODO (reviewer)
