# Code review — PT in the builtin country list (ut-docs#2963)

**Date:** 2026-09-27 · **Lane:** lane:local-mahshid76 · **Complexity:** easy
**Author:** Opus 5.5 (local session) · **Reviewer:** Fable (fresh context, one pass, its own throwaway WSL clone)

## What shipped

Portugal becomes a builtin country in the setup wizard, as the Portugal track's first code card (ut-docs#2578, research in ut-docs `reference/portugal-compliance.md` §16 C8).

- `internal/db/migrations/051_builtin_country_pt.sql` (new, checksum pinned in `shipped_migrations_test.go`): inserts PT with EUR, `€`, 2300 bp, VAT-inclusive, `pt-PT`, the ADR-0040 floor of 3650 days, and the seed's epoch `updated_at`. It is additive because `001_init.sql` is frozen (ADR-0100). When an operator already created a custom PT row, `ON CONFLICT` keeps every value they chose. It only sets `is_builtin = 1`, which is what `CountrySettingsRepo.Upsert` would do on its next save anyway, and fills a blank `name_key`.
- `internal/data/country_settings_repo.go`: PT added to `builtinCountryDefaults`. `TestBuiltinDefaultsMatchMigrationSeed` pins it to the migration.
- `internal/pages/setup_detect.go`: `Europe/Lisbon` → PT. A new `setupTimezoneNoCountry` set (`Atlantic/Azores`, `Atlantic/Madeira`) stops detection instead of falling back to the locale region. Without it, a `pt_PT` island till would prefill the 23% mainland rate, whereas the regions have their own IVA rates (ut-docs#2961).
- `web/locales/{en,fa,ar,tr}.json`: `setup.country.pt`.
- Tests: `migration_051_builtin_country_pt_test.go` covers a fresh DB, promotion of an operator row, keeping an operator `name_key`, idempotent replay, and the on-disk file name. It also updates the seed count (14 → 15, plus PT values) and adds `TestDetectCountry` cases for Lisbon, Azores and Madeira.
- Stale wording fixed: "migration 041" in the drift test's messages, the "14 builtin countries" comment in `sync_admin_repo_test.go`, and the `adminTables` comment in `sync_admin_repo.go` (a builtin row can now be missing from an older primary's dump across versions).

Not in scope, each tracked: regional rates and exemptions (ut-docs#2961), the `pt-PT` language pack and its `setupBasePlugins` row (ut-docs#2964), a PT fiscal hard gate (ut-docs#2958), and shadow-mode document suppression (ut-docs#2967). PT is deliberately not in `fiscal.RequiresHardGate`, and nothing claims compliance (ADR-0040).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The Lisbon test case used a `pt_PT` locale, so the locale fallback also produced PT. Deleting the `Europe/Lisbon` entry failed nothing. | Fixed: the case now uses `en_GB`. Re-verified: without the entry it fails with `detectCountry(codes) = "GB", want "PT"`. |
| 2 | should-fix | Leaving Azores/Madeira unmapped did not keep them off the mainland rate: with a `pt_PT` locale, the fallback detected PT. The tests only tried `en_US` and an empty locale. | Fixed with `setupTimezoneNoCountry`, plus `pt_PT` cases for both zones. Watched both fail first (`= "PT", want ""`). |
| 3 | nit | Stale comments and messages ("migration 041", "14 builtin countries", "never absent from a primary's dump"). | Fixed. |
| — | pre-existing | A Canary Islands till (`Atlantic/Canary` + `es_ES`) prefills Spain's 21% mainland IVA rate, although the islands use IGIC. | Filed as ut-docs#3033 (Backlog). |

The reviewer probed these and found them fine:
- **Upsert:** `ON CONFLICT(code) DO UPDATE` with `country_settings.name_key` / `excluded.name_key` is valid SQLite.
- **Admin-sync triggers (023):** a fresh insert bumps the admin version once. A replay is a no-op UPDATE, so there is at most one extra harmless bump and no loop.
- **A satellite that got PT over LAN sync before its own upgrade:** 051 finds `is_builtin = 1` and a name, so nothing changes.
- **Mixed versions:**
  - A new primary with an old satellite: the satellite shows the raw key until it upgrades. Cosmetic only.
  - An old primary with a new satellite: the satellite's PT row is pruned or overwritten until the primary upgrades. The retention fallback is the same 3650 days, and satellites don't run admin edits.
- **Other enumerations:** no other code, template, help topic or e2e test lists the builtin countries. `pt-PT` is LTR, so presetting it before a pack exists falls back to English (`localeSafeToPreset`).
- **Migration rules:** checksum pinned, `001_init.sql` untouched, SQL only in `internal/db`.
- **`os.MkdirAll` / `paths.Data`:** not applicable, since the change writes no files.

## Verification

- **TDD:**
  - The author watched each new test fail first: seed count `14, want 15`, `no migration with version 51`, and Lisbon `"" want "PT"`.
  - Mutation checks on 051: `DO NOTHING` fails the promotion and name tests, and always overwriting `name_key` fails the name test.
  - The reviewer independently re-broke two things in its own clone. Keeping `is_builtin` fails both operator-row tests with `builtin:0 … want builtin:1`, and removing PT from `builtinCountryDefaults` fails the drift test with "14 entries … seeded 15".
- **Gate (Linux/WSL, on origin/main `dcde8b3` plus this change):** `gofmt`, `go build ./...` and `go vet` are clean. `go test -count=1 ./...` passes: 72 packages ok, 0 FAIL. It was re-run after the review fixes. These guards pass: data-access, i18n, core-neutral, compliance-claims, competitor-naming, migration-version-collision, help-drift, help-topics, readme-local-links, demo-env and page-http-error.
- **Driven run:**
  - The real binary ran on a fresh data dir at `127.0.0.1:8095`. `/setup` renders the Portugal tile with `data-currency="EUR" data-tax="23" data-taxinc="on"`, visible under "Show all countries".
  - The developer looked at it in a browser and confirmed it.
  - Not looked at: dark/light themes and kiosk/10-inch viewports. The change adds one tile to an existing, unchanged tile list, with no new markup or styles.

**Verdict: safe to merge** once the post-fix gate is green.

**Follow-ups in the same cycle:** `setup.country.pt` is a brand-new core key, so `ut-plugin-language-de` and `ut-plugin-language-es` PRs follow the core merge ("Portugal" in both, allowlisted as same-as-English, with a version bump).
