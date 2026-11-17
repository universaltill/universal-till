# Review: a revoked plugin release stays off — refuse enable, install and rollback (ut-docs#5658)

Date: 2026-11-06 · Author model: Sonnet (Dev) · Reviewer: Opus 5.5 (independent, fresh context, isolated worktree)
Parent: ut-docs#5653 (revocation feed resolves the listing id to the installed plugin).

## What shipped
- Migration `101_plugin_revoked_releases.sql`: durable `(manifest_id, version)` record of releases the marketplace revoked (`''` = every version), no FK to `plugins`, so it survives uninstall, update and same-version re-install. Checksum pinned in `internal/db/shipped_migrations_test.go`; classified non-admin (not synced) in `internal/data/sync_admin_repo.go`.
- `internal/data/plugin_revoked_repo.go`: `RevokedRelease` (exact version wins over all-versions, `ORDER BY version DESC`) and `ReplaceRevokedReleases` (whole-set replace in one tx).
- `SyncRevocations`: after a successful decode, the full feed replaces the recorded set (an unlisted release is lifted; nothing is re-enabled). Failed fetch, non-200 or unparsable body keeps the records (fail closed, offline-safe). Feed `action: "delete"` is treated as `disable` — the till never uninstalls on an unattended poll (settings, permissions and storage kept); the audit row keeps `requested_action`.
- `plugins.CheckNotRevoked` / `ErrRevokedRelease` (local data only), called from: the enable handler (`internal/pages/plugin_api.go`, 409 `plugins.manage.error.revoked`, 500 on a lookup error — nothing changes), `PersistManifest` (inside its tx; covers marketplace install/update, cloud-directed install, file import, builtin layouts), `Rollback` (409), and — after review M1 — `Importer.Import` and the marketplace installer **before** the live folder is touched. `ClassifyInstallError` maps it to `plugins.install.error.revoked` (not retryable). A different version of the same plugin installs and enables normally (the remedy).
- i18n: two English keys + `en.meta.json` metadata; `web/ui/pages/plugins.html` maps the manage key into its `T` map (inline error, no modal). Help `web/help/en/plugins.md` step 15 rewritten. Contract `ut-docs/reference/contracts/plugin-revocation-feed.md` → 1.1.0 (behaviour added, wire unchanged).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | Medium | Re-importing a revoked version that is still installed (cached bundle / marketplace re-install) deleted that version's live folder before `PersistManifest` refused it; the row then pointed at code that was gone. | Fixed: `CheckNotRevoked` before any `RemoveAll` in `importer.go` and `installer_marketplace.go`; `PersistManifest` stays the backstop. New `importer_revoked_test.go` (import + marketplace) **fails without the fix** (`installed files were removed by the refused re-import: … no such file or directory`) and passes with it — re-verified personally by the orchestrator. |
| L1 | Low | Any 200 JSON object without a `revocations` list (`{}`, wrong server) lifts every record. Intended lift semantics; never re-enables. | Accepted; hardening filed as ut-docs#5693. Delta-cursor caveat noted on ut-docs#5659 and in the contract. |
| L2 | Low | After a lift, enable leaves `install_state='revoked'`, so a process/hardware plugin never auto-starts (pre-existing since #5653). | Filed as ut-docs#5692. |
| L3 | Low | On servers that omit `manifestId` the record is keyed by the listing id and doesn't block a fresh install until the next poll. | Fixed: one line in the contract's Known limits. |
| L4 | Low | Enable check and write are not one tx; a poll in between lets one enable through, the next poll disables it. | Accepted (self-healing within one poll). |
| N1 | Nit | `RevokedRelease` `LIMIT 1` without `ORDER BY`. | Fixed + `TestRevokedRelease_ExactRecordWinsOverAllVersions`. |
| N2 | Nit | A feed entry naming a builtin layout id would make `builtinlayouts.Sync` fail. | Accepted (the cloud never lists builtin layouts). |
| N3 | Nit | New `en.json` keys out of order. | Fixed (`en.meta.json` is not sorted overall; left). |
| Arch cut | — | Replica tills honouring the primary's record. | Filed as ut-docs#5694. |

Checked and clean: the `PersistManifest`/rollback checks read through the open tx (no second SQLite connection); fail closed on DB error; Ed25519 verification untouched (ADR-0006); raw SQL only in `internal/data`; no `showModal`; checkout never blocked; no new file writes or cwd-relative paths.

## Verification
- Reviewer re-verified three TDD claims (PersistManifest, rollback, enable handler checks removed → tests red with `got <nil>` / 200; restored → green).
- Tester (not the Dev agent): one-at-a-time mutation of every check and of the `ReplaceRevokedReleases` call — each matching test went red; tree restored byte-identical (diff md5 before/after). Added `TestEnablePlugin_UpdatedVersionAfterRevokeEnables`. Driven run: real binary, real auth, temp data dir — enable unauthenticated 401; revoked plugin as manager 409 `{"data":null,"error":"plugins.manage.error.revoked"}`, row unchanged; control plugin 200; after removing the record, 200. Feed poll itself not driven (first tick 30 min after boot); covered by httptest `SyncRevocations` tests.
- Gate after the last edit: `gofmt -l .` empty, `go build ./...`, `go vet ./internal/...`, `golangci-lint run ./...` 0 issues, full `go test ./...`, every `ci.yml` build-job guard green except two sandbox-tool failures unrelated to this diff (`guard-deadcode-baseline.sh`: sandbox `deadcode` built with go1.26 vs module go1.27; `guard-shellcheck-version.sh`: no shellcheck binary) — CI runs both. `make docs-shots` run: only `manifest.json` changed meaningfully (surface hash + `plugins/en` topic hash); no screenshotted pixel changes (the only UI edit is an inline-script string), so the sandbox-drifted PNGs were not committed. `run-docs-shots-guard.sh` green.

Pre-flight: Till user-visible behaviour: `web/help/en/plugins.md` updated + docs-shots manifest refreshed (no pixel change); `reference/contracts/plugin-revocation-feed.md` 1.1.0 (ut-docs, same card); README n/a (not stale). en.json keys: 2 added with en.meta.json. ADR: n/a (ADR-0006 unchanged; contract bump only). Plugin type/host fn/manifest: n/a. Migration: 098 appended, checksum pinned. Review record: this file. Follow-ups: ut-docs#5692, #5693, #5694 (+ note on #5659).
