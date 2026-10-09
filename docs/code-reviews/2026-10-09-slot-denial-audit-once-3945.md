# Code review — slot denial audited once per process (ut-docs#3945)

**Date:** 2026-10-09 · **Card:** universaltill/ut-docs#3945 (complexity:easy) ·
**Branch:** `fix/3945-slot-denial-audit-once` · **Lane:** `lane:cloud-54`
**Built by:** Sonnet subagent (+ orchestrator fix for review finding 1) ·
**Reviewed by:** Opus 5.5, fresh context (different model from the author)

## What shipped

- `plugins.CheckPermissionAuditOnce` (`internal/plugins/permissions.go`): a
  non-deliberate grant check that audits a `permission_denied` row only the
  first time a (db, plugin, permission) is seen denied in this process.
  Any grant, revoke, install (`PersistManifest`) or uninstall of the plugin
  forgets its remembered denials (`forgetDenials`), so a re-revocation is
  audited again.
- `contentSlotPanels` (`internal/pages/plugin_slot.go`) filters slot entries
  with it before the entry cap: a plugin without `ui:slot:<slot>` is never
  asked, takes no entry place, and its denial is audited + warned once, not
  on every host-page load. Granted entries still go through
  `askPluginUIAs` → `CheckPermission` (fail closed on a revoke racing the
  ask). Slot *actions* keep the audit-every-time check.
- ut-docs `reference/plugin-views.md` documents the behaviour (ut-docs PR on
  `docs/3945-slot-denial-audit-once`).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The remembered denial was only cleared when a check *observed* the grant; grant→revoke with no slot load in between left later denials unaudited (and contradicted the doc). | **Fixed**: `forgetDenials` on Grant/Revoke/PersistManifest/UninstallPlugin; test `TestCheckPermissionAuditOnce_GrantChangeRearms_3945` (failed first: "after an unobserved grant and revoke … want a first denial"). |
| 2 | minor | Granted plugins now cost two indexed permission lookups per slot load. | Accepted — the second check keeps a racing revoke fail-closed. |
| 3 | minor | A revoked entry no longer consumes one of the 8 entry places (setup wizard inherits this). | Intended; documented. |
| 4 | nit | `denialKey` holds `*sql.DB`, keeping closed test DBs reachable. | Accepted — production size is bounded by plugins × slots. |
| 5 | nit | First-denial warn says "not granted" for an undeclared permission too. | Accepted — the audit row carries the right reason. |

## Verified

- TDD re-verified by the reviewer: with the `plugin_slot.go` change reverted,
  `TestPluginSlot_RevokedGrantAuditedOnce_3945` fails with
  `permission_denied audit rows after 5 loads = 5, want 1`; restored, passes.
- Concurrent first denials write exactly one row (`LoadOrStore`); `-race`
  clean on the slot tests.
- `go build ./...`, `go vet`, `gofmt -l`, `guard-data-access.sh`, full
  `go test ./...` (see PR).
- No UI surface changed (backend audit behaviour only) — nothing to look at.
- `golangci-lint` could not run in the sandbox (binary built with an older
  Go than the repo's toolchain); CI runs it.

## Verdict

Safe to merge.
