# Review: drop migration 001's seeded "My Store" shop name (ut-docs#4273)

- **Date:** 2026-10-15
- **Card:** universaltill/ut-docs#4273 (rescoped. The admin-login name prompt is split to #4391.)
- **Branch:** `feat/4273-clear-seeded-store-name`
- **Author:** Opus 5.5, inline, lane `lane:cloud-41`
- **Reviewer:** independent Sonnet subagent in its own worktree. The first review attempt on Fable was refused (out of usage credits), so Sonnet stood in (MODEL-ROUTING.md). It is still a different model from the author.

## What shipped

Owner rule on #4146: no default shop name. Migration 001 seeds `store.name = 'My Store'`.

- `internal/db/migrations/075_clear_seeded_store_name.sql` blanks that exact seed on every till, fresh or installed.
  - It is data only and matches the exact string, so a name a person typed is never touched, even one that differs only in case or spacing.
  - The row is blanked, not deleted, because `shop_name_notice.go` shows "Name your shop" only for a stored key.
  - Its checksum is pinned in `shipped_migrations_test.go`.
- `internal/settings/runtime.go` `SaveRuntimeConfig` no longer writes a placeholder `store.name` (`config.IsPlaceholderStoreName`).
  - `app.Run` runs Load then Save on every boot. Load ignores a blank `store.name`, so `cfg.StoreName` stayed the `UT_STORE_NAME` default "My Store", and Save wrote it straight back. Without this fix the migration would have been undone on the next boot.
  - A real `UT_STORE_NAME` is still adopted when the shop has no name, as before.
- Tests:
  - `migration_075_clear_seeded_store_name_test.go`: the exact seed is cleared, every other value is kept, other keys are untouched, and a fresh install ends blank.
  - `runtime_store_name_4273_test.go`: the boot round-trip keeps a placeholder blank, keeps a real stored name, and still saves a real env name.
  - `setup_tax_catalog_test.go`: the precondition now expects a blank name (review finding 1).

Out of scope, carried by #4391: the admin-login prompt for the shop and till names, the "Till 1" fallback, and the `display.md` wording.

## Readers of a blank `store.name` (checked by author and reviewer)

- `storeNameOrDefault` (receipts, reports, EOD, receipt designer, joiner `shop_name`) gives "this shop".
- mDNS `advertisedName` gives "this shop (abcd)".
- These are fine: `seedRegisterName`, `shopContextView`, `kioskShopName` (falls back to `cfg.StoreName`, unchanged), the `cloudsync_wire` rename and `enroll` (both placeholder-checked), the settings card (empty field), and `shop_name_notice` (still shows).
- No fiscal or DSFinV-K export reads `store.name`.
- The only other writers (`setup_page.go`, `auth_page.go`, `settings_page.go`) refuse placeholders via `NormalizeStoreName`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `TestSetupTaxPluginInstallUnnamedShopQueuesInsteadOfInstalling` asserted the precondition `store.name == "My Store"`, which is now blank. CI would have gone red. My first full `go test` run missed it because its output was piped through `tail`. | **Fixed.** The precondition expects blank, and its comment and the one in `setup_tax_catalog.go` are updated. The behaviour under test is unchanged (`enroll.AwaitingShopName` treats blank as unnamed). |
| 2 | minor | Receipt and EOD headers of an unnamed shop now print "this shop" instead of "My Store". Both are untranslated English placeholders. The wizard requires a name, so only a till that skipped it is affected. | **Accepted**, and recorded on #4391, which also covers the kiosk header's `cfg.StoreName` fallback. |
| 3 | minor | A replica paired to a not-yet-upgraded main till receives "My Store" again through the shop-wide `store.` settings bundle. | **Accepted.** Every reader still treats "My Store" as a placeholder, and the blank arrives once the main till upgrades. |
| 4 | nit | Is the "Its keys are written in one transaction" comment still accurate? | Accurate. The key is dropped before the transaction opens. |
| 5 | nit | `LoadRuntimeConfig` adopts a whitespace-only stored name into `cfg`. | Pre-existing. `Save` now skips it, so no change is needed. |

## Verified beyond the unit tests

- **TDD:**
  - The migration tests and the boot round-trip tests were seen failing before the fix (`no migration with version 75`, `store.name after boot = "My Store"`).
  - The reviewer reverted only `runtime.go` in its worktree and saw `BootRoundTrip` fail again.
- **Driven run on real binaries** (`UT_AUTH=off`, a throwaway data dir):
  - Boot the `origin/main` build: `store.name = 'My Store'`, schema 74.
  - Boot this branch: `''`, schema 75.
  - Boot this branch again: still `''`.
  - `/ui/shop-name-notice` showed the banner at every step.
  - A fresh install ends `''` at 75. With `UT_STORE_NAME="Env Bakery"` it ends with that name.
- **e2e:** store-name-3115, settings-refusal-reason-2982, login (first-boot wizard), registration-card-paid-4285, diagnostic-mode-indicator-2169, subscription-panel-4286 and settings-two-pane-1960 all pass (40/40).
- **Gate:**
  - `gofmt`, `go build`, `go vet` and `golangci-lint` (0 issues) are clean.
  - Every guard in ci.yml's build job passes, apart from three that can't run in this container: deadcode (its tool binary is built with go1.26), shellcheck-version (no shellcheck installed) and `retry-with-backoff.sh` (not a guard). The diff touches no shell or Go-version surface.
- **No visual surface changed.** The banner and settings card render as before. Nothing was screenshotted.

## Verdict

Safe to merge once the full suite is green with finding 1 fixed.
