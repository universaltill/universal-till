# Code review — shared plugin settings across tills (LAN sync)

**Date:** 2026-07-17
**Branch:** `feat/shared-plugin-settings-sync`
**Ask (Farshid):** change a plugin's shop-wide setting once (e.g. the Stripe
secret key) and have it pushed to every joined till.

## What changed

- `internal/data/sync_admin_repo.go`
  - `plugin_settings` added to `adminTables` (pk = surrogate `id`).
  - `DumpAdmin` ships **only `scope='global'` rows** — register/user-scoped
    rows are per-till by definition and never travel.
  - `ApplyAdmin` skips the generic prune for `plugin_settings` (it would
    wipe the replica's per-till rows, absent from the bundle) and routes
    the table to `applyPluginSettings`.
  - `applyPluginSettings`: per bundle-plugin **delete global rows, then
    insert** the primary's. Rows for plugins not installed on this till are
    skipped (`plugin_settings` FKs `plugins`; replicas install plugins
    themselves). Replica-only plugins keep their local global settings.
- `internal/plugins/manifest.go` — after a successful install,
  `sync.pull_version` is cleared (best-effort) so the next 30s pull
  re-applies the bundle and a freshly installed plugin receives its shared
  settings immediately, instead of waiting for the primary's fingerprint
  to move.

## Why delete-then-insert, not the generic upsert

The `UNIQUE (plugin_id, key, scope, scope_id)` index has `scope_id = NULL`
on global rows and SQLite treats NULLs as distinct — `ON CONFLICT` on that
index never fires (the same reason `UpsertPluginSetting` is
update-then-insert). Delete-then-insert per plugin also propagates key
deletion within a plugin for free.

## Semantics (matches the rest of admin sync: primary wins)

| Row | Behaviour |
|---|---|
| Global setting, plugin on both tills | Replica replaced by primary's value every pull |
| Global setting, plugin only on primary | Skipped until the replica installs it (then ≤1 pull) |
| Global setting, plugin only on replica | Untouched |
| Register/user-scoped rows (either side) | Never travel, never touched |

Known edge (accepted): if the primary deletes a plugin's **last** global
key, the replica keeps its stale copy — the bundle no longer mentions that
plugin, and "primary deleted all keys" is indistinguishable from "primary
doesn't have the plugin". No app path deletes individual plugin settings
today (uninstall cascades the whole plugin), so this is theoretical.

Version skew is safe both ways: an old replica ignores the unknown bundle
table; a new replica pulling from an old primary leaves `plugin_settings`
untouched (`ok=false`).

## Follow-up

- `stripe_reader_id` is written at `global` scope today, so it now syncs
  shop-wide — wrong for a shop with one reader per till. The schema and
  manifest already support `scope: register`; the read/write paths
  (`GetPluginSetting`/`UpsertPluginSetting`/settings page) are global-only.
  Make them scope-aware and mark the reader id `register` in the Stripe
  plugin manifest.

## Tests

- `TestAdminSyncSharedPluginSettings` (two migrated DBs = the 2-till
  simulation): shared key replaces the stale copy exactly once (idempotent
  double-apply), register-scoped and replica-only rows survive,
  not-installed plugin rows skipped without an FK abort, primary's
  register row doesn't leak, key deletion propagates, fingerprint moves.
- `TestPersistManifest` now also asserts the pull cursor is cleared on
  install.
- Full suite + `guard-data-access.sh` green.
