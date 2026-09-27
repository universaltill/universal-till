# Review: additional till no longer writes shop-wide settings from cloud directives or locale derivation (ut-docs#2998)

**Date:** 2026-09-27 · **Lane:** cloud-24 · **Complexity:** hard
**Author:** Opus 5.5 · **Reviewer:** Fable (fresh-context subagent, isolated clone)

## What shipped

On a till that follows a main till (`sync.primary_url` set), four paths wrote
shop-wide keys locally, and the next admin pull (main-till-wins) reverted them.

- `internal/pages/cloudsync_wire.go`: the new `refuseShopWideDirectiveOnAdditionalTill`
  covers cloud `set_setting` and `set_till_setting`. It refuses any key that
  `data.SettingScope` doesn't classify per-till, reusing `requirePrimaryDirective`
  (the catalog directives' refusal). Nothing is written. In `SetSetting` it runs
  first, so a refused `store.country` never reaches `clearFiscalStateForCountryChange`
  (the fiscal posture stays untouched). Per-till keys (`theme`,
  `printer.receipt_policy`) still apply. ut-cloud already withholds both types
  from satellites (ut-docs#2633); this is the till's fail-closed half.
- `internal/pages/setup_base_plugins.go`: `applyDerivedLocaleIfLanguagePackNowAvailable`
  (`store.locale`) and `backfillLocaleConfirmedForDivergedPendingTills`
  (`store.locale_confirmed`) return early on an additional till. The main
  till's values arrive with the pull.
- `internal/data/sync_admin_repo.go`: `setup.pending_base_plugins` is now
  per-till. It is this till's own plugin install-retry queue. Its two
  `settings-write:allow` annotations are gone, and `setup_base_plugins.go`
  left the write guard's file allowlist: every remaining write there is
  annotated or per-till, and the guard passes.
- Annotations reworded in `cloudsync_wire.go`, `fiscal_country_change.go` and
  `setup_base_plugins.go` to say what is now true. A stale comment was fixed in
  `settings_page.go`.
- Docs: ut-docs `architecture/lan-sync.md` lists the key among the per-till families.
- Split out: TSE provisioning writes (`setup_tse.go`). They need a cloud-side
  routing change for `fiscal_tse_ready` → ut-docs#3039.

## Tests (`internal/pages/replica_shop_wide_writes_2998_test.go`)

Five tests fail on `main`; the reviewer re-verified this by reverting the three source files:
- `TestCloudSetSetting_AdditionalTillRefusesShopWideKeys`: `SetSetting(store.name) on an additional till: want a refusal, got nil`. It also asserts that `store.country` leaves the DE posture and the pending queue untouched.
- `TestCloudSetTillSetting_AdditionalTillRefusesShopWideKeys`: covers every whitelisted non-per-till key; `printer.receipt_policy` still applies.
- `TestPendingBasePlugins_IsPerTill`
- `TestResolveAndInstallBasePlugin_AdditionalTillKeepsLocale`: `store.locale = "ur-PK" on an additional till`
- `TestBackfillLocaleConfirmed_AdditionalTillNoOp`

`TestCloudSetSetting_PerTillKeyOnAdditionalTill_ShopWideOnMainTill` guards
the still-allowed paths, so it passes both ways.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `settings_page.go` comment said the queue "is shop-wide and reaches this till with the next pull". | Fixed. The comment now names plugin-set sync (ut-docs#460) as how a replica gets the packs. |
| 2 | should-fix | `architecture/lan-sync.md` per-till list was missing the key; no review record yet. | Fixed: doc updated in ut-docs, and this record added. |
| 3 | nit | An upgraded replica keeps its last synced copy of the main till's queue, because ApplyAdmin never prunes settings. The retry loop drains it idempotently, or it can be dismissed on the Settings card. | Accepted. This is what every pull already did before the fix, and it drains itself. |
| 4 | nit | The refusal text is `requirePrimaryDirective`'s. It goes in a directive result column, not in the UI. | Accepted. |

The reviewer also checked:
- No main-till path breaks. Back-office mode is `display.mode` and doesn't set `sync.primary_url`.
- The refused directive is acked once as `failed` (`cloudsync.apply`), with no retry loop.
- The locale reaches a replica via `publishCachedSettings` after the pull.
- No SQL outside `internal/data`, no new user-facing strings, no file writes or paths.

## Verification

- `gofmt -l` is empty, `go build ./...` passes, `go vet` is clean on pages/data, and `golangci-lint run ./internal/pages/... ./internal/data/...` reports 0 issues.
- `go test ./internal/data/ ./internal/pages/ -count=1` passes; the reviewer ran both full packages too.
- All `ci.yml` build-job guards pass locally except two local-only failures:
  - `deadcode-baseline`: no GTK headers here, so `cmd/unitill-desktop` is skipped. It flags two `internal/logging` functions this diff doesn't touch; the same failure is recorded in the #3032 review.
  - `shellcheck-version`: no shellcheck binary here.
- `guard-docs-shots` passes after a surface-hash refresh. There is no pixel change: the only `internal/pages` edits are backend code and comments.

No user-visible UI, text or help topic changed.

**Verdict:** safe to merge.
