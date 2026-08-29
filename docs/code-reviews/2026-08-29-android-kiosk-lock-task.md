# 2026-08-29 — Android kiosk lock-task (ut-docs#1254)

## What shipped

The Android native shell (`android/`) had no kiosk/lock-down mechanism at
all — the app was fully exitable like any ordinary app (Home, Recents,
notification shade all reachable). This adds:

- **Screen-pinning Lock Task** (`Activity.startLockTask()`/`stopLockTask()`)
  engaged automatically on every `onResume` — zero device provisioning
  required, works on every device today. Home/Recents are blocked while
  pinned; the documented unpin gesture (long-press Back+Overview on
  3-button nav, swipe-up-and-hold on gesture nav) remains, by design, the
  known-weaker limitation of this mode.
- **Immersive full-screen** (`WindowInsetsControllerCompat`) as a cosmetic
  second layer, always in lockstep with the lock state.
- **Navigation confined to the till's own loopback origin**
  (`WebViewClient.shouldOverrideUrlLoading`) — closes a real reachable gap
  (below).
- **Exit wired to the server's existing "exit to OS" escape hatch**
  (`/api/settings/exit-to-os`, `internal/pages/settings_page.go`,
  ut-docs#1099) via a `window.AndroidKiosk.exitLockdown()` JS bridge
  (`addJavascriptInterface`), called from `login.html`/`settings.html`'s
  existing exit-to-os success handlers only on a 2xx response.
- **Go-side fix, not just Android**: `internal/pages/init.go`'s
  `newWindowController` previously fell through to
  `ShellPollWindowController` with no fallback on Android (no shell
  process, no `UT_KIOSK`, no `UT_DESKTOP_CONTROL_ADDR` — ever), which
  answers `ExitToOS()` with `ErrNoWindowControl` → the settings handler's
  503. Since `exitLockdown()` only fires on a 2xx, this meant the escape
  hatch could never fire on Android at all. New
  `common.AndroidNativeWindowController` (selected on `goos == "android"`)
  reports the honest success the bridge is gated on, since the real
  authorization already happened in the handler (`AuthorizeManager(pin)`)
  before `WindowCtl.ExitToOS()` is ever reached.
- **Device Owner scaffolding** (`TillDeviceAdminReceiver`,
  `res/xml/device_admin_receiver.xml`, the manifest `<receiver>`) — inert
  until a device is provisioned out-of-band; when provisioned,
  `engageKioskLock()` detects `isDeviceOwnerApp` and upgrades to full
  lock-task (no user-exit gesture at all).

## Independent review (Opus, different model from the Fable-authored first
draft and the Sonnet orchestrator that revised it)

First pass found **2 blockers, 2 should-fix, 5 nice-to-have**. All
blockers and should-fixes were fixed in this same cycle before merge; see
below for exactly what changed and why.

### Blockers (fixed)

1. **The escape hatch could never fire on Android** — traced end-to-end
   through `internal/pages/init.go`, `common/shell_poll_window_controller.go`,
   `settings_page.go`, and the repo's own existing tests
   (`shell_poll_window_controller_test.go`,
   `settings_page_test.go`'s `TestExitToOSEndpoint_NoShellAttached503NoAudit`),
   which pin exactly this "no shell, no fallback → `ErrNoWindowControl` →
   503" topology. Fixed with `common.AndroidNativeWindowController` +
   `newWindowController`'s new `goos == "android"` branch (a `goos string`
   parameter now, for testability — mirrors the existing
   `piKioskUnitPresent bool` injection pattern). New regression test:
   `TestAndroidWindowControllerExitToOSSucceeds`
   (`internal/pages/init_kiosk_detect_test.go`), which fails against the
   pre-fix code (asserted by construction — it's the same topology
   `TestAttachModeWindowControllerIsReal` already proves returns
   `ErrNoWindowControl` on Linux) and passes after.
2. **`guard-docs-shots.sh` (CI-blocking) failed** — editing `login.html`/
   `settings.html` changed the tracked app-surface hash. Fixed by actually
   running `make docs-shots` for real in this session (this container has
   Chromium pre-installed — unlike the Android SDK, this one IS runnable
   here) and committing the regenerated `web/help/img/manifest.json` (+ the
   two topics whose markdown also changed content-hash: `display` for the
   new Android paragraph, and incidentally re-rendered `sell`/`sell` pngs
   with no prose change, non-deterministic rendering noise between runs).

### Should-fix (fixed)

3. **`addJavascriptInterface` safety comment was factually wrong** — claimed
   the WebView "only ever" loads loopback content; `network_security_config
   .xml` restricts cleartext only, and the app had no navigation
   restriction of its own. Reachable in practice: `my_reports.html`'s
   ungated GitHub issue link (`target="_blank"`, no platform gating) would
   navigate the same WebView instance to an untrusted origin, where the
   injected `AndroidKiosk` object (instance-scoped, not page-scoped) stays
   reachable. Fixed with `WebViewClient.shouldOverrideUrlLoading` confining
   navigation to `TillService`'s own reported loopback authority; comments
   corrected to describe the actual enforcement instead of a false
   guarantee. `android/README.md` updated to match, plus a manual
   verification step (#8) targeting this exact case.
4. **A directly-applicable, precedented test harness was skipped** — the
   Android side genuinely has no test framework (BA's claim verified: no
   `src/test`/`src/androidTest`, no `*Test.kt`, no test dependency in
   `build.gradle.kts`), but the web half does:
   `e2e/tests/exit-to-os-lockout-1104.spec.ts` exists for exactly this
   class of bug ("the bug lived entirely in the client JS's status-code
   branching, which only a real browser can exercise"). Added
   `e2e/tests/android-kiosk-bridge-1254.spec.ts` (settings.html, against
   the plain auth-off till) and a new test in `e2e/tests/login.spec.ts`
   (login.html's session-less escape hatch, which needs the AUTH project's
   already-completed-wizard state to reach at all) — both stub
   `window.AndroidKiosk` and assert `exitLockdown()` fires on a real 2xx
   and does NOT fire on 503/403. **Actually run in this session** (this
   container's pre-installed Chromium made that possible, unlike the
   Android/Kotlin side) — all pass; full suite run alongside them, 223/224
   passed, the one failure (`split-tender-i18n-925.spec.ts`, an unrelated
   fa/RTL payment test) reproduces identically with this diff fully
   reverted (`git stash`) and passes on isolated retry — a pre-existing,
   order-dependent flake, not a regression from this change.

### Nice-to-have (addressed)

5. `engageKioskLock()`/`applyImmersiveMode()` in `onCreate` were redundant
   (startLockTask() acts on the resumed task; `onResume` — which always
   runs immediately after `onCreate`, cold launch included — already
   re-asserts both). Removed; `onResume` alone now owns this.
7. Device Owner's `setLockTaskPackages` allowlists only this app's own
   package, which would block the Plugins page's file-chooser `Intent`
   under true lock-task. Documented in `android/README.md` as a known,
   not-yet-mitigated consequence (Device Owner mode isn't active on any
   real device yet, so nothing ships broken today).
8. The unpin-gesture instructions named only the 3-button-nav gesture,
   wrong for gesture navigation (the default most current devices,
   including the TECLAST P50T test rig, actually ship with). Corrected in
   both the design note and the manual verification checklist.
9. `web/help/en/display.md` (the shipped user manual, `routes: [/settings]`)
   had no Android sentence in its per-platform "Exit to OS window"
   enumeration. Added, now that the mechanism it describes is actually
   correct (blocker 1 fixed) rather than documenting something that didn't
   work.

Not separately addressed: nice-to-have 6 (staying unlocked indefinitely
while parked on `/settings`/`/login` without a background/foreground
cycle or navigating away) — a real, longer-lived-than-comments-implied
window, but bounded (re-locks on navigation away or on any resume), not a
stuck-forever case, and explicitly lowest-priority per the review itself.
Left as a documented, accepted gap for a possible future
`onPause`-triggered re-lock or short timer.

## Verified beyond automated tests

- `gofmt -l .` clean, `go build ./...` clean, full `go test ./...` clean
  (every package, including the new
  `TestAndroidWindowControllerExitToOSSucceeds`).
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  and green: `guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots`, `guard-help-topics`, `guard-android-status-address`,
  `guard-android-i18n` (the ones this diff could plausibly touch; the
  remainder are unaffected by a diff with zero webkit/emoji/htmx/autofill/
  brand-asset/Makefile surface, per `guard-webkit-version` et al.'s own
  scope).
- Full Playwright e2e suite run for real against this container's
  pre-installed Chromium (a locally-scoped config override,
  `launchOptions.executablePath`, never committed — CI's own runner has a
  normal network path to install Playwright's pinned browser and doesn't
  need this): 223/224 passed, the one failure confirmed pre-existing and
  unrelated (see should-fix 4 above).
- **NOT verified**: the Kotlin/Android side was never compiled — this
  container has no Android SDK/NDK/adb/emulator, and no network path to
  install one (`dl.google.com` blocked at the proxy). Verified instead by
  close reading against known-correct Android API semantics, cross-checked
  line-by-line against every Go/HTML file it depends on (not taken on
  faith), by two independent passes (Sonnet orchestrator, Opus reviewer).
  Real-hardware verification against the actual TECLAST P50T test rig —
  named in the ticket's own acceptance criteria — is filed as its own
  follow-up card, since no cold cloud session can action it.

## Safe-to-merge verdict

Safe to merge. Both blockers and both should-fix findings are fixed and
independently re-verified (Go: built + tested; web: e2e-run for real). The
one remaining nice-to-have is a documented, bounded, explicitly
lowest-priority gap, not a defect.

## Explicitly deferred (follow-up cards to be filed)

- Real-hardware verification on the TECLAST P50T (pin engages, Home/
  Recents/notification-shade genuinely blocked, unpin gesture confirmed,
  both exit-to-os forms confirmed to actually unpin a real device) — needs
  a local/interactive session with the physical device, not a cold cloud
  cycle.
- Device Owner provisioning itself (the `adb shell dpm set-device-owner`
  step) and the file-chooser allowlist gap noted above, if/when that mode
  is actually adopted for a real deployment.
- Nice-to-have 6 (re-lock while parked unlocked on a manager page, without
  requiring a background/foreground cycle first) — real but low-priority
  UX hardening, not a security hole.
