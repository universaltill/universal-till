# Review: device-info permission + device_* host functions (ut-docs#3862)

- **Date:** 2026-10-08
- **Card:** universaltill/ut-docs#3862 (ADR-0140 Decisions 1–2)
- **Built by:** Opus 5.5 (Dev subagent). **Reviewed by:** Fable (independent, different model).
- **Sibling changes:** ut-cloud `internal/downloads/permlint.go` (reviewer flag), ut-docs
  reference/architecture docs, `ut-plugin-language-{de,es,pt}` consent line.

## What shipped

- `device-info` added to `exactPermissions` as a bare permission: no wildcard or parameter form,
  and `Device-Info`, `device-info:*` and `device_info` are refused.
- Consent line `plugins.permissions.desc.device_info` in en/ar/fa/tr, shown on the plugin store card
  and the settings page through `permissionDescKeys`.
- `internal/plugins/wasm_device_info.go`: three functions in the `ut` module, all on the buffer ABI.
  - `device_id_get` returns a v4 UUID that the till mints once with `GetOrCreate` and keeps under
    `till_identity.device_info_id`. That key is per-till (never admin-synced), stripped from the join
    snapshot and hidden in All-settings. Re-pairing never touches it and it is never sent to the cloud.
  - `device_local_ips_get` returns a JSON array without loopback or unspecified addresses, with any zone
    stripped, deduplicated and sorted. `[]` is valid.
  - `device_timezone_get` returns `UTC±hh:mm`.
- Each call goes through `CheckPermissionGranted`:
  - A denial returns -2 and is audited (`permission_denied`).
  - A failed lookup returns -3.
  - A successful read writes a `device_info_read` row naming the function. The row is written only once
    the value exists. If the row can't be written, the call fails closed with -3.
- The functions join the ABI 3 set (registered for all plugins, gated by permission), the same way as
  `secret_set` and `event_publish`. `MaxSupportedWasmABI` is unchanged.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Any `CheckPermission` error (a DB failure included) mapped to -2, so a granted plugin read a lookup failure as a revoke. | **Fixed:** `CheckPermissionGranted` now separates err → -3 from denied → -2. New `TestDeviceInfo_PermissionLookupErrorIsInternal`; reverting the fix makes it fail with `-2, want -3`. |
| 2 | minor | The audit row was written before the value existed, so a failed id read was still recorded as a read. | **Fixed:** the audit is written in `deviceInfoHandOut`, after the value is obtained and before `writeGuest`. `TestDeviceIDGet_BlankStoredIDFailsClosed` now asserts no row is written; it failed before the fix. |
| 3 | nit | A blank check on the trimmed value, but the untrimmed value was returned. | **Fixed:** the trimmed id is returned. |
| 4 | nit | A subtest `setup` closure called the parent `t.Fatal`. | **Fixed:** the closure takes `t`. |
| 5 | nit | No device-info-specific -4 test (bad `dstPtr`). | **Accepted:** `writeGuest` is shared and already covered (`import_wasm_dispatch_test.go`). |

Checked by the reviewer and found correct:
- No value is reachable without the permission check.
- No placeholder is ever returned.
- The id is not reachable through DumpAdmin/ApplyAdmin, the join snapshot, the settings editor or cloudsync.
- `GetOrCreate` is one transaction, so concurrent first calls converge on the same id.
- Audit volume is proportionate: denials were already audited per call, and events are deadline-bound.
- No file writes and no cwd-relative paths.
- Translations match the English and use the neighbouring terminology.

## TDD re-verification (reviewer, in a separate worktree)

Each implementation piece was reverted in turn and the targeted tests re-run:
- Removing the three `Export` lines failed 7 guest-driven tests.
- Removing `device-info` from the allow-list failed `TestIsKnownPermission`.
- Removing the grant audit failed the `_Granted` and `_AuditFailureFailsClosed` tests.
- Removing the permission check failed `_Denied`.
- Renaming the settings key failed `TestDeviceInfoIDSettingsKeyIsPerTill`.

All tests passed again once restored. The orchestrator separately re-verified the finding 1 test.

## Gate

| Check | Result |
|---|---|
| `gofmt` | clean |
| `go build ./...` | pass |
| `go vet` (plugins/pages/data/db) | pass |
| `go test` (plugins/pages/data/db) | see PR |
| guards: card-data, competitor naming, compliance claims, core-neutral, data-access, help-drift, help-topics, i18n, kiosk-engine, netaccess, no-inline-handlers, no-showmodal, page-http-error, pipefail, plugin-menu-read, plugin-settings-bump, migration collision, readme links, autofill | pass |
| `golangci-lint`, `guard-deadcode-baseline` | not run locally: the installed tools are built with Go ≤1.26, below the repo's go1.27; CI runs them |

No help-topic change: no new screen, only one consent line on an existing surface.

## Verdict

Safe to merge.

## Deferred

- `ut-plugin-tax-uk` wiring stays on ut-docs#3863.
- A replica till that joins a main till gets a new device id (its database is replaced by the
  snapshot, and the snapshot strips `till_identity.*`). This is consistent with "a fresh install".
  No card filed.
