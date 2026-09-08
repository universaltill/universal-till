# 2026-09-08 — Test fixture: language-pack plugin install-flow gap (ut-docs#1111)

## What shipped

Test-infrastructure only — zero production code changed.

- `internal/pages/sync_plugins_test.go`: a new `signedFakeMktArtifactWithLocale`
  helper (packs `manifest.json` + `locales/<locale>.json` into a signed
  `.tar.gz`, no executable — asset-only) and a new
  `(m *fakeMarketplace) publishLanguageVersion` method, mirroring the
  existing `signedFakeMktArtifactWithBinary`/`publishVersion` pattern but
  with a real `CanonicalType:"language"`, `Runtime:"none"` manifest and a
  real locale overlay inside the artifact.
- `internal/pages/setup_base_plugins_test.go`: a new test,
  `TestResolveAndInstallBasePlugin_LanguagePackLocaleRendersAfterInstall`,
  that installs the new fixture through the real `resolveAndInstallBasePlugin`
  path and asserts a real `config.I18n.T()` call renders the plugin-shipped
  string afterward.

## The gap this closes

Every existing install-flow test that installs a `CanonicalType:"language"`
plugin through the marketplace-download path used `newFakeMarketplace`'s
`publishVersion`, which always hardcodes `CanonicalType:"page"` and ships no
locale files — even `TestSetupLanguageInstallHappyPath` and
`TestResolveAndInstallBasePlugin_DEHappyPath`, whose catalog *entries*
claimed `CanonicalType:"language"` (ut-docs#1055's wire-shape fix never
touched the installed artifact itself). So nothing ever proved a language
pack installed through the real download path actually changes what gets
rendered — every such test only asserted `PluginActive`/the `ut_lang`
cookie. `Manager.syncLocales` itself already had direct unit coverage
(`manager_test.go`'s `TestSetLocalizerSyncsPluginLocales`), but that test
writes locale files straight to disk, bypassing download/verify/extract
entirely.

The new test drives the whole real chain: marketplace download → Ed25519
signature verification → tar.gz extraction → `Manager.Reload` →
`syncLocales` → `Localizer.SetOverlays` → `config.I18n.T()` — and asserts
on the translator's actual output, not a proxy for it.

## Independent review

Reviewed by a fresh-context Sonnet subagent (per this card's
`complexity:easy` label — MODEL-ROUTING.md), isolated in its own git
worktree. Findings: **none blocking.**

Non-blocking observations, both accepted as-is:
- `signedFakeMktArtifactWithLocale` is near-identical to
  `signedFakeMktArtifactWithBinary` (only the packed-file list differs).
  Consistent with this file's own established pattern — several
  near-identical `publishXVersion`/`signedFakeMktArtifactWithY` variants
  already exist side by side. A shared tar-packing helper is a plausible
  future cleanup, not warranted here.
- The test key `nav.home` lives entirely inside the test's own in-memory
  `fstest.MapFS` — no coupling to or risk of colliding with the real
  `web/locales/en.json`.

## Verified beyond automated tests

- **Read the full production chain** this test exercises end-to-end and
  confirmed it matches the test's assumptions: `resolveAndInstallBasePlugin`
  → `cloudInstallPluginVersion` → `d.ReloadPlugins` (nil-safe on `d.Pm`) →
  `Manager.Reload` → `syncLocales` → `Localizer.SetOverlays`.
- **Confirmed `extractMarketplaceTarGz`** extracts arbitrary regular-file
  tar entries generically (creating parent directories as needed), not
  just `manifest.json` plus a named executable — so a `locales/de.json`
  entry with no directory entry of its own extracts correctly to
  `paths.Plugins()/<id>/<version>/locales/de.json`, exactly where
  `syncLocales` reads from.
- **Confirmed `Runtime:"none"` manifests need no `Entrypoint`/`Executable`**
  in both `manifest.go`'s validation and `manifest_verifier.go`'s
  verification, matching the existing `signedAssetOnlyManifest` precedent
  (`plugins_store_api_test.go`, `CanonicalType:"theme"`).
- **Traced `config.I18n.T`'s resolution order** to confirm the pre-install
  sanity assertion (`i18n.T("de","nav.home")` must not already equal the
  German fixture string) is real and not vacuous, and that the post-install
  assertion is satisfied by the overlay specifically, not by an
  incidental fallback collision.
- **TDD claim independently re-verified twice** — once by the implementing
  session (stripped the fixture's locale content, confirmed the test fails
  with a clear message, restored it, confirmed it passes again), and once
  more, independently, by the review subagent via the same mutation-test
  technique in its own isolated worktree, reproducing the same failure
  message before restoring.
- Full gate green: `go build ./...`, `gofmt -l .` (clean), `go vet` (both
  touched packages), full-repo `go test ./...` (every package, no
  regressions), full-repo `golangci-lint run ./...` (0 issues),
  `scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh`,
  `scripts/ci/guard-e2e-fixtures-import.sh` — all pass. The new test also
  passes under `-race`.

## Safe to merge

Yes. No production code changed; the new fixture and test only add
coverage for a real, previously-unverified path. No UI/user-facing surface
touched (no manual/help-topic update needed).

## Explicitly deferred

- Factoring `signedFakeMktArtifactWithBinary`/`signedFakeMktArtifactWithLocale`'s
  shared tar-packing boilerplate into one helper — noted above, not scoped
  to this card.
