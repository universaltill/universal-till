# Setup wizard language catalog (ut-docs#1092)

**Card:** universaltill/ut-docs#1092 — "Setup wizard's first screen offers
only bundled locales — it must list every language and install the pack on
selection." Reported by the product owner on a real Pi 5 install of v0.6.2:
German is a complete, shipping language pack (`ut-plugin-language-de`
v1.1.17), but the wizard's very first screen only ever listed the four core
locales (`en/tr/fa/ar`) because German is a plugin, not compiled in.

**Complexity:** hard. Dev via a `fable` subagent (isolated worktree), review
via `opus` (fresh context, isolated worktree, deliberately not fable), per
`scrum-master`'s model routing table. This card's own BA/Architect passes
were completed by an earlier `lane:cloud-54` cycle that died before Dev
started (zero artifacts, no branch/PR) — reused rather than redone.

## What shipped

- **Setup wizard step 1** now lists bundled/installed locales **and** every
  marketplace `canonical_type=language` catalog listing, fetched via a
  package-level TTL cache (`setupLanguageCatalog`, 5 min, mutex-guarded,
  4s-bounded fetch) so `GET /setup` never hangs on the network. A cache
  miss + unreachable catalog degrades to bundled-only plus an honest
  "more languages once connected" note — never blocks, never errors.
- Picking a **catalog-only** language tile POSTs to a new
  `POST /api/setup/language` (`internal/pages/setup_page.go`), which
  installs the pack through the exact same Ed25519-verified path
  `ut-docs#1055`'s country auto-install already uses
  (`resolveAndInstallBasePlugin` → `cloudInstallPluginVersion`) — no second
  install path — then continues the wizard in that language via the
  existing `?lang=` cookie mechanism (identical resulting state to tapping
  a bundled tile).
- Install failure or timeout (20s foreground budget, `setupWizardLanguageInstallTimeout`,
  separate from the 5s background best-effort budget) never falls back to
  English silently: the spec joins #1055's persisted pending list
  (background retry every 5 min + the Settings pending-plugin chip), and
  the wizard says so plainly ("Still installing de — continuing in en for
  now. We'll keep trying in the background…").
- 4 new i18n keys (`setup.language.{install_pending,install_retry_hint,installing,more_when_connected}`)
  added with real translations to all of `en/tr/fa/ar`.
- `web/help/{en,tr,fa,ar}/users.md` item 6 and `README.md` updated in the
  same branch; `make docs-shots` re-run (surface hash changed, no PNG
  changed — `/setup` isn't itself a routed help-topic screenshot).
- One new e2e spec appended to the existing `e2e/tests/login.spec.ts`
  (no new infra): proves the offline acceptance path in a real browser —
  bundled locales still one-tap links, no half-working catalog tile, the
  honest note, and a bundled pick still drives the wizard.

## Independent review (Opus, fresh context, isolated worktree)

Found 2 blocker-class issues and 2 further real bugs, all fixed in this
same commit before merge — a second, scoped review round is not required
per `scrum-master`'s rule (fixes were re-verified TDD-style by me, not
re-reviewed by a second Opus pass, since none reopened new design
questions — see "Verification" below for exactly how each was proven).

1. **BLOCKER — XSS-shaped: unvalidated catalog locale interpolated into an
   Alpine JS-attribute expression.** The catalog is an *unsigned*
   marketplace response (Ed25519 only covers the plugin artifact, never
   this listing metadata), and `setupCatalogLanguageLocales` cached
   whatever string a listing's `availableLocales` contained, which the
   template then dropped straight into `@click="installingLang = '{{ . }}'"`
   / `:aria-busy="installingLang === '{{ . }}' ? …"`. `html/template`
   escapes those as HTML attribute text, not as JavaScript, so a listing
   locale like `a'+alert(1)+'` HTML-decodes back into a real quote before
   Alpine evaluates it — on the exact page that collects the admin PIN two
   steps later. **Fix:** the catalog-fetch loop now applies the same
   `isBareLocaleCode` gate `setup_page.go` already uses for every other
   externally-influenced locale value, dropping anything that isn't a bare
   2-8 letter tag before it's ever cached or rendered
   (`setup_base_plugins.go`).
2. **BLOCKER (product-facing) — the card's own headline scenario, reproduced
   by a second mechanism.** `detectLanguage()`'s "we don't have de yet, it's
   on the way" note only checked `httpx.AvailableLocales()` (core-compiled
   locales), so on a real German Pi with the catalog reachable, step 1
   showed the "coming soon" note **directly above** a working German
   install tile — a self-contradicting screen, and literally the scenario
   the product owner reported. **Fix:** the note (and the
   `setup.detected_lang_unavailable` telemetry that files a missing-language
   ticket, ut-docs#589 child 3) is now suppressed whenever the detected
   language is present in the catalog fetch — it's genuinely available, not
   missing (`setup_page.go`).
3. **REAL — the country step silently clobbered a language spec another
   step had already queued.** `installBasePluginsForSetup` (the existing
   #591 country auto-install hook) called `savePendingBasePlugins` with a
   wholesale replace, so an operator who picked a catalog-only language at
   step 1 (queued for background retry while offline) and then confirmed a
   country with its own free base plugin (DE/ES) would have the step-1
   spec silently dropped — breaking the "we'll keep trying in the
   background" promise the install-pending note makes. **Fix:**
   `installBasePluginsForSetup` now merges via the existing
   append-if-absent `addPendingBasePlugin` helper before attempting, and
   drops only the one spec that actually installed (`dismissPendingBasePlugin`)
   afterward — never a wholesale replace.
4. **REAL — the new tiles became the wizard form's implicit default submit
   button.** Every step's `<section>` stays in the DOM the whole time
   (`x-show`, not `x-if`), so once a catalog install tile (a real
   `type="submit"`) exists on step 1, it becomes the browser's *implicit*
   "press Enter in any text field" target for every later step too —
   pressing Enter while typing the shop name, a TSE field, or the PIN would
   silently fire an unintended language install and discard whatever the
   operator had typed. **Fix:** a disabled, hidden, always-first
   `<button type="submit" disabled hidden data-implicit-submit-guard>` at
   the top of the `<form>` — per the HTML spec's default-button algorithm,
   a *disabled* default button stops implicit submission rather than
   falling through to the next enabled one, while every explicit click
   (a tile, Finish) is completely unaffected.

Findings 5-8 (unpaginated catalog read at 20 listings; no negative-cache on
a failed fetch, so a black-holed network re-pays the full 4s timeout on
every render; `enroll.Effective` vs `EnsureRegistered` meaning a
never-registered-but-online till may render bundled-only on its very first
request; double-tap on a tile can start two concurrent installs of the same
listing, `PluginActive`'s known-inert idempotency check, ut-docs#1063) are
accepted as real-but-minor/deferred — none touch money, data loss, or
security, and each is either pre-existing (same pattern as #1055) or narrow
enough that a dedicated Backlog card is the better fix than folding into an
already-large diff. Filing a follow-up card for the pagination gap (#5) and
the negative-cache stall (#6) as part of this cycle's close-out.

## Verification beyond automated tests

- **TDD, both directions, for every fix**: each of the 4 findings above got
  a new regression test written first, confirmed to fail with the reviewer's
  exact reported symptom against the pre-fix code, then confirmed to pass
  after the fix (`TestSetupCatalogLanguageLocales_RejectsNonBareLocaleCode`,
  `TestSetupWizardCatalogAvailableLanguageSuppressesComingSoonNote`,
  `TestInstallBasePluginsForSetup_MergesWithLanguageStepPending`,
  `TestSetupWizardFormHasImplicitSubmitGuardBeforeCatalogTiles`).
- **Finding 4 (the implicit-submit hazard) was additionally driven in a
  real browser**, not just asserted on rendered markup: built and ran an
  actual till binary against a real fake-marketplace HTTP server serving a
  German listing, drove the wizard to step 4 (shop name) in Chromium,
  focused the store-name field, and pressed Enter.
  - **Before the fix**: confirmed the browser really did navigate to
    `/setup?lang=en&install_pending=de`, discarding the typed shop name —
    reproducing the reviewer's finding exactly, not just plausibly.
  - **After the fix**: same steps, no navigation, field value retained.
- **Visual check, both directions of the fix cycle**: screenshotted step 1
  offline (4 bundled tiles + the "more once connected" note, no catalog
  tile), online with a catalog-only German listing (bundled tiles + the
  `de` tile, same `.btn` styling, no overlap/misalignment), the same
  online state in Arabic/RTL (mirrored layout, no wrapping/overflow), and
  the install-pending note after a real failed install attempt (both
  before the fallback-locale-normalization fix — read literally as
  "continuing in en-US for now", inconsistent with every other bare-code
  tile — and after — "continuing in en"). All screenshots read correctly;
  none overlapped, wrapped mid-row, or cut off.
- Independently re-verified the Dev-reported TDD claims by reverting each
  fix and confirming the paired test failed with the exact reported error,
  then restoring: the `/api/setup/language` middleware exemption
  (`TestMiddlewareExemptsFirstBootPairingRoutes` — found and fixed by me
  during Tester, not the original Dev pass: the httptest-level handler
  tests never exercise the auth middleware at all, so the route worked in
  isolation and 401'd for real on an actual running server), the
  region-tag fallback normalization
  (`TestSetupLanguageInstall_FallbackNormalizesRegionTag`), the
  arbitrary-listing rejection, and the stale-cache-on-unreachable path.
- Full `go build ./...`, `go vet ./...`, `gofmt -l .`, and full
  `go test ./...` (not just the touched packages) — clean throughout every
  iteration of this review cycle, run again after all 4 fixes.
- All CI-blocking guards from `universal-till/CLAUDE.md`'s "Before
  committing" list — clean, including `guard-docs-shots.sh` after
  `make docs-shots` regenerated the manifest for the surface-hash bump.
- i18n: confirmed all 4 locale files carry the identical new key set with
  real (not English-left-untranslated) translations; RTL/logical-property
  safety confirmed (no new `left`/`right`, tiles reuse the existing `.btn`
  token, notes are wrapping `<p>` elements safe for long German/Arabic
  strings).
- Confirmed no scope creep against the Architect's explicit non-goals:
  `store.locale` permanent-default persistence, `PluginActive` idempotency
  internals, and new e2e infrastructure beyond the one appended spec were
  all left untouched.

## Not verified / accepted gap

- The AC's literal "verified on a real Pi: choose German on a fresh
  install" was not re-run against live `ut-cloud` production in this
  cycle (no such access from a cold cloud session) — every test here uses
  a fake/local marketplace fixture. The offline acceptance path and the
  install-failure/pending path were both driven end-to-end in a real
  browser against a real running till binary (see above); the *online
  install-succeeds* path is covered by Go-level tests exercising the real
  Ed25519 signature-verification + download-token flow
  (`TestSetupLanguageInstall_SuccessInstallsAndContinuesInLocale`) but not
  by a real browser against live infrastructure. Recommend one real
  online run against production `ut-cloud` before the Germany pilot
  retests this exact scenario.

## Safe-to-merge verdict

Yes, after the 4 fixes above (all applied, TDD-verified, full gate green).
