# Review: Play bundle native debug symbols + sideload install-permission check (ut-docs#6935)

Date: 2026-11-23. Complexity: easy, one review pass.
Author model: Sonnet 5.5 (Dev). Reviewer: Opus 5.5 (independent, inline,
fresh read of the uncommitted diff on `feat/6935-aab-native-symbols`).
Tester: separate agent, PASS (one nit, judged below).
Verdict: **approve**, with one documentation fix by the Reviewer. The real
proof (AGP actually emits the symbol files) only exists on android-ci / the
play-publish dry run: see "Unverified until android-ci".

Pre-flight: build/CI change only. Till user-visible behaviour n/a (no UI, no
`web/help/` topic); `reference/` n/a; `android/README.md` updated (play-bundle
symbols paragraph by Dev; NDK prerequisite line by Reviewer, finding 1); no
`en.json` keys; platform/feature claim n/a (Play listing unchanged); plan/price,
ADR, plugin/manifest-field surfaces n/a (implements ADR-0194/#6409 as decided,
no new mechanism, so no ADR); follow-up work: finding 2 → new Backlog card.

## What shipped

- `android/app/build.gradle.kts`: `ndkVersion = "28.2.13676358"` (the NDK all
  three Android workflows install with `sdkmanager`; AGP 8.7.3's default 27.x is
  not installed in CI, so without the pin AGP finds no `objcopy`/`strip` and
  only warns), and the `play` flavor's `ndk { debugSymbolLevel = "SYMBOL_TABLE" }`.
- `android/scripts/check-aab.sh`: every `base/lib/<abi>/<lib>.so` needs
  `BUNDLE-METADATA/com.android.tools.build.debugsymbols/<abi>/<lib>.so.sym`
  (or `.so.dbg`), else FAIL. New `android/scripts/check-aab_test.sh` (stub
  `aapt2`, synthetic zips, all in one `mktemp -d`), run in `ci.yml` build job.
- New `scripts/ci/check-android-sideload-install-permission.sh` (+ `_test.sh`,
  `ci.yml` build job): a merged manifest without
  `<uses-permission android:name="android.permission.REQUEST_INSTALL_PACKAGES">`
  (comments stripped) fails. Run on the arm64/universal/armv7 Debug merged
  manifests in `android-ci.yml` and on each sideload Release merged manifest in
  `release.yml` (inside the per-flavor loop, right after `$MERGED` is set and
  checked non-empty, under the step's `set -euo pipefail`; play is not in that
  loop).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Should | Pinning `ndkVersion` makes 28.2.13676358 the NDK a local build needs, but `android/README.md` Prerequisites still said "an NDK version". A dev with only another NDK gets AGP's NDK-missing path (auto-install where SDK licences allow, else a strip/extract warning and a play bundle that now fails `check-aab.sh`). | Fixed: Prerequisites name the pinned `ndk;28.2.13676358` and why. |
| 2 | Should (pre-existing, out of scope) | `-PtapToPay=true` replaces the arm64 manifest: `build.gradle.kts:308` `getByName("arm64").manifest.srcFile("src/ttpReal/AndroidManifest.xml")` runs after `:285` set `src/sideload/AndroidManifest.xml`, so a TTP-armed arm64 build loses `REQUEST_INSTALL_PACKAGES` and the updates FileProvider (self-update broken). Ships dark today (TTP off by default), so no released APK is affected; the new check is deliberately not added to android-ci's TTP step (it would go red). | Deferred → new Backlog card (orchestrator files it): merge the TTP overlay instead of replacing the sideload manifest, then add `check-android-sideload-install-permission.sh` to the TTP step. |
| 3 | Nit | Tester: `tools:node="remove"` on the permission still passes the sideload check. | Accepted, no change: the script's only inputs are merged manifests, where the merger has already dropped removed elements and stripped `tools:` attributes. Usage text says "merged". |
| 4 | Info | Symbol rule covers `base/` only and one directory level (`base/lib/<abi>/*.so`). | Accepted: no dynamic-feature modules; Android packages only `lib/<abi>/*.so`. |
| 5 | Info | `debugSymbolLevel` on a product flavor (not the release buildType). | Accepted: matches the card. If AGP were to ignore the flavor-level value, check-aab goes red on android-ci (fail loud, not silent); fallback is `buildTypes.release { ndk { debugSymbolLevel = "SYMBOL_TABLE" } }`, which costs sideload APKs nothing (extraction only feeds `bundle*`). |

Checked and clean:
- **Will AGP emit `libgojni.so.sym` for an already `-s -w` lib?** AGP's
  ExtractNativeDebugMetadata task runs NDK `llvm-objcopy` over every merged
  native lib (AAR `jni/` libs included, which is where gomobile's
  `libgojni.so` comes from) and writes `<abi>/<lib>.so.sym` for
  `SYMBOL_TABLE`; I know of no skip for a lib that has no `.symtab`. A Go
  `c-shared` lib keeps `.dynsym` after `-s`, so the output is a small but valid
  ELF, which is what Play needs to clear its warning (symbolication quality is
  limited to exported symbols: the card's stated non-goal). The two ways this
  could go red are an unfound NDK (fixed by the pin; previously AGP only
  *warned* "unable to locate objcopy") and the flavor-level DSL (finding 5).
  I could not fetch AGP source to confirm (android.googlesource.com is not
  reachable from this session), so this is reasoned, not proven.
- **Pin and sideload APKs:** before, AGP's default NDK was absent in CI, so
  `strip*DebugSymbols` warned and packaged `libgojni.so` as-is; now
  `llvm-strip --strip-unneeded` runs. Expect a negligible size change (`-s -w`
  already removed `.symtab`/DWARF). Stripping a gomobile lib is the normal path
  for any app with an NDK installed, and android-ci's debug builds (stripped
  too) exercise it. `ANDROID_NDK_HOME` (gomobile) and `ndkVersion` (AGP) point
  at the same `$ANDROID_HOME/ndk/28.2.13676358` in all three workflows.
- **check-aab.sh:** the new block sits under the existing `set -euo pipefail` +
  ERR trap; `grep` failures are in `if`/`|| true`; `grep -F` so dots in lib
  names are literal; `--help` range `2,25` ends at the header end.
- **Sandboxing (CLAUDE.md, #4311):** both `_test.sh` write only under one
  `mktemp -d` with an EXIT trap, never the real tree;
  `guard-ci-tests-sandboxed.sh` exit 0.
- **Real manifests:** `src/sideload/AndroidManifest.xml` → exit 0;
  `src/main/AndroidManifest.xml` (permission only in a comment) → exit 1.
- No secrets, no shop names, files `+x`, `git diff --check` clean.

## TDD claims re-verified (inline, in a `mktemp -d` copy, shared tree untouched)

- `check-aab_test.sh` against `HEAD`'s `check-aab.sh` (symbol section absent):
  3 of 4 cases FAIL ("no symbols fail: exit 0, want 1", "only arm64 symbols
  fail ...: exit 0, want 1", "both ABIs pass: output lacks 'native debug
  symbols'"); against the new script: all pass.
- Tester's mutation set (M1-M5) covers the sideload script and the `.dbg`
  alternative; not repeated.

## Gate run

- `bash android/scripts/check-aab_test.sh` → 4/4 PASS.
- `bash scripts/ci/check-android-sideload-install-permission_test.sh` → 7/7 PASS.
- `shellcheck scripts/ci/*.sh android/scripts/check-aab_test.sh` → 0 issues;
  `android/scripts/check-aab.sh` → 6 infos, identical count to `HEAD` (pre-existing).
- `guard-ci-tests-sandboxed.sh` → exit 0.
- No Go change: `go build`/`go test` not affected; no Android SDK here, so no
  Gradle run.

## Unverified until android-ci

These need the real runner (SDK, NDK 28.2.13676358, AGP 8.7.3) and are the
merge gate for this PR:
1. android-ci `bundlePlayRelease` succeeds with the pin and the flavor DSL.
2. `check-aab.sh` on the real bundle prints `ok: native debug symbols for
   arm64-v8a/libgojni.so` and `.../armeabi-v7a/libgojni.so` (i.e. the
   `BUNDLE-METADATA/com.android.tools.build.debugsymbols/<abi>/libgojni.so.sym`
   layout is what AGP really writes). If it fails, read the Gradle log for
   "Unable to extract native debug metadata" (NDK not found) before touching
   the check; then try finding 5's buildType fallback.
3. The sideload check prints `ok:` for arm64/universal/armv7 Debug merged manifests.
4. play-publish dry run (`workflow_dispatch`, no upload) green with the same
   check-aab assertion; release.yml's new line first runs on the next tag.
5. Sideload APK size in the android-ci/release log roughly unchanged after
   stripping now runs.

## Deferred

- Finding 2 (TTP arm64 manifest replaces the sideload manifest) → Backlog card.
