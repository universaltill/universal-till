# Review: plugin settings / fiscal storage apply without churn (ut-docs#3312)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-54
- **Branch:** `fix/3312-plugin-settings-noop-apply`
- **Author:** Opus 5.5. **Reviewer:** Fable (independent subagent, fresh context)

## What shipped

Follow-up to #2875 (its review finding 2). `ApplyAdmin` sent
`plugin_settings` and the fiscal-register `plugin_storage` rows through a
scoped `DELETE` and a re-insert. Each apply fired migration 023's DELETE
and INSERT triggers for every row, so an identical bundle moved
`sync_admin_version` (20 → 28 in the new test) and the tile cache missed
once on every pull for any shop with a configured payment or fiscal plugin.

- `internal/data/sync_admin_repo.go`
  - `applyPluginSettings`: for each plugin the bundle mentions, reads the
    local global `(id, key)` rows and deletes only those whose pair the
    (deduped, global) bundle doesn't carry. A missing key propagates the
    deletion. A same-key row with another id (#807) is cleared, so the id
    upsert can't hit `ux_plugin_settings_global`. Everything else goes
    through #2875's conditional upsert and fires no trigger when unchanged.
  - `applyFiscalRegisterStorage`: deletes only the `tax-de` +
    `fiscal_register:` keys missing from the bundle, then upserts as before.
    Scope is unchanged (plugin id + prefix on the SELECT, plugin id + exact
    key on the DELETE).
  - Comments on both functions and on their `adminTables` entries updated.
- `internal/data/sync_admin_plugin_noop_3312_test.go`: four tests on real
  migrated DBs with a JSON wire round-trip:
  - identical bundle (2 global settings + 2 fiscal rows) applied twice →
    `sync_admin_version` unchanged;
  - one value change + one key deletion in each table → exactly +4, and
    each change lands;
  - a local global row with the same key but another id is replaced by the
    primary's row without aborting; a register-scoped row is untouched;
  - two rows swapping keys on the primary apply cleanly.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `adminTables` comments for `plugin_settings` / `plugin_storage` still said delete-then-insert | **Fixed** |
| 2 | nit | A replica's own `StorageSet` row is a BLOB, the bundle carries TEXT, so that row is rewritten once (same bytes) and is then stable | **Accepted**, documented in a comment so nobody adds a CAST. `StorageGet` reads both. |
| 3 | suggestion | Assert an exact counter delta; add a swapped-keys case | **Done** (+4 assertion, swapped-keys test) |
| 4 | observation | Non-global bundle rows would empty that plugin's keep-set and clear its global rows | Same as the old blanket delete, and unreachable: `DumpAdmin` only sends global rows. No change. |

The reviewer also checked: every query's rows are drained and closed before
the next `ExecContext` on the same tx; the keep-set logic for same-id/other-key,
plugin not installed locally, and replica-only plugins (never scanned).

## Verified

- TDD: with only `sync_admin_repo.go` reverted to `main`,
  `TestApplyAdmin_IdenticalPluginBundleMovesNoVersion` fails with
  `sync_admin_version 20 -> 28 on an identical plugin bundle, want unchanged`
  (re-verified independently by the reviewer in its own worktree); restored,
  all pass.
- Existing #807 dedupe and #1670 fiscal-storage scope tests still pass.
- Full gate: `gofmt`, `go build`, `go vet`, `go test -race ./...`, every
  guard in `ci.yml`'s build job.

Backend only: no UI surface, no help topic, no locale keys, no migration.

## Verdict

Safe to merge.
