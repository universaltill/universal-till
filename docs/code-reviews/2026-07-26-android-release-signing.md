# 2026-07-26 — Android release signing & CI (ADR-0023)

## Context
The Android app (PR #62, merged 2026-07-25/26) was real and working but only
ever built/verified locally against an emulator — no CI build, no release
signing, nothing published anywhere a user could download it. Farshid's
explicit choice when asked: self-signed APK, direct sideload distribution,
no Google Play involvement (same "no Google contact" posture as the rest of
this app's design). This closes that gap end-to-end: a signing key, a CI
job that builds/signs/publishes on every release, and the website wired to
offer the download automatically.

## Design
- **Signing key**: a 4096-bit RSA key, self-signed via `keytool`, valid
  until 2053 (standard ~27-year validity for an Android release key — it
  can never be rotated without breaking updates for everyone who already
  installed the app, so it's generated once and kept indefinitely). Two
  durable copies: Azure Key Vault (`kv-unitill-dev`,
  `android-release-keystore-base64`/`-password`/`-alias`, disaster-recovery
  only, not Terraform-managed — a keystore isn't something
  `random_password` can generate, and this repo's IaC apply flow is
  manual/gated) and this repo's GitHub Actions secrets
  (`ANDROID_KEYSTORE_BASE64`/`ANDROID_KEYSTORE_PASSWORD`/
  `ANDROID_KEY_ALIAS`, the ones CI actually reads).
- `android/app/build.gradle.kts`: a `signingConfig` reads those three env
  vars; a release build is left **unsigned** (not silently debug-signed)
  when they're absent — a loud "can't install" failure beats a quiet trust
  gap where a locally-built "release" APK looks real but isn't.
  `versionCode`/`versionName` became overridable via Gradle `-P`
  properties so CI can stamp the real release version instead of the
  hardcoded dev default.
- `.github/workflows/release.yml`: new `android-app` job, shaped like the
  existing `macos-app` job (same `needs`/`if` resilience — runs whenever
  the release exists, regardless of whether `goreleaser` itself succeeded,
  so a goreleaser hiccup never costs the Android build either). Installs
  the Android SDK/NDK/JDK 17/gomobile, decodes the keystore secret to
  `$RUNNER_TEMP` (never the workspace), builds+signs via
  `./gradlew assembleRelease`, verifies the result is genuinely signed with
  `apksigner` before uploading, and attaches
  `unitill-pos_<version>_android.apk` to the release.
- `ut-website`: `download.html` gained an Android card and — the one
  actual behavior change beyond "add a new option" — the `detect()`
  function's Android branch changed from a deliberate `return null`
  (placeholder, no download offered) to a real match, moved ahead of the
  generic `/Linux/` check it would otherwise have been shadowed by
  (Android's user-agent also matches `/Linux/`). No other site code
  changes needed: the page already resolves real download URLs from the
  live GitHub Releases API by matching a `SUFFIX` substring against
  release asset names, so publishing `unitill-pos_<version>_android.apk`
  is sufficient on its own.

## Independent review
Two rounds — the initial CI/Gradle/website diff, then the fix for what it
found.

**One real bug, fixed**: `versionCode = MAJOR*10000 + MINOR*100 + PATCH`
collides once patch reaches 100 on a given minor line (`0.2.100` and
`0.3.0` both hash to `300`). Not theoretical: this repo's own tag history
was already 20+ patch releases deep on one minor line
(`v0.2.20`…`v0.2.39`) by the time this was written. Fixed with wider
per-component headroom, `MAJOR*1000000 + MINOR*1000 + PATCH` (patch/minor
now have room to 999 each, comfortably beyond any realistic release
cadence, still far inside Android's 32-bit versionCode range).

**Secondary, fixed**: the `workflow_dispatch` explicit-version input had no
format validation — a malformed value (e.g. `0.3.0-beta1`) would silently
misparse in the `versionCode` bash arithmetic (`IFS=. read` +
`$((...))` treats an unset/non-numeric captured group as `0`, no error)
rather than failing loudly. Fixed: the `prepare` job now validates
`^[0-9]+\.[0-9]+\.[0-9]+$` on any explicit version and fails the release
immediately with a clear message if it doesn't match, before anything
downstream (goreleaser's ldflags injection, the Android job) ever sees it.

**Confirmed correct, not just assumed**: the keystore is decoded only to
`$RUNNER_TEMP` and never echoed/logged anywhere; a missing/partial signing
secret produces an unsigned APK that fails loudly at the `apksigner
verify` step rather than silently uploading something broken; the
`android-app` job's working-directory handling (`cd android` scoped to one
step, `mv`/`gh release upload` back at repo root) is consistent; the
`detect()` reorder doesn't regress any other platform branch.

## Verification
`actionlint`/`shellcheck` on `release.yml` — clean (one pre-existing,
unrelated warning on a `macos-app` line dated 2026-07-16). Both inline
`<script>` blocks in `download.html` parse. `yamllint` on both workflow
files — no new warnings.

**Live-verified against a real build, not just "the YAML looks right"**:
built a real signed release APK locally (same Gradle invocation CI uses,
same keystore), confirmed with `apksigner verify --print-certs` that the
signature matches the exact key stored in Key Vault (SHA-256 fingerprint
`8B:D8:...:B4`), confirmed `aapt dump badging` shows the injected
version/versionCode landed correctly. Installed on a real (emulated)
device: the app launched, `libgojni.so` loaded, the real embedded Go
server booted (`Universal Till POS starting...` → `listening on
127.0.0.1:36405` in logcat — genuinely running, not just "APK installed").
**The actual upgrade guarantee this whole pipeline exists for** was tested
directly: built a second signed APK with a higher versionCode (same key),
installed it directly over the still-running first install with no
uninstall — succeeded, `dumpsys package` confirmed the version bumped in
place. This is the real-world scenario a shop's till needs to survive
(update without losing local data), and it works.

**One real environment gotcha worth recording**: the emulator hung
indefinitely on first boot attempts in this sandboxed session — not a
crash, just silently stuck at 0% CPU. Root cause: crashpad's opt-in
crash-report consent dialog blocking headless startup, waiting for a GUI
interaction that can never come without a display. Fixed by explicitly
disabling crash-report collection (`-no-metrics` +
`ANDROID_EMULATOR_ENABLE_CRASH_REPORTING=0`) — boots cleanly (~19s) once
that's set. Worth remembering for any future headless emulator use in
this kind of environment.

## What's still true from before
Physical hardware integrations, an in-app "stop the till" affordance, and
all of iOS remain out of scope (unchanged from `android/README.md`'s
existing "Not yet done" list). Everything about *signing and
distribution* specifically is now closed.
