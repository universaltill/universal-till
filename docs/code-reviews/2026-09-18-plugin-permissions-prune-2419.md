# Code review — prune plugin_permissions rows a manifest update drops (ut-docs#2419)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2419 (`complexity:easy`, `bug`)
- **Branch:** `fix/2419-prune-dropped-plugin-permissions`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per this
  card's `complexity:easy` routing, `MODEL-ROUTING.md`), isolated in its
  own git worktree (cleaned up on completion, read-only — nothing
  committed or pushed by the reviewer).
- **Verdict: SAFE TO MERGE.** No blocking findings; no non-blocking
  findings folded in either (see "What the reviewer flagged" below —
  everything raised was an explicit non-issue, not a should-fix).

## The bug

`internal/data/plugin_repo.go`'s `InsertPluginPermissions` only ever
inserts `plugin_permissions` rows (`ON CONFLICT(plugin_id, permission) DO
NOTHING` — deliberately, to protect an existing grant across a manifest
re-apply; `TestPluginRepo_InsertPluginPermissions` already pins this).
Nothing in `internal/plugins/manifest.go`'s `PersistManifest` ever
`DELETE`d a row for a permission a plugin's *new* manifest version no
longer declares, so a dropped permission's row — and whatever grant it
held — survived in the DB forever. Two consequences, both pre-existing
(found during the independent review of ut-docs#2240's new Permissions
listing, which is what made the gap visible for the first time):

1. `PluginRepo.CheckPermission`/`plugins.CheckPermission` would still
   report the stale permission as granted if it was granted before the
   update — a plugin could keep exercising a capability its current
   manifest no longer even claims to need.
2. The `/plugins/{id}/settings` Permissions listing could show a
   permission not present in the plugin's *current* manifest, over-claiming
   against the help topic's own wording.

Severity was called Low in the original ticket (no live incident, no way
for an untrusted party to grant themselves anything — grant/revoke is
already `plugin_management`-gated) — this is a stale-state correctness
gap, addressed proactively rather than in response to an incident.

## The fix

- New `PluginRepo.PrunePluginPermissions(ctx, tx, pluginID, declared
  []string) error` (`internal/data/plugin_repo.go`) — reads existing
  `plugin_permissions` rows for the plugin, deletes any whose `permission`
  isn't in the newly-declared set. Delete-only, by design: it pairs with
  the existing insert-only `InsertPluginPermissions` rather than replacing
  it, so an existing grant on a still-declared permission is untouched.
  Mirrors the existing `ReconcilePluginSettings` shape (read existing →
  diff against declared → delete what's gone) already used for
  `plugin_settings` a few functions up in the same file.
- `PersistManifest` (`internal/plugins/manifest.go`) calls
  `PrunePluginPermissions` immediately after `InsertPluginPermissions`,
  inside the same transaction as the rest of the manifest persist.
- New regression test `TestPersistManifest_PrunesDroppedPermission`
  (`internal/plugins/manifest_test.go`): installs a plugin declaring
  `{sales:read, devices:printer}`, grants `devices:printer`, re-applies a
  manifest version that only declares `{sales:read}`, and asserts
  `devices:printer`'s row (and grant) is gone while `sales:read` survives
  untouched.

## What the reviewer verified, concretely

- **Transaction atomicity**: `PersistManifest` begins one `tx`
  (`db.BeginTx`) with `defer tx.Rollback()`; both `InsertPluginPermissions`
  and `PrunePluginPermissions` run inside that same `tx` before `Commit`.
  Any failure anywhere in `PersistManifest` rolls back everything — no
  partial reconciliation is possible.
- **SQL scoping**: prune's `SELECT`/`DELETE` are scoped by `plugin_id = ?`
  and delete by row `id` — cannot touch another plugin's rows. Confirmed
  a fresh install is a correct no-op (insert runs first, so prune sees
  only rows already in the declared set) and that a manifest dropping
  every permission legitimately deletes all of that plugin's rows —
  correct per the same reconcile-to-current-manifest intent
  `ReconcilePluginSettings` already established for settings.
- **Regression test re-derived independently, not trusted on my say-so**:
  the reviewer commented out just the new `PrunePluginPermissions` call
  and reran the test — it failed with `devices:printer row should be
  pruned... got exists=true granted=true`, confirming the test is a real
  regression guard, not a false pass. Restored, reran, passes; confirmed
  the test checks both halves (dropped row gone, surviving row untouched).
- **No regressions**: `go test ./internal/data/... ./internal/plugins/...`
  — all packages pass. Every caller of `PersistManifest`
  (`installer_marketplace.go`, `importer.go`, `builtinlayouts.go`) funnels
  through the same one function and passes the manifest's own real
  `Permissions` field, so none can trigger unintended pruning.
- **Concurrency**: the select-then-per-row-delete loop mirrors
  `ReconcilePluginSettings`'s existing shape in the same code path — a
  plugin-install/update action, not a checkout hot path, so the marginal
  extra round trips inside the write transaction are not a concern. Noted
  as a possible future polish (a single `DELETE ... WHERE permission NOT
  IN (...)` with an empty-set guard) but explicitly not worth blocking on.
- **Gates**: `gofmt -l .` clean, `go build ./...` clean, `go vet
  ./internal/data/... ./internal/plugins/...` clean, `golangci-lint run
  ./internal/data/... ./internal/plugins/...` → 0 issues,
  `scripts/ci/guard-data-access.sh` passes (all SQL stays inside
  `internal/data`, this repo's binding rule).
- Non-blocking observation, not a defect: `installer_marketplace.go`'s
  post-commit `GrantPermission` loop runs in a separate transaction after
  `PersistManifest` returns — pre-existing design, unchanged by this diff,
  no ordering hazard since the prune's transaction has already committed
  by then.

## Verification (Dev + independently by Reviewer)

- `gofmt -l .` clean
- `go build ./...` clean
- `go test ./internal/data/... ./internal/plugins/...` — all pass,
  including the new regression test
- `go test ./...` (full suite) — all pass
- `golangci-lint run ./...` — 0 issues
- `scripts/ci/guard-data-access.sh` — passes
- Regression test confirmed to genuinely fail without the fix (reverted
  and reran by both Dev and, independently, the Reviewer)
