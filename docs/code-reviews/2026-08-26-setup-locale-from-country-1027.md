# 2026-08-26 — Setup wizard derives store.locale from country (ut-docs#1027)

**Pipeline lane:** `lane:cloud-54`. **Complexity:** medium (Dev: Sonnet inline, Review: Opus subagent).

## What shipped

The setup wizard prefilled currency/tax from the chosen country but never
derived `store.locale`, so a German shop completing the wizard shipped
running `en-US` — wrong UI language default, and (per the standing
multilingual rule) wrong default for locale-derived customer-facing output.

- `internal/db/migrations/073_country_default_locale.sql` — new
  `country_settings.default_locale` column (BCP-47 tag), seeded per builtin
  country (DE→de-DE, GB→en-GB, FR→fr-FR, ES→es-ES, IT→it-IT, NL→nl-NL,
  TR→tr-TR, US→en-US, AE→ar-AE, SA→ar-SA, IN→en-IN, IR→fa-IR, PK→ur-PK;
  OTHER unset) — the same per-country-defaults table currency/tax already
  use, not a parallel hardcoded map.
- `internal/data/country_settings_repo.go` — `CountrySetting.DefaultLocale`
  wired through `List`/`Get`/`Upsert`; `builtinCountryDefaults` carries
  matching values; `country_settings_drift_test.go` extended so the
  migration seed and the Go fallback can't drift on this field either.
- `internal/pages/setup_page.go` — `POST /setup` derives `st.Locale`
  **server-side** from the posted country code against `country_settings`
  (never from client-supplied text — this endpoint is auth-exempt during
  first boot) and live-applies it via `httpx.SetDefaultLocale`.
- `internal/pages/settings_page.go` — `POST /api/settings/save` (defensive/
  API-shape correctness) and `POST /api/settings/upsert`'s `store.country`
  case (the actual shipped path an operator can use to change country
  post-setup — see review finding 2 below) both re-derive locale when the
  country changes.
- `internal/pages/country_settings_page.go` — the admin Country settings
  form now preserves `DefaultLocale` on save (it has no field for it),
  matching the pre-existing `NameKey` preservation right above it, and
  exposes the shared `localeSafeToPreset` gate (see "Independent review"
  below) used by all three call sites above.
- Test-fixture drift fix: `setup_page_test.go`'s `seedCountrySettingsTable`
  now also applies migration 073 (it previously replayed only 041 directly).
- Five `internal/db/*_test.go` files needed a new
  `rewindCountryDefaultLocale073` helper (added in `dead_seed_test.go`,
  called from all 5) — these tests rewind `schema_migrations` and replay
  forward; 073's `ALTER TABLE ADD COLUMN` isn't idempotent on replay, same
  class of problem the repo already has an established fix pattern for
  (`rewindPaymentsVoucherID072` and friends).
- `web/help/en/country-settings.md` updated; `make docs-shots` re-run.

**Rescoped OUT this cycle** (filed as ut-docs#1130, corrected during
review — see below): number/date **formatting** (grouping/decimal
separator, date order) following locale for Latin-digit locales. This
card only fixes locale *derivation*.

## Independent review (Opus subagent, worktree-isolated)

Verdict on the first pass: **not safe to merge** — 2 blockers, 3
should-fix, 4 nits. All blockers and should-fix items were fixed; nits
were triaged and either accepted-deferred or don't apply.

### Blocker 1 — ungated locale set had an RTL/digit-shaping hazard (fixed)

The original design set `st.Locale` unconditionally from the client tile's
`data-locale`, reasoning that `I18n.T()` gracefully falls back to English
for missing translations. True, but incomplete: `store.locale` also drives
`httpx.IsRTL` (`<html dir>`) and `httpx.LocalizeDigits` (Perso-/
Eastern-Arabic digit substitution) **immediately and unconditionally** —
neither has a translation-missing fallback. `PK` seeds `ur-PK`; `ur` ships
no bundled translations. A Pakistani shop completing the wizard would have
gotten English text, mirrored RTL layout, and Eastern-Arabic digits — a
regression versus today's `en-US`, and permanent (no `ur` pack exists).

**Fix:** `localeSafeToPreset(locale)` (`country_settings_page.go`) — a
non-RTL locale is always safe to preset (Latin digits, LTR either way,
which is what makes DE/FR/ES/IT/NL — this card's own headline case — safe
unconditionally); an RTL locale (`fa`/`ar`/`ur`/`ps`/`he`/`ckb`/`dv`/`yi`)
only presets once its base language is actually available
(`httpx.AvailableLocales()`). Applied at all three write sites. Verified
live against a real running server (see "Manual verification" below).

### Blocker 2 — help doc described a nonexistent screen; the AC was unreachable (fixed)

The original help-doc line claimed "changing your shop's country later,
from Settings → Store details, does the same thing" — **there is no such
screen**. Neither shipped Settings form (Currency, Language) posts
`country`; the only shipped path to change `store.country` post-setup is
the raw "All Settings" key/value editor (`POST /api/settings/upsert`),
whose `case common.KeyCountry` reflection did **not** re-derive locale.
So the card's own "changing country afterwards re-derives the locale"
acceptance criterion was unreachable through any real UI path.

**Fix:** wired the re-derive into `POST /api/settings/upsert`'s
`KeyCountry` case (looked up before `UpdateState`'s closure, not inside
it, so the DB read doesn't run under the state write lock), added
`TestUpsertCountry_RederivesLocale`, and corrected the help doc to name
the real path (Settings → All Settings → `store.country`) instead of the
fictional one.

### Should-fix 3 — help doc overclaimed number formatting; #1130's premise was wrong (fixed)

"Default language/**number-format** locale" overclaimed — `de-DE` changes
nothing about number rendering (confirmed: `LocalizeDigits` is a no-op for
`de`). Dropped "number-format" from the help doc. Separately, #1130's
original framing ("no locale-aware formatting mechanism exists anywhere")
was itself wrong — `LocalizeDigits` already does locale-driven digit
substitution for `fa`/`ur`/`ps`/`ar`. Corrected #1130's title/body to scope
the real remaining gap (Latin-digit grouping/decimal conventions and date
ordering) rather than re-inventing a mechanism that partly exists.

### Should-fix 4 — two negative tests were vacuous (fixed)

Both "leaves locale untouched" tests asserted against a `""` baseline, so
they'd pass with the guard code deleted entirely. Rewrote to seed a
genuine non-blank starting locale (`"tr"`) via `d.UpdateState` first, and
replaced the DE negative case (now correctly a *positive* case under the
RTL-aware gate) with PK/`ur-PK` — the one country in this seed data that
actually exercises the RTL-and-unavailable branch.

### Should-fix 5 — posted locale was validated only by shape (fixed)

`isPlausibleLocale` only checked length/charset, not that the value
actually matched the chosen country. **Fix:** removed client-locale-trust
entirely — `POST /setup` now derives the value server-side by looking up
`country_settings` itself; the wizard's hidden `locale` field, `data-locale`
tile attribute, and `setupCountry.Locale`/`detectedLocale` plumbing were
removed as dead weight once the client value was no longer trusted
(verified: a posted `locale=xx-NOTREAL` is now silently ignored, see
`TestSetupWizardDerivesLocaleFromCountry`'s "posted locale field is
ignored" subtest).

### Nits — triaged, not fixed this cycle

- **Nit 6** (an explicit step-1 language cookie choice can be "overtaken"
  by a later country-derived `store.locale` for requests with no cookie):
  accepted — not a regression (was `en-US` before either), and the
  explicit-choice-should-win refinement is a real but separate UX
  question, not this card's scope.
- **Nit 7** (the shipped Language card always posts a bare
  `AvailableLocales()` code, so applying it after this card's fix rewrites
  a region-qualified `en-GB` back to bare `en`): accepted — harmless
  (`IsRTL`/`LocalizeDigits`/`T()` all strip region already), pre-existing
  shape of that card, not introduced here.
- **Nit 8** (only `en/country-settings.md` updated; `ar`/`fa`/`tr` topics
  still describe the old behaviour): accepted-deferred — `guard-help-topics.sh`
  only checks topic *presence*, not content parity, and translating three
  locales' prose is a real but separate follow-up; not blocking given the
  english-first authoring pattern this manual already uses elsewhere.
- **Nit 9** (`country_settings_page.go`'s `Get` error is folded into "not
  found," so a transient read error blanks `DefaultLocale` too): accepted
  — pre-existing pattern (already true for `NameKey`), not introduced or
  worsened by this diff.

## TDD verification (independent, worktree-isolated, revert→run→restore)

Performed by the review subagent against the WIP snapshot, before the
blocker fixes above (so against the *original* design) — re-run and
re-confirmed manually against the *final* diff before commit:

- `TestSetupWizardDerivesLocaleFromCountry` — reverted the `st.Locale =`
  block: fails with `store.locale = "", want de-DE` / `en-GB`. Restored:
  passes.
- `TestCountrySettingsPageSavePreservesDefaultLocale` — reverted the
  `defaultLocale` preservation: fails with `DefaultLocale ... = "", want
  unchanged de-DE`. Restored: passes.
- `TestSaveSettings_CountryChangeRederivesLocale` — reverted the re-derive
  block: only the *positive* subtest failed (the vacuous negative
  subtests, since fixed, passed even with the code gone — this is what
  should-fix 4 above fixes).
- `TestSeedBarcodeChecksumsFixedOnUpgrade` — reverted one
  `rewindCountryDefaultLocale073(t, d)` call: fails with `duplicate column
  name: default_locale`, exactly the predicted error.

All four restored; full `internal/data`/`internal/pages`/`internal/db`
suites green afterward.

## Manual verification (real running server, not just Go tests)

- Built and ran the app for real (`UT_STORE=sqlite`), drove `POST
  /api/setup` with `country=DE` → `GET /login` returns
  `<html lang="de-DE" dir="ltr" ...>`. Confirms the non-RTL headline case
  live, end to end.
- Fresh DB, `country=PK` (the RTL-and-unavailable case) → `GET /login`
  returns `<html lang="en-US" dir="ltr" ...>` — locale correctly left
  untouched, no RTL flip. Confirms the fix for blocker 1 live, not just in
  a Go test.
- Screenshotted the setup wizard's country step (1024×600, the sale
  screen's own touch-target viewport) before and after selecting Germany —
  no layout regression, tile selection state renders correctly. Not
  re-screenshotted after the blocker-1/2 redesign since that redesign
  removed client-side markup/attributes entirely (no `data-locale`, no
  hidden `locale` input) rather than changing anything visible — the
  screenshot taken against the earlier (attribute-carrying) version is
  representative of the same visual surface. Dark theme / RTL locale (fa)
  visual check of the wizard itself was **not** performed this cycle
  (no layout was touched at all, only a hidden-input/data-attribute
  addition that has since been removed) — noting the gap explicitly per
  the tester skill rather than implying full coverage.

## What was NOT verified

- `internal/plugins` (the WASM plugin runtime test suite, ~20min in CI) was
  not re-run locally — this diff touches no plugin code.
- The five help-topic translations (`ar`/`fa`/`tr`) were not updated (nit 8,
  accepted-deferred).

## Verdict

Safe to merge. All CI-blocking guards green
(`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
`guard-help-topics.sh`), `go build ./...` / `go vet ./...` clean,
`go test $(go list ./... | grep -v /internal/plugins)` green.
