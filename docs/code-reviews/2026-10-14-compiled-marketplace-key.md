# Review: marketplace signing key compiled into the till (ut-docs#4292 part a, ADR-0158)

- **Date:** 2026-10-14
- **Lane:** `lane:cloud-41` (autonomous pipeline)
- **Author model:** Opus 5.5 (dev subagent). **Reviewer:** Fable (independent, fresh context)
- **Branch:** `fix/4292-compiled-marketplace-key`

## What shipped

- `internal/buildinfo.MarketplacePublicKey` is set with `-ldflags -X`. When it is set it is the
  only trusted key: `NewManifestVerifier` uses it whatever the caller passes,
  `config` ignores `UT_MARKETPLACE_PUBLIC_KEY` (and logs a difference), and
  `enroll.Effective`/`Init` never replace it with the persisted or fetched key and never fetch one.
- Fail closed. A verifier with no key refuses every manifest (`ErrNoMarketplaceKey`). The
  importer refuses with a nil verifier. "Import from file" refuses when it has no key, or when
  building the verifier fails; it used to fall back to `nil` = no verification. The new
  translated reason is `plugins.error.import_no_key`.
- `-tags devplugins` (`internal/plugins/devplugins_{on,off}.go`) is the only way to run
  unsigned plugins or honour `skip_signature`. This replaces the dead `cfg.Env == "dev"`
  switch, which nothing ever set.
- CI: `scripts/ci/resolve-marketplace-key.sh` resolves the key per plane. It reads the
  repository variables `UT_MARKETPLACE_PUBLIC_KEY` (the pin) and `UT_MARKETPLACE_SIGNING_KEY_URL`,
  cross-checks the pin against the live `/ui/api/signing-key`, and fails on a mismatch or when
  no valid key is found. Every release-ish build injects the key: goreleaser (all 5 builds),
  the `release.yml` desktop, macOS and Android jobs, `nightly.yml`, `ios-testflight.yml`, gradle,
  `build-app.sh` and the `Makefile`. `release.yml` verification fails when a shipped binary or
  `.so` lacks the key. Its self-test is `resolve-marketplace-key_test.sh`, wired into `ci.yml`.
  `ci.yml` also builds and tests the `devplugins` variant.
- Docs: README, `pos.env.example`, `web/help/{en,de,ar,fa,tr}/plugins.md`, and in ut-docs
  ADR-0158, `independent-operator.md`, `guides/pos.md` and `for-plugin-developers.md`.

## Findings (Fable review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Workflows never passed `UT_MARKETPLACE_SIGNING_KEY_URL`, so a fork that pinned its own key would fail every release on the cross-check against our URL. | **Fixed.** Every resolve step passes `vars.UT_MARKETPLACE_SIGNING_KEY_URL`; the self-test pins it (I removed it from nightly and saw the test go red); ADR and operator doc corrected. |
| 2 | minor | No CI run compiled or tested `-tags devplugins`. | **Fixed.** New step in the `ci.yml` build job. |
| 3 | minor | With no pin, a release compiles whatever the live endpoint serves (warning only). | **Accepted for now.** Making the pin mandatory would block every release until the owner sets the variable. The ADR states it, and the owner ask is on ut-docs#4292. |
| 4 | minor | `nightly.yml` has no post-build presence check. | Accepted. Nightlies are not shipped to shops; the wiring is pinned by the self-test. |
| 5 | minor | The `grep -aqF` presence check is sound only for a high-entropy key. | Accepted. Real keys are random 32 bytes; the reviewer confirmed found/missing on keyed/unkeyed `-trimpath -s -w` builds. Fixtures are never used against binaries. |
| 6 | nit | curl's own error was discarded. | **Fixed.** The reason is now in the "could not fetch" line. |
| 7 | nit | The ADR said a differing env key is logged but didn't mention that a malformed one is dropped. | **Fixed** in the ADR wording. |

Bypass hunt by the reviewer: every verifier construction, install site (`plugin_api.go`,
`plugins_store_page.go`, `cloudsync_wire.go`, replica sync), the importer and every
`PersistManifest` caller checked. The only unverified path left is `builtinlayouts.go`, whose
layouts are embedded in the binary itself, so they share the binary's trust root. That path
predates this change.

## Verification beyond unit tests

- **TDD re-verified by the reviewer.** Reverting the verifier/importer bodies to `main` turns 6
  plugins tests and the pages no-key import test red; restoring them turns them green.
- **Real binary.** A normal build (no key) refuses an unsigned upload with the translated
  message, even with `skip_signature=true`. A `-tags devplugins` build imports it with the
  "Signature verification skipped (devplugins build)" warning.
- `go version -m` on a keyed non-`-trimpath` build shows the `-X …MarketplacePublicKey=` flag;
  `-trimpath` builds omit ldflags, which is why the release check greps the binary.
- e2e: `plugin-settings-phone-overflow-4098`, `sale-only-cashier-3079` and
  `session-expiry-redirect-admin-2157` all pass (13/13).
- Docs-shots: only the `plugins` topic markdown changed (step 6 prose). The /plugins page
  renders no changed pixel (the change is an error path only), so only the four topic hashes in
  `manifest.json` move. Precedent: commit 0266c0f. `Docs-Shots-Unchanged: true`.

## Deferred

- Paid-plugin run-time licence (part (b)): ut-docs#4318, Admin Review, needs the owner's decision.
- Owner: set the repository variable `UT_MARKETPLACE_PUBLIC_KEY` on universal-till to pin the
  production key; after that, the pin can be made mandatory for `release.yml`.
- Language packs de/es/pt need `plugins.error.import_no_key`. Follow-up PRs go out in the same cycle.

## Verdict

Safe to merge once the full gate is green.
