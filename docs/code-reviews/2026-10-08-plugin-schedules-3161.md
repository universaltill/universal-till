# Code review: plugin `schedules` ticker (ut-docs#3161, ADR-0121 build 9a)

**Date:** 2026-10-08 · **Lane:** `lane:cloud-41` · **Built by:** Opus 5.5 subagent · **Reviewed by:** Fable subagent (different model), fixes by the orchestrator

## What shipped

The till now runs a plugin's manifest `schedules[]` (ADR-0121 §8):
- **Persistence:** migration `068_plugin_schedules.sql` adds `plugin_schedules`. Its CHECKs mirror `validateABI3Fields` (`every_s >= 30`, `0 <= jitter_s <= every_s`), and it has an FK cascade to `plugins`. The rows are written by `PersistManifest` (step 2c) and rewritten by `Rollback`. The table is classified non-admin in `sync_admin_repo.go`, like `plugin_hooks`.
- **Ticker (`internal/plugins/wasm_schedules.go`):** each `WasmRuntime.Sync` cancels the previous generation's tickers. It then starts one goroutine per schedule for each plugin whose module loaded and subscribed, and whose `schedule` permission is granted. The tickers run on a child of `drainCtx`, are counted in `w.wg` and are stopped by `Close`.
- **Each tick:**
  1. It waits `every_s` plus a uniform jitter from 0 to `jitter_s`.
  2. It waits out a sale burst: a `sale.completed` publish within the last 3 s.
  3. It re-checks the permission live. A revoke stops the ticker.
  4. It skips while the previous run of the same schedule is still going. That guard spans Sync generations.
  5. It fires `Event{Type: event, Scheduled: true, Payload: {"scheduled_at"}}` to that plugin alone, through `handleQueuedEvent`, with no hook row.
- **Never sale-path:** `isSalePathCall` excludes `Event.Scheduled`, and `timeoutForEvent` gives a scheduled event no event-class floor. So even a schedule named `<id>.x.ask` never takes the reserved slot.
- **Docs:**
  - `docs/plugin_guidelines.md` has a new "Scheduled work" section; `README.md` has a paragraph.
  - ut-docs `reference/plugin-manifest.md` and `architecture/wasm-runtime.md` are updated (ut-docs branch `docs/3161-plugin-schedules`).

`MaxSupportedWasmABI` stays 2. Jobs, `poll` and `job_progress` were split to ut-docs#3908.

## Findings (Fable review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A backward wall-clock step after a sale left the sale mark in the future, stalling every ticker for the size of the step | **Fixed.** A window ending more than `saleBurstPause` ahead is treated as stale. The reviewer suggested clamping the wait instead, but that would only re-poll every 3 s until the clock caught up. Test: `TestSchedule_BackwardClockStepDoesNotStallTicker`, seen failing first. |
| 2 | minor | Sync's `CheckPermissionGranted` wrote a `permission_denied` audit row on every Reload for a plugin still awaiting its grant | **Fixed.** Sync uses the unaudited `PluginRepo.CheckPermission`; the per-tick check stays audited. Test: `TestSchedule_SyncPermissionCheckWritesNoDenialAudit`, which saw 2 rows before the fix. |
| 3 | minor | The sale-burst pause is "3 s after each published `sale.completed`", not a guarantee for checkout | **Fixed (comment).** `saleBurstPause` now says the reserved slot is what protects a sale. |
| 4 | minor | A plugin installed before migration 068 with `schedules` has no rows, so it never ticks | **Accepted, documented** in `plugin_guidelines.md` (reinstall or update). The field is four days old (#1700) and no published plugin uses it. |
| 5 | nit | The ticker phase restarts on every Reload, so the first tick comes `every_s`+jitter after it | **Documented** in `plugin_guidelines.md`. Reload only runs on lifecycle changes. |
| 6 | nit | `jitter_s: 0` is allowed, so plugins can tick in lockstep | **Accepted.** The ADR and parser allow it, and it is the plugin's choice. |
| 7 | nit | A tick cancelled by Close/Reload logged at Error | **Fixed.** `context.Canceled` now logs at Info. |
| 8 | nit | If `ListPluginSchedules` fails at Sync, nothing ticks until the next Reload | **Accepted.** It fails closed and is logged, per CLAUDE.md; documented. |

The reviewer also asked for a Sync-level test that a plugin whose module fails to load gets no ticker. It was added as `TestSchedule_NoTickerWhenModuleFailsToLoad`, a regression guard: the behaviour was already correct.

## Verified beyond the automated tests

**Re-verified TDD claims:**
- Restoring the pre-fix `wasm_schedules.go` makes both new fix tests fail for the claimed reasons.
- Removing `!ev.Scheduled` from `isSalePathCall` makes `TestSchedule_EventNeverTakesReservedSlot` fail.
- The Dev subagent mutation-tested nine further properties (skip guard, pause, permission at Sync, generation stop, live re-check, jitter bounds, cross-generation busy key). Each matching test failed.

**Reviewer's checks:**
- Ran the schedule, PersistManifest and Rollback tests under `-race`: no data race.
- Confirmed no lock is held across a blocking call.
- Confirmed in-flight runs are bounded to one per schedule.
- Confirmed the tickers outlive the HTTP request that triggers a Reload, because they hang off `drainCtx`, not the request context.

**Not applicable:** no UI surface and no user-facing strings, so no UX, help or screenshot pass. No file writes and no paths.

## Gate

`gofmt`, `go build`, `go vet`, `golangci-lint` (v2.14 built with go1.27), every guard in `ci.yml`, the schedule tests under `-race`, and the full `go test ./...` — results recorded in the PR.

## Verdict

Safe to merge. Deferred: jobs, `poll` and `job_progress` (ut-docs#3908).
