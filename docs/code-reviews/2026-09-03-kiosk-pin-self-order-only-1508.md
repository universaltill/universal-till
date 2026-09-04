# 2026-09-03 — Android kiosk pin restricted to self-order mode (ut-docs#1508)

## What shipped

The Android native shell (`MainActivity.kt`) engaged Lock Task ("screen
pinning") unconditionally on every screen since ut-docs#1254 — including
the pre-enrollment setup wizard. That is how the product owner got
bricked for real on 2026-09-03: the wizard's Install step hit a raw JSON
error page (ut-docs#1507, a *deliberate* `guard-page-http-error.sh`
exception — the wizard has no operator layout to render an escape link
into) with Home/Recents both blocked, no OS bar, no way out short of
force-stopping the app over adb.

Product owner's rule, verbatim in spirit: "for the till, we can only
hide the bottom OS menu but let it go to the OS. Only it cannot go to
the OS if it is on the self-ordering mode. In that case we can do it by
the lock button that we have or remotely."

This change:

- Gates Android Lock Task to engage **only** while the WebView is on a
  `/self-order` (or `/self-order/*`) page — the same exact-or-subtree
  shape as the server's own auth exemption
  (`internal/auth/middleware.go`). Ordinary till use (sale screen, setup
  wizard, `/login`, `/settings`, anything else) never pins; the OS bar
  stays hidden via immersive mode, but Home/Recents/the notification
  shade all work normally, so a bare error page anywhere outside
  self-order can no longer brick the device.
- Tracks the pin as **intended state** (`kioskPinned`, set inside
  `engageKioskLock`/`releaseKioskLock` themselves) rather than deriving
  it from the current URL on every resume — see blocker 1 below for why
  that distinction is load-bearing.
- Removes `kioskUnlocked`, a field that became write-only once the
  `onPageFinished`/`onResume` logic no longer needed to ask "was this
  explicitly unlocked" (the mode-based gate makes that question moot).
- Updates `android/README.md` (design description + a rewritten manual
  verification checklist) and the shipped user manual
  (`web/help/en/display.md`'s Android paragraph, which previously
  described the old "pins itself into place automatically" behavior)
  to match. `make docs-shots` re-run for real; the two-file `sell.png`
  byte diff is the same non-deterministic rendering noise documented in
  the 2026-08-29 kiosk-lock-task review, unrelated to this change.
- Splits the acceptance criteria this change does NOT close — a
  physical-lock-button and remote unlock path for self-order, decoupled
  from the till's own web UI — into its own follow-up card, ut-docs#1513,
  since it needs new cross-repo design (a hardware decision, an
  ut-cloud-side remote channel) rather than a bug fix.

## Independent review (Opus, different model from the Sonnet author)

Spawned in an isolated worktree (`isolation: "worktree"`) since the fix
was still an uncommitted WIP snapshot at review time. Found **1
blocker**, fixed by the reviewer directly (verified independently
afterward, see below); **2 should-fix**, both fixed; **3 nice-to-have**,
reported, not fixed (see "Explicitly deferred" below).

### Blocker (fixed, independently re-verified)

1. **`onResume()` derived its pin decision from the current URL, which
   silently dropped a genuine self-order pin.** `self_order.html` puts a
   🔒 exit link to `/login` on the customer-facing screen — one tap.
   `onPageFinished` correctly leaves the lock state untouched on
   `/login`/`/settings` (only the PIN-gated `exitLockdown()` bridge call
   may release it there). But the first draft's `onResume` read a
   `onSelfOrderScreen` field computed from the *current path* — so the
   sequence pinned-on-`/self-order` → tap 🔒 → `/login` (still correctly
   pinned) → screen blanks (power button / display timeout) → wakes →
   `onResume` sees the URL is `/login`, not `/self-order` →
   **`releaseKioskLock()`** — unpinned the tablet with no PIN entered.
   Self-heals only if the kiosk idle-reset timer is enabled and fires;
   with it disabled, a customer is one screen-blink away from Home.

   Fix: replaced the URL-derived `onSelfOrderScreen` field with
   `kioskPinned`, set inside `engageKioskLock()`/`releaseKioskLock()`
   themselves (every path that changes the pin records it automatically).
   `onResume` now only **re-asserts** `kioskPinned` — it never derives a
   decision from the URL. Traced by hand afterward (this session, not
   just trusting the reviewer's own verification) through: cold launch;
   self-order-mode boot (server 303-redirects `/` → `/self-order`,
   confirmed against `internal/pages/init.go`'s
   `SetAnonymousRootRedirect`); self-order → 🔒 → `/login` → screen
   blink → still pinned; PIN success → `exitLockdown()` → unpinned;
   profile switch back to Register → unpinned sale screen;
   `launchPackageInstaller`'s defensive release. All consistent with the
   intended tri-mode behavior; `kioskPinned` has exactly two writers and
   one reader, no other write sites.

### Should-fix (fixed)

2. **`web/help/en/display.md` stated the opposite of the code.** The
   first draft's Android paragraph said leaving the self-order screen
   "for any other page" released the pin — but `/login` (the page the
   🔒 icon itself opens) is exactly the page that does NOT release it.
   Rewritten to say the sign-in screen stays pinned (through sleep/wake)
   until a manager PIN goes in, and that switching the device profile
   back to Register/Back office is what actually releases it once the
   till lands on an ordinary screen.
3. **Stale comments** (this repo's own recurring bug class — a comment
   describing behavior the code doesn't actually have): `
   launchPackageInstaller`'s claim that a carried-over pin there is
   "only possible if a self-order screen was never properly released"
   (it's designed, not anomalous — `managerFacing` deliberately
   preserves it); the field/`onResume` comment's claim that the WebView's
   first load is "always the till root `/`, never self-order" (false on
   a till already configured for self-order mode — the server
   303-redirects `/` straight to `/self-order`). Both corrected.
   `android/README.md` updated to match, and to name the deferred
   follow-up by its actual issue number (ut-docs#1513).

### Nice-to-have (reported, not fixed — accepted gaps)

4. The pin tracks the *current page*, not the *device profile* — a till
   switched into self-order mode via the settings.html inline-PIN
   elevation path (rather than the already-signed-in-manager path) can
   sit on `/settings`, self-order-configured, unpinned, until the next
   navigation. Self-healing (not a regression — everything was pinned
   before this change), worth a line on ut-docs#1513 rather than a
   change here.
5. An Activity recreate (a language change via
   `AppCompatDelegate.setApplicationLocales()`, or process death) resets
   `kioskPinned` to `false`; the WebView reload re-derives and re-pins on
   its next `onPageFinished`. Bounded, self-healing.
6. A `/self-order` page that itself fails to render leaves no in-app
   escape (server down mid-render → error page still at a `/self-order`
   URL → still pinned, no 🔒 to tap). This is precisely what ut-docs#1513
   exists to close (a route independent of the app's own web UI); stated
   explicitly in `android/README.md` rather than left implicit.

## Verified beyond automated tests

- `gofmt -l .` clean, `go build ./...` clean, full `go test ./...` clean
  — **zero `.go` files touched** by this diff (confirmed), so this is
  expected, not a weak signal.
- Every CI-blocking guard this diff could plausibly touch, run and green
  in this session (not just trusted from the reviewer's own run):
  `guard-page-http-error`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots` (after a real `make docs-shots` run — 96/96
  Playwright specs passed), `guard-help-topics`,
  `guard-android-status-address`, `guard-android-i18n`,
  `guard-kiosk-launch-flags`, `guard-kiosk-engine`, `guard-data-access`,
  `guard-plugin-menu-read`.
- `kioskUnlocked`'s removal verified complete (`grep` across the repo:
  zero remaining references) and its one real prior read-site
  (`launchPackageInstaller`'s old "stop onResume re-pinning" comment) is
  now covered structurally by `kioskPinned` rather than lost.
- No new user-facing strings were added (this is a native-logic + docs
  change, no new UI text), so no `T()`/locale key was owed —
  `guard-i18n`/`guard-android-i18n` confirm.
- No real client/shop name or secret-shaped literal anywhere in the diff.
- **NOT verified**: the Kotlin was never compiled — this environment has
  no Android SDK/NDK/emulator and no network path to install one, same
  constraint the original ut-docs#1254 review hit. Verified instead by
  close, line-by-line reading against known Android API semantics and
  the surrounding code's own existing patterns, by two independent
  passes (Sonnet author, Opus reviewer) plus a third hand-trace by this
  session before merge. Real-hardware verification on the TECLAST P50T
  test rig is a required follow-up — the manual checklist in
  `android/README.md` is written for whoever does it, including new
  steps specifically covering the blocker this review caught (screen
  blink while parked, unauthenticated, on the self-order 🔒 exit
  prompt).

## Safe-to-merge verdict

Safe to merge. The one blocker (a no-PIN escape out of the one mode that
must stay pinned) is fixed and independently re-traced by hand in this
session, not just taken on the reviewer's word. Both should-fix findings
are fixed. The three nice-to-have items are genuine, bounded, and either
self-healing or explicitly tracked on the follow-up card — not silently
dropped.

## Explicitly deferred (follow-up cards)

- **ut-docs#1513** — a physical-lock-button and remote unlock path for
  self-order, independent of the till's own web UI (per this ticket's
  own acceptance criteria, split out because it needs cross-repo
  hardware/business design decisions this cycle can't make). Nice-to-
  have 4 above (profile-switch-vs-page-tracking lag) is worth folding
  into that card's scoping.
- Real-hardware verification on the TECLAST P50T, per the ticket's own
  acceptance criteria — needs a local/interactive session with the
  physical device, not a cold cloud cycle. Manual checklist in
  `android/README.md`.
