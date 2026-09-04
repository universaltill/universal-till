# Code review — Android update chip installs (ut-docs#1537)

- **Date:** 2026-09-04
- **Branch:** `fix/1537-android-chip-installs`
- **Card:** ut-docs#1537 (`pipeline`, `p1`, `source:user`, `complexity:medium`)
- **Author model:** Opus 5 · **Independent review:** Fable 5.1, fresh context,
  isolated git worktree

## What shipped

Follow-on to ut-docs#1534, which made the Android update control *reachable*
but still cost three taps and a manager PIN. The product owner, on the tablet:

> "it should update the app even when I click on the bottom of the screen on
> the update green button"

and, on the PIN flow he did find: *"it can get the pin and update, it doesn't
do anything."* Both true. The PIN path worked, but a correct PIN started a
silent 135 MB `DownloadManager` fetch with no UI feedback whatsoever — success
and total failure looked identical.

- `POST /api/update/android-install` now authorises from the **session**
  (`canPerform("plugin_management")`) when no PIN is supplied — the same gate
  the desktop chip has always used — **except in self-order mode**, where the
  PIN stays mandatory.
- The Android chip is a `<button>` with the desktop's two-click confirm, not a
  link.
- Every state says something: downloading, wrong PIN, lockout, network
  failure, no bridge.
- The PIN input renders only when the server already knows it will demand one.
- Five new i18n keys in en/tr/fa/ar + the de lang pack; manual rewritten in all
  four locales.

### Why the self-order carve-out is not optional

Installing calls `installUpdate()`, which releases the kiosk lock-task pin —
the capability `exit-to-os` guards. Since ut-docs#1508 the app only pins itself
in self-order mode, so on an ordinary till that PIN guarded a lock that was not
engaged. But entering self-order **never logs the till out** (ut-docs#1253), so
the kiosk browser can still carry a live manager cookie. Authorising from it
would give a customer standing at the machine a one-tap way out of the kiosk.
`display.mode` is server-side, so the server decides rather than trusting the
page to report whether it is pinned.

## What the independent review found

It ran build, vet, both packages, `-race`, ten guards, and re-verified all
three TDD claims by reverting each file and capturing the failures. It also did
something I had not asked precisely enough for and should have: it measured the
**blast radius of the test-fixture change** by reverting only that hunk and
running the whole `internal/pages` suite — exactly two tests changed outcome,
the two intended. That was the highest-risk part of the diff.

Its verdict on the central security question was more careful than my own
reasoning: the premise "the app only pins in self-order mode" is **not** what
the Kotlin does — `MainActivity.kt:850` pins on the URL path `/self-order*`,
which is reachable in any `display.mode`. So a manager can pin an ordinary till
by opening that page. The carve-out still holds, but for a reason the diff did
not state: `login.html` is a standalone document with no chip, `next=kiosk`
always demands a fresh PIN, and in self-order mode `/` redirects every session
to `/self-order` — so the chip is only ever reachable by someone who just
PIN-authenticated. Worth recording, because the next person to touch either
side needs it.

**Fixed:**

1. **The `hidden` PIN row was not hidden** (`settings.html`). `app.css`'s
   `label:has(> input…) { display:flex }` is an author rule and beats the UA
   `[hidden]` style at any specificity — a trap `app.css` documents three times
   for `.btn`/`.field-pair`/`.field-checks` and had no `label` guard for. The
   feature only appeared to work because an empty PIN still takes the session
   path. Added `label[hidden] { display: none }`.
2. **A 403 while already on `/settings` froze the chip** on "Downloading"
   forever: `location.href = '/settings#android-update'` is a fragment-only
   navigation there, so nothing reloads. Precisely the failure class this card
   exists to kill. Now re-enables and reveals the form in place.
3. **Failed open on a settings read error.** The comment said "cannot read the
   mode must mean assume pinned"; the code discarded the error, yielding
   `mode == ""` → not self-order → authorised.
4. **Audit row with a NULL actor.** With `UT_AUTH=off`, `canPerform` returns
   true with no user in context. Now uses `settingsActorID(r)`, and both paths
   record `via: session|pin` so the trail can tell them apart.
5. **Comments stating the opposite of the code** in `settings.html`.
6. **The manual told the operator to tap a disabled button** after the
   "install unknown apps" round trip. The chip now re-arms on
   `visibilitychange`.

Plus the review's NIT about a wasted round trip: the server already knows
whether a PIN will be needed, so `androidUpdateSessionAuthorizes` is now the
single owner of that decision, called by both the handler and the Settings
page, and fails closed on every uncertainty (no store, unreadable mode,
self-order, insufficient permission). New test pins that.

**Accepted:** the review's note that `data-latest` on the Android chip is
unused, and that `strings.TrimSpace(mode)` was inconsistent with `init.go`'s
untrimmed comparison — the trim is gone with the shared helper.

## Deliberate behaviour change

A signed-in manager can now restart a trading till in two taps with no PIN.
That is not new capability — the desktop chip has always worked this way, and
`plugin_management`'s grant set (admin/manager/super_admin) is exactly
`AuthorizeManager`'s role set — but it is a real widening of *convenience* on
Android and was flagged to the product owner as a settable preference if he
wants the PIN kept on the main till.

## Verdict

**Safe to merge.** No blocking findings. Six review findings fixed, nine tests,
`go test ./internal/...` green apart from the pre-existing darwin-only
`internal/server` failure (ut-docs#1535).
