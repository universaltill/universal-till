# 2026-09-09 — Route plugin_settings_page GET's null unwrap through the shared decode seam (ut-docs#1945)

## What shipped

`internal/pages/plugin_settings_page.go`'s `GET /plugins/{id}/settings`
handler had its own inline unwrap of a stored `plugin_settings.value_json`:

```go
var v string
if json.Unmarshal([]byte(row.ValueJSON), &v) != nil {
    v = row.ValueJSON // non-string JSON edits raw
}
```

This was a fourth copy of logic ut-docs#1269 already unified into
`unwrapSettingValue`/`data.DecodeMapSettingValue`. It disagreed with the
shared seam on a bare stored `null`: `json.Unmarshal("null", &v)` is a
documented Go no-op that leaves `v == ""`, while `DecodeMapSettingValue`'s
leading-quote check leaves any non-string-JSON value (`null` included)
untouched — so the correct rendered value for a bare `null` row is the
literal text `null`, not a silently empty field.

- `internal/pages/plugin_settings_page.go`: the inline unwrap is replaced
  with `v := unwrapSettingValue(row.ValueJSON)`.
- `internal/pages/plugin_settings_page_test.go`: new regression test
  `TestPluginSettingsPage_GET_BareNullStoredValueMatchesSharedDecodeSeam`
  seeds a plugin setting with the raw stored value `null` (via
  `UpsertPluginSettingScoped` directly, not the `seedPluginSetting` helper
  which JSON-encodes a Go string) and asserts the rendered `<input
  value="...">` shows the literal text `null`.

## Independent review

Fresh-context Sonnet review, no access to the implementer's reasoning.
Actually ran, not just read: `go build ./...` (clean), `go vet
./internal/pages/...` (clean), `gofmt -l` on both changed files (no
diffs), `go test ./internal/pages/... -run TestPluginSettings -v`
(27/27 pass, including the new test), and the full `go test
./internal/pages/...` package suite (green).

**Verdict: safe to merge. No fixes needed.**

### TDD re-verification (independent, not taken on trust)

Reverted only the production-code line back to the old inline
`json.Unmarshal`-into-string block, kept the new test. It failed with a
real assertion error (`expected the bare-null stored value to render as
literal "null" ..., got: ...` — no `value="null"` in the body, confirming
the old code renders `""`). Restored the fix, re-ran the full
`TestPluginSettings` set — all pass again, working tree confirmed clean
against the committed diff. The new test is a genuine, non-tautological
regression check, not a false-pass.

### Semantic checks

- **Traced every caller of the changed `v`**:
  - `buildTaxOverrideRows(v, ...)` — for a bare `null` stored value,
    `json.Unmarshal([]byte("null"), &overrides)` (map target) is itself a
    no-op leaving the map empty, same as the old code's `v == ""` path
    (which skips the unmarshal entirely, `trimmed == ""`). The typed-editor
    `ok` decision and rows are unaffected — this diff only changes the
    plain-text fallback input's displayed value.
  - Secret path (`sv.IsSet = v != ""`, `sv.Value = ""` on secret keys) —
    a bare-null stored value would flip `IsSet` from false→true under the
    new code. Checked every writer of `plugin_settings` rows: the POST
    handler always writes `json.Marshal(<trimmed string>)`, never a bare
    `null`, and `internal/plugins/wasm_hostfns.go` exposes only
    `settings_get`, no `settings_set` a plugin could use to write its own
    row. So a secret key cannot acquire a bare-`null` stored value through
    any live code path today — this is the same "harmless today" case
    `unwrapSettingValue`'s own doc comment already calls out, not a new
    live-leak path.
  - POST handler (`POST /api/plugins/{id}/settings`) — confirmed zero diff
    lines touch it; this PR is GET-path only.
- **Style/imports**: comments proportionate to the file's existing
  doc-comment density; `encoding/json` import remains genuinely needed
  (`buildTaxOverrideRows`, the typed-overrides read at the POST handler,
  and the POST handler's own `json.Marshal`).
- **Scope**: 2 files, net +28/−4 lines (mostly the new test) — no UI
  surface touched beyond the single unwrap call, no new i18n keys, no
  docs-shots impact (confirmed: `guard-docs-shots.sh` stays green with no
  regeneration needed).

## Verified beyond automated tests

- `go build ./...` — whole repo, clean.
- `go vet ./internal/pages/...` — clean.
- `gofmt -l internal/pages/plugin_settings_page.go
  internal/pages/plugin_settings_page_test.go` — no diffs.
- `go test ./internal/pages/... -run TestPluginSettings -v` — 27/27 pass.
- `go test ./internal/pages/...` (full package suite) — green.
- Manual mutation test (see TDD re-verification above) — confirmed the new
  test is load-bearing.

## Deferred / follow-up

None new. ut-docs#1946 (the sibling finding from the same #1269 review
batch, about `internal/plugins/manifest.go`'s install-time default seeding
still writing the raw-object shape) is a separate, still-open card — not
folded into this diff, kept to its own stated scope.

## Safe-to-merge verdict

**Yes.** Build, vet, gofmt, and the full targeted + full-package test
scopes are all clean; the semantic change (bare `null` now renders as
literal `"null"` instead of `""`) is correct and traced through every
caller with no live-code path that could turn it into a worse outcome;
the POST handler is untouched; the regression test is confirmed
non-tautological via mutation testing.
