# Android debug address bar exposed to end users (ut-docs#412)

**Branch:** `feat/412-hide-debug-address-bar` · **Reviewer model:** Sonnet (fresh-context subagent) — `complexity:easy` per the scrum-master skill's model-routing rubric (Sonnet builds, Sonnet reviews on easy cards, as long as it's a genuinely fresh instance).

## What shipped

An external tester on a real Android phone found a persistent status bar
reading `Running at 127.0.0.1:41855` on every screen of the Android wrapper
app, visible in release builds — an internal loopback bind address with no
use to a shop worker, eating scarce vertical space on a phone screen.

- `MainActivity.kt`: the on-screen status bar (`statusView`) is now gated
  behind `BuildConfig.DEBUG` — `View.GONE` in a real release build,
  `View.VISIBLE` only in a debug build.
- `TillService.kt`: the foreground-service notification (a genuine Android
  requirement, shown in every build) now uses a new string resource,
  `notification_running` ("Universal Till is running"), with no address
  interpolated — instead of reusing `status_running`, which still carries
  the address for the debug-only status bar.
- `strings.xml`: added `notification_running`.
- New guard `scripts/ci/guard-android-status-address.sh`, wired into
  `.github/workflows/ci.yml`, since this repo has no Android
  build/instrumented-test CI job to catch a regression at compile/runtime
  (`release.yml`'s `android-app` job only runs at release time, with a real
  SDK/NDK this pipeline's sessions don't have). Static, code-lines-only
  grep checks: (1) `statusView`'s visibility is gated on `BuildConfig.DEBUG`
  with the correct polarity (`if (BuildConfig.DEBUG) View.VISIBLE else
  View.GONE`, exact-string, not just token co-occurrence), (2) the
  notification's success path never calls `status_running` and always
  calls `notification_running`.

## Independent review — findings, severity, and resolution

Reviewed by a fresh-context Sonnet subagent (no access to the implementer's
reasoning), per the `reviewer` skill.

1. **BLOCKER (fixed) — `BuildConfig` would not have been generated.**
   AGP 8.7.3 (this module's version) makes `BuildConfig` class generation
   opt-in since AGP 8.0 — it requires `buildFeatures { buildConfig = true }`
   in the module's `android {}` block. No such block existed anywhere in
   `android/app/build.gradle.kts`. As originally written,
   `MainActivity.kt`'s `BuildConfig.DEBUG` reference would have failed to
   compile with "unresolved reference: BuildConfig" in **every** build,
   debug and release alike — this would have broken the Android app
   entirely, not just failed to fix the ticket.
   **Fix:** added `buildFeatures { buildConfig = true }` to the `android {}`
   block, with a dated comment. Re-verified by a second fresh-context
   subagent pass: block placement is syntactically correct (brace count
   balances), `namespace` matches `MainActivity`'s package so
   `BuildConfig` resolves with no import needed, and no other file/AGP
   interaction found. Cannot be verified by an actual Gradle build in this
   environment (no Android SDK/NDK available to any pipeline session) —
   this is DSL/spec-level reasoning, not an executed build. Flagged
   honestly rather than claimed as fully proven; this is the residual risk
   DevOps/a human should watch on the first real CI/release build of this
   change.

2. **Minor (fixed) — the original guard checked co-occurrence, not
   polarity.** The reviewer mutated a scratch copy to
   `if (!BuildConfig.DEBUG) View.VISIBLE else View.GONE` (bar shown in
   *release*, hidden in debug — the ticket's exact bug, inverted) and the
   original guard regex still passed, since it only checked that
   `BuildConfig.DEBUG` and `statusView.visibility` appeared on the same
   line. **Fix:** tightened the guard to an exact-string match on the
   correct polarity. Both the original reviewer and a second, independent
   re-verify subagent mutation-tested the tightened version: it now fails
   on the inverted mutant and passes on the correct code.

3. **Confirmed non-issues** (checked, not flagged): locale parity (only
   `values/strings.xml` exists — no `values-fa/tr/ar` yet, that's separate
   ticket ut-docs#414's job; adding one more English-only string ahead of
   it is fine), the WebView/listener path still receives the real address
   unchanged (only the *displayed* text changed), the failure-path
   notification (`status_failed`) is untouched and never carried the
   address, CI wiring is correct (new step alongside the other `guard-*.sh`
   steps, before the Go build), no SQL/money/repository-pattern surface
   touched, no manual (`web/help/`) topic exists or is owed for this native
   wrapper-only fix.

## Verified beyond automated tests

- `go build ./...` and `go test ./...` pass (this change touches no Go
  code directly; confirmed as a no-op regression check). One pre-existing,
  unrelated failure (`internal/issuereport`'s
  `TestSaveCleansUpDirectoryOnWriteFailure`, fails when tests run as root —
  confirmed present on unmodified `main` via `git stash`) was found
  incidentally and filed separately as
  [ut-docs#415](https://github.com/universaltill/ut-docs/issues/415), not
  fixed here (out of scope for this ticket).
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh` all pass, confirming this change doesn't touch
  any surface those guards cover.
- The new guard was mutation-tested twice, independently, against both the
  full-revert case and the inverted-polarity case (see above) — not just
  run once against the passing state.
- No Android SDK/NDK is available to any session in this pipeline, so the
  actual `assembleDebug`/`assembleRelease` Gradle build could not be
  executed here. This is the one verification step genuinely deferred to
  the real release pipeline (`release.yml`'s `android-app` job, which does
  have SDK/NDK) — flagged explicitly rather than silently assumed.

## Verdict

**Safe to merge.** The blocker found in the first review pass was fixed and
independently re-verified; the guard-tightening finding was fixed and
mutation-tested. No client/shop name or credential-shaped literal appears
anywhere in this diff.

## Explicitly deferred / out of scope

- Adding `values-fa/`, `values-tr/`, `values-ar/` Android string resources
  — ut-docs#414 (separate, already-groomed ticket).
- `internal/issuereport`'s root-run test fragility — ut-docs#415 (filed
  during this cycle, unrelated pre-existing issue).
- Real Gradle build verification of the `buildFeatures` fix — deferred to
  the release pipeline's own SDK/NDK-equipped job; no pipeline session can
  do this today.
