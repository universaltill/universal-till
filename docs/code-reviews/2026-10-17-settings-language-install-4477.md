# 2026-10-17 — Settings → Language: install a marketplace language on select (ut-docs#4477, ADR-0160 §6)

## What shipped
- **Language list.** Settings → Language keeps the installed locales and adds a
  "More languages (download)" group of catalog language packs the till
  doesn't have. It uses the setup wizard's list builder
  (`installableSetupLanguages`, ut-docs#1092).
  - **No group offline.** Nothing is offered when netreach reports the cloud
    unreachable, or when no catalog was ever fetched.
  - **No group on an additional till.** The shop language comes from the main
    till, so the download belongs there.
  - **The render never waits on the network.** `GET /settings` reads only the
    cached catalog and, when that cache is cold or expired, refreshes it in the
    background (at most one refresh in flight, tracked on `Deps.AsyncWork`).
- **Pick + Apply.** `POST /api/settings/save` re-checks the pick against the
  catalog list, so a form can't name an arbitrary listing.
  - After the save succeeds, the pack is queued on the ut-docs#591 pending
    list and the retry loop is nudged. This is the same path as a country
    change. There is no second installer, and the install still goes through
    Ed25519 verification.
  - The pick is recorded as `setup.pending_base_plugins_locale_switch` =
    `<locale>@<unix pick time>`. The key is per-till, under the existing
    `setup.pending_base_plugins` prefix.
  - `store.locale` is unchanged until the pack arrives.
- **Status chip.** The Language card mounts `GET /ui/settings/language-install`
  (`partials/settings_language_install.html`). It is a chip, never a modal.
  - **installing:** polls every 3 s.
  - **ready** (the pack is installed and the pick is less than an hour old):
    posts the card's own save once, so the switch takes the normal path
    (audit, `locale_confirmed`, locale generation and cookie retirement), then
    reloads.
  - **unavailable** (the pack left the queue without arriving): shown once,
    then cleared.
  - Any explicit language save clears the pending pick.
- **i18n and help.** 5 new `settings.language.*` keys in en/tr/ar/fa plus
  metadata. Help `display.md` step 2 updated in en/de/tr/ar/fa.
  ut-plugin-language-de/es/pt follow-up PRs carry the same keys.
- **Tests.**
  - `internal/pages/settings_language_install_test.go`: 9 tests, including the
    full pick → retry-tick install → ready chip → apply flow against a fake
    marketplace that installs a real pack.
  - e2e `catalog-language-download-4477.spec.ts` (`diagnostics` project).

## Review
Independent review on a different model from the author. The author was
Opus 5.5. Fable was refused (out of usage credits), so the review ran on
Sonnet, per MODEL-ROUTING. Verdict: no blockers.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | A stale pick auto-applied whenever Settings was next opened, even days later, switching the whole shop. The help text said otherwise. | **Fixed.** The pick time is stored, and auto-apply is limited to 1 h (`languageSwitchApplyWindow`). An older or untimed pick is dropped, and the language stays in the list as installed. Help text in all 5 languages now matches. `TestSettingsLanguageStaleSwitchIsNotApplied` fails when the window check is removed. |
| 2 | major/minor | `GET /settings` could block for up to 3 s on a cold or expired catalog. The fetch runs under the cache mutex, so concurrent renders queued behind it. | **Fixed.** The render is non-blocking and the refresh runs in the background (`settingsInstallableLanguagesCached`). `TestSettingsLanguageListsCatalogLanguagesForDownload` asserts that the first render doesn't fetch, that a background refresh happens, and that the next render lists the language. |
| 3 | minor | A pick that is no longer offered by the time Apply is pressed is a silent no-op (204). | **Accepted.** This is the handler's existing lenient contract for an unknown locale (ut-docs#861). The window is a page held open across going offline or a delisting. |
| 4 | minor | Corrupt pending-list JSON leaves the chip polling while the page is open. | **Accepted.** It does no harm, and the next queue write resets an unreadable list (`addPendingBasePlugins`). |
| 5 | minor | e2e: a hash-only `goto` is a same-document navigation, so the chip check relied on poll timing. | **Fixed.** The spec reloads explicitly. The e2e run also found that the other `diagnostics` specs' fakes answer the catalog with an empty list, which the till caches for 5 min. The spec is named to sort first in that project, and the reason is documented in the spec and the config. |
| 6 | nit | No test for the additional-till case or for the elevated re-post. No review record yet. | The reviewer read the code and confirmed the elevated re-post queues (the elevation prompt re-posts `locale`). The additional-till branch is one call to the existing `tillFollowsMain`, so it was not tested separately. This file is the review record. |

Checked by the reviewer with no finding: the ordering between the chip and the
retry tick; elevation (the chip's `canPerform("settings")` matches the save's
gate, and cashiers get nothing); injection (html/template, `isPlausibleLocale`,
native names from `httpx.NativeLanguageName`); per-till classification; the
settings-write guard; and translation placeholder parity.

## Verified beyond unit tests
- **TDD.** With the save-handler hook removed, the flow and unavailable-chip
  tests fail; restored, they pass. With the stale-window check removed, its
  test fails.
- **Race detector.** `go test -race` passes on the settings/setup language
  tests.
- **e2e, whole `diagnostics` project.** All 9 specs pass, including the new
  one. It drives pick → reload → "Downloading Deutsch…" chip (role=status, no
  open dialog), then removes the pending download under Settings → Data, then
  the "couldn't be downloaded" chip, which shows once.
- **Screenshots, looked at:**
  - 1024×600 en: the chip and note sit under the select. No overlap, and the
    chip wraps inside the card.
  - 360 px: the chip is asserted to sit inside the card, and the page has no
    sideways scroll. In the card screenshot the fixed status bar covers the
    card's lower edge, which is the existing page behaviour.
  - fa (RTL): the layout mirrors correctly. The Latin "Deutsch" inside the
    Persian sentence is readable.
- **Not checked:** the "ready → applied" switch in a real browser. A real
  signed pack from a fake marketplace exists only in the Go harness, which
  covers it end to end through the handlers. Also not checked: real touch
  hardware, dark theme.
- **Gate.**
  - `gofmt`, `go build`, `go vet`: clean. `golangci-lint`: 0 issues.
  - `go test ./...`: green. `internal/plugins` needed `-timeout 30m` on this
    container; it is untouched.
  - Every guard in `ci.yml`'s build job passes, except two that fail the same
    way on `main` in this container: `guard-deadcode-baseline` (the desktop
    root is skipped without GTK headers) and `guard-shellcheck-version`
    (shellcheck is not installed). No shell scripts changed.

## Deferred
- None filed. The bidi isolation of a Latin language name inside an RTL
  sentence is cosmetic, and the same pattern exists in the wizard's
  `setup.language.install_pending`.

Safe to merge.
