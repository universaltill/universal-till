# Review: Portuguese (pt-PT) language pack registration (ut-docs#2964)

**Date:** 2026-09-28 · **Branch:** `feat/2964-pt-language-pack` · **Built by:** Opus 5.5 (orchestrator, inline) · **Reviewed by:** Fable (independent subagent)

## What shipped
Portugal (`PT`, default locale `pt-PT`) had no Portuguese UI. The pack itself is the new repo `ut-plugin-language-pt` (its own review record). Core changes:

- **`basePluginsForCountry`** replaces the hard-coded `setupBasePlugins` table (`{"DE": de, "ES": es}`). A country's base language pack is now derived from its `country_settings.DefaultLocale`: the base language subtag, unless core bundles `web/locales/<lang>.json` (en/ar/fa/tr) or the subtag isn't 2–3 ASCII letters. Adding `"PT"` to the table would have been a new core-neutral offender (ADR-0121 §11, shrink-only allow-list). The two `setup_base_plugins.go` allow-list entries are gone. DE→de and ES→es resolve as before; PT→pt is new.
- **`resolveAndInstallBasePlugin`** now looks the pack up with `enroll.Effective`, not `EnsureRegistered`. The install itself (`cloudInstallPluginVersion`) still registers, so ADR-0015's trigger is unchanged (review finding 1).
- **`numberSeparators`**: `pt` joins `fr` (space thousands, comma decimal; CLDR pt-PT).
- `check-lang-pack-drift.sh` (+ test, new "only pt drifts" case) and `audit-nav-i18n-parity.sh` include the pt pack. README and `docs/arch/plugin-integration-roadmap.md` list it. Stale `setupBasePlugins` comments updated.
- No new locale keys, so no de/es pack follow-up.

## Findings (Fable)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Countries with no pack (FR, IT, NL, PK…) now queue a spec, and `resolveAndInstallBasePlugin` called `EnsureRegistered` before checking the catalog, so those tills registered with the cloud at setup (ADR-0015) | Fixed: `enroll.Effective` for the lookup. `TestResolveAndInstallBasePlugin_NoListingDoesNotRegisterStore` fails on the old code (`1 POST /v1/stores/register`) and passes now |
| 2 | major | The pack's first PR fails its own `version-bump` job (no manifest at the base) | Fixed in the pack repo (see its record) |
| 3 | major (process) | Core's `lang-pack-drift` on `main` goes red if core merges before the pt repo exists | Merge order: pt repo created and its PR merged first, then core. Recorded on the card |
| 4 | minor | Offline FR/IT/NL/PK tills show the "installing your free language pack" chip until the catalog is reachable; setup makes one bounded (5 s) catalog attempt for those countries | Accepted: the chip clears on the first online attempt (no listing → spec dropped, verified by the reviewer); the wait is the existing bound |
| 5–8 | major/minor | Translation terminology and wording | Fixed in the pack |
| 9 | nit | Comment cited ADR-0119 for the core-neutral guard (it's ADR-0121 §11); roadmap doc said "Languages (de, es)" | Fixed in this diff. The same mislabel in `scripts/ci/coreneutral/main.go:2` and ADR-0067/0068 prose describing the old table are left for a follow-up card |

## Verified beyond unit tests
- The reviewer re-verified TDD in its own WSL clone: `currency.go` reverted → the pt-PT grouping tests fail (`"€1,234.56"`); `setup_base_plugins.go` reverted to the table → `PT = [], want [{language pt}]` and the PT setup test fails. Finding 1's test was run red on the old code by the orchestrator.
- Driven run: a till built from this branch with the pt pack's `locales/` as the test overlay (`UT_TEST_I18N_OVERLAY_DIR`), shop set to `PT` / `pt-PT`. Nine pages crawled (sell, settings, catalog, journal, reports, inventory, users, tables, shifts): `<html lang="pt-PT">` and no English from any pack key. The remaining English is core data or core bugs (seeded brand/user names, theme labels, English-only release notes, `users.html:27` printing the raw role), filed as follow-ups.
- Screenshots looked at (light theme): sell screen at 1280×800 and 1024×600, Settings, Journal. The 10-inch pay button clipped "Adicione artigos para pagar"; the pack now uses "Adicione artigos". **Not looked at:** dark theme, a printed receipt (no printer), a journal with sales (demo data has none).

## Gate
`gofmt` clean, `go build ./...`, `go vet ./...`, `go test ./...` (WSL, Linux) all ok. `guard-core-neutral` (24 entries), `guard-i18n`, `guard-data-access` pass. `check-lang-pack-drift.test.sh` needs the pt repo on GitHub (it fetches the pack's real script), so it runs in CI after step 1 of the merge order.

**Verdict:** safe to merge after the pt pack repo exists and its PR is merged.
