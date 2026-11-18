# Review: App Store build bundles Stripe + SumUp, refuses every other code plugin (ut-docs#3433, #6565)

**Scope:** uncommitted working tree on branch
`feat/6565-ios-bundled-payment-plugins` (based on PR #2583,
`feat/6388-store-channel`): 26 modified files plus the new
`internal/plugins/bundled/` package, `internal/plugins/code_gate.go`,
`internal/pages/plugin_code_gate.go`, `scripts/ci/fetch-bundled-plugins.sh`,
`scripts/ci/guard-appstore-no-code-fetch.sh` and their tests. Docs side
(`ut-docs`, branch `docs/6565-ios-bundled-payment-plugins`): ADR-0194
Amendment 1, an ADR-0121 cross-reference,
`reference/ios-bundled-payment-plugins.md`, and a draft (not sent) Apple
review-notes addendum.

**Author:** Dev agent. **Tester:** separate agent (mutation checks, real
wasm run). **Reviewer:** Fable 5.2, independent of both, security focus
(code integrity, gate bypass, channel source, switch authorization, secrets,
other channels unchanged), plus diff quality and correctness.

## What shipped

- `buildinfo.Channel.CodePluginInstallAllowed()`: false on `appstore` only;
  an unknown channel string already fails closed to `appstore`. The channel
  is read only through `buildinfo.CurrentChannel()` (linker stamp or the
  shell's one-shot `SetInstallChannel`), never from settings, env or a
  request.
- `plugins.ErrCodePluginRefused` + `RefuseCodeRuntime` (by manifest runtime)
  and `RefuseCodeCanonicalType` (pre-network, by catalog type; unknown or
  empty type fails closed). Gated: marketplace `Install`, `DownloadToStore`,
  `installBundleFile` (covers `InstallFromStore`), `Importer.Import`,
  `PersistManifest`, the install / update / store-download handlers, the
  cloud-directive / replica / auto-install path, `UpdateChecker`,
  `Supervisor.StartPlugin`. Refusals answer 403 with
  `plugins.install.error.code_refused_appstore`.
- `internal/plugins/bundled`: a committed lock
  (`bundled.lock.json`: id, version, repo, tag, sha256 of bundle, manifest,
  wasm and extra files; first-party ids and repos only), `go:embed` behind
  the `bundledplugins` tag, boot `Sync` that re-verifies the embedded bytes
  against the compiled-in lock before writing files and persisting with
  trust `system`. First install is disabled; an app update keeps the shop's
  enabled state, settings and keys.
- Build: `fetch-bundled-plugins.sh` downloads the pinned release bundles on
  a GitHub-hosted runner and verifies every hash (fail closed, nothing
  written on failure); `ci.yml` gets a `bundled-plugins` job (real wasm
  under the tag) and the guard + its test; both iOS workflows fetch and bind
  with `-tags bundledplugins`.
- Settings > Plugins: a "Built in" group first, no update / versions /
  uninstall controls for it, Settings link while disabled; uninstall API
  answers 409. Help topic, `en.json` and `en.meta.json` updated.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | high | **Silent drop of an active code plugin on `appstore`.** The loader filter removed an unverified wasm row before the load loop, so it was never marked `install_state='broken'`. A joined till's snapshot or a restored database carrying another till's active tax, fiscal or payment plugin then looked healthy while the tax fail-closed check (`HasBrokenActivePluginForEvent`, ut-docs#368) and the broken-plugin checks on the payment and refund paths saw nothing. | **Fixed.** The check moved into `WasmRuntime.load`; a refused row takes the normal load-failure path (marked broken, tallied, generation bumped). Test `TestAppStore_RefusedActiveWasmRowIsMarkedBroken` (red before: state stayed `installed`). |
| 2 | medium | **Verify and load read the file separately, and the verifier trusted a `(path, size, mtime)` memo.** The verifier hashed the path (or reused a cached hash); `load` then read the file again and compiled whatever was there. A swap between the two, or a same-size swap with the modification time put back, ran unverified bytes. The memo was also an unbounded map (binding rule: bounded caches). | **Fixed.** `BundledCodeVerifier` now takes the bytes; `load` verifies the exact slice it compiles; the memo is gone; the warm-compile pass applies the same check before compiling. Tests `TestAppStore_LoaderCompilesExactlyTheVerifiedBytes` (verifier swaps the file after being asked; module still loads from the verified bytes) and `TestVerify_SameSizeSwapAtTheLockedPathIsRefused`. The memo defect was shown red against the old code with a same-size, mtime-restored swap (accepted) before the change. |
| 3 | medium | **"Off by default" was two writes.** The plugin upsert always writes `is_active = 1`; the bundled first install then disabled the row in a separate statement. An app killed in between came back with the row enabled and, because the version and files matched, nothing ever switched it off: a payment plugin on with no keys, whose no-credentials demo path approves. | **Fixed.** New `InstallOptions.InstallInactive`, applied inside `PersistManifest`'s transaction; `bundled.Sync` uses it for first install and for "was off" upgrades. Tests `TestSync_FirstInstallNeverCommitsAnActiveRow` (red before: observed enabled at the kill point) and `TestPersistManifest_InstallInactiveCommitsDisabled`. |
| 4 | medium | **Rollback was not gated.** `RollbackManager.rollback` restores a snapshot's files and moves the row to that version without `PersistManifest`, so no channel gate ran. The loader would still refuse the bytes, but a manager could leave a bundled plugin broken until the next boot, and the path contradicted "code changes only with an app release". | **Fixed.** `RefuseCodeRuntime` after the manifest parse, before the DB or live dir is touched; the handler answers the translated key. Test `TestAppStore_RollbackOfCodePluginRefused_OtherChannelsUnchanged` (red before: rollback succeeded on `appstore`; `direct` still rolls back). Listed in the guard table. |
| 5 | low | `verifyOnDisk` checked only the wasm, so an altered or deleted manifest or locale file of the installed version was never healed, against the code's own comment. | **Fixed.** Every locked file is checked. Test `TestSync_RewritesAlteredManifestAndLocaleOfSameVersion`. |
| 6 | low | Fetch script: the lock's `wasm_path` / `files` names accepted a `..` segment (the Go parser refuses it), and curl followed redirects without pinning the redirect protocol. Integrity was not at risk (every byte is hash-pinned) but the two validators disagreed. | **Fixed.** One `safe_rel` rule for lock paths and archive members; `--proto-redir =https`. Two new cases in `fetch-bundled-plugins_test.sh` (red before). |
| 7 | low | The guard's gate table did not list the Tester's `Supervisor.StartPlugin` gate (removing it would pass CI), nor the warm-compile and rollback gates. | **Fixed.** Three rows added; the guard test derives a removal case per row (28 rejection cases, all pass). |
| 8 | low | `applyPluginUpdate` took the rollback snapshot (a full copy of the plugin dir) before the gate refused the update. | **Fixed.** Gate moved above the snapshot; no change on other channels (existing `TestAppStore_ApplyPluginUpdateRefused_NoNetwork` still green). |
| 9 | high (product, owner decision) | **No tax or fiscal plugin can run on the App Store build.** Only Stripe and SumUp are bundled, so the setup wizard's tax-plugin auto-install is refused, and an iPad joined to a main till that has a tax plugin now shows that plugin as Broken and blocks the affected lines (fail closed, after finding 1) instead of selling at base rates. Correct for safety, but it decides what an iOS till can do in a shop that needs a tax plugin. | **Not fixed; needs the owner.** Either add the needed tax plugin(s) to the bundled set (ADR-0194 amendment + lock entry) or state that iOS tills are not supported in such shops. Follow-up card needed before the App Store build is offered to a shop that relies on a tax plugin. |
| 10 | medium (owner decision) | **Demo approvals in a production build.** Enabling a bundled plugin without keys runs its no-credentials path (SumUp approves, amounts ending .13 decline). That is the plugins' existing behaviour on every channel and the draft review notes rely on it, but "Built in → Enable" makes it one press away on a live till. | **Not fixed (plugin repos, product decision).** Suggested: refuse to enable without keys outside an explicit test mode, or mark test-mode tenders visibly. Follow-up card. |
| 11 | medium | **Installed version newer than the embedded one.** A join snapshot or restore can carry Stripe/SumUp at a marketplace version above the app's embedded one. `Sync` deliberately never downgrades, the loader refuses the newer bytes (they are not on disk and not in the lock), so the plugin stays Broken until an app release catches up. Fails closed. | **Deferred.** Needs a design choice (take the embedded version over on `appstore`, or keep fleets aligned by policy). Follow-up card. |
| 12 | low | The pre-network gate is keyed on the catalog's `canonical_type`, which the marketplace supplies. A code listing mislabelled as a theme would be downloaded to staging (or the store folder) before the manifest-runtime backstop refuses it; it is never installed, persisted or loaded. | **Accepted.** The runtime is not in the catalog row; the backstops (installer, importer, `PersistManifest`, loader) hold. |
| 13 | low | The lock pins bytes, not provenance: the bundled path deliberately skips the marketplace signature and trusts the reviewed lock. A lock bump is only as good as the check that its hashes belong to a release built from the tagged source. | **Accepted, with a process note.** Reviewer of a lock bump re-downloads the release and compares (done here, see Verification). A CODEOWNERS entry for the lock file is worth adding with the PR. |
| 14 | low | A same-version row that arrived with a non-`system` trust level and intact files is left as is, so it is not shown under "Built in" (it loads, and can be uninstalled; the next boot reinstalls it disabled). | **Deferred** to the card for finding 11. |
| 15 | low | The Plugin Store still shows Download on code listings on `appstore`; the refusal appears on press. | **Deferred** (UX follow-up, noted by Dev). |
| 16 | low | The rollback handler's new refusal mapping has no handler-level test (the gate itself is tested in `internal/plugins`). Screenshots were not regenerated and the "Built in" group was not looked at in a browser by anyone. | **Deferred** to the orchestrator: `make docs-shots` and one driven look before merge. |
| — | info | Refusals answer 403, not the 404 the brief mentioned: the reason stays visible to the operator. Accepted. |
| — | info | Draft Apple notes: "the only content downloaded after install is data" is accurate (asset-only plugins are served as CSS, fonts, icons and locale JSON; no script or HTML), but the list omits layouts, fonts and icons, and its test-mode sentence depends on finding 10. Draft stays unsent; owner placeholders remain. |
| — | info | `canPerform` returns true when auth is disabled through `UT_AUTH` (existing behaviour, all channels, not reachable by a shop on iOS). The channel itself is never env-driven. |

## Security review, by question

- **Can unverified or swapped wasm run on `appstore`?** Not after findings
  1, 2 and 4. `WasmRuntime.load` is the only place a module enters the
  module map, and it compiles only bytes whose id, version, path suffix and
  sha256 match the lock compiled into the binary. With no verifier
  registered, or in a build without the tag, nothing loads. iOS runs the
  wazero interpreter with no on-disk compile cache, so there is no second
  artifact to swap.
- **Gate bypass.** Every writer of a plugin row or live dir was traced:
  marketplace install / store download / install-from-store, file import,
  cloud directive, replica convergence, wizard auto-install, update and
  auto-update, rollback, `builtinlayouts` (runtime `none`), `bundled`. A row
  that arrives without an installer (join snapshot, DB restore) is stopped
  at the loader. `go` / `native` rows are refused by the supervisor. A
  manifest with no runtime defaults to `wasm` and is refused. Runtime `none`
  bundles are served as CSS, fonts, icons and locale JSON only.
- **Channel source.** Build constant or the shell's single
  `SetInstallChannel` call; a mobile build that never sets it is `appstore`.
  No settings, env or request input.
- **Switch authorization.** Enable, disable, uninstall, update, rollback,
  import and trust-level handlers all check `plugin_management` server-side;
  cashier gets 403 and the flag does not move (tested). Session cookie is
  `SameSite=Lax` and these are POSTs.
- **Secrets.** Bundled settings go through the same `PersistManifest` path
  (secret-typed settings stay encrypted, survive an upgrade: tested). No key
  or setting value is logged by the new code. Workflows use no token or
  secret; the plugin repos are public; runners are GitHub-hosted.
- **Supply chain.** Lock is validated by three independent parsers (Go,
  fetch script, guard). Archive extraction accepts regular files with safe
  relative names only, after the bundle hash matched; nothing reaches the
  embed dir unless every pinned hash matched.
- **Other channels.** `sideload`, `play`, `direct`: every gate is a no-op;
  tests cover install, import, persist, store download, load, update and
  rollback there.

## Verification

- Red-first for every reviewer fix (findings 1 to 6), then green; see the
  test names above.
- TDD claims of Dev and Tester re-verified by mutation, each reverted and
  restored within one command with the working-tree diff hash compared
  before and after: see "Mutation re-verification" below.
- Lock hashes checked independently: both release bundles were downloaded
  again from the public plugin repos; bundle sha256 equals the published
  `.sha256` asset and the lock, and the `manifest.json` and
  `bin/plugin.wasm` inside equal the lock. The fetch script then verified
  and embedded those bundles (`--from-dir`).
- **Real wasm:** `go test -tags bundledplugins ./internal/plugins/bundled`
  green with the real Stripe 1.5.0 and SumUp 1.5.1 wasm, including
  `TestRealBundles_InstallVerifyLoadAndRunDemoPaths_AppStore`, after the
  loader change. Embed dir emptied afterwards (only `.gitkeep`).
- `go build ./...`, `gofmt -l`, `go vet` (plugins, pages, buildinfo, app,
  data) and `go vet -tags bundledplugins ./internal/plugins/...` clean.
- Test runs: see "Test runs" below.
- Shell: `guard-appstore-no-code-fetch.sh` passes on the tree; its test
  (28 rejection cases) and `fetch-bundled-plugins_test.sh` pass; shellcheck
  clean on all four scripts.

### Mutation re-verification

Each gate disabled in place, its test run, the file restored byte for byte
(`git diff` hash identical before and after the whole pass):

| Mutation | Owner | Test that failed |
|---|---|---|
| loader byte check in `WasmRuntime.load` → never | reviewer | `TestAppStore_RefusedActiveWasmRowIsMarkedBroken`, `TestAppStore_WasmSyncLoadsOnlyBundledVerifiedRows` |
| `InstallInactive` branch in `PersistManifest` → never | reviewer | `TestPersistManifest_InstallInactiveCommitsDisabled`, `TestSync_FirstInstallNeverCommitsAnActiveRow` |
| rollback gate → always passes | reviewer | `TestAppStore_RollbackOfCodePluginRefused_OtherChannelsUnchanged` |
| `verifyOnDisk` ignores the manifest | reviewer | `TestSync_RewritesAlteredManifestAndLocaleOfSameVersion` |
| verifier hash compare removed | reviewer | `TestVerify_SameSizeSwapAtTheLockedPathIsRefused`, `TestVerifyAgainst_OnlyTheLockedBytesAtTheLockedPath` |
| `PersistManifest` gate → never | Dev | `TestAppStore_PersistManifestRefusesCodeUnlessBundledVerified` |
| embedded-bytes hash compare in `readVerified` → never | Dev | `TestSync_TamperedBytesRefusedNeverPersisted` |
| importer gate → always passes | Dev (Tester's test) | `TestAppStore_RefusedImportKeepsExistingLiveDir` |
| supervisor gate → always passes | Tester | `TestAppStore_SupervisorRefusesProcessPlugins` |
| `plugin_management` check in the enable/disable handler → never | Dev | `TestBundledPlugin_CashierCannotToggle_ManagerCan` (cashier enable answered 200) |

### Test runs

- `go test ./internal/buildinfo ./internal/plugins/... ./internal/data`
  (with every `internal/plugins` fix in place: findings 1 to 5): `buildinfo`, `plugins/bundled`,
  `plugins/builtinlayouts`, `plugins/marketplace`, `data` pass. Full
  `internal/plugins` (580 s): one failure,
  `TestReservedPageRoutesCoverLiteralChildNamespaces`, inherited from PR
  #2583 (see Deferred items); neither file it names is in this diff.
- Re-run at the end:
  `go test ./internal/plugins -run 'TestAppStore_|TestPersistManifest_InstallInactive|TestRollback'`
  pass (all rollback tests, both channels); `plugins/bundled`,
  `builtinlayouts`, `buildinfo`, `data` pass again.
- `go test ./internal/pages -run 'Plugin|Store|Install|Update|Import|Bundled|AppStore|Cashier|CodeGate|Rollback'`
  pass (91 s), with every pages-side code change in place (rollback
  answer, update gate order); `-run 'Help|Manual'` pass after the help edit.
- Guards green after the reviewer changes: appstore-no-code-fetch (+ test),
  data-access, i18n, i18n-meta, help-topics, help-terms, help-breadcrumbs,
  compliance-claims, competitor-naming, core-neutral, ci-tests-sandboxed,
  fixed-dates.
- **Not run by the reviewer:** the whole-repo suite, the full
  `internal/pages` package, Playwright e2e, gomobile bind / Xcode build, the
  new CI jobs on a runner (the fetch step's live download path was exercised
  only through `--from-dir` with bundles fetched by `gh`), `make docs-shots`.
  Nobody has looked at the "Built in" group in a browser.

### Reviewer changes (uncommitted, in the working tree)

- `internal/plugins/code_gate.go`, `wasm_runtime.go`,
  `wasm_compile_warm.go`, `bundled/bundled.go`: byte-level verifier, check
  inside `load`, memo removed, pre-filter removed (findings 1, 2);
  `verifyOnDisk` covers every locked file (5).
- `internal/plugins/manifest.go`, `bundled/bundled.go`:
  `InstallOptions.InstallInactive` (3).
- `internal/plugins/rollback.go`, `internal/pages/plugin_api.go`: rollback
  gate and its translated answer (4); update gate above the snapshot (8).
- `scripts/ci/fetch-bundled-plugins.sh` (+ test),
  `scripts/ci/guard-appstore-no-code-fetch.sh`: findings 6, 7.
- Tests: `internal/plugins/code_gate_reviewer_test.go`,
  `internal/plugins/bundled/bundled_reviewer_test.go`; existing verifier
  tests moved to the byte-based signature.
- Docs: one paragraph in `web/help/en/plugins.md` (a code plugin that
  arrived another way shows as Broken on the iPhone and iPad app); two
  bullets in `ut-docs` `reference/ios-bundled-payment-plugins.md`.

## Pre-flight

Pre-flight: platform/capability claim (iOS plugin behaviour): `web/help/en/plugins.md` updated, `README.md` n/a (no plugin-on-iOS claim changed), `ut-website` n/a (no public claim; store text is an unsent draft); till user-visible behaviour: help topic updated, `make docs-shots` deferred → finding 16 (card needed), `reference/ios-bundled-payment-plugins.md` updated (reviewer added the loader/rollback/atomic-install detail); new `en.json` keys: `en.meta.json` updated, English only (4 keys); ADR amendment: ADR-0194 Amendment 1 + ADR-0121 cross-reference, `adr/README.md` index n/a (no new ADR, status unchanged; `guard-adr-index` green per Tester); new plugin type / host function / manifest field: n/a (none; `InstallOptions` is internal); review record: this file; follow-ups: findings 9, 10, 11 (+14), 15, 16 need Backlog cards, none filed by the reviewer.

## Verdict

**Approve to commit with the reviewer fixes above**, on the condition that
findings 9 and 10 go to the owner as cards before the App Store build is
offered to shops: neither is a code-integrity gap, both are product
consequences of this design that someone has to decide. The 2.5.2 property
the card asks for holds: on `appstore` no path installs, updates, rolls
back, persists or runs code that did not ship in the binary, and what does
run is checked byte for byte against the compiled-in lock at the moment it
is compiled.

## Deferred items

- Owner decision: tax / fiscal plugins on iOS (finding 9).
- Owner decision + plugin repos: no-credentials demo approvals (finding 10).
- Newer-than-embedded and non-`system` same-version rows on `appstore`
  (findings 11, 14).
- Plugin Store cards for code listings on `appstore` (finding 15).
- `make docs-shots`, a driven look at the "Built in" group, handler-level
  rollback refusal test (finding 16).
- CODEOWNERS entry for `internal/plugins/bundled/bundled.lock.json` and the
  fetch script (finding 13).
- Inherited from PR #2583, not this change:
  `TestReservedPageRoutesCoverLiteralChildNamespaces` fails on the base
  branch (`/about/licences` registered without `/about` reserved).
