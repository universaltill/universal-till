# Code review: PIN-gated exit link on the self-order kiosk

**Date:** 2026-08-01
**Scope:** `internal/pages/auth_page.go`, `internal/pages/self_order_mode_test.go`,
`internal/pages/self_order_shop_test.go`, `web/ui/pages/{login,self_order,self_order_shop}.html`,
`web/ui/partials/self_order_{modifier_picker,payment_picker,confirmation}.html`,
`web/public/app.css`, `web/locales/{ar,en,fa,tr}.json`
**Trigger:** ut-docs#208 — the self-order kiosk (anonymous, auth-exempt
routes) had no way back to till settings, a direct gap against this repo's
own offline-first "status/lock/exit must always be reachable" rule.

## What shipped

A discreet exit control reachable from every self-order kiosk screen
(start, browse, cart, and — after the fixes below — the checkout/
modifier/confirmation modals too), linking to the existing `/login` PIN
flow rather than any new auth mechanism:

- `web/ui/pages/self_order.html` / `self_order_shop.html`: a
  `.selforder-exit` link to `/login?next=kiosk`.
- `internal/pages/auth_page.go`: `sanitizeLoginNext`/`loginDestination`
  allow-list a `next=kiosk` param (never an arbitrary echoed redirect) so a
  successful login from the kiosk lands on `/settings` instead of `/`.
- `login.html`: carries `next` through the POST as a hidden field, shows a
  "back to kiosk" link and an idle-reset timer when `next=kiosk`.
- i18n: `selforder.exit` added to all four locales.

## Independent review (first pass) — real, load-bearing findings

Different-model subagent (Opus), briefed with the exact diff, the repo's
CLAUDE.md, and told explicitly to run things and try to break the TDD
claim, not rubber-stamp. It did all of that (ran `go build`/`vet`/tests/
both guard scripts — all passed; deleted the exit-link line from each
template and confirmed the new tests actually fail, then restored and
confirmed pass), and on top of the mechanical checks, wrote a throwaway
end-to-end probe against a `display.mode=self_order` till that caught a
real, shipped-would-have-been bug the mechanical checks could not see:

1. **BLOCKER** — `/` unconditionally redirects every session back to
   `/self-order` while `display.mode=self_order`
   (`internal/pages/index_page.go`, and this repo's own
   `TestSelfOrderModeRedirectsEverySession`). Since a bare `/login` success
   redirected to `/`, a manager who used the new exit link and entered a
   valid PIN was bounced straight back into the anonymous kiosk — the
   headline acceptance criterion ("valid PIN returns to the till surface")
   did not hold in the one mode this feature exists for. Reproduced
   empirically by the reviewer before I touched anything.
2. **MAJOR** — the original fixed-position `.selforder-exit` (top
   inline-start corner, `z-index: 10`) sat directly on top of the shop
   screen's "← Back" button.
3. **MAJOR** — `/login` had no way back to the kiosk and no idle timer;
   unlike every other kiosk screen, one stray tap left an unattended
   terminal parked on a PIN pad indefinitely.
4. **MAJOR** — checkout/modifier-picker/confirmation render inside a
   native `<dialog>` (`showModal()`), which makes everything *outside* it
   inert. The exit control living only in the page body was unreachable
   during exactly the "checkout" screen the ticket named.
5. **MAJOR (test coverage)** — both original tests asserted only that the
   markup contains `href="/login"`; nothing exercised the actual PIN
   round-trip, which is exactly why finding 1 shipped undetected.
6. Two accepted **minor/nit** findings also fixed: touch target below this
   repo's own 3rem/46px floor, and the Arabic string using "device
   settings" instead of "till settings" (fa/tr both say "till/register").
7. One **nit** (emoji not `aria-hidden`) fixed for completeness.

Full findings quoted in the review agent's own report (not duplicated
here in full) — nothing was disputed; every blocker/major/minor was real
and fixed, not argued away.

## Fixes applied

- `auth_page.go`: `next` param (allow-listed to the single literal
  `"kiosk"`, never an open redirect) threaded through `GET /login`,
  `POST /api/auth/login`, and `renderLogin`; success redirects to
  `/settings` when `next=kiosk`, `/` otherwise.
- `login.html`: hidden `next` field survives the POST; a "back to kiosk"
  link and a `kiosk.idle_reset_seconds`-driven auto-return script render
  only when `next=kiosk` (same client-side-only pattern the self-order
  screens themselves already use — no server session to revoke here
  either).
- `app.css`: `.selforder-exit` is now sized to the repo's own touch-target
  floor (3rem, matching `.btn`) and is an in-flow flex item everywhere
  except the plain start screen, where a fixed corner is safe (nothing
  else occupies that corner there). The shop screen's copy moved into the
  header's own flex row, after the search box — no more overlap with Back.
- Each of the three `#selforder-modal` partials
  (`self_order_modifier_picker.html`, `self_order_payment_picker.html`,
  `self_order_confirmation.html`) got its own copy of the exit link next
  to its existing cancel/done button, so it's reachable while the dialog
  holds the top layer.
- `ar.json`: "الخروج إلى إعدادات الصندوق" (till, not device).
- Emoji wrapped in `<span aria-hidden="true">`.
- New test `TestSelfOrderExit_PinLoginReachesTillSettingsNotKioskLoop`
  (`self_order_mode_test.go`) drives the actual round trip against a
  fully-migrated DB and a real `auth.Service`: sets `display.mode=self_order`,
  seeds a manager with a known PIN, asserts an invalid PIN sets no session
  cookie, and asserts a valid PIN redirects to `/settings` (not
  `/self-order`) with a session that resolves to that manager and that
  `/settings` actually renders for it. This is the test that would have
  caught finding 1; the original markup-only tests are kept as cheap
  smoke checks but are no longer the only proof.

## Verification (self, after fixes)

- `go build ./...`, `go vet ./...`: clean.
- `go test ./internal/pages/...` (all `TestSelfOrder*`, plus
  `TestFirstBootSetupThenLogin`/`TestUsersPagePermissions` to check nothing
  in the shared login path broke): all pass.
- `go test ./...`: clean except the standing, pre-existing
  `internal/issuereport` root-in-container flake (`TestSaveCleansUpDirectoryOnWriteFailure` —
  a read-only-directory test that can't fail under a root UID; confirmed
  unrelated by reproducing it identically on the pre-change tree via
  `git stash`).
- `bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-data-access.sh`:
  both green.
- Real running binary, not just `httptest`: completed first-boot setup,
  set `display.mode=self_order`, confirmed the shop page's exit link
  carries `next=kiosk`, then drove the actual HTTP round trip with
  `curl`+cookie jar — invalid PIN: 200, no session cookie; valid PIN: 303
  → `/settings` (not `/self-order`); `GET /settings` with that session:
  200. This is the exact scenario finding 1 broke, now confirmed fixed
  outside the test harness too. Test binary and its process killed and
  temp data dir removed afterward.

## Deferred / accepted risk

- The reviewer raised whether a manager PIN-authenticating via this exit
  link and then walking away leaves an authenticated session reachable on
  a public-facing terminal. This is a pre-existing property of `/login`'s
  12h session TTL and lack of an idle-lock-on-navigate-away, not something
  this diff introduces — and landing on `/settings` (which renders the
  normal nav with its session chip + Lock button, unlike the bare kiosk
  templates) is a strict improvement over the finding-1 bug's prior
  behavior (silently bounced back to the anonymous kiosk with a live
  cookie and zero visible lock affordance). Not blocking; noted here per
  the reviewer's own framing rather than silently dropped.

## Verdict

**Safe to merge** after the fixes above. The independent review's first
pass was not a rubber stamp — it found a real blocker that would have
shipped a feature failing its own headline acceptance criterion, plus
three further majors, all fixed and re-verified against both the test
suite and a real running instance of the app.
