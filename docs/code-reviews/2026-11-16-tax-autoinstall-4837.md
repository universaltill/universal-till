# Review: a new till auto-installs its country's tax-rate plugin (ut-docs#4837, #6285-#6289)

Date: 2026-11-16. Lane: lane:local. Authority: ADR-0172 (Accepted), ADR-0025
decision 4 as amended, ADR-0129, `reference/plugin-manifest.md`, this repo's
`CLAUDE.md`. Money/legal sensitive (a wrong or missing VAT rate on a real till).

Reviewed independently of the Dev and the Tester (Tester record: FAIL at
7884ef6, defects D1/D2/D4 since fixed by the Dev; D3 was a stale branch).

## What shipped

- `universal-till` PR #2559: `internal/pages/tax_autoinstall.go` (eligibility,
  resolve+install, first-load gate), `tax_autoinstall_status.go` (chip, declined
  record, uninstall/dismiss decline), `plugins.DryAskWasm` (unregistered
  sandbox), `PreInstall` hook on the one marketplace install path, markets in
  the catalogue client, wizard tile follows the catalogue (`countryTaxLocale`
  gone), per-till prefix `setup.tax_autoinstall.`, help topic, README.
- `ut-plugin-tax-uk` PR #15 (1.4.0): `markets: [GB]`, `min_pos_version`
  0.30.11, `net:` permission removed, MTD export moved out.
- `ut-plugin-export-mtd-uk` PR #1 (1.0.0): the MTD VAT return bridge, by choice only.

## Pre-flight

Pre-flight: help topic `web/help/en/plugins.md` updated; docs-shots regenerated
(plugins topic only, Linux container); `README.md` updated; `en.json` keys have
`en.meta.json` entries (English only); no new host function or manifest field
(`markets` already existed, ADR-0129); ADR-0172 index already merged; follow-ups
below as cards (never silent).

## Method

Read every non-test line of the three diffs against the ADR; ran
`go build/vet`, golangci-lint, `internal/pages` tax tests, guards
(deadcode-baseline, core-neutral, docs-shots); compared the moved MTD code
file by file with the old tax-uk tree; reproduced the two fixed defects by
reverting the fix and watching the new tests fail (below).

## Findings

| # | Finding | Severity | Status |
|---|---|---|---|
| R1 | CI `build` job (golangci-lint `unused`) failed: test helper `taxListing` in `tax_autoinstall_test.go` was unused | blocker (CI) | fixed |
| R2 | `guard-docs-shots` stale at HEAD (a merge of main left the manifest and the en/plugins topic hash out of step; the surface hash also moves with any `internal/pages` edit) | blocker (CI) | fixed: plugins shots + manifest regenerated in the ut-docshots Linux container |
| R3 | A PROMPTED tax install (operator tapped the wizard tile) used `countryTaxListings`, which keeps listings with NO `markets` (every market) and took the highest semver of them. The tile offers only a listing that names the country (`taxTileFor`), so the operator could consent to plugin A and get a third-party "every market" plugin B, with no §2 rule applied | should-fix (consent integrity, money) | fixed: a prompted spec never picks a listing with empty markets; none left = `errBasePluginNotPublished` (keeps waiting). Test `TestTaxPromptedInstall_NeverPicksNoMarketsListing` fails without the fix |
| R4 | Dismissing the pending row before the plugin id is known records `market:<XX>` as the declined value. The install gate (`PreInstall`) only matched `declined == m.ID`, so a stale or in-flight spec could still install it, contradicting "declined really prevents reinstall" | should-fix | fixed: the automatic path returns at once when ANY declined record exists (same rule `taxPluginForCountry` already used to refuse queueing). Test `TestTaxAutoInstall_MarketOnlyDeclineBlocksStaleSpec` fails without the fix |
| R5 | The dry ask runs host functions against the real DB. A module that calls `storage_*` for a plugin id not yet installed gets a permission denial, and `CheckPermission` writes a denial audit row for an uninstalled plugin id. Nothing else persists (no install row, no subscription, no bus registration; runtime closed) | low | accepted, noted: audit noise only, never a grant. Card: give `DryAskWasm` a denial-audit-free permission mode |
| R6 | A manifest refused after download (class `refused`) stays pending and the retry (5 min) re-downloads it forever | low | accepted: bounded by the interval, honest chip says "we'll keep checking"; card to back off after N refusals of the same version |
| R7 | The installed chip and card say "installed" for a UK plugin whose eat-in mapping is empty, so it changes no rate. The help topic says so; the chip does not. Seeding a mapping is the open legal question ADR-0172 §7 / ut-docs#6290 and must stay unseeded | low, legal copy | deferred to #6290 (copy review with the legal role) |
| R8 | Core `en.json` strings say "Eat-in sales ..." (a UK/hospitality term) in the tax-plugin uninstall/dismiss confirm | nit | accepted (not a country or plugin id; core-neutral guard passes) |
| R9 | A tax-uk 1.3.1 till that used the MTD export keeps its `hmrc_*` settings under the tax-uk id after update; they are not carried to the new plugin (sandbox only, no real users) | low | accepted (the MTD README names the nine `hmrc_*` keys as that plugin's settings, but not a migration step); no card (sandbox only, no real users) |
| R10 | No test drives a guest that loops forever or grows memory through the dry ask; both bounds come from the production wazero config (2 s event deadline with close-on-context-done, 64 MiB page cap), which `DryAskWasm` shares | test gap | accepted, noted; trap, malformed and bad-rate guests are covered |

## What was checked and held (adversarial list)

1. Security. `PreInstall` runs inside `installBundleFile` AFTER bundle checksum,
   Ed25519 manifest verification, signature/hash match, compatibility and the
   wasm-module check, BEFORE the live directory or any row is touched. There is
   one install path (`cloudInstallPluginVersionChecked`); the gate only adds a
   refusal. Eligibility is checked twice: on the listing (official, free, names
   the country, arch, allow-list) and again on the VERIFIED manifest (id prefix
   `com.universaltill.`, canonical_type tax, explicit markets naming the country,
   no `fiscal.` hook/entry/provides, permissions only `events:receive`,
   `sales:read`, `storage`, so no `net:` and no `*_register_*`). Exactly one
   eligible listing or nothing installs. Country comes from `st.Country` after
   `applyCloudShop` overwrote the posted field with the pair response's
   (existing mechanism, test posts a tampered country and it is ignored).
   Uninstall is `plugin_management` (manager/admin), cashier gets 403 and
   nothing changes; the declined record blocks the retry (mutation checked by
   the Tester, re-checked above for the market-only form). The chip endpoint is
   a `settings`-permission read and is in the poll allow-list.
2. Failure never blocks checkout or yields wrong tax. `tax_hook.go` is
   untouched; while a spec is pending no plugin is registered, the ask
   declines, the item's own rate applies and tender is not blocked (test).
   An empty mapping declines (`handleTaxRateAsk`, default `{}`). Nothing seeds a
   tax rate or an eat-in mapping: grep of added non-test lines is clean; the
   only literal rate is the dry-ask probe (2000 bp, never persisted, never a sale).
3. Core-neutral, English-only, logical CSS in the new partial, offline-first
   (wizard never waits on the catalogue beyond the cached 5 s bound; setup
   finishes with the spec pending), retry loop (old persisted specs: a legacy
   locale-only tax spec has `Market == ""` and keeps the old locale path;
   `Prompted`/`Market` are `omitempty`), per-till prefix registered.
   Leftovers: `countryTaxLocale`, `setupInstallableTaxPlugin` singular call
   sites, `resolveCountryTaxPlugin`, `dismissPendingBasePlugin` and the DE
   allow-list line are gone (grep clean; remaining hits are the function name
   `setupInstallableTaxPlugin`, which is the per-country resolver, still used).
4. Plugin split. tax-uk 1.4.0 manifest is eligible (the repo's own
   `eligibility_test.go` and `validate.sh` pin it, and the Tester ran the real
   manifest through `taxAutoInstallEligible`: GB eligible, DE refused). 0.30.11
   is justified: it is the first till tag carrying manifest `markets` (an older
   till drops the field and the signed-manifest check fails closed); the old
   `min_pos_version` of 1.0.0 was higher than any shipped till. The MTD export
   is byte-identical to the old `hmrcvat` code apart from vendor name/version and
   log prefixes; its tests moved with it; the accountant CSV stays in tax-uk
   (`vatfigures`). `charge.policy.ask` is informational only and changes no charge.
5. Test quality. The fixture guests are real compiled wasip1 modules run through
   the real installer, not mocks; refusal tests assert nothing on disk and not
   active. The two new tests were verified by reverting the fix (both FAIL with
   the messages "a declined automatic tax install must never run ..." and
   `errBasePluginNotPublished` missing), restoring, both PASS.

## Verification

- `go build ./...`, `go vet ./...`, `golangci-lint run ./internal/pages/...`
  (0 issues), `internal/pages` tax/setup/base-plugin tests pass,
  `guard-deadcode-baseline` and `guard-core-neutral` pass.
- docs-shots: regenerated in the ut-docshots Linux container; plugins topic
  PNGs and `manifest.json` committed, other PNGs left as on main.

## Verdict

PR #2559: approve with the fixes above (R1-R4 done). ut-plugin-tax-uk #15:
approve. ut-plugin-export-mtd-uk #1: approve. Merge only on green CI; no
rebase (owner 2026-11-13); merge method `merge`.

## Deferred

- R5 audit-free dry-ask permission mode; R6 refusal back-off; R7 chip copy and
  the §7 mapping question (#6290); R10 loop/memory guest test.
