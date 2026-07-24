# 2026-07-24 — Self-order kiosk till mode, Phase 2: shell (ADR-0020)

## Context
Farshid (2026-07-24): a self-order kiosk connected to the shop's till and
back-office is top priority — full design in
`ut-docs/adr/0020-self-order-kiosk-and-item-modifiers.md`,
`specs/011-self-order-kiosk/spec.md`. This is Phase 2: the shell — new
till mode, routing, and the security boundary a customer-facing kiosk
needs — deliberately just a placeholder landing page proving the plumbing
end-to-end. Real browse/search/customize/cart (Phase 3) and checkout
(Phase 4) are separate, later tasks.

## Design
**The core decision, found while building, not anticipated when the spec
was first written**: every route in this app requires a cashier PIN
session today (`docs/architecture/pos-auth.md`). A self-order kiosk used
by anonymous walk-up customers can't PIN-login. Rather than inventing
session-auto-creation machinery for an anonymous visitor, `/self-order`
(and its future `/api/self-order/` routes) were added to
`internal/auth/middleware.go`'s exempt-path allowlist — the same
precedent already established for the `/themes/`/`/plugin-icons/`
static-asset exemptions. Sales completed there will attribute to a fixed,
seeded "kiosk" user (migration 018, `role=cashier` — can never pass a
manager check) rather than a session, since there isn't one; nothing in
this Phase creates a sale yet, so that attribution isn't exercised until
Phase 4.

Also corrected from the original spec: `auth.idle_lock_minutes` is
fundamentally session-based (revokes a cashier session server-side) and
doesn't apply to an auth-exempt route with no session at all. Added a
separate `kiosk.idle_reset_seconds` setting instead, driving a plain
client-side reload timer — nothing to lose yet at the shell stage (no
cart exists until Phase 3).

Other pieces: `display.mode` gains `self_order` (settings UI + API,
manager-gated like the existing `backoffice` value); `/` redirects an
authenticated visitor to `/self-order` when that mode is set — unlike
`backoffice` mode (which only redirects managers, with cashiers falling
through to the sale screen), self-order mode redirects EVERY session,
since a kiosk-mode till isn't meant to show the cashier screen to anyone
by default.

## Independent review
Opus-model review, adversarial brief, explicitly weighted toward the new
auth-exemption boundary — the single highest-stakes part of this diff,
since an over-broad or bypassable exemption there is a security hole, not
just a correctness bug.

**Confirmed correct (reviewer verified independently, went beyond the
brief):**
- **Empirically tested path-traversal bypass attempts through the real
  middleware+mux pipeline**, not just read the code: `/self-order/../settings`
  → `exempt()` matches on the undecoded path, but `http.ServeMux` issues a
  307 to the *cleaned* `/settings` with no content, and the browser's
  follow-up request re-enters the middleware, isn't exempt, and gets
  redirected to `/login` — no content leak. `/self-order%2f..%2fsettings`
  → 404. Confirmed no bypass.
- The prefix check is correctly boundary-anchored (`== "/self-order"` or
  `HasPrefix "/self-order/"`) — `/self-order-not-really` is genuinely not
  exempt, verified against the actual test assertion, not just its name.
- Blast radius is exactly one route: grepped the whole repo, confirmed
  nothing else is registered under an exempt prefix that would
  unintentionally inherit it.
- The landing page leaks nothing beyond the shop name and static text —
  `d.CurrentState()` is called but only a plain int is read out of it.
- The seeded "kiosk" user can never pass a manager check (`role=cashier`)
  and, having no `pin_hash`, is correctly excluded from both the
  first-boot-admin path and the login-candidate list (traced the actual
  SQL `WHERE pin_hash IS NOT NULL` filter).
- Redirect logic, manager-gating on the mode setter, idle-reset bounds
  (0–600, manager-gated), `RuntimeState` default application, and the
  client-side timer's fail-safe behavior (missing/zero/garbage attribute →
  early return, no throw, no reload loop) were all traced and confirmed
  correct.
- New tests genuinely exercise the real `auth.Middleware`, not a bare mux
  that would bypass the thing under test.

**No findings requiring a fix.** Two minor observations, explicitly not
bugs: (1) the seeded `kiosk` user sorts before `system` in one `ORDER BY
id LIMIT 1` fallback used only by `UT_AUTH=off` dev tooling for sale
attribution — attribution-only, no auth path, harmless; (2) the idle-reset
API accepts values as low as 1 second even though the UI only offers
0/30/60/120/180 — a manager could self-configure an aggressive reload
loop, which is a configuration choice, not a defect.

## Verification
`go build ./...`, `go vet ./...`, `go test ./...` (full suite, zero
regressions), `bash scripts/ci/guard-data-access.sh`,
`bash scripts/ci/guard-i18n.sh` — all green, both by me and independently
by the reviewer. Live-verified against a real built binary: mode switch
via the settings API, `/` redirect to `/self-order`, a genuinely
anonymous `curl` (no cookie) to `/self-order` rendering 200, idle-reset
setting persisting and re-rendering, the settings page showing the new
mode option and idle-reset card. New tests:
`TestSelfOrderModeRedirectsHome`, `TestSelfOrderModeRedirectsEverySession`,
`TestDisplayModeSelfOrderRequiresManagerRole`,
`TestSelfOrderPage_ServesAnonymousRequest`, `TestKioskUserSeeded`, plus
extended `TestMiddleware` in `internal/auth/auth_test.go` covering the
new exempt paths and the negative boundary case.
