# Review — installed official marketplace plugins show "untrusted" (ut-docs#6549)

Branch `fix/6549-official-trust-tier` (HEAD 1a6e4420). Author model Opus 5.5;
reviewer Sonnet 5.5 (independent, different model).

## What shipped
- `marketplaceTrustTier` maps the catalogue tier official / verified /
  trusted / approved (trimmed, case-insensitive) to `trusted`; everything
  else, including empty, to `untrusted`. Never `system`.
- `listingCatalogTier(d, listingID)` reads the CACHED catalogue snapshot
  only (no network; offline-safe), matching `ListingID` or `ID`; returns ""
  when repo, snapshot or listing is missing, which yields `untrusted`
  (install never fails or blocks on this).
- Passed at all install call sites: `handleInstallFromMarketplace`,
  `applyPluginUpdate`, `cloudInstallPluginVersionChecked`, store
  `InstallFromStore`. `DownloadToStore` does not record a tier (n/a).
- Help `en/plugins.md` trust-tag text and `help/img/manifest.json` hashes;
  ut-docs one-line note in `architecture/plugin-architecture.md`.

## Findings
1. Tier source: comes only from the cloud catalogue cache, never from the
   plugin's own manifest or bundle; the bundle is still Ed25519-verified
   before install (ADR-0006 unchanged). `trust_level` readers that gate
   privilege compare only against `system`. The label is display-only.
   No issue.
2. Missing/stale cache -> `untrusted`, no error path. No issue.
3. nit — `listingCatalogTier` also matches `p.ID == listingID`; a cross-match
   with another plugin's ID is implausible (UUID vs slug). Accepted.
4. nit — plugins installed before the fix keep `untrusted` until next
   update/reinstall; help text says so. Accepted.
5. Removed table test in `misc_coverage_test.go`: justified, it asserted the
   old mapping; superseded by the extended table in `installer_store_test.go`
   (official/trusted/case/whitespace added) plus
   `TestInstallFromStoreOfficialTierStoredTrusted`.
6. Recurring bugs: no file writes added, no cwd-relative paths. No issue.
7. Help text accurate (official/verified -> trusted, unverified -> untrusted,
   system for built-in layouts, imports untrusted).

No code changes made by the reviewer.

## Extra verification
- Tester (notes `ut-docs/.notes-6549-tester.md`): full `go test` of
  `internal/plugins` + `internal/pages` ok, lint/vet/gofmt clean, guards
  green; revert-check: `TestInstallFromStoreOfficialTierStoredTrusted` fails
  without the fix.
- Reviewer: `go build ./...` ok; `go test ./internal/pages/ -run
  'Tier|Trust|Marketplace'` ok; `go test ./internal/plugins/ -run
  'Tier|Trust|Store'` ok; grep of `InstallFromStore`,
  `MarketplaceInstallRequest`, `TrustTier` shows no missed call site.

## Verdict
APPROVE. No blockers or majors.

## Deferred
None.

Pre-flight: till behaviour — help topic `plugins.md` updated, docs-shots
manifest hashes updated; reference/plugin-architecture note updated (ut-docs
branch docs/6549-official-trust-tier); README n/a (no stale text); en.json
keys n/a (none added); review record updated.
