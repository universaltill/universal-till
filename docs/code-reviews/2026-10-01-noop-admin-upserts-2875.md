# Review: admin sync skips no-op upserts (ut-docs#2875)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-24
- **Branch:** `perf/2875-noop-admin-upserts`
- **Author:** Opus 5.5. **Reviewer:** Fable (independent subagent, fresh context)

## What shipped

`ApplyAdmin` upserted every admin row with an unconditional
`INSERT … ON CONFLICT DO UPDATE`. SQLite fires AFTER UPDATE triggers even
when every value is identical. So once any admin write moved the main till's
generation, every replica re-applied all ~37 tables, and every row bumped
`sync_admin_version` and (for catalog tables, migration 047)
`sell_screen_version`. Every open sale screen on every replica then
refreshed its tile grid for an unrelated change: a setting, a PIN, a
payment method.

- `internal/data/sync_admin_repo.go`
  - `resolveUpsertRow` builds a change predicate next to every SET
    fragment: `c IS NOT excluded.c`, `c IS NOT NULL` for redacted columns,
    and `c IS NOT COALESCE(NULLIF(excluded.c, ''), c)` for sticky columns.
    `execUpsertBatch` emits `DO UPDATE SET … WHERE <any differs>`. Bare
    column names in an upsert WHERE are the existing row.
  - `deleteMissing`'s FK-blocked retire-in-place `UPDATE` skips a row that
    is already retired (review finding 1). It also logs "retired in place"
    only when it actually changed a row, so it no longer re-logs on every
    pull.
  - Stale comment on `backfillCodelessSyncedVariants` corrected (finding 3).
- `internal/data/sync_admin_noop_upsert_2875_test.go`: four tests on real
  migrated DBs with a wire round-trip (JSON float64):
  - an identical bundle applied twice moves neither counter;
  - a setting-only change moves `sync_admin_version` but not
    `sell_screen_version`;
  - a real price change still lands and moves both counters, and a local
    redacted `bearer_hash` is still scrubbed;
  - an already-retired FK-blocked row moves neither counter on later
    applies.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `deleteMissing` retire-in-place UPDATE re-ran on every apply, moving both counters, which is the same refresh symptom this card fixes | **Fixed**, with a regression test (failed 30→32 / 9→10 before the fix) |
| 2 | should-fix | `applyPluginSettings` / `applyFiscalRegisterStorage` delete-then-insert still bump `sync_admin_version` (not `sell_screen_version`) on an identical bundle | **Deferred → ut-docs#3312**. It is a different write path, and making it diff-based must preserve the #807 dedupe. |
| 3 | nit | Stale "not sticky across polls" comment on `backfillCodelessSyncedVariants` | **Fixed** |
| 4 | nit | Review record must qualify the AC | Done: see the scope note below |

**AC scope note:** "identical bundle → counters unchanged" now holds for
every generic admin table and for retired rows. Global plugin settings and
fiscal-register storage still move `sync_admin_version`, but not
`sell_screen_version`, so the open sale screen doesn't refresh. That gap is
ut-docs#3312.

## Verified beyond the tests

The reviewer probed the real modernc.org/sqlite driver directly:
- Type affinity: INTEGER/REAL/TEXT/NUMERIC/BLOB columns compared after a
  JSON float64 round-trip. There is no case where a real change is seen as
  unchanged, so no data-loss path. `''` → NULL is still treated as a change.
  No admin column uses `COLLATE`.
- The generated statement for every adminTable prepares, and `sets` and
  `changed` always have the same length. Pure-PK tables still use
  `DO NOTHING`.
- Nothing reads `RowsAffected` from the upsert. Only the 023/042/047
  version triggers exist on these tables.

TDD re-verified: with the `sync_admin_repo.go` change reverted, the Identical
and SettingOnly tests fail (reviewer, in an isolated worktree) and the
AlreadyRetired test fails (author, before the deleteMissing fix). All pass
with the fix applied. `go test ./...`, `go vet`, `golangci-lint`, `gofmt`
and the `ci.yml` guards are clean locally. `guard-shellcheck-version` (no
shellcheck binary) and `guard-deadcode-baseline` (no GTK headers, so
`cmd/unitill-desktop` is skipped) fail only in this container, and fail
the same way on `main`.

No user-facing text or UI changed, so no help-topic, locale or
docs-shots update is needed. `docs/performance.md`'s tile-cache description
is still accurate.

## Verdict

Safe to merge.
