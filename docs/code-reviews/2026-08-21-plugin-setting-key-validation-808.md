# Code review: plugin manifest duplicate-setting-key validation (ut-docs#808)

**Branch:** `fix/808-plugin-setting-key-validation`
**Scope:** item 1 of ut-docs#808 only (items 2–3 explicitly deferred, see below)

## What shipped

`internal/plugins/manifest.go`'s `PersistManifest` had no in-manifest
duplicate-key validation for `type:"settings"` entries, unlike its siblings
`validatePaymentEntryKeys` and `validatePageEntryKeys` in the same file.

- Two settings entries sharing a key at the default/explicit `"global"`
  scope both attempted an INSERT during `ReconcilePluginSettings`
  (`internal/data/plugin_repo.go`) — on a fresh install there's no existing
  DB row to reconcile against, so both hit the insert branch, and the
  second tripped `ux_plugin_settings_global` (migration 053's partial
  UNIQUE INDEX on `(plugin_id, key) WHERE scope='global'`), surfacing as a
  raw `UNIQUE constraint failed: plugin_settings.plugin_id,
  plugin_settings.key` error instead of a clean, actionable one.
- Two settings entries sharing a key at a non-global scope (`register`/
  `user`) silently double-inserted with **no error at all** — the
  table-level `UNIQUE(plugin_id, key, scope, scope_id)` never fires for
  them because `scope_id` is always `NULL` and SQLite treats NULLs as
  distinct.

**Fix:** new `validateSettingKeys(settings []ManifestSetting) error` in
`internal/plugins/manifest.go`, called as step `0e` in `PersistManifest`
(after `validateExclusiveHookOwnership`, before any DB write). Rejects a
manifest declaring two or more `Settings` entries sharing the same
`(key, effective scope)` pair — an empty `Scope` defaults to `"global"`,
mirroring `PersistManifest`'s own defaulting a few lines below the new call
site. Keyed on `(key, scope)` rather than key alone, since a manifest
declaring the same key at two genuinely different scopes is a legitimate,
pre-existing shape (`ReconcilePluginSettings` already treats moving a key
between scopes as a normal upgrade).

Only call site needed: `Rollback` (`rollback.go`) never touches
`plugin_settings`/`ManifestSetting` at all, unlike the payment/page
validators which also guard a Rollback path.

## Independent review

Spawned a fresh-context Sonnet subagent (complexity:easy → same-tier
review per the `scrum-master` model-routing table), isolated in its own
git worktree, with explicit instructions to build/vet/test, re-derive the
bug from scratch, and independently re-run the TDD revert/restore rather
than trust the implementer's claim.

**Verdict: PASS.**

- Scope discipline confirmed: diff touches exactly
  `internal/plugins/manifest.go` (+51) and the new
  `internal/plugins/setting_key_validation_test.go` (+160), nothing else.
- Scope-defaulting logic verified byte-for-byte consistent with
  `PersistManifest`'s own defaulting.
- Map key is a Go struct (`scopedKey{key, scope string}`), not string
  concatenation — no delimiter-injection possibility.
- Fires before any DB write in `PersistManifest` (step `0e`, ahead of step
  `1`'s `EnsureCatalogEntry`) — a rejected install leaves nothing behind.
- Nil/empty `m.Settings` handled correctly (loop doesn't execute, returns
  `nil`).
- Error wording checked consistent in tone with the sibling validators.
- All 5 new tests independently judged non-tautological (each would
  genuinely fail if the fix were absent/broken).
- **TDD re-verification performed independently**, not taken on trust: the
  reviewer temporarily disabled the new call site, re-ran the 5 tests, and
  observed the exact two failure modes described above reproduce
  byte-for-byte (raw `UNIQUE constraint failed` for the global case;
  silent accept with zero error for the register-scope case) — then
  restored the fix and confirmed all 5 pass again, confirming `git diff`
  showed the file byte-identical to the pre-revert state afterward.
- `go build ./...`, `go vet ./...`, full `internal/plugins` package test
  run, and `guard-data-access.sh` all independently re-run and green.

**One non-blocking nit, judged deliberate/out-of-scope, not fixed here:**
unlike `validatePaymentEntryKeys`/`validatePageEntryKeys`, the new
validator does no key-format hygiene (empty key, surrounding whitespace,
reserved `:` separator) — it only catches literal duplicates. This was the
explicit BA/Architect scope for item 1 (duplicate-key rejection only); a
malformed-but-unique settings key was never in scope for this card.
Logged as a real, separate small follow-up rather than silently dropped —
no dedicated card opened for it given how small it is (a future card
touching `ManifestSetting` validation should pick it up alongside items 2
and 3 below).

## Deferred by design (ut-docs#808 items 2–3, BA/Architect scoped these out)

- **Item 2** — `plugin_settings.scope` has no CHECK constraint (a
  non-canonical value like `'GLOBAL'` would escape
  `ux_plugin_settings_global`'s `WHERE scope = 'global'` predicate). Low
  risk today (the app only ever writes the literal `'global'`); a schema
  change, not a manifest-validation one — separate follow-up if ever
  needed.
- **Item 3** — hand-rolled `plugin_settings` schemas in `manifest_test.go`
  and `ui_smoke_test.go` don't include the real migrated schema (the
  `ux_plugin_settings_global` index), so they can't catch a duplicate-key
  regression the way the real schema would. Noted, not built — this
  review's own tests use the real, fully-migrated `openRealDB` helper
  instead, so the new coverage itself isn't affected by this pre-existing
  gap elsewhere.

## Beyond automated tests

- Checked for the two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll` on a file write, cwd-relative path instead of
  `paths.Data(...)`): **N/A** — this diff performs no filesystem I/O, pure
  in-memory manifest validation.
- No UI/HTTP-facing surface, no i18n string, no money/offline/kiosk-
  isolation angle — a backend-only Go install-time validator, same class
  as the two precedents it mirrors. No manual/`web/help/` topic applies.
- No client/shop name or secret-shaped literal in the diff — test fixture
  values are placeholder strings (`"api_key"`/`"a"`/`"b"`, etc.).

## Verdict

Safe to merge.
