# Marketplace install path locale catch-up — ut-docs#1893

## What shipped

`ut-docs#1074` added a locale catch-up (`applyDerivedLocaleIfLanguagePackNowAvailable`
in `internal/pages/setup_base_plugins.go`): once a "language" canonical-type
plugin finishes installing, if it matches the shop's country-default locale
and the operator never explicitly confirmed a different one
(`common.KeyLocaleConfirmed`), the shop's `store.locale` switches to the
country default. It was wired into the three install paths that funnel
through `resolveAndInstallBasePlugin` (setup wizard synchronous install,
background retry, wizard step-1 catalog tile) — but not into the fourth: the
marketplace Plugins-store page post-setup
(`plugin_api.go`'s `handleInstallFromMarketplace`, the real
`POST /api/plugins/install-from-marketplace` handler), which installs
directly by listing ID and never touches `resolveAndInstallBasePlugin`. An
operator installing a deferred RTL/local pack from the store page got no
locale switch.

This change:

- Adds `languagePackLocalesForListing` (`setup_base_plugins.go`), which
  queries the marketplace catalog (`Capability: []string{"language"}`) to
  recover a just-installed listing's `AvailableLocales` — the store-page
  install path only has a raw listing ID, unlike the other three paths
  which start from a `basePluginSpec` that already carries a locale.
- Calls it from `handleInstallFromMarketplace`, **after** `d.ReloadPlugins`
  (that's what wires the pack's locale files into
  `httpx.AvailableLocales()`, which `localeSafeToPreset` depends on), then
  loops the returned locales through the unchanged, existing
  `applyDerivedLocaleIfLanguagePackNowAvailable`. Best-effort, mirroring
  the existing `reconcileTaxDeTakeawayOverridesIfActivated` (`ut-docs#1370`)
  call right above it: a catalog hiccup here never fails or hangs the
  install response.
- Two new tests in `setup_base_plugins_test.go`, both driving the real HTTP
  handler via `registerPluginAPI`/`httptest`:
  - `TestHandleInstallFromMarketplace_AppliesLocaleCatchUpForLanguagePack`
    — mirrors the existing
    `TestResolveAndInstallBasePlugin_AppliesCountryLocaleOnceRTLPackInstalled`
    through this fourth install path.
  - `TestHandleInstallFromMarketplace_LocaleCatchUpFindsListingBeyondFirstCatalogPage`
    — added after the independent review below found a pagination gap.

## Independent review

Fresh-context Sonnet subagent (complexity:easy routing), read cold, in an
isolated environment. Ran `go build ./...`, `go vet ./...`, `gofmt -l`,
targeted tests, and the full `go test ./...` (all green). Independently
re-verified the TDD claim by reverting only `plugin_api.go` to its pre-fix
content, confirming the new test failed for the right reason
(`store.locale = "en"`, not `"ur-PK"`), then restoring the fix and
confirming it passed again.

**Finding (should-fix, fixed):** `languagePackLocalesForListing`'s catalog
fetch took only page 1 of the marketplace catalog and never followed
`NextPageToken`. `setup_language_catalog.go`'s own browse fetch already had
to learn this the hard way (`ut-docs#1108`) — the real ut-cloud catalog
paginates at ~20 listings/page. This diff reintroduced the same
single-page gap in a new, broader (all-"language"-listings) fetch: once the
catalog grows past one page, a listing sitting on a later page would
silently get no locale catch-up (fails silent — `ok=false`, no error
surfaced), i.e. ut-docs#1893 could recur for any catalog beyond page 1.
**Fixed**: `languagePackLocalesForListing` now follows `NextPageToken` the
same way `setup_language_catalog.go` does, bounded by the same
`setupLanguageCatalogMaxPages` cap. Added
`TestHandleInstallFromMarketplace_LocaleCatchUpFindsListingBeyondFirstCatalogPage`
to lock this in (forces the installed listing onto catalog page 2 with an
unrelated filler listing on page 1, page size 1).

**Nice-to-have (not fixed, noted):** the catalog lookup adds one
synchronous network round trip to every marketplace install response, not
just language-pack ones — bounded by the marketplace client's own request
timeout so it can't hang the response, but installing on `result`'s
canonical type first (skipping the lookup entirely for non-language
installs) would save the round trip. Left as-is: minor, and the existing
`reconcileTaxDeTakeawayOverridesIfActivated` call right above it has the
same unconditional-lookup shape, so this matches established precedent in
this handler rather than deviating from it.

Other checks the review ran, all clear: listing-ID/ListingID matching is
safe (the two fields always converge per `PluginSummary.UnmarshalJSON`,
confirmed by `catalog_contract_crossrepo_test.go`); the
`KeyLocaleConfirmed` guard is untouched and still enforced (existing
`TestResolveAndInstallBasePlugin_DoesNotOverrideExplicitlyConfirmedLocale`
covers the shared function); best-effort error handling never fails/hangs
the install response; no new concurrency risk (handler already
primary-only-gated); looping over all `AvailableLocales` is safe because
`applyDerivedLocaleIfLanguagePackNowAvailable`'s own guards make a
mismatched/repeated call a no-op. No raw SQL, no money handling, no new
i18n keys/UI strings, no file writes, no path handling touched — none of
`universal-till/CLAUDE.md`'s enforced guards are implicated.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` on all three changed files —
  clean.
- Full `go test ./...` — all packages green.
- `guard-i18n.sh`, `guard-data-access.sh` — clean (no new i18n keys or
  inline SQL in this diff).
- Independent revert-then-restore TDD re-verification (above).

**Not verified:** real hardware / a live marketplace server — this change
is exercised entirely against the repo's own fake-marketplace test harness,
same limitation every other marketplace-install test in this package
already carries.

## Safe-to-merge verdict

Yes. The should-fix finding was fixed and locked in with a regression test
before merge; nothing deferred is a correctness or safety concern.
