# 2026-09-07 — Android: the kiosk "not secured" banner never cleared once the pin WAS granted (ut-docs#1639, follow-up)

## How this came about

ut-docs#1639 shipped on 2026-09-06 (universal-till#834) and its card sat in
**In Review** with one acceptance criterion outstanding: *"Re-verified on the
TECLAST P50T, with `mLockTaskModeState` read directly — not inferred from the
screen."* That criterion is the entire subject of this record. Running it found
the shipped fix defective in the opposite direction to the bug it fixed, so the
card went back to In Progress rather than to Done.

The tablet was on `v0.12.4`; the fix first shipped in `v0.12.5`. It was updated
to `v0.12.7` before any measurement was taken.

## What the verification found

`startLockTask()` on an unprovisioned device only *requests* the pin; Android
answers with an "App is pinned … No, thanks / Got it" dialog. #834 added a
banner + log for the case where that dialog is never answered — and that half
works, verified:

```
09-07 01:01:45 W/MainActivity: self-order kiosk lock NOT engaged (lockTaskModeState=NONE)
                               — confirmation dialog unanswered or declined; kiosk is running unpinned
```

with the red banner up and `dumpsys` reading `mLockTaskModeState=NONE`.

Then **Got it** was tapped. The device pinned — Android's own "App pinned"
toast, `mLockTaskModeState=PINNED` — and the banner claiming *"Kiosk lock is not
active — this device is not secured"* **stayed up**. Measured with the page's
idle timer deliberately kept alive (a neutral tap every 20s):

```
granted at 00:52:39
t+20s … t+120s   mLockTaskModeState=PINNED     banner still shown, and no log line at all
```

### Root cause

`verifyPinEngaged()` was a **one-shot** `postDelayed(…, 3000)` armed by
`engageKioskLock()`. Clearing the banner was delegated to
`onWindowFocusChanged`, on the documented reasoning that Android's pinning
confirmation takes input focus without pausing the Activity, so dismissing it
hands focus back. On this hardware it does not. With the one-shot already
spent, nothing re-read the state, ever.

It appeared to clear during the first, sloppier run only because of an
**unrelated accident**: `engageKioskLock()` is called from `onPageFinished`, and
`self_order.html`'s idle-reset reload re-ran it every 60s. That reload is
opt-*out* (`kiosk.idle_reset_seconds` defaults to 60, `0` disables it) and its
timer restarts on every touch — so on a till with it switched off, or simply one
being used, the false alarm is permanent.

**A permanent false alarm is not a lesser bug than the missing alarm this card
was opened for — it is the same bug.** Staff who see "not secured" on a till
that is secured stop reading the banner, and the genuinely unpinned kiosk this
card exists to make visible becomes invisible again.

## What shipped

`verifyPinEngaged()` re-arms itself every `PIN_RECHECK_INTERVAL_MS` (3s) for as
long as a pin is intended, so both a late answer and a later *loss* of the pin
are noticed. Android exposes no Activity-level lock-task callback without Device
Owner (`DeviceAdminReceiver.onLockTaskModeEntering/Exiting` are the only ones),
so re-reading on a timer is the only mechanism available, not a shortcut past a
better one.

Supporting changes: `schedulePinCheck()` centralises arming; logging is keyed off
the banner's own visibility so a 3s poll logs transitions only; `onPause`
stops the poll; `onWindowFocusChanged` no longer cancels the pending check (that
cancel is exactly how the original ended up with no live reader).

## Independent review (fresh-context Opus, `complexity:medium` routing)

It compiled the branch (`:app:compileReleaseKotlin`, BUILD SUCCESSFUL) and ran
the three Android CI guards, then found **one must-fix regression and four
accuracy problems**. All five were fixed; none were argued away.

1. **(must-fix) The poll could re-arm while the Activity was paused, and then
   never stop.** `onPause` cancelled the pending check, but `onPageFinished` has
   no lifecycle gating and this app never pauses the WebView's JS timers — so
   the self-order idle reload kept re-entering `engageKioskLock()` while the
   Activity was stopped, re-arming a poll that `onPause` would not run again to
   cancel, on a process `TillService` keeps alive. A strict regression: the
   pre-change code armed a one-shot on that path. Fixed with an
   `activityResumed` flag that `schedulePinCheck` refuses to arm outside —
   deliberately stricter than cancelling in `onPause`, because cancelling is
   what proved insufficient.
2. **`pinConfirmed` was not reset by `engageKioskLock`.** Its own doc scopes it
   to one pin intent. A kiosk that was pinned, unpinned by the documented
   gesture and re-entered would carry the stale `true` into a fresh, unanswered
   dialog, where `onWindowFocusChanged` reads it as an unambiguous *drop* and
   raises the banner immediately — contradicting that same function's stated
   rule that an unanswered request is `verifyPinEngaged`'s call. Now reset.
   (Pre-existing on `main`, but this change rewrites both branches and
   re-asserts the invariant in prose, so it was in scope.)
3. **`showPinWarning()` was not idempotent.** The poll called it every 3s while
   the warning stood; `setText` is not a free no-op like `setVisibility` — with
   an accessibility service running it emits a text-changed event, so a
   customer-facing kiosk with TalkBack on would re-announce *"this device is not
   secured"* every 3 seconds, indefinitely. Now returns early when already
   visible.
4. **No `try`/`catch` on the one piece of kiosk bookkeeping that now runs
   unattended forever.** `engageKioskLock`/`releaseKioskLock` both carry
   deliberately broad catches on the stated grounds that the exception types
   here vary by OS version and OEM; the repeating read had none, and a throw
   would both crash a live till and permanently kill the poll. Wrapped, with the
   reschedule outside the catch so a bad read costs one interval, not the poll.
5. **Two prose claims were wrong or overstated**, in files that are the durable
   record for this subsystem. The cost sentence promised a lifecycle bound the
   code did not yet enforce (see 1). And the idle-reset reload was called
   *optional* when it is opt-out and on by default — which also cast doubt on
   the 120s measurement, since a 60s reload should have contradicted it. Both
   corrected, and the 120s figure now states why it holds (the run tapped the
   screen every 20s precisely to stop that reload firing).

### The finding that changed the diagnosis

The review also caught that a root-cause claim written into a permanent comment
— *"whether the callback never fired or fired too early is not something the app
can tell apart"* — asserted a platform property when it was only a property of
the shipped logging: `onWindowFocusChanged`'s third branch was silent, so both
readings produced identical (empty) logs. The honest version was "the build we
measured could not tell".

Rather than soften the wording, the branch now logs at debug level — and that
**settled it on the device**:

```
01:21:43 D/MainActivity: self-order kiosk pin request still unanswered at focus regain
                         (lockTaskModeState=NONE) — deferring to the scheduled check   ← sleep/wake, dialog still up
01:20:59 (Got it tapped)                                                                ← nothing logged at all
01:21:00 I/MainActivity: self-order kiosk lock confirmed engaged (lockTaskModeState=2)  ← the poll, 1.0s later
```

The debug line fires reliably for a genuine focus return and did not fire at all
when the dialog was dismissed. So on the P50T **the callback is not delivered at
all when Android's pinning confirmation is dismissed** — an absent event, not a
read taken too early. That is now recorded as a measured fact in both
`MainActivity.kt` and `android/README.md` instead of an undecidable-by-decree
comment. This is the #1607 lesson applied: a rule reused across sources without
re-measuring is where this class of bug keeps coming from.

## Verification — what was actually run

All on the TECLAST P50T over adb, against a locally built, release-signed
`0.12.7+fix1639b` installed over the release build (no wipe), `mLockTaskModeState`
read from `dumpsys activity activities` every time.

| Scenario | Result |
|---|---|
| Cold launch into self-order, dialog unanswered | `W … NOT engaged`, banner up, state `NONE` |
| Tap **Got it** | state `PINNED`; `I … confirmed engaged` **1.0s later**; banner cleared |
| Idle 120s after granting, screen tapped every 20s | banner stays cleared, **no further log lines** (transition-only logging holds) |
| Tap **No, thanks** | state stays `NONE`, banner stays up, **exactly one** log line across 60s (~20 poll iterations) |
| `am task lock stop` while pinned | state `NONE`; `W … DROPPED` within ~3s; banner returns |
| Sleep/wake with dialog still unanswered | new `D` line fires — proving the negative result above is a real absence, not a filtered log |
| Till NOT in self-order (register mode) | no `MainActivity` log lines at all across a full navigation — the poll does not run outside kiosk mode |

`scripts/ci/guard-android-{status-address,i18n,external-links}.sh` all pass.

**Limit of the evidence, stated plainly:** review finding 1 (the poll re-arming
while paused) is fixed by code and traced by the reviewer, but it is **not**
device-observable — the logging is deliberately transition-only, so a poll
running behind `onPause` on an unchanged state produces no signal either way.
That fix rests on code reasoning, not on a measurement.

## Not addressed here, deliberately

The Android module has **no automated test surface at all** — no `test/`, no
`androidTest/`, no Kotlin test source anywhere, and no test dependencies in
`android/app/build.gradle`. Verified, not assumed. Worse, `ci.yml` never invokes
Gradle, so nothing in PR CI even compiles this file (ut-docs#1658).

A pure-logic extraction would **not** have caught this bug or its predecessor —
both are scheduling/lifecycle defects, not decision defects. A Robolectric test
driving the main looper would catch both: idle 10s after `onPageFinished` and
assert the state was read more than once (the original one-shot bug), then
`onPause()` + another `onPageFinished` and assert no further reads (review
finding 1). That needs a new source set, Robolectric and a Gradle CI job, so it
is a card, not a line in this change — filed rather than dismissed.
