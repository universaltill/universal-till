# Code review — Android `generateAar` preflight + stale-artifact fix (ut-docs#1240)

- **Date:** 2026-08-29
- **Branch:** `fix/1240-android-generateaar-stale-artifact` (commit `f7bd60d`)
- **Reviewer:** independent reviewer (fresh-context Sonnet subagent, per
  `complexity:easy` routing — did not write the code), read-only pass over
  the diff plus the surrounding ~150 lines of `android/app/build.gradle.kts`
  and this repo's `CLAUDE.md`.
- **Verdict at first pass: 1 BLOCKING finding.** Fixed same session, scoped
  re-check by the dev (not a second independent round — see below).

## What shipped

`android/app/build.gradle.kts`'s `generateAar` task had two compounding
traps, both found live during the ut-docs#1239 device verification
(2026-08-28):

1. `generateAar` shells out to `gomobile bind`, which itself shells out to
   `gobind` as a separate PATH lookup. After a Go toolchain upgrade,
   `gobind` can still resolve via `go tool gobind` while being completely
   invisible to `gomobile` — Gradle's `Exec` then fails with the generic
   "A problem occurred starting process 'command gomobile'", with no hint
   at the actual fix.
2. A failed `generateAar` run left whatever `.aar` (and downstream APK) was
   already sitting in `libs/`/`build/outputs/` from a prior successful
   build untouched — a broken bind looked like success, and a stale
   pre-fix `.aar` got packaged and installed onto a real device, producing
   confusing symptoms (an app that booted because its embedded server
   predated a since-shipped migration).

Fix: a `doFirst` block on `generateAar` that

- deletes `libs/unitill-mobile.aar` **first**, before anything that could
  throw, so a failed/partial run can never leave a packagable stale
  artifact behind regardless of which check fails;
- then checks both `gomobile` and `gobind` are present and executable
  somewhere on `PATH`, and if not, throws a `GradleException` naming the
  exact fix (`go install golang.org/x/mobile/cmd/gomobile golang.org/x/mobile/cmd/gobind`)
  instead of letting `Exec` fail with the generic message.

The pre-existing `commandLine(...)` (still `-target=android/arm64,android/arm`,
`-androidapi 24`) and `outputs.upToDateWhen { false }` are untouched — a
clean build still regenerates the `.aar` with exactly the two real-phone
ABIs, and the task is never skipped as up-to-date, so the new `doFirst`
logic always runs.

## Independent review — what was actually checked

Environment constraint stated up front by the reviewer and confirmed
independently by the dev: this sandboxed session has **no Android SDK and
no network access to Google's/Maven's Gradle plugin repositories** —
`./gradlew help` fails during plugin resolution
(`Plugin [id: 'com.android.application', version: '8.7.3', apply: false]
was not found in any of the following sources` — Google/MavenRepo/Gradle
Central all searched, none reachable through this session's proxy). So
neither the dev nor the reviewer could run a real Gradle/Android build
here; both passes are careful manual reading of the Kotlin DSL, not an
executed build. `.github/workflows/ci.yml`'s `build` job doesn't build
Android at all (only `guard-android-status-address.sh` and
`guard-android-i18n.sh`, which scan `strings.xml`/`MainActivity.kt`/
`TillService.kt` — neither touches this file); the only job that actually
runs `./gradlew assembleRelease` with a real Android SDK is
`release.yml`'s `android-app` job, gated on a version tag push. So this
change's first real build-verification happens at the next Android
release, not in this PR's own CI.

### Finding 1 — BLOCKING: the delete was unreachable in exactly the failure mode the issue is named after

First draft ordered the `doFirst` block as: PATH-check loop (which can
`throw GradleException(...)`) → then `file(...).delete()`. When
`gomobile`/`gobind` is missing from PATH — the specific trigger in the
issue title ("gomobile PATH failure surfaced it") — the function threw and
returned *before* ever reaching the delete call. The comment directly
above the delete line claimed it handled "the preflight check above," but
the code didn't: a stale `.aar` from a prior successful build was left
untouched for precisely the scenario the card was filed against, missing
acceptance criterion 2 for that failure mode.

**Fix applied:** moved `file("libs/unitill-mobile.aar").delete()` to the
very first statement in `doFirst`, ahead of the PATH-check loop, so
deletion happens unconditionally before anything that could throw.
Re-diffed and confirmed the delete now precedes the loop with no other
change to the check's logic or message.

### Findings 2–3 — non-blocking, not applied

- `PATH.split(":")` assumes a POSIX path separator. Correct for this
  repo's actual dev/CI targets (Linux/macOS, Go/gomobile/NDK tooling);
  not worth a cross-platform abstraction for a build script nobody runs
  on Windows today.
- `delete()`'s boolean return value is ignored — if deletion fails
  (permissions, file locked), the task proceeds silently. `gomobile bind`
  writing to the same path would then either overwrite it (fine) or fail
  loudly (also fine; the stale file would linger only in that narrow,
  unlikely case). Not worth the added complexity for an easy-tier fix.

### Explicitly checked, no problem found

- `doFirst` runs before `Exec`'s main action (the actual `commandLine`)
  regardless of where it's declared relative to `commandLine(...)` in the
  configuration block — placement is correct.
- `outputs.upToDateWhen { false }` (pre-existing, untouched) still makes
  sense and is required for the new logic to run on every invocation.
- `GradleException`, `file()`, `System.getenv`, `File.isFile`/
  `canExecute()` are all used correctly — `org.gradle.api.*` is a default
  Kotlin-DSL import (no missing import), `isFile` is Kotlin's synthetic
  property view of Java's `isFile()`, `canExecute()` is correctly called
  as a method (not a getter-shaped name).
- No `environment(...)` override elsewhere on this `Exec` task, so the
  `PATH` the preflight inspects via `System.getenv("PATH")` is the same
  `PATH` the subsequent `commandLine` exec actually inherits.
- The delete target (`file("libs/unitill-mobile.aar")`, resolved relative
  to `android/app`) is byte-identical to the pre-existing
  `outputs.file(file("libs/unitill-mobile.aar"))` declaration a few lines
  below — targets the real declared output, not a different path.
- No CI-blocking guard applies: this is a build script, not
  `internal/**` Go code, template markup, or a translated string —
  `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh` etc. are all out of scope for this diff.
- No accepted ADR governs this (checked `ut-docs/adr/`); ADR-0023
  (android/ios till strategy) is the relevant background doc and this
  change doesn't contradict it — it's cited directly in the new error
  message for where to find real setup steps.
- No real client/shop name, no secret-shaped literal anywhere in the diff.

## Scope not covered by this fix (deliberately)

- **Preflight message doesn't check ANDROID_HOME/NDK.** Out of scope —
  the issue is specifically about the gomobile/gobind PATH trap that bit
  a real session; a missing Android SDK/NDK already fails loudly and
  separately, earlier in AGP's own plugin-apply phase, before this task
  ever runs.
- **No automated test exercises this Gradle logic.** Not feasible in this
  environment (no Android SDK, no network to the plugin repos) — the
  acceptance criteria ("a build with gomobile missing fails with an
  actionable message AND leaves no packagable stale .aar behind") can
  only be exercised with a real Gradle+Android toolchain. Deferred to the
  next actual Android build (a release cut, or a developer with local
  Android tooling) to confirm end-to-end; noted as a known verification
  gap in the PR body.

## Summary of required actions before merge

1. ~~Fix the delete/throw ordering (Finding 1, blocking).~~ Done.
2. None outstanding.
