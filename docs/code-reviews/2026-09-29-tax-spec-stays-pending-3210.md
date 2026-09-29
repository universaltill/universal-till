# Code review — consented tax plugin stays pending when the catalog has no listing (ut-docs#3210)

- **Date:** 2026-09-29
- **Branch:** `fix/3210-tax-spec-stays-pending`
- **Author:** pipeline build lane (lane:cloud-54), Opus 5.5
- **Reviewer:** independent subagent, Fable (different model from the author)

## What shipped

Found as finding 1 of the #1512 review. The operator's consent to the fiscal plugin (offline tile, Skip or Next) puts `{tax, de}` on the #591 pending list. When the background retry then reached a catalog with no DE tax listing (a staging endpoint, or an unpublished listing), `resolveAndInstallBasePlugin` returned nil for "nothing published" and `basePluginRetryTick` removed the entry. The pending list, the Settings chip and the tile all disappeared, leaving a German till with no fiscal plugin and no warning.

- `internal/pages/setup_base_plugins.go`: when there is no listing and `spec.CanonicalType == "tax"`, `resolveAndInstallBasePlugin` now returns the new sentinel `errBasePluginNotPublished`, so the retry tick keeps the spec pending. Language specs keep the silent nil no-op. `"tax"` is an ADR-0002 canonical type, not a country or vendor, and `guard-core-neutral` passes.
- New test `TestBasePluginRetryTick_TaxSpecStaysPendingWhenCatalogHasNoTaxListing`. With a reachable, empty catalog, the tax spec stays pending, the language spec is dropped, and the Settings chip views show one tax/DE entry. Once the listing is published, the same tick installs it through the real Ed25519 path and clears the entry.

## Caller audit (reviewer)

- `installBasePluginsForSetup` and `setupLanguageInstallHandler` only ever pass language specs, so they are unaffected.
- `basePluginRetryTick` gets the intended change. It logs one Info line per pending spec every 5 minutes, the same cadence as today's offline case.
- `setupTaxPluginInstallHandler`: a race where the tile saw a listing and the live fetch finds none used to install nothing, queue nothing and show no note. It now joins the pending list and shows the pending note, which is more consistent than before.
- Tax specs are only written with consent inside the first-boot window. `setup.pending_base_plugins` is per-till, so a replica never inherits one. An operator can dismiss the chip from Settings, so it can't get stuck.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The chip copy `setup.base_plugins.pending_tax` ("…in the background — this can take a few minutes if you're offline") is untrue for an online till whose catalog has no listing. | Follow-up **ut-docs#3243**. Fixing it needs a persisted pending reason and new locale keys plus pack PRs. The card's AC only asks for the chip to stay visible. |
| 2 | minor | The wizard's step-3 tile still vanishes with a reachable catalog and no listing, because the tile is rendered only from a catalog match. | Follow-up **ut-docs#3244**. The Settings chip is the remaining signal, as the AC asks. |
| 3 | nit | There is a lost-update race between `dismissPendingBasePlugin` and a concurrent retry-tick removal. | Pre-existing, and this change does not widen it. Accepted. |
| 4 | nit | The sentinel is never matched with `errors.Is`. | Accepted: it documents the "not published" case and shows up in logs. |

## Verification

- TDD: the test failed on the pre-fix code (`pending after the tick = []`) and passed with the fix. The author checked this by reverting the fix and restoring it. The reviewer confirmed it by reading the old code path.
- `gofmt` clean, `go build ./...`, `go vet ./internal/pages/`, `golangci-lint` 0 issues, and full `go test ./...` passed.
- Guards run: data-access, core-neutral, kiosk-engine, page-http-error, i18n, compliance-claims, competitor-naming, help-topics, no-showmodal and plugin-menu-read all passed.
- No UI, locale or help change, so no screenshots and no e2e surface. No help topic described the old drop behaviour.

## Verdict

Safe to merge.
