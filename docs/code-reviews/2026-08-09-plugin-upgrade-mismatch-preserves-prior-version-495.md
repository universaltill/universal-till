# Code review: pinned plugin-sync mismatch on an UPGRADE preserved the prior good version — round 1 found it only survived one tick

**Card:** universaltill/ut-docs#495
**Date:** 2026-08-09
**Complexity:** medium — Dev inline (Sonnet), Review via a fresh-context Opus
subagent (worktree-isolated), per this pipeline's model-routing rule. One
review round: round 1 found two blockers sharing a single root cause, fixed
in-round and re-verified; no second round needed since the fixes were
scoped exactly to what round 1 named and the full gate re-ran clean after.

## What shipped

Follow-up from ut-docs#479's own round-2 review. `cloudInstallPluginVersion`
(`internal/pages/cloudsync_wire.go`) is the pinned marketplace install the
LAN plugin sync (`convergePluginSet`, ut-docs#460) uses to converge a
replica to the primary's recorded plugin version. When the installed
bundle's actual version doesn't match the pin, it rolls back — but the old
rollback was always a full `cloudRemovePlugin`, which `os.RemoveAll`s the
plugin's *entire* directory tree (every version, not just the bad one). On
a fresh install that's fine (nothing to lose); on an in-place **upgrade**
of a plugin with a different, previously-good version already active, it
uninstalled the whole plugin as collateral damage — a real availability
regression on a path the code's own comments call out as money-affecting
(tax plugins).

The fix:

- `RollbackManager.StoreVersion` (`internal/plugins/rollback.go`) was a
  stub that never copied files ("In a full implementation, you'd copy
  files here"). It now actually copies the live per-version install
  directory (`pluginBaseDir/pluginID/version/`, the layout
  `installer_marketplace.go`'s `installBundleFile` leaves) into
  `RollbackManager`'s own `versions/` snapshot tree, via a new
  `copyVersionFiles` helper (`filepath.WalkDir` + `os.ReadFile`/
  `os.WriteFile`, preserving permissions). `sourcePath == ""` keeps the old
  no-op-copy behavior unchanged (an existing test depends on it), so this
  is backward compatible. This also fixes the *existing* manual "update
  plugin" admin-rollback call site (`plugin_api.go:531`), which already
  called `StoreVersion` and was silently getting nothing from it.
- `cloudInstallPluginVersion` now looks up (via `InstallStatusStore.Get`)
  whether the listing has a currently-Active install with a known
  version, snapshots it before attempting the pinned install, and on a
  mismatch calls `RollbackManager.Rollback` to restore that version
  (DB row, `plugin_entries`, files) instead of `cloudRemovePlugin` — falling
  back to the old full-uninstall only when there's no prior version to
  restore, or `Rollback` itself fails.

## Independent review — round 1 (Opus, worktree-isolated)

Verified build/vet/full `go test ./internal/plugins/... ./internal/pages/...
-race`/all 4 `CLAUDE.md`-required guards clean, and independently
re-verified the TDD claim (reverted just the two implementation files,
confirmed both new tests failed with the real reported-bug error messages,
restored, confirmed passing). Then found, via targeted probe tests it wrote
and ran itself:

**Blocker 1 — the fix protected exactly one sync tick.** The snapshot-
before-install step required the install-status record's `State ==
Active`, but the mismatch branch saved the record as `Failed` after a
*successful* rollback. The sync-pull loop retries every ~30s, so on the
very next tick against the same still-mismatching backend, the "is there a
prior good version" check now read `false` and fell straight through to
the old full `cloudRemovePlugin` — destroying the plugin the fix exists to
protect, just one tick later than before. Proved with a two-tick probe
(`("1.0.0", true)` after tick 1 → `("", false)` after tick 2).

**Blocker 2 — the same root cause left a rolled-back plugin permanently
unprunable.** `convergePluginSet`'s prune loop only ever prunes a record
whose `State == Active`. With the record stuck at `Failed` post-rollback,
a shop owner removing the plugin on the primary afterward could never
propagate to a replica that had gone through a rollback — a real integrity
risk on the exact code path the round-1 fix comments call out as
money-affecting.

**Real-but-minor (fixed alongside the blockers, same root cause):**
`Rollback` was being called even when the marketplace's mismatched
response happened to already equal the prior good version, which made
`Rollback` error `"already at version"` and fall through to a needless
full uninstall of an already-correct install.

**Real-but-minor (fixed):** the mismatched version's own on-disk directory
was never cleaned up after a successful `Rollback` (which only touches DB
rows) — an unbounded disk-space leak on repeated retries against a
persistently-mismatching backend, on flash-backed POS hardware.

**Real-but-minor (fixed):** `hasPriorGood` stayed `true` even when
`StoreVersion` itself failed to snapshot anything, relying on `Rollback`'s
own `os.Stat` failure as an accidental safety net living in the callee
rather than a decision made at the call site.

**Nitpicks (accepted, not fixed):** `copyVersionFiles` buffers whole files
via `ReadFile`/`WriteFile` rather than streaming with `io.Copy` (bundles
are signature-verified and size-capped at install time, so the risk is
low); it also dereferences symlinks into regular files rather than
preserving them (plugin bundles are extracted from a verified tar.gz, not
expected to contain symlinks). Both deferred as genuinely low-risk given
this code's actual input shape, not overlooked.

## The fix, round 1

- The status record after a successful rollback now reads `State: Active`
  (not `Failed`) with `CurrentVersion` set to the restored version — this
  is what makes both the next tick's protection *and* the prune loop work
  correctly; the pinned-install call itself still returns an error (so
  `convergePluginSet` still logs a warning, still marks `converged =
  false`, and the sync fingerprint still doesn't advance — unchanged from
  before this review round).
- A three-way switch in the mismatch branch: (a) the served version
  already matches the prior good version → nothing to restore, treat as
  already-correct; (b) a real prior version to restore → `Rollback`, then
  remove the now-orphaned mismatched-version directory; (c) no prior
  version, or `Rollback` failed → the original full `cloudRemovePlugin`,
  unchanged.
- `hasPriorGood` is now cleared if `StoreVersion` itself errors.
- Two new regression tests added directly in response to the two
  blockers: `TestSyncPullTick_VersionMismatchOnUpgradeSurvivesRepeatedTicks`
  (three consecutive mismatched ticks, asserts the prior version survives
  all three — not just one) and
  `TestSyncPullTick_RolledBackPluginStaysPrunableAfterPrimaryRemovesIt`
  (rollback, then the primary genuinely removes the listing, asserts the
  replica prunes it). Both were confirmed failing against the pre-fix code
  with the exact bug the reviewer's probes found, then confirmed passing
  after the fix — the same TDD discipline the original implementation
  used, applied to the review's own findings.
- The existing single-tick test's status assertion was corrected from
  `Failed` to `Active` to match the corrected (and now actually meaningful)
  semantics.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` clean.
- Full `go test ./...` (36 packages) green.
- `go test ./internal/plugins/... ./internal/pages/... -race` clean, twice
  (once pre-fix-round-1-findings, once after).
- All four `universal-till/CLAUDE.md`-required guards pass:
  `guard-data-access.sh`, `guard-i18n.sh` (no strings changed — background
  sync-pull code, no template/route/user-facing surface), `guard-kiosk-
  engine.sh`, `guard-plugin-menu-read.sh`.
- No real client/shop name in test data (`com.test.sync-alpha`,
  `com.test.snapshot`, `listing-alpha` throughout).
- No manual/`web/help/` update required — confirmed, not assumed: no page
  route, no template, no new user-facing string (reuses the existing
  `plugins.install.error.version_mismatch` i18n key). `guard-help-
  topics.sh` also passes.
- Not fixed here, explicitly out of scope (per the original issue and
  unchanged by this review round): a *persistently*-mismatching pin is
  still retried forever on every tick (pre-existing behavior, not
  introduced or worsened) — defense-in-depth for a hypothetical future/
  different marketplace backend, not a live bug against today's
  marketplace, which hard-errors on an unknown version instead of
  substituting one. ut-docs#368 (files-deleted-but-DB-row-survives) is a
  separate, still-open card.

## Verdict

Safe to merge. Both round-1 blockers are fixed, independently re-verified
against the reviewer's own reproduction, and covered by permanent
regression tests (not just the throwaway probes the review used to find
them).
