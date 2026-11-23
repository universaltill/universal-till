# Review: replica plugin sync installs a plugin the main has off as inactive (ut-docs#6930)

Date: 2026-11-23. Branch: `fix/6930-replica-inactive-install` (+ ut-docs
`docs/6930-replica-inactive-install`). Author: Sonnet 5.5 (Dev and the fix
round). Independent reviewer: Opus 5.5, fresh context, read-only. Tester:
Opus 5.5, separate agent. Verdict: **approve after fixes**. There were no
blockers; all three should-fix items and two nits are fixed in one round.

Pre-flight: till user-visible behaviour: none on screen. A replica now
converges where it used to log a `plugin_sync.install` problem on every tick.
Help `web/help/en/multitill.md` is n/a, because its #6666 text already says a
disabled plugin stays installed and off on every till. docs-shots: n/a.
`reference/`: ut-docs `architecture/lan-sync.md` is updated. README: n/a.
Amended ADRs: the ADR-0011 #6666 amendment sentence is rewritten to match the
code, and ADR-0077 D3 has a dated note that an inactive replica-sync install
is checked at enable time. No ADR is superseded, and the index is unchanged.
New `en.json` keys: none. Manifest/host-function mirrors: n/a, because
`InstallOptions` and `MarketplaceInstallRequest` are not part of the ut-cloud
signing contract. Review record: this file.

## What shipped

- `convergePluginSet` (`internal/pages/sync_admin.go`) computes
  `inactive := row.Enabled != nil && !*row.Enabled` and passes it through the
  `pluginSyncInstall` seam into `cloudInstallPluginVersionChecked`. A nil
  `enabled` (an older main, or a pre-#6666 cached bundle) behaves as before.
- `MarketplaceInstallRequest` / `bundleInstallSpec` carry `InstallInactive`
  and `SkipActiveOwnerChecks` into `PersistManifest`.
- `PersistManifest` skips the fiscal-sign exclusive-hook check (ADR-0041 B)
  and the layout-preset check (ADR-0106 C) only when **both** flags are set.
  The `fiscal.*` provides check (ADR-0129 §2, installed-based) always runs.
  The bundled installer sets `InstallInactive` only, so it is still checked.
- The tax-DE takeaway reconcile is skipped for an inactive install.
  `ReloadPlugins` still runs, because it only loads `is_active=1` rows and
  must unload v1 when an update lands off.
- `Rollback` reads the installed version (it used to read the active one)
  and skips the preset check for a plugin that is off. `UpdatePluginVersion`
  never touches `is_active`, so the plugin stays off. Without this, a
  version-mismatch rollback of an inactive preset failed and cloudsync fell
  back to a full uninstall.
- The enable-time gate (`pluginEnableRefusal`, reached from `/enable` and
  from the #6666 follow) is unchanged, so exclusivity holds whenever a
  plugin is switched on.

## Findings and resolution

| # | Severity | Finding | Resolution |
|---|---|---|---|
| R1 | should-fix | The bundled installer inherited the skip, and its later enables (`followBundledState`, `resolveBundledTaxForMarket`) are not gated, so two active owners were possible (widens #6666 N4). | Fixed: a separate `SkipActiveOwnerChecks` option is set only by sync. New test `TestPersistManifest_BundledStyleInactiveInstallStillChecksActiveOwner`. |
| R2 | should-fix | Version-mismatch rollback of an inactive preset was refused, then fully uninstalled by `cloudRemovePlugin`. | Fixed in `rollback.go`. New test `TestRollback_InactivePresetRollsBackAndStaysInactive`. |
| R3 | should-fix | ADR-0077 D3 says install and enable time. | Fixed: a dated D3 note, and the ADR-0011 text names replica sync only. |
| R4 | nit | Install-status `Active` for an off plugin. | Comment: it tracks the install lifecycle, not `is_active`. |
| R5 | nit | The test stub bypasses the installer. | Doc comment: T5 and T14 cover the real chain. |
| R6 | nit | Preset swap converges a tick late (pre-existing, #6666). | Not changed. Follow-up noted on the card. |
| T1 | tester | T14 asserted on the tax-code id, so it half false-passed. | Fixed: it uses the plugin id and adds a positive row-exists control. |

## Verification

- TDD: T1, T2, T5–T9, T12 and T13 failed before the change. The Tester
  re-checked T1, T5, T9, T6, T7, T8, T12 and the fixed T14 by reverting the
  production change. The orchestrator re-checked
  `TestRollback_InactivePresetRollsBackAndStaysInactive` against `HEAD`'s
  `rollback.go`: it fails with "has no active version" before and passes
  after.
- Full `./internal/plugins` and `./internal/pages` pass (no `-race`, Dev
  round). The affected tests pass with `-race`, with no races.
- `gofmt`, `go build ./...`, `go vet ./...`, `golangci-lint` (0 issues) and
  28 `build`-job guards pass. `deadcode-baseline` was not run locally (tool
  built with go1.26), so CI runs it.
- A driven two-till run was not feasible in this container (two paired tills
  plus a signed marketplace). The integration tests drive
  `convergePluginSet` through the real `PersistManifest`, `ReloadPlugins` and
  plugin manager, and the real installer against a fake signed marketplace.
