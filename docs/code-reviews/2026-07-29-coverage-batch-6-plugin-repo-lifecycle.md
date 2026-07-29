# Test coverage batch 6: plugin install/trust/permission lifecycle

2026-07-29

Sixth batch: `internal/data/plugin_repo.go`'s remaining 11 zero-coverage
methods (confirmed via coverage combined across `internal/data`,
`internal/plugins`, and `internal/pages`) — `InstallPlugin`,
`UpdatePluginVersion`, `SetPluginActive`, `GetPlugin`,
`GetActivePluginVersion`, `SetPluginState`, `ListRevokedPlugins`,
`ListPluginSettings`, `ListPluginHookEvents`, `HasActivePrinterPermission`,
`HasActivePrinterCapability`, `GetPluginVersionAt`, `CatalogPage`. This is
the plugin install/trust/permission lifecycle — plugins are Ed25519-
verified and run in-process (WASM/wazero per ADR-0001), so install-state
and permission-effectiveness logic has real security implications.

## What changed

`internal/data/plugin_repo_lifecycle_test.go` (new). Opens the DB via real
migrations (`plugins` has a composite FK to `plugin_catalog(id, version)`,
so a hand-rolled schema would need to reproduce that anyway).

## Independent review (opus) — one real gap found and fixed

The review caught that `TestHasActivePrinterPermissionAndCapability` —
the one test in this batch with genuine security stakes — didn't actually
prove what it claimed. `HasActivePrinterCapability` differs from
`HasActivePrinterPermission` only by an extra
`AND p.install_state = 'installed'` clause, but the original test only
ever exercised a plugin that was simultaneously `is_active=1` AND
`install_state='installed'` for every positive assertion, and never
checked `HasActivePrinterCapability` in the deactivated case at all. **The
test would have passed identically if that install_state gate were deleted
from production.** Confirmed this by actually deleting the clause and
re-running: the (now-strengthened) test correctly failed.

**Fixed**: added a mid-upgrade state (`is_active=1`, `install_state=
'installing'`) and asserted `HasActivePrinterPermission` still sees the
grant (it ignores install_state) while `HasActivePrinterCapability`
correctly withholds it; also added the missing `HasActivePrinterCapability`
check to the deactivation case. Verified by temporarily removing the
`install_state` clause from production and confirming the strengthened
test fails, then restoring it and confirming it passes.

Also fixed a second, more minor issue the review caught: the original
upgrade test relied on `InstallPlugin`'s `SELECT ... FROM plugin_catalog
WHERE id = ?` (no `ORDER BY`) picking a specific row when the catalog held
TWO versions of the same plugin id — an unspecified SQLite row-emission
detail, not a guaranteed behavior. Restructured to test only the
unambiguous idempotent-reinstall-of-the-same-version case, with a comment
documenting the real ambiguity for anyone who later wires a production
caller onto `InstallPlugin` (currently unused — the live install path goes
through `UpsertPluginManifest`, not this method).

## Verification

`go build ./...`, `go test ./...`, `go test ./internal/data/... -count=3
-shuffle=on`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass. No production code changed in this
batch (confirmed via `git diff --stat internal/data/plugin_repo.go`
showing no changes) — this was a test-only fix.

## Coverage delta

`internal/data/plugin_repo.go`: last 11 zero-coverage functions now
covered — combined across `internal/data`/`internal/plugins`/
`internal/pages`, the file has no remaining 0%-coverage functions.
