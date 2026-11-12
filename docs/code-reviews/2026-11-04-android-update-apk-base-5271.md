# 2026-11-04 — Android in-app update base as a Gradle build value (ut-docs#5271)

**What shipped.** `android/app/build.gradle.kts` reads the APK self-update
base from `-PupdateApkUrlBase` (`findProperty`, same pattern as
`utApkSignerPins`). Today's URL stays the default (`defaultUpdateApkUrlBase`),
so a plain build produces the same `BuildConfig` values. A top-level
`require(isValidUpdateApkUrlBase(...))` fails configuration on a bad value
and never falls back to our releases: https only, ends in `/`, lower-case
host, optional port 1-65535, no userinfo/query/fragment/`%`/dot segments,
a character allow-list that cannot break the `buildConfigField` string,
max 512; on `github.com` the path must be `/<owner>/<repo>/releases/latest/download/`.
All per-flavor `UPDATE_APK_URL`s and the universal build's per-ABI table
derive from the base. `release.yml` passes
`-PupdateApkUrlBase=https://github.com/$GITHUB_REPOSITORY/releases/latest/download/`
(byte-identical to the default for us; a GitHub fork updates from itself),
or the optional repo variable `UT_ANDROID_UPDATE_APK_URL_BASE` for a
non-GitHub mirror; a one-line/no-whitespace check runs before the value
reaches `GITHUB_ENV`, and each built APK's dex must contain the base
(same `grep -acF` mechanism as `check-apk-signer-pin.sh`).

Tests/guards: new JVM test `UpdateSourceBuildConfigTest` pins the default
BuildConfig URLs; `android-ci.yml` adds a `testUniversalDebugUnitTest` run
with the property set and `scripts/ci/check-android-update-base.sh`
(15 bad / 4 good values through `./gradlew help`).
`guard-android-abi-packages.sh` (+ `_test.sh`, 12 cases) now pins the
default literal, the property read, the `require`, no other https literal,
no use of the default outside the `?:` fallback, and the release.yml flag.
Its comment stripper became `(?<!:)//` so it no longer cuts URLs at `https:`.

**Pre-flight:** web/help topic: n/a (build-time only, no till behaviour
change). `make docs-shots`: n/a. reference/ page: updated
(ut-docs `reference/independent-operator.md`, ut-docs PR on
`docs/5271-android-update-base`). README: n/a (no platform claim changed);
`android/README.md` and `RELEASING.md` updated. en.json keys: n/a.
Lang packs: n/a. ADR: n/a (existing build-value pattern, ADR-0179 trust
unchanged). Review record: this file. Follow-ups: none.

**Phases.** Architect brief Opus 5.5; Dev Sonnet; Tester Sonnet (separate
agent); Review Opus 5.5 (separate agent, did not write the code).

**Review findings and outcome.**
- M1 (fixed): new `_test.sh` sed line tripped shellcheck SC2016, which would
  turn the `build` job red; the pattern now matches `=[^ ]*`. `shellcheck
  scripts/ci/*.sh` → 0 issues.
- L1 (fixed): repo variable written to `GITHUB_ENV` before validation (a
  newline could add env lines) → regex pre-check before the write.
- L2 (accepted): the dex check cannot catch a missed flag in our own release
  (base == default); the guard's release.yml check covers that.
- L3 (accepted): a GitHub repo name starting with `.` is refused; fails
  closed, unlikely.
- L4 (fixed): the check script reused one Gradle daemon instead of 19
  `--no-daemon` runs, then `--stop`s it.
- Tester gap (fixed, test first): `defaultUpdateApkUrlBase + "..."` bypassed
  the property and passed the guard → new guard rule + case 12 (seen failing,
  then passing).

**Verified.** Guard + `_test.sh` all cases pass; guard OK on the real tree;
Tester mutation run in a throwaway copy (8 mutations, all rejected);
validator copied into a scratch Gradle build: 15/15 bad rejected, 4/4 good
accepted, plus ~18 adversarial values rejected; release.yml parses;
ci.yml guards touching changed files pass. **Not verifiable here** (no
Android SDK, dl.google.com blocked): real Gradle configuration, the
BuildConfig unit test and the property run → `android-ci.yml` on the PR;
the dex check → the next release run.
