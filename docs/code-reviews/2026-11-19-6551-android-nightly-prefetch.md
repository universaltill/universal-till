# Review — Android nightly pre-download, one-tap install, 409 while busy (ut-docs#6551)

Branch `fix/6551-android-nightly-prefetch` (draft PR universaltill/universal-till#2607),
plus ut-docs branch `docs/6551-android-nightly-prefetch` (`architecture/packaging.md`).

## What shipped
- `internal/androidupdate.Prefetcher` + `Current`/`RegisteredPrefetcher`
  (RWMutex registry, same shape as `kiosk.SetBridge`); gobind mirror
  `mobile.UpdateBridge` / `SetUpdateBridge` (nil passes through as a nil
  interface; compile-time assertions keep the two method sets identical).
- `autoUpdateTick`: on Android (`InstallBridgeAvailable`) the nightly step is a
  prefetch, never `Apply`. Gates: due, cached+fresh available, not `dev`,
  store channel refused (ADR-0194 §2.3), not busy; no registered shell keeps
  the day's slot unspent. `PrefetchUpdate` only hands off to the UI thread,
  so the tick goroutine never blocks; checkout is untouched (offline-first).
- `POST /api/update/android-install` answers 409 `settings.update.android.busy`
  while `autoUpdateBusy` (cashier basket, kiosk basket, table-QR session
  baskets, shift close in flight) — after the store refusal and the cheap
  blank-PIN pre-check, before the network freshness call and before
  `AuthorizeManager`, so no PIN attempt is spent and no audit row written.
- Kotlin (sideload): `downloadAndInstallUpdate(prefetch)`; the
  `download_mode=prefetch` pref survives an Activity recreate so a prefetch
  that finishes in a new Activity still never installs; `AfterCheck.KEEP_READY`
  stores `ready_version_code`/`ready_length` after the ADR-0179 check and
  deletes the private copy; a manager's tap re-copies and re-verifies before
  `launchPackageInstaller`. `AndroidKiosk.updateReady()` (no argument,
  boolean) lets a freshly loaded page replay `downloaded`. Play flavour: inert
  stubs, never registers the bridge.
- Chip / Settings: `downloaded` and `busy` states, `Install now` label;
  three new `en.json` keys with `en.meta.json`; `web/help/en/updates.md`
  updated. CI's deadcode-baseline growth guard (ut-docs#3406) refused the
  first push's new baseline entry for a gobind-only `SetPrefetcher`. The
  setter was replaced by an exported `atomic.Pointer` (`androidupdate.Current`)
  that `mobile.SetUpdateBridge` stores into, so the baseline is unchanged.

## Review (independent, Opus 5.5; code authored by Sonnet)
Verdict: **approve-with-fixes**. No blocker or major finding.

1. **minor (Kotlin, lost tap / skipped permission preflight)** — a manager's
   tap that arrived while a nightly prefetch was in its silent size-check
   (`updateCheckingReuse`) or verify (`updateVerifyingApk`) phase removed
   `download_mode`, but those callbacks had already captured `prefetch=true`:
   `enqueueUpdateDownload` re-wrote the prefetch mode, or `afterCheck` kept
   the file ready, so the tap did nothing visible beyond a later "ready — tap
   to install" (safe direction: never an unbidden install). Separately the
   in-progress branch returned before the install-permission preflight, so a
   tap that upgraded a running prefetch could reach the installer without
   `canRequestPackageInstalls` (the silent bounce ut-docs#6551 set out to
   remove). → **fixed** in `SelfUpdateActivity.kt` (sideload): the
   non-prefetch preflight now runs before the in-progress check, and a
   UI-thread flag `updateTapUpgradedPrefetch` (set by a tap on an in-progress
   attempt, reset by each fresh attempt) is consulted by the size-check
   callback and by `afterCheck`. Not compiled here (no Android SDK) — see
   below.
2. **minor (test gap)** — `TestAndroidInstall_RefusesWhileBasketOpen` covered
   only cashier and kiosk baskets. → **fixed**: added table-QR session basket
   and shift-close-in-flight cases. Reviewer mutated `autoUpdateBusy` (dropped
   `SelfOrderSessions.HasItems()` and `shiftCloseInFlight`): both new cases
   failed with 200≠409; restored, all pass.
3. **minor (test gap, from Tester T1)** — store-channel gate untested →
   already resolved by Dev (`android_update_store_channel_6551_test.go`,
   `OverrideChannelForTest(ChannelPlay)`).
4. **accepted (low)** — the 409 is a check at authorisation time; a sale
   started between the 200 and Android's installer screen is not caught.
   Android's own confirm screen is still in between and the till's basket is
   in memory only; same window as before this change. No card.
5. **accepted (nit)** — `downloaded` has `max_length: 40`; "Update v0.36.12
   ready — tap to install" is exactly 40, longer versions exceed it in
   English (Tester). Chip measured fine; UX may widen later.

Checked and fine: registration lifetime (onResume/onPause, Play never
registers; a stale bridge between `RegisteredPrefetcher` and the call only
runs `runOnUiThread` on a paused Activity, which the Kotlin guards with
`isDestroyed`); nil bridge (tick returns before marking the slot); prefetch
never launches the installer, releases the kiosk lock or opens the permission
screen; window reopened mid-download reads the persisted mode;
`updateReady` takes no input and leaks only a boolean; no `showModal`, no
native confirm, no hardcoded strings; deadcode entry sorted and genuinely a
gobind-only false positive.

## Pre-flight
Pre-flight: help topic `web/help/en/updates.md` updated (screenshot manifest
touched; chip states not reachable on linux docs-shots); `en.meta.json`
updated for all three keys (English only, ADR-0160); `reference/` n/a (no
contract change; ut-docs `architecture/packaging.md` updated on its branch);
`android/README.md` updated; root `README.md` n/a (no platform claim
changed); ADR n/a (ADR-0179 check still gates every installer launch,
ADR-0194 store gate honoured); review record this file.

## Verification run by the reviewer
- `gofmt -l` clean; `go vet` and `golangci-lint run` on `internal/androidupdate`,
  `mobile`, `internal/pages`: 0 issues.
- `go test ./internal/androidupdate ./mobile ./internal/httpx -run 'Prefetch|UpdateBridge|AndroidUpdate'` ok;
  `go test ./internal/pages -run 'TestAndroidInstall|TestAutoUpdateTick_Android|TestUpdateUnavailableHTML_Android|TestAndroidSettingsForm'` ok
  (not the whole package, per time box).
- Guards: i18n, i18n-meta, help-topics, no-native-confirm, fixed-dates,
  rtl-logical-css ok.

## Not verified
- Kotlin under Gradle (no Android SDK here): `mobile.UpdateBridge` binding,
  `override fun prefetchUpdate()` against gobind's `throws Exception`, and the
  reviewer's `SelfUpdateActivity.kt` edit. `android-ci.yml` must prove them.
- On a device (TECLAST P50T / Fire OS): overnight prefetch, Activity
  recreate mid-download, one-tap install, `updateReady()` replay.
- `guard-deadcode-baseline.sh` could not run locally (toolchain go1.26 vs
  `cmd/unitill-uninstall` go1.27); CI runs it.
- Dark theme and non-English chip lengths (no translations yet).
