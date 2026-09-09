# Code review — backfill locale-confirmed for pre-existing diverged pending tills (ut-docs#1892)

**Date:** 2026-09-09
**Branch:** `fix/1892-locale-confirmed-backfill`
**Reviewer:** independent fresh-context Opus subagent (different model from
the implementer, per the `complexity:medium` routing in the `scrum-master`
skill's "Model routing by complexity")
**Verdict:** NOT SAFE TO MERGE (as submitted) → the blocker and all other
findings fixed and independently re-verified in this same pass.

## What changed

ut-docs#1892 is a deferred finding (F4) from ut-docs#1074's own review:
`common.KeyLocaleConfirmed` is write-forward-only, so a shop that manually
diverged `store.locale` away from its country's default *before* the flag
existed has no way to be recognised as "already confirmed." Once a
still-pending `"language"` base-plugin install finally completes,
`applyDerivedLocaleIfLanguagePackNowAvailable` would silently flip the
locale back to a country default, overriding the operator's earlier manual
choice with no action and no audit trail.

Adds `backfillLocaleConfirmedForDivergedPendingTills`, a boot-time check
wired into `internal/pages/init.go` right before `StartBasePluginRetry`, so
it always runs before the background retry loop can reach the code path it
protects against.

## What the reviewer verified independently (first pass)

- `gofmt -l`, `go build ./...`, `go vet ./...`,
  `golangci-lint run ./internal/pages/...` (0 issues).
- `go test ./internal/pages/... -run TestBackfillLocaleConfirmed -v` and the
  full `go test ./internal/pages/...` (all green, 254s).
- Searched broadly for every writer of `store.locale`/`common.KeyLocale`
  across the repo (not just the three named in `KeyLocaleConfirmed`'s own
  doc comment).
- Traced the boot sequence in `internal/app/app.go` end to end: which
  goroutines start before `pagesInit`, whether any HTTP handler reachable
  before `Init` returns could race the backfill, and the exact ordering
  around `settingsStore.LoadRuntimeConfig(ctx, cfg)`.
- Wrote a temporary probe test (`internal/pages/zz_review_probe_test.go`,
  deleted before finishing) replicating that exact production boot
  ordering against the submitted diff.

## Findings and what was done about them

1. **F1 (BLOCKER) — the submitted diff compared against `d.Cfg.Locales.Locale`,
   which is *not* the compiled bundled default at runtime; the backfill was a
   no-op on every real till.** `internal/app/app.go` runs
   `settingsStore.LoadRuntimeConfig(ctx, cfg)` on the *same* `*config.Config`
   pointer later handed to `pages.Init`, and `LoadRuntimeConfig`
   unconditionally overwrites `cfg.Locales.Locale` with the shop's persisted
   `store.locale` on every boot after the first. So by the time
   `backfillLocaleConfirmedForDivergedPendingTills` ran,
   `st.Locale == d.Cfg.Locales.Locale` was trivially true in production —
   the guard always matched and returned early. The reviewer proved this
   with a probe test that replicated the real boot ordering (seed a
   diverged, persisted locale, call the real `LoadRuntimeConfig`, then run
   the backfill): `store.locale_confirmed` stayed unset. The six original
   tests all passed anyway only because the shared test fixture
   (`newMigratedSyncDeps`) builds a fresh `cfg` that never goes through
   `LoadRuntimeConfig`, so they never exercised this ordering at all.

   **Fixed:** added `config.Config.CompiledDefaultLocale`, set once in
   `config.Init()` from the same `UT_DEFAULT_LOCALE` env resolution as
   `Locales.Locale`, and never touched again — `LoadRuntimeConfig` (in
   `internal/settings/runtime.go`) only ever mutates `Locales.Locale`. The
   backfill now compares against `d.Cfg.CompiledDefaultLocale`. Added
   `TestBackfillLocaleConfirmed_SurvivesProductionBootOrdering`, which calls
   the real `settings.Store.LoadRuntimeConfig` against the same `*Deps.Cfg`
   before running the backfill — the permanent regression test the missing
   probe should have been. **Independently mutation-verified in this pass**
   (not just claimed): reverted the comparison back to
   `d.Cfg.Locales.Locale`, confirmed the new test fails with the exact
   expected message, then restored the fix and confirmed it passes again.
   Also added direct `internal/config` coverage
   (`TestInitDefaults`/`TestInitHonorsEnvOverrides`) proving
   `CompiledDefaultLocale` tracks `UT_DEFAULT_LOCALE`, not
   `UT_MARKETPLACE_LOCALE` (the unrelated, pre-existing top-level
   `DefaultLocale` field — a prior code review, ut-docs#863, already warned
   these two are easy to conflate).

2. **F2c (MEDIUM) — comparing only against the shop's CURRENT country's
   default missed a real, harmful false positive.** A merchant who changes
   country while an RTL pack is still pending leaves `store.locale` at the
   *previous* country's default: derivation to the new country's default is
   skipped the same way the never-touched case is
   (`localeSafeToPreset` returns false until the pack installs), so nothing
   ever rewrites the stale value. That leftover matches neither the
   compiled default nor the *current* country's default, so the original
   single-country check would have wrongly marked it confirmed —
   permanently disabling the catch-up for exactly the shop this card exists
   to protect. **Fixed:** the guard now checks `st.Locale` against *every*
   known country's `DefaultLocale` (`CountrySettingsRepo.List`, not
   `.Get(st.Country)`), so a leftover from any country is still recognised
   as automatic. Added
   `TestBackfillLocaleConfirmed_LeavesLeftoverPreviousCountryDefaultUnconfirmed`.
   **Independently mutation-verified**: reverted to the single-country
   `Get` call, confirmed the new test fails with the expected message,
   restored the fix, confirmed green again.

3. **F2ab (informational, no fix needed) — the "only three writers" premise
   undercounted.** `POST /api/settings/upsert` with `key=store.locale`
   (`settings_page.go`) and the cloud `set_setting` directive
   (`cloudsync_wire.go`) are both real, additional writers of an
   operator/remote-chosen locale that don't set `KeyLocaleConfirmed` either.
   Both are directionally benign for this backfill: they write a genuine
   manual/remote choice, so the backfill correctly treats a locale reaching
   either path as "not automatic" and backfills it — which is the intended
   behaviour, not a gap. Reflected in the updated doc comment so a future
   reader doesn't re-derive a stricter "only three" claim that no longer
   holds.

4. **F3 (accepted residual gap, same framing the original card used) — an
   operator who deliberately chose exactly the compiled default locale is
   indistinguishable from a shop that never touched the setting.** E.g. an
   English-speaking owner of a non-English-default shop who picks `en-US`
   on purpose (plausible: exactly the shape of shop this fix's own target
   population — offline setup, still-pending pack — skews toward). There is
   no third signal in this schema to tell the two apart. That shop stays
   exposed to the original override once its pack installs. The card's own
   body already accepted a comparably narrow residual risk ("the exposure
   is bounded... not urgent") rather than reaching for an invasive fix
   (e.g. a till schema/feature-version marker); this is the same class of
   accepted gap, now stated explicitly in the function's doc comment rather
   than implied away.

5. **Test-naming/comment nit — the original "fr-FR" fixture value
   accidentally collided with FR's own seeded `country_settings.default_locale`**
   once the F2c fix widened the check to all countries. Replaced with
   `de-AT` (not a seeded country default) across all five tests that used
   it, and updated the explanatory comment.

## Independent re-verification of the fixes (this session, before merge)

- Both mutations (F1's comparison field, F2c's single-country check)
  reverted in turn, confirmed the corresponding new regression test fails
  with the exact expected message, then restored — diffed clean against
  the intended fix each time.
- `gofmt -l`, `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` (0 issues) on the full repo, not just the
  touched package.
- Full `go test ./...` — every package green, `internal/pages` included
  (255s).
- `go test ./internal/pages/... -run 'TestBackfillLocaleConfirmed|TestResolveAndInstallBasePlugin|TestInstallBasePluginsForSetup|TestBasePluginRetryTick' -v`
  — all 8 backfill tests plus every existing base-plugin/locale test still
  green.
- `go test ./internal/config/...` — green, including the two new
  `CompiledDefaultLocale` assertions.
- CI-blocking guards: `guard-data-access.sh`, `guard-i18n.sh`,
  `guard-page-http-error.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh` — all clean.

**Not verified:** real hardware / a live till reboot. This is a pure
boot-sequence/settings-store change with no UI surface — no `web/` or
`internal/pages/**/*.html` touched, so no `make docs-shots` regeneration
applies, and there is nothing for a manual visual pass to check.

## Scope confirmed

Stays in `universal-till` core (a boot-time backfill inside the
already-accepted ut-docs#1074 design). No new plugin, no ADR (closes a
design gap #1074 already flagged, introduces no new cross-cutting
mechanism, contradicts nothing in `adr/`). No money/tax implications. No
new user-facing string (log lines only) — `guard-i18n.sh` confirms. No
route/screen change — `guard-help-topics.sh` confirms. No raw SQL outside
`internal/data`/`internal/db` — only `CountrySettingsRepo.List`/`.Get` and
`d.Settings.Get`/`.Set` are used — `guard-data-access.sh` confirms.

## Safe-to-merge verdict

Yes. The blocker (F1) and the harmful false-positive (F2c) are both fixed
and independently mutation-verified; the informational finding (F2ab) and
the accepted residual gap (F3) are documented rather than silently
dropped. Nothing deferred is a correctness or safety concern beyond what
the card's own original body already accepted.
