# Review: till-side handling of a 402 from /v1/stores/sync and /v1/stores/checkin (ut-docs#3624)

- **Branch**: `feat/3624-till-402-plan-required`
- **Reviewer**: Fable (independent of Dev, who built at Opus 5.5 — `complexity:hard`)
- **Author**: Dev subagent (Opus 5.5), orchestrated by the `:41` cloud pipeline lane

## What shipped

This is the `universal-till` half of ut-docs#3624. The `ut-cloud` half
(reviewed separately, `ut-cloud/docs/code-reviews/2026-10-04-cloud-sync-refuses-unpaid-3624.md`)
now answers `/v1/stores/sync` and `/v1/stores/checkin` with `402
plan_required` for an unpaid store. Before this change, the till's generic
`post()`/`statusError` plumbing discarded a non-200 response body except a
401's error code, and `retryAfterHint` only honoured `Retry-After` on
429/503 — so a 402 would have looked like a bare, logged failure, with no
entitlement caching and no honouring of the cloud's 1-hour back-off hint.

Changes, all in `internal/cloudsync/`:
- `cloudsync.go`: `statusError` gained an `Entitlement` field, filled by
  `post()` only for a 402 (parallel to how `Code` is filled only for a
  401). `pushSync` now caches that block via the existing `cacheEntitlement`.
  Tick-error logging moved into `logTickError`: a 402 logs at `Info`
  (outside the Warn-level problems ring), everything else still `Warn`.
- `checkin.go`: `getCheckin` decodes the 402's block the same way and
  returns it with the parsed `Retry-After`; `planCheckin` caches it.
- `schedule.go`: `retryAfterHint` now also honours `http.StatusPaymentRequired`,
  still clamped at `retryAfterClamp` (1h).

## Review

Independent Fable review. Ran `go build`, `go vet`, `gofmt -l`, and the
full `go test ./...` (all packages, including the ~271s `internal/pages`
suite that exercises the operator-check-in wiring). Re-verified the core
TDD claim personally in an isolated `git worktree` (not the shared
checkout): reverting `cloudsync.go`/`checkin.go`/`schedule.go` while
keeping the new `plan_required_test.go` produces real **build failures**
(the test references the new `Entitlement` field and `logTickError`
directly) — about as strong a "this test isn't a false pass" proof as
exists. Restoring makes the full targeted set pass again.

### Findings

1. **correction to Dev's own report, not a bug** — Dev's handback claimed
   the operator-check-in-window mechanism (`OpenOperatorWindow`) had no
   production callers and was therefore dormant. That's false: it's live
   since #3615 (`settings_page.go`, `plugin_api.go`, `setup_page.go`,
   `setup_tse.go` all call it via `requestOperatorCheckin`). Traced the
   actual consequence: an operator clicking "Check for a paid plan" on a
   still-unpaid store now gets a 402 instead of a quiet 200, parking the
   scheduler at ~1h — but `Start()`'s existing kick/backoff-bypass logic is
   generic on any tick error, so a second manual click still forces an
   immediate retry, and no chip/Warn-log regression occurs either way.
   Not a bug; a real but non-blocking coverage gap (no test exercises
   "operator window open + 402" together — both halves are tested
   separately). Noted on the issue for an optional follow-up test.
2. **pre-existing, not introduced here** — `post()` reads the whole
   response body before any truncation; `planRequiredEntitlement`
   truncates only after the read. The 401 code path has always done this.
   `getCheckin`'s new 402 arm does it correctly with a bounded
   `io.LimitReader`. Backlog-worthy on its own, out of this card's scope.
3. **noted, accepted** — `authTracker.observe` needed no change: a 402
   already falls into its existing "anything else, neutral" branch, so it
   never counts toward the auth-lockout streak. Confirmed by test.

### Verified beyond the automated tests

- A genuine driven integration test exists and is real, not mocked out:
  `TestTickOn402CachesBlockBacksOffHourlyThenStaysQuiet` spins up a real
  `httptest.Server`, calls the real `tick()`, covers both arrival paths
  (checkin-GET-402 and sync-POST-402), asserts the actual 1h scheduler
  wait, that `logging.Recent()`'s problems ring stays empty, that
  `AuthChipStatus().Show` is false, that the block is really persisted to
  a real `SettingsRepo`, and — critically — that once the real
  `syncAllowedFn` gate takes over, the *next* tick makes zero further HTTP
  calls at all (ADR-0148 §5's fully-quiet steady state).
- No new file writes (the only persistence is `cacheEntitlement` →
  `SettingsRepo`, SQLite) — `os.MkdirAll`/`paths.Data(...)` don't apply.
- Offline-first unaffected: only the background `internal/cloudsync` tick
  is touched; no checkout/sale-engine code.
- No new user-facing strings (new strings are Go log lines and a cloud
  `message` field the till never displays) — no locale key, no help topic.
- No real client/shop name or literal secret in the new test fixtures.

## Verdict

**Safe to merge.** No blockers. The one real finding (Dev's dormant-window
claim was wrong) changes nothing about correctness — the existing generic
kick/backoff-bypass mechanism already handles it — but is worth the record
for whoever next touches the operator check-in window.
