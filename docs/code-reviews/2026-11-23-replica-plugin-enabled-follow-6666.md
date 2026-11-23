# Review: a main till's plugin enable/disable follows on joined tills (ut-docs#6666, R1 + R2)

Date: 2026-11-23. Branch: `fix/6666-replica-plugin-state-follow`. Author model:
a different model (Dev). Independent reviewer: Opus 5.5, fresh context,
read-only (TDD claims re-checked as mutations in a throwaway worktree, not on
the shared checkout). Verdict: **approve with should-fixes**. There are no
must-fixes. S1 is pre-existing; fix it here or defer it to a card (see
Resolution).

Pre-flight: till user-visible behaviour: help `web/help/en/multitill.md`
step 12 and `web/help/en/plugins.md` §Several tills are updated (prose only,
English only). docs-shots: n/a (no screen changed). `reference/`:
ut-docs `architecture/lan-sync.md` is updated. README: n/a (no claim went
stale). Amended ADR: ADR-0011 §7 has an additive amendment. The `adr/README.md`
index is n/a because the title and status are unchanged, and no ADR is
superseded. New `en.json` keys: none, because the problem text is a
log/problem line. Manifest/host-function mirrors: n/a, because
`PluginSyncRow` is a LAN wire type and not part of the ut-cloud signing
contract. Review record: this file.

## What shipped

- `internal/data/sync_plugins_repo.go`:
  - `PluginSyncRow.Enabled *bool` (`json:"enabled,omitempty"`). nil means
    absent, which means leave the plugin alone. `&false` is serialised.
  - `DumpActivePlugins` also selects `p.is_active`. The WHERE clause is
    unchanged, so a disabled plugin is still listed.
  - Bundled rows from `DumpActivePluginsWithSystem` leave it nil.
  - `Fingerprint()` already hashes the rows, so a flip makes replicas re-pull.
    A comment records this.
- `internal/pages/sync_admin.go`:
  - The new function `followMarketplaceState` is called in `convergePluginSet`
    after the install loop and before the prune. It reloads plugins once if
    anything flipped.
  - It skips nil, blank, bundled and not-installed rows. Disables are applied
    first. Enables go through `pluginEnableRefusal`; a refused enable stays
    off under `warnPluginSyncOnce("plugin_sync.state:<listing>")`.
  - The `plugin_sync.state:` key is closed when its listing leaves the
    primary.
  - In `followBundledState`, a marketplace row for a bundled id with
    `Enabled == &false` no longer switches the bundled plugin on.
- `internal/pages/plugin_api.go`: the enable gate (revoked release,
  fiscal-sign group, layout preset, `fiscal.*` provides) moved verbatim into
  `pluginEnableRefusal` → `*enableRefusal{Status, GuardKey, Msg}`. The handler
  renders it as before. The stale "nothing reconciles is_active back" comment
  is updated.
- Tests:
  - `internal/data/sync_plugins_repo_6666_test.go` has 3 tests (dump,
    fingerprint, wire compatibility).
  - `internal/pages/sync_plugin_state_6666_test.go` has 9 tests: follow
    disable and re-enable, nil leaves the state alone, independence from a
    failing install, fresh rows across ticks via httptest, fresh install that
    the main has off, fiscal refusal plus problem resolution on drop,
    disable-before-enable for fiscal and preset, no prune, and bundled
    marketplace row off.
- ut-docs: ADR-0011 §7 amendment and an `architecture/lan-sync.md` bullet.

## Verification (reviewer, personally)

- `gofmt -l internal/` printed nothing. `go build ./...` and
  `go vet ./internal/pages/ ./internal/data/` were clean.
  `golangci-lint run ./internal/pages/... ./internal/data/...` reported
  0 issues.
- These guards passed: `guard-data-access`, `guard-i18n`,
  `guard-help-topics`, `guard-fixed-dates`, `guard-core-neutral`,
  `guard-kiosk-engine`.
- `go test ./internal/data/` passed. The affected `internal/pages` tests
  (`6666|Converge|FollowBundled|PluginAPI|Enable|Revoked|Exclusiv|Preset|Fiscal|SyncPull|PluginSync|Apple2`)
  passed. That run includes the existing enable-handler, revoked and
  exclusivity tests, which are **unedited**, so they are the regression net
  for the refactor.
- The whole `internal/pages` package passed with `-timeout 40m`
  (`ok … 567.8s`). It needs more than go's default 10-minute timeout on this
  container.
- Mutation checks, run in a throwaway worktree with the diff applied:

  | Mutation | Tests that fail |
  |---|---|
  | `followMarketplaceState` call removed | FollowsMainDisable, StateFollowIndependentOfFailingInstall, FreshRowsWhileInstallFails, FreshInstallFollowsDisabled, EnableRefusedByFiscalExclusivity, DisablesBeforeEnables |
  | Gate replaced with nil | EnableRefusedByFiscalExclusivity |
  | `followBundledState` reverted to "named = on" | MarketplaceRowDisabledIsOff |

  All tests passed again with the code restored.
- Handler refactor, compared line by line:
  - Every message string and format argument is identical.
  - The 500 statuses map to the lookup failures and the 409 statuses to the
    conflicts, as before.
  - Revoked still goes through `writeReplicaGuard(w, "plugins.manage.error.revoked")`.
    That function writes its own 409, so `Status` is unused there.
  - The order of the checks is unchanged.
  - The disable path does not change.
  - `reconcileTaxDeTakeawayOverridesIfActivated` and the reload still run
    only on the manual path.
  - No behaviour or response changed.
- Repository pattern: the only new SQL is the `p.is_active` column inside
  `internal/data`. The gate calls only existing repo and `plugins.*` functions.
- Races: `syncPullTick` runs from one loop (`startSyncPullLoop`), so ticks
  never overlap. A manual toggle on a replica is refused by the handler, so
  nothing local fights the follow.
- A promote that lands mid-converge can still let a flip write on the new
  main. The same thing is already accepted for installs (#5903 F3).
- Compatibility with older tills:
  - An old replica ignores the unknown key, and a disabled plugin was
    already listed before this change, so nothing new is pruned.
  - An old main sends nil, so the replica never touches the state.
  - A pre-6666 cached bundle fails the fingerprint check, so the replica does
    a full fetch.
- ADRs:
  - ADR-0011: the amendment is additive, and "active" stays the install
    lifecycle.
  - ADR-0194 §9: unchanged, because bundled rows keep "absent = off" and the
    satellite is still not reported converged while it differs.
  - ADR-0031: not touched (R3 is split out).
  - No contradiction, apart from the wording in S2.

## Findings

### Must-fix

None.

### Should-fix

- **S1: a plugin that the main keeps installed but disabled in an exclusive
  group can never be installed on a replica.** The ADR amendment's "stays
  installed on every replica" is false for these plugins. Pre-existing, but
  this card's promise depends on it.
  - Code: `internal/plugins/manifest.go:1418,1428` and
    `internal/pages/sync_admin.go:1110` (install call).
  - Cause, part 1: the replica installs every listed row through
    `pluginSyncInstall` → `PersistManifest`. That function runs
    `validateExclusiveHookOwnership` and `validateLayoutPresetExclusivity`
    unconditionally, and its upsert writes `is_active = 1`.
  - Cause, part 2: `InstallInactive` exists, but sync never passes it, and it
    would not skip those checks anyway.
  - Scenario: the main has preset layout X active and preset Y installed
    but switched off. That is the normal state after a shop changes layout.
    A new or re-joined replica then works through the rows in `listing_id`
    order.
    - If X comes first: X installs and is active. Y's persist is refused
      ("already the active layout preset") on every tick, forever.
      `converged=false`, so the fingerprint never advances and every tick
      does a full fetch, and the `plugin_sync.install:` problem stays open.
    - If Y comes first: Y installs and is live until the follow turns it off
      in the same tick, and X waits one tick.
    - The same happens for two fiscal-sign (TSE) plugins.
  - Fix: install with `InstallInactive` when `row.Enabled` is `&false`.
    `PersistManifest` should then skip the two **active**-owner checks for an
    inactive install, because it cannot create a second answerer. The
    `fiscal.*` provides check is installed-based and stays.
  - This also closes the accepted "live for milliseconds" window of a fresh
    install of a disabled plugin.
  - Add a test: X active plus Y `&false`, both not installed, X's listing
    sorted first. Both should end up installed, X on and Y off, in one tick.
- **S2: the ADR-0011 amendment and lan-sync.md overstate fail-closed.**
  - Files: ut-docs `adr/0011-multi-till-sync.md` (amendment) and
    `architecture/lan-sync.md`.
  - The amendment says "If a check fails or a DB read fails, the plugin stays
    off and a keyed plugin-sync problem is raised". That is true only for the
    enable path.
  - In `followMarketplaceState` (`sync_admin.go:~1012-1027`):
    - A failed `InstalledPluginVersion` or `PluginActive` read, or a failed
      `SetPluginActive`, only `Errorf`s. No keyed problem is raised.
    - On a row that the main has **off**, the plugin then stays **on**.
  - "With the same local switch the primary uses" is also not exact. The
    primary's enable also runs `reconcileTaxDeTakeawayOverridesIfActivated`,
    and the replica deliberately does not, as the design records.
  - The ADR is binding, so make the text say what the code does. Either
    narrow the sentence to "an enable that is refused, or whose check cannot
    be read, stays off under a keyed problem", or route those error paths
    through `warnPluginSyncOnce(pluginSyncStateKeyPrefix+…)` as well.

### Nits

- **N1:** `enableRefusal.Error()` (`plugin_api.go:~494`) returns the raw i18n
  key for the revoked case. The replica's problem line then reads
  "…cannot switch X on to follow the main till: plugins.manage.error.revoked".
  Return readable English, for example "this release was revoked by the
  marketplace". Problem lines are English log text.
- **N2:** a tick that flips both a bundled plugin and a marketplace plugin
  reloads plugins twice (`sync_admin.go:1077` and the new reload). Harmless,
  but these could be one reload at the end of the converge.
- **N3:** a `plugin_sync.state:<listing>` problem closes only when the
  plugin's state matches the main, a flip lands, or the listing is dropped.
  It stays open in two cases:
  - the main is downgraded, so `Enabled` becomes nil;
  - the plugin is no longer installed locally while it is still listed
    (`continue` before any resolve).

  The window is small, but the item is shown as "open" until the next real
  convergence.
- **N4:** pre-existing. `followBundledState` enables are not gated and run
  **before** the marketplace disables. A main that swaps a marketplace
  exclusive provider for a bundled one would briefly have both on in one
  tick. No bundled plugin declares fiscal-sign or preset today. Worth one
  line in the ADR-0194 follow-up card, not this PR.
- **N5:** there is no follow test for the revoked-release refusal, only for
  fiscal-sign. The gate is shared and the handler's revoked test covers it,
  so this is optional.

## Deferred / out of scope (from the design)

- R3: the ownerless synced tender (`payment_methods.plugin_id` skipped by
  admin sync, ADR-0031 ②). This is the split card in the architect notes.
- The `tax.rate.ask` exclusivity gap.

## Resolution

Orchestrator, 2026-11-23 (lane:cloud-41):
- **S1: deferred, pre-existing.** Filed as ut-docs#6930 (install a disabled
  plugin as inactive and skip the active-owner checks for it). The ADR-0011
  amendment now names #6930 for the exclusive-group case.
- **S2: fixed.** Every failure path in `followMarketplaceState` (both reads,
  and `SetPluginActive` on and off) raises the keyed
  `plugin_sync.state:<listing>` problem through `warnPluginSyncOnce`. On a
  failed disable, the text says the plugin may still be on. New test:
  `TestConvergePluginSet_FailedSwitchRaisesKeyedProblem`. It aborts the
  update with a SQLite trigger, then checks that the retry clears the
  problem. The ADR-0011 amendment and lan-sync.md wording now match, and
  they state that the tax-de takeaway reconcile is skipped on replicas.
- **N1: fixed.** The revoked refusal now logs readable English instead of
  the raw i18n key.
- **N2 to N5: accepted as is.**
  - N2: a second reload is cheap, and it happens only when both sets flip
    in one tick.
  - N3: the problem clears when the listing drops or the state matches; a
    stuck chip is cosmetic.
  - N4: pre-existing, and ADR-0194 §9 bundled behaviour is unchanged.
  - N5: the revoked branch shares `pluginEnableRefusal` with the handler,
    and the handler tests cover it.
- **Re-verified after the fix (orchestrator re-read the fix diff):**
  - `go test ./internal/pages -run '6666|ConvergePluginSet|SyncPullTick' -race`
    passes (138s).
  - `gofmt`, `go build ./...`, `go vet ./internal/pages`, golangci-lint and
    guard-i18n are clean.
  - The full `./internal/pages` suite passed before the fix (568s). The fix
    touches only the two functions above, and CI runs the full suite.
- **Tester** (independent): PASS.
  - 11 mutations went red. The one that survived (dropping the reload) got
    a new test.
  - Driven run: the in-process two-till harness, with a real primary over
    HTTP and a failing second install. The main's disable landed on the
    replica in 2 ticks, and the re-enable on the next tick.
