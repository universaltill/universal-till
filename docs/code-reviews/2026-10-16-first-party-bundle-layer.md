# Code review — first-party language-bundle layer + buildinfo.BuildDate (ut-docs#4369)

**Card:** ut-docs#4369 (ADR-0160 §6, task 8). It is split: this PR is the translator half. The language list that installs on select (UI, chip, help topic) is ut-docs#4477.
**Author:** Opus 5.5 (lane:cloud-54). **Reviewer:** Fable, a fresh subagent in an isolated worktree.

## What shipped
- `config.I18n` has two first-party tiers (`fresh`, `stale`) beside the third-party `overlays`. Every reader uses one per-locale order, through `layerLookupLocked`, and the order is documented on `T()`. The readers are `T`, `Entries`, `lookupWithSourceLocked`, `Has` and `Available`. The order is: shop override → fresh first-party → built-in → stale first-party → third-party, then the base-language → fallback → `en` chain.
- `SetPluginLocales(PluginLocales)` replaces all three layers atomically, with one `gen` bump. It drops any plugin string whose printf verbs differ from English: core `en.json` first, else the plugins' own `en.json`. The check runs outside the write lock. `SetOverlays` remains as a third-party-only shorthand.
- `plugins.syncLocales` routes by signed id. `com.universaltill.` (`FirstPartyIDPrefix`) means first-party. Its tier depends on `bundle.json` `generated_at` compared with `buildinfo.BuildDate`, via `buildinfo.NewerThanBuild`. If `generated_at` is missing or bad, the plugin goes to stale, which is where today's de/es/pt packs land. A source build with no `BuildDate` treats every dated bundle as fresh.
- `buildinfo.BuildDate` is the source commit time in RFC 3339. It is stamped wherever `Version` is: Makefile, goreleaser `{{ .CommitDate }}`, release/nightly/iOS workflows, the macOS app script and Android gradle. In CI, `scripts/ci/build-date.sh` produces it and fails rather than stamp an empty value. `guard-builddate-stamped.sh` (with a self-test, wired into `ci.yml`) fails when a `Version` stamp has no `BuildDate` stamp on the same line.
- The `UT_TEST_I18N_OVERLAY_DIR` wrapper merges into the stale tier. It still beats installed packs and still loses to the built-in files.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | `ios-testflight-workflow_test.sh` pins the exact old ldflags string, so CI would have gone red | Fixed: the test expects the new stamp |
| 2 | should-fix | An inline `$(git show …)` failure inside an argument never trips `set -e`. A release would then silently behave like a source build | Fixed: `scripts/ci/build-date.sh` validates RFC 3339 and is assigned on its own line under `set -euo pipefail` (release desktop + mac, nightly, iOS) |
| 3 | should-fix | Gradle `providers.exec` throws when the `git` executable is missing, so `isIgnoreExitValue` doesn't cover it | Fixed: wrapped in `runCatching`; comment corrected |
| 4 | should-fix (perf) | The placeholder regex ran under the write lock and blocked `T()` on every reload | Fixed: the check runs on a snapshot; only the swap takes the lock |
| 5 | nit | The guard doesn't scan `e2e/*.sh` | Accepted: those throwaway tills are source builds on purpose; the guard header now says so |
| 6 | nit | German "100%ig" reads as a `%i` verb | Accepted and documented on the regex. A probe found 0 drops across 10,191 shipped key pairs, and 0 across the real de/es/pt packs (3,397 keys each) |
| 7 | nit | Stale "SetOverlays" comment in `pages/init.go` | Fixed |
| 8 | nit | ADR-0160 §6 says a source build "treats every bundle as newer". The code only treats *dated* bundles that way | Accepted: an undated bundle can't prove it is newer. ut-docs `plugin-architecture.md` §7 states the precise rule |

The reviewer checked these and found no problem:
- **First-party tier security.** Ids come only from installs that passed `VerifyArtifact` + `VerifyManifest`, and `bundle.json` is read from the already-verified directory. ut-cloud reserves the prefix at ingest and review. Only `-tags devplugins` could claim the tier, and nothing here weakens that.
- **Core-neutrality.** One protocol prefix constant; `guard-core-neutral.sh` passes.
- **Callers, locking, shell quoting and the goreleaser template.**

## Verified beyond the unit tests
- **TDD re-verified by the reviewer.** Putting the built-in files before the fresh tier fails `TestPluginLayers_LookupOrder`. Routing every plugin to third-party fails `TestSyncLocales_RoutesFirstPartyBundlesByIDAndBuildDate`. Both pass once restored.
- **`make build`** embeds the HEAD commit time (checked with `strings` on the binary).
- **Tests, guards and lint.**
  - `go test ./...`: every package passes. `internal/plugins` needs more than the default 10-minute `go test` timeout on this container (1,113 s with `-timeout 30m`). That is a pre-existing WASM-heavy suite, not this change.
  - Every guard in `ci.yml`'s build job passes.
  - `golangci-lint`: 0 issues. `shellcheck` is clean on the new scripts.

## Verdict
Safe to merge. Deferred: the UI half is ut-docs#4477.

## Rebase note
- While this PR was waiting, ut-docs#4373 (ICU plurals) merged a `resolveLocked` helper and `TM`, and ut-docs#4374 merged bundle fonts.
- The `internal/config/i18n.go` conflict is resolved so that `resolveLocked`, and therefore both `T` and `TM`, walk each locale through `layerLookupLocked`. `TM` thus gets the same first-party/built-in/third-party order as `T`.
- Re-ran on the rebased head: config, buildinfo, msgfmt, httpx, the pages package, the plugin locale/font tests, `guard-i18n.sh`, `guard-builddate-stamped.sh` and golangci-lint (0 issues). All pass.
