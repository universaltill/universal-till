# Code review — per-till (register-scoped) plugin settings + upgrade-dupe fix

**Date:** 2026-07-17
**Branch:** `feat/per-till-plugin-settings`
**Why now:** shared plugin settings (same day) made GLOBAL rows sync
shop-wide — correct for `stripe_secret_key`, wrong for `stripe_reader_id`
(one card reader per till). The schema and manifest already supported
`scope: register`; the read/write paths were global-only.

## Bug found while scoping (pre-existing, worse than the feature gap)

`InsertPluginSettings` upserted on `ON CONFLICT(plugin_id, key, scope,
scope_id)` — but `scope_id` is NULL on these rows and SQLite treats NULLs as
distinct, so the conflict **never fired**: every plugin **upgrade** inserted
a fresh duplicate default row. `GetPluginSetting` then read an indeterminate
row, so a configured value (e.g. the Stripe secret key) could appear to
revert to blank after an upgrade.

## What changed

- `internal/data/plugin_repo.go`
  - `ReconcilePluginSettings` replaces `InsertPluginSettings` (only caller:
    `PersistManifest`): declared-but-missing keys get their default;
    an existing key keeps its **value** and moves to the manifest's scope
    when that changed; duplicate rows collapse (configured value beats
    default, then newest); undeclared keys are removed.
  - `GetPluginSetting` prefers the most specific scope (register > user >
    global) — a per-till value shadows a shop-wide one. Backs the WASM
    `settings_get` host fn, so plugins need no change.
  - `UpsertPluginSettingScoped(…, scope)`; `UpsertPluginSetting` = global
    shorthand.
- `internal/pages/plugin_settings_page.go` — the editor lists global +
  register rows and writes each back into **its own scope**; register rows
  get a muted "(this till only)" marker (`plugins.settings.per_till`,
  en/tr/fa/ar).
- Sync interaction (already shipped today): global rows travel, register
  rows never do — so a register-scoped reader id is safe on every till.

## Known limitation (accepted)

A replica's join **snapshot** copies the primary's register-scoped rows, so
a new till starts with the primary's reader id until the operator sets its
own — natural, since pairing the new till's reader is a setup step anyway.

## Companion change

`ut-plugin-payment-stripe` v1.1.1: manifest declares `stripe_reader_id`
with `scope: register`. On upgrade, `ReconcilePluginSettings` migrates the
existing global row to register keeping its value.

## Tests

- `TestReconcilePluginSettingsUpgrade` (real migrated schema): v1 install →
  configure → simulate the historical dupe → v2 upgrade with a scope change,
  a dropped key, and a new key. Asserts: value preserved across the scope
  move, dupes collapsed to the configured value, undeclared key pruned, new
  default inserted, register shadows global on read, scoped writes hit their
  own row.
- Full suite + `guard-data-access` + `guard-i18n` (608 keys) green.
