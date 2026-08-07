# Android wrapper UI is English-only (ut-docs#414)

**Branch:** `feat/414-android-wrapper-i18n` · **Reviewer model:** Opus — `complexity:medium` per the scrum-master skill's model-routing rubric. This card was escalated from `complexity:easy` mid-cycle, before build, because AC #2 ("the wrapper follows the till's configured locale") turned out to need a real runtime locale-sync mechanism, not just static translation files — exactly the part the Opus review focused hardest on and found real blockers in.

## What shipped

The Android wrapper's own native chrome (status text, notification text,
the launcher label — `android/app/src/main/res/values/strings.xml`) had
no `values-fa/`, `values-tr/`, `values-ar/` translations, even though the
web UI loaded inside its WebView is fully translated (846+ keys, en/fa/
tr/ar, enforced by `guard-i18n.sh`).

- Added `values-fa/`, `values-tr/`, `values-ar/strings.xml` with all 6
  keys translated (`app_name` matches each locale's own existing
  `web/locales/*.json` `app.name` choice — `fa` translated, `tr`/`ar`
  keep the literal brand name, exactly mirroring the web side).
- `android:supportsRtl="true"` added to `AndroidManifest.xml`.
- **Locale-sync mechanism** (the part this card was escalated for):
  `MainActivity`'s `WebViewClient.onPageFinished` reads
  `document.documentElement.lang` off the loaded page (the same value
  `base.html`'s `<html lang="...">` — and `web/locales/*.json` — are
  driven by) and applies it via `AppCompatDelegate.setApplicationLocales`,
  so the native wrapper follows the till's own configured language, not
  just the phone's device locale.
- New `scripts/ci/guard-android-i18n.sh` (wired into `ci.yml`), mirroring
  `guard-i18n.sh`'s key-parity + placeholder-integrity check for the
  Android resource files.

## Independent review — two real blockers found and fixed

Reviewed by a fresh-context Opus subagent, which verified claims against
the actual AndroidX source and a real JVM (BCP-47 round-tripping), not
from memory — this is what caught both blockers below; neither would have
surfaced from reading the diff alone.

1. **BLOCKER (fixed) — AC #2 didn't reach any string a real (non-debug)
   user actually sees.** `AppCompatDelegate.setApplicationLocales()`'s own
   javadoc states it "does not work for non-AppCompatActivity context" on
   API 24-32 (this app's `minSdk` is 24) — a plain `Service`'s own
   `getString()` does NOT follow it on those levels. `MainActivity`'s
   locale-aware strings (`status_starting`/`running`/`failed`) are
   `View.GONE` in release builds (per #412), so the ONLY strings a real
   user sees — `TillService`'s persistent notification title/text/channel
   name — were the ones this mechanism never reached, on the majority of
   this app's supported OS range. Compounding it on every API level: the
   notification is built once in `TillService.onCreate` and never
   re-posted, so even where the per-app locale did apply, it wouldn't
   reach an already-running notification until the next process restart.
   **Fix:** every `TillService` string lookup now goes through a local
   `str(resId)` helper wrapping `ContextCompat.getString` — the AndroidX-
   documented remedy for exactly this gap — and a new
   `refreshLocalizedNotification()` method (channel re-created with the
   same ID, notification re-posted) that `MainActivity` calls immediately
   after applying a detected locale change, so the notification catches
   up without waiting for a restart it might never get.
2. **BLOCKER (fixed) — the "persists across restarts" claim was false,
   causing an extra Activity recreate on EVERY cold launch, not just a
   genuine language change.** `AppCompatDelegate`'s auto-storage on API
   24-32 is opt-in via an `AppLocalesMetadataHolderService` manifest
   declaration, which the first draft never added — so
   `AppCompatDelegate.getApplicationLocales()` was always empty at process
   start, permanently mismatching the just-loaded page's locale and
   triggering `setApplicationLocales` (and the recreate/WebView-reload it
   causes) on every single app start, in every locale including English.
   **Fix:** added the manifest declaration (`android:enabled="false"`,
   never started — AndroidX only reads its metadata) to opt into real
   persistence, matching the original claim's intent.
3. **MEDIUM (fixed) — the locale-match comparison compared a canonicalized
   tag against unvalidated raw DOM text.** `internal/httpx.ResolveLocale`
   accepts any `?lang=` value unvalidated and writes it straight into
   `<html lang="...">` — a value like `"EN"`, `"fa_IR"`, or garbage would
   either never satisfy the (then string-literal) comparison, re-firing
   `setApplicationLocales` on every navigation, or reach
   `LocaleListCompat.forLanguageTags` as outright garbage. Verified: does
   not infinite-loop (AndroidX and the OS `LocaleManager` both dedupe
   internally), but the comparison was wrong as written. **Fix:** gated on
   a fixed `KNOWN_LOCALES` set (`en`, `fa`, `tr`, `ar` — this app's actual
   shipped translations, matching `internal/httpx.AvailableLocales()`)
   before ever touching `AppCompatDelegate`, and normalized both sides of
   the comparison through the same `LocaleListCompat` round-trip.
4. **LOW (fixed) — three guard-script robustness gaps, all mutation-
   demonstrated by the reviewer, not theoretical:**
   - Glob-matching ALL of `values-*/` treated any non-locale resource
     qualifier (`values-night/` — this app's own
     `Theme.AppCompat.DayNight` theme makes this a plausible near-term
     addition — plus `values-v33`/`values-land`/`values-sw600dp`) as a
     locale missing every key, a guaranteed false failure. Fixed with a
     regex matching Android's actual locale-qualifier grammar
     (`fa`, `en-rUS`, `b+en+US` shapes), skipping (not failing on)
     anything else, with a note printed either way.
   - A `<plurals>` element added to the base with zero translations
     passed silently (only `<string>` elements were read). Fixed:
     `load()` now also reads `<plurals>`/`<item quantity>`, checks the
     plural NAME exists on both sides (quantity-category sets legitimately
     differ across languages' CLDR rules, so those aren't compared 1:1),
     and checks placeholder parity for whichever quantities exist on both.
   - Reading `el.text` alone (not `el.itertext()`) silently truncated a
     string using inline markup at the first child tag — on BOTH sides of
     a comparison, which then produced a message blaming the wrong side.
     Fixed by reading full text content via `itertext()`.
5. **LOW (fixed) — bidi rendering bug in the Arabic notification string.**
   `notification_running`'s first strong character was Latin
   ("Universal"), so the bidi algorithm resolved the whole line as LTR,
   rendering left-anchored inside an otherwise-RTL notification shade —
   genuinely user-visible in a release build. Fixed with a leading RLM
   (U+200F, invisible) forcing RTL paragraph resolution.
6. **Guard I (this reviewer's own pre-existing #412 guard) went stale**
   once `TillService` started calling `str(...)` instead of `getString(...)`
   directly — updated `guard-android-status-address.sh`'s pattern to
   accept either call shape (it protects which STRING RESOURCE feeds the
   notification, not which function fetched it), re-mutation-tested to
   confirm it still catches a reversion to the address-bearing string.

## Verified clean by the reviewer (no action needed)

Translation quality and idiom (Farsi ZWNJ usage, Turkish vowel harmony,
Arabic tanween — all correct, cross-checked by someone who reads the
languages), the `app_name` web-locale-consistency claim (verified against
the actual JSON, not trusted), `evaluateJavascript`'s JSON-encoding parse
(handles `null`, empty string, and a normal tag correctly), no mid-sale
interruption risk from the recreate (`TillService`'s state is untouched;
the bind/listener/loadUrl sequence re-fires exactly once, idempotently),
`activity_main.xml` needs no additional RTL-specific attributes beyond
`supportsRtl`, CI wiring, no SQL/money/repository-pattern surface touched,
no `web/help/` topic exists or is owed (native-wrapper-only, same
precedent as #412).

## Verified beyond automated tests

- `go build ./...` clean (no `.go` changes in this diff).
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-android-status-address.sh`
  (updated), `guard-android-i18n.sh` (hardened), `guard-docs-shots.sh`,
  `guard-help-topics.sh` all pass.
- `guard-android-i18n.sh` mutation-tested against: a missing key, an
  orphan key, a dropped placeholder, a `values-night/` non-locale
  directory (must be skipped, not failed), and a base-only `<plurals>`
  with zero translations (must fail) — all five behave correctly.
- `guard-android-status-address.sh` mutation-tested against a reversion to
  the address-bearing notification string — still fails correctly after
  the call-shape update.
- **Not verified**: an actual Gradle build, or the locale-sync mechanism
  on a real device — no Android SDK/NDK is available to any session in
  this pipeline, and `android/` has no test sources at all (pre-existing,
  not introduced here). The S1/S2 fixes are reasoned from AndroidX's own
  source and documented API contracts, not empirically demonstrated on
  hardware. Stated plainly since AC #2's escalation to `medium` was
  justified by exactly this class of runtime behavior.

## Verdict

**Safe to merge**, with the hardware-verification caveat above stated
explicitly rather than silently assumed. Both real blockers are fixed and
addressed at the root (AndroidX's documented remedy, not a workaround);
all lower-severity findings fixed except where explicitly noted as
low-risk and accepted.

## Explicitly deferred / out of scope

- Real-device verification of the locale-sync mechanism — no pipeline
  session can do this; worth a note for the next person doing a manual
  release-build check on real hardware.
- `#412`'s own follow-up: `internal/issuereport`'s root-run test
  fragility — unrelated, already tracked as ut-docs#415.
