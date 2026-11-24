# Review: replica plugin sync, prune before the state follow and installs (ut-docs#7006)

Date: 2026-11-24 · Lane: lane:cloud-41 · Branch: `fix/7006-swap-by-uninstall`
Author (Dev + fix round): Sonnet subagents · Review: Opus 5.5 subagent (fresh context) · Tester: separate Sonnet subagent.

## What shipped
- `internal/pages/sync_admin.go` `convergePluginSet`: builds `inPrimary` in its own pre-pass (same rule as
  before: rows with an empty ListingID or PluginID are skipped, bundled rows count), then runs the prune of
  marketplace installs the main no longer lists **before** the first `followStateInto` pass and the install
  loop. The second state pass, the problem resolution and the chip recompute stay at the end.
  Before: when the main swapped exclusive owners by uninstalling X (layout preset, fiscal-sign), a replica with
  X active refused Y for one tick (`PersistManifest` exclusive-owner refusal, `plugin_sync.install:LY` or
  `plugin_sync.state:LY`), and X was pruned only after that.
- Tests (`internal/pages/sync_plugin_swap_6952_test.go`), all driving the real `PersistManifest` refusal with a
  DB-only `pluginSyncRemove` stub:
  - `TestConvergePluginSet_SwapPresetOwnersByUninstallInOneTick`, `…SwapFiscalSignOwnersByUninstallInOneTick` (Y not installed yet);
  - `TestConvergePluginSet_SwapPresetByUninstallYInstalledOffInOneTick`, `…SwapFiscalSignByUninstallYInstalledOffInOneTick` (Y installed off, review finding 1);
  - `TestConvergePluginSet_MalformedRowStillNotInPrimary` (pins the `inPrimary` rule).
- ut-docs `architecture/lan-sync.md`: one sentence on the order (ut-docs PR on `docs/7006-prune-before-installs`).

## TDD evidence
- Not-installed swap tests on the pre-fix code: FAIL "swap by uninstall did not converge in one tick", with
  "already the active layout preset" / "already the active fiscal signing provider" under
  `plugin_sync.install:LY`. They PASS with the fix. Dev ran this, the Tester re-ran it with `git stash`, and the Reviewer re-ran it on a scratch copy.
- Installed-off tests on the first-round diff (prune still after the first state pass): FAIL with a
  `plugin_sync.state:LY` WARN. They PASS after the fix round.
- The malformed-row test passes before and after. It is a pin, not a regression test.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | Prune ran after the first `followStateInto`, so "Y installed off, then X uninstalled, Y on" still took two ticks and raised a `plugin_sync.state` warning | fixed: the prune now runs before the first state pass, with tests |
| 2 | minor | Tests covered only "Y not installed" | fixed: installed-off variants for preset and fiscal-sign |
| 3 | nit (pre-existing) | Prune removes by `rec.PluginID`, so a stale record for an old listing whose plugin the main now lists under a new listing uninstalls it (and its settings); the new order re-installs it in the same tick, which is better than before | deferred → Backlog card |
| 4 | nit | "Installs/updates" header comment above the wrong block | fixed |
| 5 | nit | lan-sync.md wording after the fix in finding 1 | fixed |

## Verification
- `gofmt -l internal/pages` printed nothing, `go vet ./internal/pages` was clean, and `golangci-lint run ./internal/pages/...` reported 0 issues.
- `go test ./internal/pages -count=1` passed in 657 s (after the fix round). The targeted `-race` run of the
  converge/prune/follow/sync tests passed in 270 s. The full-package `-race` run was not done: it takes more than 90 min on this
  container, so CI covers it.
- `guard-fixed-dates.sh` and `guard-data-access.sh` pass.

Pre-flight: till user-visible behaviour n/a (internal sync order; no screen, no help topic, no `en.json` key);
reference updated (ut-docs `architecture/lan-sync.md`); review record this file; follow-up finding 3 → Backlog card.

Verdict: ship. The re-check after the fix round was scoped to the fixes.
