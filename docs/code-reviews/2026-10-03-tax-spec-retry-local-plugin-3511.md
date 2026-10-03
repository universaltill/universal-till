# Code review — queued tax spec satisfied by a local tax plugin (ut-docs#3511)

- **Date:** 2026-10-03
- **Branch:** `fix/3511-tax-spec-retry-local-plugin`
- **Card:** universaltill/ut-docs#3511 (complexity:easy) — found in the
  review of ut-docs#3211 (`2026-10-02-offline-tax-tile-installed-3211.md`,
  finding 2).
- **Author:** Sonnet (Dev subagent) + orchestrator fixes; **reviewer:** Opus
  5.5, fresh context, separate worktree.

## What shipped

- `internal/pages/setup_base_plugins.go`: new
  `taxSpecSatisfiedLocally(ctx, d, spec) (bool, error)`, checked first in
  `resolveAndInstallBasePlugin`, before any marketplace client or catalog
  call. For a `tax` spec it asks
  `PluginRepo.HasActiveEntryTypeForMarket(ctx, "tax", country)` for every
  country whose `countryTaxLocale` value is the spec's locale (the existing
  mapping — no plugin ids, no new country list, #2848). Satisfied → `nil`,
  so `basePluginRetryTick` drops the spec. Check error → returned, so the
  spec stays pending.
- Tests (`internal/pages/setup_tax_catalog_test.go`):
  - `TestBasePluginRetryTick_TaxSpecDroppedWhenLocalTaxPluginActive`: local
    plugin with no markets / with `DE`; a reachable catalog that does publish
    a DE tax listing. Asserts that the spec is dropped, that
    `ut-plugin-tax-de` is not installed, and that there were no catalog or
    download-token hits.
  - `…StaysPendingWhenLocalTaxPluginIsForOtherMarket`: an `FR`-only plugin
    does not satisfy a `de` spec.
  - `…StaysPendingWhenLocalCheckErrors`: `plugin_markets` is dropped, so the
    spec stays and there is no catalog browse and no install.
- `web/help/{en,de,ar,fa,tr}/users.md` step 8: one sentence saying that the
  background retry stops and the reminder goes away once a tax plugin
  for the country arrives by file or restored backup.
- `CHANGELOG.md` Fixed entry.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (test quality) | `catalogHitsFor("tax")` could never fail: `resolveAndInstallBasePlugin` browses with no capability, so hits land under `""`. The reviewer confirmed it with the fix reverted (`tax=0 empty=1`). | **Fixed**: the test asserts `catalogHitsFor("")` and `downloadTokenHits()` are 0. |
| 2 | minor | On a DB error the check failed open (returned false), so the tick went on to browse and install. That is an unattended second fiscal install, the very bug, and the doc comment claimed "stays pending". | **Fixed**: the helper returns the error, so `resolveAndInstallBasePlugin` returns it and the spec stays pending. A new regression test covers it. |
| 3 | minor (latent) | The match is on locale, not the till's country: if AT/CH ever map to `de`, an AT-only plugin would satisfy a DE spec. Today the map is DE→de only, which is identical to #3211's check. | **Accepted**: documented in the helper's comment. |
| 4 | nit | WIP commit messages, missing review record and CHANGELOG entry. | **Fixed** (this record, CHANGELOG, squashed commit). |

The reviewer also checked the other callers. `POST /api/setup/tax-plugin`
already returns before this path through `setupInstallableTaxPlugin`.
`installBasePluginsForSetup` never queues tax. Language specs return at once
with no query. The Settings chip and dismiss only read the list. No
behaviour change for any of them. No file writes and no paths are involved.

## Verified beyond automated tests

- **TDD, re-verified twice.**
  - With the fix reverted, the reviewer saw both `…DroppedWhenLocalTaxPluginActive`
    subtests fail ("a second fiscal plugin must not be installed over the
    local one").
  - With the error path changed back to fail-open, the orchestrator saw
    `…StaysPendingWhenLocalCheckErrors` fail ("catalog was browsed 1
    time(s) after the local check failed").
  - Both tests pass with the fix restored.
- **Gate:** `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt`,
  golangci-lint, and the data-access, core-neutral, help-drift, help-topics,
  compliance-claims and competitor-naming guards.
- **No visual surface changed.** The Settings chip simply stops rendering once
  the list is empty, and that template is unchanged. So no screenshots.

## Verdict

Safe to merge.
