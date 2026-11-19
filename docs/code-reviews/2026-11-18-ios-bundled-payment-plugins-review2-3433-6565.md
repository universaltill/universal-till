# Review 2: bundled App Store plugins, Fable review fixes (#3433, #6565)

Pre-flight: gofmt, vet, golangci-lint, tests per below; reviewer = Fable, independent of the Sonnet dev.

# Review 2: App Store bundled plugins (universal-till#2587, ut-docs#6628)

Combined Tester + Reviewer, independent of the Dev agent. 2026-11-18.
Code PR head `1d9ca651e` (`feat/6565-ios-bundled-payment-plugins`, base `feat/6388-store-channel`); docs PR head `ce9926b2`.
Scratch worktree `/private/tmp/tabletfix/wt-review-apple`, private GOCACHE `/tmp/tabletfix/gocache-review`.
Nothing pushed, no other worktree touched, no process killed.

## Verdict

**Changes requested, small.** The integrity chain and the gate hold: nothing I tried turns bytes from the network,
a backup, a join snapshot, a directive or another till into running code on `appstore`. Three real defects and one
false-pass test were found; all four are fixed and tested in a scratch patch that needs applying before merge.
Two items need an owner decision (store UI, supply chain), neither blocks this PR.

Patch: `/tmp/tabletfix/review-apple2-fixes.patch` (one commit `f5fa03e46` on local branch
`review/6565-apple2-fixes` in the scratch worktree; `git am` onto the PR branch). 8 files, +398/-2.

## Findings, ranked

| # | Sev | Where | Failure scenario | Status |
|---|---|---|---|---|
| 1 | Medium | `internal/pages/cloudsync_wire.go:1878` `cloudRemovePlugin` (callers `:1279` cloud directive, `sync_admin.go:1050` satellite prune) | The uninstall API refuses a built-in plugin (409) because an uninstall deletes the shop's keys. The cloud "remove plugin" directive and the satellite prune loop end in the same uninstall with no check. Result: row, keys and settings deleted; the plugin is gone from the Plugins page until the next boot, then comes back disabled. For `tax-uk` that leaves a UK shop with no VAT plugin on, and nothing re-enables it (the tax spec is only queued at setup). Prune case: an iPad satellite that joined a main till holding a marketplace copy of Stripe carries that listing's install record; when the main drops the plugin, the satellite uninstalls its built-in one. Proven red: `TestCloudRemovePlugin_BundledIsSwitchedOffNotUninstalled`, `TestConvergePluginSet_PruneNeverUninstallsBundledPlugin`. | **Fixed in patch**: switched off + stale install record cleared |
| 2 | Medium | `scripts/ci/guard-appstore-no-code-fetch.sh:188` | Finding 19 only half fixed. The behaviour step ran six hand-picked tests. With the loader gate neutralised (`if false && !codeLoadAllowed(...)` in `wasm_runtime.go:687`) the guard printed green, exit 0. `ios-testflight.yml` runs this guard and three tagged tests, not the suite, so a release build depended on `ci.yml` having run on that commit. | **Fixed in patch**: runs every `TestAppStore_` test (10 s); same mutation now exits 1 |
| 3 | Low | `internal/pages/plugin_api.go:107` `POST /api/plugins/trust` | A manager can set a built-in plugin to `untrusted`. Until the next boot it leaves "Built in", the uninstall refusal no longer applies (keys deletable), and the main till stops listing it to satellites (`trust_level = 'system'` filter), which then switch it off, VAT included. Proven red: `TestPluginTrustAPI_RefusesBundledPlugin` (was 200). | **Fixed in patch**: 409, new key `plugins.manage.error.bundled_trust_fixed` (en.json + en.meta.json) |
| 4 | Low | `internal/pages/plugin_apple2_test.go:202` `TestImportFromFile_RejectsSystemTrustLevel` | False pass: stays green with the `trust_level` check removed (the unsigned bundle is answered 400 for another reason). The L3 TDD claim was not verified by it. | **Fixed in patch**: `TestImportFromFile_TrustLevelCheckIsWhatRefuses` kills the mutant |
| 5 | Medium (owner) | Plugin Store UI, `plugins_store_page.go` | Not a code defect: on `appstore` a code listing still shows its Download button and answers with the refusal message on press. An App Review tester sees a store full of code plugins. Dev notes already flag it as a UX follow-up. | Not fixed. Card needed before submission |
| 6 | Medium (owner) | Supply chain (finding 20) | Unchanged in substance: CODEOWNERS is added but binds only once branch protection requires code-owner review; no attestation exists for the plugin releases, no rebuild-and-compare. Stated honestly in the ADR amendment item 10. | Owner action: tick the branch-protection box; file the attestation card |
| 7 | Low | `internal/plugins/bundled/bundled.go:235-249` | Permissions are granted after the persist transaction commits. An app killed between the two leaves a same-version, system-trust row with nothing granted, which the next boot treats as healthy: payments fail closed until a manager grants them by hand. Narrow window, fails closed. | Not fixed |
| 8 | Low | `sync_admin.go:925`, `tax_autoinstall.go:412` | Follow-the-main and the tax path switch a built-in plugin on with a bare `SetPluginActive`, skipping the enable handler's revoked-release check. Only matters if the marketplace revokes a bundled version. | Not fixed |
| 9 | Low | `tax_autoinstall.go:392` | On a satellite the join path enables `tax-uk` locally, then the next converge follows the main (one flip within about half a minute if the main has it off). "Switch it off yourself and it stays off" holds because the tax spec is queued only at setup completion; Disable records no decline, so finishing setup again re-enables it. | Not fixed, wording only |
| 10 | Low | test gaps (mutation survivors) | `wasm_compile_warm.go:179` gate removed: green (pre-compile only, never loaded). `plugins_store_page.go:344` page gate removed: green, the installer's own gate answers, but `EnsureRegistered` then runs first, so a refused download would still enrol the store. | Not fixed |
| 11 | Low | `docs/code-reviews/2026-11-17-ios-bundled-payment-plugins-3433-6565.md` | The record covers the first commit only: no mention of UK VAT, permissions, satellites (#6626, #6627). | Orchestrator to append this review |

Tests I added for behaviour that had none (in the patch, all mutation-checked): same-version row with lost system
trust is reconciled; an embedded manifest that is not the lock entry's plugin is refused; a failed embedded reinstall
is not reported converged; the real UK VAT wasm loads on `appstore` once enabled (the existing real-wasm test only
installed it); `Verify` needs the embedded set.

## Early findings re-verified (proof idea re-run as a mutation)

| Finding | Fixed? | Evidence |
|---|---|---|
| 4 UK VAT on appstore | Yes | Tax branch removed: `TestAppStoreTax_GBTill...` fails. Real `tax-uk` 1.3.1 installs, loads and registers its three hooks under the tag. Zero marketplace hits. |
| 5 Permissions | Yes | Grant call removed: `TestSync_GrantsDeclaredPermissionsOnFirstInstallOnly` fails. Real bundles: `net:api.stripe.com`, `net:api.sumup.com`, `net:test-api.service.hmrc.gov.uk` granted. Only the manifest's own declarations, first declaration only, read failure grants nothing (both mutants killed). |
| 17 Satellite | Yes, plus finding 1 above | `followBundledState` disabled, fetch-skip removed, satellite message removed: all killed. It iterates only the lock's ids and needs local system trust, so a main till cannot switch on an arbitrary plugin id. |
| 18 Version skew | Yes | "Newer row left alone" mutant killed by two tests. |
| 19 CI guard | Partly, see finding 2 | Predicate forced `return true`: guard exit 1. Tagged build with an empty embed dir: compile error "no matching files found". Loader mutation: was green, fixed in patch. |
| 20 Supply chain | Mitigated, not closed | See finding 6. Lock hashes equal the three published release bundles I downloaded fresh. |
| 21 Docs | Yes, see docs section | |
| L1 rollback | Yes | Gate removed: two tests fail. |
| L2 staged bundle | Yes | Delete removed: test fails. |
| L3 trust / Built in | Yes after patch | `Available()` and trust mutants killed; import check only by my new test; trust reconcile had no test (added). Residual: finding 3. |
| L4 verify/load gap | Yes | Loader hashes the bytes it compiles; same-size swap test fails without the hash check. |
| L5 channel latch / seam | Yes by read | Swift calls `MobileSetInstallChannel("appstore")` before start (not compiled here, needs macOS CI); guard `[seam]` case passes. |
| L6 mutation gaps | Mostly | 44 mutants run: 36 killed by the PR's tests, 6 more by the patch's tests, 2 survive (finding 10). |

Gate bypass, every persist path traced: marketplace install, store download, install-from-store, file import,
update, update checker, rollback, cloud directive, replica convergence, tax auto-install all end in a gated
function; restore and join snapshot carry rows only and are stopped at the loader (marked broken); go/native is
stopped at the supervisor; `DryAskWasm` goes through the same `load`. Channel is read only through
`buildinfo.CurrentChannel()`. `BundledLockVerified` is set only inside `internal/plugins/bundled`.
Also checked: no `if false &&` / `if true {` in non-test code, no junk files in `internal/pages`, SQL only in
`internal/data`, `MkdirAll` before every write, `paths.Plugins()` throughout, i18n English + `en.meta.json` only,
help topic updated, cashier gets 403 on the switch.

## Docs PR #6628

The review notes now claim no more than the code does, with three small caveats:

- **"The app downloads no executable code."** True on every designed path. Residual: a listing our own catalogue
  mislabels as a theme is downloaded, refused after manifest verification and deleted, never run. "Installs or runs
  no downloaded code" would be exact. Owner's call.
- **`reference/ios-bundled-payment-plugins.md`, "What it exposes"** says no new permission or network grant is added.
  Amendment item 7 grants the declared permissions at install with no consent step, including the HMRC sandbox host
  for `tax-uk`, which other channels do not auto-grant. The sentence should say so. (I checked the plugin source: the
  HMRC call only runs on the MTD export with the shop's own credentials set.)
- **Finding 5 above** is not mentioned: the reviewer will meet Download buttons that refuse.

No compliance claim found (`guard-compliance-claims` green; the amendment says so itself). PLACEHOLDERs are flagged
in the banner and the text (demo steps and Stripe test keys, card-payment guideline position, Tap to Pay flag,
purchase surface). Cosmetic: the open-items table uses shorthand labels that do not match the in-text placeholders,
and "versions in lock" has no in-text placeholder; "restored by an update" reads oddly. "Take a card payment with a
Stripe test card" depends on a flow nobody has walked on the build; it sits under the demo-steps placeholder.
The ADR amendment matches the code after the patch (item 4 "keys survive" was not true for the directive path before it).

## Tests run

| Check | Result |
|---|---|
| `gofmt -l internal mobile cmd` | clean |
| `go vet` plugins, pages, data, buildinfo, app; `go vet -tags bundledplugins ./internal/plugins/bundled` | clean |
| `golangci-lint run` plugins, data, buildinfo, pages, app | 0 issues |
| `go test ./internal/buildinfo ./internal/plugins/... ./internal/data` | pass, except `TestReservedPageRoutesCoverLiteralChildNamespaces` (known, #2583) |
| `go test ./internal/pages -run 'Plugin\|Store\|Install\|Update\|Import\|Cashier\|Bundled\|AppStore\|Tax\|Sync\|Satellite\|Replica'` | pass (127 s) on the PR head; pass (119 s) with the patch |
| `gh release download` of the three pinned bundles into a fresh temp dir, `fetch-bundled-plugins.sh --from-dir`, `go test -tags bundledplugins ./internal/plugins/bundled` | hashes equal the lock and the published `.sha256`; pass, also with the patch's two extra real-wasm tests |
| Release assets reachable with no token | 200 for all three |
| `guard-appstore-no-code-fetch.sh` and `_test.sh` (39 cases), `fetch-bundled-plugins_test.sh`, `ios-testflight-workflow_test.sh`, shellcheck | pass |
| `guard-data-access`, `guard-i18n`, `guard-help-topics`, `guard-compliance-claims`, `guard-store-strings`, `guard-ios-tap-to-pay-default-off` | pass |
| `guard-i18n-meta.sh` | fails on `settings.telemetry.help` (baseline, not a key of this PR or the patch) |
| Mutation runs | `/tmp/tabletfix/review-out/mut/results-*.log` |

Not run: `guard-store-private.sh` (BSD awk on this Mac), Xcode / `gomobile bind`, Playwright, `make docs-shots`
(the default build's Plugins page is unchanged, so its screenshots are not stale), any device. PR CI was still
pending when I looked; the new `bundled-plugins` job had not started.

Surfaces looked at: none visually. The Plugins page changes are covered by rendered-HTML and handler tests only; the
"Built in" group, the "Set by the main till" tag and long-locale wrapping have not been seen on an iPad.

## Left in place

- Scratch worktree `/private/tmp/tabletfix/wt-review-apple` on `review/6565-apple2-fixes`, with the real bundles in
  its gitignored embed dir. Remove with `git -C /private/tmp/tabletfix/wt-ut worktree remove`.
- Downloaded bundles `/tmp/tabletfix/rel-review.*`, extracted copies `/tmp/tabletfix/rel-x.*`, run output and the
  mutation script in `/tmp/tabletfix/review-out/`.
