# Review: wasm Close test races its own ctx deadline (ut-docs#3299)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-54
- **Author:** Sonnet (Dev subagent). **Reviewer:** Opus 5.5 (independent, fresh context, separate worktree)
- **Card:** universaltill/ut-docs#3299 (found by lane:cloud-24 during #3139's full `go test ./...` on a loaded machine)

## What shipped

Test-only. `TestWasmRuntimeClose_TimesOutLoudlyOnWedgedDrainer`
(`internal/plugins/wasm_sync_test.go`) built its 100 ms ctx *before*
`start := time.Now()` and then required `time.Since(start) >= 100ms`. The
ctx deadline is `ctxCreated + 100ms`, which can be earlier than
`start + 100ms`, so under load a correct `Close` looked early ("returned
after 98.4ms"). The test now reads `ctx.Deadline()` and fails only if
`time.Now()` is still before it once `Close` returns. `start` stays for the
log-recency check. Production `WasmRuntime.Close` is unchanged.

## TDD evidence (Dev, re-verified independently by the reviewer)

- Old test + a 10 ms `time.Sleep` between `WithTimeout` and `start`: fails
  every run (`Close returned after 91.0ms, before its ctx deadline`).
- Fixed test + the same sleep: passes (`-count=5`).
- Fixed test against a `Close` that doesn't wait (two mutants: `default:`
  in place of `case <-ctx.Done():`, and an early `return`): fails with
  `Close returned 99.9ms before its ctx deadline — it never actually waited`.
- `gofmt`, `go vet ./internal/plugins`, `go build ./...` clean;
  `go test ./internal/plugins -count=1` ok;
  `-run TestWasmRuntimeClose -race -count=10/20` ok.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Comment wording "drift apart by more than the timeout's slack" is a little vague (the old check had no slack). | Accepted as written. |
| 2 | nit (outside diff) | `internal/db/open_test.go:130` has the same race class: B takes `start` after `<-held` while A's 200 ms hold already runs; a >100 ms scheduling stall would fail it. 100 ms of slack, never seen flaking. | Accepted; same fix idea applies if it ever flakes. |

Every other lower-bound `elapsed` assertion in `*_test.go` was checked
(`shutdown_drain_test.go:189`, `app_test.go:75`,
`wasm_concurrency_test.go:257`, `transport_test.go:246`,
`reload_busy_production_test.go:241`): each takes its reference before the
timer starts, so it can only over-measure.

## Verdict

Safe to merge. No user-visible change, so no help/manual or locale update.
