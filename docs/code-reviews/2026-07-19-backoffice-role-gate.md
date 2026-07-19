# Code review — backoffice manager/admin role gate (2026-07-19)

Branch `feat/backoffice-role-gate`. Farshid's design call on the backoffice
architecture question: keep the single source of truth (primary/replica,
ADR-0011 — unchanged), make backoffice a **role gate, not a device lock** —
any till can open it for a manager/admin operator, rather than only the one
device flipped into `display.mode=backoffice`.

## What changed

`/backoffice` had **no auth gate at all** — any operator, even a cashier (or
an unauthenticated session, since the route wasn't in the auth middleware's
exempt list either way), could view it just by navigating there; the doc
comment even said "any till can visit it directly" without qualifying who.
Added the same `isManagerOrAuthOff` gate already used for manager-only
settings/reports (`settings_page.go`) — 403 for non-managers, transparent
pass for `UT_AUTH=off` dev/CI tooling.

This is the minimal concrete step that operationalizes "role of backoffice":
the existing `display.mode=backoffice` toggle still controls what a device's
home page redirects to by default (the "only backoffice" kiosk case), but
now ANY till's operator with the manager/admin role can also open
`/backoffice` directly regardless of that device's mode — single source of
truth (whichever device is primary/replica) stays completely unchanged.

## Tests

- `TestBackofficeModeRedirectsHome`: existing test now sets `UT_AUTH=off`
  (documented bypass) since it exercises the dashboard's rendering without a
  real session — the assertion this test cares about (mode toggle → home
  redirect → dashboard renders) is unaffected by the auth gate.
- `TestBackofficeRequiresManagerRole` (new): no session → 403; cashier → 403;
  manager → 200; admin → 200 — via `auth.WithUser`, the same pattern
  `auth_page_test.go` already uses.

Full suite + data-access guard + i18n guard green. `main.go`'s pre-existing
gofmt drift (unrelated to this change) is untouched.

## Review (independent, sonnet) — findings and dispositions

**1. Self-lockout regression (confirmed, high) — fixed.** `POST
/api/settings/display-mode` had no role gate, so any operator (a cashier)
could flip their OWN till into `display.mode=backoffice`; `index_page.go`'s
`/` handler unconditionally redirected to `/backoffice` for whoever hit it
next — after this change, that's now a bare 403 dead-end instead of the
previously-harmless dashboard. Fixed two ways: (a) the display-mode setter
itself now requires manager/admin (`settings_page.go`); (b) more importantly,
the home-page redirect only fires `mode == "backoffice" && isManagerOrAuthOff
(r)` (`index_page.go`) — a non-manager session on an already backoffice-mode
till now falls through to the normal sale screen instead of hitting the
gate at all. (b) is the one that actually matters: it holds even for modes
set before this change shipped. Covered by
`TestDisplayModeBackofficeRequiresManagerRole` and
`TestBackofficeModeFallsThroughForNonManagerSession`.
2. Middleware coverage, `UT_AUTH=off` risk profile, test-harness safety —
all confirmed correct, no changes needed.

## Remaining (backlog, not this change)

Setup-wizard prompt (only till / till+backoffice / only backoffice-kiosk)
and a dedicated Settings toggle distinct from `display.mode` — tracked in
`docs/QUEUE.md`, needs its own scoped work.
