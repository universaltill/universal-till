# Review: admin apply refuses once the till is no longer a replica (ut-docs#5896)

**Date:** 2026-11-08 · **Lane:** `lane:cloud-41` · **Author model:** Sonnet (Dev) ·
**Reviewer model:** Opus 5.5 (independent, fresh context) · **Branch:** `fix/5896-applyadmin-replica-check`

## What shipped

- `internal/data/sync_admin_repo.go`: new exported `data.ErrNotReplica`. The
  first statement inside `applyAdmin`'s transaction reads `sync.primary_url`
  through the existing `AgentSettingRaw(ctx, tx, …)`. If the trimmed value is
  blank, it returns `ErrNotReplica` and writes nothing. The transaction is
  `BEGIN IMMEDIATE` (`_txlock=immediate`, `internal/db/db.go`), so the check
  and `ClearReplicaIdentity` (promote) are fully serialised. Promote can't
  land between the check and the writes. There is no flag or unguarded inner
  function, so production can't skip the check.
- `internal/pages/sync_admin.go` `syncPullTick`: on
  `errors.Is(err, data.ErrNotReplica)` it logs at INFO ("skipped, this till
  was promoted to main till during the pull") and returns from the whole
  tick. That skips pull_version/last_pull_at, the `admin_pulled` audit,
  refresh, main-contact bookkeeping and the plugin and stock pulls. Other
  apply errors keep the existing ERROR path.
- Doc comments on `ApplyAdminWithResult` and `ClearReplicaIdentity` (in
  `settings_repo.go`).
- Tests:
  - `internal/data/sync_admin_replica_guard_5896_test.go`: refuses with no
    URL (AC1: `lan_discovery.till_id`, `till.name` and table contents stay
    unchanged), refuses with a blank URL, and applies with the URL set (AC2).
  - `TestSyncPullTick_PromotedMidPullIsSkipped_5896` (AC3): a hook in the
    fake main till's `/api/sync/admin` runs the real `ClearReplicaIdentity`
    mid-pull. The test asserts that the new discovery id and name are kept,
    no `sync.*` key is re-created, there is no audit row and no refresh, and
    the INFO skip line (not the ERROR line) is logged.
  - The new `openReplicaDB` / `markReplica` helpers replace
    `replica := openMigratedDB(` across about 21 data test files (every apply
    target there is a replica). There are three hand edits: the duplicate
    primary_url INSERT, the reverse-direction apply, and the batch
    INSERT-count test.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium (pre-existing) | `rememberOwnTillRole` runs before the apply with no re-check, so a promote mid-request can leave `sync.till_role=satellite` on the new main till. That value feeds the display-mode gate and the closes mirror. | Deferred → ut-docs#5903 |
| 2 | Low (pre-existing) | An `Unchanged` bundle never reaches the guard, so contact bookkeeping and the plugin and stock pulls can still run against the old main till after a promote. | Deferred → ut-docs#5903 |
| 3 | Low (test gap) | The pages test still passed with the new `ErrNotReplica` branch stubbed out, because the generic error branch also returns early. | **Fixed**: the test captures logs and requires the INFO skip line, and fails on `apply failed`. Re-verified red (stubbed branch → FAIL with `[ERROR] sync pull: apply failed: … not a replica`), then green. |

The reviewer also confirmed:
- The only caller of `ApplyAdminWithResult` in the repo (cmd/, e2e/ and
  scripts/ included) is `syncPullTick`. Join, pairing, backup restore and
  promote don't apply bundles, so no production path is newly refused.
- The test sed didn't change any test's meaning.
- There are no file writes, cwd-relative paths, user-facing strings or fixed
  dates.

## Verification

- **TDD re-verified by the reviewer** in a separate worktree. With the
  production check stubbed out, both refuse tests fail with
  `err = <nil>, want ErrNotReplica`, and the pages test fails with
  `refresh called 1 times, want 0`. With the check restored, all pass.
- **Tester** (separate worktree): PASS. With the fix reverted and the
  assertions made non-fatal, every assertion fired: discovery id, name and
  an item row were written back, four `sync.*` keys were re-created, and an
  audit row, a refresh and a stock pull all happened.
  `go test -race ./internal/data/ -run 'ApplyAdmin|Replica|SyncAdmin'` is
  green.
- **Gate:**
  - `gofmt -l .` is clean, and `go build ./...` and `go vet` pass.
  - `go test ./internal/data/` and `./internal/pages/` pass in full.
  - `golangci-lint` reports 0 issues.
  - The CI build-job guards pass locally, except `guard-deadcode-baseline`
    (the local deadcode binary was built with Go 1.26 and the module needs
    1.27) and `guard-shellcheck-version` (no shellcheck installed). Both are
    environmental, and CI runs them.
- **No driven run:** this is a timing race with no screen. The AC3 test
  drives the real tick, mux and migrated databases through the race
  instead.

Pre-flight: help topic n/a (no user-visible behaviour); reference docs n/a (no reference page describes admin-pull internals); README n/a; en.json n/a; migration n/a; follow-up work → ut-docs#5903.

**Verdict:** approve.
