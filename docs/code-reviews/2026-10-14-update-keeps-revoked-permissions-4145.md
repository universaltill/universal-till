# Review — a marketplace update keeps the operator's permission revokes (ut-docs#4145)

**Branch:** `fix/4145-update-keeps-revokes` · **Author:** Opus 5.5 (lane:cloud-41) · **Reviewer:** Fable, fresh context, own worktree

## What shipped

- `installGrantsPermission` (`internal/plugins/installer_marketplace.go`) returns false for every
  permission the plugin already declared before this install, so an update or same-version
  reinstall keeps the operator's grant/revoke state. Only a first declaration is granted, still
  minus ADR-0153 Decision 5's ungranted set. Read error on the previous rows → nothing granted
  (fail closed, unchanged). Uninstall cascades the rows, so a fresh install after it grants again.
- Review M1 fix: a **fresh** install that fails after `PersistManifest` (catalog upsert error,
  or a cancelled request context) is undone with `UninstallPlugin(context.WithoutCancel(ctx), …)`,
  so the retry is a first declaration again. An update that fails keeps its existing plugin
  (an unreadable "installed before" also counts as installed — a failure never deletes a plugin).
- Help: `web/help/{en,de,tr,fa,ar}/plugins.md` — one sentence after "Revoking takes effect
  immediately". `docs/plugin_guidelines.md` and ut-docs `architecture/plugin-architecture.md` state
  the general rule.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | major | Failed fresh install left ungranted rows; the retry then granted nothing (before, the retry healed it) | **Fixed** + `TestMarketplaceInstall_FailedFreshInstallLeavesNoRows` (fails without the fix: "left permission rows map[view:sales:false]") |
| — | minor | A `GrantPermission` failure on first install (Warnf only) is no longer healed by a reinstall | Accepted: logged, the operator can grant on the settings page; a DB write failing there is not a normal state |
| m1 | minor | `docs/plugin_guidelines.md` stated the rule only for `ai.send.*` | Fixed |
| m2 | minor | Manual import, then marketplace install of the same id, now grants nothing | Accepted and documented in the doc comment: consistent with "manual imports stay ungranted" (ADR-0153) and with keeping stored state |
| nit | nit | en "asks for the first time", de "aus dem Marktplatz" | Fixed ("requests for the first time", "über den Marktplatz") |

Reviewer checked every production path into grants (Install ← plugin_api install/update and
cloudsync; InstallFromStore) — all go through `installBundleFile`; Rollback and the manual
importer grant nothing. No go or e2e test relied on a reinstall re-granting.

## Verification

- TDD: reviewer reverted `installer_marketplace.go` to `origin/main` → `TestMarketplaceInstall_UpdateKeepsRevokedPermissions`
  fails (`after update view:sales granted=true … want false`, `reinstall of 1.0.1 re-granted the revoked view:sales`)
  plus the two new `TestInstallGrantsPermission` rows; restored → passes. M1 test written first, seen failing.
- `go build ./...`, `go vet`, `gofmt`, `golangci-lint run ./...` (0 issues); `go test -timeout 20m ./internal/plugins`,
  `./internal/pages` and the rest of CI's main test step; the `build` job guards (help drift, i18n, compliance, data access …).
- No UI surface changed (help prose only); no screenshot needed.

## Verdict

Safe to merge.
