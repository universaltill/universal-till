# Review: Android per-device packages (#1541) + a reliable in-app update (#4312)

**Owner, 2026-10-14:** "fix the problem and also the android size. each system must download the correct package suite for its own configuration." The owner also said: never hand-install on the test tablet.

**What was wrong:**
- The APK was 150 MB, because both ABIs' Go engines (74.5 + 73.0 MB) were stored uncompressed.
- The in-app update tended to stick on "Downloading". It gave up after a fixed 5 minutes, the error went to a native bar that is hidden in release builds, and each extra tap deleted the finished file and started again.
- On Android, the check-result "Download" was only a link to the page the user was already on.

**Shipped:**
- **Gradle flavors `arm64` / `armv7` / `universal`.** Each flavor bakes its own update URL. A universal-build till moves to its own ABI's package, chosen from a fixed compile-time table by `Build.SUPPORTED_ABIS[0]`. `installUpdate()` still takes no argument (#1246).
- **Packaging:** `useLegacyPackaging` compresses the `.so`. versionCode is `base*10 + {0,1,2}`, which is monotonic above 34001.
- **Sizes measured:** arm64 38.3 MB, armv7 38.4 MB, universal 73.9 MB (was 150 MB). The arm64 APK holds only `lib/arm64-v8a`, deflated.
- **release.yml** builds and checks all three: signature, ABI set, deflate and versionCode. It publishes versioned names plus `-arm64-latest`, `-armv7-latest` and the legacy `unitill-pos-android-latest.apk` (universal), so v0.34.x tills still update. A new guard checks the ABI packages, with its own test. android-ci builds and tests arm64.
- **One install flow** (`web/public/android-update.js`), used by the status-bar chip, the Settings form and the check-result button, which is now a button. The 403 → PIN, 429 and already-current paths are kept.
- **MainActivity:**
  - The in-flight download id and its enqueue time are persisted, so a repeat tap only re-reports.
  - A complete APK is reused when its length matches the HEAD `Content-Length` (or, offline, the finished download's total), it parses as this package, and it is newer than the running build.
  - Stall detection (3 minutes with no bytes) replaces the 5-minute limit; the hard cap is 60 minutes from enqueue.
  - Progress, ready and failed states go to the page through `evaluateJavascript` with escaped JSON.
  - `Log.i` on every transition.
- **i18n:** 4 new keys in en, tr, ar and fa. `settings.update.android.confirm` is removed. Help `updates.md` is aligned in 5 languages; the fa list now uses ASCII numerals, which removes a stale drift-baseline entry.

**Tests:**
- 17 JVM unit tests (`UpdateLogicTest`): ABI→URL, stall detection, reuse decisions, the expected-length fallback, detector start, percent, JSON escaping.
- Go handler tests for the check-result button. The guard has its own test.
- Emulator run (API 36, local throttled server):
  - progress 0–94 % reached the page;
  - a second tap mid-download logged "already in progress";
  - Android's installer opened;
  - a completed file was reused;
  - a stalled server ended in `failed` on the page and in logcat.
- **Not verified:** the 3-minute stall on a device (DownloadManager failed first on the emulator; unit-tested), a real signed release run, and the test tablet (the owner forbade installs; the tablet proves it on the next two releases).

**Independent review:** Fable (built by Opus 5.5). Approved with follow-ups.
- Docs-shots stale: regenerated after the rebase.
- DE/ES/PT language packs are missing the new keys: pack PRs follow this merge in the same cycle.
- Offline reuse deleted a complete file when HEAD failed: fixed, with a fallback to DownloadManager's total (test first).
- The 60-minute cap reset per Activity: fixed, with the enqueue time persisted (test first).
- Two stale comments: fixed.
- Verified OK: no page input can steer the install, JSON escaping, FileProvider and the lock-task release unchanged, versionCode monotonic, every release.yml step covers all three APKs.
- **TDD re-verified** in a throwaway worktree: with the stall detector disabled, 2 of the 13 tests fail.

**Follow-ups:**
- Website per-device download: ut-website#63.
- Move the embedded web assets out of the Go library (bg-removal 18 MB, help screenshots 7.8 MB, demo images 4.4 MB): new card.
- If an AAB is ever built for Play, the flavor-digit versionCode must be revisited.

**Verdict:** safe to merge.
