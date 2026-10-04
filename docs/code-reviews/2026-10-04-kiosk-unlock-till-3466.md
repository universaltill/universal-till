# Review: honour `kiosk_unlock` on a self-order Android till (ut-docs#3466, ADR-0142 card 2)

**Date:** 2026-10-04 · **Author model:** Opus 5.5 (Dev subagent) · **Tester:** independent pass, PASS · **Reviewer:** Fable 5.1 (independent subagent, did not write the code)
**Branch:** `feat/3466-kiosk-unlock-till` (`universal-till`)

## What shipped

Build card 2 of `adr/0142-remote-unlock-for-self-order-kiosk.md` (Accepted
2026-10-02): the till side of the owner-sent, device-targeted `kiosk_unlock`
directive that card 1 (ut-cloud#296, merged) already serves. Design: BA +
Architect comments on ut-docs#3466; no new ADR (the mechanism is 0142's own).

- **`internal/cloudsync`:** `rename_till.go`'s per-type skip wrappers
  (`renameTillSkipReason`, `printReportSkipReason`) and `Tick`'s if-chain
  are replaced by one data-driven check, `deviceTargetSkipReason` over a
  `deviceTargetedTypes` set (`rename_till`, `print_report`, `kiosk_unlock`)
  in the new `device_target.go` (ADR D3 "generalise, don't copy"). New
  `Hooks.KioskUnlock(ctx, directiveID, createdBy)` and its `apply` case.
  `kiosk_unlock` is in neither `mainTillOnlyTypes` nor `catalogTypes`
  (ADR D1: any-till, never main-till only).
- **`internal/pages/common`:** `WindowController.ReleaseKiosk() error` on
  all six implementations plus the four test fakes. Two new sentinels,
  `ErrKioskReleaseNotSupported` (`not_supported`, desktop shells and the
  no-op) and `ErrNoKioskShell` (`no_shell`, Android with no bridge). The Pi
  appliance reuses the existing `ErrNoOSDesktop`, as the Architect
  corrected. Android's `ReleaseKiosk` reads the bridge from the new
  `internal/kiosk` package.
- **`internal/kiosk`:** one small RW-mutexed store for the native pin
  state (`SetKioskPinned`/`KioskPinned`) and the Go→native release
  callback (`SetBridge`/`RegisteredBridge`), shaped like
  `diagnostics/device_model.go`. Lives outside `mobile` because `common`
  cannot import `mobile` (`mobile → app → pages`).
- **`internal/pages`:** `cloudsync_kiosk_unlock.go` — the handler
  (`cloudKioskUnlock`): its own `display.mode == self_order` check is the
  security boundary (the cloud's is a UX guard), then `ReleaseKiosk`, the
  sentinel→reason mapping (`kiosk_appliance` / `not_supported` /
  `no_shell`, anything else passed through), and one till audit row
  `kiosk_unlock` (actor `system`, entity `kiosk`/directive id, payload
  `directive_id`, `created_by`, `via`, `status`, `reason` on failure) on
  every outcome. `DeviceExtra` gains `self_order` and `kiosk_pinned`
  (ADR D5, the exact lower-case keys card 1 stores).
- **`mobile`:** gobind-visible `KioskBridge` mirror of `kiosk.Bridge`,
  `SetKioskBridge` (nil un-registers) and `SetKioskPinned`.
- **`android/`:** `MainActivity` implements the gomobile interface as
  `RemoteUnlockBridge` (aliased import — the Activity already has an
  unrelated JS-bridge inner class called `KioskBridge`), registered in
  `onResume`, cleared in `onPause`. `releaseKiosk()` runs
  `releaseKioskLock(); clearImmersiveMode()` on the UI thread behind a
  bounded `CountDownLatch` (5 s), opens a 15-minute **release window**
  (monotonic clock) and loads `/login`; `onPageFinished` leaves
  `/self-order*` unpinned while the window is open and closes it on the
  first navigation outside `/login`, `/settings*`, `/self-order*`.
  `engageKioskLock`/`releaseKioskLock` push the state to Go. New
  instrumented test `RemoteKioskUnlockTest.kt` (`androidTest`, runner +
  deps added to `build.gradle.kts`), README section and device-check
  items 15–17.
- `scripts/ci/deadcode-baseline.txt`: the two `internal/kiosk` setters,
  for the same reason as `SetDeviceModel`/`SetAndroidBridge` (their only
  production caller is the gomobile library package, not a guard root).
- `web/help/img/manifest.json`: `surface_sha256` refreshed only — no
  rendered pixel changed (background hook + check-in fields; the till
  renders no new screen). `guard-docs-shots.sh` passes on the new hash.
  **The commit must carry `Docs-Shots-Unchanged: true`.**

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | Nit, fixed | `cloudsync_wire.go`'s `DeviceExtra` comment described `kiosk_pinned` as whether the shell "holds the OS pin right now". What Kotlin pushes is `kioskPinned`, its *intended* state (`startLockTask()` only requests the pin; on an unprovisioned device the system dialog may be refused — `RemoteKioskUnlockTest.kt` says so itself). A my. reader must not read `true` as OS-confirmed. | Comment reworded to say "intends to hold". No behaviour change; ADR D5 asks for exactly the engage/release push that ships. |
| N1 | Non-blocking, accepted | Kotlin `RemoteUnlockBridge.releaseKiosk()`: if the main thread is wedged past the 5 s latch, Go reports `ui_thread_timeout` (`failed`) but the posted runnable still runs once the main thread frees, so the pin may drop *after* my. was told it failed. | Accepted: a late release inside the Activity is the thing the owner asked for, bounded by the same window, and the failed result stops the cloud retrying. Documented here, not changed. |
| N2 | Non-blocking, deferred | `kiosk_pinned` reports intent, not the `getLockTaskModeState()`-verified pin that `verifyPinEngaged()` already polls. Pushing the verified state too would make the my. picture truer on unprovisioned devices. | Out of ADR D5's scope (it specifies push on engage/release). Candidate follow-up, not a blocker. |
| N3 | Non-blocking, noted | The empty-`directiveID` → `"-"` audit entity fallback is untested (Tester's observation). The cloud always sends an id; the fallback only keeps the `audit_log.entity_id` non-empty. | Accepted as-is. |
| N4 | Non-blocking, noted | Instrumented test exists as source only — no Android SDK in this session, so it was reviewed by reading, not run. `android/README.md` items 15–17 are the on-device check the ADR (and the card) require before Done. | Carried to the orchestrator; the card's own text already names this gate. |

### Adversarial checks that came back clean

- **Generalised skip vs. the old chain:** `deviceTargetSkipReason` is
  logic-identical to `renameTillSkipReason`→`printReportSkipReason`→
  `ownDeviceSkipReason` for those two types (same trim, same blank/absent/
  non-string/own-id-unknown answers, same once-per-id log — the old chain
  already keyed both on `firstRenameSkip`). The pre-existing
  `TestPrintReportSkipReason`/`TestTick{RenameTill,PrintReport}OnlyForOwnDevice`
  still pass unchanged apart from the rename of the reset helper.
- **Audit exactly once per directive:** `cloudKioskUnlock` writes one row
  per call on every path (success, `not_self_order`, every sentinel, a raw
  bridge error); `apply` runs once per served directive per tick. The only
  re-apply is `Tick`'s existing contract when the result post fails (the
  cloud re-serves, hooks must be idempotent) — a second release inside the
  window is a no-op on the device, and that is pre-existing behaviour for
  every type.
- **Sentinel chain:** `ErrNoOSDesktop`, `ErrKioskReleaseNotSupported`,
  `ErrNoKioskShell` are distinct `errors.New` values; no controller wraps
  one in another or joins them, so order cannot misclassify. A wrapped
  sentinel (`fmt.Errorf("%w")`) is tested and maps correctly; a raw bridge
  error passes through verbatim.
- **Concurrency:** `kiosk.RegisteredBridge` drops its `RLock` before the
  caller invokes the bridge, so the UI thread's `Mobile.setKioskPinned`
  (which takes `mu.Lock`) cannot deadlock against the waiting Go
  goroutine. The latch always counts down (`finally`), the wait is bounded,
  and `Activity.runOnUiThread` posts to the main looper so an Activity
  destroyed mid-call still runs the runnable (its exceptions are caught and
  surface as the failure reason) — no leak, no hang. `onPause` clears the
  bridge before `onDestroy`, so a Go reference to a dead Activity is
  released. `remoteUnlockWindowUntil`/`kioskPinned` are UI-thread-only.
- **One process:** `AndroidManifest.xml` gives `TillService` no
  `android:process`, so the Activity's registered bridge and the Go
  cloudsync loop share one Go runtime; the in-memory store is sound.
- **Any-till:** satellite gate (`mainTillOnlyTypes`) precedes the device
  check in `Tick`; `TestTickKioskUnlockOnlyForOwnDevice` proves a satellite
  with its own device id applies its own unlock (ADR D1).
- **Fail-closed boundary:** `inSelfOrderMode` returns false on nil
  settings or a read error → `not_self_order`, and `nil`/absent
  `WindowCtl` → `not_supported`, never a fabricated `applied`.
- No new user-facing strings (no i18n), no file writes (no `MkdirAll`
  concern), no `paths.Data` concern, no money, no real shop/client names in
  tests, no secrets. No help topic needed: the owner-side action is card 3
  (`ut-my-shop`); the till shows no new screen.

## Verified

- Reviewer re-ran: `gofmt -l .` (clean), `go build ./...`, `go vet` and
  `go test` over `internal/cloudsync/...`, `internal/kiosk/...`,
  `internal/pages/...`, `mobile/...` — all green; `bash
  scripts/ci/guard-docs-shots.sh` green on the refreshed `surface_sha256`.
  (Tester additionally ran `-race` and `golangci-lint` v2.14.0 green; not
  repeated here.)
- Reviewer re-verified four TDD claims by mutating the production code,
  running the test, confirming the failure symptom, restoring and
  re-running green, each within one command so no partial state could land:
  1. drop the `inSelfOrderMode` check →
     `TestCloudKioskUnlock_RefusedOutsideSelfOrder` fails with
     `got ("kiosk released", <nil>), want error not_self_order` on all five
     modes;
  2. remove `kiosk_unlock` from `deviceTargetedTypes` →
     `TestDeviceTargetSkipReason` and `TestTickKioskUnlockOnlyForOwnDevice`
     fail (`hook runs = 1 … want 0 runs, 0 posts` for other/blank/absent
     device, on main till and satellite);
  3. return `ErrNoOSDesktop` unmapped →
     `TestCloudKioskUnlock_PlatformRefusalsMapToReasons` fails on the
     `pi appliance`, `pi appliance, real` and `wrapped sentinel` cases;
  4. invert `kiosk.KioskPinned()` in `DeviceExtra` →
     `TestDeviceExtra_ReportsSelfOrderAndKioskPinned` fails
     (`defaults: … kiosk_pinned=true`).
- Kotlin reviewed by inspection against the full pre-existing
  `onPageFinished` routing, `engageKioskLock`/`releaseKioskLock`,
  `exitLockdown` and the `BluetoothBridgeImpl` exception precedent.

## Not verified (honest gaps)

- No Android SDK, device or emulator in this session: `MainActivity.kt`
  and `RemoteKioskUnlockTest.kt` did not compile or run here. The card's
  on-device check (README items 15–17) stays a gate before Done.
- No real cloud round-trip; the cross-repo contract (reason strings
  `not_self_order`, `kiosk_appliance`, `not_supported`, `no_shell`; check-in
  keys `self_order`, `kiosk_pinned`) was checked against card 1's merged
  text on ut-docs#3466, not against a live ut-cloud.

## Verdict

**Safe to merge** (approved with one comment-only fix, F1). Commit with a
`Docs-Shots-Unchanged: true` trailer. The Android device check remains the
card's documented gate before Done.

---
_Generated by [Claude Code](https://claude.ai/code)_
