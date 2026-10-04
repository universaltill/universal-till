# Review: fleetlink — write link state before publishing the mode (ut-docs#3590)

**Date:** 2026-10-03 · **Card:** ut-docs#3590 (bug, p2, complexity:easy) ·
**Branch:** `fix/3590-fleetlink-polling-order` · **Author:** Sonnet (dev
subagent) · **Reviewer:** Opus 5.5, fresh context (different model)

## What shipped

`internal/fleetlink/client.go` published the new `LinkMode` before writing
the state that goes with it, at three sites:

- `attempt`, `case level < 1` — `setMode(ModePolling)` before `clearOutage()`;
- `Run`, no target — `setMode(ModeIdle)` before `clearOutage()`;
- `attempt`, `case err != nil` — `setMode(ModeConnecting)` before
  `markAttemptFailed()`.

A `Status()` reader between the two could see the new mode beside the old
outage (`Mode: Polling` with stale `LostAt`/`FailedAt`), which made
`TestClient_StatusRecordsAFailedAttemptAndForgetsItOnAnAnswer` flaky on
`main` (reproduced: 1 failure in ~4,500–6,000 runs with `-race -cpu 1,2,4`).
All three now write the state under `c.smu` first and publish the mode
after, as `markLinkUp` already did.

Production impact is an invariant fix, not a visible one: no non-test code
reads `ClientStatus.Mode` (the link-status chip in
`internal/pages/link_status.go` uses `Linked`/`LostAt`/`LostSeen`/`FailedAt`
only), so the chip never showed this state. The fix removes the CI flake
and keeps `Status()` self-consistent for any future reader.

## Tests (TDD)

Two new white-box tests in `status_test.go` make the race deterministic by
holding `c.smu` from the test (`holdStateLockWhileWatching`): a client that
publishes the mode first reaches it while the lock is held; one that writes
the state first blocks on `smu` and leaves the mode unchanged.

- `TestClient_PollingIsPublishedOnlyAfterTheOutageIsCleared`
- `TestClient_IdleIsPublishedOnlyAfterTheOutageIsCleared` (drives `Run`
  through the Revoked loop, the one loop that never takes `smu`, so the
  target recheck still happens during the hold)

Both also assert, after the hold, that the mode is reached and the outage
fields are clear, so neither passes vacuously.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The card's rationale (chip briefly says "not reachable") is overstated: nothing in production reads `Mode`. | Accepted; this record and the commit message describe it as an invariant + flake fix. |
| 2 | nit | The Connecting swap has no regression test (reverting only that swap keeps both tests green). Pre-fix it showed Connecting with `FailedAt` still zero — a missing failure, not a stale outage. | Accepted: low impact; driving that branch deterministically under the lock needs a non-Revoked loop that itself blocks on `smu`. |
| 3 | nit | The Polling test could false-*pass* (never false-fail) if the probe's error path lands in `markAttemptFailed` in the few instructions before the test takes `smu`. | Accepted: reverting the fix failed both tests 3/3 and 6/6. |

Memory ordering checked: `statusAt` loads `mode` while holding `smu`; the
writer now does Lock → write → Unlock → `mode.Store`, so a reader that sees
the new mode also sees the cleared fields. Remaining sites (`markLinkUp`,
`markAnswered`+Connecting, `markRevoked`, `runLink`'s defer) already wrote
state first or touch no outage fields.

## Verified

- TDD re-verified by the reviewer in an isolated worktree: fix reverted →
  both new tests FAIL (`mode 2 was published while the outage was still
  recorded`, `mode 0 …`); restored → 20/20 PASS.
- Original flaky test: `-count=1500 -race -cpu 1,2,4` — pre-fix 1/4500
  failures, post-fix 0.
- `gofmt -l`, `go vet ./...`, `go build ./...`, `go test ./... -race`
  (all green except `internal/db`, `internal/pages`, `internal/plugins`,
  which hit the 10-minute timeout under `-race` on a 4-core runner — CI
  runs them without `-race`; re-run that way: all `ok`),
  `golangci-lint run ./internal/fleetlink/...` (0 issues),
  `guard-data-access`, `guard-netaccess`, `guard-kiosk-engine`,
  `guard-i18n`, `guard-core-neutral`, `guard-pipefail-grep-q`,
  `guard-page-http-error`.
- No UI surface, strings, SQL, migrations or file I/O touched; no help
  topic or README change needed.

**Verdict:** safe to merge.
