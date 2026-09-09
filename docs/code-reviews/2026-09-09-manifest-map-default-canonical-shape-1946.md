# Code review — manifest install-time map-default canonical shape (ut-docs#1946)

## What shipped

`internal/plugins/manifest.go`'s `PersistManifest` install-time setting
seeding did a bare `json.Marshal(s.DefaultValue)` for every setting
regardless of type. When a manifest declares `default_value` as a JSON
object (decodes to Go `map[string]interface{}`), that wrote the **raw,
unwrapped** object shape into `plugin_settings.value_json` instead of the
JSON-string-wrapped shape `data.EncodeMapSettingValue` already established
as canonical for `MergeAdditiveJSONMapSetting` and `writeTaxOverrides`
(ut-docs#1269). Harmless in practice — `DecodeMapSettingValue`'s read side
already tolerates both shapes — but it meant a freshly-installed plugin's
row was non-canonical until the first merge/save touched it, and
`EncodeMapSettingValue`'s own doc comment overclaimed universality it
didn't actually have.

Fix: in `PersistManifest`, type-assert `s.DefaultValue` to
`map[string]interface{}`; when it is one, route it through
`data.EncodeMapSettingValue` instead of the bare `json.Marshal`. Every
other type (string, number, bool, array, nil) is unchanged, since those
already matched the canonical shape used elsewhere. `EncodeMapSettingValue`'s
doc comment updated to name `PersistManifest` as the third writer.

Regression test: `TestPersistManifest_MapDefaultValueWritesCanonicalShape`
(`internal/plugins/manifest_test.go`) installs a manifest with a map-typed
`default_value`, asserts the stored `value_json` equals
`data.EncodeMapSettingValue`'s own output for that same map, and round-trips
it back through `data.DecodeMapSettingValue` + `json.Unmarshal`.

## Independent review (fresh-context Sonnet subagent, `complexity:easy` routing)

**Verdict: SAFE TO MERGE.** It did not take the TDD claim on trust — it
reverted `plugin_setting_value.go`/`manifest.go` to the pre-fix commit
itself (keeping the new test), reran the test and observed the exact
failure message, then restored the fix and reran clean. It also ran the
real gate itself rather than reading about it: `go build ./...`, the full
`internal/plugins`/`internal/data` package tests, `gofmt -l`, and
`golangci-lint run` on the touched packages — all clean.

It independently checked for a missed sibling writer (grepped every
`DefaultValue`/`default_value` use under `internal/`; confirmed
`PersistManifest` — reachable only from `installer_marketplace.go` and
`importer.go` — really was the last un-migrated writer), confirmed the type
assertion can't miss a real map shape or wrongly capture an array (`ParseManifest`
decodes with no `UseNumber()`, so a JSON object is always exactly
`map[string]interface{}`), confirmed the swallowed error is no worse than
the code it replaced (the map came from already-valid decoded JSON, so
`EncodeMapSettingValue`'s error path is unreachable here), and confirmed the
diff is genuinely backend-only (no `web/`/`internal/pages` files touched, so
the UX-guidelines/manual checks don't apply).

**One non-blocking observation, accepted as-is:** `PluginRepo.ReconcilePluginSettings`'s
duplicate-row "which row is operator-configured" heuristic compares stored
vs. freshly-computed `value_json` byte-for-byte, so a pre-#1946 install's
untouched-default row (old raw-object shape) could misclassify as
"configured" on its next upgrade. Confirmed harmless: the single-row case —
the overwhelming majority — takes the sole row unconditionally regardless of
that comparison, and it's not a regression this diff introduces (the same
drift class already existed for any manifest default whose encoding changes
between versions). Not filing a separate follow-up card — the reviewer
judged it correctly harmless, not merely deferred.

## Verified beyond automated tests

- Full package test runs for `internal/plugins` and `internal/data` (not
  just the new test), green.
- `golangci-lint run ./...` (whole repo) and `gofmt -l .`, clean.
- Every CI-blocking guard listed in `universal-till/CLAUDE.md`'s build job,
  run locally, all green (data-access, kiosk-engine, plugin-menu-read,
  page-http-error, i18n, compliance-claims, help-topics, webkit-version,
  kiosk-launch-flags, android-status-address, android-i18n, emoji-font,
  htmx-loaded, autofill-suppression, e2e-fixtures-import, brand-assets,
  makefile-version) plus `guard-plugin-settings-bump.sh` (directly adjacent
  to this change).
- No real client/shop name or secret-shaped literal in the diff (test uses
  a synthetic `com.test.mapdefault` plugin ID).
- Diff confirmed backend-only: no UI/template/locale files touched, so no
  manual-topic or screenshot update is owed.

## Deferred

Nothing deferred — the one review observation above was judged harmless in
place, not punted.
