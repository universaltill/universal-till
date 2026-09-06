# 2026-09-06 — Android: detect and surface a self-order kiosk pin that never engaged (ut-docs#1639)

## What shipped

`MainActivity.engageKioskLock()` has called `startLockTask()` since
ut-docs#1254 and treated it as done. It isn't. On an unprovisioned device
— which is *every* device we ship today, no Device Owner — `startLockTask()`
only **requests** the pin: Android answers with a system confirmation
dialog ("App is pinned… No, thanks / Got it") this app cannot see, dismiss
or pre-answer, and `startLockTask()` returns normally regardless of what
happens to it. Real-hardware measurement on the TECLAST P50T
(ut-docs#1281) found the app sitting at `lockTaskModeState=NONE` for 12s+
with the dialog unanswered, and forever after a "No, thanks" — while
immersive full-screen (which succeeds independently of the pin) made the
unpinned kiosk **visually identical** to a pinned one. One press of Home
from that state reaches the launcher and every other app on the tablet.
Nothing was logged, so it was invisible in the field too.

This change does not make the pin stronger — it cannot, from app code —
it makes the app tell the truth about it:

- `verifyPinEngaged()`, scheduled `PIN_VERIFY_DELAY_MS` (3s) after every
  `engageKioskLock()`, reads `ActivityManager.getLockTaskModeState()`
  directly instead of trusting the request.
- `onWindowFocusChanged(hasFocus)` re-reads the same state the moment the
  app gets input focus back — i.e. the moment that confirmation dialog is
  dismissed, since it is a system window that takes focus **without**
  pausing the Activity.
- `Log.w`/`Log.i` on both paths, tag `MainActivity`. This file had **zero**
  logging before this change (confirmed by grep), so a failed pin produced
  nothing in `logcat` at all.
- A red banner (`R.id.pin_warning` / `R.string.kiosk_pin_not_engaged`,
  translated en/fa/tr/ar) above the WebView while a pin is intended but not
  confirmed. A status banner, not a modal — the kiosk stays fully usable
  underneath, per this repo's standing "status chips/banners, never modal
  blockers in the kiosk flow" rule.
- `android/README.md` design section + manual verification checklist, and
  `web/help/en/display.md` (see should-fix 3 below).

## Independent review (Opus, different model from the Sonnet author)

Run in an isolated worktree against the branch's WIP snapshot. Found
**1 blocker**, **3 should-fix**, all fixed by the reviewer directly.

### Blocker (fixed) — the code would not have compiled

**`override fun onLockTaskModeChanged(mode: Int)` overrides a method that
does not exist.** There is no Activity-level lock-task callback in Android
at all. Verified from primary sources, not from memory:

- AOSP `core/java/android/app/Activity.java` (10,019 lines, fetched and
  grepped in this session): the only lock-task members are
  `startLockTask()`, `stopLockTask()` and `showLockTaskEscapeMessage()`.
  Zero occurrences of `onLockTaskModeChanged`.
- AOSP `core/java/android/app/ActivityThread.java`: **zero** occurrences of
  `LockTask` — there is no client-side dispatch mechanism for such a
  callback to arrive through, so this was never going to fire even if it
  had compiled.
- The whole supertype chain — `androidx.appcompat.app.AppCompatActivity`,
  `androidx.fragment.app.FragmentActivity`,
  `androidx.activity.ComponentActivity` — all fetched and grepped: zero
  occurrences.
- The platform's only lock-task callbacks are
  `DeviceAdminReceiver.onLockTaskModeEntering/Exiting`, which require the
  Device Owner provisioning this app deliberately does not have.

In Kotlin that is a hard compile error twice over ("overrides nothing", and
an unresolved `super.onLockTaskModeChanged`). **No PR check in this repo
would have caught it**: nothing in `.github/workflows/ci.yml` builds the
Gradle project — only `release.yml`'s `android-app` job runs
`./gradlew assembleRelease`. So this would have merged green and surfaced
as a broken *release*, not a broken PR.

Fixed by replacing it with `onWindowFocusChanged(hasFocus: Boolean)` — a
real `android.app.Activity` method, non-final, not overridden anywhere in
the AndroidX chain (verified in the same fetched sources), and the
*correct* hook for this specific problem: its own platform javadoc calls
out that "the system may display system-level windows … which will
temporarily take window input focus without pausing the foreground
activity", which is precisely the pinning confirmation dialog. Regaining
focus is therefore the moment the user has just answered it, whether that
took half a second or half a minute.

### Should-fix 1 (fixed) — a late "Got it" left a false banner up forever

With only the 3s delayed check, a user who answers the dialog after the
delay gets the red banner raised and then **never cleared** — nothing
re-checks until the next `/self-order` navigation or resume. A truthful
warning that stays up on a correctly pinned kiosk is its own defect. The
`onWindowFocusChanged` re-check clears it as soon as the dialog goes away.

### Should-fix 2 (fixed) — the focus re-check had to be deliberately asymmetric

`onWindowFocusChanged(true)` can also fire while the pin request is merely
*still unanswered*, so re-using `verifyPinEngaged()` there verbatim would
raise the banner before `PIN_VERIFY_DELAY_MS` had had its say — a flash on
every successful pin. The focus path therefore only ever **clears** a
warning, never raises one, with one exception: a new `pinConfirmed` field
records that the OS actually reported the pin engaged for the current
intent. A state that reads NONE *after* a confirmation is an unambiguous
drop (the documented unpin gesture), not a pending answer, and is warned
about immediately — that is the one case the deleted callback was there to
catch, kept rather than lost. `pinConfirmed` is reset by
`releaseKioskLock()`, which ends the intent; it is deliberately *not*
folded into `kioskPinned` (see "verified OK" below).

### Should-fix 3 (fixed) — a manual topic was owed, contrary to the brief's scoping

The review brief scoped this as "nothing a shop owner configures changed,
no `web/help/` topic implicated". Disagreed, and verified before
disagreeing. `web/help/en/display.md` told the owner, verbatim: *"switching
this device's profile to Self-order kiosk pins the screen automatically
(Home, Recents and the notification shade are all blocked)"* — the exact
claim ut-docs#1281's hardware run disproves — and this change puts a new
red bar on the customer-facing screen that the manual explained nowhere.
Both are squarely inside the standing product-owner rule ("anything a shop
owner sees … gets its topic updated in the same branch", ut-docs#324).

Rewritten to describe Android's confirmation prompt, what the red bar means,
that ordering still works underneath it, and how to clear it. `make
docs-shots` re-run for real (100/100 specs). Only `en/display.md` was
touched, matching ut-docs#1508's own precedent — the non-en `display.md`
files do not mention Android at all and are already behind independently of
this change. Note `en/display.png` itself did **not** change: the banner is
native Android chrome, not a web screen, so only the manifest's topic hash
moved. Four unrelated PNGs shifted by 1–114 bytes on ~100 KB files
(`en/sell`, `fa/multitill`, `ar|fa/till-designer`) — the same
non-deterministic rendering noise documented in the 2026-08-29 kiosk-lock-task
and 2026-09-03 ut-docs#1508 reviews, not content drift.

## Checked hard, found correct — no change needed

Each of these was a specific concern raised for review; each was traced,
not assumed.

- **`lateinit var pinWarning` can never be read uninitialised.** Assigned
  in `onCreate` immediately after `setContentView`, alongside `webView`
  and `statusView`, before any listener registration, `bindService`, or
  page load. `showPinWarning`/`hidePinWarning` are reachable only through
  `engageKioskLock` / `releaseKioskLock` / `verifyPinEngaged` /
  `onWindowFocusChanged`. Three of those four are additionally gated on
  `kioskPinned`, which only `onPageFinished` ever sets true — and a page
  load cannot precede `onCreate`. The one ungated path is
  `releaseKioskLock()`'s `hidePinWarning()`, whose four call sites are
  `onResume` (always one lifecycle step after `onCreate`), `onPageFinished`,
  `KioskBridge.exitLockdown` (needs a loaded page and a server-side manager
  PIN) and `launchPackageInstaller` (same PIN gate). No pre-`onCreate` path
  exists. This mattered: it would have been a live-till crash, not a
  cosmetic miss.
- **No Handler/Runnable pile-up.** `engageKioskLock()` calls
  `cancelPendingPinCheck()` *before* every `postDelayed`, so at most one
  check is ever pending no matter how fast `onResume` cycles;
  `releaseKioskLock()` and `onDestroy()` both cancel too. Traced the rapid
  resume/pause case explicitly.
- **No stale check does the wrong thing.** `verifyPinEngaged()` re-reads
  `kioskPinned` at fire time and returns if the intent changed. Even a
  check that somehow fired after `onDestroy` would touch a detached-but-live
  View, not a null reference — and `onDestroy` cancels it anyway.
- **`kioskPinned`'s semantics are unchanged.** Still purely *intended*
  state, still written only by the two lock functions, still the only thing
  `onResume` reads to decide. The new `pinConfirmed` is a separate
  *observed* field on purpose: merging the two would make `onResume` stop
  re-asserting a pin the OS had declined, which is the retry this design
  depends on.
- **XML.** All five touched files parse (`xml.etree.ElementTree`); all four
  `strings.xml` carry `kiosk_pin_not_engaged`; none of the four values
  contains anything needing Android resource escaping (no `'`, `"`, `\`,
  leading `@`/`?`, `&`, or format specifier). The layout is a vertical
  `LinearLayout` with the WebView on `layout_height="0dp"` +
  `layout_weight="1"`, so a `visibility="gone"` banner occupies zero space
  until shown and steals nothing from the WebView.
- **RTL / translations.** `values-ar`'s new string starts with `ق`, a
  strong RTL character, so the RLM (U+200F) workaround the neighbouring
  `notification_running` comment describes genuinely does not apply here —
  confirmed by reading the value, not by assuming. The banner uses
  `gravity="center"` and no `left`/`right`. fa/tr/ar all read as faithful
  renderings of the en source ("kiosk lock is not active — this device is
  not secured").
- **Offline-first / kiosk rules.** A banner, not a modal; ordering
  continues underneath; status/lock/exit stay reachable.
- **`reference/ux-guidelines.md`'s web checklist does not apply** — agreed
  with the brief here. No `internal/pages/**` or `web/ui/**` surface is
  touched; this is native Android chrome. (The *manual* obligation is a
  separate rule, and did apply — see should-fix 3.)
- No real client/shop name, no secret-shaped literal anywhere in the diff.

## Verified beyond automated tests

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean, full
  `go test ./...` clean — **zero `.go` files touched** by this diff
  (confirmed), so this is an expected no-op confirmation, not a strong
  signal on its own.
- Every CI-blocking guard this diff could plausibly touch, run in this
  session and green: `guard-android-i18n`, `guard-android-status-address`,
  `guard-kiosk-launch-flags`, `guard-data-access`, `guard-kiosk-engine`,
  `guard-i18n`, `guard-compliance-claims`, `guard-help-topics`,
  `guard-docs-shots` (green only *after* a real `make docs-shots` run —
  100/100 Playwright specs passed).
- **NOT verified: the Kotlin was never compiled.** This environment has no
  Android SDK/NDK/emulator (`$ANDROID_HOME` empty) and no network path to
  install one — the same constraint accepted for ut-docs#1254 and
  ut-docs#1508 (see
  `docs/code-reviews/2026-09-03-kiosk-pin-self-order-only-1508.md`'s own
  "Verified beyond automated tests"). Verified instead by close reading
  against **fetched primary sources** rather than recalled API knowledge:
  AOSP `Activity.java` and `ActivityThread.java`, and the AndroidX
  `AppCompatActivity`/`FragmentActivity`/`ComponentActivity` chain, each
  grepped directly for the members this diff overrides or calls. That
  method is what caught the blocker; a reading based on recollection would
  plausibly have waved `onLockTaskModeChanged` through, since it *sounds*
  like an API that ought to exist.
- API-level floors checked against `android/app/build.gradle.kts`:
  `minSdk = 24`, so both `ActivityManager.getLockTaskModeState()` (API 23)
  and `ActivityManager.LOCK_TASK_MODE_*` (API 23) are unconditionally
  available; `onWindowFocusChanged` is API 1.

## Safe-to-merge verdict

**Safe to merge.** The blocker is a compile failure, fixed and
independently re-verified against the actual platform sources; both
follow-on should-fix items are consequences of that fix and are fixed with
it; the manual gap is closed with a real `make docs-shots` run. Nothing
outstanding is blocking.

The honest caveat, unchanged from ut-docs#1508: the Kotlin in this repo is
still compiled by exactly one job in one workflow, on release. This review
found a hard compile error that would have reached `main` green. See the
deferred item below.

## Explicitly deferred

1. **Acceptance criterion 5 of ut-docs#1639 — real-hardware
   re-verification on the TECLAST P50T — is NOT satisfied and cannot be
   from this environment.** This is why the PR deliberately carries no
   closing keyword: merging it must not close the ticket. `android/README.md`
   checklist items **11–14** are written for whoever has the tablet next;
   13 and 14 were rewritten by this review to test what the code now
   actually does (a *late*-answered dialog clearing the banner by itself,
   and a confirmed-then-dropped pin) rather than the behaviour the deleted
   callback would have had.
2. **A pin dropped by the unpin gesture with no accompanying window-focus
   change is not surfaced until something else re-checks** (the next
   resume, or the next `/self-order` navigation). Android gives an
   unprovisioned app no callback for this, so there is no app-side fix;
   polling on a timer was rejected as an unjustified battery/IPC cost for a
   case Device Owner provisioning removes entirely. Recorded in README
   item 14 so the hardware run reports what the device actually does.
3. **Recommended new card (not opened by this review): add a
   `./gradlew assembleDebug` job to `ci.yml`.** No signing, no
   `gomobile bind` of the release .so needed for a compile-only check of
   `android/app/src/main/**`. It would have caught this review's blocker in
   seconds, and it is the second Android-only defect in a month that PR CI
   structurally cannot see.
