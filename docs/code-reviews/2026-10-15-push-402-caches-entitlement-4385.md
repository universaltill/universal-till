# Review — a push's 402 caches the entitlement block and ends the tick's pushes (ut-docs#4385)

Date: 2026-10-15 · Branch: `fix/4385-push-402-caches-entitlement` · Card: universaltill/ut-docs#4385

## What shipped

Follow-up to ut-docs#4313. When a store lapses between a tick's 200
`/v1/stores/sync` and its snapshot pushes, the catalog / order-tracking push
gets `402 plan_required` carrying the ADR-0060 block, but the till did not
cache it: the order-tracking push and the after-directives snapshot push each
sent one more doomed POST, and the ADR-0148 gate only closed on the next
tick's sync 402.

- `internal/cloudsync/cloudsync.go`: new `pushRefused(ctx, settings, err,
  windowSeen)` — on a 402 it calls `cachePlanRequired`; when the 402 carried
  a block it also closes the operator window and disarms the start-up
  check-in (parity with the sync path, ADR-0148 amendment §4) and logs the
  same Info line `logTickError` writes for a sync 402 (Info is outside the
  problems ring). `tick` sets `refusedMidTick` from it and skips the
  order-tracking push and the after-directives snapshot push for the rest of
  the tick. `tick` still returns `contacted = true` (the 200 sync was the
  contact). Comments on `cachePlanRequired` / `logPushError` updated.
- Unchanged on purpose: `pushSalesAggregates` / `pushReportArchives` (own
  throttle and 402 handling; the archive refusal sets the "kept on this till"
  chip key, worth surfacing promptly), directive result posts, hashing (no
  hash stored on error, so pushes retry once paid).
- `internal/cloudsync/plan_required_test.go`:
  `TestTickPushes402AreQuietAndStoreNoHash` now asserts no order-tracking POST
  follows the snapshot 402 and `entitlement.plan` = the block's plan;
  new `TestTickOrderTrackingPush402CachesBlock` (snapshot 200, order-tracking
  402, the real sync gate seeded paid): block cached, quiet, no hash, and the
  next tick is a quiet gated tick with zero requests.

## Review

Built by Opus 5.5 inline; reviewed by a fresh-context Fable subagent
(different model) in a separate worktree. Verdict: safe to merge, no
blocker/major.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The mid-tick lapse closed the gate before the next sync 402, so the ADR-0148 §5 Info line was never logged — only Debug. | Fixed: `pushRefused` logs the Info line when a block was cached. |
| 2 | minor | Push 402 did not close the operator window / disarm the start-up check-in like the sync 402 does (one extra quiet sync POST possible). | Fixed: same two calls in `pushRefused` on a cached block. |
| 3 | minor | Tests checked `entitlement.plan` only; `main_test.go` pins the gate open, so a gate that stays open wouldn't be caught. | Fixed: the new test uses `useRealSyncGate`, seeds a paid block and asserts a quiet next tick with no requests. |
| 4 | nit | Comment claimed the gate is closed even for a block-less 402. | Fixed wording. |
| 5 | nit | `cachePlanRequired` doc said the tick always fails with the 402. | Fixed: sync path only. |

## Verified

- TDD: with `cloudsync.go` at the pre-fix commit, both tests fail —
  `1 order-tracking POSTs after the snapshot push's 402, want 0` and
  `entitlement.plan = "" … want the block's "local"` (reviewer); after the
  review fixes, the strengthened test fails pre-fix with
  `entitlement.plan = "pro" …, want the block's "local"` (orchestrator).
  All pass with the fix.
- `gofmt`, `go build ./...`, `go vet`, `golangci-lint run ./...` (0 issues),
  `go test ./...`, and the `build` job's guards. Locally only
  `guard-deadcode-baseline.sh` (the local `deadcode` binary was built with
  go1.26, the repo needs go1.27; it runs in the GTK desktop job in CI) and
  `guard-shellcheck-version.sh` (no shellcheck in this container) could not
  run — neither touches this diff.
- Backend-only: no UI, i18n, help topic or migration change.

## Deferred

None.
