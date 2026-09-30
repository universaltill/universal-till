# Code review — queued non-blocking plugin events wait for a slot (ut-docs#3171)

- **Date:** 2026-09-30
- **Branch:** `fix/3171-queued-event-admission`
- **Author:** Opus 5.5 (lane:cloud-24). **Reviewer:** Fable, independent subagent, fresh context.
- **Docs:** ut-docs `architecture/wasm-runtime.md` → Sandbox limits → Concurrent calls (branch `docs/3171-queued-event-admission`).

## What shipped

`WasmRuntime.HandleEvent` waited for a call slot (`wasmCallGate`, #3154) inside
the event's own deadline. The per-plugin drainer (serial, non-blocking
`sale.completed` / `stock.adjusted`) shares the ordinary slots with blocking
asks. On Android/iOS a plugin has one ordinary slot, so during an export
(30 s) or import (5 min) every queued event failed after 2 s and was never
retried.

- `internal/plugins/wasm_runtime.go`
  - `HandleEvent` → `handleEvent(ctx, id, ev, 0)`: blocking behaviour unchanged.
  - New `handleQueuedEvent`: the slot wait is bounded by `w.queueWait`
    (`queuedEventWait` = `importTimeout`, the longest ordinary call), and the
    event's own deadline starts only after admission. The run context drops
    the drainer context's cancellation (`context.WithoutCancel`), so an
    admitted event keeps Close's grace window (#380).
  - The module and db are re-read after admission, so a long wait that
    outlives a Sync never runs a closed module.
  - Drainers use `drainCtx`. `Close` cancels it, which ends queued waits at
    once, and replaces it for a later Sync. A drainer skips the events still
    buffered once `drainCtx` is done.
- `internal/plugins/wasm_concurrency_test.go`: four tests on the mobile gate (2 slots).

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The run ctx derived from `drainCtx`, so Close aborted admitted, running handlers (`WithCloseOnContextDone`) instead of letting them finish in the 5 s grace window. | **Fixed:** `context.WithoutCancel`, plus test `TestWasmQueuedEventAdmittedRunSurvivesDrainCancel`. Its looping guest fails with the pre-fix line (`module closed with context canceled`). |
| 2 | should-fix | After Close, the drainer ranged over up to 100 buffered events and logged a "context canceled" error for each one. | **Fixed:** an early return on `drainCtx.Err()`. The doc says buffered events are discarded at shutdown. |
| 3 | nit | The stale-generation check runs only before the wait. | **Accepted, commented:** an event admitted after a resync runs on the current module, which is re-read. It is single delivery and harmless. |
| 4 | nit | No fairness against blocking asks, which can outrun the drainer until `queueWait`. | **Accepted, commented:** bounded and rare. |
| 5 | nit | The blocking-path assertion was only `err == nil`, and the tests never called `w.Close`. | **Fixed:** it asserts `errors.Is(err, context.DeadlineExceeded)`, and cleanup calls `w.Close`. |

The reviewer's checks with no finding:
- context and timer hygiene;
- `drainCtx` is used only under `w.mu`;
- no sale-path event reaches the drainer (`.ask`, `.authorize` and `.refund` are all Blocking);
- the 5 min bound against the 100-event channel and its drop auditing;
- the deadcode guard gains no entry;
- the existing 35 Close/Sync/drainer tests pass under `-race`.

## Verification

- **Red→green, run by the author:**
  - With `handleQueuedEvent` forced back to the old path, the tests fail with the card's error: `has too many calls in flight: context deadline exceeded`, while the slot is held. With the fix they pass.
  - The admitted-run test fails with the pre-fix run context and passes with the fix.
- The new tests pass under `-race` (`-count=8`, run by the reviewer).
- `gofmt`, `go vet`, `golangci-lint` (0 issues), `go build ./...`, and every `ci.yml` guard pass, with these local-only gaps:
  - no shellcheck binary in this container;
  - the deadcode guard can't compile the GTK desktop root here, so it reports `internal/logging` funcs that only that root reaches. Those files are untouched by this diff, and CI analyses all roots.

## Verdict

Safe to merge. No UI, i18n, SQL or migration impact. Behaviour change is documented in ut-docs `architecture/wasm-runtime.md`.
