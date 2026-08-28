# Code review — self-order kiosk exit always requires a fresh PIN (ut-docs#1253)

- **Date:** 2026-08-28
- **Branch:** `fix/1253-self-order-exit-pin-bypass`
- **Reviewer:** independent reviewer (different model from the implementer)
- **Verdict: SAFE TO MERGE AS-IS.** One real, non-blocking root-cause gap
  identified and filed as a follow-up ticket (ut-docs#1259); everything else
  is a nit or a documented, accepted trade-off.

## What shipped

`GET /login`'s "already authenticated? skip the PIN form" shortcut
(`internal/pages/auth_page.go`) applied identically whether the request came
from a plain register re-visiting `/login`, or from the self-order kiosk's
public-facing exit link (`/login?next=kiosk`, ut-docs#208). Putting a till
into self-order mode never logs it out — `display.mode` only changes what
`/` redirects to — so any customer who reached that URL while a prior staff
session was still alive in that browser walked straight into Settings with
no PIN prompt at all. Reported live against a real device (ut-docs#1253).

Fix: the shortcut no longer applies when `next == "kiosk"`; that path always
renders the PIN form now, regardless of any existing session cookie. Every
other caller (bare `/login`) is unaffected.

Two new tests in `internal/pages/self_order_mode_test.go`:
- `TestSelfOrderExit_ExistingSessionCookieStillRequiresPIN` — reproduces the
  bug against the unfixed code, confirms the fix renders the PIN form
  instead, and confirms a correctly re-entered PIN still reaches `/settings`.
- `TestLogin_ExistingSessionStillSkipsFormWhenNotKioskExit` — regression
  guard for the convenience path deliberately preserved: bare `/login` with
  a live session still skips straight to `/`.

## Verification performed

| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` / `gofmt -l .` | pass / pass / empty |
| `go test ./internal/pages/...` (full package) | pass |
| `go test ./...` (whole repo) | pass |
| `-race` scoped to the new/related tests | pass |
| `bash scripts/ci/guard-data-access.sh` | pass — no SQL added outside tests |
| `bash scripts/ci/guard-i18n.sh` | pass — no new user-facing strings |
| `bash scripts/ci/guard-kiosk-engine.sh` | pass |

### Independent re-verification of the TDD claim

Reverted only `internal/pages/auth_page.go` to its pre-fix content (test
file left in place), confirmed the package still compiles, and re-ran the
new test:

```
--- FAIL: TestSelfOrderExit_ExistingSessionCookieStillRequiresPIN
    self_order_mode_test.go:413: existing session cookie let /login?next=kiosk
    skip straight to "/settings" with no PIN prompt — must render the PIN
    form instead
```

Genuinely red for the reported reason, not an incidental failure. Restoring
the fix returns it to green.

### Empirical blast-radius probe

A throwaway probe test (run, then deleted; working tree confirmed clean
afterward) hit every `next` spelling — case variants, whitespace, duplicate
params, path-injection attempts (`next=/settings`, `next=%2Fsettings`) —
plus `/settings` and `/` directly, against a live manager session on a
`display.mode=self_order` till. **No spelling other than the exact literal
`kiosk` bypasses the gate into an admin page**; every miss sanitizes to
`next=""`, whose destination is `/`, which on a self-order till bounces back
to `/self-order` — harmless by construction. Grepped every kiosk template
(`self_order.html`, `self_order_shop.html`, and the three self-order
partials) and confirmed `/login?next=kiosk` is the *only* link out of the
kiosk UI — the fix closes the entire tap-reachable surface.

## Findings

### Non-blocking — the stale session itself is never invalidated (root cause, not this ticket's scope)

`POST /api/settings/display-mode` writes the setting only; it never revokes
the acting session or clears the requesting browser's cookie. So the
browser keeps a fully valid session after the till switches into self-order
mode, and `GET /settings` with it still returns 200 directly (no tap-through
kiosk UI involved at all) — worse, a stale **manager/admin** session would
mutate settings with no PIN, since `checkOrElevate` returns `allowed`
outright when `canPerform` already passes.

This diff closes every path reachable by *tapping through the kiosk UI*
(confirmed by the template grep above) — that fully satisfies the ticket's
acceptance criteria. It does not close the case of a non-kiosk device (a
desktop till put into self-order mode with a visible URL bar, browser
address bar reachable) where a customer could type `/settings` directly.
Filed as **ut-docs#1259** (revoke/clear the session when a till enters
self-order mode) rather than expanding this PR's scope.

### Non-blocking — kiosk exit accepts any operator PIN, not specifically a manager/admin PIN

Pre-existing ut-docs#208 design, unchanged by this fix: any valid PIN
(including a plain cashier's) unlocks the kiosk exit into `/settings`, which
itself renders for any session — individual mutating actions are separately
gated by `canPerform`/elevation. The ticket's title says "no **admin** PIN
prompt", which this fix satisfies literally (a PIN prompt now always
appears), but whether the kiosk exit specifically should require
`AuthorizeManager` rather than any operator PIN is a product question
that predates this fix. Raised on the issue for a product-owner call rather
than decided unilaterally here.

### Nit — first-boot ordering delta, unexercised but not a new hole

`next=kiosk` now falls through to the `NeedsFirstBoot` check ahead of the
old shortcut. `NeedsFirstBoot` is only true when no user has a PIN set yet,
in which case the till is already unprotected and `/setup` is already
auth-exempt — not a new exposure, just an untested reordering. Not worth a
test on its own; noted for the record.

### Docs

`web/help/en/self-order.md` didn't document the lock/exit affordance before
this change either, so nothing went stale (`guard-help-topics.sh` passes
either way) — not a blocker for this fix.

## Checked and found clean

- No raw SQL outside `internal/data`/tests; no i18n/RTL surface touched
  (Go-only routing change).
- Offline-first "status/lock/exit must always be reachable" preserved: the
  PIN form still renders the idle-reset timer and the exit-to-OS panel; a
  legitimate admin with a correct PIN is never stranded (existing
  `TestSelfOrderExit_PinLoginReachesTillSettingsNotKioskLoop` still green).
- Only callers of `sanitizeLoginNext`/`loginDestination` are the two
  handlers in `auth_page.go`; `POST /api/auth/login` (the actual PIN check)
  is untouched.
- The one accepted UX cost: a manager already signed in who deliberately
  browses to `/self-order` must now re-enter their PIN to get back out —
  the intended trade for closing the hole.
