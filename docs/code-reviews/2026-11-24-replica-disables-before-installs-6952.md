# Review — replica plugin sync: disables before installs (ut-docs#6952)

Date: 2026-11-24 · Lane: lane:cloud-41 · Branch: `fix/6952-replica-disables-before-installs`
Author (Dev): Sonnet subagent · Review: Opus 5.5 subagent (fresh context) · Tester: separate subagent ·
Fix-round re-check: Sonnet subagent (the fix round was written by the Opus orchestrator).

## What shipped
- `internal/pages/sync_admin.go` `convergePluginSet` now runs `followMarketplaceState` twice, through
  the new helper `followStateInto` (fold result + reload on change):
  1. **before the install loop** (`beforeInstalls=true`). Disables still run first, so when the main
     swaps exclusive owners (layout preset X off → Y on; same for two fiscal-sign plugins), a replica with X
     active and no Y has X's disable land before Y's active install. That install is no longer refused by
     `PersistManifest`'s exclusive-owner check for a tick. This pass defers an enable whose installed version
     differs from the main's, because the upgrade install lands it on at the main's version.
  2. **after the install loop**, before the prune (`beforeInstalls=false`, the #6666 behaviour). It applies
     a deferred enable whose upgrade failed (the old version, as before) and closes any state problem
     that a successful install made moot.
- `cloudsync_wire.go`: stale comment ("before the enabled-state follow") reworded.
- Tests, `internal/pages/sync_plugin_swap_6952_test.go`:
  - `TestConvergePluginSet_SwapPresetOwnersInOneTick`, `…SwapFiscalSignOwnersInOneTick`: real
    `PersistManifest` refusal through `stubPersistingSyncInstall`; one tick converges, no
    `plugin_sync.install` problem, second tick installs nothing.
  - `TestConvergePluginSet_EnableWithUpgradeNotGatedOnOldVersion` (Tester): revoked v1 off on the replica,
    main on v2 + on → one tick, v2 on, no problem.
  - `TestConvergePluginSet_ActiveInstallResolvesStateProblem`: an open `plugin_sync.state` problem is
    closed once the install lands the plugin on.
  - Existing callers in the 6666/6930 tests pass `false` (full pass).
- ut-docs `architecture/lan-sync.md` describes the two passes (ut-docs PR on `docs/6952-follow-before-installs`).

## TDD evidence (re-verified by the orchestrator and the reviewer)
- Swap tests on the pre-fix `sync_admin.go`: FAIL "swap did not converge in one tick" with
  "com.t.x is already the active layout preset" / "…active fiscal signing provider (holds fiscal.sign.ask)"
  under `plugin_sync.install:LY`; PASS with the fix (reviewer, own worktree).
- Upgrade test with the defer case disabled: FAIL "upgrade+enable did not converge in one tick … the
  marketplace has withdrawn this plugin version Key:plugin_sync.state:LX"; PASS restored.
- State-problem test without a resolve: FAIL "plugin_sync.state problem still open"; PASS restored.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | medium (pre-existing) | Swap by **uninstall** (main removes X, adds Y): X is not in rows, so the follow never disables it, and the prune runs after the installs → Y refused for a tick. | Deferred → ut-docs#7006 |
| R2 / T1 | low → fixed | Pre-install enable gated on the OLD installed version (revoked v1, main on v2): spurious `plugin_sync.state` problem + `converged=false` for a tick. Tester found the same with a failing test. | Fixed: defer + second pass |
| R3 | low | Stale comment in `cloudsync_wire.go`. | Fixed |
| R4 | nit | Over-long doc-comment line. | Fixed |
| F1 | nit (re-check) | `ActiveInstallResolvesStateProblem` comment claimed the first pass raises the problem. | Fixed |
| F2 | nit (re-check) | No test for the deferral's fallback (first pass defers, upgrade install fails, second pass enables or refuses the old version). It is reasoned and unchanged from #6666's after-install behaviour. | Accepted |

Reviewer also checked, no issue: upgrade + enable of the same plugin, disable + upgrade, reload order
(X unloads before Y loads), prune, problem-key sweep, tender adoption (#6928), the broken-row path.

## Gate
`gofmt -l .` clean · `go build ./...` · `go vet ./internal/pages/` · `golangci-lint run ./internal/pages/` 0 issues ·
`go test ./internal/pages/ -run 'Sync|Plugin|Preset|Fiscal|6666|6930|6928|Swap|ActiveInstall|EnableWithUpgrade'` ok ·
`go test ./internal/plugins/ ./internal/data/` (Tester) · guards data-access, fixed-dates, core-neutral ok.
Not run locally: the full `internal/pages` package with `-race` (it times out on this 4-core box; CI runs it).

Pre-flight: till user-visible behaviour n/a (internal sync ordering; no help topic, no screen); reference
`architecture/lan-sync.md` updated; README n/a; en.json n/a; follow-up → ut-docs#7006.

Verdict: ship.
