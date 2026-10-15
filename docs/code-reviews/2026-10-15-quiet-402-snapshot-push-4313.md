# Review — quiet 402 on the snapshot / order-tracking pushes (ut-docs#4313)

Date: 2026-10-15 · Branch: `fix/4313-quiet-402-snapshot-push` · Card: universaltill/ut-docs#4313

## What shipped

`tick` logged a failed catalog-snapshot / order-tracking-snapshot push with
`Warnf`. In the race where a store lapses between the tick's 200 sync and the
push, the cloud's `402 plan_required` landed in the problems ring as a
warning, against ADR-0148 §5 (a plan refusal is quiet).

- `internal/cloudsync/cloudsync.go`: new `logPushError(what, err)` — a 402
  logs at Debug (the next sync's 402 writes the one Info line via
  `logTickError`); anything else warns with the unchanged text. Used at all
  three push sites (snapshot, order tracking, snapshot after directives).
- No change to hashing/backoff: neither push stores its hash on error, and
  `rejectedRollup(402)` is false, so the push retries once the store is paid.
- `internal/cloudsync/plan_required_test.go`:
  `TestTickPushes402AreQuietAndStoreNoHash` (tick as the main till against a
  migrated DB; sync 200, both pushes 402; asserts both pushes were attempted,
  an empty problems ring, and no stored hash) and
  `TestLogPushError402IsNotAProblem` (402 quiet, 503 control logs one).

## Review

Built by Sonnet; reviewed by a fresh-context Opus 5.5 subagent (different
model). Verdict: safe to merge, no blocker/major.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The push's 402 carries the entitlement block but isn't cached, so in the lapse race the rest of that tick can send 1–2 more doomed POSTs; the gate closes on the next tick's sync 402. | Out of scope (changes gate state from a logging helper; interacts with `markGatedRefusal` / renewal watch) — filed as ut-docs#4385. |
| 2 | minor | Order-tracking post count was only logged, not asserted. | Fixed: the test now fails if either push was never attempted. |
| 3 | minor | The after-directives site has no tick-driven test (needs an applied catalog directive). | Accepted: identical call through the helper; the helper unit test covers it. |
| 4 | nit | The empty-ring assertion covers the whole tick, so an unrelated future warning would trip it. | Accepted: the test mocks `/v1/stores/issue-reports` and uses a migrated DB so today's tick is clean; a new warning showing up here is worth a look anyway. |
| 5 | nit | `planRequired` matches any 402. | Accepted: consistent with `logTickError`; ut-cloud only sends `plan_required` 402s on these two endpoints. |

Also checked: ut-cloud's other gated endpoints — sales aggregates and report
archives use `402 subscription_inactive` and are already quiet with a latch,
so nothing else in the tick needed this. No test-global state leaks (ring
reset + cleanup, fakeClock/resetTillAuth restore, per-test DB); shuffled
package run and a 20× repeat of the tick test passed. Test-file raw SQL is
excluded by `guard-data-access.sh` (same pattern as `cloudsync_test.go`).

## TDD re-verified

Reviewer reverted the three call sites to the original `Warnf` lines:
`TestTickPushes402AreQuietAndStoreNoHash` FAILED with both 402 warnings in
the ring; restored → both tests pass.

## Gate

`gofmt -l .` clean, `go build ./...`, `golangci-lint run ./...`, full
`go test ./...`, guards (data-access, core-neutral, deadcode-baseline, i18n,
kiosk-engine). `go test -race ./internal/cloudsync/` passes (needs
`-timeout 40m`; the package takes ~11 min under race). No UI, locale,
migration or help change; no doc lists per-push log levels.

## Deferred

- Cache the entitlement block from a push's 402 so the ADR-0148 gate closes
  within the same tick (finding 1) — ut-docs#4385.
