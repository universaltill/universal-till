# Review: tax auto-install, listing-level and "exactly one" tests (ut-docs#6285)

Date: 2026-11-22. Lane `lane:cloud-41`. Test-only change: no production code changed.

## What shipped

ADR-0172 §2 slice A shipped in `7884ef6a` (part of ut-docs#4837). This change closes the AC gaps it left:

- `TestTaxListingEligible_Table` (`internal/pages/tax_autoinstall_test.go`) is a table over `taxListingEligible` / `listingArchOK`. Cases:
  - markets empty, or naming another country only; country matched case-insensitively;
  - trust tier verified / community / empty (refused), and padded `Official` (eligible);
  - a paid listing;
  - a foreign architecture (refused); `any`, ` ANY `, `GOOS/GOARCH` and `GOARCH` (eligible);
  - permissions `net:`, `fiscal_register_de:read` and an unknown permission.
- `TestSetupGBJoin_TwoEligibleTaxListingsInstallNothing` (`internal/pages/setup_tax_autoinstall_test.go`) covers two eligible listings on the automatic join path. It checks that nothing is installed, nothing is downloaded, no chip shows and nothing is left pending.

## Review

- Dev: Sonnet. Independent review: Opus 5.5, fresh context, in a separate worktree.
- Verdict: approve after fixes. Both fixes are in.
  - **F1 (important, fixed).** The installer's `len(eligible) != 1` rule (`tax_autoinstall.go:264`) had no test. A mutant that changed it to `== 0` survived every Tax/Setup/Wizard test, because `TestWizardTaxStepFollowsTheCatalog` covers only the wizard tile's count. Fixed by the new two-eligible join test.
  - **F2 (nit, fixed).** The arch normalisation (ToLower/TrimSpace) was untested. Fixed by the ` ANY ` row.
- Checked and not a defect:
  - The `com.universaltill.` id check and the `fiscal.*` hooks/provides checks are manifest-level. They are covered by `TestTaxAutoInstallEligible_Manifest` and `TestTaxAutoInstall_RefusesFiscalOrNetworkManifestAfterDownload`.
  - `min_host_version` is dropped upstream by `marketplace.ListPlugins`.

## Verification

- Mutants, each one mutate, run, restore:
  - Dev: 12 mutants on `taxListingEligible` and `listingArchOK`, plus M6 (arch normalisation) and M8 (`!= 1` → `== 0`). All fail.
  - Reviewer: 8 mutants re-run independently. All are killed except M6 and M8, which led to F2 and F1.
  - Orchestrator: re-ran M8 inline. It fails at `setup_tax_autoinstall_test.go:111` and passes once restored.
- Gates:
  - `gofmt -l internal/pages` (empty) and `go vet ./internal/pages` pass.
  - `go test ./internal/pages -run 'Tax|SetupGB|Wizard'` passes.
  - `guard-core-neutral.sh` and `guard-fixed-dates.sh` pass.
  - The full suite runs in CI.

Pre-flight: help topic n/a (no user-visible change); en.json n/a; README n/a; reference/ n/a (ADR-0172 unchanged); review record updated (this file); follow-ups none.
