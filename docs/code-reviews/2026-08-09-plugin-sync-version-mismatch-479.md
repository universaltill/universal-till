# Code review: plugin sync-pull install has no post-install version-match assertion

**Card:** universaltill/ut-docs#479
**Date:** 2026-08-09
**Complexity:** easy — Dev inline (Sonnet), Review via fresh-context Sonnet
subagents, per this pipeline's model-routing rule. Two review rounds: round 1
found a blocker, which earns exactly one scoped round-2 verification per the
skill's "second round must be earned" rule — no third round, since round 2's
own finding was should-fix, not blocker.

## What shipped

`cloudInstallPluginVersion` (`internal/pages/cloudsync_wire.go`) is the
version-pinned marketplace install the LAN plugin sync
(`convergePluginSet` in `sync_admin.go`, ut-docs#460) uses so a replica
converges to the *primary's* recorded plugin version, not whatever the
marketplace happens to serve as latest. Nothing previously asserted that
the version `MarketplaceInstaller.Install` actually returned matched the
version requested. Today's marketplace hard-errors on an unknown pinned
version (not exploitable live), but a future/different backend answering
the pin with a substitute release instead of erroring would have let a
replica silently converge to the wrong plugin version — invisible, on a
path that includes tax plugins.

The fix, in `cloudInstallPluginVersion`:

- After `Install` returns successfully, if the request was pinned
  (`version != ""`) and `result.Version != version`, treat it as a failure:
  roll back the install completely via the existing `cloudRemovePlugin`
  helper (same primitive a real uninstall and the sync-pull's own uninstall
  step use — deletes the `plugins` row with FK-cascaded permissions/
  settings/hooks, clears `plugin_storage`, removes the on-disk plugin
  directory), record the install-status row as `Failed` with a new
  `plugins.install.error.version_mismatch` message key, and return an
  error.
- The caller (`convergePluginSet`) already logs a warning and sets
  `converged = false` on any error from this function — unchanged, so the
  fingerprint doesn't advance and the tick retries, matching the
  log-and-retry pattern the rest of `syncPullPlugins` already uses.
- The unpinned path (`version == ""`, used by `cloudInstallPlugin` — the
  cloud directive hook and the manual "install latest" flow) is untouched;
  the check is gated on `version != ""`.
- New locale key `plugins.install.error.version_mismatch` added to all 4
  locales (en/ar/tr/fa); it surfaces automatically through
  `collectProblems`' existing sweep of `Failed` install-status records —
  no separate UI wiring needed (that sweep already reads whatever
  `MessageKey` a failed record carries).
- New test `TestSyncPullTick_VersionMismatchFromMarketplaceFailsConvergence`
  in `sync_plugins_test.go`, plus a new `fakeMarketplace.publishMismatchedRelease`
  helper that publishes a validly-signed release keyed under the
  *requested* version whose manifest actually declares a *different*
  version — simulating the shape of backend bug this card defends against.

## Independent review

Two fresh-context Sonnet subagents, cold (no dev reasoning carried over).

### Round 1 — verdict: FAIL (one blocker), fixed

- **Blocker (confirmed, fixed):** the original fix only flagged the
  install-status row `Failed`; it never undid what `Install` had already
  persisted (files in place, `plugins`/`plugin_catalog` rows written,
  permissions granted via `installBundleFile`/`PersistManifest`) before the
  version check ran. `Manager.Reload` reads the `plugins` table with no
  version filtering, so any other reload later in the same tick (or a
  later admin action) would have wired the wrong, mismatched version into
  the live menu/WASM runtime regardless of the status flag. Verified
  directly by the reviewer: querying `plugins` right after the mismatched
  tick showed the wrong version `is_active=1, install_state='installed'`.
  **Fix:** call `cloudRemovePlugin(ctx, d, result.PluginID)` on mismatch,
  before recording `Failed` and returning — see "What shipped" above.
- should-fix (orphaned on-disk bundle) — resolved as a side effect of the
  blocker fix, since `cloudRemovePlugin` removes the whole plugin
  directory tree.
- nit (test didn't check the `plugins` table right after the mismatch, only
  after a later successful retry) — addressed: the test now asserts
  `pluginInstalledVersion` is not-ok and `hasPluginFiles` is false
  immediately after the mismatched tick.
- Confirmed solid: correctly distinguishes the pinned vs. unpinned call
  paths; `converged`/fingerprint behavior; i18n key present and consistent
  across all locales (`guard-i18n.sh` pass); no repository-pattern
  violation (`guard-data-access.sh` pass); full `go build`/`go test ./...`
  green, independently re-run by the reviewer (not taken on faith).

### Round 2 — scoped re-verification of the round-1 fix only — verdict: PASS (mechanically), one should-fix

- Independently confirmed the blocker is genuinely closed: traced
  `cloudRemovePlugin` → `plugins.UninstallPlugin` → the FK-cascade in
  `internal/db/migrations/001_init.sql` for `plugin_entries`/
  `plugin_settings`/`plugin_hooks`/`plugin_permissions`, plus the explicit
  `plugin_storage` clear and `os.RemoveAll`. Confirmed `result.PluginID` is
  reliably populated by the time the mismatch branch runs (every earlier
  error path returns first). Confirmed the install-status `Save`/`Clear`
  ordering has no lost-update risk (different keys — `ListingID` vs.
  `PluginID` — and the final `Failed` save unconditionally overwrites).
  Re-ran `go build`, the targeted test subset, and `guard-data-access.sh`
  independently — all green.
- **should-fix (accepted, not fixed this cycle — filed as ut-docs#495):**
  `plugins` is one row per plugin ID; `Install` had already overwritten it
  to the wrong version before the mismatch check runs, so if this listing
  had a *previously good, different* version installed (an in-place
  upgrade attempt that mismatched, not a fresh install), `cloudRemovePlugin`
  uninstalls the *whole* plugin as collateral damage — not just the bad
  version. The old good version's on-disk files happen to survive
  (`Install` only touches its own version's directory), but the plugin
  drops out of the DB/live menu entirely rather than staying on the prior
  working version. Trading "wrong version silently active" for "clean
  uninstall" is the safer of the two failure modes for a money-affecting
  plugin, but it's a real availability regression on the upgrade path that
  a defense-in-depth check shouldn't itself cause.
  - **Why not fixed in this same cycle:** the correct fix is to preserve
    and restore the prior version via the existing `RollbackManager`/
    `StoreVersion` machinery (`internal/plugins/rollback.go`) — but
    `StoreVersion` is currently a stub (its own comment: "we assume
    sourcePath is already in the correct location... In a full
    implementation, you'd copy files here" — it never actually snapshots
    anything today), and wiring it in correctly means snapshotting the
    current version *before* every pinned install attempt (not only after
    a mismatch is detected, since by then the old DB row is already
    gone), threaded through `convergePluginSet` → `cloudInstallPluginVersion`.
    That's real design work, not a same-ticket fix for an `easy`-labelled
    card addressing a scenario the issue itself describes as "not
    exploitable today." Filed as **universaltill/ut-docs#495**
    (`complexity:medium`), with acceptance criteria including a test that
    seeds a good prior version and asserts it survives a later mismatch.
    Documented inline at the mismatch branch in `cloudsync_wire.go` with a
    cross-reference to #495.
- nit (already-accepted pattern elsewhere in this file): if
  `cloudRemovePlugin` itself errors mid-rollback, the code logs and still
  proceeds to record `Failed` — same best-effort-log-and-continue style
  the rest of the file already uses for non-critical cleanup failures. Not
  new to this diff.

## What was verified beyond automated tests

- Both reviewers independently re-ran `go build ./...` and the full/
  targeted `go test` suites rather than trusting the dev's report.
- `guard-data-access.sh` and `guard-i18n.sh` re-run independently.
- Round 1 reviewer directly queried the `plugins` table via SQL-shaped test
  assertions to confirm the blocker's real-world effect (not just reading
  the diff and reasoning about it).
- Round 2 reviewer traced the actual FK-cascade schema
  (`001_init.sql`) to confirm the rollback's DB-level completeness, rather
  than assuming `UninstallPlugin` does what its name implies.
- Full gate run personally by Dev after each round's fix: `go build ./...`,
  `go test ./...` (whole repo, not just touched packages),
  `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh` — all green as of the final
  commit.

## Safe-to-merge verdict

Yes. The acceptance criteria are met (mismatch detected, `converged` not
set, fingerprint doesn't advance, retried and converges on a later correct
tick), the round-1 blocker (silently-active wrong version) is genuinely
closed and independently re-verified, and the remaining known gap is a
narrower, honestly-scoped, separately-tracked follow-up rather than a
silently-shipped regression.

## Explicitly deferred items

- **universaltill/ut-docs#495** — preserve a previously-installed good
  version across a pinned-install version mismatch during an upgrade,
  instead of fully uninstalling the plugin as collateral damage. Needs
  `RollbackManager`/`StoreVersion` to actually work (currently a stub) and
  to be threaded into the sync-pull path.
