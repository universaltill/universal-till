# Code review: delete unverified `plugins.InstallPlugin` install primitive

**Card:** universaltill/ut-docs#484
**Date:** 2026-08-09
**Complexity:** easy — Dev at Sonnet (inline), Review at a fresh-context Sonnet subagent, per this pipeline's model-routing rule.

## What shipped

`internal/plugins/install.go`'s `InstallPlugin` verified only a
caller-supplied SHA256 checksum, with no Ed25519 manifest-signature check —
in contradiction of this repo's "never run an unverified plugin" rule. Its
last two production call sites (`POST /api/plugins/upload` and
`POST /api/plugins/marketplace/install`) were removed in ut-docs#480, which
left the function itself in place with a `// Deprecated:` comment as a
scoped follow-up. Since then it had zero production callers — only its own
package tests.

This change deletes:

- `InstallPlugin` from `internal/plugins/install.go` (and the now-unused
  `os` import it pulled in).
- Its four direct unit tests in `install_test.go`
  (`TestInstallPlugin_Success`, `_ChecksumMismatch`, `_MissingBinary`,
  `_DefaultTrustLevel`), and the now-unused `os`/`path/filepath` imports.
- Three tests in `integration_test.go` (gated behind the `integration`
  build tag) that used `InstallPlugin` as setup —
  `TestIntegration_PermissionDenialAudit`,
  `TestIntegration_MenuFilteringByPermissions`,
  `TestIntegration_MarketplaceChecksumRejection`. All three were already
  unconditionally skipped via the never-implemented `createTestManifest`
  stub (`t.Skip("createTestManifest not fully implemented")`), so no real
  coverage was lost. Their now-orphaned helpers (`createTestManifest`,
  `createEmptyBinary`, `computeTestChecksum`, `testConfig`, `contains`,
  `findInString`) and unused imports (`path/filepath`,
  `internal/config`) were removed too.

`UninstallPlugin`, `UpdatePluginTrustLevel`, `PersistManifest`,
`ParseManifest`, `ComputeSHA256`, and `InstallOptions` are all untouched and
still have live callers elsewhere (`installer_marketplace.go`,
`importer.go`, `cloudsync_wire.go`). `MarketplaceInstaller.Install` (the
real Ed25519-verified path) remains the only install route.

## Independent review

Run as a fresh-context Sonnet subagent (complexity:easy → same-tier review,
different instance, per the scrum-master skill's model routing).

- **Verdict: safe to merge. No findings.**
- Independently re-ran the repo-wide grep for `InstallPlugin` (26 hits, all
  the unrelated `data.PluginRepo.InstallPlugin` method or `cloudsync`
  hook/wrapper functions) and specifically for the fully-qualified
  `plugins.InstallPlugin` call form (zero hits anywhere).
- Read `cloudInstallPluginVersion`'s body directly and confirmed it already
  routes through `MarketplaceInstaller.Install`, never the deleted function.
- Independently ran `go build ./...`, `go vet ./...`,
  `go build -tags integration ./internal/plugins/...` (confirms the
  integration-tagged file still compiles even though it isn't part of the
  normal test run), and a full `go test -count=1 ./...` — all green.
- Ran `gofmt -l` on all three touched files — clean.
- Ran `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, and `guard-i18n.sh` (extra, not required for
  this change) — all pass.
- Checked the surviving tests (`TestUninstallPlugin`,
  `TestUpdatePluginTrustLevel*`, `TestIntegration_EventDispatchCrashIsolation`,
  `TestIntegration_IPCEventRoundTrip`) set up their own state via
  `PersistManifest` directly and never depended on `InstallPlugin` — nothing
  was accidentally weakened.
- Confirmed via grep that every removed helper had zero remaining
  references anywhere in the package before removal.

## What was verified beyond automated tests

- Repo-wide `grep -rn "InstallPlugin\b"` (both before and after the change,
  independently by Dev and by Reviewer) confirming zero production call
  sites existed prior to deletion — the card's own acceptance criterion.
- Full gate (`go build`, `go vet`, `go test ./...`, the three CI-enforced
  guards) run twice: once by Dev/Tester, once independently by Reviewer.
- `go build -tags integration ./internal/plugins/...` to confirm the
  build-tag-gated integration test file still compiles cleanly, even though
  it's outside the normal (non-integration) test run.

## Safe-to-merge verdict

Yes — clean, correctly-scoped dead-code deletion with a genuinely
independent review that re-derived the "no production caller" claim itself
rather than taking it on faith, and ran the full gate rather than just
reading the diff.

## Explicitly deferred items

None — this card's own non-goals excluded re-litigating the ut-docs#480
endpoint removal, and no new gap was found during review.
