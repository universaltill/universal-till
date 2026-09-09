# Code review — auto-switch store.locale once a language plugin installs (ut-docs#1074)

**Date:** 2026-09-09
**Branch:** `feat/1074-locale-after-language-plugin-install`
**Reviewer:** independent fresh-context Opus subagent (different model from the
implementer, per the `complexity:medium` routing in the `scrum-master` skill's
"Model routing by complexity")
**Verdict:** NOT SAFE TO MERGE (as submitted) → all MEDIUM findings fixed and
independently re-verified in this same pass; two LOW/deferred findings filed
as follow-up cards rather than expanding this diff further.

## What changed

ut-docs#1027 (merged, `universal-till#561`) already derives `store.locale`
from `country_settings.<code>.DefaultLocale` synchronously at
country-selection time, gated by `localeSafeToPreset`
(`internal/pages/country_settings_page.go`): a non-RTL locale presets
unconditionally, an RTL one (fa/ar/ur/he/...) only once its base language
pack is already installed. When it isn't yet, that derivation is
deliberately left unapplied and nothing re-checks it once the pack actually
finishes installing — the literal gap ut-docs#1074 asks to close.

`resolveAndInstallBasePlugin` (`internal/pages/setup_base_plugins.go`) now
re-applies the shop's current country's default locale once a matching
`"language"` canonical-type spec is confirmed installed-and-active — both
on a fresh install and on the pre-existing idempotent "already active"
branch, so it covers all three paths that reach that function: the setup
wizard's synchronous attempt, the background retry tick, and an operator
manually installing a language pack (the wizard's own step-1 tile, or the
marketplace). A new `common.KeyLocaleConfirmed` setting (mirrors
`KeyCurrencyConfirmed`), written only by Settings' Language card (the one
genuine manual-choice path, `POST /api/settings/save`'s `locale` field),
gates it — and, per the reviewer's finding F3 below, now also gates
ut-docs#1027's own two pre-existing country-change derivation call sites.

## What the reviewer verified independently (first pass)

- `go build ./...`, `gofmt -l`, `golangci-lint run ./internal/pages/...` (0
  issues), `go test ./internal/pages/... ./internal/data/... ./internal/httpx/...`
  (all green, `-race` on the targeted subset clean).
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-page-http-error.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh` all pass. No raw
  SQL outside `internal/data`, no new user-facing strings, no file writes.
- Five mutation experiments (production code reverted, tests re-run, then
  restored — working tree verified byte-identical afterwards). Confirmed
  the install→reload→`syncLocales`→`SetOverlays` chain runs *before* the
  new locale-apply call, so `httpx.AvailableLocales()` is fresh at that
  point; confirmed `st.Country` isn't stale at the wizard's own call site;
  confirmed `CountrySettingsRepo.Get` normalizes the country code and
  `baseLang` lowercases both sides of the locale comparison; confirmed the
  `KeyLocaleConfirmed` read failure path fails closed (leaves the locale
  untouched); confirmed the `spec.CanonicalType == "language"` guard
  correctly excludes the tax-plugin caller in `setup_tax_catalog.go`.
- No infinite loop, no double-write, no audit spam across repeated retry
  ticks: once applied, `st.Locale == cs.DefaultLocale` short-circuits every
  later call before any write.

## Findings and what was done about them

1. **F1 (MEDIUM) — the idempotent "already active" branch's own call to the
   new catch-up logic had zero real test coverage; the test that claimed to
   cover it didn't actually exercise that code path** (it asserted a
   post-condition already established by the earlier fresh-install call in
   the same test). **Fixed:** replaced with
   `TestResolveAndInstallBasePlugin_LocaleAppliesOnlyViaIdempotentAlreadyActivePath`,
   which installs the plugin with no country set (so the fresh-install
   branch's own application is a deliberate no-op), then sets the country
   to match and calls again — the second call necessarily takes the
   idempotent branch (`downloadTokenHits` stays 1), so it's the *only* call
   that could possibly apply the locale. **Independently re-verified in
   this pass**, not just claimed: temporarily deleted the idempotent
   branch's call to `applyDerivedLocaleIfLanguagePackNowAvailable`, reran
   this exact test, watched it fail with the expected message, then
   restored the file and confirmed a clean diff before re-running the full
   gate.
2. **F2 (MEDIUM) — the original tests' premise was factually wrong: IR's
   own default (`fa-IR`) is bundled (`web/locales/fa.json` ships with the
   product), so `localeSafeToPreset("fa-IR")` is already `true` with no
   plugin installed at all** — ut-docs#1027's own existing test
   (`TestSetupWizardDerivesLocaleFromCountry`'s "AE derives ar-AE (RTL, but
   ar ships bundled — safe)" case) already demonstrates this for AE/`ar-AE`
   too. The reviewer traced the real seeded country list and found PK's
   default (`ur-PK`) is the one genuinely RTL-and-unbundled case. **Fixed:**
   every RTL-scenario test now uses PK/`ur`, and each explicitly wires
   `httpx.InitI18n` (not just `dp.Pm.SetLocalizer`, which the review also
   showed does NOT alone update the package-level translator
   `localeSafeToPreset` reads — production pairs the two calls in
   `internal/pages/init.go`; a test setting only one diverges from it) with
   a hermetic `en`-only bundle, and asserts `ur` is genuinely unavailable
   before install and available after, so the test's own premise is now
   verified rather than assumed.
3. **F3 (MEDIUM) — a comment on the new `KeyLocaleConfirmed` write claimed
   it stops ut-docs#1027's country-change re-derivation from overriding an
   explicit choice, but that re-derivation never actually read the flag.**
   Concrete scenario: operator sets locale to `en` via Settings, later
   changes country to `FR` with no `locale` field posted — #1027's existing
   logic would silently flip `store.locale` to `fr-FR`. **Fixed** (took the
   reviewer's second, stronger option rather than just narrowing the
   comment): both of #1027's existing call sites
   (`POST /api/settings/save`'s country handler and
   `POST /api/settings/upsert`'s raw key/value table) now also check
   `KeyLocaleConfirmed` before re-deriving. This closes a real,
   independently-discovered gap in already-shipped behaviour, not just
   this card's own new code, and the original comment is now accurate.
4. **F5 (MEDIUM) — `applyDerivedLocaleIfLanguagePackNowAvailable` built its
   candidate via `d.UpdateState` (committing the new locale into shared
   memory) before `common.SaveState`, inverted from the
   read-candidate-then-`SaveState`-then-`SetState` pattern every sibling
   handler in this file already follows** (see `Deps.SetState`'s own doc
   comment on why: a failed save must never become the new in-memory
   state). **Fixed:** rewritten to build `cand := st; cand.Locale = ...`
   and call `SaveState` before `SetState`, matching the established
   pattern exactly.
5. **F7 (LOW) — no log line when a listing satisfies `localeInList`'s
   base-language match but not `localeSafeToPreset`'s exact-code check
   against `httpx.AvailableLocales()`** (e.g. a pack shipping
   `locales/fa-IR.json` rather than `fa.json`) — would silently stay
   pending forever with no signal. **Fixed:** added a `Warnf` naming the
   installed locale, the target default, and the current available set.
6. **F8 (NIT) — `baseLang` calls on the country/installed locale skipped
   `strings.TrimSpace`, unlike the sibling `localeInList` helper in the
   same file.** **Fixed:** added for parity.
7. **F6 (LOW) — the user manual described the old behaviour** (an
   RTL language "is left as it is until its pack is installed", with no
   mention it now switches automatically once that finishes).
   **Fixed:** updated `web/help/en/country-settings.md` and
   `web/help/en/display.md` in this same branch, per the repo's standing
   "manual ships with the feature" rule.

## Findings deferred, and why

- **F4 (MEDIUM, accepted residual risk, not fixed here) — no backfill for
  shops that manually chose a locale before `KeyLocaleConfirmed` existed.**
  The exact scenario needs a still-pending base-plugin install spec that
  predates this deploy *and* a shop whose current locale already diverged
  from its country's default without ever going through Settings' Language
  card (e.g. it was left at the pre-#1027 fallback). Explored a boot-time
  backfill and rejected it: there is no clean signal in this codebase to
  distinguish "an old shop that manually diverged before this flag
  existed" from "a brand-new shop that hasn't touched locale yet" — every
  candidate heuristic tried either falsely marks all new shops as
  already-confirmed (defeating this card's own purpose) or doesn't
  narrow the exposure at all. The exposure is bounded to the deploy
  transition window (any shop that installs a language pack for the first
  time under the new code gets `KeyLocaleConfirmed` semantics correctly
  from day one) and requires a currently-stuck pending install, a narrow
  precondition. Filed as ut-docs#1074's own follow-up rather than silently
  dropped: **ut-docs#1892**.
- **"Also worth knowing" (not a defect in this diff) — installing a
  language pack from the marketplace's own Plugins-store page
  (`plugin_api.go`'s `handleInstallFromMarketplace`) doesn't route through
  `resolveAndInstallBasePlugin`, so it gets no locale catch-up either.**
  The function's own doc comment only ever claimed the three paths it
  actually covers, so this isn't a false statement in the new code — but
  it's a real coverage gap worth closing. Filed as **ut-docs#1893**.

## Independent re-verification of the fixes (this session, before merge)

- Full targeted suite (`BasePlugin|Locale|Setup|Settings|Country|Upsert`)
  and the full `go test ./...` both green after every fix above.
- `gofmt -l`, `go vet ./...`, `golangci-lint run ./...` (0 issues).
- All CI-blocking guards listed above re-run clean after the manual-doc and
  settings_page.go changes.
- Mutation-killed F1's new test directly (see finding 1 above) — not
  re-trusted from the reviewer's own report, re-proven in this session.

## Scope confirmed

Stays in `universal-till` core (the base-plugin auto-install mechanism
ADR-0002/ADR-0009 already treat as core) — no new plugin, no ADR (closes a
design gap #1027 already established, introduces no new cross-cutting
mechanism). No money/tax/security implications. `setup_page.go`'s own
first-boot derivation is untouched — it's the wizard's very first write, so
`KeyLocaleConfirmed` is always false there and gating it would be a no-op;
left alone rather than adding dead code.
